package app

import (
	"testing"

	"gokin/internal/config"
)

func TestDetectPrimaryProvider_Priority(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{
			name: "explicit Model.Provider wins over API.ActiveProvider",
			cfg: &config.Config{
				Model: config.ModelConfig{Provider: "minimax", Name: "glm-5.1"},
				API:   config.APIConfig{ActiveProvider: "kimi"},
			},
			want: "minimax",
		},
		{
			name: "empty Model.Provider falls back to API.ActiveProvider",
			cfg: &config.Config{
				API: config.APIConfig{ActiveProvider: "kimi"},
			},
			want: "kimi",
		},
		{
			name: "empty Model.Provider and API.ActiveProvider falls back to Backend",
			cfg: &config.Config{
				API: config.APIConfig{Backend: "minimax"},
			},
			want: "minimax",
		},
		{
			name: "all empty falls through to APIConfig default glm",
			cfg:  &config.Config{},
			want: "glm",
		},
		{
			name: "nil config returns glm default",
			cfg:  nil,
			want: "glm",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectPrimaryProvider(tc.cfg); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildFailoverCandidates_NilConfigReturnsNil(t *testing.T) {
	if got := buildFailoverCandidates(nil, "glm"); got != nil {
		t.Errorf("nil cfg should return nil, got %v", got)
	}
}

func TestBuildFailoverCandidates_PrimaryFirst(t *testing.T) {
	cfg := &config.Config{
		API: config.APIConfig{GLMKey: "x", KimiKey: "y"},
	}
	got := buildFailoverCandidates(cfg, "kimi")
	if len(got) == 0 || got[0] != "kimi" {
		t.Errorf("got %v, want kimi as first element", got)
	}
}

func TestBuildFailoverCandidates_UserFallbacksOrderedAfterPrimary(t *testing.T) {
	cfg := &config.Config{
		Model: config.ModelConfig{FallbackProviders: []string{"minimax", "kimi"}},
		API:   config.APIConfig{GLMKey: "x", MiniMaxKey: "y", KimiKey: "z"},
	}
	got := buildFailoverCandidates(cfg, "glm")

	// Primary + user fallbacks should come first, in the user-provided order.
	if len(got) < 3 {
		t.Fatalf("got %v, want at least 3", got)
	}
	if got[0] != "glm" || got[1] != "minimax" || got[2] != "kimi" {
		t.Errorf("got %v, want first three = [glm minimax kimi]", got[:3])
	}
}

func TestBuildFailoverCandidates_AutoIncludesConfiguredProviders(t *testing.T) {
	// No explicit FallbackProviders — helper should still surface providers
	// whose keys are configured.
	cfg := &config.Config{
		API: config.APIConfig{GLMKey: "x", MiniMaxKey: "y"},
	}
	got := buildFailoverCandidates(cfg, "glm")
	if len(got) < 2 {
		t.Fatalf("got %v, want at least 2 candidates", got)
	}
	if got[0] != "glm" {
		t.Errorf("primary should be first; got %v", got)
	}
	seen := make(map[string]bool, len(got))
	for _, p := range got {
		seen[p] = true
	}
	if !seen["minimax"] {
		t.Errorf("minimax has key configured, should appear in chain: %v", got)
	}
}

func TestBuildFailoverCandidates_ExcludesOllamaWhenNotExplicit(t *testing.T) {
	// Ollama is KeyOptional — HasProvider always returns true for it. The
	// helper must nevertheless skip ollama from auto-append, because
	// emergency failover to a local server surprise-loads a user who
	// probably just wants another remote provider.
	cfg := &config.Config{
		API: config.APIConfig{GLMKey: "x"},
	}
	got := buildFailoverCandidates(cfg, "glm")
	for _, p := range got {
		if p == "ollama" {
			t.Errorf("ollama auto-appended into %v", got)
		}
	}
}

func TestBuildFailoverCandidates_KeepsOllamaWhenExplicitlyRequested(t *testing.T) {
	cfg := &config.Config{
		Model: config.ModelConfig{FallbackProviders: []string{"ollama"}},
		API:   config.APIConfig{GLMKey: "x"},
	}
	got := buildFailoverCandidates(cfg, "glm")
	found := false
	for _, p := range got {
		if p == "ollama" {
			found = true
		}
	}
	if !found {
		t.Errorf("user-configured ollama fallback missing from chain: %v", got)
	}
}

func TestBuildFailoverCandidates_KeepsOllamaAsPrimary(t *testing.T) {
	cfg := &config.Config{
		API: config.APIConfig{GLMKey: "x"},
	}
	got := buildFailoverCandidates(cfg, "ollama")
	if len(got) == 0 || got[0] != "ollama" {
		t.Errorf("got %v, want ollama first when primary", got)
	}
}

func TestBuildFailoverCandidates_NormalizesAndDedupes(t *testing.T) {
	cfg := &config.Config{
		Model: config.ModelConfig{FallbackProviders: []string{"  GLM ", "minimax", "MINIMAX", ""}},
		API:   config.APIConfig{GLMKey: "x", MiniMaxKey: "y"},
	}
	got := buildFailoverCandidates(cfg, "glm")
	// Expect primary "glm" first, minimax once, no empties, no duplicates.
	want := []string{"glm", "minimax"}
	if !stringSlicesEqual(got, want) {
		t.Errorf("got %v, want %v (normalization + dedup)", got, want)
	}
}

func TestBuildFailoverCandidates_PrimaryInUserFallbacksNotDuplicated(t *testing.T) {
	cfg := &config.Config{
		Model: config.ModelConfig{FallbackProviders: []string{"glm", "minimax"}},
		API:   config.APIConfig{GLMKey: "x", MiniMaxKey: "y"},
	}
	got := buildFailoverCandidates(cfg, "glm")
	count := 0
	for _, p := range got {
		if p == "glm" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("glm appears %d times in %v, want 1", count, got)
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
