package agent

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

// TestQueueAndDrainSteers pins the v0.86.0 MetaAgent-injection wiring: a queued
// steering message is drained into history as a user turn (so the model sees it),
// with dedup and empty-message guards.
func TestQueueAndDrainSteers(t *testing.T) {
	a := &Agent{}

	a.QueueSteer("break the task into smaller steps")
	a.QueueSteer("break the task into smaller steps") // duplicate → ignored
	a.QueueSteer("   ")                               // empty → ignored
	a.QueueSteer("reconsider the approach")

	if got := len(a.pendingSteers); got != 2 {
		t.Fatalf("pendingSteers = %d, want 2 (dedup + empty-guard)", got)
	}

	a.drainSteers()

	if len(a.pendingSteers) != 0 {
		t.Errorf("pendingSteers not cleared after drain: %d", len(a.pendingSteers))
	}
	if len(a.history) != 2 {
		t.Fatalf("history len after drain = %d, want 2 injected turns", len(a.history))
	}
	for _, c := range a.history {
		if c.Role != genai.RoleUser {
			t.Errorf("injected steer role = %v, want User (model must see it as guidance)", c.Role)
		}
		if len(c.Parts) == 0 || !strings.Contains(c.Parts[0].Text, "[guidance]") {
			t.Errorf("injected steer missing [guidance] marker: %+v", c.Parts)
		}
	}

	// Draining again is a no-op (nothing queued).
	a.drainSteers()
	if len(a.history) != 2 {
		t.Errorf("second drain changed history: %d", len(a.history))
	}
}
