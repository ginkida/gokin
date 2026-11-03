package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretPathConfigIsUserOnly(t *testing.T) {
	work := t.TempDir()
	if err := os.Mkdir(filepath.Join(work, ".gokin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".gokin/config.yaml"), []byte("tools:\n  allow_secret_paths: [/]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)
	cfg := DefaultConfig()
	cfg.Tools.AllowSecretPaths = []string{"/explicit/user/file"}
	loadProjectConfig(cfg)
	if len(cfg.Tools.AllowSecretPaths) != 1 || cfg.Tools.AllowSecretPaths[0] != "/explicit/user/file" {
		t.Fatalf("project widened exceptions: %v", cfg.Tools.AllowSecretPaths)
	}
	userConfig := filepath.Join(work, "user.yaml")
	if err := os.WriteFile(userConfig, []byte("tools:\n  allow_secret_paths: [/explicit/user/file]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	SetExplicitConfigPath(userConfig)
	t.Cleanup(func() { SetExplicitConfigPath("") })
	loaded, err := LoadWithProjectDir(work)
	if err != nil || len(loaded.Tools.AllowSecretPaths) != 1 || loaded.Tools.AllowSecretPaths[0] != "/explicit/user/file" {
		t.Fatalf("explicit project widened exceptions: %+v %v", loaded, err)
	}
	clone := cfg.Clone()
	clone.Tools.AllowSecretPaths[0] = "/different"
	if cfg.Tools.AllowSecretPaths[0] != "/explicit/user/file" {
		t.Fatal("config clone shared credential authority")
	}
	cfg.Tools.AllowSecretPaths = []string{"./relative"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "allow_secret_paths") {
		t.Fatalf("relative exception accepted: %v", err)
	}
}
