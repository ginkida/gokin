package app

import (
	"context"
	"testing"
	"time"

	"gokin/internal/agent"
	"gokin/internal/config"
	"gokin/internal/ui"
)

// TestPermPromptTimeout pins the config → wait-duration mapping: >0 → seconds,
// 0 → default (covers old configs), <0 → 0 meaning "no timeout" (indefinite).
func TestPermPromptTimeout(t *testing.T) {
	withSecs := func(secs int) *App {
		return &App{config: &config.Config{Permission: config.PermissionConfig{PromptTimeoutSeconds: secs}}}
	}
	if got := withSecs(0).permPromptTimeout(); got != DefaultPermissionTimeout {
		t.Errorf("0 → %v, want default %v", got, DefaultPermissionTimeout)
	}
	if got := withSecs(600).permPromptTimeout(); got != 600*time.Second {
		t.Errorf("600 → %v, want 10m", got)
	}
	if got := withSecs(-1).permPromptTimeout(); got != 0 {
		t.Errorf("-1 → %v, want 0 (indefinite)", got)
	}
	if got := (&App{}).permPromptTimeout(); got != DefaultPermissionTimeout {
		t.Errorf("nil config → %v, want default %v", got, DefaultPermissionTimeout)
	}
}

func TestApprovedWorkspaceFilesPreservesPerFileReview(t *testing.T) {
	changes := []agent.WorkspaceChangePreview{
		{FilePath: "a.txt"},
		{FilePath: "b.txt"},
		{FilePath: "c.txt"},
	}
	approved := approvedWorkspaceFiles(changes, map[string]ui.DiffDecision{
		"a.txt": ui.DiffApply,
		"b.txt": ui.DiffReject,
		"c.txt": ui.DiffApply,
		// A response cannot smuggle an unreviewed file into apply-back.
		"outside.txt": ui.DiffApply,
	})
	if len(approved) != 2 || approved[0] != "a.txt" || approved[1] != "c.txt" {
		t.Fatalf("approved files = %v, want [a.txt c.txt]", approved)
	}
}

// TestQuestionPromptTimeout pins the config → wait-duration mapping for
// ask_user questions: >0 → seconds, 0 → default (covers old configs),
// <0 → 0 meaning "no timeout" (indefinite). Mirrors TestPermPromptTimeout.
func TestQuestionPromptTimeout(t *testing.T) {
	withSecs := func(secs int) *App {
		return &App{config: &config.Config{Permission: config.PermissionConfig{QuestionTimeoutSeconds: secs}}}
	}
	if got := withSecs(0).questionPromptTimeout(); got != QuestionTimeout {
		t.Errorf("0 → %v, want default %v", got, QuestionTimeout)
	}
	if got := withSecs(900).questionPromptTimeout(); got != 900*time.Second {
		t.Errorf("900 → %v, want 15m", got)
	}
	if got := withSecs(-1).questionPromptTimeout(); got != 0 {
		t.Errorf("-1 → %v, want 0 (indefinite)", got)
	}
	if got := (&App{}).questionPromptTimeout(); got != QuestionTimeout {
		t.Errorf("nil config → %v, want default %v", got, QuestionTimeout)
	}
}

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
