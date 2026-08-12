// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
	"sync"
	"testing"
)

// THE GUARANTEE: two writers changing different fields of state.json both keep
// their change. Every field in that file has exactly one owner, and the file is
// written by a dozen of them — the daemon's 30-second flush, the alert history,
// every setup command — so a write that carries a stale copy of everything it
// does not own is a write that undoes somebody else's.
//
// Twenty goroutines, each owning one destination's recorded UUID and share
// password, is the shape of the real failure with the timing removed: without
// the read-modify-write being serialised, whichever one saved last wins and the
// other nineteen are simply gone.
func TestConcurrentUpdatesEachKeepTheFieldTheyOwn(t *testing.T) {
	pointConfigDirInto(t, t.TempDir())

	const writers = 20
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("dest-%d", i)
			_, err := UpdateState(func(s *State) error {
				if s.DriveTargetUUIDs == nil {
					s.DriveTargetUUIDs = map[string]string{}
				}
				if s.ShareCredentials == nil {
					s.ShareCredentials = map[string]string{}
				}
				s.DriveTargetUUIDs[name] = "uuid-" + name
				s.ShareCredentials[name] = "password-" + name
				return nil
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("updating state: %v", err)
		}
	}

	after, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	for i := range writers {
		name := fmt.Sprintf("dest-%d", i)
		if after.DriveTargetUUIDs[name] != "uuid-"+name {
			t.Errorf("%s has no recorded UUID: the daemon would say to re-add it", name)
		}
		if after.ShareCredentials[name] != "password-"+name {
			t.Errorf("%s lost its share password: that destination stops backing up", name)
		}
	}
}

// A mutate that refuses leaves the file exactly as it was. The refusals matter:
// RenameTarget declines while a password is stranded in a locked keyring, and
// `keychain disable` declines when it cannot read the secrets back — in both
// cases the message ends "nothing has been changed", and this is what makes that
// sentence true.
func TestARefusedUpdateWritesNothing(t *testing.T) {
	pointConfigDirInto(t, t.TempDir())
	if _, err := UpdateState(func(s *State) error {
		s.WebhookURL = "https://example.invalid/hook"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	refusal := fmt.Errorf("not while the keyring is locked")
	if _, err := UpdateState(func(s *State) error {
		s.WebhookURL = "https://example.invalid/clobbered"
		return refusal
	}); err != refusal {
		t.Fatalf("the refusal was not passed back: %v", err)
	}

	after, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if after.WebhookURL != "https://example.invalid/hook" {
		t.Errorf("a refused update still wrote: webhook is now %q", after.WebhookURL)
	}
}

// ErrStateUnchanged is "there turned out to be nothing to do", not a failure —
// and it must not reach the caller as one, because half the call sites report
// whatever comes back.
func TestNothingToDoIsNotAnError(t *testing.T) {
	pointConfigDirInto(t, t.TempDir())
	s, err := UpdateState(func(s *State) error {
		s.AdvisorSeen = true
		return ErrStateUnchanged
	})
	if err != nil {
		t.Fatalf("a deliberate no-op was reported as a failure: %v", err)
	}
	if s == nil {
		t.Fatal("no state came back")
	}
	after, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if after.AdvisorSeen {
		t.Error("the file was written despite the mutate saying there was nothing to write")
	}
}
