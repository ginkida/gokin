package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigDisablesGlobalMemory(t *testing.T) {
	if DefaultConfig().Memory.AllowGlobal {
		t.Fatal("memory.allow_global must default false to prevent cross-project leakage")
	}
}

func TestGlobalConfigCanExplicitlyEnableGlobalMemory(t *testing.T) {
	cfg := DefaultConfig()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("memory:\n  allow_global: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadFromFile(cfg, path); err != nil {
		t.Fatal(err)
	}
	if !cfg.Memory.AllowGlobal {
		t.Fatal("explicit user config did not enable global memory")
	}
}

func TestProjectConfigCannotEnableGlobalMemory(t *testing.T) {
	projectDir := t.TempDir()
	gokinDir := filepath.Join(projectDir, ".gokin")
	if err := os.MkdirAll(gokinDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(gokinDir, "config.yaml"),
		[]byte("memory:\n  allow_global: true\n  auto_inject: false\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWD) }()
	if err := os.Chdir(projectDir); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	loadProjectConfig(cfg)
	if cfg.Memory.AllowGlobal {
		t.Fatal("repository-controlled config enabled user-wide memory")
	}
	if cfg.Memory.AutoInject {
		t.Fatal("ordinary project memory setting was not merged")
	}
}

func TestProjectConfigCannotWidenButPreservesUserGlobalOptInWhenUnspecified(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	userConfigDir := filepath.Join(configRoot, "gokin")
	if err := os.MkdirAll(userConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(userConfigDir, "config.yaml"),
		[]byte("memory:\n  allow_global: true\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	projectDir := t.TempDir()
	gokinDir := filepath.Join(projectDir, ".gokin")
	if err := os.MkdirAll(gokinDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(gokinDir, "config.yaml"),
		[]byte("memory:\n  auto_inject: false\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadWithProjectDir(projectDir)
	if err != nil {
		t.Fatalf("LoadWithProjectDir: %v", err)
	}
	if !cfg.Memory.AllowGlobal {
		t.Fatal("project config with no allow_global field erased the user opt-in")
	}
}

func TestProjectConfigMayNarrowUserGlobalOptIn(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	userConfigDir := filepath.Join(configRoot, "gokin")
	if err := os.MkdirAll(userConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(userConfigDir, "config.yaml"),
		[]byte("memory:\n  allow_global: true\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	projectDir := t.TempDir()
	gokinDir := filepath.Join(projectDir, ".gokin")
	if err := os.MkdirAll(gokinDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(gokinDir, "config.yaml"),
		[]byte("memory:\n  allow_global: false\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadWithProjectDir(projectDir)
	if err != nil {
		t.Fatalf("LoadWithProjectDir: %v", err)
	}
	if cfg.Memory.AllowGlobal {
		t.Fatal("project-level deny did not narrow the user global-memory capability")
	}
}
