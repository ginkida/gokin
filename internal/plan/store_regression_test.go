package plan

import (
	"testing"
	"time"
)

// TestRegression_ListResumable_EmptyWorkDir verifies that ListResumable("")
// returns all resumable plans, not an empty list.
// Bug: inverted filter `if workDir == "" || ... { continue }` skipped everything.
// Fix: `if workDir != "" && p.WorkDir != "" && mismatch { continue }`.
func TestRegression_ListResumable_EmptyWorkDir(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPlanStore(dir)
	if err != nil {
		t.Fatalf("NewPlanStore: %v", err)
	}

	plan := &Plan{
		ID:      "test-plan-1",
		Title:   "Test Plan",
		WorkDir: "/some/project",
		Status:  StatusPaused,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.Save(plan); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// ListResumable with empty workDir should return ALL resumable plans
	resumable, err := store.ListResumable("")
	if err != nil {
		t.Fatalf("ListResumable: %v", err)
	}
	if len(resumable) == 0 {
		t.Error("ListResumable(\"\") returned empty list — inverted filter bug")
	}
}

// TestRegression_ListResumable_StrictMatch verifies that ListResumable
// with a specific workDir only returns plans from that directory.
func TestRegression_ListResumable_StrictMatch(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPlanStore(dir)
	if err != nil {
		t.Fatalf("NewPlanStore: %v", err)
	}

	for _, wd := range []string{"/project/a", "/project/b"} {
		plan := &Plan{
			ID:      "plan-" + wd[len(wd)-1:],
			Title:   "Plan " + wd,
			WorkDir: wd,
			Status:  StatusPaused,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		store.Save(plan)
	}

	resumable, err := store.ListResumable("/project/a")
	if err != nil {
		t.Fatalf("ListResumable: %v", err)
	}
	if len(resumable) != 1 {
		t.Errorf("expected 1 plan for /project/a, got %d", len(resumable))
	}
}

// TestRegression_ListResumable_SkipEmptyWorkDirPlans verifies that plans
// without a WorkDir are skipped when filtering by directory.
func TestRegression_ListResumable_SkipEmptyWorkDirPlans(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPlanStore(dir)
	if err != nil {
		t.Fatalf("NewPlanStore: %v", err)
	}

	plan := &Plan{
		ID:      "plan-no-workdir",
		Title:   "No WorkDir",
		WorkDir: "",
		Status:  StatusPaused,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	store.Save(plan)

	resumable, err := store.ListResumable("/specific/dir")
	if err != nil {
		t.Fatalf("ListResumable: %v", err)
	}
	if len(resumable) != 0 {
		t.Error("plans without WorkDir should be skipped when filtering by directory")
	}
}
