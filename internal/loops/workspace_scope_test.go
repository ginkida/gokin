package loops

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Loops persist in the GLOBAL config dir while every iteration is spawned
// against the SCHEDULER's workspace. Unowned, that meant any gokin process
// adopted every loop on disk and ran it in its own repository — an unattended
// agent editing, building and committing in the wrong project, on the user's
// quota.
func TestSchedulerOnlyFiresItsOwnWorkspaceLoops(t *testing.T) {
	projectA := t.TempDir()
	projectB := t.TempDir()

	mgr := NewManager(NewFileStorage(filepath.Join(t.TempDir(), "loops")))
	mgr.SetWorkDir(projectA)
	owned, err := mgr.Add("fix the failing tests", ModeSelfPaced, 0)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if owned.WorkDir != projectA {
		t.Fatalf("a new loop must be stamped with its creating workspace, got %q", owned.WorkDir)
	}

	var fired []string
	runner := NewRunner(mgr, func(context.Context, string) (SpawnResult, error) {
		return SpawnResult{Output: "done", OK: true}, nil
	}, func() bool { return true })
	runner.SetWorkDir(projectB) // a gokin opened in ANOTHER project

	if runner.ownsLoop(owned) {
		t.Fatal("project B must not own project A's loop")
	}
	_ = fired

	// The owner still runs it.
	runnerA := NewRunner(mgr, nil, func() bool { return true })
	runnerA.SetWorkDir(projectA)
	if !runnerA.ownsLoop(owned) {
		t.Fatal("the creating workspace must still own its loop")
	}
}

// A loop written before loops carried an owner must not silently stop firing:
// the first scheduler that sees it adopts it, and the binding is persisted so
// no OTHER project picks it up afterwards.
func TestLegacyLoopIsAdoptedOnceNotSilentlyKilled(t *testing.T) {
	projectA := t.TempDir()
	projectB := t.TempDir()
	storage := NewFileStorage(filepath.Join(t.TempDir(), "loops"))

	legacy := &Loop{
		ID:              NewID(),
		Task:            "legacy task",
		Mode:            ModeSelfPaced,
		Status:          StatusRunning,
		CreatedAt:       time.Now(),
		MinDelaySeconds: DefaultMinDelaySeconds,
	}
	if err := storage.Save(legacy); err != nil {
		t.Fatalf("Save: %v", err)
	}

	mgr := NewManager(storage) // constructor loads from disk
	loaded := mgr.Active()
	if len(loaded) != 1 {
		t.Fatalf("expected the legacy loop to load, got %d", len(loaded))
	}

	runnerA := NewRunner(mgr, nil, func() bool { return true })
	runnerA.SetWorkDir(projectA)
	if !runnerA.ownsLoop(loaded[0]) {
		t.Fatal("an unowned legacy loop must be adopted, not silently killed")
	}

	// Adoption is persisted and exclusive from then on.
	reloaded, ok := mgr.Get(legacy.ID)
	if !ok || reloaded.WorkDir != projectA {
		t.Fatalf("adoption was not persisted: %+v", reloaded)
	}
	runnerB := NewRunner(mgr, nil, func() bool { return true })
	runnerB.SetWorkDir(projectB)
	if runnerB.ownsLoop(reloaded) {
		t.Fatal("after adoption another workspace must not claim the loop")
	}
}
