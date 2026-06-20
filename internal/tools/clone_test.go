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
