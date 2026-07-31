package permission

import "testing"

// Gokin registers MCP tools under its own `<server>_<tool>` names, so a
// Claude-style rule used to match nothing and was accepted as a silent no-op —
// the worst outcome for a rule whose whole purpose is to remove authority.
func TestClaudeMCPDenyRulesReachGokinNamedMCPTools(t *testing.T) {
	SetToolRiskOverride("github_create_issue", RiskHigh)
	SetToolRiskOverride("github_list_prs", RiskHigh)
	SetToolRiskOverride("linear_create_ticket", RiskHigh)
	t.Cleanup(func() {
		ClearToolRiskOverride("github_create_issue")
		ClearToolRiskOverride("github_list_prs")
		ClearToolRiskOverride("linear_create_ticket")
	})

	cases := []struct {
		rule string
		tool string
		want bool
	}{
		{"mcp__*", "github_create_issue", true},
		{"mcp__*", "linear_create_ticket", true},
		{"mcp__github", "github_create_issue", true},
		{"mcp__github__*", "github_list_prs", true},
		{"mcp__github__create_issue", "github_create_issue", true},
		{"mcp__github__create_issue", "github_list_prs", false},
		{"mcp__github__*", "linear_create_ticket", false},
		// A built-in must stay out of range even when it shares the prefix
		// shape — membership is confirmed against MCP registration.
		{"mcp__*", "read", false},
		{"mcp__read", "read", false},
	}
	for _, tc := range cases {
		if got := ToolDenyRuleMatchesName(tc.rule, tc.tool); got != tc.want {
			t.Errorf("ToolDenyRuleMatchesName(%q, %q) = %v, want %v",
				tc.rule, tc.tool, got, tc.want)
		}
	}
}

// An unregistered MCP-shaped name must not become reachable just because a rule
// mentions it: nothing is denied that the process cannot prove is an MCP tool.
func TestClaudeMCPRuleIgnoresUnregisteredTool(t *testing.T) {
	if ToolDenyRuleMatchesName("mcp__*", "github_create_issue") {
		t.Fatal("rule matched a tool no MCP server registered")
	}
}
