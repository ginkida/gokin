package app

import (
	"context"
	"testing"

	"gokin/internal/config"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

type countingBuilderSchemaTool struct {
	declarationCalls int
}

func (*countingBuilderSchemaTool) Name() string        { return "read" }
func (*countingBuilderSchemaTool) Description() string { return "builder schema fixture" }
func (t *countingBuilderSchemaTool) Declaration() *genai.FunctionDeclaration {
	t.declarationCalls++
	return &genai.FunctionDeclaration{Name: t.Name(), Description: t.Description()}
}
func (*countingBuilderSchemaTool) Validate(map[string]any) error { return nil }
func (*countingBuilderSchemaTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	return tools.NewSuccessResult("ok"), nil
}

func TestBuilderSelectToolSetsBuildsDynamicSchemaOnce(t *testing.T) {
	for _, backend := range []string{"anthropic", "ollama"} {
		t.Run(backend, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.API.Backend = backend
			builder := NewBuilder(cfg, t.TempDir())
			defer builder.cancel()

			registry := tools.NewRegistry()
			fixture := &countingBuilderSchemaTool{}
			registry.MustRegister(fixture)
			builder.registry = registry

			schema := builder.selectToolSets()
			if fixture.declarationCalls != 1 {
				t.Fatalf("dynamic Declaration calls = %d, want one schema build", fixture.declarationCalls)
			}
			if count := toolSchemaDeclarationCount(schema); count != 1 {
				t.Fatalf("selected declaration count = %d, want 1", count)
			}
		})
	}
}

func TestToolSchemaDeclarationCountHandlesMultipleAndNilEnvelopes(t *testing.T) {
	schema := []*genai.Tool{
		nil,
		{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "read"}}},
		{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "grep"}, {Name: "glob"}}},
	}
	if count := toolSchemaDeclarationCount(schema); count != 3 {
		t.Fatalf("declaration count = %d, want 3", count)
	}
}
