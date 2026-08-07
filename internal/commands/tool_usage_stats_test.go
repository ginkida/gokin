package commands

import (
	"strings"
	"testing"
)

func TestFormatLifetimeToolUsage(t *testing.T) {
	out := formatLifetimeToolUsage(LifetimeToolUsage{
		Counts:    map[string]int64{"read": 120, "grep": 40},
		NeverUsed: []string{"repl_exec", "ssh"},
		Total:     160,
	})
	for _, want := range []string{"Tool Usage (lifetime", "160", "read", "Never invoked:   2", "repl_exec"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	// Nothing measured and nothing offered — stay silent rather than print an
	// empty section that reads like a finding.
	if formatLifetimeToolUsage(LifetimeToolUsage{}) != "" {
		t.Fatal("empty usage must render nothing")
	}
}
