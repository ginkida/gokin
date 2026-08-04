//go:build !windows && !plan9

package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPlanStoreRepairsLegacyPrivateModes(t *testing.T) {
	configDir := t.TempDir()
	dir := filepath.Join(configDir, "plans")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "legacy.json")
	data := mustMarshalStoredPlan(t, &Plan{
		ID:        "legacy",
		Title:     "private plan",
		Status:    StatusPaused,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := NewPlanStore(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("legacy"); err != nil {
		t.Fatal(err)
	}
	assertPlanStoreMode(t, dir, 0o700)
	assertPlanStoreMode(t, path, 0o600)

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := store.Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(plan); err != nil {
		t.Fatal(err)
	}
	assertPlanStoreMode(t, path, 0o600)
}

func TestPlanStoreRejectsSymlinkedDirectoryWithoutTouchingTarget(t *testing.T) {
	configDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "external")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(configDir, "plans")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := NewPlanStore(configDir); err == nil {
		t.Fatal("NewPlanStore accepted a symlinked plans directory")
	}
	assertPlanStoreMode(t, target, 0o755)
}

func TestPlanStoreOperationsRejectSymlinkedFileWithoutTouchingTarget(t *testing.T) {
	store, err := NewPlanStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "external")
	targetData := mustMarshalStoredPlan(t, &Plan{
		ID:        "victim",
		Title:     "external",
		Status:    StatusPaused,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	if err := os.WriteFile(target, targetData, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(store.dir, "victim.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := store.Load("victim"); err == nil {
		t.Fatal("Load accepted a symlinked plan")
	}
	if err := store.Delete("victim"); err == nil {
		t.Fatal("Delete accepted a symlinked plan")
	}
	// Listing SKIPS the anomalous entry rather than failing: aborting took the
	// whole plan store down — including Cleanup, the path that exists to recover
	// from exactly this — over one bad file. The symlink is still never read.
	listed, err := store.List()
	if err != nil {
		t.Fatalf("List aborted over one symlinked entry: %v", err)
	}
	for _, plan := range listed {
		if plan.ID == "victim" {
			t.Fatal("List returned the symlinked plan")
		}
	}
	if _, err := store.Cleanup(time.Hour); err != nil {
		t.Fatalf("Cleanup aborted over one symlinked entry: %v", err)
	}
	if store.Exists("victim") {
		t.Fatal("Exists reported a symlinked plan")
	}

	data, err := os.ReadFile(target)
	if err != nil || string(data) != string(targetData) {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
	assertPlanStoreMode(t, target, 0o644)
}

func mustMarshalStoredPlan(t *testing.T, plan *Plan) []byte {
	t.Helper()
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertPlanStoreMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
