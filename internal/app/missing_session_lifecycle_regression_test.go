package app

import (
	"context"
	"testing"
	"time"
)

// A submit is accepted before processMessageWithContext validates that the
// session exists, so the early validation error must still release App's busy
// ownership. Otherwise ErrorMsg returns the TUI to an apparently idle prompt
// while App.processing remains true: every later submit is queued behind a
// request that has already returned and can never drain that queue.
func TestProcessMessageWithoutSessionReleasesBusyOwnership(t *testing.T) {
	program, model := newCapturingProgram(t)
	app := &App{
		program: program,
		ctx:     context.Background(),
	}

	app.mu.Lock()
	app.processing = true // handleSubmit claims this before starting the worker.
	app.mu.Unlock()

	app.processMessageWithContext(context.Background(), "retry my request")

	hasError := func() bool {
		return model.hasMsgType("ui.ErrorMsg") ||
			model.hasMsgType("*errors.errorString") ||
			model.hasMsgType("*fmt.wrapError")
	}
	deadline := time.Now().Add(time.Second)
	for !hasError() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !hasError() {
		t.Fatalf("missing-session failure was not surfaced to the UI; messages: %v", model.dump())
	}

	app.mu.Lock()
	busy := app.processing
	app.mu.Unlock()
	if busy {
		t.Fatal("missing-session failure left App.processing true; the idle-looking prompt will queue every later submit forever")
	}
}
