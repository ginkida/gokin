package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/commands"
)

// TestPrepareSteerMessage_CommandsNeverSteer pins the review fix for the
// command-swallowing regression: a slash command typed while a request is in
// flight must EXECUTE (via the pending queue → dequeue → handleSubmit with
// processing=false → command routing), never be steered into the model's turn
// as literal "[user follow-up] /tasks" text that silently does nothing.
func TestPrepareSteerMessage_CommandsNeverSteer(t *testing.T) {
	a := &App{commandHandler: commands.NewHandler()}

	for _, cmd := range []string{"/tasks", "/compact", "/undo", "/loop status"} {
		if _, ok := a.prepareSteerMessage(cmd); ok {
			t.Errorf("%q is a slash command and must NOT be steerable", cmd)
		}
	}

	msg, ok := a.prepareSteerMessage("please also add tests")
	if !ok {
		t.Fatal("plain text must be steerable")
	}
	if msg != "please also add tests" {
		t.Errorf("plain text without @refs must pass through unchanged, got %q", msg)
	}
}

// TestPrepareSteerMessage_ExpandsAtRefs pins the second half of the fix: the
// steer path returns from handleSubmit before the normal-path
// expandAtReferences call, so prepareSteerMessage must expand @file references
// itself — otherwise the model receives a bare unresolved @token mid-turn.
func TestPrepareSteerMessage_ExpandsAtRefs(t *testing.T) {
	a, work := newAtRefTestApp(t)
	a.commandHandler = commands.NewHandler()
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	msg, ok := a.prepareSteerMessage("look at @main.go please")
	if !ok {
		t.Fatal("a plain message with an @ref must be steerable")
	}
	if !strings.Contains(msg, "package main") {
		t.Errorf("steered message must carry the expanded @file content, got:\n%s", msg)
	}
}

// TestHandleResubmit_BusyQueuesInsteadOfSteering pins the programmatic-retry
// dispatch: when the retry timer fires while the user is ALREADY busy with a
// NEW task, the old message must go to the pending FIFO (the pre-steering
// behavior) — never be injected into the unrelated in-flight turn. All four
// programmatic re-entry sites (rate-limit retry, auto-resume, pending-queue
// dispatch, file-command prompt) route through handleResubmit.
func TestHandleResubmit_BusyQueuesInsteadOfSteering(t *testing.T) {
	a := &App{processing: true}

	a.handleResubmit("retry the failed task")

	queued := a.pendingSnapshot()
	if len(queued) != 1 || queued[0] != "retry the failed task" {
		t.Fatalf("busy resubmit must land in the pending queue, got %v", queued)
	}
}
