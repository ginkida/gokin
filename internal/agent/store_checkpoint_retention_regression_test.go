package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckpointListingLatestAndRetentionUsePersistedMetadata(t *testing.T) {
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	checkpoints := []*AgentCheckpoint{
		checkpointForStoreTest("agent", "agent-z-old", base),
		checkpointForStoreTest("agent", "agent-a-new", base.Add(time.Hour)),
		checkpointForStoreTest("agent", "agent-b-new", base.Add(time.Hour)),
		// Its checkpoint ID matches the old filename-prefix filter for "agent",
		// but persisted ownership belongs exclusively to agent-child.
		checkpointForStoreTest("agent-child", "agent-child-newest", base.Add(2*time.Hour)),
	}
	for _, checkpoint := range checkpoints {
		if err := store.SaveCheckpoint(checkpoint); err != nil {
			t.Fatalf("SaveCheckpoint(%s): %v", checkpoint.CheckpointID, err)
		}
	}

	ids, err := store.ListCheckpoints("agent")
	if err != nil {
		t.Fatalf("ListCheckpoints(agent): %v", err)
	}
	wantIDs := []string{"agent-z-old", "agent-a-new", "agent-b-new"}
	if strings.Join(ids, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("agent checkpoints = %v, want timestamp order %v", ids, wantIDs)
	}

	childIDs, err := store.ListCheckpoints("agent-child")
	if err != nil {
		t.Fatalf("ListCheckpoints(agent-child): %v", err)
	}
	if len(childIDs) != 1 || childIDs[0] != "agent-child-newest" {
		t.Fatalf("agent-child checkpoints = %v", childIDs)
	}

	latest, err := store.GetLatestCheckpoint("agent")
	if err != nil {
		t.Fatalf("GetLatestCheckpoint(agent): %v", err)
	}
	// Equal timestamps use CheckpointID as a stable ascending-order tie break,
	// making the lexicographically larger ID the deterministic latest member.
	if latest.CheckpointID != "agent-b-new" {
		t.Fatalf("latest checkpoint = %q, want agent-b-new", latest.CheckpointID)
	}

	removed, err := store.CleanupCheckpoints("agent", 2)
	if err != nil {
		t.Fatalf("CleanupCheckpoints(agent): %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed checkpoints = %d, want 1", removed)
	}
	ids, err = store.ListCheckpoints("agent")
	if err != nil {
		t.Fatal(err)
	}
	wantIDs = []string{"agent-a-new", "agent-b-new"}
	if strings.Join(ids, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("retained agent checkpoints = %v, want newest-by-timestamp %v", ids, wantIDs)
	}
	childIDs, err = store.ListCheckpoints("agent-child")
	if err != nil {
		t.Fatal(err)
	}
	if len(childIDs) != 1 || childIDs[0] != "agent-child-newest" {
		t.Fatalf("agent cleanup touched child checkpoints: %v", childIDs)
	}
}

func TestCleanupOldCheckpointFilesUsesPersistedTimestamp(t *testing.T) {
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	oldByMetadata := checkpointForStoreTest("agent", "old-by-metadata", now.Add(-2*time.Hour))
	newByMetadata := checkpointForStoreTest("agent", "new-by-metadata", now)
	for _, checkpoint := range []*AgentCheckpoint{oldByMetadata, newByMetadata} {
		if err := store.SaveCheckpoint(checkpoint); err != nil {
			t.Fatal(err)
		}
	}

	checkpointDir := filepath.Join(store.dir, "checkpoints")
	// Deliberately make filesystem mtimes disagree with persisted checkpoint
	// time. Retention must not keep/delete the wrong recovery generation.
	if err := os.Chtimes(
		filepath.Join(checkpointDir, oldByMetadata.CheckpointID+".json"),
		now,
		now,
	); err != nil {
		t.Fatal(err)
	}
	oldFilesystemTime := now.Add(-24 * time.Hour)
	if err := os.Chtimes(
		filepath.Join(checkpointDir, newByMetadata.CheckpointID+".json"),
		oldFilesystemTime,
		oldFilesystemTime,
	); err != nil {
		t.Fatal(err)
	}

	removed, err := store.CleanupOldCheckpointFiles(time.Hour)
	if err != nil {
		t.Fatalf("CleanupOldCheckpointFiles: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed checkpoints = %d, want 1", removed)
	}
	if _, err := store.LoadCheckpoint(oldByMetadata.CheckpointID); err == nil {
		t.Fatal("old persisted timestamp survived age cleanup")
	}
	if _, err := store.LoadCheckpoint(newByMetadata.CheckpointID); err != nil {
		t.Fatalf("new persisted timestamp was removed due to old filesystem mtime: %v", err)
	}
}

func TestCheckpointScansRejectSymlinkAndNonRegularEntries(t *testing.T) {
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	good := checkpointForStoreTest("agent", "good-checkpoint", time.Now())
	if err := store.SaveCheckpoint(good); err != nil {
		t.Fatal(err)
	}
	checkpointDir := filepath.Join(store.dir, "checkpoints")

	t.Run("symlink", func(t *testing.T) {
		targetCheckpoint := checkpointForStoreTest("agent", "linked-checkpoint", time.Now().Add(time.Minute))
		data, err := json.Marshal(targetCheckpoint)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target.json")
		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(checkpointDir, targetCheckpoint.CheckpointID+".json")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(link) })

		if _, err := store.LoadCheckpoint(targetCheckpoint.CheckpointID); err == nil ||
			!strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("LoadCheckpoint(symlink) error = %v", err)
		}
		if _, err := store.ListCheckpoints("agent"); err == nil ||
			!strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("ListCheckpoints with symlink error = %v", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		path := filepath.Join(checkpointDir, "directory-checkpoint.json")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(path) })

		if _, err := store.LoadCheckpoint("directory-checkpoint"); err == nil ||
			!strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("LoadCheckpoint(directory) error = %v", err)
		}
		if _, err := store.CleanupCheckpoints("agent", 0); err == nil ||
			!strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("CleanupCheckpoints with directory error = %v", err)
		}
		if _, err := store.LoadCheckpoint(good.CheckpointID); err != nil {
			t.Fatalf("fail-closed cleanup removed valid checkpoint: %v", err)
		}
	})
}

func checkpointForStoreTest(agentID, checkpointID string, timestamp time.Time) *AgentCheckpoint {
	return &AgentCheckpoint{
		AgentState: &AgentState{
			ID:   agentID,
			Type: AgentTypeGeneral,
		},
		Timestamp:    timestamp,
		CheckpointID: checkpointID,
	}
}
