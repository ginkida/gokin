package commands

import (
	"context"
	"strings"
	"testing"
)

// TestRestartCommand_UserGuidance pins the user-facing contract: when
// the user types /restart, they get a clear confirmation that spells out
// (a) gokin is about to re-exec, (b) session state will be lost, and
// (c) /save is the escape hatch. The actual `syscall.Exec` fires in a
// goroutine and is non-deterministic in tests — we can't assert it ran
// without actually exec'ing the test binary — so the assertions focus
// on the message.
func TestRestartCommand_UserGuidance(t *testing.T) {
	cmd := &RestartCommand{}
	out, err := cmd.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "Restarting gokin") {
		t.Errorf("output should announce the restart, got: %q", out)
	}
	if !strings.Contains(out, "Session state will be lost") {
		t.Errorf("output should warn about session loss, got: %q", out)
	}
	if !strings.Contains(out, "/save") {
		t.Errorf("output should name /save as the escape hatch, got: %q", out)
	}
}

// TestRestartCommand_Metadata verifies the command appears under the
// Auth & Setup category with a priority that puts it next to /update —
// that's where users expect to find "apply the update I just installed".
func TestRestartCommand_Metadata(t *testing.T) {
	cmd := &RestartCommand{}
	meta := cmd.GetMetadata()
	if meta.Category != CategoryAuthSetup {
		t.Errorf("category = %v, want CategoryAuthSetup (next to /update)", meta.Category)
	}
	if meta.Priority > 10 {
		t.Errorf("priority = %d, should be low enough to appear near top (≤10)", meta.Priority)
	}
	if cmd.Name() != "restart" {
		t.Errorf("name = %q, want %q", cmd.Name(), "restart")
	}
}
