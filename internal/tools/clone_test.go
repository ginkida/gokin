package tools

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gokin/internal/undo"
)

// TestCloneToolForWorkDir_SkillToolUsesAgentWorkspace pins the worktree
// boundary for project-local skills. A cloned registry used to retain the
// foreground SkillTool (and therefore its foreground catalog), so an isolated
// sub-agent could neither see the worktree's workflows nor avoid loading a
// same-named workflow from the wrong checkout.
func TestCloneToolForWorkDir_SkillToolUsesAgentWorkspace(t *testing.T) {
	// Keep the global Gokin root deterministic; the unique project skill names
	// also make any user-level Claude skill directory irrelevant to this test.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	foreground := t.TempDir()
	worktree := t.TempDir()
	writeToolSkill(t, filepath.Join(foreground, ".gokin", "skills"), "foreground-clone-proof",
		"---\nname: foreground-clone-proof\ndescription: Foreground-only workflow\n---\nFOREGROUND_WORKFLOW")
	writeToolSkill(t, filepath.Join(worktree, ".gokin", "skills"), "worktree-clone-proof",
		"---\nname: worktree-clone-proof\ndescription: Worktree-only workflow\n---\nWORKTREE_WORKFLOW")

	original := NewSkillTool(foreground)
	cloned, ok := CloneToolForWorkDir(original, worktree).(*SkillTool)
	if !ok {
		t.Fatalf("skill clone has type %T, want *SkillTool", CloneToolForWorkDir(original, worktree))
	}
	if cloned == original {
		t.Fatal("skill clone must be a distinct tool instance")
	}

	loaded, err := cloned.Execute(context.Background(), map[string]any{"name": "worktree-clone-proof"})
	if err != nil || !loaded.Success || !strings.Contains(loaded.Content, "WORKTREE_WORKFLOW") {
		t.Fatalf("worktree skill was not loaded by clone: result=%#v err=%v", loaded, err)
	}
	wrongWorkspace, err := cloned.Execute(context.Background(), map[string]any{"name": "foreground-clone-proof"})
	if err != nil || wrongWorkspace.Success {
		t.Fatalf("clone leaked foreground project skill: result=%#v err=%v", wrongWorkspace, err)
	}

	originalResult, err := original.Execute(context.Background(), map[string]any{"name": "foreground-clone-proof"})
	if err != nil || !originalResult.Success || !strings.Contains(originalResult.Content, "FOREGROUND_WORKFLOW") {
		t.Fatalf("cloning changed original skill binding: result=%#v err=%v", originalResult, err)
	}
	originalLeak, err := original.Execute(context.Background(), map[string]any{"name": "worktree-clone-proof"})
	if err != nil || originalLeak.Success {
		t.Fatalf("original leaked worktree project skill: result=%#v err=%v", originalLeak, err)
	}
}

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

func TestCloneToolForWorkDir_BashPreservesNarrowBoundaryWithoutOverride(t *testing.T) {
	constructionDir := t.TempDir()
	narrowBoundary := filepath.Join(constructionDir, "narrow")
	original := NewBashTool(constructionDir)
	original.SetWorkspaceBoundary(narrowBoundary)

	cloned, ok := CloneToolForWorkDir(original, "").(*BashTool)
	if !ok {
		t.Fatalf("bash clone has type %T, want *BashTool", CloneToolForWorkDir(original, ""))
	}
	cloned.policyMu.RLock()
	gotEnabled := cloned.workspaceBoundaryEnabled
	gotRoot := cloned.workspaceRoot
	cloned.policyMu.RUnlock()
	if !gotEnabled || gotRoot != narrowBoundary {
		t.Fatalf("bash clone widened source boundary: enabled=%v root=%q want=%q", gotEnabled, gotRoot, narrowBoundary)
	}
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

// TestCloneToolForWorkDir_UndoManagerPropagated (round 4) pins that write/edit/
// copy/move/delete/mkdir clones carry the foreground undoManager forward.
// Without this, every sub-agent (task tool, /loop, coordinate, router
// sub-agent strategies — CloneToolForWorkDir runs for EVERY spawn, not only
// workspace-isolated ones) got a FRESH tool instance with a nil undoManager;
// each tool's Execute guards its undo.Manager.Record() call with
// `if t.undoManager != nil`, so the mutation succeeded on disk but was never
// recorded — silently invisible to /undo. *BatchTool and *RefactorTool already
// copy undoManager correctly; this test covers the six that didn't.
func TestCloneToolForWorkDir_UndoManagerPropagated(t *testing.T) {
	um := undo.NewManager()

	writeOrig := NewWriteTool("/foreground")
	writeOrig.SetUndoManager(um)
	writeClone, ok := CloneToolForWorkDir(writeOrig, "").(*WriteTool)
	if !ok || writeClone.undoManager != um {
		t.Errorf("WriteTool clone lost undoManager: ok=%v got=%p want=%p", ok, writeClone.undoManager, um)
	}

	editOrig := NewEditTool("/foreground")
	editOrig.SetUndoManager(um)
	editClone, ok := CloneToolForWorkDir(editOrig, "").(*EditTool)
	if !ok || editClone.undoManager != um {
		t.Errorf("EditTool clone lost undoManager: ok=%v got=%p want=%p", ok, editClone.undoManager, um)
	}

	copyOrig := NewCopyTool("/foreground")
	copyOrig.SetUndoManager(um)
	copyClone, ok := CloneToolForWorkDir(copyOrig, "").(*CopyTool)
	if !ok || copyClone.undoManager != um {
		t.Errorf("CopyTool clone lost undoManager: ok=%v got=%p want=%p", ok, copyClone.undoManager, um)
	}

	moveOrig := NewMoveTool("/foreground")
	moveOrig.SetUndoManager(um)
	moveClone, ok := CloneToolForWorkDir(moveOrig, "").(*MoveTool)
	if !ok || moveClone.undoManager != um {
		t.Errorf("MoveTool clone lost undoManager: ok=%v got=%p want=%p", ok, moveClone.undoManager, um)
	}

	deleteOrig := NewDeleteTool("/foreground")
	deleteOrig.SetUndoManager(um)
	deleteClone, ok := CloneToolForWorkDir(deleteOrig, "").(*DeleteTool)
	if !ok || deleteClone.undoManager != um {
		t.Errorf("DeleteTool clone lost undoManager: ok=%v got=%p want=%p", ok, deleteClone.undoManager, um)
	}

	mkdirOrig := NewMkdirTool("/foreground")
	mkdirOrig.SetUndoManager(um)
	mkdirClone, ok := CloneToolForWorkDir(mkdirOrig, "").(*MkdirTool)
	if !ok || mkdirClone.undoManager != um {
		t.Errorf("MkdirTool clone lost undoManager: ok=%v got=%p want=%p", ok, mkdirClone.undoManager, um)
	}
}
