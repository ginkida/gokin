package config

import "testing"

func TestDefaultPermissionRulesAllowReadOnlyAgentInfrastructure(t *testing.T) {
	rules := DefaultConfig().Permission.Rules
	for _, tool := range []string{
		"read", "glob", "grep", "git_status", "git_log", "git_diff",
		"git_blame", "review_changes", "history_search", "tools_list", "skill",
		"task_output", "task_stop",
	} {
		if got := rules[tool]; got != "allow" {
			t.Errorf("default permission for %s = %q, want allow", tool, got)
		}
	}
}
