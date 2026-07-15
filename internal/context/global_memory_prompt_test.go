package context

import "testing"

type memoryScopeProbe struct {
	contextScopes  []bool
	relevantScopes []bool
}

func (p *memoryScopeProbe) GetForContext(projectOnly bool) string {
	p.contextScopes = append(p.contextScopes, projectOnly)
	return ""
}

func (p *memoryScopeProbe) GetRelevantForContext(_ string, projectOnly bool, _ int) string {
	p.relevantScopes = append(p.relevantScopes, projectOnly)
	return ""
}

func TestPromptBuilderGlobalMemoryIsExplicitOptIn(t *testing.T) {
	probe := &memoryScopeProbe{}
	builder := NewPromptBuilder(t.TempDir(), &ProjectInfo{})
	builder.SetMemoryStore(probe)

	builder.Build()
	if len(probe.contextScopes) != 1 || !probe.contextScopes[0] {
		t.Fatalf("default Build scopes = %v, want projectOnly=true", probe.contextScopes)
	}

	builder.SetGlobalMemoryEnabled(true)
	builder.Build()
	if len(probe.contextScopes) != 2 || probe.contextScopes[1] {
		t.Fatalf("opted-in Build scopes = %v, want latest projectOnly=false", probe.contextScopes)
	}
}

func TestPromptBuilderGlobalOptInCoversAgentAndPlanRetrieval(t *testing.T) {
	probe := &memoryScopeProbe{}
	builder := NewPromptBuilder(t.TempDir(), &ProjectInfo{})
	builder.SetMemoryStore(probe)
	builder.SetGlobalMemoryEnabled(true)

	builder.BuildSubAgentPromptForTask("authentication migration")
	if len(probe.contextScopes) != 1 || probe.contextScopes[0] {
		t.Fatalf("sub-agent persistent scopes = %v, want global-enabled", probe.contextScopes)
	}
	if len(probe.relevantScopes) != 1 || probe.relevantScopes[0] {
		t.Fatalf("sub-agent relevant scopes = %v, want global-enabled", probe.relevantScopes)
	}

	probe.contextScopes = nil
	probe.relevantScopes = nil
	builder.BuildPlanExecutionPrompt("Auth", "Migrate issuer", []PlanStepInfo{{Title: "Update", Description: "change auth"}})
	if len(probe.contextScopes) != 1 || probe.contextScopes[0] {
		t.Fatalf("plan persistent scopes = %v, want global-enabled", probe.contextScopes)
	}
	if len(probe.relevantScopes) != 1 || probe.relevantScopes[0] {
		t.Fatalf("plan relevant scopes = %v, want global-enabled", probe.relevantScopes)
	}
}
