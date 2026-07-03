package tools

import (
	"sync"
	"testing"
)

// TestCloneToolForWorkDir_MemoryToolIsIsolated pins that each agent gets its OWN
// MemoryTool instance. Without a clone case the shared foreground instance is
// returned, and the per-agent SetLearning(pl) wiring then (a) races concurrent
// parallel spawns and (b) lets an isolated worktree agent clobber the
// foreground's learning pointer. The clone must be a distinct instance whose
// SetLearning writes its own field.
func TestCloneToolForWorkDir_MemoryToolIsIsolated(t *testing.T) {
	orig := NewMemoryTool()

	cloned := CloneToolForWorkDir(orig, "")
	ct, ok := cloned.(*MemoryTool)
	if !ok {
		t.Fatalf("clone is not a *MemoryTool: %T", cloned)
	}
	if ct == orig {
		t.Fatal("clone must be a distinct instance, not the shared foreground one")
	}

	// Concurrent SetLearning on independent clones must be race-free (run under
	// -race). With the shared instance this raced on t.learning.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := CloneToolForWorkDir(orig, "").(*MemoryTool)
			c.SetLearning(nil)
		}()
	}
	wg.Wait()
}

// TestCloneToolForWorkDir_MemorizeToolIsIsolated pins the same isolation for the
// MemorizeTool. This matters now that /loop routes each iteration through the
// per-agent clone path (SpawnWithContext): a shared instance would race the
// per-agent SetLearning under parallel spawns / let a worktree agent clobber the
// foreground's learning pointer.
func TestCloneToolForWorkDir_MemorizeToolIsIsolated(t *testing.T) {
	orig := NewMemorizeTool(nil)

	cloned := CloneToolForWorkDir(orig, "")
	ct, ok := cloned.(*MemorizeTool)
	if !ok {
		t.Fatalf("clone is not a *MemorizeTool: %T", cloned)
	}
	if ct == orig {
		t.Fatal("clone must be a distinct instance, not the shared foreground one")
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			CloneToolForWorkDir(orig, "").(*MemorizeTool).SetLearning(nil)
		}()
	}
	wg.Wait()
}

// TestCloneToolForWorkDir_TodoToolIsIsolated pins that each agent gets its OWN
// todo list: the clone is a distinct, empty instance and mutating it does not
// clobber the parent's list (the foreground task list). This is both the
// clobber-bug fix and what makes the sub-agent incomplete-work continuation read
// THIS agent's todos rather than the foreground's.
func TestCloneToolForWorkDir_TodoToolIsIsolated(t *testing.T) {
	orig := NewTodoTool()
	orig.RestoreItems([]TodoItem{{Content: "foreground task", Status: "in_progress"}})

	cloned := CloneToolForWorkDir(orig, "")
	ct, ok := cloned.(*TodoTool)
	if !ok {
		t.Fatalf("clone is not a *TodoTool: %T", cloned)
	}
	if ct == orig {
		t.Fatal("clone must be a distinct instance, not the shared foreground one")
	}
	if n := len(ct.GetItems()); n != 0 {
		t.Errorf("cloned todo should start empty (no inherited todos), got %d items", n)
	}

	// Mutating the clone must NOT leak into the original.
	ct.RestoreItems([]TodoItem{{Content: "subagent task", Status: "pending"}})
	items := orig.GetItems()
	if len(items) != 1 || items[0].Content != "foreground task" {
		t.Errorf("clone mutation clobbered the original list: %+v", items)
	}
}

// TestCloneToolForWorkDir_SemanticToolsIsolated pins that go_to_definition /
// find_references get their OWN per-agent instance scoped to the agent's workDir.
// Without a clone case the shared foreground instance was returned, so a
// worktree-isolated agent's SetAllowedDirs clobbered the shared pathValidator
// (and parallel isolated agents raced it).
func TestCloneToolForWorkDir_SemanticToolsIsolated(t *testing.T) {
	origDef := NewGoToDefinitionTool("/foreground")
	clonedDef, ok := CloneToolForWorkDir(origDef, "/worktree").(*GoToDefinitionTool)
	if !ok {
		t.Fatalf("go_to_definition clone wrong type: %T", CloneToolForWorkDir(origDef, "/worktree"))
	}
	if clonedDef == origDef {
		t.Fatal("go_to_definition clone must be a distinct instance")
	}
	if clonedDef.workDir != "/worktree" {
		t.Fatalf("clone workDir = %q, want /worktree", clonedDef.workDir)
	}

	origRef := NewFindReferencesTool("/foreground")
	clonedRef, ok := CloneToolForWorkDir(origRef, "/worktree").(*FindReferencesTool)
	if !ok || clonedRef == origRef || clonedRef.workDir != "/worktree" {
		t.Fatal("find_references clone not isolated/scoped to the worktree")
	}

	// Empty override → inherit the source tool's own workDir (pickWorkDir).
	inherited := CloneToolForWorkDir(NewGoToDefinitionTool("/foreground"), "").(*GoToDefinitionTool)
	if inherited.workDir != "/foreground" {
		t.Fatalf("empty override should inherit source workDir, got %q", inherited.workDir)
	}

	// Concurrent SetAllowedDirs on independent clones must be race-free (-race).
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			CloneToolForWorkDir(origDef, "/wt").(*GoToDefinitionTool).SetAllowedDirs([]string{"/grant"})
		}()
	}
	wg.Wait()
}

// TestCloneToolForWorkDir_ReviewChangesIsolated pins that review_changes follows
// the agent's workDir — without a clone case an isolated agent's self-review ran
// git against the foreground repo, not its own worktree.
func TestCloneToolForWorkDir_ReviewChangesIsolated(t *testing.T) {
	orig := NewReviewChangesTool("/foreground")
	cloned, ok := CloneToolForWorkDir(orig, "/worktree").(*ReviewChangesTool)
	if !ok {
		t.Fatalf("review_changes clone wrong type: %T", CloneToolForWorkDir(orig, "/worktree"))
	}
	if cloned == orig {
		t.Fatal("review_changes clone must be a distinct instance")
	}
	if cloned.workDir != "/worktree" {
		t.Fatalf("clone workDir = %q, want /worktree", cloned.workDir)
	}
}
