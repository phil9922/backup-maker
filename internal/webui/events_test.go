// SPDX-License-Identifier: MIT

package webui

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
)

// eventTiming runs the stream loop at test speed; whichever of the two a test
// isn't exercising gets pushed out of the way, so "quiet" means quiet.
func eventTiming(t *testing.T, interval, heartbeat time.Duration) {
	t.Helper()
	oldInterval, oldBeat := eventInterval, eventHeartbeat
	eventInterval, eventHeartbeat = interval, heartbeat
	t.Cleanup(func() { eventInterval, eventHeartbeat = oldInterval, oldBeat })
}

// eventStream connects to handleEvents over a real connection (a recorder would
// buffer, which is exactly what this handler must not do) and reports every
// line the handler writes — data payloads unwrapped, keep-alive comments as
// they are — plus a channel closed when the handler returns.
func eventStream(t *testing.T, s *Server) (*http.Response, <-chan string, <-chan struct{}, context.CancelFunc) {
	t.Helper()
	handlerDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		s.handleEvents(w, r)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = resp.Body.Close()
		select {
		case <-handlerDone:
			srv.Close()
		case <-time.After(2 * time.Second):
			// Close waits for handlers, so a stream that ignored the dead
			// client would hang the whole test binary. Say what happened.
			t.Error("the event handler was still running after its client went away")
			srv.CloseClientConnections()
		}
	})

	events := make(chan string, 16)
	go func() {
		defer close(events)
		rd := bufio.NewReader(resp.Body)
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if payload, ok := strings.CutPrefix(line, "data: "); ok {
				events <- payload
			} else if strings.HasPrefix(line, ":") {
				events <- line
			}
		}
	}()
	return resp, events, handlerDone, cancel
}

// stubServer is the smallest Server that can stream: a snapshot source and the
// shutdown channel New would have made.
func stubServer(status func() any) *Server {
	return &Server{status: status, done: make(chan struct{})}
}

func nextEvent(t *testing.T, events <-chan string) string {
	t.Helper()
	select {
	case ev, ok := <-events:
		if !ok {
			t.Fatal("the stream closed instead of sending a snapshot")
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no snapshot arrived")
		return ""
	}
}

// A dashboard opening the stream must paint at once, and the response has to
// declare itself as an unbuffered event stream or intermediaries will hold it.
func TestEventStreamAnnouncesItselfAndSendsFirstSnapshotImmediately(t *testing.T) {
	eventTiming(t, 5*time.Millisecond, time.Hour)
	resp, events, _, _ := eventStream(t, stubServer(func() any {
		return map[string]any{"machine_name": "workstation"}
	}))

	for header, want := range map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-cache",
		"X-Accel-Buffering": "no",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if ev := nextEvent(t, events); !strings.Contains(ev, `"machine_name":"workstation"`) {
		t.Errorf("first event was %q; it must carry the same snapshot /api/status returns", ev)
	}
}

// The entire point of the stream: a machine with nothing happening must produce
// no data events at all, however long the dashboard stays open.
func TestEventStreamStaysSilentWhileNothingChanges(t *testing.T) {
	eventTiming(t, 5*time.Millisecond, time.Hour)
	var mu sync.Mutex
	rows := []any{}
	// Built fresh under the lock each call: the handler marshals what it is
	// handed, so it must not share anything the test then writes to.
	_, events, _, _ := eventStream(t, stubServer(func() any {
		mu.Lock()
		defer mu.Unlock()
		return map[string]any{"machine_name": "workstation", "rows": rows}
	}))
	nextEvent(t, events) // the connect snapshot

	select {
	case ev := <-events:
		t.Fatalf("an idle stream sent %q; it should have written nothing at all", ev)
	case <-time.After(200 * time.Millisecond): // ~40 ticks at test speed
	}

	// ...and it must still notice when something does move.
	mu.Lock()
	rows = []any{map[string]any{"state": "syncing"}}
	mu.Unlock()
	if ev := nextEvent(t, events); !strings.Contains(ev, "syncing") {
		t.Errorf("after a change the stream sent %q", ev)
	}
}

// Silence is the normal state here, so the connection needs a heartbeat or a
// browser (or anything in between) will eventually decide it is dead.
func TestEventStreamPingsAnOtherwiseSilentClient(t *testing.T) {
	eventTiming(t, time.Hour, 5*time.Millisecond)
	_, events, _, _ := eventStream(t, stubServer(func() any {
		return map[string]any{"machine_name": "workstation"}
	}))
	nextEvent(t, events) // the connect snapshot

	// A comment, not an event: the client must not mistake it for a snapshot.
	if ev := nextEvent(t, events); ev != ": ping" {
		t.Errorf("keep-alive was %q, want a %q comment", ev, ": ping")
	}
}

// A closed tab must not leave a ticker running for the rest of the daemon's
// life.
func TestEventStreamEndsWhenTheClientGoesAway(t *testing.T) {
	eventTiming(t, 5*time.Millisecond, time.Hour)
	_, events, handlerDone, cancel := eventStream(t, stubServer(func() any {
		return map[string]any{"machine_name": "workstation"}
	}))
	nextEvent(t, events)

	cancel()
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler was still running after the client disconnected")
	}
}

// A stream that outlives Shutdown would hold the daemon's exit open until the
// shutdown timeout expired, every single time.
func TestEventStreamEndsWhenTheServerShutsDown(t *testing.T) {
	eventTiming(t, 5*time.Millisecond, time.Hour)
	s := stubServer(func() any { return map[string]any{"machine_name": "workstation"} })
	_, events, handlerDone, _ := eventStream(t, s)
	nextEvent(t, events)

	close(s.done)
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler ignored shutdown; Shutdown would block on it")
	}
}

// EventSource can't set an Authorization header, so the session cookie is the
// only way the dashboard can authenticate the stream. If this breaks, the
// dashboard silently falls back to polling forever.
func TestSessionCookieAuthorizesTheEventStream(t *testing.T) {
	s := &Server{state: &config.State{IPCToken: "s3cret"}}
	var reached bool
	h := s.requireToken(func(w http.ResponseWriter, r *http.Request) { reached = true })

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "s3cret"})
	rec := httptest.NewRecorder()
	h(rec, req)
	if !reached {
		t.Fatalf("the cookie was refused with %d; EventSource has no other way in", rec.Code)
	}

	reached = false
	req = httptest.NewRequest(http.MethodGet, "/api/events", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "wrong"})
	rec = httptest.NewRecorder()
	h(rec, req)
	if reached || rec.Code != http.StatusUnauthorized {
		t.Errorf("a bad cookie got %d (reached=%v), want 401", rec.Code, reached)
	}
}
