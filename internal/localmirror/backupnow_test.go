// SPDX-License-Identifier: MIT

package localmirror

import (
	"context"
	"io/fs"
	"sync"
	"testing"
	"time"
)

// gateBackend makes the destination walk — the first thing a full pass does
// that costs a round trip — observable and stoppable, so a test can hold one
// pass open and prove nothing else starts beside it.
type gateBackend struct {
	Backend
	mu       sync.Mutex
	entered  int  // full passes that have reached the destination walk
	inFlight int  // ...and how many are inside it at once
	overlap  bool // ...ever more than one
	first    chan struct{}
	release  chan struct{}
}

func newGateBackend(b Backend) *gateBackend {
	return &gateBackend{Backend: b, first: make(chan struct{}), release: make(chan struct{})}
}

// mirrorRoot is the destination tree a full pass enumerates. Version pruning
// walks its own directory on this same backend, so the gate has to know which
// walk is the pass.
const mirrorRoot = "mach/proj"

func (g *gateBackend) WalkDir(root string, fn fs.WalkDirFunc) error {
	if root != mirrorRoot {
		return g.Backend.WalkDir(root, fn)
	}
	g.mu.Lock()
	g.entered++
	g.inFlight++
	if g.inFlight > 1 {
		g.overlap = true
	}
	if g.entered == 1 {
		close(g.first)
	}
	g.mu.Unlock()
	<-g.release
	defer func() {
		g.mu.Lock()
		g.inFlight--
		g.mu.Unlock()
	}()
	return g.Backend.WalkDir(root, fn)
}

func (g *gateBackend) counts() (entered int, overlap bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.entered, g.overlap
}

// THE GUARANTEE. "Back up now" can never run a pass beside one that is already
// running.
//
// Two reconciles of the same folder onto the same destination at once would
// each be deciding what the other is halfway through writing: one versions away
// a file the other is still copying, and the totals on the dashboard belong to
// neither. The whole reason sync() has exactly one caller — the loop in Run —
// is that it makes overlap impossible, and a button that called it directly
// would have quietly undone that. So the button pokes the loop and the loop
// decides when.
func TestBackUpNowNeverRunsAPassBesideOneAlreadyRunning(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, "a.txt", "alpha")
	// A destination that already holds a copy, so every pass really does
	// enumerate it — a first backup skips the walk entirely, there being nothing
	// there to list.
	write(t, dst, mirrorRoot+"/a.txt", "alpha")
	g := newGateBackend(NewLocalFS(dst))
	if err := WriteMarker(g, "uuid-1", "mach"); err != nil {
		t.Fatal(err)
	}
	e := New(Options{
		FolderID: "f1", TargetName: "dest", SourcePath: src, Backend: g,
		MachineName: "mach", Label: "proj", UUID: "uuid-1", MaxAgeDays: 30,
		// Long enough that neither backstop can fire during the test and be
		// mistaken for the manual kick.
		OfflinePoll: time.Hour, Log: quietLog(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx) }()

	// The initial pass is now held inside the destination walk.
	select {
	case <-g.first:
	case <-time.After(5 * time.Second):
		t.Fatal("the engine never started its first pass")
	}

	for i := 0; i < 20; i++ {
		e.BackUpNow() // and it must not block, whatever the loop is doing
	}
	// Give a second pass every chance to start, if one were going to.
	time.Sleep(100 * time.Millisecond)
	if entered, overlap := g.counts(); entered != 1 || overlap {
		t.Fatalf("a pass started while one was in flight: %d passes, overlap=%v", entered, overlap)
	}

	// Released, the queued request is served — one pass, not twenty.
	close(g.release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		entered, overlap := g.counts()
		if overlap {
			t.Fatal("two passes ran at once once the first was released")
		}
		if entered >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the manual request was dropped: no pass ever ran for it")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done
}

// A manual request is a request for a REAL check, and must not be served by the
// fast path.
//
// e.kick holds one item, so a manual send lands on top of whatever the watcher
// queued — and a two-file batch from the watcher is exactly what the fast path
// is for. Without the flag beside the channel, pressing "Back up now" during a
// save would have copied the two files fsnotify happened to see and called that
// a backup, which is the one thing the button must not do.
func TestBackUpNowTakesTheFullPassEvenWhenTheWatcherQueuedASmallBatch(t *testing.T) {
	e, _, _, _ := fastEngine(t) // one completed pass: the fast path is legal here

	watched := []string{"a.txt"}
	if e.fullPassWanted(watched) {
		t.Fatal("a small watcher batch should take the fast path; the test is not testing what it thinks")
	}

	e.BackUpNow()
	if !e.fullPassWanted(watched) {
		t.Error("a manual request was served by the fast path — it would copy only the files " +
			"the watcher happened to see and report that as a backup")
	}
	// One press buys one full pass; it does not switch the fast path off.
	if e.fullPassWanted(watched) {
		t.Error("the manual flag was not cleared, so every later save now costs a full pass")
	}
}

// The request survives a full channel.
//
// Dropping the send is correct — a kick is already queued — but the REQUEST must
// not be dropped with it, or the button would silently do nothing whenever the
// watcher had just fired.
func TestBackUpNowIsStillHonouredWhenAKickIsAlreadyQueued(t *testing.T) {
	e, _, _, _ := fastEngine(t)

	e.kick <- []string{"a.txt"} // the watcher got there first; the channel is full
	e.BackUpNow()               // must not block, and must not be lost

	select {
	case queued := <-e.kick:
		if !e.fullPassWanted(queued) {
			t.Error("the kick the loop will actually read was left as a fast path, " +
				"so the manual request was swallowed by a full channel")
		}
	default:
		t.Fatal("nothing is queued at all")
	}
}
