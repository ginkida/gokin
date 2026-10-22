package config

import "testing"

func TestGLMAlwaysOnReasoningModelBoundary(t *testing.T) {
	for _, model := range []string{"glm-5.3", " GLM-5.3 ", "glm-5.3-flash", "glm-5.3-flashx", "glm-5.3[1m]", "glm-5.3:latest"} {
		if !GLMAlwaysOnReasoning(model) {
			t.Errorf("%q should require reasoning", model)
		}
	}
	for _, model := range []string{"glm-5.2", "glm-5.30", "glm-5", "glm-4.7", "k3", ""} {
		if GLMAlwaysOnReasoning(model) {
			t.Errorf("%q must not inherit GLM-5.3 policy", model)
		}
	}
}

func TestReasoningEffortConfigValidationAndClone(t *testing.T) {
	for _, value := range []string{"", "auto", "low", " HIGH ", "max"} {
		cfg := DefaultConfig()
		cfg.API.GLMKey = "test-key"
		cfg.Model.ReasoningEffort = value
		if err := NormalizeConfig(cfg); err != nil {
			t.Fatal(err)
		}
		if err := cfg.Validate(); err != nil {
			t.Fatal(err)
		}
		original := cfg.Model.ReasoningEffort
		clone := cfg.Clone()
		clone.Model.ReasoningEffort = "low"
		if cfg.Model.ReasoningEffort != original {
			t.Fatal("clone mutated source config")
		}
	}
	cfg := DefaultConfig()
	cfg.Model.ReasoningEffort = "medium"
	if err := NormalizeConfig(cfg); err == nil {
		t.Fatal("normalization accepted unsupported effort")
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("validation accepted unsupported effort")
	}
}
