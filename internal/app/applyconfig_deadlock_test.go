package app

import (
	"context"
	"testing"
	"time"

	"gokin/internal/config"
	"gokin/internal/router"
	"gokin/internal/tools"
)

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
