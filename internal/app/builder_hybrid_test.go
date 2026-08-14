package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/config"
	"gokin/internal/harness"
	"gokin/internal/repl"
	"gokin/internal/tools"
)

type fakeHybridRuntime struct {
	closed     bool
	closeCalls atomic.Int32
	executions atomic.Int32
	handler    repl.CallHandler
}

func (f *fakeHybridRuntime) Execute(context.Context, string) (repl.Result, error) {
	f.executions.Add(1)
	return repl.Result{Generation: 1, Value: "42"}, nil
}

func (f *fakeHybridRuntime) Reset(context.Context) error { return nil }
func (f *fakeHybridRuntime) Stats() repl.Stats           { return repl.Stats{Generation: 1} }

func (f *fakeHybridRuntime) SetCallHandler(handler repl.CallHandler) { f.handler = handler }

func (f *fakeHybridRuntime) Close() error {
	f.closeCalls.Add(1)
	f.closed = true
	return nil
}

func hybridTestBuilder(t *testing.T, mode string) *Builder {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Engine.Mode = mode
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Builder{
		cfg: cfg, workDir: t.TempDir(), ctx: ctx, cancel: cancel,
		registry: tools.DefaultRegistry(t.TempDir()),
	}
}

func TestBuilderHybridAutoDefersWorkerUntilREPLExecution(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	fake := &fakeHybridRuntime{}
	var detectorCalls atomic.Int32
	var factoryCalls atomic.Int32
	builder.replDetector = func(context.Context, string) repl.Availability {
		detectorCalls.Add(1)
		return repl.Availability{Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec}
	}
	builder.replFactory = func(opts repl.Options) (hybridRuntime, error) {
		factoryCalls.Add(1)
		if opts.Backend != repl.BackendSandboxExec || opts.WorkDir != builder.workDir {
			t.Fatalf("runtime options = %+v", opts)
		}
		return fake, nil
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	if detectorCalls.Load() != 0 || factoryCalls.Load() != 0 || builder.replManager != nil {
		t.Fatalf("auto startup was eager: detector=%d factory=%d manager=%T",
			detectorCalls.Load(), factoryCalls.Load(), builder.replManager)
	}
	application := builder.assembleApp()
	application.executor = tools.NewExecutor(builder.registry, nil, builder.cfg.Tools.Timeout)
	var promptRefreshes atomic.Int32
	application.deferredHybrid.SetPromptChangedCallback(func() { promptRefreshes.Add(1) })
	application.deferredHybrid.SetCallHandler(application.handleRLMCall)
	if detectorCalls.Load() != 0 || factoryCalls.Load() != 0 {
		t.Fatalf("ordinary request activated hybrid runtime: detector=%d factory=%d",
			detectorCalls.Load(), factoryCalls.Load())
	}
	if schemaHasDeclaration(application.toolsForMessage("fix the auth bug"), "repl_exec") {
		t.Fatal("unactivated auto runtime was advertised for an ordinary request")
	}

	registered, ok := builder.registry.Get("repl_exec")
	if !ok || !schemaHasDeclaration(
		application.toolsForMessage("Count TODOs per package across the repository"), "repl_exec") {
		t.Fatalf("eligible lazy repl tool was not advertised: tool=%T ok=%v", registered, ok)
	}
	if detectorCalls.Load() != 0 || factoryCalls.Load() != 0 || application.deferredHybrid.isReady() {
		t.Fatalf("schema exposure started worker: detector=%d factory=%d ready=%t",
			detectorCalls.Load(), factoryCalls.Load(), application.deferredHybrid.isReady())
	}
	status, err := registered.Execute(t.Context(), map[string]any{"action": "status"})
	if err != nil || !status.Success || detectorCalls.Load() != 0 || factoryCalls.Load() != 0 {
		t.Fatalf("status started worker: result=%+v err=%v detector=%d factory=%d",
			status, err, detectorCalls.Load(), factoryCalls.Load())
	}
	reset, err := registered.Execute(t.Context(), map[string]any{"action": "reset"})
	if err != nil || !reset.Success || detectorCalls.Load() != 0 || factoryCalls.Load() != 0 {
		t.Fatalf("reset started worker: result=%+v err=%v detector=%d factory=%d",
			reset, err, detectorCalls.Load(), factoryCalls.Load())
	}
	result, err := registered.Execute(t.Context(), map[string]any{"code": "6 * 7"})
	if err != nil || !result.Success || !strings.Contains(result.Content, "42") {
		t.Fatalf("wired tool result=%+v err=%v", result, err)
	}
	deferredManager, deferredStore := application.deferredHybrid.components()
	if deferredManager != fake || deferredStore != nil {
		t.Fatalf("first execute did not publish components: manager=%T store=%T", deferredManager, deferredStore)
	}
	if promptRefreshes.Load() != 0 || fake.handler == nil {
		t.Fatalf("lazy wiring prompt_refreshes=%d handler_set=%t",
			promptRefreshes.Load(), fake.handler != nil)
	}
	if _, ok := builder.registry.Get("harness"); !ok {
		t.Fatal("lazy harness disappeared before its first use")
	}
	harnessResult, err := fake.handler(t.Context(), repl.Call{
		Method: "harness.prompt_create",
		Params: map[string]any{"text": "keep evidence compact"},
	})
	_, deferredStore = application.deferredHybrid.components()
	if err != nil || harnessResult == nil || deferredStore == nil || promptRefreshes.Load() != 1 {
		t.Fatalf("lazy harness result=%+v err=%v store=%T prompt_refreshes=%d",
			harnessResult, err, deferredStore, promptRefreshes.Load())
	}
	if detectorCalls.Load() != 1 || factoryCalls.Load() != 1 {
		t.Fatalf("ready runtime initialized more than once: detector=%d factory=%d",
			detectorCalls.Load(), factoryCalls.Load())
	}
	result, err = registered.Execute(t.Context(), map[string]any{"code": "7 * 6"})
	if err != nil || !result.Success || promptRefreshes.Load() != 1 {
		t.Fatalf("second execute result=%+v err=%v prompt_refreshes=%d",
			result, err, promptRefreshes.Load())
	}
}

func TestBuilderHybridAutoHidesUnavailableCapabilityAfterFirstExecution(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	var detectorCalls atomic.Int32
	builder.replDetector = func(context.Context, string) repl.Availability {
		detectorCalls.Add(1)
		return repl.Availability{Reason: "sandbox denied"}
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	if detectorCalls.Load() != 0 {
		t.Fatal("auto startup probed an unavailable runtime eagerly")
	}
	application := builder.assembleApp()
	registered, ok := builder.registry.Get("repl_exec")
	if !ok || !schemaHasDeclaration(
		application.toolsForMessage("Count TODOs across every repository file"), "repl_exec") {
		t.Fatal("eligible lazy REPL was hidden before its first availability attempt")
	}
	result, err := registered.Execute(t.Context(), map[string]any{"code": "1 + 1"})
	if err != nil || result.Success || !strings.Contains(result.Error, "sandbox denied") {
		t.Fatalf("unavailable lazy result=%+v err=%v", result, err)
	}
	if detectorCalls.Load() != 1 {
		t.Fatalf("first execute detector calls = %d, want 1", detectorCalls.Load())
	}
	if schemaHasDeclaration(
		application.toolsForMessage("Count TODOs across every repository file"), "repl_exec") {
		t.Fatal("auto fallback still advertised repl_exec after conclusive failure")
	}
	if !application.RuntimeREPLCapabilityEnabled() {
		t.Fatal("unavailable auto runtime was incorrectly reported as capability-denied")
	}
	result, err = registered.Execute(t.Context(), map[string]any{"code": "2 + 2"})
	if err != nil || result.Success {
		t.Fatalf("cached unavailable result=%+v err=%v", result, err)
	}
	status, err := registered.Execute(t.Context(), map[string]any{"action": "status"})
	if err != nil || !status.Success || !strings.Contains(status.Content, "sandbox denied") {
		t.Fatalf("failed lazy status=%+v err=%v", status, err)
	}
	reset, err := registered.Execute(t.Context(), map[string]any{"action": "reset"})
	if err != nil || reset.Success || !strings.Contains(reset.Error, "sandbox denied") {
		t.Fatalf("failed lazy reset=%+v err=%v", reset, err)
	}
	if detectorCalls.Load() != 1 {
		t.Fatalf("conclusive fallback retried detector: calls=%d", detectorCalls.Load())
	}
}

func TestBuilderHybridAutoPreflightHidesImpossibleRuntimeWithoutProbe(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	builder.replPreflight = func(string) repl.Availability {
		return repl.Availability{Reason: "python3 was not found in PATH"}
	}
	builder.replDetector = func(context.Context, string) repl.Availability {
		t.Fatal("failed process-free preflight still performed a secure probe")
		return repl.Availability{}
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	if builder.deferredHybrid == nil || builder.deferredHybrid.canAdvertise() {
		t.Fatal("failed process-free preflight did not install a hidden failed capability")
	}
	application := builder.assembleApp()
	if schemaHasDeclaration(
		application.toolsForMessage("Count TODOs across every repository file"), "repl_exec") {
		t.Fatal("failed process-free preflight advertised repl_exec")
	}
	for _, name := range []string{"repl_exec", "harness"} {
		if _, ok := builder.registry.Get(name); !ok {
			t.Fatalf("failed process-free preflight forgot known capability %s", name)
		}
	}
	if err := application.ConfigureToolCapability([]string{"repl_exec"}, nil); err != nil {
		t.Fatalf("explicit unavailable capability was misdiagnosed as unknown: %v", err)
	}
	if schemaHasDeclaration(
		application.toolsForMessage("Count TODOs across every repository file"), "repl_exec") {
		t.Fatal("explicit ceiling exposed a preflight-failed repl_exec")
	}
	registered, _ := builder.registry.Get("repl_exec")
	result, err := registered.Execute(t.Context(), map[string]any{"code": "1 + 1"})
	if err != nil || result.Success || !strings.Contains(result.Error, "python3 was not found") {
		t.Fatalf("preflight-failed invocation result=%+v err=%v", result, err)
	}
}

func TestBuilderRequiredHybridFailsClosed(t *testing.T) {
	builder := hybridTestBuilder(t, "hybrid")
	builder.replDetector = func(context.Context, string) repl.Availability {
		return repl.Availability{Reason: "no secure backend"}
	}
	err := builder.initHybridEngine()
	if !errors.Is(err, repl.ErrUnavailable) || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("required hybrid error = %v", err)
	}
	if _, ok := builder.registry.Get("repl_exec"); ok {
		t.Fatal("failed required hybrid retained repl_exec")
	}
	if _, ok := builder.registry.Get("harness"); ok {
		t.Fatal("failed required hybrid retained harness")
	}
}

func TestBuilderRequiredHybridSkipsProbeWhenCapabilityDeniesREPL(t *testing.T) {
	builder := hybridTestBuilder(t, "hybrid")
	builder.options.StartupToolCapabilityAllowed = []string{"read"}
	builder.replDetector = func(context.Context, string) repl.Availability {
		t.Fatal("capability-denied hybrid mode performed a secure runtime probe")
		return repl.Availability{}
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"repl_exec", "harness"} {
		if _, ok := builder.registry.Get(name); ok {
			t.Fatalf("capability-denied hybrid retained %s", name)
		}
	}
	if builder.assembleApp().RuntimeREPLCapabilityEnabled() {
		t.Fatal("startup-excluded REPL reported invocation capability enabled")
	}
}

func TestBuilderRequiredHybridCanKeepHarnessWithoutPython(t *testing.T) {
	builder := hybridTestBuilder(t, "hybrid")
	builder.options.StartupToolCapabilityAllowed = []string{"harness"}
	builder.replDetector = func(context.Context, string) repl.Availability {
		t.Fatal("harness-only hybrid mode performed a secure runtime probe")
		return repl.Availability{}
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	if _, ok := builder.registry.Get("repl_exec"); ok {
		t.Fatal("harness-only capability retained repl_exec")
	}
	if builder.assembleApp().RuntimeREPLCapabilityEnabled() {
		t.Fatal("harness-only invocation reported REPL capability enabled")
	}
	registered, ok := builder.registry.Get("harness")
	if !ok || builder.harnessStore == nil {
		t.Fatalf("harness-only capability was not initialized: tool=%T store=%T", registered, builder.harnessStore)
	}
	result, err := registered.Execute(t.Context(), map[string]any{"action": "memory_list"})
	if err != nil || !result.Success {
		t.Fatalf("harness-only tool result=%+v err=%v", result, err)
	}
}

func TestBuilderRequiredHybridStillFailsWhenREPLIsAllowed(t *testing.T) {
	builder := hybridTestBuilder(t, "hybrid")
	builder.options.StartupToolCapabilityAllowed = []string{"repl_exec"}
	builder.replDetector = func(context.Context, string) repl.Availability {
		return repl.Availability{Reason: "no secure backend"}
	}
	err := builder.initHybridEngine()
	if !errors.Is(err, repl.ErrUnavailable) {
		t.Fatalf("allowed required hybrid did not fail closed: %v", err)
	}
}

func TestBuilderRequiredHybridCanRunREPLWithoutHarness(t *testing.T) {
	builder := hybridTestBuilder(t, "hybrid")
	builder.options.StartupToolCapabilityAllowed = []string{"repl_exec"}
	fake := &fakeHybridRuntime{}
	builder.replDetector = func(context.Context, string) repl.Availability {
		return repl.Availability{
			Available:  true,
			PythonPath: "/trusted/python3",
			Backend:    repl.BackendSandboxExec,
		}
	}
	builder.replFactory = func(repl.Options) (hybridRuntime, error) { return fake, nil }

	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	registered, ok := builder.registry.Get("repl_exec")
	if !ok || builder.replManager != fake {
		t.Fatalf("REPL-only capability was not initialized: tool=%T manager=%T", registered, builder.replManager)
	}
	if _, ok := builder.registry.Get("harness"); ok || builder.harnessStore != nil {
		t.Fatalf("REPL-only capability initialized harness: registered=%t store=%T", ok, builder.harnessStore)
	}
	if !builder.assembleApp().RuntimeREPLCapabilityEnabled() {
		t.Fatal("REPL-only invocation reported REPL capability disabled")
	}
	result, err := registered.Execute(t.Context(), map[string]any{"code": "6 * 7"})
	if err != nil || !result.Success || !strings.Contains(result.Content, "42") {
		t.Fatalf("REPL-only tool result=%+v err=%v", result, err)
	}
}

func TestBuilderToolsModeSkipsProbeAndUnregistersREPL(t *testing.T) {
	builder := hybridTestBuilder(t, "tools")
	builder.replDetector = func(context.Context, string) repl.Availability {
		t.Fatal("tools mode performed runtime probe")
		return repl.Availability{}
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	if _, ok := builder.registry.Get("repl_exec"); ok {
		t.Fatal("tools mode retained repl_exec")
	}
	if _, ok := builder.registry.Get("harness"); ok {
		t.Fatal("tools mode retained harness")
	}
}

func TestBuilderHybridAutoKeepsREPLWhenHarnessStateIsCorrupt(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	dir := filepath.Join(builder.workDir, ".gokin", "harness")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.json"), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeHybridRuntime{}
	var detectorCalls atomic.Int32
	var factoryCalls atomic.Int32
	builder.replDetector = func(context.Context, string) repl.Availability {
		detectorCalls.Add(1)
		return repl.Availability{Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec}
	}
	builder.replFactory = func(repl.Options) (hybridRuntime, error) {
		factoryCalls.Add(1)
		return fake, nil
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	if fake.closed {
		t.Fatal("deferred runtime was created during auto startup")
	}
	application := builder.assembleApp()
	application.executor = tools.NewExecutor(builder.registry, nil, builder.cfg.Tools.Timeout)
	registered, ok := builder.registry.Get("repl_exec")
	if !ok {
		t.Fatal("lazy repl_exec is not registered")
	}
	result, err := registered.Execute(t.Context(), map[string]any{"code": "1 + 1"})
	if err != nil || !result.Success {
		t.Fatalf("corrupt harness activation result=%+v err=%v", result, err)
	}
	if fake.closed {
		t.Fatal("optional corrupt harness closed the working REPL runtime")
	}
	if !schemaHasDeclaration(
		application.toolsForMessage("Count TODOs across every repository file"), "repl_exec") {
		t.Fatal("optional corrupt harness hid the working repl_exec")
	}
	if _, err := application.handleRLMCall(t.Context(), repl.Call{
		Method: "harness.memory_list",
	}); err == nil ||
		!strings.Contains(err.Error(), "decode harness memory") {
		t.Fatalf("corrupt optional harness callback error = %v", err)
	}
	if _, err := application.deferredHybrid.ensureHarness(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "decode harness memory") {
		t.Fatalf("cached corrupt optional harness error = %v", err)
	}
	result, err = registered.Execute(t.Context(), map[string]any{"code": "2 + 2"})
	if err != nil || !result.Success || fake.closed || fake.executions.Load() != 2 {
		t.Fatalf("REPL after optional harness failure result=%+v err=%v closed=%t executions=%d",
			result, err, fake.closed, fake.executions.Load())
	}
}

func TestBuilderRequiredHybridFailsClosedOnCorruptHarnessState(t *testing.T) {
	builder := hybridTestBuilder(t, "hybrid")
	dir := filepath.Join(builder.workDir, ".gokin", "harness")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.json"), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeHybridRuntime{}
	var detectorCalls atomic.Int32
	var factoryCalls atomic.Int32
	builder.replDetector = func(context.Context, string) repl.Availability {
		detectorCalls.Add(1)
		return repl.Availability{Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec}
	}
	builder.replFactory = func(repl.Options) (hybridRuntime, error) {
		factoryCalls.Add(1)
		return fake, nil
	}

	err := builder.initHybridEngine()
	if err == nil || !strings.Contains(err.Error(), "decode harness memory") {
		t.Fatalf("required hybrid corrupt harness error = %v", err)
	}
	if detectorCalls.Load() != 0 || factoryCalls.Load() != 0 || fake.closed {
		t.Fatalf("required hybrid opened a worker before process-free harness validation: detector=%d factory=%d closed=%t",
			detectorCalls.Load(), factoryCalls.Load(), fake.closed)
	}
	for _, name := range []string{"repl_exec", "harness"} {
		if _, ok := builder.registry.Get(name); ok {
			t.Fatalf("failed required hybrid retained %s", name)
		}
	}
}

func TestDeferredHybridCancelledProbeRemainsRetryable(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	fake := &fakeHybridRuntime{}
	started := make(chan struct{})
	var once sync.Once
	var detectorCalls atomic.Int32
	builder.replDetector = func(ctx context.Context, _ string) repl.Availability {
		call := detectorCalls.Add(1)
		if call == 1 {
			once.Do(func() { close(started) })
			<-ctx.Done()
			return repl.Availability{Reason: ctx.Err().Error()}
		}
		return repl.Availability{Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec}
	}
	builder.replFactory = func(repl.Options) (hybridRuntime, error) { return fake, nil }
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	application := builder.assembleApp()
	registered, ok := builder.registry.Get("repl_exec")
	if !ok {
		t.Fatal("lazy repl_exec is not registered")
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan tools.ToolResult, 1)
	go func() {
		result, _ := registered.Execute(ctx, map[string]any{"code": "1 + 1"})
		done <- result
	}()
	<-started
	cancel()
	if result := <-done; result.Success {
		t.Fatalf("cancelled activation succeeded: %+v", result)
	}
	if application.deferredHybrid.isReady() {
		t.Fatal("cancelled probe published a ready runtime")
	}

	result, err := registered.Execute(t.Context(), map[string]any{"code": "1 + 1"})
	if err != nil || !result.Success {
		t.Fatalf("retry execute result=%+v err=%v", result, err)
	}
	deferredManager, _ := application.deferredHybrid.components()
	if detectorCalls.Load() != 2 || !application.deferredHybrid.isReady() || deferredManager != fake {
		t.Fatalf("cancelled probe was not retried successfully: calls=%d ready=%t manager=%T",
			detectorCalls.Load(), application.deferredHybrid.isReady(), deferredManager)
	}
}

func TestDeferredHybridCancellationAfterSuccessfulProbeDoesNotPublishClosedManager(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	first := &fakeHybridRuntime{}
	second := &fakeHybridRuntime{}
	var calls atomic.Int32
	var cancel context.CancelFunc
	builder.replOpener = func(context.Context, repl.Options) (hybridRuntime, repl.Availability) {
		if calls.Add(1) == 1 {
			cancel()
			return first, repl.Availability{
				Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec,
			}
		}
		return second, repl.Availability{
			Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec,
		}
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	application := builder.assembleApp()
	registered, ok := builder.registry.Get("repl_exec")
	if !ok {
		t.Fatal("lazy repl_exec is not registered")
	}
	ctx, cancelRequest := context.WithCancel(t.Context())
	cancel = cancelRequest
	result, err := registered.Execute(ctx, map[string]any{"code": "1 + 1"})
	if err != nil || result.Success {
		t.Fatalf("cancelled post-probe result=%+v err=%v", result, err)
	}
	if !first.closed || application.deferredHybrid.isReady() ||
		!schemaHasDeclaration(application.toolsForMessage("Count TODOs across every repository file"), "repl_exec") {
		t.Fatalf("cancelled activation published state: closed=%t ready=%t",
			first.closed, application.deferredHybrid.isReady())
	}

	result, err = registered.Execute(t.Context(), map[string]any{"code": "1 + 1"})
	if err != nil || !result.Success {
		t.Fatalf("retry execute result=%+v err=%v", result, err)
	}
	manager, _ := application.deferredHybrid.components()
	if calls.Load() != 2 || manager != second || second.closed {
		t.Fatalf("retry activation calls=%d manager=%T second_closed=%t", calls.Load(), manager, second.closed)
	}
}

func TestDeferredHybridConcurrentEligibilityInitializesOnce(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	fake := &fakeHybridRuntime{}
	var detectorCalls atomic.Int32
	var factoryCalls atomic.Int32
	builder.replDetector = func(context.Context, string) repl.Availability {
		detectorCalls.Add(1)
		return repl.Availability{Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec}
	}
	builder.replFactory = func(repl.Options) (hybridRuntime, error) {
		factoryCalls.Add(1)
		return fake, nil
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	application := builder.assembleApp()
	registered, ok := builder.registry.Get("repl_exec")
	if !ok {
		t.Fatal("lazy repl_exec is not registered")
	}
	var group sync.WaitGroup
	errors := make(chan string, 16)
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := registered.Execute(t.Context(), map[string]any{"code": "1 + 1"})
			if err != nil || !result.Success {
				errors <- fmt.Sprintf("result=%+v err=%v", result, err)
			}
		}()
	}
	group.Wait()
	close(errors)
	for failure := range errors {
		t.Error(failure)
	}
	if detectorCalls.Load() != 1 || factoryCalls.Load() != 1 || !application.deferredHybrid.isReady() {
		t.Fatalf("concurrent initialization detector=%d factory=%d ready=%t",
			detectorCalls.Load(), factoryCalls.Load(), application.deferredHybrid.isReady())
	}
}

func TestDeferredHybridStatusAndSchemaStayResponsiveDuringInitialization(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	fake := &fakeHybridRuntime{}
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	builder.replOpener = func(ctx context.Context, _ repl.Options) (hybridRuntime, repl.Availability) {
		once.Do(func() { close(started) })
		select {
		case <-ctx.Done():
			return nil, repl.Availability{Reason: ctx.Err().Error()}
		case <-release:
			return fake, repl.Availability{
				Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec,
			}
		}
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	application := builder.assembleApp()
	registered, ok := builder.registry.Get("repl_exec")
	if !ok {
		t.Fatal("lazy repl_exec is not registered")
	}
	executeDone := make(chan tools.ToolResult, 1)
	go func() {
		result, _ := registered.Execute(t.Context(), map[string]any{"code": "1 + 1"})
		executeDone <- result
	}()
	<-started

	statusDone := make(chan tools.ToolResult, 1)
	go func() {
		result, _ := registered.Execute(t.Context(), map[string]any{"action": "status"})
		statusDone <- result
	}()
	select {
	case result := <-statusDone:
		if !result.Success || !strings.Contains(result.Content, "kernel stopped") {
			t.Fatalf("initializing status=%+v", result)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("status blocked behind lazy sandbox initialization")
	}

	schemaDone := make(chan bool, 1)
	go func() {
		schemaDone <- schemaHasDeclaration(
			application.toolsForMessage("Count TODOs across every repository file"), "repl_exec")
	}()
	select {
	case visible := <-schemaDone:
		if !visible {
			t.Fatal("initializing lazy capability disappeared from schema")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("schema construction blocked behind lazy sandbox initialization")
	}

	close(release)
	if result := <-executeDone; !result.Success {
		t.Fatalf("released lazy execute=%+v", result)
	}
}

func TestDeferredHybridWaitingExecuteHonorsOwnCancellation(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	fake := &fakeHybridRuntime{}
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var openerCalls atomic.Int32
	builder.replOpener = func(ctx context.Context, _ repl.Options) (hybridRuntime, repl.Availability) {
		openerCalls.Add(1)
		once.Do(func() { close(started) })
		select {
		case <-ctx.Done():
			return nil, repl.Availability{Reason: ctx.Err().Error()}
		case <-release:
			return fake, repl.Availability{
				Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec,
			}
		}
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	registered, _ := builder.registry.Get("repl_exec")
	firstDone := make(chan tools.ToolResult, 1)
	go func() {
		result, _ := registered.Execute(t.Context(), map[string]any{"code": "1 + 1"})
		firstDone <- result
	}()
	<-started

	waitCtx, cancel := context.WithCancel(t.Context())
	waitDone := make(chan tools.ToolResult, 1)
	go func() {
		result, _ := registered.Execute(waitCtx, map[string]any{"code": "2 + 2"})
		waitDone <- result
	}()
	cancel()
	select {
	case result := <-waitDone:
		if result.Success || !strings.Contains(result.Error, context.Canceled.Error()) {
			t.Fatalf("cancelled waiting execute=%+v", result)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("waiting execute ignored its own cancellation")
	}
	if openerCalls.Load() != 1 {
		t.Fatalf("waiting execute started another opener: %d", openerCalls.Load())
	}
	close(release)
	if result := <-firstDone; !result.Success {
		t.Fatalf("owner execute after waiter cancellation=%+v", result)
	}
}

func TestDeferredHybridCloseCancelsAndJoinsInitialization(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	started := make(chan struct{})
	openerExited := make(chan struct{})
	builder.replOpener = func(ctx context.Context, _ repl.Options) (hybridRuntime, repl.Availability) {
		close(started)
		<-ctx.Done()
		close(openerExited)
		return nil, repl.Availability{Reason: ctx.Err().Error()}
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	registered, _ := builder.registry.Get("repl_exec")
	executeDone := make(chan tools.ToolResult, 1)
	go func() {
		result, _ := registered.Execute(t.Context(), map[string]any{"code": "1 + 1"})
		executeDone <- result
	}()
	<-started

	closeDone := make(chan error, 1)
	go func() { closeDone <- builder.deferredHybrid.close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not cancel and join lazy initialization")
	}
	select {
	case <-openerExited:
	default:
		t.Fatal("close returned before lazy opener exited")
	}
	if result := <-executeDone; result.Success || builder.deferredHybrid.canAdvertise() {
		t.Fatalf("execute after close=%+v advertise=%t", result, builder.deferredHybrid.canAdvertise())
	}
}

func TestDeferredHybridStatusStaysResponsiveDuringHarnessLoad(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	fake := &fakeHybridRuntime{}
	builder.replOpener = func(context.Context, repl.Options) (hybridRuntime, repl.Availability) {
		return fake, repl.Availability{
			Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec,
		}
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	registered, _ := builder.registry.Get("repl_exec")
	if result, _ := registered.Execute(t.Context(), map[string]any{"code": "1 + 1"}); !result.Success {
		t.Fatalf("activate lazy runtime=%+v", result)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	builder.deferredHybrid.harnessLoader = func(_ context.Context, workDir string) (*harness.Store, error) {
		close(started)
		<-release
		return harness.NewStore(workDir)
	}
	harnessDone := make(chan error, 1)
	go func() {
		_, err := builder.deferredHybrid.ensureHarness(t.Context())
		harnessDone <- err
	}()
	<-started

	statusDone := make(chan tools.ToolResult, 1)
	go func() {
		result, _ := registered.Execute(t.Context(), map[string]any{"action": "status"})
		statusDone <- result
	}()
	select {
	case result := <-statusDone:
		if !result.Success {
			t.Fatalf("status during harness load=%+v", result)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("status blocked behind lazy harness load")
	}
	close(release)
	if err := <-harnessDone; err != nil {
		t.Fatalf("released harness load: %v", err)
	}
}

func TestDeferredHybridWaitingHarnessLoadHonorsOwnCancellation(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	fake := &fakeHybridRuntime{}
	builder.replOpener = func(context.Context, repl.Options) (hybridRuntime, repl.Availability) {
		return fake, repl.Availability{
			Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec,
		}
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	registered, _ := builder.registry.Get("repl_exec")
	if result, _ := registered.Execute(t.Context(), map[string]any{"code": "1 + 1"}); !result.Success {
		t.Fatalf("activate lazy runtime=%+v", result)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var loaderCalls atomic.Int32
	builder.deferredHybrid.harnessLoader = func(_ context.Context, workDir string) (*harness.Store, error) {
		loaderCalls.Add(1)
		close(started)
		<-release
		return harness.NewStore(workDir)
	}
	ownerDone := make(chan error, 1)
	go func() {
		_, err := builder.deferredHybrid.ensureHarness(t.Context())
		ownerDone <- err
	}()
	<-started

	waitCtx, cancel := context.WithCancel(t.Context())
	waitDone := make(chan error, 1)
	go func() {
		_, err := builder.deferredHybrid.ensureHarness(waitCtx)
		waitDone <- err
	}()
	cancel()
	select {
	case err := <-waitDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled harness waiter error=%v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("waiting harness load ignored its own cancellation")
	}
	if loaderCalls.Load() != 1 {
		t.Fatalf("waiting harness call started another loader: %d", loaderCalls.Load())
	}
	close(release)
	if err := <-ownerDone; err != nil {
		t.Fatalf("owner harness load: %v", err)
	}
}

func TestDeferredHybridCloseCancelsAndJoinsHarnessLoad(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	fake := &fakeHybridRuntime{}
	builder.replOpener = func(context.Context, repl.Options) (hybridRuntime, repl.Availability) {
		return fake, repl.Availability{
			Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec,
		}
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	registered, _ := builder.registry.Get("repl_exec")
	if result, _ := registered.Execute(t.Context(), map[string]any{"code": "1 + 1"}); !result.Success {
		t.Fatalf("activate lazy runtime=%+v", result)
	}

	started := make(chan struct{})
	loaderExited := make(chan struct{})
	builder.deferredHybrid.harnessLoader = func(ctx context.Context, _ string) (*harness.Store, error) {
		close(started)
		<-ctx.Done()
		close(loaderExited)
		return nil, ctx.Err()
	}
	harnessDone := make(chan error, 1)
	go func() {
		_, err := builder.deferredHybrid.ensureHarness(t.Context())
		harnessDone <- err
	}()
	<-started

	closeResults := make(chan error, 2)
	go func() { closeResults <- builder.deferredHybrid.close() }()
	go func() { closeResults <- builder.deferredHybrid.close() }()
	for range 2 {
		select {
		case err := <-closeResults:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("close did not cancel and join lazy harness load")
		}
	}
	select {
	case <-loaderExited:
	default:
		t.Fatal("close returned before lazy harness loader exited")
	}
	if err := <-harnessDone; err == nil {
		t.Fatal("harness load succeeded after close")
	}
	if !fake.closed {
		t.Fatal("close did not terminate the already-active REPL manager")
	}
	if calls := fake.closeCalls.Load(); calls != 1 {
		t.Fatalf("concurrent close terminated REPL manager %d times, want 1", calls)
	}
	if manager, store := builder.deferredHybrid.components(); manager != nil || store != nil {
		t.Fatalf("closed hybrid retained components manager=%T store=%T", manager, store)
	}
	harnessTool, ok := builder.registry.Get("harness")
	if !ok {
		t.Fatal("known harness capability disappeared from registry on close")
	}
	result, err := harnessTool.Execute(t.Context(), map[string]any{"action": "prompt_list"})
	if err != nil || result.Success || !strings.Contains(result.Error, "unavailable") {
		t.Fatalf("closed registry harness retained store: result=%+v err=%v", result, err)
	}
}

func TestDeferredHybridCancelledHarnessOwnerCanRetry(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	fake := &fakeHybridRuntime{}
	builder.replOpener = func(context.Context, repl.Options) (hybridRuntime, repl.Availability) {
		return fake, repl.Availability{
			Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec,
		}
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	registered, _ := builder.registry.Get("repl_exec")
	if result, _ := registered.Execute(t.Context(), map[string]any{"code": "1 + 1"}); !result.Success {
		t.Fatalf("activate lazy runtime=%+v", result)
	}

	var loaderCalls atomic.Int32
	started := make(chan struct{})
	builder.deferredHybrid.harnessLoader = func(ctx context.Context, workDir string) (*harness.Store, error) {
		if loaderCalls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return harness.NewStore(workDir)
	}
	firstCtx, cancel := context.WithCancel(t.Context())
	firstDone := make(chan error, 1)
	go func() {
		_, err := builder.deferredHybrid.ensureHarness(firstCtx)
		firstDone <- err
	}()
	<-started
	cancel()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled owner error=%v", err)
	}
	if _, err := builder.deferredHybrid.ensureHarness(t.Context()); err != nil {
		t.Fatalf("retry after cancelled harness owner: %v", err)
	}
	if calls := loaderCalls.Load(); calls != 2 {
		t.Fatalf("harness loader calls=%d, want cancelled attempt plus retry", calls)
	}
}

func TestDeferredHybridPrecancelledHarnessLoadDoesNotStartLoader(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	fake := &fakeHybridRuntime{}
	builder.replOpener = func(context.Context, repl.Options) (hybridRuntime, repl.Availability) {
		return fake, repl.Availability{
			Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec,
		}
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	registered, _ := builder.registry.Get("repl_exec")
	if result, _ := registered.Execute(t.Context(), map[string]any{"code": "1 + 1"}); !result.Success {
		t.Fatalf("activate lazy runtime=%+v", result)
	}
	var loaderCalls atomic.Int32
	builder.deferredHybrid.harnessLoader = func(context.Context, string) (*harness.Store, error) {
		loaderCalls.Add(1)
		return nil, errors.New("unexpected loader call")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := builder.deferredHybrid.ensureHarness(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled harness load error=%v", err)
	}
	if calls := loaderCalls.Load(); calls != 0 {
		t.Fatalf("pre-cancelled harness load called loader %d times", calls)
	}
}

func TestDeferredHybridSkipsProbeWhenInvocationCapabilityDeniesREPL(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	var detectorCalls atomic.Int32
	builder.replDetector = func(context.Context, string) repl.Availability {
		detectorCalls.Add(1)
		return repl.Availability{Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec}
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	application := builder.assembleApp()
	if !schemaHasDeclaration(
		application.toolsForMessage("Count TODOs across every repository file"), "repl_exec") {
		t.Fatal("eligible lazy schema was unavailable")
	}
	if detectorCalls.Load() != 0 {
		t.Fatalf("schema-only request triggered %d runtime probes", detectorCalls.Load())
	}
}

func TestDeferredHybridCloseIsIdempotent(t *testing.T) {
	fake := &fakeHybridRuntime{}
	d := &deferredHybridInit{attempted: true, ready: true, manager: fake}
	d.SetCallHandler(func(context.Context, repl.Call) (any, error) { return nil, nil })
	d.SetPromptChangedCallback(func() {})
	if err := d.close(); err != nil {
		t.Fatal(err)
	}
	d.SetCallHandler(func(context.Context, repl.Call) (any, error) { return "late", nil })
	d.SetPromptChangedCallback(func() {})
	d.mu.Lock()
	handler, callback := d.handler, d.onPromptChanged
	d.mu.Unlock()
	if !fake.closed || d.isReady() || fake.handler != nil || handler != nil || callback != nil {
		t.Fatalf("closed deferred runtime retained state: closed=%t ready=%t manager_handler=%v handler=%v callback=%v",
			fake.closed, d.isReady(), fake.handler != nil, handler != nil, callback != nil)
	}
	if err := d.close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestDeferredHybridCloseBeforeFirstUseDoesNotStartWorker(t *testing.T) {
	var openerCalls atomic.Int32
	d := &deferredHybridInit{
		registry: tools.NewRegistry(),
		opener: func(context.Context, repl.Options) (hybridRuntime, repl.Availability) {
			openerCalls.Add(1)
			return &fakeHybridRuntime{}, repl.Availability{Available: true}
		},
	}
	if err := d.close(); err != nil {
		t.Fatal(err)
	}
	if d.canAdvertise() || d.isReady() {
		t.Fatalf("closed unused lazy runtime remains available: advertise=%t ready=%t",
			d.canAdvertise(), d.isReady())
	}
	if _, err := d.Execute(t.Context(), "1 + 1"); !errors.Is(err, repl.ErrUnavailable) {
		t.Fatalf("closed unused execute error=%v, want ErrUnavailable", err)
	}
	if openerCalls.Load() != 0 {
		t.Fatalf("closing/executing closed lazy runtime called opener %d times", openerCalls.Load())
	}
}
