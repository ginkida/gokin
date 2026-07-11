package agent

import "testing"

func TestRunnerAgentUsageCallbackReportsEveryInvocation(t *testing.T) {
	r := &Runner{}
	var got []int
	r.SetOnAgentUsage(func(id string, result *AgentResult) {
		if id != "same-agent" {
			t.Fatalf("callback id = %q", id)
		}
		got = append(got, result.InputTokens)
	})

	// Resume reuses an ID, but represents another billable invocation. The
	// accounting callback must not deduplicate by agent ID.
	r.reportAgentUsage("same-agent", &AgentResult{InputTokens: 100})
	r.reportAgentUsage("same-agent", &AgentResult{InputTokens: 200})
	r.reportAgentUsage("same-agent", nil)

	if len(got) != 2 || got[0] != 100 || got[1] != 200 {
		t.Fatalf("reported usage = %v, want [100 200]", got)
	}
}
