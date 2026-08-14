package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/agent"
	"gokin/internal/config"
	"gokin/internal/router"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

func TestApplyConfigSynchronizesModelRoundTimeoutAtRuntime(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.API.ActiveProvider = "glm"
	cfg.API.Backend = "glm"
	cfg.API.GLMKey = "test-key-that-is-long-enough-1234567890"
	cfg.Model.Provider = "glm"
	cfg.Model.Name = "glm-5.2"
	cfg.Tools.ModelRoundTimeout = 19 * time.Minute

	registry := tools.DefaultRegistry(".")
	executor := tools.NewExecutor(registry, nil, time.Minute)
	executor.SetModelRoundTimeout(time.Minute)
	runner := agent.NewRunner(context.Background(), nil, registry, ".")
	runner.SetModelRoundTimeout(time.Minute)
	app := &App{
		config:      cfg,
		ctx:         context.Background(),
		executor:    executor,
		agentRunner: runner,
	}

	if err := app.ApplyConfig(cfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	if got := executor.ModelRoundTimeout(); got != 19*time.Minute {
		t.Fatalf("executor model round timeout = %v, want 19m", got)
	}
	if got := runner.ModelRoundTimeout(); got != 19*time.Minute {
		t.Fatalf("agent runner model round timeout = %v, want 19m", got)
	}
}

// TestApplyConfig_NoSelfDeadlock verifies ApplyConfig doesn't self-deadlock
// when holding a.mu while calling safeSendToProgram (which also takes a.mu).
//
// Context: the /provider and /login commands call app.ApplyConfig which takes
// a.mu for the entire function. Near the end, ApplyConfig calls
// safeSendToProgram(ConfigUpdateMsg{...}). safeSendToProgram does a.mu.Lock()
// — on a non-reentrant mutex, that deadlocks the goroutine.
//
// Users reported `/provider deepseek` hanging with "Generating 11.8s" even
// though the command has no network calls. This test reproduces the hang
// with a 3s timeout so regressions are caught in CI.
func TestApplyConfig_NoSelfDeadlock(t *testing.T) {
	// Minimal App with no TUI program attached — ApplyConfig should still
	// complete without acquiring a lock twice on the same goroutine.
	app := &App{
		config: &config.Config{
			API: config.APIConfig{
				ActiveProvider: "kimi",
				KimiKey:        "sk-kimi-test-key-1234567890",
			},
			Model: config.ModelConfig{
				Name:     "kimi-for-coding",
				Provider: "kimi",
			},
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- app.ApplyConfig(app.config)
	}()

	select {
	case err := <-done:
		// Either nil or a legitimate "failed to re-initialize client" is fine —
		// the point is that ApplyConfig RETURNED rather than deadlocked.
		_ = err
	case <-time.After(3 * time.Second):
		t.Fatal("ApplyConfig deadlocked — took longer than 3s with no network work to do")
	}
}

// TestApplyConfig_NoSelfDeadlock_WithExecutorAndRegistry pins a SECOND,
// independent self-deadlock at the same v0.72.0 call site, structurally
// invisible to TestApplyConfig_NoSelfDeadlock above because that test's
// minimal App leaves a.executor/a.registry nil — skipping the exact branch
// this deadlock lives in.
//
// ApplyConfig holds a.mu for its whole critical section (see the NOTE at the
// top of the function). Step 4 used to call a.toolsForCurrentMode(), which
// calls a.IsPlanningModeEnabled(), which does a.mu.Lock() on the SAME
// non-reentrant sync.Mutex — an unconditional self-deadlock. Any fully-booted
// App (via builder.go) always has both a.executor and a.registry set, so
// EVERY post-boot ApplyConfig call — /login, /provider, /model, /set,
// /settings, Ctrl+K model selection, /permissions, /sandbox — hit this
// branch. Fixed by using the lock-free a.planModeToolsLocked(...) instead,
// mirroring the pattern TogglePlanningMode/disablePlanModeAfterApproval
// already use for exactly this reason.
func TestApplyConfig_NoSelfDeadlock_WithExecutorAndRegistry(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.API.ActiveProvider = "glm"
	cfg.API.GLMKey = "test-key-that-is-long-enough-1234567890"
	reg := tools.DefaultRegistry(".")
	app := &App{
		config:   cfg,
		ctx:      context.Background(),
		registry: reg,
		executor: tools.NewExecutor(reg, nil, 30*time.Second),
	}

	done := make(chan error, 1)
	go func() {
		done <- app.ApplyConfig(cfg)
	}()

	select {
	case err := <-done:
		_ = err
	case <-time.After(3 * time.Second):
		t.Fatal("ApplyConfig deadlocked with executor+registry populated — took longer than 3s")
	}
}

// TestApplyConfig_RefreshesExecutorContextWindow verifies live model changes
// update the executor's pruning threshold. Before this regression test,
// Builder set the value only once at startup, so switching from a 128K model
// to GLM-5.2 left the executor pruning at 128K despite the model's 1M window.
func TestApplyConfig_RefreshesExecutorContextWindow(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.API.ActiveProvider = "glm"
	cfg.API.Backend = "glm"
	cfg.API.GLMKey = "test-key-that-is-long-enough-1234567890"
	cfg.Model.Provider = "glm"
	cfg.Model.Name = "glm-5.2"

	exec := tools.NewExecutor(tools.DefaultRegistry("."), nil, 30*time.Second)
	exec.SetMaxInputTokens(128_000) // simulate a previous GLM-4.x session
	app := &App{
		config:   cfg,
		ctx:      context.Background(),
		executor: exec,
	}

	if err := app.ApplyConfig(cfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	if got := exec.MaxInputTokens(); got != 1_000_000 {
		t.Fatalf("executor context window = %d, want GLM-5.2 limit 1000000", got)
	}

	// Switching back to a smaller model must lower the guard as well; retaining
	// GLM-5.2's 1M value would defer pruning until long after GLM-4.7 overflowed.
	cfg.Model.Name = "glm-4.7"
	if err := app.ApplyConfig(cfg); err != nil {
		t.Fatalf("ApplyConfig GLM-4.7: %v", err)
	}
	if got := exec.MaxInputTokens(); got != 128_000 {
		t.Fatalf("executor context window after downgrade = %d, want 128000", got)
	}
}

func TestApplyConfig_ExecutorContextWindowHonorsOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.API.ActiveProvider = "glm"
	cfg.API.Backend = "glm"
	cfg.API.GLMKey = "test-key-that-is-long-enough-1234567890"
	cfg.Model.Provider = "glm"
	cfg.Model.Name = "glm-5.2"
	cfg.Context.MaxInputTokens = 256_000

	exec := tools.NewExecutor(tools.DefaultRegistry("."), nil, 30*time.Second)
	app := &App{config: cfg, ctx: context.Background(), executor: exec}

	if err := app.ApplyConfig(cfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	if got := exec.MaxInputTokens(); got != 256_000 {
		t.Fatalf("executor context window = %d, want configured override 256000", got)
	}
}

func TestEffectiveMaxInputTokens(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model.Name = "glm-5.2"
	if got := effectiveMaxInputTokens(cfg); got != 1_000_000 {
		t.Fatalf("GLM-5.2 model limit = %d, want 1000000", got)
	}
	cfg.Context.MaxInputTokens = 300_000
	if got := effectiveMaxInputTokens(cfg); got != 300_000 {
		t.Fatalf("configured limit = %d, want 300000", got)
	}
	if got := effectiveMaxInputTokens(nil); got != 0 {
		t.Fatalf("nil config limit = %d, want 0", got)
	}
}

func TestApplyConfig_RefreshesRouterCapabilityForGLM52(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.API.ActiveProvider = "glm"
	cfg.API.Backend = "glm"
	cfg.API.GLMKey = "test-key-that-is-long-enough-1234567890"
	cfg.Model.Provider = "glm"
	cfg.Model.Name = "glm-5.2"

	weakCapability := router.InferModelCapability("ollama", "llama3.2")
	taskRouter := router.NewRouter(&router.RouterConfig{
		Enabled:         true,
		ModelCapability: weakCapability,
	}, nil, nil, nil, nil, false, ".")
	app := &App{config: cfg, ctx: context.Background(), taskRouter: taskRouter}

	if err := app.ApplyConfig(cfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	capability, ok := taskRouter.CurrentModelCapability()
	if !ok {
		t.Fatal("router capability missing after ApplyConfig")
	}
	if capability.Tier != router.CapabilityStrong || capability.ModelName != "glm-5.2" {
		t.Fatalf("router capability = tier %v model %q, want strong glm-5.2",
			capability.Tier, capability.ModelName)
	}
}

func TestApplyConfigEngineModeIsRestartRequired(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const aggregation = "Rank repository files by how many TODO comments they contain"

	tests := []struct {
		name          string
		active        string
		configured    string
		message       string
		wantREPL      bool
		wantHarness   bool
		physicalTools bool
	}{
		{
			name:   "tools to hybrid does not invent runtime",
			active: "tools", configured: "hybrid", message: aggregation,
			physicalTools: false,
		},
		{
			name:   "auto to tools retains adaptive runtime",
			active: "auto", configured: "tools", message: aggregation,
			wantREPL: true, physicalTools: true,
		},
		{
			name:   "hybrid to tools retains runtime and harness",
			active: "hybrid", configured: "tools", message: "fix the auth bug",
			wantREPL: true, wantHarness: true, physicalTools: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Plan.Enabled = false
			cfg.API.ActiveProvider = "glm"
			cfg.API.Backend = "glm"
			cfg.API.GLMKey = "test-key-that-is-long-enough-1234567890"
			cfg.Model.Provider = "glm"
			cfg.Model.Name = "glm-5.2"
			cfg.Engine.Mode = tt.active

			registry := tools.DefaultRegistry(t.TempDir())
			if !tt.physicalTools {
				registry.Unregister("repl_exec")
				registry.Unregister("harness")
			}
			application := &App{
				config:   cfg,
				ctx:      context.Background(),
				registry: registry,
				executor: tools.NewExecutor(registry, nil, 30*time.Second),
			}
			application.runtimeEngineMode.Store(encodeRuntimeEngineMode(tt.active))
			application.taskRouter = router.NewRouter(&router.RouterConfig{
				Enabled:            true,
				DecomposeThreshold: 100,
				ParallelThreshold:  100,
				EngineMode:         tt.active,
			}, application.executor, nil, nil, registry, false, t.TempDir())

			candidate := application.GetConfig()
			candidate.Engine.Mode = tt.configured
			if err := application.ApplyConfig(candidate); err != nil {
				t.Fatalf("ApplyConfig: %v", err)
			}

			if got := application.GetConfig().Engine.Mode; got != tt.configured {
				t.Fatalf("persisted engine mode = %q, want %q", got, tt.configured)
			}
			if got := application.runtimeEngineModeSnapshot(); got != tt.active {
				t.Fatalf("runtime engine mode = %q, want boot mode %q", got, tt.active)
			}
			schema := application.toolsForMessage(tt.message)
			if got := schemaHasDeclaration(schema, "repl_exec"); got != tt.wantREPL {
				t.Fatalf("repl_exec exposed = %v, want %v", got, tt.wantREPL)
			}
			if got := schemaHasDeclaration(schema, "harness"); got != tt.wantHarness {
				t.Fatalf("harness exposed = %v, want %v", got, tt.wantHarness)
			}
			policy := application.hybridPolicyForSchema(tt.message, schema)
			if policy.Mode != tt.active {
				t.Fatalf("journal policy mode = %q, want runtime mode %q", policy.Mode, tt.active)
			}
			decision := application.taskRouter.RouteWithContext(context.Background(), tt.message)
			if got := containsToolSet(decision.SuggestedToolSets, tools.ToolSetHybrid); got != tt.wantREPL {
				t.Fatalf("router hybrid set = %v, want %v", got, tt.wantREPL)
			}
			if got := containsToolSet(decision.SuggestedToolSets, tools.ToolSetHarness); got != tt.wantHarness {
				t.Fatalf("router harness set = %v, want %v", got, tt.wantHarness)
			}
		})
	}
}

func containsToolSet(sets []tools.ToolSet, want tools.ToolSet) bool {
	for _, set := range sets {
		if set == want {
			return true
		}
	}
	return false
}

func TestApplyConfigEngineModeChangeWarnsRestartRequired(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.API.ActiveProvider = "glm"
	cfg.API.Backend = "glm"
	cfg.API.GLMKey = "test-key-that-is-long-enough-1234567890"
	cfg.Model.Provider = "glm"
	cfg.Model.Name = "glm-5.2"
	cfg.Engine.Mode = "tools"

	program, model := newCapturingProgram(t)
	application := &App{config: cfg, ctx: context.Background(), program: program}
	application.runtimeEngineMode.Store(runtimeEngineModeTools)
	candidate := application.GetConfig()
	candidate.Engine.Mode = "hybrid"
	if err := application.ApplyConfig(candidate); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		model.mu.Lock()
		found := false
		for _, message := range model.msgs {
			status, ok := message.(ui.StatusUpdateMsg)
			if ok && status.Type == ui.StatusWarning &&
				strings.Contains(status.Message, "engine.mode saved as hybrid") &&
				strings.Contains(status.Message, "remains in tools mode") &&
				strings.Contains(status.Message, "/restart") {
				found = true
				break
			}
		}
		model.mu.Unlock()
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restart-required engine warning was not delivered")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestApplyConfigUnchangedPendingEngineModeDoesNotWarnAgain(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.API.ActiveProvider = "glm"
	cfg.API.Backend = "glm"
	cfg.API.GLMKey = "test-key-that-is-long-enough-1234567890"
	cfg.Model.Provider = "glm"
	cfg.Model.Name = "glm-5.2"
	// Simulate a previous ApplyConfig call that saved hybrid for the next
	// launch while this process is still physically in tools mode.
	cfg.Engine.Mode = "hybrid"

	program, model := newCapturingProgram(t)
	application := &App{config: cfg, ctx: context.Background(), program: program}
	application.runtimeEngineMode.Store(runtimeEngineModeTools)
	candidate := application.GetConfig()
	candidate.Model.Name = "glm-4.7"
	if err := application.ApplyConfig(candidate); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	// ConfigUpdateMsg is sent before any optional warning, so observing it proves
	// the ApplyConfig delivery stream has drained through the relevant point.
	deadline := time.Now().Add(2 * time.Second)
	for {
		model.mu.Lock()
		hasConfigUpdate := false
		hasRestartWarning := false
		for _, message := range model.msgs {
			switch typed := message.(type) {
			case ui.ConfigUpdateMsg:
				hasConfigUpdate = true
			case ui.StatusUpdateMsg:
				hasRestartWarning = hasRestartWarning || strings.Contains(typed.Message, "/restart")
			}
		}
		model.mu.Unlock()
		if hasConfigUpdate {
			if hasRestartWarning {
				t.Fatal("unrelated model change repeated a stale engine restart warning")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("config update was not delivered")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestApplyConfigREPLLimitsAreRestartRequired(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.API.ActiveProvider = "glm"
	cfg.API.Backend = "glm"
	cfg.API.GLMKey = "test-key-that-is-long-enough-1234567890"
	cfg.Model.Provider = "glm"
	cfg.Model.Name = "glm-5.2"

	program, model := newCapturingProgram(t)
	application := &App{config: cfg, ctx: context.Background(), program: program}
	application.runtimeEngineMode.Store(runtimeEngineModeAuto)
	candidate := application.GetConfig()
	candidate.Engine.REPL.CellTimeout += time.Second
	if err := application.ApplyConfig(candidate); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		model.mu.Lock()
		found := false
		for _, message := range model.msgs {
			status, ok := message.(ui.StatusUpdateMsg)
			if ok && status.Type == ui.StatusWarning &&
				strings.Contains(status.Message, "REPL settings saved") &&
				strings.Contains(status.Message, "startup limits") &&
				strings.Contains(status.Message, "/restart") {
				found = true
				break
			}
		}
		model.mu.Unlock()
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restart-required REPL warning was not delivered")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestApplyConfigDoesNotCloseHybridRuntimeBeforeRestart(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Plan.Enabled = false
	cfg.API.ActiveProvider = "glm"
	cfg.API.Backend = "glm"
	cfg.API.GLMKey = "test-key-that-is-long-enough-1234567890"
	cfg.Model.Provider = "glm"
	cfg.Model.Name = "glm-5.2"
	cfg.Engine.Mode = "hybrid"

	manager := &fakeHybridRuntime{}
	registry := tools.DefaultRegistry(t.TempDir())
	application := &App{
		config:      cfg,
		ctx:         context.Background(),
		registry:    registry,
		executor:    tools.NewExecutor(registry, nil, 30*time.Second),
		replManager: manager,
	}
	application.runtimeEngineMode.Store(runtimeEngineModeHybrid)
	candidate := application.GetConfig()
	candidate.Engine.Mode = "tools"

	if err := application.ApplyConfig(candidate); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	if manager.closed {
		t.Fatal("hybrid worker was closed by a config-only transition")
	}
	if !schemaHasDeclaration(application.toolsForMessage("ordinary request"), "repl_exec") {
		t.Fatal("hybrid schema was hidden while its worker remained owned by the process")
	}
}
