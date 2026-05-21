package commands

import (
	"context"
	"strings"
	"testing"
)

// TestQuickstartCommand_NoBoxedBanner pins the v0.84.8 polish:
// /quickstart joins /doctor (v0.84.7) and /stats /tree-stats (v0.82.5)
// in dropping the heavy ╔═╗ ╚═╝ double-border banner. Lowercase muted
// section labels match the rest of the app's chrome convention.
//
// If a future commit re-introduces banner runes in the quickstart
// output, this test trips.
func TestQuickstartCommand_NoBoxedBanner(t *testing.T) {
	out, err := (&QuickstartCommand{}).Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Execute returned err: %v", err)
	}

	stale := []string{"╔", "╗", "╚", "╝", "║"}
	for _, s := range stale {
		if strings.Contains(out, s) {
			t.Errorf("/quickstart output contains legacy banner element %q", s)
		}
	}

	// Header content must still identify the page.
	if !strings.Contains(out, "Quick Start with Gokin") {
		t.Errorf("/quickstart header missing — output:\n%s", out)
	}
}

// TestQuickstartCommand_KeyCommandsAreCurrent pins that the "key
// commands" cheatsheet references real commands. Pre-loop audit found
// the docs in good shape; this test guards against future stale refs.
func TestQuickstartCommand_KeyCommandsAreCurrent(t *testing.T) {
	out, err := (&QuickstartCommand{}).Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Execute returned err: %v", err)
	}
	mustContain := []string{
		"/help",
		"/quickstart",
		"/doctor",
		"/plan",
		"/resume-plan",
		"/model",
		"/update",
		"/clear",
	}
	for _, cmd := range mustContain {
		if !strings.Contains(out, cmd) {
			t.Errorf("/quickstart missing command %q", cmd)
		}
	}
}
