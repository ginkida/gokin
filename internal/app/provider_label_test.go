package app

import (
	"testing"

	"gokin/internal/config"
)

// TestShortActiveProviderName pins the mapping from provider key to the
// inline display label used in toast messages ("Kimi rate limit — resumes
// in 30s"). Raw keys like "glm" look wrong as sentence-starters, the full
// DisplayName ("GLM (BigModel / Z.AI)") is too long for a status-bar
// toast — we want the first word of DisplayName.
func TestShortActiveProviderName(t *testing.T) {
	cases := []struct {
		name   string
		active string
		want   string
	}{
		{"kimi_full_provider", "kimi", "Kimi"},
		{"glm_abbrev_provider", "glm", "GLM"},
		{"minimax_single_word", "minimax", "MiniMax"},
		{"ollama_strips_parens", "ollama", "Ollama"},
		// Empty ActiveProvider+Backend → GetActiveProvider defaults to
		// "glm", so the label reflects that. This documents the chain
		// rather than testing a fallback that never fires.
		{"empty_active_defaults_to_glm", "", "GLM"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &App{
				config: &config.Config{
					API: config.APIConfig{ActiveProvider: c.active},
				},
			}
			if got := a.shortActiveProviderName(); got != c.want {
				t.Errorf("shortActiveProviderName() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestShortActiveProviderName_NilSafe: the helper runs inside a status
// callback that might fire during teardown — it must not panic when app
// or config is nil.
func TestShortActiveProviderName_NilSafe(t *testing.T) {
	var a *App
	if got := a.shortActiveProviderName(); got != "Provider" {
		t.Errorf("nil app returns %q, want %q", got, "Provider")
	}

	a2 := &App{} // non-nil app, nil config
	if got := a2.shortActiveProviderName(); got != "Provider" {
		t.Errorf("nil config returns %q, want %q", got, "Provider")
	}
}

// TestShortActiveProviderName_UnknownProviderFallsBackToKey: a provider
// key that isn't in the registry (custom/experimental config) should
// still produce readable output — return the raw key rather than an
// empty string.
func TestShortActiveProviderName_UnknownProviderFallsBackToKey(t *testing.T) {
	a := &App{
		config: &config.Config{
			API: config.APIConfig{ActiveProvider: "experimental-backend"},
		},
	}
	if got := a.shortActiveProviderName(); got != "experimental-backend" {
		t.Errorf("unknown provider = %q, want raw key", got)
	}
}
