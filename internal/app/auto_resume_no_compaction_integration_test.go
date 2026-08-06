package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/config"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

func newNoCompactionTimeoutApp(
	t *testing.T,
	initialAttempts int,
) (*App, context.CancelFunc, string, *msgCapturingModel) {
	t.Helper()
	const prompt = "finish the already compact task"
	program, model := newCapturingProgram(t)
	ctx, cancel := context.WithCancel(context.Background())
	mock := testkit.NewMockClient().EnqueueError(
		client.NewModelRoundTimeoutError(client.DefaultModelRoundTimeout))
	registry := tools.NewRegistry()
	executor := tools.NewExecutor(registry, mock, time.Second)
	workDir := t.TempDir()
	journal, err := NewExecutionJournal(workDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Model.Provider = "mock"
	cfg.Model.Name = "mock-model"
	cfg.DoneGate.Enabled = false
	application := &App{
		config:              cfg,
		workDir:             workDir,
		client:              mock,
		registry:            registry,
		executor:            executor,
		session:             chat.NewSession(),
		ctx:                 ctx,
		program:             program,
		journal:             journal,
		rateLimitRetryCount: make(map[string]int),
		autoResumeCount:     make(map[string]int),
	}
	if initialAttempts > 0 {
		application.autoResumeCount[rateLimitRetryKey(prompt)] = initialAttempts
	}
	return application, cancel, prompt, model
}

func capturedAutoResumeMessages(model *msgCapturingModel) (statuses []ui.StatusUpdateMsg, hasError bool) {
	model.mu.Lock()
	defer model.mu.Unlock()
	for _, message := range model.msgs {
		switch typed := message.(type) {
		case ui.StatusUpdateMsg:
			statuses = append(statuses, typed)
		case ui.ErrorMsg:
			hasError = true
		}
	}
	return statuses, hasError
}

func waitForAutoResumeUI(t *testing.T, model *msgCapturingModel, predicate func([]ui.StatusUpdateMsg, bool) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		statuses, hasError := capturedAutoResumeMessages(model)
		if predicate(statuses, hasError) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	statuses, hasError := capturedAutoResumeMessages(model)
	t.Fatalf("expected auto-resume UI state not observed: statuses=%+v error=%v", statuses, hasError)
}

func TestProcessMessage_FirstModelTimeoutRetriesWhenContextAlreadyCompact(t *testing.T) {
	application, cancel, prompt, model := newNoCompactionTimeoutApp(t, 0)
	defer cancel()

	application.processMessageWithContext(context.Background(), prompt)
	if got := application.autoResumeCount[rateLimitRetryKey(prompt)]; got != 1 {
		t.Fatalf("auto-resume attempts = %d, want first retry scheduled", got)
	}
	waitForAutoResumeUI(t, model, func(statuses []ui.StatusUpdateMsg, hasError bool) bool {
		for _, status := range statuses {
			if status.Type == ui.StatusRetry &&
				strings.Contains(status.Message, "context already compact") {
				return !hasError
			}
		}
		return false
	})
	entries, err := application.journal.Tail(20)
	if err != nil {
		t.Fatal(err)
	}
	foundStructuredRecovery := false
	for _, entry := range entries {
		if entry.Event != "auto_resume_scheduled" {
			continue
		}
		foundStructuredRecovery = entry.Details["failure_reason"] == "model_round_timeout" &&
			entry.Details["timeout"] == client.DefaultModelRoundTimeout.String() &&
			entry.Details["max_attempts"] == float64(maxAutoResumeAttempts)
	}
	if !foundStructuredRecovery {
		t.Fatalf("auto-resume journal event lacks canonical timeout telemetry: %+v", entries)
	}
	// The test deliberately has no SessionManager, so recovery falls back to a
	// process-local timer. Cancel it immediately rather than waiting 15 seconds.
	cancel()
}

func TestProcessMessage_SecondUnchangedModelTimeoutStopsWithGuidance(t *testing.T) {
	application, cancel, prompt, model := newNoCompactionTimeoutApp(t, 1)
	defer cancel()

	application.processMessageWithContext(context.Background(), prompt)
	if got := application.autoResumeCount[rateLimitRetryKey(prompt)]; got != 1 {
		t.Fatalf("skipped second attempt should be refunded to 1, got %d", got)
	}
	waitForAutoResumeUI(t, model, func(statuses []ui.StatusUpdateMsg, hasError bool) bool {
		if !hasError {
			return false
		}
		for _, status := range statuses {
			if status.Type == ui.StatusWarning &&
				strings.Contains(status.Message, "/timeout 20m") {
				return true
			}
		}
		return false
	})
}
