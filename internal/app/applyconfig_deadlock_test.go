package app

import (
	"context"
	"testing"
	"time"

	"gokin/internal/config"
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
