package app

import "testing"

// A follow-up accepted by the executor's steering window can still be waiting
// in userSteers when Esc cancels the foreground request. CancelProcessing first
// drains the app pending queue; then executeLoop unwinds and OnSteerLeftover
// moves that late steer into the queue. Without cancellation ownership, the
// message therefore resurrects after the user explicitly stopped the turn and
// finishMessageProcessing dispatches it as a new request.
func TestCancelProcessingDoesNotRequeueLateSteerLeftover(t *testing.T) {
	a := &App{}
	a.processing = true
	a.processingCancel = func() {}
	handler := a.buildExecutionHandler(nil)

	// This is the real cancellation ordering: Esc drains first, while the
	// executor reacts to ctx cancellation and closes its steering window later.
	a.CancelProcessing()
	handler.OnSteerLeftover([]string{"follow-up typed before Esc"})

	if got := a.pendingCount(); got != 0 {
		t.Fatalf("late steer was requeued after cancellation: pending=%d; Esc must not start accepted type-ahead as a new turn", got)
	}
}
