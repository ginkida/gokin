package app

import (
	"bytes"
	"encoding/json"
	"testing"

	"gokin/internal/config"
	"gokin/internal/hybrid"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

func TestAdaptiveHybridSchemaHasZeroOrdinaryPayloadTax(t *testing.T) {
	registry := tools.DefaultRegistry(t.TempDir())
	newApp := func(mode string) *App {
		cfg := config.DefaultConfig()
		cfg.Engine.Mode = mode
		return &App{config: cfg, registry: registry}
	}
	marshal := func(schema []*genai.Tool) []byte {
		t.Helper()
		payload, err := json.Marshal(schema)
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}

	autoApp := newApp("auto")
	toolsApp := newApp("tools")
	ordinarySchema := autoApp.toolsForMessage("fix the auth bug")
	ordinary := marshal(ordinarySchema)
	toolsOnly := marshal(toolsApp.toolsForMessage("Count TODOs across every repository file"))
	if !bytes.Equal(ordinary, toolsOnly) {
		t.Fatalf("ordinary auto schema differs from tools mode: auto=%d bytes tools=%d bytes",
			len(ordinary), len(toolsOnly))
	}

	eligibleSchema := autoApp.toolsForMessage("Count TODOs across every repository file")
	eligible := marshal(eligibleSchema)
	if delta := len(eligible) - len(ordinary); delta <= 0 || delta > 3400 {
		t.Fatalf("eligible auto schema delta=%d bytes, want one bounded repl_exec declaration", delta)
	}
	if !schemaHasDeclaration(eligibleSchema, "repl_exec") || schemaHasDeclaration(eligibleSchema, "harness") {
		t.Fatal("eligible auto schema must add repl_exec only")
	}
	eligibleBase := marshal(tools.FilterGeminiToolsExcluding(eligibleSchema, "repl_exec"))
	if !bytes.Equal(eligibleBase, ordinary) {
		t.Fatalf("eligible auto changed the base schema in addition to repl_exec: base=%d bytes ordinary=%d bytes",
			len(eligibleBase), len(ordinary))
	}

	hybridSchema := newApp("hybrid").toolsForMessage("fix the auth bug")
	if !schemaHasDeclaration(hybridSchema, "repl_exec") || !schemaHasDeclaration(hybridSchema, "harness") {
		t.Fatal("explicit hybrid schema must include repl_exec and harness")
	}
	hybridBase := marshal(tools.FilterGeminiToolsExcluding(hybridSchema, "repl_exec", "harness"))
	if !bytes.Equal(hybridBase, ordinary) {
		t.Fatalf("explicit hybrid changed the base schema in addition to repl_exec and harness: base=%d bytes ordinary=%d bytes",
			len(hybridBase), len(ordinary))
	}
	t.Logf("tool schema JSON: tools/ordinary-auto=%d bytes eligible-auto=%d (+%d) explicit-hybrid=%d (+%d)",
		len(ordinary), len(eligible), len(eligible)-len(ordinary),
		len(marshal(hybridSchema)), len(marshal(hybridSchema))-len(ordinary))
}

func BenchmarkToolsForMessageHybridPolicy(b *testing.B) {
	cfg := config.DefaultConfig()
	application := &App{config: cfg, registry: tools.DefaultRegistry(b.TempDir())}
	ordinary := "fix the auth bug"
	eligible := "Rank repository files by how many TODO comments they contain"
	eligibleDecision := hybrid.Decide("auto", eligible)

	b.Run("ordinary", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if schemaHasDeclaration(application.toolsForMessage(ordinary), "repl_exec") {
				b.Fatal("ordinary request exposed repl_exec")
			}
		}
	})
	b.Run("eligible", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if !schemaHasDeclaration(application.toolsForMessage(eligible), "repl_exec") {
				b.Fatal("eligible request hid repl_exec")
			}
		}
	})
	b.Run("eligible_preclassified", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if !schemaHasDeclaration(
				application.toolsForMessageDecision(eligible, eligibleDecision), "repl_exec",
			) {
				b.Fatal("eligible request hid repl_exec")
			}
		}
	})
	b.Run("base_registry_schema", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if len(application.registry.GeminiTools()) == 0 {
				b.Fatal("empty registry schema")
			}
		}
	})
	for _, test := range []struct {
		name   string
		remove []string
	}{
		{name: "schema_without_skill", remove: []string{"skill"}},
		{name: "schema_without_skill_task", remove: []string{"skill", "task"}},
		{name: "schema_static_only", remove: []string{"skill", "task", "bash"}},
	} {
		b.Run(test.name, func(b *testing.B) {
			registry := tools.DefaultRegistry(b.TempDir())
			for _, name := range test.remove {
				registry.Unregister(name)
			}
			b.ReportAllocs()
			for b.Loop() {
				if len(registry.GeminiTools()) == 0 {
					b.Fatal("empty registry schema")
				}
			}
		})
	}
}

func TestToolsForMessageAdaptiveHybridExposure(t *testing.T) {
	cfg := config.DefaultConfig()
	application := &App{config: cfg, registry: tools.DefaultRegistry(t.TempDir())}

	if schemaHasDeclaration(application.toolsForMessage("fix the auth bug"), "repl_exec") {
		t.Fatal("auto mode exposed repl_exec for an ordinary implementation request")
	}
	if schemaHasDeclaration(application.toolsForMessage("fix the auth bug"), "harness") {
		t.Fatal("auto mode exposed the direct harness")
	}
	for _, targeted := range []string{
		"Count TODO lines in this file only",
		"Compare `pair/left.json` with `pair/right.json` in this repository",
	} {
		if schemaHasDeclaration(application.toolsForMessage(targeted), "repl_exec") {
			t.Fatalf("auto mode exposed repl_exec for targeted analysis %q", targeted)
		}
		policy := application.hybridPolicyForSchema(targeted, application.toolsForMessage(targeted))
		if policy.REPLEligible || policy.REPLExposed {
			t.Fatalf("targeted policy snapshot for %q = %+v", targeted, policy)
		}
	}

	aggregation := "Rank repository files by how many TODO comments they contain"
	aggregationTools := application.toolsForMessage(aggregation)
	if !schemaHasDeclaration(aggregationTools, "repl_exec") {
		t.Fatal("auto mode omitted repl_exec for a repository aggregation request")
	}
	if schemaHasDeclaration(aggregationTools, "harness") {
		t.Fatal("auto mode exposed the direct harness alongside repl_exec")
	}
	policy := application.hybridPolicyForSchema(aggregation, aggregationTools)
	if !policy.REPLEligible || !policy.REPLExposed || policy.HarnessExposed {
		t.Fatalf("auto policy snapshot = %+v", policy)
	}

	// Eligibility is intent classification, not proof of availability. This is
	// the auto-mode secure-runtime fallback shape the journal must report
	// truthfully instead of claiming the model received repl_exec.
	application.registry.Unregister("repl_exec")
	unavailableTools := application.toolsForMessage(aggregation)
	policy = application.hybridPolicyForSchema(aggregation, unavailableTools)
	if !policy.REPLEligible || policy.REPLExposed {
		t.Fatalf("unavailable REPL was conflated with policy eligibility: %+v", policy)
	}
	details := policy.journalDetails()
	if details["repl_eligible"] != true || details["repl_enabled"] != false || details["exposure_gap"] != true ||
		details["strategy"] != hybrid.StrategyAggregation {
		t.Fatalf("truthful policy journal details = %#v", details)
	}
	application.registry.MustRegister(tools.NewReplExecTool(nil))

	// engine.mode owns registry/runtime topology and is intentionally pinned at
	// process start. Mutating the backing test config cannot partially switch a
	// live App.
	cfg.Engine.Mode = "hybrid"
	if schemaHasDeclaration(application.toolsForMessage("fix the auth bug"), "repl_exec") ||
		schemaHasDeclaration(application.toolsForMessage("fix the auth bug"), "harness") {
		t.Fatal("auto process partially switched after its config value changed")
	}

	hybridCfg := config.DefaultConfig()
	hybridCfg.Engine.Mode = "hybrid"
	hybridApp := &App{config: hybridCfg, registry: tools.DefaultRegistry(t.TempDir())}
	if !schemaHasDeclaration(hybridApp.toolsForMessage("fix the auth bug"), "repl_exec") ||
		!schemaHasDeclaration(hybridApp.toolsForMessage("fix the auth bug"), "harness") {
		t.Fatal("explicit hybrid mode did not expose both hybrid declarations")
	}
	hybridCfg.Engine.Mode = "tools"
	if !schemaHasDeclaration(hybridApp.toolsForMessage("fix the auth bug"), "repl_exec") ||
		!schemaHasDeclaration(hybridApp.toolsForMessage("fix the auth bug"), "harness") {
		t.Fatal("hybrid process partially switched after its config value changed")
	}

	toolsCfg := config.DefaultConfig()
	toolsCfg.Engine.Mode = "tools"
	toolsApp := &App{config: toolsCfg, registry: tools.DefaultRegistry(t.TempDir())}
	if schemaHasDeclaration(toolsApp.toolsForMessage(aggregation), "repl_exec") ||
		schemaHasDeclaration(toolsApp.toolsForMessage(aggregation), "harness") {
		t.Fatal("tools mode exposed a hybrid declaration")
	}
	toolsCfg.Engine.Mode = "hybrid"
	if schemaHasDeclaration(toolsApp.toolsForMessage(aggregation), "repl_exec") ||
		schemaHasDeclaration(toolsApp.toolsForMessage(aggregation), "harness") {
		t.Fatal("tools process partially switched after its config value changed")
	}
}

func schemaHasDeclaration(schema []*genai.Tool, name string) bool {
	for _, envelope := range schema {
		if envelope == nil {
			continue
		}
		for _, declaration := range envelope.FunctionDeclarations {
			if declaration != nil && declaration.Name == name {
				return true
			}
		}
	}
	return false
}

func TestRestoreDirectPromptScaffoldingKeepsPersistedUserTextClean(t *testing.T) {
	original := "Count TODOs in the repository"
	history := []*genai.Content{
		genai.NewContentFromText("earlier", genai.RoleUser),
		genai.NewContentFromText("internal hybrid hint\n\n"+original, genai.RoleUser),
		genai.NewContentFromText("answer", genai.RoleModel),
	}
	restored := restoreDirectPromptScaffolding(history, 1, original)
	if got := restored[1].Parts[0].Text; got != original {
		t.Fatalf("persisted user text = %q", got)
	}
	if got := restored[2].Parts[0].Text; got != "answer" {
		t.Fatalf("model response changed during restoration: %q", got)
	}
}
