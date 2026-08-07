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

// A fresh install has no ledger file, so every registered tool lands in
// NeverUsed with nothing recorded against it. Rendering that would tell the
// user that read, write and bash — what they are using at that moment — have
// never been invoked. Absence of measurement must not read as a finding.
func TestFormatLifetimeToolUsage_FreshInstallSaysNothing(t *testing.T) {
	out := formatLifetimeToolUsage(LifetimeToolUsage{
		Counts:    map[string]int64{},
		NeverUsed: []string{"read", "write", "bash", "grep"},
		Total:     0,
	})
	if out != "" {
		t.Fatalf("fresh install must render nothing, got:\n%s", out)
	}

	// Once anything is recorded the comparison is meaningful again.
	measured := formatLifetimeToolUsage(LifetimeToolUsage{
		Counts:    map[string]int64{"read": 5},
		NeverUsed: []string{"repl_exec"},
		Total:     5,
	})
	if !strings.Contains(measured, "Never invoked:   1") {
		t.Fatalf("with real measurements the never-used list must appear:\n%s", measured)
	}
}
