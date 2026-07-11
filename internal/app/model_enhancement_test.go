package app

import (
	"strings"
	"testing"

	"gokin/internal/config"
)

func TestBuildModelEnhancement_GLM52IncludesExecutionContract(t *testing.T) {
	app := &App{
		config: &config.Config{
			API:   config.APIConfig{Backend: "glm"},
			Model: config.ModelConfig{Name: "glm-5.2"},
		},
	}

	got := app.buildModelEnhancement()
	for _, needle := range []string{
		"GLM Execution Policy",
		"keep the todo list current",
		"batch independent reads/searches",
		"do not repeat a read/grep",
		"never stop immediately after a tool call",
		"inspect the resulting diff or files",
		"implemented and verified",
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("buildModelEnhancement() missing %q:\n%s", needle, got)
		}
	}
}

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

func TestBuildModelEnhancement_DeepSeekIncludesClaudeCodeWorkflow(t *testing.T) {
	app := &App{
		config: &config.Config{
			API:   config.APIConfig{Backend: "deepseek"},
			Model: config.ModelConfig{Name: "deepseek-v4-pro"},
		},
	}

	got := app.buildModelEnhancement()
	for _, needle := range []string{
		"DeepSeek Execution Policy",
		"Claude-Code-style execution",
		"live todo list",
		"Prefer grep -> targeted read",
		"run the narrowest verification",
		"inspect git status or git diff",
		"Established / Unknown / Next",
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("buildModelEnhancement() missing %q:\n%s", needle, got)
		}
	}
}
