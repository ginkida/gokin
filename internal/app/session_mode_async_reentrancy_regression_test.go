package app

import (
	"sync"
	"testing"
	"time"
)

// Shift+Tab and the matching palette action invoke CycleSessionModeAsync from
// Bubble Tea's Update. Each callback starts an independent goroutine, so rapid
// accepted keypresses can enter CycleSessionMode concurrently. Every accepted
// tap still owns one transition: Normal -> Plan -> YOLO -> Normal.
func TestConcurrentSessionModeCyclesDoNotCollapseAcceptedKeypresses(t *testing.T) {
	app := newSessionModeTestApp(false, true) // Normal

	// Queue every caller behind a.mu long enough for them to become mutex
	// waiters. Once released, this deterministically exercises the split
	// read-current/apply-next critical sections in CycleSessionMode rather than
	// depending on ordinary scheduler timing.
	const taps = 15 // five complete Normal -> Plan -> YOLO -> Normal cycles
	app.mu.Lock()
	started := make(chan struct{}, taps)
	var wg sync.WaitGroup
	for range taps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started <- struct{}{}
			app.CycleSessionMode()
		}()
	}
	for range taps {
		<-started
	}
	// sync.Mutex switches queued waiters to starvation/FIFO hand-off after
	// roughly 1ms. Holding it longer makes all accepted callbacks contend at
	// the same pre-transition snapshot, matching a burst of UI callbacks.
	time.Sleep(10 * time.Millisecond)
	app.mu.Unlock()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent session-mode callbacks did not finish")
	}

	app.mu.Lock()
	got := app.currentSessionMode()
	app.mu.Unlock()
	if got != SessionModeNormal {
		t.Fatalf("%d accepted session-mode keypresses ended in %s, want normal; concurrent cycles collapsed user input", taps, got.String())
	}
}
