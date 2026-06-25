package tools

import "testing"

func TestClassifyTurnDiscuss(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		mode string
		want bool // true = discuss/analysis
	}{
		// Clear action imperatives → act (never gated), even mid-exploration.
		{"implement", "implement the cache layer", "", false},
		{"fix the (beats exploring mode)", "fix the off-by-one in paging", "exploring", false},
		{"go ahead and add", "go ahead and add the test", "", false},
		{"ru сделай", "сделай рефакторинг очереди", "", false},
		{"ru реализуй now", "реализуй это сейчас", "exploring", false},

		// Discuss frames WIN even when action verbs also appear.
		{"analyze how to fix", "let's analyze how we'd fix the parser bug", "", true},
		{"what do you think about", "what do you think about adding retries?", "", true},
		{"how would we refactor", "how would we refactor this module?", "", true},
		{"should we implement", "should we implement option B?", "", true},
		{"ru проанализируй", "проанализируй модуль очереди", "", true},
		{"ru как лучше реализовать", "как лучше реализовать кэш?", "", true},

		// Ambiguous (no frame, no imperative) → conversation mode decides.
		{"ambiguous + exploring → discuss", "the parser is slow on big inputs", "exploring", true},
		{"ambiguous + implementing → act", "the parser is slow on big inputs", "implementing", false},
		{"ambiguous + fresh → act", "the parser is slow on big inputs", "", false},

		{"empty → act", "", "exploring", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyTurnDiscuss(tc.msg, tc.mode); got != tc.want {
				t.Errorf("ClassifyTurnDiscuss(%q, %q) = %v, want %v", tc.msg, tc.mode, got, tc.want)
			}
		})
	}
}

func TestIsImplementationTool(t *testing.T) {
	// Code/repo-mutating tools are gated in discuss mode.
	for _, n := range []string{
		"write", "edit", "delete", "move", "copy", "mkdir",
		"refactor", "batch", "atomicwrite", "git_commit", "git_add", "ssh",
	} {
		if !IsImplementationTool(n) {
			t.Errorf("IsImplementationTool(%q) = false, want true", n)
		}
	}
	// Read/verify/explore tools MUST flow free during analysis — bash and
	// run_tests deliberately excluded so `go test`/`go build` aren't gated.
	for _, n := range []string{
		"read", "grep", "glob", "tree", "list_dir", "bash", "run_tests",
		"git_status", "git_diff", "web_search", "todo", "",
	} {
		if IsImplementationTool(n) {
			t.Errorf("IsImplementationTool(%q) = true, want false (must flow free in discuss mode)", n)
		}
	}
}
