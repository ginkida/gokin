package app

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/commands"
	"gokin/internal/testkit"
)

func TestRunHeadlessWithOptions_InteractiveForegroundReturnsBusyWithoutMutation(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText("interactive turn completed")
	application, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})

	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	mock.OnSend = func(ctx context.Context) {
		startedOnce.Do(func() { close(started) })
		select {
		case <-release:
		case <-ctx.Done():
		}
	}

	// Mirror the interactive submit path: the TUI owns the application runtime,
	// and this accepted turn owns processing + its cancellation handle.
	application.mu.Lock()
	application.running = true
	application.processing = true
	interactiveCtx := application.claimForegroundContextLocked()
	application.mu.Unlock()
	interactiveDone := make(chan struct{})
	go func() {
		application.processMessageWithContext(interactiveCtx, "interactive request")
		close(interactiveDone)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("interactive turn did not reach the model")
	}

	var stdout bytes.Buffer
	result, err := application.RunHeadlessWithOptions(context.Background(), "must not overtake", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       &stdout,
		Stderr:       io.Discard,
	})
	if err == nil {
		t.Fatal("concurrent headless invocation unexpectedly succeeded")
	}
	if result.Error == nil || result.Error.Kind != "headless_busy" {
		t.Fatalf("busy result = %+v, err=%v", result, err)
	}
	decoded := decodeSingleHeadlessResult(t, stdout.Bytes())
	if decoded.Error == nil || decoded.Error.Kind != "headless_busy" {
		t.Fatalf("encoded busy result = %+v", decoded)
	}
	if calls := mock.Calls(); len(calls) != 1 || calls[0].Message != "interactive request" {
		t.Fatalf("model calls after busy refusal = %+v", calls)
	}

	application.mu.Lock()
	running := application.running
	processing := application.processing
	headlessActive := application.headlessRunActive
	headlessDirect := application.headlessDirect
	application.mu.Unlock()
	application.processingMu.Lock()
	cancelOwnerPreserved := application.processingCancel != nil
	application.processingMu.Unlock()
	if !running || !processing || !cancelOwnerPreserved {
		t.Fatalf("interactive ownership was overwritten: running=%v processing=%v cancel=%v",
			running, processing, cancelOwnerPreserved)
	}
	if headlessActive || headlessDirect {
		t.Fatalf("busy refusal leaked headless state: active=%v direct=%v", headlessActive, headlessDirect)
	}

	releaseOnce.Do(func() { close(release) })
	select {
	case <-interactiveDone:
	case <-time.After(time.Second):
		t.Fatal("interactive turn did not finish after busy refusal")
	}
	application.mu.Lock()
	application.running = false
	application.mu.Unlock()
}

func TestRunHeadlessWithOptions_RejectsEachOccupiedLifecycleFlag(t *testing.T) {
	for _, tt := range []struct {
		name       string
		running    bool
		processing bool
	}{
		{name: "interactive runtime", running: true},
		{name: "foreground turn", processing: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock := testkit.NewMockClient().EnqueueText("must not execute")
			application, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})
			application.mu.Lock()
			application.running = tt.running
			application.processing = tt.processing
			application.mu.Unlock()

			result, err := application.RunHeadlessWithOptions(context.Background(), "do not start", HeadlessOptions{
				OutputFormat: HeadlessOutputJSON,
				Stdout:       io.Discard,
				Stderr:       io.Discard,
			})
			if err == nil || result.Error == nil || result.Error.Kind != "headless_busy" {
				t.Fatalf("occupied lifecycle result = %+v, err=%v", result, err)
			}
			if calls := mock.Calls(); len(calls) != 0 {
				t.Fatalf("busy invocation reached model: %+v", calls)
			}
			application.mu.Lock()
			gotRunning := application.running
			gotProcessing := application.processing
			active := application.headlessRunActive
			application.running = false
			application.processing = false
			application.mu.Unlock()
			if gotRunning != tt.running || gotProcessing != tt.processing || active {
				t.Fatalf("busy refusal mutated flags: running=%v processing=%v active=%v",
					gotRunning, gotProcessing, active)
			}
		})
	}
}

func TestRunHeadlessWithOptions_SequentialRunsPreserveInfrastructureAndFlags(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueText("first headless result").
		EnqueueText("second headless result")
	application, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})
	rootContext := application.ctx
	previousPresenter := application.currentPresenter()
	application.setPresenter(previousPresenter)

	first, err := application.RunHeadlessWithOptions(context.Background(), "first", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})
	if err != nil || first.Status != "success" || !strings.Contains(first.Result, "first headless result") {
		t.Fatalf("first run = %+v, err=%v", first, err)
	}
	assertHeadlessRunReleased(t, application, rootContext, previousPresenter, false)

	// An existing routing choice is caller-owned state too. Headless execution
	// may force direct routing for its turn, but must restore the exact prior bit.
	application.mu.Lock()
	application.headlessDirect = true
	application.mu.Unlock()
	second, err := application.RunHeadlessWithOptions(context.Background(), "second", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})
	if err != nil || second.Status != "success" || !strings.Contains(second.Result, "second headless result") {
		t.Fatalf("second run = %+v, err=%v", second, err)
	}
	assertHeadlessRunReleased(t, application, rootContext, previousPresenter, true)
	if calls := mock.Calls(); len(calls) != 2 {
		t.Fatalf("model calls = %d, want exactly two sequential turns", len(calls))
	}
}

func TestRunHeadlessWithOptions_HoldsForegroundThroughTerminalEncoding(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueText("headless result").
		EnqueueText("queued interactive result")
	application, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})
	application.commandHandler = commands.NewHandler()

	modelStarted := make(chan struct{})
	modelRelease := make(chan struct{})
	queuedStarted := make(chan struct{})
	var sends atomic.Int32
	mock.OnSend = func(ctx context.Context) {
		switch sends.Add(1) {
		case 1:
			close(modelStarted)
			select {
			case <-modelRelease:
			case <-ctx.Done():
			}
		case 2:
			close(queuedStarted)
		}
	}

	encodeRelease := make(chan struct{})
	writer := &gatedHeadlessWriter{
		started: make(chan struct{}),
		release: encodeRelease,
	}
	type runOutcome struct {
		result HeadlessResult
		err    error
	}
	runDone := make(chan runOutcome, 1)
	go func() {
		result, err := application.RunHeadlessWithOptions(context.Background(), "headless", HeadlessOptions{
			OutputFormat: HeadlessOutputJSON,
			Stdout:       writer,
			Stderr:       io.Discard,
		})
		runDone <- runOutcome{result: result, err: err}
	}()

	select {
	case <-modelStarted:
	case <-time.After(time.Second):
		t.Fatal("headless turn did not reach the model")
	}
	if pos, ok := application.enqueuePending("queued interactive"); !ok || pos != 1 {
		t.Fatalf("enqueue pending = (%d, %v), want (1, true)", pos, ok)
	}
	close(modelRelease)

	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("headless turn did not reach terminal encoding")
	}
	if calls := mock.Calls(); len(calls) != 1 {
		t.Fatalf("queued turn started before terminal encoding completed: calls=%d", len(calls))
	}
	application.mu.Lock()
	processing := application.processing
	headlessActive := application.headlessRunActive
	application.mu.Unlock()
	if !processing || !headlessActive || application.pendingCount() != 1 {
		t.Fatalf("foreground released early: processing=%v active=%v pending=%d",
			processing, headlessActive, application.pendingCount())
	}

	close(encodeRelease)
	select {
	case outcome := <-runDone:
		if outcome.err != nil || outcome.result.Status != "success" {
			t.Fatalf("headless outcome = %+v, err=%v", outcome.result, outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("headless invocation did not return after encoding")
	}
	select {
	case <-queuedStarted:
	case <-time.After(time.Second):
		t.Fatal("queued turn was not dispatched after headless release")
	}

	deadline := time.Now().Add(time.Second)
	for {
		application.mu.Lock()
		processing = application.processing
		application.mu.Unlock()
		if !processing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queued turn did not release foreground ownership")
		}
		time.Sleep(time.Millisecond)
	}
	if calls := mock.Calls(); len(calls) != 2 || calls[1].Message != "queued interactive" {
		t.Fatalf("model calls after handoff = %+v", calls)
	}
}

func assertHeadlessRunReleased(t *testing.T, application *App, rootContext context.Context, previousPresenter agentPresenter, wantDirect bool) {
	t.Helper()
	if err := rootContext.Err(); err != nil {
		t.Fatalf("headless invocation cancelled App infrastructure: %v", err)
	}
	if got := application.currentPresenter(); got != previousPresenter {
		t.Fatalf("headless invocation did not restore presenter: got %T, want %T", got, previousPresenter)
	}
	application.mu.Lock()
	running := application.running
	processing := application.processing
	active := application.headlessRunActive
	direct := application.headlessDirect
	application.mu.Unlock()
	application.processingMu.Lock()
	hasCancelOwner := application.processingCancel != nil
	application.processingMu.Unlock()
	if running || processing || active || hasCancelOwner || direct != wantDirect {
		t.Fatalf("headless lifecycle leaked state: running=%v processing=%v active=%v cancel=%v direct=%v (want %v)",
			running, processing, active, hasCancelOwner, direct, wantDirect)
	}
}

type gatedHeadlessWriter struct {
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (w *gatedHeadlessWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(p), nil
}
