package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeConfigRejectsUnknownFallbackProvider(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Model.FallbackProviders = []string{"kmi"}
	err := NormalizeConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "unknown fallback provider") {
		t.Fatalf("NormalizeConfig error = %v, want unknown fallback provider", err)
	}
}

func TestNormalizeConfigNormalizesAndDeduplicatesFallbackProviders(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Model.FallbackProviders = []string{" KIMI ", "kimi", "MiniMax", ""}
	if err := NormalizeConfig(cfg); err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	want := []string{"kimi", "minimax"}
	if !reflect.DeepEqual(cfg.Model.FallbackProviders, want) {
		t.Fatalf("fallback providers = %v, want %v", cfg.Model.FallbackProviders, want)
	}
}
