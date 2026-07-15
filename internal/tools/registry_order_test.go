package tools

import (
	"context"
	"slices"
	"sort"
	"testing"

	"google.golang.org/genai"
)

type registryOrderTool struct {
	name string
}

func (t registryOrderTool) Name() string        { return t.name }
func (t registryOrderTool) Description() string { return "ordering fixture" }
func (t registryOrderTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.name, Description: t.Description()}
}
func (t registryOrderTool) Validate(map[string]any) error { return nil }
func (t registryOrderTool) Execute(context.Context, map[string]any) (ToolResult, error) {
	return NewSuccessResult("ok"), nil
}

func declarationOrder(declarations []*genai.FunctionDeclaration) []string {
	names := make([]string, 0, len(declarations))
	for _, declaration := range declarations {
		if declaration != nil {
			names = append(names, declaration.Name)
		}
	}
	return names
}

func registeredToolOrder(registered []Tool) []string {
	names := make([]string, 0, len(registered))
	for _, tool := range registered {
		if tool != nil {
			names = append(names, tool.Name())
		}
	}
	return names
}

func assertRegistryOrder(t *testing.T, label string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s order = %v, want %v", label, got, want)
	}
}

func TestRegistrySchemaAndDiscoveryOrderIsDeterministic(t *testing.T) {
	registry := NewRegistry()
	for _, name := range []string{"zeta", "alpha", "middle"} {
		registry.MustRegister(registryOrderTool{name: name})
	}
	want := []string{"alpha", "middle", "zeta"}

	for iteration := 0; iteration < 64; iteration++ {
		assertRegistryOrder(t, "names", registry.Names(), want)
		assertRegistryOrder(t, "list", registeredToolOrder(registry.List()), want)
		assertRegistryOrder(t, "declarations", declarationOrder(registry.Declarations()), want)

		geminiTools := registry.GeminiTools()
		if len(geminiTools) != 1 {
			t.Fatalf("GeminiTools envelope count = %d, want 1", len(geminiTools))
		}
		assertRegistryOrder(t, "GeminiTools declarations", declarationOrder(geminiTools[0].FunctionDeclarations), want)
	}
}

func TestRegistryFilteredDeclarationsAreSorted(t *testing.T) {
	registry := NewRegistry()
	for _, name := range []string{"write", "skill", "read"} {
		registry.MustRegister(registryOrderTool{name: name})
	}
	want := []string{"read", "skill", "write"}

	for iteration := 0; iteration < 64; iteration++ {
		assertRegistryOrder(t, "filtered declarations", declarationOrder(registry.FilteredDeclarations(ToolSetCore)), want)
		geminiTools := registry.FilteredGeminiTools(ToolSetCore)
		assertRegistryOrder(t, "filtered Gemini declarations", declarationOrder(geminiTools[0].FunctionDeclarations), want)
	}
}

func TestLazyRegistrySchemaAndDiscoveryOrderIsDeterministic(t *testing.T) {
	registry := NewLazyRegistry()
	for _, name := range []string{"zeta", "alpha"} {
		name := name
		registry.RegisterFactory(name, func() Tool { return registryOrderTool{name: name} }, registryOrderTool{name: name}.Declaration())
	}
	registry.registerFactoryWithDeclarationProvider(
		"middle",
		func() Tool { return registryOrderTool{name: "middle"} },
		registryOrderTool{name: "middle"}.Declaration(),
		func() *genai.FunctionDeclaration { return registryOrderTool{name: "middle"}.Declaration() },
	)
	want := []string{"alpha", "middle", "zeta"}

	for iteration := 0; iteration < 64; iteration++ {
		assertRegistryOrder(t, "lazy names", registry.Names(), want)
		assertRegistryOrder(t, "lazy declarations", declarationOrder(registry.Declarations()), want)

		geminiTools := registry.GeminiTools()
		if len(geminiTools) != 1 {
			t.Fatalf("lazy GeminiTools envelope count = %d, want 1", len(geminiTools))
		}
		assertRegistryOrder(t, "lazy GeminiTools declarations", declarationOrder(geminiTools[0].FunctionDeclarations), want)
	}

	assertRegistryOrder(t, "lazy list", registeredToolOrder(registry.List()), want)
}

func TestDefaultEagerAndLazyDeclarationOrderAligns(t *testing.T) {
	workDir := t.TempDir()
	eagerRegistry := DefaultRegistry(workDir)
	eagerAll := declarationOrder(eagerRegistry.Declarations())
	eager := append([]string(nil), eagerAll...)
	lazyRegistry := DefaultLazyRegistry(workDir)
	lazy := declarationOrder(lazyRegistry.Declarations())

	// tools_list intentionally exists only in the eager registry because it
	// needs the concrete registry reference. Compare every shared declaration.
	eager = slices.DeleteFunc(eager, func(name string) bool { return name == "tools_list" })
	if !sort.StringsAreSorted(eager) || !sort.StringsAreSorted(lazy) {
		t.Fatalf("declaration schemas are not sorted: eager=%v lazy=%v", eager, lazy)
	}
	assertRegistryOrder(t, "eager/lazy declarations", lazy, eager)

	for iteration := 0; iteration < 16; iteration++ {
		assertRegistryOrder(t, "repeated eager declarations", declarationOrder(eagerRegistry.Declarations()), eagerAll)
		assertRegistryOrder(t, "repeated lazy declarations", declarationOrder(lazyRegistry.Declarations()), lazy)
	}
}
