package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gokin/internal/config"
)

func TestExplicitAuxiliaryModelPreservesGuardAndOverridesInvalidDefault(t *testing.T) {
	t.Setenv("GOKIN_SMALL_FAST_MODEL", "deepseek-v4-flash")
	base, _ := NewAnthropicClient(AnthropicConfig{APIKey: "synthetic-key", BaseURL: "http://127.0.0.1:1", Provider: "glm", Model: config.DefaultGLMModel})
	revoked := errors.New("authority revoked")
	guarded := WithRequestGuard(base, func(context.Context) error { return revoked })
	clone, isolated, err := CloneForAuxiliaryWithModel(guarded, "glm-4.7")
	if err != nil || !isolated || clone.GetModel() != "glm-4.7" || base.GetModel() != config.DefaultGLMModel {
		t.Fatalf("explicit auxiliary model lost isolation: %T %v", clone, err)
	}
	if _, err := SendAuxiliaryMessage(t.Context(), clone, "must not send"); !errors.Is(err, revoked) {
		t.Fatalf("auxiliary clone lost execution guard: %v", err)
	}
}

func TestAuxiliaryOverrideIsIsolatedAndKeepsFallbackTransports(t *testing.T) {
	t.Setenv("GOKIN_SMALL_FAST_MODEL", "")
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "glm-4.7")
	t.Setenv("ANTHROPIC_SMALL_FAST_MODEL", "")
	primary, _ := NewAnthropicClient(AnthropicConfig{APIKey: "synthetic-key", BaseURL: "https://glm.example.test", Provider: "glm", Model: config.DefaultGLMModel})
	secondary, _ := NewAnthropicClient(AnthropicConfig{APIKey: "synthetic-key", BaseURL: "https://deepseek.example.test", Provider: "deepseek", Model: "deepseek-v4-pro"})
	fallback, err := NewFallbackClient([]Client{primary, secondary}, []string{"glm", "deepseek"})
	if err != nil {
		t.Fatal(err)
	}
	clone, ok := CloneForAuxiliary(fallback)
	if !ok || AuxiliaryModelError(clone) != nil || clone.GetModel() != "glm-4.7" || clone.(ProviderIdentity).GetProvider() != "glm" {
		t.Fatalf("wrong auxiliary choice: %T %v", clone, ok)
	}
	aux := clone.(*FallbackClient)
	if aux.clients[1].GetModel() != "deepseek-v4-pro" || primary.GetModel() != config.DefaultGLMModel || secondary.GetModel() != "deepseek-v4-pro" {
		t.Fatal("override changed a foreground or fallback model")
	}
	if aux.clients[0].(*AnthropicClient).config.BaseURL != primary.config.BaseURL {
		t.Fatal("override changed transport")
	}
	model, _, err := ResolveSkillModelSettings(fallback, "haiku", "")
	if err != nil || model != "glm-4.7" {
		t.Fatalf("skill alias ignored override: %s %v", model, err)
	}
	model, _, err = ResolveSkillModelSettings(fallback, config.DefaultGLMModel, "")
	if err != nil || model != config.DefaultGLMModel {
		t.Fatal("explicit skill model was overridden")
	}
}

func TestAuxiliaryInvalidOverrideFailsBeforeSending(t *testing.T) {
	t.Setenv("GOKIN_SMALL_FAST_MODEL", "")
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "deepseek-v4-flash")
	t.Setenv("ANTHROPIC_SMALL_FAST_MODEL", "")
	base, _ := NewAnthropicClient(AnthropicConfig{APIKey: "synthetic-key", BaseURL: "https://no-network.example.test", Provider: "glm", Model: config.DefaultGLMModel})
	clone, ok := CloneForAuxiliary(base)
	if !ok || AuxiliaryModelError(clone) == nil {
		t.Fatal("bad override did not retain an actionable error")
	}
	if _, err := SendAuxiliaryMessage(t.Context(), clone, "must not send"); err == nil || !strings.Contains(err.Error(), "active provider") {
		t.Fatalf("bad override reached the transport: %v", err)
	}
	if base.GetModel() != config.DefaultGLMModel {
		t.Fatal("invalid override changed main model")
	}
}

func TestSameProviderModelSelectionCannotFollowForeignNamedLocalModel(t *testing.T) {
	local, err := NewOllamaClient(OllamaConfig{BaseURL: "http://127.0.0.1:11434", Model: "qwen-coder"})
	if err != nil {
		t.Fatal(err)
	}
	remote, _ := NewAnthropicClient(AnthropicConfig{APIKey: "synthetic-key", Provider: "glm", Model: config.DefaultGLMModel})
	fallback, err := NewFallbackClient([]Client{local, remote}, []string{"ollama", "glm"})
	if err != nil {
		t.Fatal(err)
	}
	selected := WithModelOnCurrentProvider(fallback, config.GLMFlashModel)
	if selected.(ProviderIdentity).GetProvider() != "ollama" || selected.GetModel() != config.GLMFlashModel || fallback.GetModel() != "qwen-coder" {
		t.Fatal("local model name switched transport or mutated main client")
	}
}
