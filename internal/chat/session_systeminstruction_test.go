package chat

import (
	"sync"
	"testing"
)

func TestSession_SystemInstructionRoundTrip(t *testing.T) {
	s := NewSession()
	if got := s.GetSystemInstruction(); got != "" {
		t.Fatalf("fresh session should have empty system instruction, got %q", got)
	}
	s.SetSystemInstruction("be concise")
	if got := s.GetSystemInstruction(); got != "be concise" {
		t.Fatalf("GetSystemInstruction = %q, want %q", got, "be concise")
	}
	// GetState must reflect the current value.
	if st := s.GetState(); st.SystemInstruction != "be concise" {
		t.Fatalf("GetState.SystemInstruction = %q, want %q", st.SystemInstruction, "be concise")
	}
}

// The system instruction is read by GetState() on the async save goroutine while
// the app goroutine updates it via SetSystemInstruction — both must go through
// the lock. Run under -race to catch a regression back to a direct field write.
func TestSession_SystemInstructionConcurrentSetAndGetState(t *testing.T) {
	s := NewSession()
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				s.SetSystemInstruction("prompt")
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			_ = s.GetState()
			_ = s.GetSystemInstruction()
		}
		close(stop)
	}()

	wg.Wait()
}
