package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectBashPolicyCannotWiden(t *testing.T) {
	t.Run("upward", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".gokin"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".gokin", "config.yaml"), []byte("tools:\n  bash:\n    sandbox: false\n    require_sandbox: false\n    allow_network: true\n"), 0600); err != nil {
			t.Fatal(err)
		}
		cfg := DefaultConfig()
		cfg.Tools.Bash.RequireSandbox = true
		t.Chdir(dir)
		loadProjectConfig(cfg)
		if !cfg.Tools.Bash.Sandbox || !cfg.Tools.Bash.RequireSandbox || cfg.Tools.Bash.AllowNetwork {
			t.Fatalf("project widened user policy: %+v", cfg.Tools.Bash)
		}
	})
}

func TestProjectBashPolicyMayNarrow(t *testing.T) {
	cfg := DefaultConfig()
	user := BashConfig{Sandbox: false, AllowNetwork: true}
	cfg.Tools.Bash = BashConfig{Sandbox: true, RequireSandbox: true, AllowNetwork: false}
	narrowProjectBashPolicy(cfg, user)
	if !cfg.Tools.Bash.Sandbox || !cfg.Tools.Bash.RequireSandbox || cfg.Tools.Bash.AllowNetwork {
		t.Fatalf("project restrictions lost: %+v", cfg.Tools.Bash)
	}
}

func TestLoadWithProjectDirCannotWidenBashPolicy(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	SetExplicitConfigPath("")
	t.Cleanup(func() { SetExplicitConfigPath("") })
	path := filepath.Join(base, "config", "gokin", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("tools:\n  bash:\n    sandbox: true\n    require_sandbox: true\n    allow_network: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(base, "project")
	if err := os.MkdirAll(filepath.Join(project, ".gokin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".gokin", "config.yaml"), []byte("tools:\n  allowed_dirs: [\"/\"]\n  bash:\n    sandbox: false\n    require_sandbox: false\n    allow_network: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithProjectDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Tools.Bash.Sandbox || !cfg.Tools.Bash.RequireSandbox || cfg.Tools.Bash.AllowNetwork {
		t.Fatalf("explicit project widened policy: %+v", cfg.Tools.Bash)
	}
	if len(cfg.Tools.AllowedDirs) != 0 {
		t.Fatalf("explicit project granted host filesystem: %v", cfg.Tools.AllowedDirs)
	}
}

func TestMisplacedSandboxConfigDiagnosed(t *testing.T) {
	var warnings bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&warnings, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("tools:\n  sandbox: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	if err := loadFromFile(cfg, path); err != nil || !strings.Contains(warnings.String(), "tools.bash.sandbox") || !cfg.Tools.Bash.Sandbox {
		t.Fatalf("misplaced sandbox not diagnosed: %v %s", err, warnings.String())
	}
}

func TestProjectCannotSelfGrantSandboxDirectories(t *testing.T) {
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".gokin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".gokin", "config.yaml"), []byte("tools:\n  allowed_dirs: [\"/\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	cfg := DefaultConfig()
	loadProjectConfig(cfg)
	if len(cfg.Tools.AllowedDirs) != 0 {
		t.Fatalf("project granted host filesystem: %v", cfg.Tools.AllowedDirs)
	}
}

func TestProjectDirectoryGrantMayOnlyNarrowUserGrant(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	outside := t.TempDir()
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Tools.AllowedDirs = []string{child, outside, "/"}
	narrowProjectAllowedDirs(cfg, []string{root})
	resolved, err := filepath.EvalSymlinks(child)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Tools.AllowedDirs) != 1 || cfg.Tools.AllowedDirs[0] != resolved {
		t.Fatalf("project widened or lost contained grant: %v", cfg.Tools.AllowedDirs)
	}
}
