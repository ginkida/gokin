package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func batchResolvedTemp(t *testing.T) string {
	t.Helper()
	// EvalSymlinks: macOS /var → /private/var, which PathValidator would reject.
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestBatchTool_PathValidation pins that the batch tool rejects model-supplied
// paths outside the workspace and inside .git for every mutating op — it
// previously had NO path validation (it could delete/overwrite arbitrary paths).
func TestBatchTool_PathValidation(t *testing.T) {
	work := batchResolvedTemp(t)
	outside := batchResolvedTemp(t) // a distinct dir, outside the workspace
	bt := NewBatchTool(work)

	// DELETE: out-of-workspace path rejected AND the file is left intact.
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := bt.executeDelete(context.Background(), []string{victim}, false, false)
	if _, failed := res.Failed[victim]; !failed {
		t.Errorf("out-of-workspace delete must be rejected; Failed=%v Succeeded=%v", res.Failed, res.Succeeded)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("victim file must NOT be deleted: %v", err)
	}

	// DELETE: a path inside .git is rejected (.git protection).
	gitFile := filepath.Join(work, ".git", "config")
	res2 := bt.executeDelete(context.Background(), []string{gitFile}, false, false)
	if _, failed := res2.Failed[gitFile]; !failed {
		t.Errorf(".git delete must be rejected; Failed=%v", res2.Failed)
	}

	// REPLACE: out-of-workspace write rejected (file content untouched).
	if err := bt.replaceInFile(victim, "keep", "gone", false); err == nil {
		t.Error("out-of-workspace replace must be rejected")
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep me" {
		t.Errorf("victim content must be untouched, got %q", string(b))
	}

	// An IN-workspace file still works (validation doesn't over-block).
	inside := filepath.Join(work, "ok.txt")
	if err := os.WriteFile(inside, []byte("alpha beta"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := bt.replaceInFile(inside, "alpha", "gamma", false); err != nil {
		t.Errorf("in-workspace replace should succeed: %v", err)
	}
	if b, _ := os.ReadFile(inside); string(b) != "gamma beta" {
		t.Errorf("in-workspace replace wrong result: %q", string(b))
	}
}

// TestBatchTool_Execute_AllFailedReportsError pins the honesty fix: when
// EVERY file in a batch operation fails (here, all paths are outside the
// workspace and rejected by path validation), formatResult must return
// Success:false, not a success with a "Failed: N" line buried in the
// content — the same "tool must never report success on an all-error path"
// class already fixed for refactor's executeRename.
func TestBatchTool_Execute_AllFailedReportsError(t *testing.T) {
	work := batchResolvedTemp(t)
	outside := batchResolvedTemp(t)
	bt := NewBatchTool(work)

	victim1 := filepath.Join(outside, "victim1.txt")
	victim2 := filepath.Join(outside, "victim2.txt")
	for _, v := range []string{victim1, victim2} {
		if err := os.WriteFile(v, []byte("keep me"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := bt.Execute(context.Background(), map[string]any{
		"operation": "delete",
		"files":     []any{victim1, victim2},
	})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.Success {
		t.Errorf("Execute() with all-failed batch delete reported Success:true; want false. Content=%q", result.Content)
	}
	if result.Error == "" {
		t.Error("Execute() with all-failed batch delete has empty Error field")
	}
	// Neither victim was actually deleted (validation correctly rejected both).
	for _, v := range []string{victim1, victim2} {
		if _, statErr := os.Stat(v); statErr != nil {
			t.Errorf("victim file %s must NOT be deleted: %v", v, statErr)
		}
	}
}

// A MIXED batch (some succeed, some fail) must keep reporting success with
// the failure detail intact — only the ALL-failed case is downgraded.
func TestBatchTool_Execute_PartialFailureStillSuccess(t *testing.T) {
	work := batchResolvedTemp(t)
	outside := batchResolvedTemp(t)
	bt := NewBatchTool(work)

	good := filepath.Join(work, "good.txt")
	if err := os.WriteFile(good, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := bt.Execute(context.Background(), map[string]any{
		"operation": "delete",
		"files":     []any{good, victim},
	})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() with a partial success reported Success:false; want true. Error=%q", result.Error)
	}
}
