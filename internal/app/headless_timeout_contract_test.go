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
