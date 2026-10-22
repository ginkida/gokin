package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRemovedProviderMigration(t *testing.T) {
	for _, raw := range []string{
		"api: {active_provider: kimi}",
		"api: {backend: minimax}",
		"model: {provider: kimi}",
		"model: {name: k3}",
		"model: {name: moonshot-v1-128k}",
		"api: {active_provider: ' MINIMAX '}",
		"model: {name: kimi-for-coding-highspeed}",
		"model: {name: MiniMax-M2.7}",
		"api: {kimi_key: old-key}",
		"api: {minimax_key: old-key}",
		"model: {preset: kimi}",
	} {
		t.Run(raw, func(t *testing.T) {
			cfg := DefaultConfig()
			if err := yaml.Unmarshal([]byte(raw), cfg); err != nil {
				t.Fatal(err)
			}
			MigrateConfig(cfg)
			if err := NormalizeConfig(cfg); err != nil {
				t.Fatal(err)
			}
			if cfg.API.GetActiveProvider() != "glm" || cfg.API.Backend != "glm" || cfg.Model.Provider != "glm" || cfg.Model.Name != DefaultGLMModel || cfg.API.KimiKey != "" || cfg.API.MiniMaxKey != "" {
				t.Fatalf("removed provider not migrated: active=%q backend=%q model=%+v", cfg.API.GetActiveProvider(), cfg.API.Backend, cfg.Model)
			}
		})
	}
}

func TestRemovedProviderMigrationLoadAndWarning(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	path := filepath.Join(dir, "config.yaml")
	raw := `api:
  active_provider: kimi
  backend: minimax
  api_key: never-send-this-to-glm
  kimi_key: discarded
  minimax_key: discarded
  glm_key: valid-glm-key
  retry:
    providers:
      kimi: {http_timeout: 10s}
      minimax: {stream_idle_timeout: 30s}
model:
  provider: kimi
  name: k3
  preset: kimi
  custom_base_url: https://removed.example/anthropic
  fallback_providers: [kimi, minimax, deepseek]
unknown_future_key: accepted
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	MigrateConfig(cfg)
	if err := NormalizeConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.API.GetActiveProvider() != "glm" || cfg.Model.Name != DefaultGLMModel {
		t.Fatalf("not migrated: %+v", cfg.Model)
	}
	if cfg.API.APIKey != "" || cfg.Model.CustomBaseURL != "" || cfg.API.GLMKey != "valid-glm-key" {
		t.Fatal("removed endpoint/key retained or GLM key lost")
	}
	if len(cfg.API.Retry.Providers) != 0 || !reflect.DeepEqual(cfg.Model.FallbackProviders, []string{"deepseek"}) {
		t.Fatal("removed retry/fallback configuration retained")
	}
	if err := yaml.Unmarshal([]byte("model: {provider: minimax}"), cfg); err != nil {
		t.Fatal(err)
	}
	MigrateConfig(cfg)
	if strings.Count(output.String(), "providers were removed") != 1 {
		t.Fatalf("want one warning, got %s", &output)
	}
	serialized, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "kimi") || strings.Contains(string(serialized), "minimax") {
		t.Fatalf("removed settings saved again: %s", serialized)
	}
}

func TestLegacyToolBudgetCompatibility(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"tools: {kimi_tool_budget: 17}", 17},
		{"tools: {kimi_tool_budget: 17, tool_budget: 0}", 0},
		{"tools: {tool_budget: 23}", 23},
		{"tools: {<<: {tool_budget: 0}, kimi_tool_budget: 17}", 0},
		{"tools: {<<: {tool_budget: null}, kimi_tool_budget: 17}", 40},
		{"tools: {timeout: 1m}", 40},
	} {
		cfg := DefaultConfig()
		if err := yaml.Unmarshal([]byte(tc.raw), cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.Tools.ToolBudget != tc.want {
			t.Fatalf("%s: got %d want %d", tc.raw, cfg.Tools.ToolBudget, tc.want)
		}
	}
}

func TestRemovedProviderAuxiliarySettingsKeepOllama(t *testing.T) {
	cfg := DefaultConfig()
	cfg.API.ActiveProvider, cfg.API.Backend = "ollama", "ollama"
	cfg.API.OllamaKey = "remote-ollama-key"
	cfg.Model.Provider, cfg.Model.Name = "ollama", "llama3.2"
	cfg.Model.CustomBaseURL = "http://127.0.0.1:11434"
	cfg.Model.FallbackProviders = []string{"kimi", "deepseek"}
	cfg.API.Retry.Providers = map[string]ProviderRetryConfig{"minimax": {}}
	MigrateConfig(cfg)
	if err := NormalizeConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.API.GetActiveProvider() != "ollama" || cfg.Model.Provider != "ollama" || cfg.Model.Name != "llama3.2" || cfg.Model.CustomBaseURL != "http://127.0.0.1:11434" || cfg.API.OllamaKey != "remote-ollama-key" {
		t.Fatalf("obsolete auxiliary settings changed Ollama: provider=%q model=%+v", cfg.API.GetActiveProvider(), cfg.Model)
	}
	if !reflect.DeepEqual(cfg.Model.FallbackProviders, []string{"deepseek"}) || len(cfg.API.Retry.Providers) != 0 {
		t.Fatal("removed auxiliary settings retained")
	}
}

func TestRemovedProviderModelNamesRemainValidForOllama(t *testing.T) {
	for _, model := range []string{"kimi-k2:latest", "MiniMax-M2.7"} {
		cfg := DefaultConfig()
		cfg.API.ActiveProvider, cfg.API.Backend = "ollama", "ollama"
		cfg.Model.Provider, cfg.Model.Name = "ollama", model
		MigrateConfig(cfg)
		if err := NormalizeConfig(cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.Model.Provider != "ollama" || cfg.Model.Name != model {
			t.Fatalf("local model %q was migrated: %+v", model, cfg.Model)
		}
	}
}
