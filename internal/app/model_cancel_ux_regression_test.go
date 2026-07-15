package app

import (
	"context"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/config"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

// Explicit cancellation is an intentional terminal state, not a provider
// failure. Surfacing context.Canceled as ErrorMsg and saving it in lastError
// makes the next request receive a misleading "previous attempt failed" note.
func TestCancelledModelTurnDoesNotSurfaceOrCarryForwardFailure(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model.Provider = "mock"
	cfg.Model.Name = "mock-model"
	cfg.DoneGate.Enabled = false

	mock := testkit.NewMockClient().EnqueueStartupError(context.Canceled)
	registry := tools.NewRegistry()
	executor := tools.NewExecutor(registry, mock, time.Second)
	program, model := newCapturingProgram(t)
	a := &App{
		config:              cfg,
		workDir:             t.TempDir(),
		client:              mock,
		registry:            registry,
		executor:            executor,
		session:             chat.NewSession(),
		ctx:                 context.Background(),
		program:             program,
		processing:          true,
		rateLimitRetryCount: make(map[string]int),
	}

	a.processMessageWithContext(context.Background(), "cancel this turn")
	deadline := time.Now().Add(time.Second)
	for !model.hasMsgType("ui.ResponseDoneMsg") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !model.hasMsgType("ui.ResponseDoneMsg") {
		t.Fatalf("explicit cancellation did not finish the UI request state; messages: %v", model.dump())
	}

	if model.hasMsgType("ui.ErrorMsg") {
		t.Fatalf("explicit cancellation surfaced as an error; messages: %v", model.dump())
	}
	if a.lastError != "" {
		t.Fatalf("explicit cancellation was retained for injection into the next request: %q", a.lastError)
	}
}
