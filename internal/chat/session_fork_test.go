package chat

import (
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestPrepareSessionForkPreservesConversationAndScrubsExecutionLineage(t *testing.T) {
	source := NewSession()
	source.SetID("source-session")
	source.AddUserMessage("keep this context")
	source.SetToolCheckpoints([]SerializedToolCheckpoint{{CallID: "write-1", ToolName: "write"}})
	source.AddPendingRecovery(SerializedPendingRecovery{
		ID:        "retry-1",
		SessionID: "source-session",
		Message:   "retry",
		State:     PendingRecoveryScheduled,
	}, "")

	branch := NewSession()
	branch.SetID("branch")
	branch.AddContent(&genai.Content{Role: "user", Parts: []*genai.Part{{Text: "branch context"}}})
	branch.SetToolCheckpoints([]SerializedToolCheckpoint{{CallID: "bash-1", ToolName: "bash"}})
	source.Branches = map[string]*Session{"work": branch}

	sourceState := source.GetState()
	startedAt := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	fork, err := PrepareSessionFork(sourceState, "fork-session", startedAt)
	if err != nil {
		t.Fatalf("PrepareSessionFork: %v", err)
	}

	if fork.ID != "fork-session" || !fork.StartTime.Equal(startedAt) || !fork.LastActive.Equal(startedAt) {
		t.Fatalf("fork identity/timestamps = %q %s %s", fork.ID, fork.StartTime, fork.LastActive)
	}
	if len(fork.History) == 0 || len(fork.Branches) != 1 {
		t.Fatalf("conversation graph was not preserved: history=%d branches=%d", len(fork.History), len(fork.Branches))
	}
	if len(fork.ToolCheckpoints) != 0 || len(fork.PendingRecoveries) != 0 {
		t.Fatalf("top-level execution lineage survived: tools=%d recoveries=%d",
			len(fork.ToolCheckpoints), len(fork.PendingRecoveries))
	}
	if got := fork.Branches["work"]; got == nil || len(got.ToolCheckpoints) != 0 || len(got.PendingRecoveries) != 0 {
		t.Fatalf("nested execution lineage survived: %+v", got)
	}

	// The source Session is still the old owner and retains its durable retry.
	if source.GetID() != "source-session" || len(source.GetPendingRecoveries()) != 1 || len(source.GetToolCheckpoints()) != 1 {
		t.Fatalf("source session was mutated while preparing fork")
	}
}

func TestPrepareSessionForkRejectsInvalidIdentity(t *testing.T) {
	if _, err := PrepareSessionFork(nil, "fork", time.Now()); err == nil {
		t.Fatal("nil state was accepted")
	}
	state := &SessionState{ID: "source"}
	if _, err := PrepareSessionFork(state, "../escape", time.Now()); err == nil {
		t.Fatal("unsafe ID was accepted")
	}
	if _, err := PrepareSessionFork(state, "source", time.Now()); err == nil {
		t.Fatal("source identity was accepted as fork identity")
	}
}
