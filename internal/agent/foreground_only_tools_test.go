package agent

import (
	"strings"
	"testing"

	"gokin/internal/tools"
)

// AgentTypeGeneral allows every tool, so a foreground-only tool that is merely
// "not usually used by sub-agents" would in fact be handed to every general
// sub-agent. repl_exec drives one Python kernel bound to the foreground
// workspace behind a single mutex — sharing it leaks globals between agents and
// lets one agent's reset wipe another's state mid-analysis.
func TestSubAgentRegistryExcludesForegroundOnlyTools(t *testing.T) {
	base := tools.NewRegistry()
	if err := base.Register(tools.NewReplExecTool(nil)); err != nil {
		t.Fatal(err)
	}
	if err := base.Register(tools.NewTodoTool()); err != nil {
		t.Fatal(err)
	}

	// The unrestricted path: nil allowlist means "everything".
	general := createFilteredRegistry(AgentTypeGeneral, base)
	if _, ok := general.Get("repl_exec"); ok {
		t.Fatal("a general sub-agent must not receive repl_exec")
	}
	if _, ok := general.Get("todo"); !ok {
		t.Fatal("the exclusion must not remove ordinary tools")
	}

	// The allowlist path: naming it explicitly must not get it back either.
	explicit := createFilteredRegistryFromList([]string{"repl_exec", "todo"}, base)
	if _, ok := explicit.Get("repl_exec"); ok {
		t.Fatal("an explicit allowlist must not restore a foreground-only tool")
	}
	if _, ok := explicit.Get("todo"); !ok {
		t.Fatal("the allowlist path dropped a permitted tool")
	}
}

// The registry filter alone does not hold: RequestTool pulls from the BASE
// registry, and an unrestricted type (general returns a nil allowlist) skips
// the authorization check entirely — so without an explicit guard a general
// sub-agent could request the excluded tool straight back.
func TestRequestToolRefusesForegroundOnlyTools(t *testing.T) {
	base := tools.NewRegistry()
	if err := base.Register(tools.NewReplExecTool(nil)); err != nil {
		t.Fatal(err)
	}
	if err := base.Register(tools.NewTodoTool()); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{
		registry:     createFilteredRegistry(AgentTypeGeneral, base),
		baseRegistry: base,
		workDir:      t.TempDir(),
	}

	err := agent.RequestTool("repl_exec")
	if err == nil {
		t.Fatal("a general agent recovered a foreground-only tool through RequestTool")
	}
	if !strings.Contains(err.Error(), "foreground") {
		t.Fatalf("refusal should say why: %v", err)
	}
	if _, ok := agent.registry.Get("repl_exec"); ok {
		t.Fatal("the refused tool was still added to the agent registry")
	}

	// An ordinary tool must still be requestable, or the guard is too broad.
	if err := agent.RequestTool("todo"); err != nil {
		t.Fatalf("ordinary tool request failed: %v", err)
	}
	if _, ok := agent.registry.Get("todo"); !ok {
		t.Fatal("ordinary tool was not added")
	}
}
