package app

import (
	"os"
	"path/filepath"
	"testing"

	"gokin/internal/config"
)

func TestMarkOnboardingWelcomeSeenPersistsFlag(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg := config.DefaultConfig()
	if !cfg.UI.ShowWelcome {
		t.Fatal("default config must start with the onboarding welcome enabled")
	}

	app := &App{config: cfg}
	app.markOnboardingWelcomeSeen()

	if cfg.UI.ShowWelcome {
		t.Fatal("onboarding welcome remained enabled after a successful save")
	}

	persisted, err := config.Load()
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	if persisted.UI.ShowWelcome {
		t.Fatal("persisted config still has ui.show_welcome enabled")
	}
}

func TestMarkOnboardingWelcomeSeenRestoresFlagWhenSaveFails(t *testing.T) {
	tempDir := t.TempDir()
	blocker := filepath.Join(tempDir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block config directory creation"), 0600); err != nil {
		t.Fatalf("create config path blocker: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(blocker, "config-root"))

	cfg := config.DefaultConfig()
	app := &App{config: cfg}
	app.markOnboardingWelcomeSeen()

	if !cfg.UI.ShowWelcome {
		t.Fatal("onboarding welcome was disabled even though persistence failed")
	}
}
