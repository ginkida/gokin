package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/config"
)

func TestSetupAfterRemovedProviderKeepsNewSelection(t *testing.T) {
	for _, provider := range []string{"deepseek", "ollama"} {
		for _, preset := range []string{"kimi", "creative"} {
			t.Run(provider+"/"+preset, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", dir)
				path := filepath.Join(dir, "gokin", "config.yaml")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				raw := `api:
  active_provider: kimi
  backend: kimi
  api_key: removed-generic-key
  kimi_key: removed-key
  minimax_key: removed-key
  glm_key: keep-glm-key
  retry:
    providers:
      kimi: {http_timeout: 10s}
      minimax: {stream_idle_timeout: 30s}
model:
  provider: kimi
  name: k3
  preset: kimi
  fallback_providers: [kimi, minimax, deepseek]
  custom_base_url: https://removed.example/anthropic
future_setting: keep-me
`
				raw = strings.Replace(raw, "preset: kimi", "preset: "+preset, 1)
				if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
				var err error
				if provider == "ollama" {
					_, err = saveOllamaConfig("", "llama3.2", "")
				} else {
					_, err = saveProviderConfig(provider, "new-deepseek-key", "")
				}
				if err != nil {
					t.Fatal(err)
				}
				cfg, err := config.LoadFrom(path)
				if err != nil {
					t.Fatal(err)
				}
				config.MigrateConfig(cfg)
				if err := config.NormalizeConfig(cfg); err != nil {
					t.Fatal(err)
				}
				if cfg.API.GetActiveProvider() != provider || cfg.Model.Provider != provider {
					t.Fatalf("setup selection was overridden on reload: api=%q model=%+v", cfg.API.GetActiveProvider(), cfg.Model)
				}
				saved, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				for _, old := range []string{"kimi", "minimax", "removed-generic-key", "removed.example"} {
					if strings.Contains(string(saved), old) {
						t.Errorf("saved config retained %q: %s", old, saved)
					}
				}
				if !strings.Contains(string(saved), "keep-me") || cfg.API.GLMKey != "keep-glm-key" {
					t.Fatal("setup clobbered unrelated settings")
				}
			})
		}
	}
}
