package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentStateListIsDeterministicAndIdentityChecked(t *testing.T) {
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"z-agent", "a-agent", "middle-agent"} {
		if err := store.SaveState(&AgentState{ID: id, Type: AgentTypeGeneral}); err != nil {
			t.Fatalf("SaveState(%s): %v", id, err)
		}
	}

	ids, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"a-agent", "middle-agent", "z-agent"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("List = %v, want deterministic order %v", ids, want)
	}

	tampered, err := json.Marshal(&AgentState{ID: "different-agent", Type: AgentTypeGeneral})
	if err != nil {
		t.Fatal(err)
	}
	tamperedPath := filepath.Join(store.dir, "claimed-agent.json")
	if err := os.WriteFile(tamperedPath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("List with mismatched persisted identity error = %v", err)
	}
	if store.Exists("claimed-agent") {
		t.Fatal("Exists accepted state whose persisted identity mismatches its filename")
	}
	if err := os.Remove(tamperedPath); err != nil {
		t.Fatal(err)
	}

	invalidFilename := filepath.Join(store.dir, "invalid state.json")
	if err := os.WriteFile(invalidFilename, []byte(`{"id":"invalid-state"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil || !strings.Contains(err.Error(), "agent state filename") {
		t.Fatalf("List with invalid state filename error = %v", err)
	}
	if err := os.Remove(invalidFilename); err != nil {
		t.Fatal(err)
	}

	malformedPath := filepath.Join(store.dir, "malformed-state.json")
	if err := os.WriteFile(malformedPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil || !strings.Contains(err.Error(), "unmarshal") {
		t.Fatalf("List with malformed state JSON error = %v", err)
	}
	if store.Exists("malformed-state") {
		t.Fatal("Exists accepted malformed state JSON")
	}
}

func TestAgentStateScansRejectSymlinkAndNonRegularEntries(t *testing.T) {
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	good := &AgentState{ID: "good-state", Type: AgentTypeGeneral}
	if err := store.SaveState(good); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(filepath.Join(store.dir, good.ID+".json"), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	t.Run("symlink", func(t *testing.T) {
		linked := &AgentState{ID: "linked-state", Type: AgentTypeGeneral}
		data, err := json.Marshal(linked)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target.json")
		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(store.dir, linked.ID+".json")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(link) })

		if store.Exists(linked.ID) {
			t.Fatal("Exists followed a symlinked agent state")
		}
		if _, err := store.List(); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("List with symlink error = %v", err)
		}
		if _, err := store.Cleanup(time.Hour); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("Cleanup with symlink error = %v", err)
		}
		if !store.Exists(good.ID) {
			t.Fatal("fail-closed cleanup removed valid state")
		}
	})

	t.Run("directory", func(t *testing.T) {
		path := filepath.Join(store.dir, "directory-state.json")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(path) })

		if store.Exists("directory-state") {
			t.Fatal("Exists accepted a directory as agent state")
		}
		if _, err := store.Cleanup(time.Hour); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("Cleanup with directory error = %v", err)
		}
		if !store.Exists(good.ID) {
			t.Fatal("fail-closed cleanup removed valid state")
		}
	})
}

func TestPersistedRecoveryReadsRejectOversizedSparseFiles(t *testing.T) {
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	statePath := filepath.Join(store.dir, "oversized-state.json")
	createSparseStoreFile(t, statePath, maxPersistedRecoveryFileBytes+1)
	if _, err := store.Load("oversized-state"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Load oversized sparse state error = %v", err)
	}
	if store.Exists("oversized-state") {
		t.Fatal("Exists accepted oversized sparse state")
	}
	if _, err := store.List(); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("List oversized sparse state error = %v", err)
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}

	checkpointDir := filepath.Join(store.dir, "checkpoints")
	if err := os.MkdirAll(checkpointDir, 0o700); err != nil {
		t.Fatal(err)
	}
	checkpointPath := filepath.Join(checkpointDir, "oversized-checkpoint.json")
	createSparseStoreFile(t, checkpointPath, maxPersistedRecoveryFileBytes+1)
	if _, err := store.LoadCheckpoint("oversized-checkpoint"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("LoadCheckpoint oversized sparse file error = %v", err)
	}
	if _, err := store.ListCheckpoints(""); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("ListCheckpoints oversized sparse file error = %v", err)
	}
}

func TestPersistedRecoverySavesRejectPayloadsAboveReadLimit(t *testing.T) {
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	originalState := &AgentState{
		ID:         "bounded-state",
		Type:       AgentTypeGeneral,
		LastPrompt: "original state",
	}
	if err := store.SaveState(originalState); err != nil {
		t.Fatal(err)
	}
	originalCheckpoint := checkpointForStoreTest("bounded-state", "bounded-checkpoint", time.Now())
	originalCheckpoint.ScratchpadContent = "original checkpoint"
	if err := store.SaveCheckpoint(originalCheckpoint); err != nil {
		t.Fatal(err)
	}

	// Exercise the real public Save methods without allocating a 128 MiB test
	// string. Production stores keep the hard default; the limit can only be
	// lowered internally and is always clamped to the read ceiling.
	store.maxPersistedWriteBytes = 512
	oversized := strings.Repeat("x", 2*1024)
	newState := &AgentState{
		ID:         originalState.ID,
		Type:       AgentTypeGeneral,
		LastPrompt: oversized,
	}
	if err := store.SaveState(newState); err == nil || !strings.Contains(err.Error(), "exceeds 512-byte") {
		t.Fatalf("SaveState oversized payload error = %v", err)
	}
	newCheckpoint := checkpointForStoreTest("bounded-state", originalCheckpoint.CheckpointID, time.Now().Add(time.Minute))
	newCheckpoint.ScratchpadContent = oversized
	if err := store.SaveCheckpoint(newCheckpoint); err == nil || !strings.Contains(err.Error(), "exceeds 512-byte") {
		t.Fatalf("SaveCheckpoint oversized payload error = %v", err)
	}

	loadedState, err := store.Load(originalState.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedState.LastPrompt != originalState.LastPrompt {
		t.Fatalf("oversized SaveState overwrote durable state: %q", loadedState.LastPrompt)
	}
	loadedCheckpoint, err := store.LoadCheckpoint(originalCheckpoint.CheckpointID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedCheckpoint.ScratchpadContent != originalCheckpoint.ScratchpadContent {
		t.Fatalf("oversized SaveCheckpoint overwrote durable checkpoint: %q", loadedCheckpoint.ScratchpadContent)
	}
}

func TestAgentStoreCleanupRejectsNegativeAgeWithoutMutation(t *testing.T) {
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := &AgentState{ID: "retained-state", Type: AgentTypeGeneral}
	if err := store.SaveState(state); err != nil {
		t.Fatal(err)
	}
	checkpoint := checkpointForStoreTest(state.ID, "retained-checkpoint", time.Now())
	if err := store.SaveCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}

	if removed, err := store.Cleanup(-time.Nanosecond); err == nil || removed != 0 {
		t.Fatalf("Cleanup(negative) = (%d, %v), want (0, error)", removed, err)
	}
	if !store.Exists(state.ID) {
		t.Fatal("negative state cleanup removed durable state")
	}
	if removed, err := store.CleanupOldCheckpointFiles(-time.Nanosecond); err == nil || removed != 0 {
		t.Fatalf("CleanupOldCheckpointFiles(negative) = (%d, %v), want (0, error)", removed, err)
	}
	if _, err := store.LoadCheckpoint(checkpoint.CheckpointID); err != nil {
		t.Fatalf("negative checkpoint cleanup removed durable checkpoint: %v", err)
	}
}

func createSparseStoreFile(t *testing.T, path string, size int64) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
