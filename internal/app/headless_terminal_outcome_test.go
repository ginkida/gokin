package app

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestRunHeadlessWithOptions_RecoveredPanicIsTypedTerminalOutcome(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText("must not be returned")
	mock.OnSend = func(context.Context) {
		panic("headless test boom")
	}
	app, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})

	var stdout bytes.Buffer
	returned, err := app.RunHeadlessWithOptions(context.Background(), "trigger the panic", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       &stdout,
		Stderr:       io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "headless test boom") {
		t.Fatalf("recovered panic error = %v, want non-nil panic diagnostic", err)
	}
	decoded := decodeSingleHeadlessResult(t, stdout.Bytes())
	if decoded.Status != "error" || decoded.Error == nil || decoded.Error.Kind != "panic" {
		t.Fatalf("recovered panic result = %+v", decoded)
	}
	if returned.Error == nil || returned.Error.Kind != "panic" {
		t.Fatalf("returned panic result = %+v", returned)
	}

	// The latch belongs to one invocation. A later healthy turn on the same App
	// must not inherit the recovered panic and fail spuriously.
	mock.OnSend = nil
	mock.EnqueueText("second turn recovered")
	stdout.Reset()
	second, err := app.RunHeadlessWithOptions(context.Background(), "try again", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       &stdout,
		Stderr:       io.Discard,
	})
	if err != nil || second.Status != "success" || second.Error != nil {
		t.Fatalf("stale panic outcome leaked into next turn: result=%+v err=%v", second, err)
	}
}

func TestRunHeadlessWithOptions_DoneGateRefusalIsTypedTerminalOutcome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	mock := testkit.NewMockClient().
		EnqueueToolCall("write", map[string]any{"file_path": "artifact.txt"}).
		EnqueueText("Updated artifact.txt.")
	tool := &appHeadlessScriptedTool{
		name:    "write",
		results: []tools.ToolResult{tools.NewSuccessResult("updated")},
	}
	app, _ := newHeadlessPolicyTestApp(t, mock, tool)
	app.config.DoneGate.Enabled = true
	app.config.DoneGate.Mode = "normal"
	app.config.DoneGate.FailClosed = true
	app.config.DoneGate.AutoFixAttempts = 0
	app.session.SetWorkDir(app.workDir)
	manager, managerErr := chat.NewSessionManager(app.session, chat.DefaultSessionManagerConfig())
	if managerErr != nil {
		t.Fatalf("NewSessionManager: %v", managerErr)
	}
	app.sessionManager = manager

	var stdout bytes.Buffer
	returned, err := app.RunHeadlessWithOptions(context.Background(), "update the artifact", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       &stdout,
		Stderr:       io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "done-gate blocked finalization") {
		t.Fatalf("done-gate refusal error = %v, want non-nil gate diagnostic", err)
	}
	decoded := decodeSingleHeadlessResult(t, stdout.Bytes())
	if decoded.Status != "error" || decoded.Error == nil || decoded.Error.Kind != "done_gate_failed" {
		t.Fatalf("done-gate result = %+v", decoded)
	}
	if returned.Error == nil || returned.Error.Kind != "done_gate_failed" {
		t.Fatalf("returned done-gate result = %+v", returned)
	}
	if returned.Usage.InputTokens == 0 || returned.Usage.TotalTokens == 0 {
		t.Fatalf("failed finalization lost already-billed usage: %+v", returned.Usage)
	}
	if tool.CallCount() != 1 {
		t.Fatalf("write tool calls = %d, want 1 before finalization gate", tool.CallCount())
	}
	history, historyErr := chat.NewHistoryManager()
	if historyErr != nil {
		t.Fatalf("NewHistoryManager: %v", historyErr)
	}
	persisted, historyErr := history.LoadFull(app.session.GetID())
	if historyErr != nil {
		t.Fatalf("LoadFull: %v", historyErr)
	}
	if len(persisted.ToolCheckpoints) == 0 || persisted.ToolCheckpoints[0].ToolName != "write" {
		t.Fatalf("terminal save lost completed write checkpoint: %+v", persisted.ToolCheckpoints)
	}
}

func TestInteractiveDoneGateRefusalRemainsRecoverable(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueToolCall("write", map[string]any{"file_path": "artifact.txt"}).
		EnqueueText("Updated artifact.txt.")
	app, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{
		name:    "write",
		results: []tools.ToolResult{tools.NewSuccessResult("updated")},
	})
	app.config.DoneGate.Enabled = true
	app.config.DoneGate.Mode = "normal"
	app.config.DoneGate.FailClosed = true
	app.config.DoneGate.AutoFixAttempts = 0

	app.processMessageWithContext(context.Background(), "update the artifact")

	if app.lastError != "" {
		t.Fatalf("interactive done-gate refusal became terminal model error: %q", app.lastError)
	}
	if terminal := app.headlessTerminalOutcomeSnapshot(); terminal != nil {
		t.Fatalf("interactive gate contaminated headless terminal state: %+v", terminal)
	}
	if app.processing {
		t.Fatal("interactive gate refusal left the foreground prompt busy")
	}
}

func TestHeadlessTerminalOutcome_FirstWinsAndClears(t *testing.T) {
	app := &App{}
	if err := app.beginHeadlessPolicyTracking(); err != nil {
		t.Fatalf("begin headless tracking: %v", err)
	}
	app.recordHeadlessTerminalOutcome("panic", "first outcome")
	app.recordHeadlessTerminalOutcome("done_gate_failed", "later outcome")
	if got := app.headlessTerminalOutcomeSnapshot(); got == nil || got.Kind != "panic" || got.Message != "first outcome" {
		t.Fatalf("latched outcome = %+v, want first outcome", got)
	}

	app.endHeadlessPolicyTracking()
	if got := app.headlessTerminalOutcomeSnapshot(); got != nil {
		t.Fatalf("terminal outcome survived end of run: %+v", got)
	}
	app.recordHeadlessTerminalOutcome("panic", "inactive outcome")
	if got := app.headlessTerminalOutcomeSnapshot(); got != nil {
		t.Fatalf("inactive run recorded terminal outcome: %+v", got)
	}
}
