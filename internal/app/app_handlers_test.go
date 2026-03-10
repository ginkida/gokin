package app

import (
	"context"
	"testing"
	"time"

	"gokin/internal/ui"
)

func TestPromptDiffDecisionSerializesRequests(t *testing.T) {
	app := &App{
		diffResponseChan: make(chan ui.DiffDecision, 1),
	}

	app.diffPromptMu.Lock()
	done := make(chan ui.DiffDecision, 1)
	errCh := make(chan error, 1)

	go func() {
		decision, err := app.promptDiffDecision(context.Background(), "file.txt", "old\n", "new\n", "write", false)
		if err != nil {
			errCh <- err
			return
		}
		done <- decision
	}()

	select {
	case <-done:
		t.Fatal("promptDiffDecision returned before diff prompt mutex was released")
	case err := <-errCh:
		t.Fatalf("promptDiffDecision returned error before mutex release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	app.diffPromptMu.Unlock()

	select {
	case decision := <-done:
		if decision != ui.DiffApply {
			t.Fatalf("decision = %v, want %v", decision, ui.DiffApply)
		}
	case err := <-errCh:
		t.Fatalf("promptDiffDecision returned error after mutex release: %v", err)
	case <-time.After(time.Second):
		t.Fatal("promptDiffDecision did not resume after diff prompt mutex was released")
	}
}

func TestPromptMultiDiffDecisionDrainsStaleResponses(t *testing.T) {
	app := &App{
		multiDiffResponseChan: make(chan map[string]ui.DiffDecision, 1),
	}
	app.multiDiffResponseChan <- map[string]ui.DiffDecision{
		"stale.txt": ui.DiffReject,
	}

	decisions, err := app.promptMultiDiffDecision(context.Background(), []ui.DiffFile{
		{FilePath: "fresh.txt", OldContent: "old\n", NewContent: "new\n"},
	})
	if err != nil {
		t.Fatalf("promptMultiDiffDecision: %v", err)
	}
	if got := decisions["fresh.txt"]; got != ui.DiffApply {
		t.Fatalf("decision for fresh.txt = %v, want %v", got, ui.DiffApply)
	}

	select {
	case stale := <-app.multiDiffResponseChan:
		t.Fatalf("stale multi diff response was not drained: %+v", stale)
	default:
	}
}
