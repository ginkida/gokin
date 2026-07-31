package app

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"gokin/internal/config"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestBuilderNonInteractiveSkipsAllowedDirsPrompt(t *testing.T) {
	b := NewBuilderWithOptions(&config.Config{}, t.TempDir(), BuildOptions{NonInteractive: true})
	originalStdin := os.Stdin
	os.Stdin = nil
	t.Cleanup(func() { os.Stdin = originalStdin })

	stdout := captureBuilderStdout(t, func() {
		if err := b.checkAllowedDirs(); err != nil {
			t.Fatalf("checkAllowedDirs() error = %v", err)
		}
	})
	if stdout != "" {
		t.Fatalf("non-interactive allowed-dir check wrote stdout: %q", stdout)
	}
	if got := b.cfg.Tools.AllowedDirs; len(got) != 0 {
		t.Fatalf("non-interactive allowed-dir check mutated config: %v", got)
	}
}

func TestBuilderNonInteractiveMissingOllamaModelDoesNotPromptOrPull(t *testing.T) {
	b := NewBuilderWithOptions(&config.Config{}, t.TempDir(), BuildOptions{NonInteractive: true})

	// Any stdin read or PullModel call would panic: the non-interactive branch
	// must return before touching either dependency.
	originalStdin := os.Stdin
	os.Stdin = nil
	t.Cleanup(func() { os.Stdin = originalStdin })

	var gotErr error
	stdout := captureBuilderStdout(t, func() {
		gotErr = b.promptModelPull(nil, "glm-5.2")
	})
	if stdout != "" {
		t.Fatalf("non-interactive missing-model check wrote stdout: %q", stdout)
	}
	if gotErr == nil {
		t.Fatal("non-interactive missing-model check unexpectedly succeeded")
	}
	if got := gotErr.Error(); !strings.Contains(got, "ollama pull glm-5.2") {
		t.Fatalf("error is not actionable: %q", got)
	}
}

func TestBuilderWiresDontAskIntoSharedPermissionManager(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Permission.DontAsk = true
	builder := NewBuilderWithOptions(cfg, t.TempDir(), BuildOptions{NonInteractive: true})
	builder.executor = tools.NewExecutor(tools.NewRegistry(), nil, time.Second)
	builder.configDirErr = errors.New("disable persistence in unit test")

	if err := builder.initManagers(); err != nil {
		t.Fatal(err)
	}
	if builder.permManager == nil || !builder.permManager.IsDontAsk() {
		t.Fatalf("permission manager = %#v, want dontAsk enabled", builder.permManager)
	}
}

func TestBareBuilderSkipsProjectAndMemoryDiscovery(t *testing.T) {
	workDir := t.TempDir()
	for _, name := range []string{"CLAUDE.md", "GOKIN.md"} {
		if err := os.WriteFile(workDir+"/"+name, []byte("DISCOVERY_MARKER"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.DefaultConfig()
	cfg.Bare = true
	cfg.Session.Enabled = false
	builder := NewBuilderWithOptions(cfg, workDir, BuildOptions{
		NonInteractive: true,
		Bare:           true,
	})
	builder.mainClient = testkit.NewMockClient()
	builder.configDir = t.TempDir()
	t.Cleanup(builder.cancel)

	if err := builder.initSession(); err != nil {
		t.Fatal(err)
	}
	if builder.projectMemory != nil || builder.sessionMemory != nil ||
		builder.workingMemory != nil || builder.contextPredictor != nil {
		t.Fatalf(
			"bare discovery initialized optional state: project=%v session=%v working=%v predictor=%v",
			builder.projectMemory, builder.sessionMemory, builder.workingMemory, builder.contextPredictor,
		)
	}
	if got := builder.promptBuilder.Build(); strings.Contains(got, "DISCOVERY_MARKER") {
		t.Fatalf("bare prompt loaded a project instruction file:\n%s", got)
	}
}

func TestBareBuilderManagersStayMinimal(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Bare = true
	cfg.Session.Enabled = false
	builder := NewBuilderWithOptions(cfg, t.TempDir(), BuildOptions{
		NonInteractive: true,
		Bare:           true,
	})
	builder.mainClient = testkit.NewMockClient()
	builder.configDirErr = errors.New("disable persistence in unit test")
	t.Cleanup(builder.cancel)

	if err := builder.initTools(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(builder.registry.Names(), ","); got != "bash,edit,read" {
		t.Fatalf("bare builder registry = %q, want bash,edit,read", got)
	}
	if err := builder.initManagers(); err != nil {
		t.Fatal(err)
	}
	if builder.permManager == nil || builder.planManager == nil ||
		builder.taskManager == nil || builder.commandHandler == nil || builder.agentRunner == nil {
		t.Fatal("bare builder did not initialize required compatibility managers")
	}
	if builder.taskRouter != nil || builder.taskOrchestrator != nil ||
		builder.coordinator != nil || builder.metaAgent != nil ||
		builder.agentTypeRegistry != nil || builder.loopManager != nil {
		t.Fatal("bare builder initialized routing, discovery, or background agent managers")
	}
}

func TestBuilderDerivesBareOptionFromRuntimeConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Bare = true
	builder := NewBuilderWithOptions(cfg, t.TempDir(), BuildOptions{})
	t.Cleanup(builder.cancel)
	if !builder.options.Bare {
		t.Fatal("config Bare marker did not select bare builder path")
	}
}

func captureBuilderStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	originalStdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = originalStdout
		_ = r.Close()
		_ = w.Close()
	})

	fn()
	os.Stdout = originalStdout
	if err := w.Close(); err != nil {
		t.Fatalf("close stdout capture: %v", err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout capture: %v", err)
	}
	return string(b)
}
