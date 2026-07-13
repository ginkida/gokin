package app

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"gokin/internal/ui"
)

func capturedTaskEventCounts(model *msgCapturingModel) (started, completed, progress int) {
	model.mu.Lock()
	defer model.mu.Unlock()
	for _, msg := range model.msgs {
		switch msg.(type) {
		case ui.TaskStartedEvent:
			started++
		case ui.TaskCompletedEvent:
			completed++
		case ui.TaskProgressEvent:
			progress++
		}
	}
	return
}

func waitForCapturedTaskEvents(t *testing.T, model *msgCapturingModel, wantStarted, wantCompleted, wantProgress int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		started, completed, progress := capturedTaskEventCounts(model)
		if started >= wantStarted && completed >= wantCompleted && progress >= wantProgress {
			return
		}
		time.Sleep(time.Millisecond)
	}
	started, completed, progress := capturedTaskEventCounts(model)
	t.Fatalf("captured events start=%d complete=%d progress=%d; want at least %d/%d/%d",
		started, completed, progress, wantStarted, wantCompleted, wantProgress)
}

func TestUIEventBroadcaster_LifecycleBurstIsLossless(t *testing.T) {
	program, model := newCapturingProgram(t)
	broadcaster := NewUIEventBroadcaster(program)
	t.Cleanup(broadcaster.Stop)
	// This used to suppress every event after the first, including terminal
	// events for unrelated tasks.
	broadcaster.minInterval = time.Hour
	const tasks = 25
	for i := 0; i < tasks; i++ {
		id := fmt.Sprintf("task-%02d", i)
		broadcaster.BroadcastTaskStart(id, "work", "test")
		broadcaster.BroadcastTaskComplete(id, true, time.Millisecond, nil, "test")
	}
	waitForCapturedTaskEvents(t, model, tasks, tasks, 0)
}

func TestUIEventBroadcaster_RateLimitsProgressPerTaskAndResetsOnCompletion(t *testing.T) {
	program, model := newCapturingProgram(t)
	broadcaster := NewUIEventBroadcaster(program)
	t.Cleanup(broadcaster.Stop)
	broadcaster.minInterval = time.Hour

	broadcaster.BroadcastTaskProgress("a", 0.1, "first")
	broadcaster.BroadcastTaskProgress("a", 0.2, "suppressed")
	broadcaster.BroadcastTaskProgress("b", 0.1, "independent")
	waitForCapturedTaskEvents(t, model, 0, 0, 2)

	broadcaster.BroadcastTaskComplete("a", true, time.Second, nil, "test")
	broadcaster.BroadcastTaskProgress("a", 1, "new lifecycle")
	waitForCapturedTaskEvents(t, model, 0, 1, 3)
}

func TestUIEventBroadcaster_StopRacesSafelyWithSendRegistration(t *testing.T) {
	program, model := newCapturingProgram(t)
	broadcaster := NewUIEventBroadcaster(program)
	broadcaster.minInterval = 0

	const senders = 8
	const messages = 25
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(senders)
	for sender := 0; sender < senders; sender++ {
		go func(sender int) {
			defer wg.Done()
			<-start
			for i := 0; i < messages; i++ {
				broadcaster.BroadcastTaskStart(fmt.Sprintf("%d-%d", sender, i), "work", "test")
			}
		}(sender)
	}
	close(start)
	stopDone := make(chan struct{})
	go func() {
		broadcaster.Stop()
		close(stopDone)
	}()
	wg.Wait()
	select {
	case <-stopDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not finish while sends were being registered")
	}

	beforeStarted, beforeCompleted, beforeProgress := capturedTaskEventCounts(model)
	for i := 0; i < 20; i++ {
		broadcaster.BroadcastTaskStart(fmt.Sprintf("after-stop-%d", i), "work", "test")
		broadcaster.BroadcastTaskProgress("after-stop", float64(i), "ignored")
	}
	time.Sleep(20 * time.Millisecond)
	afterStarted, afterCompleted, afterProgress := capturedTaskEventCounts(model)
	if beforeStarted != afterStarted || beforeCompleted != afterCompleted || beforeProgress != afterProgress {
		t.Fatalf("events accepted after Stop: before=%d/%d/%d after=%d/%d/%d",
			beforeStarted, beforeCompleted, beforeProgress, afterStarted, afterCompleted, afterProgress)
	}
	if broadcaster.IsEnabled() {
		t.Fatal("stopped broadcaster still reports enabled")
	}
}

func TestNewUIEventBroadcasterWithContext_AcceptsNilParent(t *testing.T) {
	broadcaster := NewUIEventBroadcasterWithContext(nil, nil)
	broadcaster.Stop()
}
