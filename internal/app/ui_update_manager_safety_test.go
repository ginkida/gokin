package app

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestUIUpdateManager_StartStopRestartRestoresBroadcasting(t *testing.T) {
	program, model := newCapturingProgram(t)
	manager := NewUIUpdateManager(program, nil)
	t.Cleanup(manager.Stop)

	manager.BroadcastTaskStart("before", "ignored", "test")
	time.Sleep(20 * time.Millisecond)
	started, _, _ := capturedTaskEventCounts(model)
	if started != 0 || manager.IsRunning() {
		t.Fatalf("manager broadcast before Start: started=%d running=%v", started, manager.IsRunning())
	}

	manager.Start()
	manager.BroadcastTaskStart("first", "work", "test")
	waitForCapturedTaskEvents(t, model, 1, 0, 0)
	manager.mu.RLock()
	firstBroadcaster := manager.eventBroadcaster
	manager.mu.RUnlock()
	manager.Stop()
	if manager.IsRunning() || !firstBroadcaster.IsStopped() {
		t.Fatalf("manager did not stop: running=%v broadcasterStopped=%v", manager.IsRunning(), firstBroadcaster.IsStopped())
	}
	manager.BroadcastTaskStart("stopped", "ignored", "test")
	time.Sleep(20 * time.Millisecond)
	started, _, _ = capturedTaskEventCounts(model)
	if started != 1 {
		t.Fatalf("stopped manager accepted event; started=%d", started)
	}

	manager.Start()
	manager.mu.RLock()
	secondBroadcaster := manager.eventBroadcaster
	manager.mu.RUnlock()
	if secondBroadcaster == firstBroadcaster || secondBroadcaster.IsStopped() {
		t.Fatal("Start did not replace stopped broadcaster")
	}
	manager.BroadcastTaskStart("second", "work", "test")
	waitForCapturedTaskEvents(t, model, 2, 0, 0)
}

func TestUIUpdateManager_DisabledStateSurvivesRestart(t *testing.T) {
	program, model := newCapturingProgram(t)
	manager := NewUIUpdateManager(program, nil)
	t.Cleanup(manager.Stop)
	manager.Disable()
	manager.Start()
	manager.BroadcastTaskStart("disabled-first", "ignored", "test")
	time.Sleep(20 * time.Millisecond)
	started, _, _ := capturedTaskEventCounts(model)
	if started != 0 {
		t.Fatalf("disabled manager broadcast %d event(s)", started)
	}
	manager.Stop()
	manager.Start()
	manager.BroadcastTaskStart("disabled-second", "ignored", "test")
	time.Sleep(20 * time.Millisecond)
	started, _, _ = capturedTaskEventCounts(model)
	if started != 0 {
		t.Fatalf("disabled state was lost across restart; started=%d", started)
	}
	manager.Enable()
	manager.BroadcastTaskStart("enabled", "work", "test")
	waitForCapturedTaskEvents(t, model, 1, 0, 0)
}

func TestUIUpdateManager_ConcurrentStartStopLeavesRestartableManager(t *testing.T) {
	program, model := newCapturingProgram(t)
	manager := NewUIUpdateManager(program, nil)
	t.Cleanup(manager.Stop)
	manager.Start()

	const iterations = 100
	for i := 0; i < iterations; i++ {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			manager.Stop()
		}()
		go func() {
			defer wg.Done()
			manager.Start()
		}()
		wg.Wait()
	}
	// Whichever operation won the last pair, an explicit Start must always
	// establish a fresh live broadcaster.
	manager.Start()
	if !manager.IsRunning() {
		t.Fatal("manager could not restart after concurrent lifecycle calls")
	}
	manager.BroadcastTaskStart("after-race", fmt.Sprintf("after %d cycles", iterations), "test")
	waitForCapturedTaskEvents(t, model, 1, 0, 0)
}
