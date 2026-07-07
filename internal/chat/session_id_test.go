package chat

import (
	"sync"
	"testing"
)

// TestSession_SetIDRoundTrip pins the basic contract: GetID/SetID and
// GetState must agree, and SetID returns the PREVIOUS id (SaveCommand uses
// this to restore the original id after a temporary rename-for-export).
func TestSession_SetIDRoundTrip(t *testing.T) {
	s := NewSession()
	s.ID = "original"

	prev := s.SetID("renamed")
	if prev != "original" {
		t.Fatalf("SetID returned %q, want the previous id %q", prev, "original")
	}
	if got := s.GetID(); got != "renamed" {
		t.Fatalf("GetID = %q, want %q", got, "renamed")
	}
	if st := s.GetState(); st.ID != "renamed" {
		t.Fatalf("GetState.ID = %q, want %q", st.ID, "renamed")
	}

	s.SetID(prev)
	if got := s.GetID(); got != "original" {
		t.Fatalf("GetID after restore = %q, want %q", got, "original")
	}
}

// TestSession_SetIDConcurrentWithGetState (round 6) pins the fix for a data
// race: SaveCommand.Execute (commands/builtin.go) used to write session.ID
// directly at 3 sites (temporary rename-for-export, restore-on-error,
// restore-on-success) while GetState() reads s.ID under s.mu.RLock() on the
// async autosave goroutine — concretely reachable via a message queuing an
// autosave immediately followed by /save. Run under -race to catch a
// regression back to a direct field write.
func TestSession_SetIDConcurrentWithGetState(t *testing.T) {
	s := NewSession()
	s.ID = "session-1"
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				prev := s.SetID("mysavedname")
				s.SetID(prev)
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			_ = s.GetState()
			_ = s.GetID()
		}
		close(stop)
	}()

	wg.Wait()
}
