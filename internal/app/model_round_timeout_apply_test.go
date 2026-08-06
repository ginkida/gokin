package app

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gokin/internal/agent"
	"gokin/internal/chat"
	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestApplyModelRoundTimeoutReportsSessionOnlyWhenSaveFails(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte("tools:\n  model_round_timeout: 14m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	// Replace the explicit config file with a directory. The already-loaded
	// config retains this exact savePath, making the next atomic write fail
	// deterministically without relying on platform-specific permission bits.
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(configPath, 0o700); err != nil {
		t.Fatal(err)
	}

	executor := tools.NewExecutor(tools.NewRegistry(), nil, time.Minute)
	application := &App{config: cfg, executor: executor}
	candidate := application.GetConfig()
	candidate.Tools.ModelRoundTimeout = 25 * time.Minute
	persistedOK, err := application.ApplyModelRoundTimeout(candidate)
	if err != nil {
		t.Fatalf("session-only runtime apply returned fatal error: %v", err)
	}
	if persistedOK {
		t.Fatal("failed config write reported durable success")
	}
	if got := application.GetConfig().Tools.ModelRoundTimeout; got != 25*time.Minute {
		t.Fatalf("session-only config timeout = %v, want 25m", got)
	}
	if got := executor.ModelRoundTimeout(); got != 25*time.Minute {
		t.Fatalf("session-only executor timeout = %v, want 25m", got)
	}
}

func TestApplyModelRoundTimeoutCommitsWithoutRebuildingClient(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	modelClient := testkit.NewMockClient()
	registry := tools.NewRegistry()
	executor := tools.NewExecutor(registry, modelClient, time.Minute)
	runner := agent.NewRunner(context.Background(), modelClient, registry, t.TempDir())
	planner := agent.NewTreePlanner(nil, nil, nil, modelClient)
	runner.SetTreePlanner(planner)
	meta := agent.NewMetaAgent(context.Background(), runner, nil, nil, nil, nil)
	contextManager := appcontext.NewContextManager(
		context.Background(), chat.NewSession(), modelClient, &cfg.Context)
	t.Cleanup(contextManager.Close)
	sessionMemory := appcontext.NewSessionMemoryManager(t.TempDir(), appcontext.DefaultSessionMemoryConfig())
	application := &App{
		ctx:            context.Background(),
		config:         cfg,
		client:         modelClient,
		executor:       executor,
		agentRunner:    runner,
		treePlanner:    planner,
		metaAgent:      meta,
		contextManager: contextManager,
		sessionMemory:  sessionMemory,
	}

	candidate := application.GetConfig()
	candidate.Tools.ModelRoundTimeout = 23 * time.Minute
	persistedOK, err := application.ApplyModelRoundTimeout(candidate)
	if err != nil {
		t.Fatalf("ApplyModelRoundTimeout: %v", err)
	}
	if !persistedOK {
		t.Fatal("successful config write reported session-only apply")
	}
	if application.client != modelClient {
		t.Fatal("timeout-only commit replaced the model client")
	}
	if got := executor.ModelRoundTimeout(); got != 23*time.Minute {
		t.Fatalf("executor timeout = %v, want 23m", got)
	}
	if got := runner.ModelRoundTimeout(); got != 23*time.Minute {
		t.Fatalf("runner timeout = %v, want 23m", got)
	}
	if got := meta.StuckThreshold(); got != 24*time.Minute {
		t.Fatalf("meta-agent stuck threshold = %v, want 24m", got)
	}
	if got := contextManager.ModelRoundTimeout(); got != 23*time.Minute {
		t.Fatalf("context compaction timeout = %v, want 23m", got)
	}
	if got := sessionMemory.ModelRoundTimeout(); got != 23*time.Minute {
		t.Fatalf("session-memory timeout = %v, want 23m", got)
	}
	if got := planner.PlanningTimeout(); got != 23*time.Minute {
		t.Fatalf("inherited planning timeout = %v, want 23m", got)
	}
	if got := application.GetConfig().Tools.ModelRoundTimeout; got != 23*time.Minute {
		t.Fatalf("authoritative config timeout = %v, want 23m", got)
	}
	if revision, tracked := candidate.SnapshotRevision(); !tracked || revision != 0 {
		t.Fatalf("partial candidate revision = %d tracked=%v, want original 0/true", revision, tracked)
	}
	// A narrow commit must not bless every unrelated field in the old candidate
	// as current. Reusing it for a full apply must conflict instead of silently
	// overwriting state committed after the snapshot was taken.
	candidate.DoneGate.Enabled = !candidate.DoneGate.Enabled
	if err := application.ApplyConfig(candidate); err == nil {
		t.Fatal("reused partial candidate was accepted as a fresh full config")
	}

	persisted, err := config.Load()
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	if got := persisted.Tools.ModelRoundTimeout; got != 23*time.Minute {
		t.Fatalf("persisted timeout = %v, want 23m", got)
	}
}

func TestApplyModelRoundTimeoutPreservesExplicitPlanningOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Plan.PlanningTimeout = 2 * time.Minute
	planner := agent.NewTreePlanner(nil, nil, nil, testkit.NewMockClient())
	planner.SetPlanningTimeout(cfg.Plan.PlanningTimeout)
	application := &App{config: cfg, treePlanner: planner}

	candidate := application.GetConfig()
	candidate.Tools.ModelRoundTimeout = 25 * time.Minute
	if _, err := application.ApplyModelRoundTimeout(candidate); err != nil {
		t.Fatalf("ApplyModelRoundTimeout: %v", err)
	}
	if got := planner.PlanningTimeout(); got != 2*time.Minute {
		t.Fatalf("explicit planning timeout = %v, want 2m", got)
	}
}

func TestApplyModelRoundTimeoutPersistsWinningProjectOverride(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	projectConfigDir := filepath.Join(project, ".gokin")
	if err := os.MkdirAll(projectConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(root, "user.yaml")
	if err := os.WriteFile(globalPath, []byte("tools:\n  model_round_timeout: 14m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(projectConfigDir, "config.yaml")
	if err := os.WriteFile(projectPath, []byte("tools:\n  model_round_timeout: 5m\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	cfg, err := config.LoadFrom(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	application := &App{config: cfg}
	candidate := application.GetConfig()
	candidate.Tools.ModelRoundTimeout = 26 * time.Minute
	persisted, err := application.ApplyModelRoundTimeout(candidate)
	if err != nil || !persisted {
		t.Fatalf("ApplyModelRoundTimeout() = persisted %v, err %v", persisted, err)
	}

	reloaded, err := config.LoadFrom(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Tools.ModelRoundTimeout; got != 26*time.Minute {
		t.Fatalf("reloaded project timeout = %v, want 26m", got)
	}
}

func TestApplyModelRoundTimeoutMergesIntoNewerConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	application := &App{config: cfg}

	staleTimeout := application.GetConfig()
	staleTimeout.Tools.ModelRoundTimeout = 20 * time.Minute

	newerUI := application.GetConfig()
	newerUI.UI.ReducedMotion = true
	if err := application.ApplyUIConfigForSetting(newerUI, "reducedmotion"); err != nil {
		t.Fatalf("apply newer UI config: %v", err)
	}
	if _, err := application.ApplyModelRoundTimeout(staleTimeout); err != nil {
		t.Fatalf("apply stale timeout candidate: %v", err)
	}

	got := application.GetConfig()
	if !got.UI.ReducedMotion {
		t.Fatal("timeout-only commit overwrote newer reduced-motion setting")
	}
	if got.Tools.ModelRoundTimeout != 20*time.Minute {
		t.Fatalf("timeout-only commit lost its owned field: %v", got.Tools.ModelRoundTimeout)
	}
	if application.configRevision != 2 {
		t.Fatalf("config revision = %d, want 2 commits", application.configRevision)
	}
}

func TestApplyModelRoundTimeoutNormalizesDefaultAndRejectsNil(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	application := &App{config: config.DefaultConfig()}
	if _, err := application.ApplyModelRoundTimeout(nil); err == nil {
		t.Fatal("nil timeout config was accepted")
	}
	candidate := application.GetConfig()
	candidate.Tools.ModelRoundTimeout = 0
	if _, err := application.ApplyModelRoundTimeout(candidate); err != nil {
		t.Fatal(err)
	}
	if got := application.GetConfig().Tools.ModelRoundTimeout; got != config.DefaultModelRoundTimeout {
		t.Fatalf("zero timeout normalized to %v, want %v", got, config.DefaultModelRoundTimeout)
	}
}

func TestApplyModelRoundTimeoutConcurrentReadersAndWriters(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	registry := tools.NewRegistry()
	modelClient := testkit.NewMockClient()
	executor := tools.NewExecutor(registry, modelClient, time.Minute)
	runner := agent.NewRunner(context.Background(), modelClient, registry, t.TempDir())
	contextManager := appcontext.NewContextManager(
		context.Background(), chat.NewSession(), modelClient, &config.ContextConfig{})
	t.Cleanup(contextManager.Close)
	sessionMemory := appcontext.NewSessionMemoryManager(t.TempDir(), appcontext.DefaultSessionMemoryConfig())
	application := &App{
		config:         config.DefaultConfig(),
		executor:       executor,
		agentRunner:    runner,
		contextManager: contextManager,
		sessionMemory:  sessionMemory,
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for n := 0; n < 20; n++ {
				if worker%2 == 0 {
					candidate := application.GetConfig()
					candidate.Tools.ModelRoundTimeout = time.Duration(15+n) * time.Minute
					if _, err := application.ApplyModelRoundTimeout(candidate); err != nil {
						t.Errorf("apply timeout: %v", err)
						return
					}
					continue
				}
				_ = application.GetConfig().Tools.ModelRoundTimeout
				_ = executor.ModelRoundTimeout()
				_ = runner.ModelRoundTimeout()
				_ = contextManager.ModelRoundTimeout()
				_ = sessionMemory.ModelRoundTimeout()
			}
		}(i)
	}
	wg.Wait()
}
