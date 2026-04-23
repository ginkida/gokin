package app

import (
	"strings"
	"testing"

	"gokin/internal/config"
)

func TestBuildModelEnhancement_KimiIncludesExecutionPolicy(t *testing.T) {
	app := &App{
		config: &config.Config{
			API:   config.APIConfig{Backend: "kimi"},
			Model: config.ModelConfig{Name: "kimi-for-coding"},
		},
	}

	got := app.buildModelEnhancement()
	for _, needle := range []string{
		"Kimi Execution Policy",
		"Prefer grep -> targeted read",
		"do not repeat the same read/grep/glob",
		"After 1-3 tool calls",
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("buildModelEnhancement() missing %q:\n%s", needle, got)
		}
	}
}
