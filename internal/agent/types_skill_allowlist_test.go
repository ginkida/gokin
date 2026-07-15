package agent

import "testing"

// Every built-in specialized agent must be able to load the workflow selected
// for its task. General agents already receive the full registry; these four
// roles depend on explicit allowlists and silently lost `skill` without it.
func TestBuiltInSpecializedAgentAllowlistsIncludeSkill(t *testing.T) {
	for _, agentType := range []AgentType{
		AgentTypeExplore,
		AgentTypeBash,
		AgentTypePlan,
		AgentTypeGuide,
	} {
		found := false
		for _, name := range agentType.AllowedTools() {
			if name == "skill" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s AllowedTools does not include skill", agentType)
		}
	}
}
