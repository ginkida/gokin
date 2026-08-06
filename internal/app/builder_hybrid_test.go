package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/config"
	"gokin/internal/repl"
	"gokin/internal/tools"
)

type fakeHybridRuntime struct {
	closed bool
}

func (f *fakeHybridRuntime) Execute(context.Context, string) (repl.Result, error) {
	return repl.Result{Generation: 1, Value: "42"}, nil
}

func (f *fakeHybridRuntime) Reset(context.Context) error { return nil }
func (f *fakeHybridRuntime) Stats() repl.Stats           { return repl.Stats{Generation: 1} }

func (f *fakeHybridRuntime) SetCallHandler(repl.CallHandler) {}

func (f *fakeHybridRuntime) Close() error {
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

func TestBuilderHybridAutoWiresProbedRuntime(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	fake := &fakeHybridRuntime{}
	builder.replDetector = func(context.Context, string) repl.Availability {
		return repl.Availability{Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec}
	}
	builder.replFactory = func(opts repl.Options) (hybridRuntime, error) {
		if opts.Backend != repl.BackendSandboxExec || opts.WorkDir != builder.workDir {
			t.Fatalf("runtime options = %+v", opts)
		}
		return fake, nil
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	registered, ok := builder.registry.Get("repl_exec")
	if !ok || builder.replManager != fake {
		t.Fatalf("repl tool/runtime not wired: tool=%T ok=%v runtime=%T", registered, ok, builder.replManager)
	}
	if _, ok := builder.registry.Get("harness"); !ok || builder.harnessStore == nil {
		t.Fatalf("continual harness not wired: registered=%v store=%T", ok, builder.harnessStore)
	}
	result, err := registered.Execute(t.Context(), map[string]any{"code": "6 * 7"})
	if err != nil || !result.Success || !strings.Contains(result.Content, "42") {
		t.Fatalf("wired tool result=%+v err=%v", result, err)
	}
}

func TestBuilderHybridAutoUnregistersUnavailableCapability(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	builder.replDetector = func(context.Context, string) repl.Availability {
		return repl.Availability{Reason: "sandbox denied"}
	}
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	if _, ok := builder.registry.Get("repl_exec"); ok {
		t.Fatal("auto fallback still advertises repl_exec")
	}
	if _, ok := builder.registry.Get("harness"); ok {
		t.Fatal("auto fallback still advertises harness")
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

func TestBuilderHybridAutoFallsBackOnCorruptHarnessState(t *testing.T) {
	builder := hybridTestBuilder(t, "auto")
	dir := filepath.Join(builder.workDir, ".gokin", "harness")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.json"), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeHybridRuntime{}
	builder.replDetector = func(context.Context, string) repl.Availability {
		return repl.Availability{Available: true, PythonPath: "/trusted/python3", Backend: repl.BackendSandboxExec}
	}
	builder.replFactory = func(repl.Options) (hybridRuntime, error) { return fake, nil }
	if err := builder.initHybridEngine(); err != nil {
		t.Fatal(err)
	}
	if !fake.closed {
		t.Fatal("auto fallback leaked initialized REPL runtime")
	}
	for _, name := range []string{"repl_exec", "harness"} {
		if _, ok := builder.registry.Get(name); ok {
			t.Fatalf("auto fallback retained %s", name)
		}
	}
}
