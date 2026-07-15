package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAgentStoreRejectsPathTraversalIdentifiers(t *testing.T) {
	root := t.TempDir()
	store, err := NewAgentStore(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"", ".", "..", "../escape", `..\escape`, "agent:name", "agent/name"} {
		if err := store.SaveState(&AgentState{ID: id}); err == nil {
			t.Fatalf("SaveState accepted unsafe ID %q", id)
		}
		if _, err := store.Load(id); err == nil {
			t.Fatalf("Load accepted unsafe ID %q", id)
		}
		if store.Exists(id) {
			t.Fatalf("Exists accepted unsafe ID %q", id)
		}
	}

	if _, err := os.Stat(filepath.Join(root, "escape.json")); !os.IsNotExist(err) {
		t.Fatalf("unsafe state ID wrote outside agent directory: %v", err)
	}
}

func TestAgentStoreRejectsTamperedPersistedIdentity(t *testing.T) {
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	stateData, err := json.Marshal(&AgentState{ID: "../escape", Type: AgentTypeGeneral})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.dir, "safe-agent.json"), stateData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("safe-agent"); err == nil || !strings.Contains(err.Error(), "persisted agent ID") {
		t.Fatalf("tampered state load error = %v", err)
	}

	checkpointsDir := filepath.Join(store.dir, "checkpoints")
	if err := os.MkdirAll(checkpointsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cp := &AgentCheckpoint{
		CheckpointID:  "../escape",
		AgentState:    &AgentState{ID: "safe-agent", Type: AgentTypeGeneral},
		Timestamp:     time.Now(),
		TriggerReason: "error",
	}
	cpData, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkpointsDir, "safe-checkpoint.json"), cpData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadCheckpoint("safe-checkpoint"); err == nil || !strings.Contains(err.Error(), "persisted checkpoint ID") {
		t.Fatalf("tampered checkpoint load error = %v", err)
	}
}

func TestAgentStoreRejectsIncompleteCheckpointAndNegativeRetention(t *testing.T) {
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCheckpoint(nil); err == nil {
		t.Fatal("SaveCheckpoint(nil) succeeded")
	}
	if err := store.SaveCheckpoint(&AgentCheckpoint{CheckpointID: "valid-id"}); err == nil {
		t.Fatal("checkpoint without agent state succeeded")
	}
	if _, err := store.CleanupCheckpoints("safe-agent", -1); err == nil {
		t.Fatal("negative checkpoint retention succeeded")
	}
}

func TestAgentStorePersistsPrivateRecoveryState(t *testing.T) {
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := &AgentState{ID: "private-agent", Type: AgentTypeGeneral}
	if err := store.SaveState(state); err != nil {
		t.Fatal(err)
	}
	cp := &AgentCheckpoint{
		AgentState:    state,
		CheckpointID:  "private-agent-1",
		Timestamp:     time.Now(),
		TriggerReason: "error",
	}
	if err := store.SaveCheckpoint(cp); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	for _, path := range []string{
		store.dir,
		filepath.Join(store.dir, "checkpoints"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("directory %s permissions = %04o, want 0700", path, got)
		}
	}
	for _, path := range []string{
		filepath.Join(store.dir, "private-agent.json"),
		filepath.Join(store.dir, "checkpoints", "private-agent-1.json"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("recovery file %s permissions = %04o, want 0600", path, got)
		}
	}
}
