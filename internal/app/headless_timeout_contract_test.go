package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestWatchMessageIdle_LatchesTimeoutBeforeCancellingInnerContext(t *testing.T) {
	app := &App{}
	if err := app.beginHeadlessPolicyTracking(); err != nil {
		t.Fatalf("begin headless tracking: %v", err)
	}
	defer app.endHeadlessPolicyTracking()
	turn := app.activeHeadlessTerminalToken()

	app.stepHeartbeatMu.Lock()
	app.lastStepHeartbeat = time.Now().Add(-2 * time.Second)
	app.stepHeartbeatMu.Unlock()

	ctx, cancelContext := context.WithCancel(context.Background())
	defer cancelContext()
	ticks := make(chan time.Time, 1)
	cancelObserved := make(chan *headlessTerminalOutcome, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.watchMessageIdle(ctx, func() {
			cancelObserved <- app.headlessTerminalOutcomeSnapshot()
			cancelContext()
		}, turn, time.Second, ticks)
	}()

	ticks <- time.Now()
	select {
	case terminal := <-cancelObserved:
		if terminal == nil || terminal.Kind != "timeout" ||
			!strings.Contains(terminal.Message, "idle timeout") {
			t.Fatalf("terminal outcome at cancellation = %+v", terminal)
		}
	case <-time.After(time.Second):
		t.Fatal("idle watchdog did not cancel deterministically")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("idle watchdog did not stop after cancellation")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("inner context error = %v, want cancellation", ctx.Err())
	}
}

func TestRunHeadlessWithOptions_InternalTimeoutCannotBecomeFalseSuccess(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueStartupError(context.Canceled).
		EnqueueText("clean second turn")
	app, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})

	var calls atomic.Int32
	mock.OnSend = func(context.Context) {
		if calls.Add(1) != 1 {
			return
		}
		// This is the ordering used by the idle watchdog: latch the typed
		// outcome, then cancellation propagates through the executor. The outer
		// RunHeadless context deliberately remains live.
		turn := app.activeHeadlessTerminalToken()
		app.recordHeadlessTerminalOutcomeForTurn(turn, "timeout", "message processing idle timeout")
	}

	var stdout bytes.Buffer
	result, err := app.RunHeadlessWithOptions(context.Background(), "wait for the model", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       &stdout,
		Stderr:       io.Discard,
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("internal timeout error = %v, want typed deadline error", err)
	}
	if result.Status != "timeout" || result.Error == nil || result.Error.Kind != "timeout" {
		t.Fatalf("internal timeout result = %+v", result)
	}
	decoded := decodeSingleHeadlessResult(t, stdout.Bytes())
	if decoded.Status != "timeout" || decoded.Error == nil || decoded.Error.Kind != "timeout" {
		t.Fatalf("encoded internal timeout = %+v", decoded)
	}

	// The timeout token belongs to exactly one invocation. A later healthy
	// turn on the same App must neither inherit it nor fail spuriously.
	stdout.Reset()
	second, err := app.RunHeadlessWithOptions(context.Background(), "try again", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       &stdout,
		Stderr:       io.Discard,
	})
	if err != nil || second.Status != "success" || second.Error != nil {
		t.Fatalf("timeout leaked into second run: result=%+v err=%v", second, err)
	}
}

func TestRunHeadlessWithOptions_ModelRoundTimeoutIsTyped(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueScript(testkit.ResponseScript{
		DelayBeforeFirstChunk: time.Second,
		Chunks:                []client.ResponseChunk{{Text: "too late"}},
	})
	app, executor := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})
	executor.SetModelRoundTimeout(20 * time.Millisecond)

	result, err := app.RunHeadlessWithOptions(context.Background(), "finish within the round", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("model round timeout error = %v, want typed deadline error", err)
	}
	if result.Status != "timeout" || result.Error == nil || result.Error.Kind != "timeout" {
		t.Fatalf("model round timeout result = %+v", result)
	}
	if !strings.Contains(result.Error.Message, string(client.FailureReasonModelRoundTimeout)) {
		t.Fatalf("model timeout diagnostic = %q", result.Error.Message)
	}
}

func TestRunHeadlessWithOptions_OverallTimeoutIsTypedAndDoesNotLeak(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueScript(testkit.ResponseScript{
			DelayBeforeFirstChunk: time.Second,
			Chunks:                []client.ResponseChunk{{Text: "too late"}},
		}).
		EnqueueText("healthy next invocation")
	app, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})

	started := time.Now()
	result, err := app.RunHeadlessWithOptions(context.Background(), "finish within the invocation", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
		Timeout:      20 * time.Millisecond,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("overall timeout error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("overall timeout returned after %v, want prompt cancellation", elapsed)
	}
	if result.Status != "timeout" || result.Error == nil || result.Error.Kind != "timeout" {
		t.Fatalf("overall timeout result = %+v", result)
	}

	second, err := app.RunHeadlessWithOptions(context.Background(), "try again", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})
	if err != nil || second.Status != "success" || second.Result != "healthy next invocation" {
		t.Fatalf("timeout leaked into next invocation: result=%+v err=%v", second, err)
	}
}

func TestRunHeadlessWithOptions_MaxTurnsIsTypedAndDoesNotLeak(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueToolCall("inspect", map[string]any{"path": "main.go"}).
		EnqueueText("healthy next invocation")
	tool := &appHeadlessScriptedTool{
		name:    "inspect",
		results: []tools.ToolResult{tools.NewSuccessResult("inspection complete")},
	}
	app, _ := newHeadlessPolicyTestApp(t, mock, tool)

	var stdout bytes.Buffer
	result, err := app.RunHeadlessWithOptions(context.Background(), "inspect until capped", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       &stdout,
		Stderr:       io.Discard,
		MaxTurns:     1,
	})
	if !errors.Is(err, tools.ErrMaxTurnsExceeded) {
		t.Fatalf("max-turns error = %v, want ErrMaxTurnsExceeded", err)
	}
	if result.Status != "error" || result.Error == nil || result.Error.Kind != "max_turns" {
		t.Fatalf("max-turns result = %+v", result)
	}
	decoded := decodeSingleHeadlessResult(t, stdout.Bytes())
	if decoded.Error == nil || decoded.Error.Kind != "max_turns" {
		t.Fatalf("encoded max-turns result = %+v", decoded)
	}
	if tool.CallCount() != 1 {
		t.Fatalf("tool call count = %d, want 1", tool.CallCount())
	}

	second, err := app.RunHeadlessWithOptions(context.Background(), "try again", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})
	if err != nil || second.Status != "success" || second.Result != "healthy next invocation" {
		t.Fatalf("turn limit leaked into next invocation: result=%+v err=%v", second, err)
	}
}

func TestRunHeadlessWithOptions_MaxBudgetStopsToolSideEffects(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueToolCall("inspect", map[string]any{"path": "main.go"})
	tool := &appHeadlessScriptedTool{
		name:    "inspect",
		results: []tools.ToolResult{tools.NewSuccessResult("must not run")},
	}
	app, executor := newHeadlessPolicyTestApp(t, mock, tool)
	executor.SetCostCalculator(func(_, _ string, _, _, _ int) (float64, bool) {
		return 0.60, true
	})

	var stdout bytes.Buffer
	result, err := app.RunHeadlessWithOptions(context.Background(), "inspect within budget", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       &stdout,
		Stderr:       io.Discard,
		MaxBudgetUSD: 0.50,
	})
	if !errors.Is(err, tools.ErrBudgetExceeded) {
		t.Fatalf("budget error = %v, want ErrBudgetExceeded", err)
	}
	if result.Status != "error" || result.Error == nil || result.Error.Kind != "budget_exceeded" {
		t.Fatalf("budget result = %+v", result)
	}
	if !result.Cost.Tracked || result.Cost.EstimatedUSD != 0.60 {
		t.Fatalf("budget cost = %+v, want tracked $0.60", result.Cost)
	}
	if tool.CallCount() != 0 {
		t.Fatalf("tool call count = %d, want 0", tool.CallCount())
	}
	decoded := decodeSingleHeadlessResult(t, stdout.Bytes())
	if decoded.Error == nil || decoded.Error.Kind != "budget_exceeded" {
		t.Fatalf("encoded budget result = %+v", decoded)
	}
}

func TestRunHeadlessWithOptions_MaxBudgetRequiresPricingAndDoesNotLeak(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText("healthy unbudgeted invocation")
	app, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})

	result, err := app.RunHeadlessWithOptions(context.Background(), "do not call provider", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
		MaxBudgetUSD: 0.50,
	})
	if !errors.Is(err, tools.ErrCostUnavailable) {
		t.Fatalf("pricing error = %v, want ErrCostUnavailable", err)
	}
	if result.Status != "error" || result.Error == nil || result.Error.Kind != "cost_unavailable" {
		t.Fatalf("pricing result = %+v", result)
	}
	if len(mock.Calls()) != 0 {
		t.Fatalf("provider calls = %d, want 0", len(mock.Calls()))
	}
	if result.Usage != (HeadlessUsage{}) {
		t.Fatalf("preflight usage = %+v, want zero", result.Usage)
	}
	if result.Cost.Tracked || result.Cost.EstimatedUSD != 0 {
		t.Fatalf("preflight cost = %+v, want untracked zero", result.Cost)
	}

	second, err := app.RunHeadlessWithOptions(context.Background(), "run without a budget", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})
	if err != nil || second.Status != "success" || second.Result != "healthy unbudgeted invocation" {
		t.Fatalf("budget leaked into next invocation: result=%+v err=%v", second, err)
	}
}

func TestRunHeadlessWithOptions_RejectsNegativeLimitsBeforeExecution(t *testing.T) {
	tests := []HeadlessOptions{
		{OutputFormat: HeadlessOutputJSON, MaxTurns: -1},
		{OutputFormat: HeadlessOutputJSON, Timeout: -time.Second},
		{OutputFormat: HeadlessOutputJSON, MaxBudgetUSD: -0.01},
	}
	for _, opts := range tests {
		mock := testkit.NewMockClient().EnqueueText("must remain queued")
		app, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})
		opts.Stdout = io.Discard
		opts.Stderr = io.Discard

		result, err := app.RunHeadlessWithOptions(context.Background(), "do not execute", opts)
		if err == nil || result.Error == nil || result.Error.Kind != "validation" {
			t.Fatalf("negative limit result=%+v err=%v", result, err)
		}
		if len(mock.Calls()) != 0 {
			t.Fatalf("invalid limit reached model: %d calls", len(mock.Calls()))
		}
	}
}

func TestIsHeadlessTimeoutFailure_CoversTimeoutClassesNotCancellation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "external deadline", err: context.DeadlineExceeded, want: true},
		{name: "model round", err: client.NewModelRoundTimeoutError(time.Second), want: true},
		{name: "provider HTTP", err: client.WrapProviderHTTPTimeout(context.DeadlineExceeded, "mock", time.Second), want: true},
		{name: "stream idle", err: &client.ErrStreamIdleTimeout{Timeout: time.Second}, want: true},
		{name: "explicit cancellation", err: context.Canceled, want: false},
		{name: "ordinary failure", err: errors.New("provider rejected request"), want: false},
		{name: "nil", err: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isHeadlessTimeoutFailure(tt.err); got != tt.want {
				t.Fatalf("isHeadlessTimeoutFailure(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestHeadlessTerminalOutcome_StaleTimeoutTokenCannotLeak(t *testing.T) {
	app := &App{}
	if err := app.beginHeadlessPolicyTracking(); err != nil {
		t.Fatalf("begin first run: %v", err)
	}
	stale := app.activeHeadlessTerminalToken()
	app.endHeadlessPolicyTracking()

	if err := app.beginHeadlessPolicyTracking(); err != nil {
		t.Fatalf("begin second run: %v", err)
	}
	defer app.endHeadlessPolicyTracking()
	current := app.activeHeadlessTerminalToken()

	done := make(chan struct{})
	go func() {
		app.recordHeadlessTerminalOutcomeForTurn(stale, "timeout", "late old watchdog")
		close(done)
	}()
	<-done
	if got := app.headlessTerminalOutcomeSnapshot(); got != nil {
		t.Fatalf("stale timeout contaminated current run: %+v", got)
	}

	app.recordHeadlessTerminalOutcomeForTurn(current, "timeout", "current watchdog")
	app.recordHeadlessTerminalOutcomeForTurn(current, "panic", "later outcome")
	if got := app.headlessTerminalOutcomeSnapshot(); got == nil ||
		got.Kind != "timeout" || got.Message != "current watchdog" {
		t.Fatalf("first current outcome = %+v", got)
	}
}
