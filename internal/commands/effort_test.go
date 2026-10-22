package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gokin/internal/config"
)

func TestEffortCommandAppliesAndRejectsUnsupportedValues(t *testing.T) {
	app := newThinkingApp(&config.Config{Model: config.ModelConfig{Name: "glm-5.3-flash"}})
	command := &EffortCommand{}
	for _, value := range []string{"low", " HIGH ", "max", "auto"} {
		out, err := command.Execute(context.Background(), []string{value}, app)
		if err != nil || strings.Contains(out, "Failed") {
			t.Fatalf("%s: %s, %v", value, out, err)
		}
		if app.applied.Model.ReasoningEffort != strings.ToLower(strings.TrimSpace(value)) {
			t.Fatalf("effort was not applied: %+v", app.applied.Model)
		}
	}
	before := app.applyCalls
	if out, _ := command.Execute(context.Background(), []string{"medium"}, app); !strings.Contains(out, "Unknown") {
		t.Fatalf("invalid effort accepted: %s", out)
	}
	if app.applyCalls != before {
		t.Fatal("invalid effort reached ApplyConfig")
	}
	app.cfg.Model.Name = "glm-5.2"
	if out, _ := command.Execute(context.Background(), []string{"max"}, app); !strings.Contains(out, "require") {
		t.Fatalf("unsupported model accepted effort: %s", out)
	}
	if app.applyCalls != before {
		t.Fatal("unsupported model reached ApplyConfig")
	}
}

func TestEffortCommandReportsApplyFailure(t *testing.T) {
	app := newThinkingApp(&config.Config{Model: config.ModelConfig{Name: "glm-5.3"}})
	app.applyErr = errors.New("config conflict")
	out, _ := (&EffortCommand{}).Execute(context.Background(), []string{"max"}, app)
	if !strings.Contains(out, "Failed to save: config conflict") {
		t.Fatalf("failure hidden: %s", out)
	}
}

func TestThinkingGLM53ReportsMandatoryReasoning(t *testing.T) {
	app := newThinkingApp(&config.Config{Model: config.ModelConfig{Name: "glm-5.3"}})
	for _, mode := range []string{"off", "on", "auto", "16384"} {
		out, _ := (&ThinkingCommand{}).Execute(context.Background(), []string{mode}, app)
		if !strings.Contains(out, "always on") || strings.Contains(out, "never reasons") {
			t.Fatalf("%s makes incorrect GLM claim: %s", mode, out)
		}
		if mode == "off" && !strings.Contains(out, "effort: low") {
			t.Fatalf("off must mean low: %s", out)
		}
	}
	app.cfg.Model.ReasoningEffort = "max"
	out, _ := (&ThinkingCommand{}).Execute(context.Background(), []string{"off"}, app)
	if !strings.Contains(out, "effort: max") {
		t.Fatalf("explicit effort priority hidden: %s", out)
	}
}
