package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeyboardProfileDefaultAndSavedSelection(t *testing.T) {
	if DefaultConfig().Plan.Enabled {
		t.Fatal("new interactive sessions should start in Normal mode")
	}
	if DefaultConfig().UI.Keybindings != "claude" {
		t.Fatal("application must default to Claude-style editing")
	}
	for _, profile := range []string{"claude", "gokin"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("ui: {keybindings: "+profile+"}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadFrom(path)
		if err != nil || cfg.UI.Keybindings != profile {
			t.Fatalf("saved keyboard profile lost: %v", err)
		}
		cfg.API.GLMKey = "synthetic"
		if err := NormalizeConfig(cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.Clone().UI.Keybindings != profile {
			t.Fatal("clone loses selected profile")
		}
	}
	cfg := DefaultConfig()
	cfg.UI.Keybindings = "typo"
	if err := NormalizeConfig(cfg); err == nil {
		t.Fatal("unknown profile silently accepted")
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepts unknown keyboard profile")
	}
}
