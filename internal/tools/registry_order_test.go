package tools

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"
)

type registryOrderTool struct {
	name string
}

type countingStaticDeclarationTool struct {
	name  string
	calls int
}

func (t *countingStaticDeclarationTool) Name() string        { return t.name }
func (t *countingStaticDeclarationTool) Description() string { return "static fixture" }
func (t *countingStaticDeclarationTool) Declaration() *genai.FunctionDeclaration {
	t.calls++
	return &genai.FunctionDeclaration{Name: t.name, Description: t.Description()}
}
func (t *countingStaticDeclarationTool) Validate(map[string]any) error { return nil }
func (t *countingStaticDeclarationTool) Execute(context.Context, map[string]any) (ToolResult, error) {
	return NewSuccessResult("ok"), nil
}

type countingDynamicDeclarationTool struct {
	countingStaticDeclarationTool
}

func (*countingDynamicDeclarationTool) runtimeDynamicDeclaration() {}

type registeringDeclarationTool struct {
	name     string
	registry *Registry
	once     sync.Once
}

type registeringLazyDeclarationTool struct {
	name     string
	registry *LazyRegistry
	once     sync.Once
}

type publishedNameTool struct {
	registryName  string
	publishedName string
}

func (t publishedNameTool) Name() string        { return t.registryName }
func (t publishedNameTool) Description() string { return "published-name fixture" }
func (t publishedNameTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.publishedName, Description: t.Description()}
}
func (t publishedNameTool) Validate(map[string]any) error { return nil }
func (t publishedNameTool) Execute(context.Context, map[string]any) (ToolResult, error) {
	return NewSuccessResult("ok"), nil
}

func (t *registeringDeclarationTool) Name() string        { return t.name }
func (t *registeringDeclarationTool) Description() string { return "registering fixture" }
func (t *registeringDeclarationTool) Declaration() *genai.FunctionDeclaration {
	t.once.Do(func() { t.registry.MustRegister(registryOrderTool{name: "late"}) })
	return &genai.FunctionDeclaration{Name: t.name, Description: t.Description()}
}
func (t *registeringDeclarationTool) Validate(map[string]any) error { return nil }
func (t *registeringDeclarationTool) Execute(context.Context, map[string]any) (ToolResult, error) {
	return NewSuccessResult("ok"), nil
}

func (t *registeringLazyDeclarationTool) Name() string        { return t.name }
func (t *registeringLazyDeclarationTool) Description() string { return "registering lazy fixture" }
func (t *registeringLazyDeclarationTool) Declaration() *genai.FunctionDeclaration {
	t.once.Do(func() { t.registry.MustRegister(registryOrderTool{name: "late"}) })
	return &genai.FunctionDeclaration{Name: t.name, Description: t.Description()}
}
func (t *registeringLazyDeclarationTool) Validate(map[string]any) error { return nil }
func (t *registeringLazyDeclarationTool) Execute(context.Context, map[string]any) (ToolResult, error) {
	return NewSuccessResult("ok"), nil
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

func TestSortDeclarationsByNameFallsBackForMismatchedPublishedNames(t *testing.T) {
	declarations := []*genai.FunctionDeclaration{
		{Name: "zeta"}, nil, {Name: "alpha"}, {Name: "middle"}, {Name: "alpha"},
	}
	sortDeclarationsByName(declarations)
	if declarations[len(declarations)-1] != nil {
		t.Fatalf("nil declaration was not sorted last: %#v", declarations)
	}
	assertRegistryOrder(t, "mismatched published declarations",
		declarationOrder(declarations), []string{"alpha", "alpha", "middle", "zeta"})
}

func TestRegistryDeclarationSnapshotCacheTracksMutationsAndFreeze(t *testing.T) {
	registry := NewRegistry()
	static := &countingStaticDeclarationTool{name: "alpha"}
	registry.MustRegister(static)

	first := registry.cachedDeclarationSnapshots()
	second := registry.cachedDeclarationSnapshots()
	if len(first) != 1 || len(second) != 1 || &first[0] != &second[0] {
		t.Fatal("unchanged registry did not reuse its immutable declaration snapshot")
	}

	registry.MustRegister(registryOrderTool{name: "beta"})
	registered := registry.cachedDeclarationSnapshots()
	if len(registered) != 2 || registered[0].name != "alpha" || registered[1].name != "beta" ||
		&registered[0] == &first[0] {
		t.Fatalf("register did not replace the ordered declaration snapshot: %#v", registered)
	}

	if !registry.Unregister("beta") {
		t.Fatal("registered fixture was not removed")
	}
	removed := registry.cachedDeclarationSnapshots()
	if len(removed) != 1 || removed[0].name != "alpha" || &removed[0] == &registered[0] {
		t.Fatalf("unregister retained a stale declaration snapshot: %#v", removed)
	}

	registry.freezeDefaultDeclarations()
	frozen := registry.cachedDeclarationSnapshots()
	if len(frozen) != 1 || frozen[0].declaration == nil || frozen[0].declaration.Name != "alpha" {
		t.Fatalf("freeze did not publish frozen declaration pointers: %#v", frozen)
	}
}

func TestRegistryDiscoverySnapshotsAreDefensiveCopies(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(registryOrderTool{name: "alpha"})
	registry.MustRegister(registryOrderTool{name: "beta"})

	names := registry.Names()
	names[0] = "tampered"
	assertRegistryOrder(t, "names after caller mutation", registry.Names(), []string{"alpha", "beta"})

	registered := registry.List()
	registered[0] = registryOrderTool{name: "tampered"}
	assertRegistryOrder(t, "list after caller mutation",
		registeredToolOrder(registry.List()), []string{"alpha", "beta"})
}

func TestRegistryDeclarationRunsOutsideSnapshotLockAndPublishesLaterMutation(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(&registeringDeclarationTool{name: "first", registry: registry})

	done := make(chan []*genai.FunctionDeclaration, 1)
	go func() { done <- registry.Declarations() }()
	select {
	case first := <-done:
		assertRegistryOrder(t, "consistent first snapshot", declarationOrder(first), []string{"first"})
	case <-time.After(2 * time.Second):
		t.Fatal("dynamic Declaration deadlocked while registering another tool")
	}
	assertRegistryOrder(t, "next snapshot after nested register",
		declarationOrder(registry.Declarations()), []string{"first", "late"})
}

func TestRegistryFreezesOnlyExplicitlyImmutableBuiltins(t *testing.T) {
	registry := NewRegistry()
	static := &countingStaticDeclarationTool{name: "static"}
	dynamic := &countingDynamicDeclarationTool{
		countingStaticDeclarationTool: countingStaticDeclarationTool{name: "dynamic"},
	}
	registry.MustRegister(static)
	registry.MustRegister(dynamic)
	registry.freezeDefaultDeclarations()
	if static.calls != 1 || dynamic.calls != 0 {
		t.Fatalf("freeze calls: static=%d dynamic=%d", static.calls, dynamic.calls)
	}
	for range 3 {
		_ = registry.Declarations()
	}
	if static.calls != 1 || dynamic.calls != 3 {
		t.Fatalf("declaration calls: static=%d dynamic=%d", static.calls, dynamic.calls)
	}
	registry.Unregister("static")
	if _, retained := registry.staticDeclarations["static"]; retained {
		t.Fatal("unregister retained frozen declaration")
	}
}

func TestRegistryDoesNotFreezeToolsRegisteredAfterDefaultSnapshot(t *testing.T) {
	registry := NewRegistry()
	registry.freezeDefaultDeclarations()
	late := &countingStaticDeclarationTool{name: "late"}
	registry.MustRegister(late)

	for range 3 {
		_ = registry.Declarations()
	}
	if late.calls != 3 {
		t.Fatalf("late-registered declaration calls = %d, want 3", late.calls)
	}
}

func TestDefaultRegistryLeavesRuntimeDynamicSchemasUnfrozen(t *testing.T) {
	registry := DefaultRegistry(t.TempDir())
	for _, name := range []string{"bash", "task", "skill"} {
		if _, frozen := registry.staticDeclarations[name]; frozen {
			t.Fatalf("runtime-dynamic declaration %q was frozen", name)
		}
	}
	for _, name := range []string{"read", "repl_exec", "harness"} {
		if _, frozen := registry.staticDeclarations[name]; !frozen {
			t.Fatalf("immutable declaration %q was not frozen", name)
		}
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

func TestGeminiToolsExcludingUsesPublishedNameAndSortedOutput(t *testing.T) {
	registry := NewRegistry()
	for _, fixture := range []publishedNameTool{
		{registryName: "alpha", publishedName: "zeta"},
		{registryName: "beta", publishedName: "hidden"},
		{registryName: "gamma", publishedName: "aardvark"},
	} {
		registry.MustRegister(fixture)
	}
	schema := registry.GeminiToolsExcluding(map[string]bool{"hidden": true})
	if len(schema) != 1 {
		t.Fatalf("schema envelope count = %d, want 1", len(schema))
	}
	assertRegistryOrder(t, "published-name exclusion",
		declarationOrder(schema[0].FunctionDeclarations), []string{"aardvark", "zeta"})
}

func TestToolSetMembershipIndexMatchesDefinitions(t *testing.T) {
	if len(toolSetMasks) != len(toolSetDefinitions) {
		t.Fatalf("tool-set mask count = %d, definitions = %d", len(toolSetMasks), len(toolSetDefinitions))
	}
	expected := make(map[string]toolSetMask)
	var seen toolSetMask
	for set, names := range toolSetDefinitions {
		mask := toolSetMasks[set]
		if mask == 0 {
			t.Errorf("tool set %q has no membership mask", set)
			continue
		}
		if mask&(mask-1) != 0 || seen&mask != 0 {
			t.Errorf("tool set %q has non-unique single-bit mask %016b", set, mask)
			continue
		}
		seen |= mask
		for _, name := range names {
			expected[name] |= mask
		}
	}
	if !maps.Equal(toolSetMembershipByName, expected) {
		t.Fatalf("tool-set membership index drifted: got=%v want=%v", toolSetMembershipByName, expected)
	}

	registry := NewRegistry()
	registry.MustRegister(registryOrderTool{name: "read"})
	if got := registry.FilteredDeclarations(ToolSet("unknown")); len(got) != 0 {
		t.Fatalf("unknown tool set exposed declarations: %v", declarationOrder(got))
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

func TestLazyRegistryDiscoverySnapshotCachesAndInvalidates(t *testing.T) {
	registry := NewLazyRegistry()
	registry.RegisterFactory("alpha",
		func() Tool { return registryOrderTool{name: "alpha"} },
		registryOrderTool{name: "alpha"}.Declaration())

	first := registry.cachedDiscoverySnapshots()
	second := registry.cachedDiscoverySnapshots()
	if len(first) != 1 || len(second) != 1 || &first[0] != &second[0] {
		t.Fatal("unchanged lazy registry did not reuse its immutable discovery snapshot")
	}

	registry.RegisterFactory("beta",
		func() Tool { return registryOrderTool{name: "beta"} },
		registryOrderTool{name: "beta"}.Declaration())
	registered := registry.cachedDiscoverySnapshots()
	if len(registered) != 2 || registered[0].name != "alpha" || registered[1].name != "beta" ||
		&registered[0] == &first[0] {
		t.Fatalf("factory registration retained a stale discovery snapshot: %#v", registered)
	}

	if err := registry.Register(registryOrderTool{name: "gamma"}); err != nil {
		t.Fatalf("register instantiated tool: %v", err)
	}
	instantiated := registry.cachedDiscoverySnapshots()
	if len(instantiated) != 3 || instantiated[2].name != "gamma" || &instantiated[0] == &registered[0] {
		t.Fatalf("instantiated registration retained a stale discovery snapshot: %#v", instantiated)
	}
}

func TestLazyRegistryDiscoveryResultsAreDefensiveCopies(t *testing.T) {
	registry := NewLazyRegistry()
	for _, name := range []string{"alpha", "beta"} {
		name := name
		registry.RegisterFactory(name,
			func() Tool { return registryOrderTool{name: name} },
			registryOrderTool{name: name}.Declaration())
	}

	names := registry.Names()
	names[0] = "tampered"
	assertRegistryOrder(t, "lazy names after caller mutation", registry.Names(), []string{"alpha", "beta"})

	registered := registry.List()
	registered[0] = registryOrderTool{name: "tampered"}
	assertRegistryOrder(t, "lazy list after caller mutation",
		registeredToolOrder(registry.List()), []string{"alpha", "beta"})
}

func TestLazyRegistryDynamicProvidersRemainLive(t *testing.T) {
	registry := NewLazyRegistry()
	calls := 0
	registry.registerFactoryWithDeclarationProvider(
		"dynamic",
		func() Tool { return registryOrderTool{name: "dynamic"} },
		registryOrderTool{name: "dynamic"}.Declaration(),
		func() *genai.FunctionDeclaration {
			calls++
			return registryOrderTool{name: "dynamic"}.Declaration()
		},
	)

	for range 3 {
		assertRegistryOrder(t, "live lazy declaration",
			declarationOrder(registry.Declarations()), []string{"dynamic"})
	}
	if calls != 3 {
		t.Fatalf("dynamic provider calls = %d, want 3", calls)
	}
}

func TestLazyRegistryRegisterDeclarationMayRegisterAnotherTool(t *testing.T) {
	registry := NewLazyRegistry()
	tool := &registeringLazyDeclarationTool{name: "first", registry: registry}
	done := make(chan error, 1)
	go func() { done <- registry.Register(tool) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("register dynamic tool: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lazy Register deadlocked while resolving a re-entrant declaration")
	}
	assertRegistryOrder(t, "nested lazy registration", registry.Names(), []string{"first", "late"})
	assertRegistryOrder(t, "nested lazy declarations",
		declarationOrder(registry.Declarations()), []string{"first", "late"})
}

func TestLazyRegistrySortsByPublishedToolAndDeclarationNames(t *testing.T) {
	registry := NewLazyRegistry()
	for _, fixture := range []struct {
		key  string
		tool publishedNameTool
	}{
		{key: "alpha", tool: publishedNameTool{registryName: "zeta", publishedName: "zeta"}},
		{key: "beta", tool: publishedNameTool{registryName: "aardvark", publishedName: "aardvark"}},
	} {
		fixture := fixture
		registry.RegisterFactory(fixture.key,
			func() Tool { return fixture.tool }, fixture.tool.Declaration())
	}

	assertRegistryOrder(t, "lazy internal names", registry.Names(), []string{"alpha", "beta"})
	assertRegistryOrder(t, "lazy published declarations",
		declarationOrder(registry.Declarations()), []string{"aardvark", "zeta"})
	assertRegistryOrder(t, "lazy published tools",
		registeredToolOrder(registry.List()), []string{"aardvark", "zeta"})
}

func BenchmarkRegistryDiscoverySnapshots(b *testing.B) {
	registry := DefaultRegistry(b.TempDir())
	b.Run("names", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if len(registry.Names()) == 0 {
				b.Fatal("empty registry")
			}
		}
	})
	b.Run("list", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if len(registry.List()) == 0 {
				b.Fatal("empty registry")
			}
		}
	})
	b.Run("filtered_core", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if len(registry.FilteredDeclarations(ToolSetCore)) == 0 {
				b.Fatal("empty core schema")
			}
		}
	})
	b.Run("filtered_static_sets", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if len(registry.FilteredDeclarations(ToolSetGit, ToolSetAdvanced)) == 0 {
				b.Fatal("empty static schema")
			}
		}
	})
	b.Run("excluding_features", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			schema := registry.GeminiToolsExcluding(PlanModeControlToolNames)
			if len(schema) == 0 || len(schema[0].FunctionDeclarations) == 0 {
				b.Fatal("empty feature-filtered schema")
			}
		}
	})
}

func BenchmarkLazyRegistrySnapshots(b *testing.B) {
	registry := NewLazyRegistry()
	for index := 0; index < 64; index++ {
		name := fmt.Sprintf("tool-%02d", index)
		registry.RegisterFactory(name,
			func() Tool { return registryOrderTool{name: name} },
			registryOrderTool{name: name}.Declaration())
	}
	_ = registry.List()

	b.Run("names", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if len(registry.Names()) != 64 {
				b.Fatal("lazy registry name count drifted")
			}
		}
	})
	b.Run("declarations", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if len(registry.Declarations()) != 64 {
				b.Fatal("lazy registry declaration count drifted")
			}
		}
	})
	b.Run("list", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if len(registry.List()) != 64 {
				b.Fatal("lazy registry list count drifted")
			}
		}
	})
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
