package tools

import (
	"testing"
	"time"
)

// Bubble Tea Program.Send is synchronous. The app's OnSteerLeftover callback
// sends QueuedCountMsg while finishUserSteering holds userSteerCallbackMu. If
// the event loop is concurrently handling Esc, it calls CancelUserSteering and
// cannot receive that send until cancellation returns: callback waits for UI,
// UI waits for callbackMu. This channel arrangement models that exact cycle.
func TestCancelUserSteeringDoesNotDeadlockWithSynchronousLeftoverDelivery(t *testing.T) {
	e := &Executor{}
	e.userSteerActive = true
	e.userSteers = []string{"late follow-up"}

	callbackEntered := make(chan struct{})
	programSend := make(chan struct{})
	e.handler = &ExecutionHandler{OnSteerLeftover: func([]string) {
		close(callbackEntered)
		programSend <- struct{}{}
	}}

	finishDone := make(chan struct{})
	go func() {
		e.finishUserSteering()
		close(finishDone)
	}()
	select {
	case <-callbackEntered:
	case <-time.After(time.Second):
		t.Fatal("leftover callback did not start")
	}

	// The UI can receive the callback's synchronous send only after its current
	// Esc Update (and therefore CancelUserSteering) returns.
	cancelDone := make(chan struct{})
	go func() {
		e.CancelUserSteering()
		close(cancelDone)
		<-programSend
	}()

	select {
	case <-cancelDone:
	case <-time.After(150 * time.Millisecond):
		t.Fatal("CancelUserSteering deadlocked with synchronous leftover delivery")
	}
	select {
	case <-finishDone:
	case <-time.After(time.Second):
		t.Fatal("leftover finalizer did not return after cancellation")
	}
}
