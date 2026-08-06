package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveModelRoundTimeoutUpdatesWinningProjectLayer(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	projectConfigDir := filepath.Join(project, ".gokin")
	if err := os.MkdirAll(projectConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(root, "user.yaml")
	if err := os.WriteFile(globalPath, []byte("model:\n  name: global-model\ntools:\n  model_round_timeout: 14m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(projectConfigDir, "config.yaml")
	projectYAML := "# keep this project comment\nmodel:\n  name: project-model\ntools:\n  timeout: 45s\n  model_round_timeout: 5m\n"
	if err := os.WriteFile(projectPath, []byte(projectYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	cfg, err := LoadFrom(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Tools.ModelRoundTimeout; got != 5*time.Minute {
		t.Fatalf("effective project timeout = %v, want 5m", got)
	}
	assertSameConfigFile(t, cfg.ModelRoundTimeoutConfigPath(), projectPath)
	if err := cfg.SaveModelRoundTimeout(22 * time.Minute); err != nil {
		t.Fatal(err)
	}

	projectData, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	projectText := string(projectData)
	for _, want := range []string{"# keep this project comment", "name: project-model", "timeout: 45s", "model_round_timeout: 22m0s"} {
		if !strings.Contains(projectText, want) {
			t.Fatalf("project config lost %q:\n%s", want, projectText)
		}
	}
	globalData, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(globalData), "model_round_timeout: 14m") {
		t.Fatalf("project-owned timeout unexpectedly rewrote global config:\n%s", globalData)
	}

	// A later unrelated full save still targets the global layer. It must keep
	// the original global timeout instead of leaking the project override.
	cfg.UI.CompactMode = !cfg.UI.CompactMode
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	globalData, err = os.ReadFile(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(globalData), "model_round_timeout: 14m0s") {
		t.Fatalf("full save leaked project timeout into global config:\n%s", globalData)
	}

	reloaded, err := LoadFrom(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Tools.ModelRoundTimeout; got != 22*time.Minute {
		t.Fatalf("reloaded timeout = %v, want 22m", got)
	}
}

func TestSaveModelRoundTimeoutUpdatesOnlyScalarInGlobalLayer(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(filepath.Join(project, ".gokin"), 0o700); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(root, "user.yaml")
	globalYAML := "# keep global comment\nmodel:\n  name: global-model\ntools:\n  timeout: 45s\n  model_round_timeout: 14m\n"
	if err := os.WriteFile(globalPath, []byte(globalYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(project, ".gokin", "config.yaml")
	if err := os.WriteFile(projectPath, []byte("model:\n  name: project-model\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	cfg, err := LoadFrom(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Name != "project-model" {
		t.Fatalf("project overlay did not load: %q", cfg.Model.Name)
	}
	assertSameConfigFile(t, cfg.ModelRoundTimeoutConfigPath(), globalPath)
	if err := cfg.SaveModelRoundTimeout(24 * time.Minute); err != nil {
		t.Fatal(err)
	}

	globalData, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	globalText := string(globalData)
	for _, want := range []string{"# keep global comment", "name: global-model", "timeout: 45s", "model_round_timeout: 24m0s"} {
		if !strings.Contains(globalText, want) {
			t.Fatalf("global config lost %q or absorbed project overlay:\n%s", want, globalText)
		}
	}
	projectData, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(projectData), "model_round_timeout") {
		t.Fatalf("project without timeout ownership was unexpectedly modified:\n%s", projectData)
	}
}

func TestConfigValidateRejectsNegativeModelRoundTimeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Tools.ModelRoundTimeout = -time.Second
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "tools.model_round_timeout") {
		t.Fatalf("Validate() error = %v, want model round timeout validation", err)
	}
}

func assertSameConfigFile(t *testing.T, got, want string) {
	t.Helper()
	gotInfo, gotErr := os.Stat(got)
	wantInfo, wantErr := os.Stat(want)
	if gotErr != nil || wantErr != nil || !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("timeout config path = %q, want same file as %q (errors %v/%v)", got, want, gotErr, wantErr)
	}
}
