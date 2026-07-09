package chat

import (
	"sync"
	"testing"
)

// TestSaveFull_UsesSnapshottedID pins the round-11 fix: SaveFull derived the
// filename from a lock-free session.ID re-read AFTER the locked GetState()
// snapshot. Concurrent SetID (via /save's temporary rename-for-export) both
// raced that read AND could persist the state under a filename that diverged
// from the ID serialized inside the state. Post-fix the filename comes from the
// GetState() snapshot's state.ID. Run under -race to catch a regression.
func TestSaveFull_UsesSnapshottedID(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	m, err := NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}

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
				prev := s.SetID("exported-name")
				s.SetID(prev)
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			if err := m.SaveFull(s); err != nil {
				t.Errorf("SaveFull: %v", err)
				break
			}
		}
		close(stop)
	}()

	wg.Wait()
}
