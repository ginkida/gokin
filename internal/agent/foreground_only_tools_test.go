package agent

import (
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
