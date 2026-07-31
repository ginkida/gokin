package config

import (
	"os"
	"path/filepath"
	"testing"
)

// An explicit --config file must own every default-path resolution in the
// process. The setup wizard and the user-facing "saved to <path>" messages
// resolve the location themselves, so without this a first run with --config
// wrote the API key to the DEFAULT config and then failed again on the explicit
// file that still had no credentials.
func TestSetExplicitConfigPathOwnsDefaultResolution(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defaultPath := GetConfigPath()
	if defaultPath == "" {
		t.Fatal("default config path did not resolve")
	}

	explicit := filepath.Join(t.TempDir(), "ci", "gokin.yaml")
	SetExplicitConfigPath(explicit)
	t.Cleanup(func() { SetExplicitConfigPath("") })

	if got := GetConfigPath(); got != explicit {
		t.Fatalf("GetConfigPath() = %q, want the explicit file %q", got, explicit)
	}

	// Clearing restores the default lookup so nothing leaks between runs.
	SetExplicitConfigPath("")
	if got := GetConfigPath(); got != defaultPath {
		t.Fatalf("cleared GetConfigPath() = %q, want %q", got, defaultPath)
	}
}

// LoadFrom already routes Save() back to the explicit file; binding the process
// must not change that, and a save must never land in the default location.
func TestExplicitConfigPathSaveStaysInTheNamedFile(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	explicit := filepath.Join(t.TempDir(), "ci-config.yaml")
	if err := os.WriteFile(explicit, []byte("api:\n  active_provider: glm\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	SetExplicitConfigPath(explicit)
	t.Cleanup(func() { SetExplicitConfigPath("") })
	cfg, err := LoadFrom(explicit)
	if err != nil {
		t.Fatal(err)
	}
	cfg.API.GLMKey = "test-key-value-long-enough"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	saved, err := os.ReadFile(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) == 0 {
		t.Fatal("explicit config file was not written")
	}
	if _, err := os.Stat(filepath.Join(xdg, "gokin", "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("save also touched the default config location: %v", err)
	}
}
