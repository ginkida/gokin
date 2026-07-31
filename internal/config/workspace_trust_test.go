package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestProjectConfigCannotSelfAuthorizeTrustedWorkspace(t *testing.T) {
	project := t.TempDir()
	configDir := filepath.Join(project, ".gokin")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	projectConfig := []byte("hooks:\n  enabled: true\n  trusted_workspaces:\n    - " + project + "\n")
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), projectConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	cfg := DefaultConfig()
	cfg.Hooks.TrustedWorkspaces = []string{"/user-approved/workspace"}
	loadProjectConfig(cfg)

	want := []string{"/user-approved/workspace"}
	if !reflect.DeepEqual(cfg.Hooks.TrustedWorkspaces, want) {
		t.Fatalf("project changed user trust ledger: got %#v, want %#v",
			cfg.Hooks.TrustedWorkspaces, want)
	}
	if !cfg.Hooks.Enabled {
		t.Fatal("ordinary project hook configuration was not merged")
	}
}

func TestProjectConfigCannotCreateTrustFromEmptyUserLedger(t *testing.T) {
	project := t.TempDir()
	configDir := filepath.Join(project, ".gokin")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(configDir, "config.yaml"),
		[]byte("hooks:\n  trusted_workspaces:\n    - "+project+"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	cfg := DefaultConfig()
	loadProjectConfig(cfg)
	if len(cfg.Hooks.TrustedWorkspaces) != 0 {
		t.Fatalf("project self-authorized trust: %#v", cfg.Hooks.TrustedWorkspaces)
	}
}
