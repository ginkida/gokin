package agent

import (
	"strings"
	"testing"

	"gokin/internal/client"
	"gokin/internal/config"
	"gokin/internal/tools"
)

func TestAgentHaikuSelectionUsesSameOverrideAndRejectsForeignModels(t *testing.T) {
	t.Setenv("GOKIN_SMALL_FAST_MODEL", "")
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "glm-4.7")
	t.Setenv("ANTHROPIC_SMALL_FAST_MODEL", "")
	base, err := client.NewAnthropicClient(client.AnthropicConfig{APIKey: "synthetic-key", BaseURL: "http://127.0.0.1:1", Provider: "glm", Model: config.DefaultGLMModel})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"builtin", "dynamic"} {
		t.Run(mode, func(t *testing.T) {
			var a *Agent
			if mode == "builtin" {
				a = NewAgent(AgentTypeGeneral, base, tools.NewRegistry(), t.TempDir(), 1, "haiku", nil, nil)
			} else {
				a = NewAgentWithDynamicType(&DynamicAgentType{Name: "acceptance"}, base, tools.NewRegistry(), t.TempDir(), 1, "haiku", nil, nil)
			}
			if a.definitionError != nil || a.client.GetModel() != "glm-4.7" || base.GetModel() != config.DefaultGLMModel {
				t.Fatalf("agent alias changed its parent or ignored override: model=%s err=%v", a.client.GetModel(), a.definitionError)
			}
		})
	}
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "deepseek-v4-flash")
	a := NewAgent(AgentTypeGeneral, base, tools.NewRegistry(), t.TempDir(), 1, "haiku", nil, nil)
	if _, err := a.Run(t.Context(), "must not call provider"); err == nil || !strings.Contains(err.Error(), "active provider") {
		t.Fatalf("invalid alias reached execution: %v", err)
	}
}
