package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/permission"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestRunHeadlessWithOptions_JSONEmitsOneVersionedResult(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueText("first machine-readable answer").
		EnqueueText("second answer")
	app, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{
		name:    "unused",
		results: []tools.ToolResult{tools.NewSuccessResult("unused")},
	})

	var firstOut, firstErr bytes.Buffer
	first, err := app.RunHeadlessWithOptions(context.Background(), "first turn", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       &firstOut,
		Stderr:       &firstErr,
	})
	if err != nil {
		t.Fatalf("RunHeadlessWithOptions(first): %v", err)
	}
	decoded := decodeSingleHeadlessResult(t, firstOut.Bytes())
	if decoded.SchemaVersion != HeadlessSchemaVersion || decoded.Type != "result" {
		t.Fatalf("schema envelope = %+v", decoded)
	}
	if decoded.Status != "success" || decoded.Error != nil {
		t.Fatalf("status = %q error = %+v", decoded.Status, decoded.Error)
	}
	if decoded.Result != "first machine-readable answer" {
		t.Fatalf("result = %q", decoded.Result)
	}
	if decoded.SessionID == "" || decoded.SessionID != app.session.GetID() {
		t.Fatalf("session_id = %q, current = %q", decoded.SessionID, app.session.GetID())
	}
	if decoded.Usage.InputTokens != 10 || decoded.Usage.TotalTokens <= 10 {
		t.Fatalf("usage = %+v, want one-turn provider delta", decoded.Usage)
	}
	if first.Result != decoded.Result || first.Status != decoded.Status {
		t.Fatalf("returned result diverged from JSON: returned=%+v decoded=%+v", first, decoded)
	}
	if firstErr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", firstErr.String())
	}
	if strings.Contains(firstOut.String(), "\nfirst machine-readable answer") {
		t.Fatalf("raw streamed text leaked outside JSON: %q", firstOut.String())
	}

	// Usage in the envelope is invocation-scoped even when the same App is
	// reused. A cumulative total here would make automation double-bill turns.
	var secondOut bytes.Buffer
	second, err := app.RunHeadlessWithOptions(context.Background(), "second turn", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       &secondOut,
		Stderr:       io.Discard,
	})
	if err != nil {
		t.Fatalf("RunHeadlessWithOptions(second): %v", err)
	}
	_ = decodeSingleHeadlessResult(t, secondOut.Bytes())
	if second.Usage.InputTokens != 10 {
		t.Fatalf("second input delta = %d, want 10 (not cumulative)", second.Usage.InputTokens)
	}
}

func TestRunHeadlessWithOptions_FallbackUsageDoesNotDependOnSessionDelta(t *testing.T) {
	zeroMetadata := func(text string) testkit.ResponseScript {
		return testkit.ResponseScript{Chunks: []client.ResponseChunk{
			{Text: text},
			{Done: true},
		}}
	}
	mock := testkit.NewMockClient().
		EnqueueScript(zeroMetadata("first estimated answer")).
		EnqueueScript(zeroMetadata("second estimated answer"))
	application, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})

	first, err := application.RunHeadlessWithOptions(context.Background(), "estimate this request", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})
	if err != nil || first.Usage.InputTokens == 0 || first.Usage.OutputTokens == 0 {
		t.Fatalf("first fallback usage=%+v err=%v", first.Usage, err)
	}

	// Pin the cumulative session lower bound above any realistic next context.
	// A before/after session delta would now be zero; the invocation ledger must
	// still account for the second request itself.
	application.mu.Lock()
	application.totalInputTokens = 1_000_000
	application.costTracked = true // stale session pricing must not leak either
	application.mu.Unlock()

	second, err := application.RunHeadlessWithOptions(context.Background(), "estimate this request", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})
	if err != nil {
		t.Fatalf("second fallback run: %v", err)
	}
	if second.Usage.InputTokens == 0 || second.Usage.OutputTokens == 0 {
		t.Fatalf("second invocation lost fallback usage: %+v", second.Usage)
	}
	if second.Cost.Tracked {
		t.Fatalf("session-wide tracked bit leaked into unpriced invocation: %+v", second.Cost)
	}
}

func TestRunHeadlessWithOptions_JSONPolicyFailureIsTypedAndNonZero(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueToolCall("write", map[string]any{"file_path": "blocked.txt"}).
		EnqueueText("I claim the blocked write succeeded.")
	tool := &appHeadlessScriptedTool{
		name:    "write",
		results: []tools.ToolResult{tools.NewSuccessResult("must not execute")},
	}
	app, exec := newHeadlessPolicyTestApp(t, mock, tool)
	rules := permission.DefaultRules()
	rules.SetPolicy("write", permission.LevelDeny)
	exec.SetPermissions(permission.NewManager(rules, true))

	var stdout, stderr bytes.Buffer
	returned, err := app.RunHeadlessWithOptions(context.Background(), "perform blocked work", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       &stdout,
		Stderr:       &stderr,
	})
	if err == nil {
		t.Fatal("policy-blocked JSON run returned nil error")
	}
	decoded := decodeSingleHeadlessResult(t, stdout.Bytes())
	if decoded.Status != "policy_blocked" || decoded.Error == nil {
		t.Fatalf("typed failure missing: %+v", decoded)
	}
	if decoded.Error.Kind != "policy_blocked" || decoded.Error.PolicyKind != "permission" || decoded.Error.Tool != "write" {
		t.Fatalf("policy error = %+v", decoded.Error)
	}
	if decoded.Result != "I claim the blocked write succeeded." {
		t.Fatalf("model prose should remain inspectable inside result, got %q", decoded.Result)
	}
	if returned.Status != decoded.Status || returned.Error == nil {
		t.Fatalf("returned result diverged from JSON: %+v", returned)
	}
	if tool.CallCount() != 0 {
		t.Fatalf("blocked tool executed %d times", tool.CallCount())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestRunHeadlessWithOptions_RejectsUnknownFormatBeforeExecution(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText("must remain queued")
	app, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})

	var stdout bytes.Buffer
	_, err := app.RunHeadlessWithOptions(context.Background(), "answer", HeadlessOptions{
		OutputFormat: "xml",
		Stdout:       &stdout,
		Stderr:       io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "output format") {
		t.Fatalf("unknown format error = %v", err)
	}
	if stdout.Len() != 0 || len(mock.Calls()) != 0 {
		t.Fatalf("invalid format caused side effects: stdout=%q calls=%d", stdout.String(), len(mock.Calls()))
	}
}

func TestRunHeadlessWithOptions_PersistenceFailureIsTerminal(t *testing.T) {
	goodData := t.TempDir()
	t.Setenv("XDG_DATA_HOME", goodData)
	mock := testkit.NewMockClient().EnqueueText("the model result remains available")
	app, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})
	app.session.SetWorkDir(t.TempDir())
	manager, err := chat.NewSessionManager(app.session, chat.DefaultSessionManagerConfig())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	app.sessionManager = manager

	// SaveFull resolves XDG_DATA_HOME at save time. Plant a regular file where
	// its gokin directory must be created for a deterministic persistence error.
	blockedData := t.TempDir()
	if err := os.WriteFile(filepath.Join(blockedData, "gokin"), []byte("blocked"), 0600); err != nil {
		t.Fatalf("plant blocking file: %v", err)
	}
	t.Setenv("XDG_DATA_HOME", blockedData)

	var stdout, stderr bytes.Buffer
	returned, err := app.RunHeadlessWithOptions(context.Background(), "answer then save", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       &stdout,
		Stderr:       &stderr,
	})
	if err == nil {
		t.Fatal("persistence failure returned exit success")
	}
	decoded := decodeSingleHeadlessResult(t, stdout.Bytes())
	if decoded.Status != "error" || decoded.Error == nil || decoded.Error.Kind != "persistence_failed" {
		t.Fatalf("persistence result = %+v", decoded)
	}
	if decoded.Result != "the model result remains available" {
		t.Fatalf("completed model result was lost: %q", decoded.Result)
	}
	if returned.Error == nil || returned.Error.Kind != "persistence_failed" {
		t.Fatalf("returned result = %+v", returned)
	}
	if !strings.Contains(stderr.String(), "failed to persist session") {
		t.Fatalf("stderr lacks persistence diagnostic: %q", stderr.String())
	}
}

func TestRunHeadlessWithOptions_JSONCancellationStatusesAreStable(t *testing.T) {
	tests := []struct {
		name       string
		context    func() (context.Context, context.CancelFunc)
		wantStatus string
		wantErr    error
	}{
		{
			name: "cancelled",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			wantStatus: "cancelled",
			wantErr:    context.Canceled,
		},
		{
			name: "timeout",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			wantStatus: "timeout",
			wantErr:    context.DeadlineExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := testkit.NewMockClient().EnqueueText("must not be requested")
			app, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})
			ctx, cancel := tt.context()
			defer cancel()

			var stdout bytes.Buffer
			_, err := app.RunHeadlessWithOptions(ctx, "cancel before model call", HeadlessOptions{
				OutputFormat: HeadlessOutputJSON,
				Stdout:       &stdout,
				Stderr:       io.Discard,
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			decoded := decodeSingleHeadlessResult(t, stdout.Bytes())
			if decoded.Status != tt.wantStatus || decoded.Error == nil || decoded.Error.Kind != tt.wantStatus {
				t.Fatalf("result = %+v", decoded)
			}
			if len(mock.Calls()) != 0 {
				t.Fatalf("pre-cancelled run called model %d times", len(mock.Calls()))
			}
		})
	}
}

func TestRunHeadlessWithOptions_JSONEarlyFailuresStillEmitEnvelope(t *testing.T) {
	tests := []struct {
		name     string
		app      *App
		prompt   string
		prepare  func(t *testing.T, app *App)
		cleanup  func(app *App)
		wantKind string
	}{
		{name: "validation", app: &App{}, prompt: " ", wantKind: "validation"},
		{name: "nil app", app: nil, prompt: "answer", wantKind: "app_init"},
		{name: "missing executor", app: &App{session: chat.NewSession()}, prompt: "answer", wantKind: "app_init"},
		{
			name: "concurrent headless run",
			app: func() *App {
				a, _ := newHeadlessPolicyTestApp(t, testkit.NewMockClient(), &appHeadlessScriptedTool{name: "unused"})
				return a
			}(),
			prompt: "answer",
			prepare: func(t *testing.T, app *App) {
				t.Helper()
				if err := app.beginHeadlessPolicyTracking(); err != nil {
					t.Fatal(err)
				}
			},
			cleanup:  func(app *App) { app.endHeadlessPolicyTracking() },
			wantKind: "headless_busy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.prepare != nil {
				tt.prepare(t, tt.app)
			}
			if tt.cleanup != nil {
				defer tt.cleanup(tt.app)
			}
			var stdout bytes.Buffer
			_, err := tt.app.RunHeadlessWithOptions(context.Background(), tt.prompt, HeadlessOptions{
				OutputFormat: HeadlessOutputJSON,
				Stdout:       &stdout,
				Stderr:       io.Discard,
			})
			if err == nil {
				t.Fatal("early failure returned nil error")
			}
			decoded := decodeSingleHeadlessResult(t, stdout.Bytes())
			if decoded.Status != "error" || decoded.Error == nil || decoded.Error.Kind != tt.wantKind {
				t.Fatalf("result = %+v", decoded)
			}
		})
	}
}

func decodeSingleHeadlessResult(t *testing.T, data []byte) HeadlessResult {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	var result HeadlessResult
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode headless JSON %q: %v", string(data), err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("stdout contains more than one JSON value: %q (decode=%v)", string(data), err)
	}
	return result
}

func TestSelectHeadlessResultPrefersTerminalAnswerOverInternalStreams(t *testing.T) {
	if got := selectHeadlessResult("draft answerinternal auto-fix prose", "final reviewed answer"); got != "final reviewed answer" {
		t.Fatalf("selected result = %q", got)
	}
	if got := selectHeadlessResult("partial response", ""); got != "partial response" {
		t.Fatalf("fallback result = %q", got)
	}
}

func TestPrepareHeadlessRuntimeDisablesDetachedWork(t *testing.T) {
	registry := tools.NewRegistry()
	bash := tools.NewBashTool(t.TempDir())
	task := tools.NewTaskTool()
	if err := registry.Register(bash); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(task); err != nil {
		t.Fatal(err)
	}
	application := &App{registry: registry}
	application.prepareHeadlessRuntime()

	if _, ok := bash.Declaration().Parameters.Properties["run_in_background"]; ok {
		t.Fatal("headless bash still advertises detached execution")
	}
	if _, ok := task.Declaration().Parameters.Properties["run_in_background"]; ok {
		t.Fatal("headless task still advertises detached execution")
	}
}
