package app

import (
	"testing"
	"time"

	"gokin/internal/config"
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
