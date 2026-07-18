package agent

import (
	"strings"
	"testing"

	"gokin/internal/tools"
)

// Sub-agents build their own system prompt and never see the foreground base
// rules — the verify-before-proposing rule must be present for substantive
// agents (a /loop 'improve the app' iteration is the canonical producer of
// unverified suggestion lists) and stay out of lightweight explorers.
func TestSubAgentPromptCarriesProposalHonesty(t *testing.T) {
	reg := tools.NewRegistry()
	general := &Agent{Type: AgentTypeGeneral, registry: reg}
	if !strings.Contains(general.buildSystemPrompt(), "Proposal honesty") {
		t.Error("general agent prompt must carry the proposal-honesty rule")
	}
	explore := &Agent{Type: AgentTypeExplore, registry: reg}
	if strings.Contains(explore.buildSystemPrompt(), "Proposal honesty") {
		t.Error("lightweight explore agent should keep its minimal prompt")
	}
}
