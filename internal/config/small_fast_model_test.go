package config

import (
	"strings"
	"testing"
)

func TestSmallFastModelOverridePriorityAndProvider(t *testing.T) {
	for _, test := range []struct{ name, provider, current, gokin, haiku, legacy, want, problem string }{
		{name: "glm-default", provider: "glm", current: DefaultGLMModel, want: GLMFlashModel},
		{name: "other-provider", provider: "deepseek", current: "deepseek-v4-pro", want: "deepseek-v4-pro"},
		{name: "native-priority", provider: "glm", current: DefaultGLMModel, gokin: "glm-4.7", haiku: GLMFlashModel, legacy: "glm-5.2", want: "glm-4.7"},
		{name: "compatibility-priority", provider: "glm", current: DefaultGLMModel, haiku: "glm-4.7", legacy: "glm-5.2", want: "glm-4.7"},
		{name: "legacy", provider: "glm", current: DefaultGLMModel, legacy: "glm-5.2", want: "glm-5.2"},
		{name: "gateway", provider: "glm", current: DefaultGLMModel, haiku: "gateway/small-v2", want: "gateway/small-v2"},
		{name: "local-model-name", provider: "ollama", current: "qwen-coder", haiku: DefaultGLMModel, want: DefaultGLMModel},
		{name: "foreign-provider", provider: "glm", current: DefaultGLMModel, haiku: "deepseek-v4-flash", problem: "active provider"},
		{name: "claude-on-glm", provider: "glm", current: DefaultGLMModel, haiku: "claude-haiku-4-5", problem: "active provider"},
		{name: "recursive-alias", provider: "glm", current: DefaultGLMModel, haiku: "haiku", problem: "full model ID"},
		{name: "control-character", provider: "glm", current: DefaultGLMModel, haiku: "gateway\nmodel", problem: "control"},
		{name: "no-provider", current: DefaultGLMModel, haiku: "gateway-model", problem: "identified"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GOKIN_SMALL_FAST_MODEL", test.gokin)
			t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", test.haiku)
			t.Setenv("ANTHROPIC_SMALL_FAST_MODEL", test.legacy)
			model, err := SmallFastModel(test.provider, test.current)
			if test.problem != "" {
				if err == nil || !strings.Contains(err.Error(), test.problem) {
					t.Fatalf("model=%s err=%v", model, err)
				}
				return
			}
			if err != nil || model != test.want {
				t.Fatalf("model=%s err=%v want=%s", model, err, test.want)
			}
		})
	}
}

func TestNormalizeHaikuUsesActiveProvider(t *testing.T) {
	t.Setenv("GOKIN_SMALL_FAST_MODEL", "")
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "gateway-fast")
	t.Setenv("ANTHROPIC_SMALL_FAST_MODEL", "")
	cfg := DefaultConfig()
	cfg.Model.Name = "haiku"
	cfg.Model.Preset = ""
	cfg.Model.Provider = "glm"
	if err := NormalizeConfig(cfg); err != nil || cfg.Model.Name != "gateway-fast" || cfg.Model.Provider != "glm" {
		t.Fatalf("alias was not normalized in memory: %+v %v", cfg.Model, err)
	}
}

func TestRetiredDeepSeekModelsResolveToCurrentFlash(t *testing.T) {
	for _, old := range []string{"deepseek-chat", "deepseek-reasoner"} {
		model, err := ResolveModelAlias("deepseek", old, old)
		if err != nil || model != "deepseek-flash" {
			t.Fatalf("%s: model=%q err=%v", old, model, err)
		}
	}
}
