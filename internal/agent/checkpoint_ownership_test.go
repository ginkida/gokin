package agent

import (
	"context"
	"testing"
	"time"

	"gokin/internal/tools"
)

// Checkpoints live in the GLOBAL config dir while recovery restores the agent
// bound to THIS runner's workDir. Without an owner, opening gokin in project B
// consumed project A's failed-agent checkpoint and replayed A's history — file
// edits, bash, git — against B, and A could never recover its own work because
// the claim unlinks first. Ownership is exact-directory, fail-closed, and it
// must NOT consume a foreign checkpoint.
func TestErrorCheckpointRecoveryIsScopedToItsWorkspace(t *testing.T) {
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewAgentStore: %v", err)
	}

	projectA := t.TempDir()
	projectB := t.TempDir()

	foreign := &AgentCheckpoint{
		AgentState:    &AgentState{ID: "project-a-agent", Type: AgentTypeGeneral, MaxTurns: 3},
		WorkDir:       projectA,
		Timestamp:     time.Now(),
		CheckpointID:  "project-a-agent-1",
		TriggerReason: "error",
	}
	legacy := &AgentCheckpoint{
		AgentState:    &AgentState{ID: "legacy-agent", Type: AgentTypeGeneral, MaxTurns: 3},
		Timestamp:     time.Now(),
		CheckpointID:  "legacy-agent-1",
		TriggerReason: "error",
	}
	for _, cp := range []*AgentCheckpoint{foreign, legacy} {
		if err := store.SaveCheckpoint(cp); err != nil {
			t.Fatalf("SaveCheckpoint(%s): %v", cp.CheckpointID, err)
		}
	}

	// A runner opened in project B must recover nothing here.
	runnerB := NewRunner(context.Background(), nil, tools.NewRegistry(), projectB)
	runnerB.SetStore(store)
	if resumed := runnerB.ResumeErrorCheckpoints(context.Background()); resumed != 0 {
		t.Fatalf("project B resumed %d foreign checkpoint(s)", resumed)
	}

	// And it must not have CONSUMED them — the owner still needs its work, and
	// an unknown-owner checkpoint is left for the store's age-based cleanup.
	for _, id := range []string{"project-a-agent-1", "legacy-agent-1"} {
		if _, err := store.LoadCheckpoint(id); err != nil {
			t.Fatalf("checkpoint %s was consumed by a foreign workspace: %v", id, err)
		}
	}

	if runnerB.ownsCheckpoint(foreign) {
		t.Fatal("a checkpoint from another workspace must never be owned")
	}
	if runnerB.ownsCheckpoint(legacy) {
		t.Fatal("an unknown-owner checkpoint must fail closed")
	}
	runnerA := NewRunner(context.Background(), nil, tools.NewRegistry(), projectA)
	if !runnerA.ownsCheckpoint(foreign) {
		t.Fatal("the owning workspace must still claim its own checkpoint")
	}
}
