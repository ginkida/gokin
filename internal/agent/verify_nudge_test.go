package agent

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

// TestNeedsVerificationNudge pins the v0.86.3 sub-agent done-gate analogue:
// nudge to verify only when the agent changed code AND ran no verification
// command. Conservative — any command suppresses the nudge.
func TestNeedsVerificationNudge(t *testing.T) {
	cases := []struct {
		name  string
		tools []string
		want  bool
	}{
		{"edited, never verified", []string{"read", "edit"}, true},
		{"wrote + refactored, no check", []string{"write", "refactor"}, true},
		{"edited then ran bash", []string{"edit", "bash"}, false},
		{"edited then verify_code", []string{"edit", "verify_code"}, false},
		{"edited then run_tests", []string{"edit", "run_tests"}, false},
		{"read-only (no mutation)", []string{"read", "grep", "glob"}, false},
		{"nothing used", nil, false},
	}
	for _, c := range cases {
		a := &Agent{toolsUsed: c.tools}
		if got := a.needsVerificationNudge(); got != c.want {
			t.Errorf("%s: needsVerificationNudge(%v) = %v, want %v", c.name, c.tools, got, c.want)
		}
	}
}

func TestAppendVerificationNudge(t *testing.T) {
	a := &Agent{}
	a.appendVerificationNudge()
	if len(a.history) != 1 {
		t.Fatalf("history len = %d, want 1 nudge appended", len(a.history))
	}
	c := a.history[0]
	if c.Role != genai.RoleUser {
		t.Errorf("nudge role = %v, want User", c.Role)
	}
	if !strings.Contains(c.Parts[0].Text, "build/test") || !strings.Contains(c.Parts[0].Text, "[guidance]") {
		t.Errorf("nudge text unexpected: %q", c.Parts[0].Text)
	}
}
