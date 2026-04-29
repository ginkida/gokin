package tools

import (
	"sort"
	"strings"
	"testing"
)

// TestEagerVsLazyRegistryAlignment is the structural guard for the same
// "registration drift" bug class that v0.78.14 fixed for slash commands.
//
// The codebase has two parallel tool registries:
//   - DefaultRegistry (eager): instantiates every tool at startup
//   - DefaultLazyRegistry (deferred): registers factories that build on
//     first request — used for memory-light boot in sub-agents
//
// Pre-fix pattern (hypothetical): a contributor adds a new tool, wires
// it into DefaultRegistry but forgets DefaultLazyRegistry. Main agent
// sees the tool; sub-agents don't. The bug manifests as "this tool
// works in main but missing in delegated tasks", which is hard to
// triage without reading both registries side-by-side.
//
// This test cross-references both at runtime. Tools registered eagerly
// must also have a lazy factory, and vice versa, with documented
// exceptions for tools whose factory needs the registry itself
// (cyclic-dep avoidance).
func TestEagerVsLazyRegistryAlignment(t *testing.T) {
	// Tools that intentionally exist only in the eager registry. Each
	// entry needs a justification — silent drift is the bug class this
	// test exists to prevent.
	eagerOnly := map[string]string{
		"tools_list": "needs *Registry reference to enumerate tools — would create a cyclic dependency in the LazyRegistry's factory closure",
	}

	eager := DefaultRegistry(t.TempDir())
	lazy := DefaultLazyRegistry(t.TempDir())

	eagerNames := map[string]bool{}
	for _, tool := range eager.List() {
		eagerNames[tool.Name()] = true
	}

	// Use Names() instead of List() — the latter instantiates every tool,
	// which is heavy and unnecessary for a name-set comparison.
	lazyNames := map[string]bool{}
	for _, name := range lazy.Names() {
		lazyNames[name] = true
	}

	// Direction 1: eager → lazy
	var missingFromLazy []string
	for name := range eagerNames {
		if lazyNames[name] {
			continue
		}
		if _, allowed := eagerOnly[name]; allowed {
			continue
		}
		missingFromLazy = append(missingFromLazy, name)
	}

	// Direction 2: lazy → eager
	var missingFromEager []string
	for name := range lazyNames {
		if !eagerNames[name] {
			missingFromEager = append(missingFromEager, name)
		}
	}

	if len(missingFromLazy) > 0 {
		sort.Strings(missingFromLazy)
		t.Errorf("tools registered in DefaultRegistry but missing from DefaultLazyRegistry:\n  %s\n\n"+
			"Either:\n"+
			"  1. Add r.RegisterFactory(name, func() Tool { return NewXTool(workDir) }, declarations[name]) in DefaultLazyRegistry, OR\n"+
			"  2. Add to eagerOnly map in this test with a justification.",
			strings.Join(missingFromLazy, "\n  "))
	}
	if len(missingFromEager) > 0 {
		sort.Strings(missingFromEager)
		t.Errorf("tools registered in DefaultLazyRegistry but missing from DefaultRegistry:\n  %s",
			strings.Join(missingFromEager, "\n  "))
	}
}

// TestEveryRegisteredToolHasDeclaration — a tool without a declaration
// is invisible to the LLM (no schema for the function-calling protocol)
// even if registered. Catches the case where someone adds a new tool
// to one of the registries but forgets the declaration entry.
func TestEveryRegisteredToolHasDeclaration(t *testing.T) {
	decls := GetAllDeclarations()

	declNames := map[string]bool{}
	for name := range decls {
		declNames[name] = true
	}

	// Tools whose declaration intentionally lives elsewhere or doesn't
	// participate in LLM function-calling — currently empty. If a tool
	// genuinely has no declaration on purpose, document it here.
	noDeclaration := map[string]string{}

	r := DefaultRegistry(t.TempDir())

	var missing []string
	for _, tool := range r.List() {
		name := tool.Name()
		if declNames[name] {
			continue
		}
		if _, allowed := noDeclaration[name]; allowed {
			continue
		}
		missing = append(missing, name)
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("tools registered in DefaultRegistry but missing from GetAllDeclarations():\n  %s\n\n"+
			"Add entry to the map returned by GetAllDeclarations() in declarations.go — without it the LLM has no schema to call this tool.",
			strings.Join(missing, "\n  "))
	}
}

// TestEveryToolSetMemberIsRegistered — toolSetDefinitions is the source
// of truth for which tools each model tier sees in its function-calling
// schema. A name listed in a ToolSet but missing from DefaultRegistry
// silently drops out of FilteredDeclarations — same silent-capability
// failure mode as v0.78.27, just one layer up. Catches the case where
// someone adds a tool to a ToolSet but forgets the eager registration.
func TestEveryToolSetMemberIsRegistered(t *testing.T) {
	r := DefaultRegistry(t.TempDir())
	registered := map[string]bool{}
	for _, tool := range r.List() {
		registered[tool.Name()] = true
	}

	type miss struct {
		set  ToolSet
		name string
	}
	var missing []miss
	for set, names := range toolSetDefinitions {
		for _, name := range names {
			if !registered[name] {
				missing = append(missing, miss{set, name})
			}
		}
	}

	if len(missing) > 0 {
		var lines []string
		for _, m := range missing {
			lines = append(lines, string(m.set)+" → "+m.name)
		}
		sort.Strings(lines)
		t.Errorf("toolSetDefinitions list tools that aren't registered (silent capability failure — LLM sees schema but tool returns 'not found'):\n  %s",
			strings.Join(lines, "\n  "))
	}
}

// TestNoDeadDeclarations — a declaration without a tool registration is
// dead weight — the LLM gets a schema for a function it can't call,
// resulting in "tool not found" errors mid-task. Catches cleanup drift
// where a tool was removed but its declaration entry was forgotten.
func TestNoDeadDeclarations(t *testing.T) {
	decls := GetAllDeclarations()

	r := DefaultRegistry(t.TempDir())
	registered := map[string]bool{}
	for _, tool := range r.List() {
		registered[tool.Name()] = true
	}

	var dead []string
	for name := range decls {
		if !registered[name] {
			dead = append(dead, name)
		}
	}

	if len(dead) > 0 {
		sort.Strings(dead)
		t.Errorf("declarations exist for tools that aren't registered (dead schemas exposed to LLM):\n  %s",
			strings.Join(dead, "\n  "))
	}
}
