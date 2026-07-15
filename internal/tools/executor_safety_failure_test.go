package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

type unavailableSafetyValidator struct{}

func (unavailableSafetyValidator) ValidateSafety(context.Context, string, map[string]any) (*PreFlightCheck, error) {
	return nil, errors.New("validator crashed")
}

func (unavailableSafetyValidator) GetSummary(string, map[string]any) *ExecutionSummary {
	return nil
}

func (unavailableSafetyValidator) GetMetadata(string) (*ToolMetadata, bool) {
	return nil, false
}

func TestExecutorSafetyValidatorErrorFailsClosed(t *testing.T) {
	registry := NewRegistry()
	ran := new(bool)
	registry.MustRegister(&scriptedTool{name: "read", ran: ran})
	executor := NewExecutor(registry, nil, time.Second)
	executor.SetSafetyValidator(unavailableSafetyValidator{})

	result := executor.doExecuteTool(context.Background(), &genai.FunctionCall{Name: "read"})
	if result.Success || *ran {
		t.Fatalf("tool executed while its safety policy was unavailable: result=%+v ran=%v", result, *ran)
	}
	if !strings.Contains(result.Error, "Safety check unavailable") {
		t.Errorf("failure is not actionable: %q", result.Error)
	}
}

func TestExecutorSafetyValidatorErrorRequiresExplicitUnrestrictedMode(t *testing.T) {
	registry := NewRegistry()
	ran := new(bool)
	registry.MustRegister(&scriptedTool{name: "read", ran: ran})
	executor := NewExecutor(registry, nil, time.Second)
	executor.SetSafetyValidator(unavailableSafetyValidator{})
	executor.SetUnrestrictedMode(true)

	result := executor.doExecuteTool(context.Background(), &genai.FunctionCall{Name: "read"})
	if !result.Success || !*ran {
		t.Fatalf("explicit unrestricted mode should preserve its documented bypass: result=%+v ran=%v", result, *ran)
	}
}
