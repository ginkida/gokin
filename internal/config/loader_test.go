package config

import "testing"

func TestMigrateLegacyKimiModelName_RewritesRetiredNames(t *testing.T) {
	cases := []struct {
		oldName string
		want    string
	}{
		{"kimi-k2.5", "kimi-for-coding"},
		{"kimi-k2-thinking-turbo", "kimi-for-coding"},
		{"kimi-k2-turbo-preview", "kimi-for-coding"},
	}
	for _, c := range cases {
		cfg := &Config{Model: ModelConfig{Name: c.oldName}}
		migrateLegacyKimiModelName(cfg)
		if cfg.Model.Name != c.want {
			t.Errorf("migrate %q → %q, want %q", c.oldName, cfg.Model.Name, c.want)
		}
	}
}

func TestMigrateLegacyKimiModelName_PreservesUnknown(t *testing.T) {
	// Non-legacy names must not be touched.
	cfg := &Config{Model: ModelConfig{Name: "glm-5"}}
	migrateLegacyKimiModelName(cfg)
	if cfg.Model.Name != "glm-5" {
		t.Errorf("unknown model name was rewritten: got %q", cfg.Model.Name)
	}
}

func TestMigrateLegacyKimiModelName_PreservesEmpty(t *testing.T) {
	cfg := &Config{}
	migrateLegacyKimiModelName(cfg)
	if cfg.Model.Name != "" {
		t.Errorf("empty name changed to %q", cfg.Model.Name)
	}
}

func TestMigrateLegacyKimiModelName_NilSafe(t *testing.T) {
	migrateLegacyKimiModelName(nil) // must not panic
}

func TestMigrateLegacyKimiModelName_SkipsWhenCustomBaseURL(t *testing.T) {
	// Regression guard from release review: users with explicit
	// CustomBaseURL (e.g., pointing at Moonshot Developer API) may still
	// be using legacy names on an endpoint that serves them. The
	// migration must not silently rewrite their model — that would
	// redirect their request to a model the endpoint doesn't serve.
	cases := []string{
		"https://api.moonshot.ai/anthropic",
		"https://custom.example/kimi",
		"  https://x.y/z  ", // whitespace-preserving detection via TrimSpace
	}
	for _, url := range cases {
		cfg := &Config{Model: ModelConfig{
			Name:          "kimi-k2.5",
			CustomBaseURL: url,
		}}
		migrateLegacyKimiModelName(cfg)
		if cfg.Model.Name != "kimi-k2.5" {
			t.Errorf("CustomBaseURL=%q should block migration; got %q", url, cfg.Model.Name)
		}
	}
}

func TestMigrateLegacyKimiModelName_EmptyCustomBaseURLTriggersMigration(t *testing.T) {
	// Whitespace-only CustomBaseURL counts as unset.
	cfg := &Config{Model: ModelConfig{
		Name:          "kimi-k2.5",
		CustomBaseURL: "   ",
	}}
	migrateLegacyKimiModelName(cfg)
	if cfg.Model.Name != "kimi-for-coding" {
		t.Errorf("whitespace CustomBaseURL should not block migration; got %q", cfg.Model.Name)
	}
}
