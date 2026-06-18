package tools

import "testing"

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
