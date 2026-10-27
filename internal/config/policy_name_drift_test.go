package config_test

import (
	"sort"
	"testing"

	"gokin/internal/config"
	"gokin/internal/tools"
	"gokin/internal/toolschema"
)

// The shipped default policy map is what a fresh install actually enforces, so
// a name in it that matches no tool is a rule nobody will ever hit — and the
// same typo applied to a real tool would drop that tool to the default policy
// without a single test noticing.
func TestDefaultConfigPolicyNamesOnlyRegisteredTools(t *testing.T) {
	known := map[string]bool{}
	for _, tool := range tools.DefaultRegistry(t.TempDir()).List() {
		known[tool.Name()] = true
	}
	for _, name := range tools.DefaultLazyRegistry(t.TempDir()).Names() {
		known[name] = true
	}
	if len(known) == 0 {
		t.Fatal("registry produced no tool names")
	}
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewEnvTool())
	ctx := tools.ContextWithDeferredCatalog(t.Context(), registry, registry.GeminiTools())
	for _, envelope := range toolschema.New(true).Publish(registry.GeminiTools()) {
		for _, declaration := range envelope.FunctionDeclarations {
			virtual, ok := tools.DeferredMetaTool(ctx, registry, declaration.Name)
			if !ok || virtual.Declaration().Name != declaration.Name {
				t.Fatalf("unimplemented virtual tool %q", declaration.Name)
			}
			known[declaration.Name] = true
		}
	}

	var phantom []string
	for name := range config.DefaultConfig().Permission.Rules {
		if !known[name] {
			phantom = append(phantom, name)
		}
	}
	sort.Strings(phantom)
	if len(phantom) > 0 {
		t.Fatalf("DefaultConfig permission rules name tools that are not registered: %v", phantom)
	}
}
