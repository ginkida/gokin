package plan

import (
	"testing"
)

// --- findActiveContractStep (38.5% → 100%) ---

func TestFindActiveContractStep_Empty(t *testing.T) {
	if got := findActiveContractStep(nil, 0); got != nil {
		t.Fatal("expected nil for empty steps")
	}
}

func TestFindActiveContractStep_ByCurrentStepID(t *testing.T) {
	steps := []*Step{
		{ID: 1, Status: StatusPending},
		{ID: 2, Status: StatusPending},
		{ID: 3, Status: StatusPending},
	}
	got := findActiveContractStep(steps, 2)
	if got == nil || got.ID != 2 {
		t.Fatalf("expected step ID=2, got %+v", got)
	}
}

func TestFindActiveContractStep_InProgressFallback(t *testing.T) {
	steps := []*Step{
		{ID: 1, Status: StatusCompleted},
		{ID: 2, Status: StatusInProgress},
		{ID: 3, Status: StatusPending},
	}
	// currentStepID=0 → fall through to in-progress search
	got := findActiveContractStep(steps, 0)
	if got == nil || got.ID != 2 {
		t.Fatalf("expected in-progress step ID=2, got %+v", got)
	}
}

func TestFindActiveContractStep_PendingFallback(t *testing.T) {
	steps := []*Step{
		{ID: 1, Status: StatusCompleted},
		{ID: 2, Status: StatusCompleted},
		{ID: 3, Status: StatusPending},
	}
	got := findActiveContractStep(steps, 0)
	if got == nil || got.ID != 3 {
		t.Fatalf("expected pending step ID=3, got %+v", got)
	}
}

func TestFindActiveContractStep_NoMatch(t *testing.T) {
	steps := []*Step{
		{ID: 1, Status: StatusCompleted},
		{ID: 2, Status: StatusFailed},
	}
	got := findActiveContractStep(steps, 0)
	if got != nil {
		t.Fatalf("expected nil when no match, got %+v", got)
	}
}

func TestFindActiveContractStep_CurrentStepIDNotFound(t *testing.T) {
	steps := []*Step{
		{ID: 1, Status: StatusInProgress},
	}
	// currentStepID=99 not found → fall through to in-progress
	got := findActiveContractStep(steps, 99)
	if got == nil || got.ID != 1 {
		t.Fatalf("expected fallback to in-progress step ID=1, got %+v", got)
	}
}

// --- compactContractText (80% → 100%) ---

func TestCompactContractText_ShortString(t *testing.T) {
	got := compactContractText("hello world", 100)
	if got != "hello world" {
		t.Fatalf("expected 'hello world', got %q", got)
	}
}

func TestCompactContractText_Truncates(t *testing.T) {
	got := compactContractText("hello world this is a long string", 10)
	if len(got) != 13 { // 10 + "..."
		t.Fatalf("expected 13 chars, got %d: %q", len(got), got)
	}
}

func TestCompactContractText_WhitespaceCollapsed(t *testing.T) {
	got := compactContractText("  hello   world  ", 100)
	if got != "hello world" {
		t.Fatalf("expected collapsed whitespace, got %q", got)
	}
}

func TestCompactContractText_ZeroMaxLen(t *testing.T) {
	got := compactContractText("hello", 0)
	if got != "hello" {
		t.Fatalf("maxLen=0 should return full string, got %q", got)
	}
}

// --- ClearPlanIfCurrent (50% → 100%) ---

func TestClearPlanIfCurrent_NilExpected(t *testing.T) {
	m := NewManager(true, true)
	if m.ClearPlanIfCurrent(nil) {
		t.Fatal("ClearPlanIfCurrent(nil) should return false")
	}
}

func TestClearPlanIfCurrent_StalePlan(t *testing.T) {
	m := NewManager(true, true)
	old := NewPlan("old", "desc")
	newPlan := NewPlan("new", "desc")
	m.SetPlan(newPlan)

	// old is not the current plan
	if m.ClearPlanIfCurrent(old) {
		t.Fatal("ClearPlanIfCurrent with stale plan should return false")
	}
}

func TestClearPlanIfCurrent_CurrentPlan(t *testing.T) {
	m := NewManager(true, true)
	p := NewPlan("current", "desc")
	m.SetPlan(p)

	if !m.ClearPlanIfCurrent(p) {
		t.Fatal("ClearPlanIfCurrent with current plan should return true")
	}
	if m.GetCurrentPlan() != nil {
		t.Fatal("plan should be nil after clear")
	}
}

// --- filterFileConflictsLocked (8% → higher) ---

func TestFilterFileConflictsLocked_NoLedger(t *testing.T) {
	p := NewPlan("test", "desc")
	candidates := []*Step{
		{ID: 1, Status: StatusPending},
		{ID: 2, Status: StatusPending},
	}
	// No RunLedger → should return all candidates
	got := p.filterFileConflictsLocked(candidates)
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates (no ledger), got %d", len(got))
	}
}

func TestFilterFileConflictsLocked_SingleCandidate(t *testing.T) {
	p := NewPlan("test", "desc")
	candidates := []*Step{{ID: 1, Status: StatusPending}}
	got := p.filterFileConflictsLocked(candidates)
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate (single), got %d", len(got))
	}
}

func TestFilterFileConflictsLocked_WithConflict(t *testing.T) {
	p := NewPlan("test", "desc")
	p.Steps = []*Step{
		{ID: 1, Status: StatusInProgress},
		{ID: 2, Status: StatusPending},
		{ID: 3, Status: StatusPending},
	}
	p.RunLedger = map[int]*RunLedgerEntry{
		1: {FilesTouched: []string{"main.go"}},
		2: {FilesTouched: []string{"main.go"}}, // conflicts with step 1
		3: {FilesTouched: []string{"other.go"}},
	}
	candidates := []*Step{p.Steps[1], p.Steps[2]}
	got := p.filterFileConflictsLocked(candidates)
	// Step 2 conflicts, step 3 is safe
	if len(got) != 1 || got[0].ID != 3 {
		t.Fatalf("expected only step 3 (safe), got %+v", got)
	}
}

func TestFilterFileConflictsLocked_AllConflict(t *testing.T) {
	p := NewPlan("test", "desc")
	p.Steps = []*Step{
		{ID: 1, Status: StatusInProgress},
		{ID: 2, Status: StatusPending},
		{ID: 3, Status: StatusPending},
	}
	p.RunLedger = map[int]*RunLedgerEntry{
		1: {FilesTouched: []string{"main.go"}},
		2: {FilesTouched: []string{"main.go"}},
		3: {FilesTouched: []string{"main.go"}},
	}
	candidates := []*Step{p.Steps[1], p.Steps[2]}
	got := p.filterFileConflictsLocked(candidates)
	// All conflict → return just the first one
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate (all conflict), got %d", len(got))
	}
}

// --- ResumePausedSteps (0% → 100%) ---

func TestResumePausedSteps_NoPlan(t *testing.T) {
	m := NewManager(true, true)
	if count := m.ResumePausedSteps(); count != 0 {
		t.Fatalf("expected 0 resumed with no plan, got %d", count)
	}
}

func TestResumePausedSteps_WithPausedSteps(t *testing.T) {
	m := NewManager(true, true)
	p := NewPlan("test", "desc")
	p.Steps = []*Step{
		{ID: 1, Status: StatusPaused},
		{ID: 2, Status: StatusCompleted},
		{ID: 3, Status: StatusPaused},
	}
	m.SetPlan(p)

	count := m.ResumePausedSteps()
	if count != 2 {
		t.Fatalf("expected 2 resumed, got %d", count)
	}
}

// --- HasPausedPlan (75% → 100%) ---

func TestHasPausedPlan_NoPlan(t *testing.T) {
	m := NewManager(true, true)
	if m.HasPausedPlan() {
		t.Fatal("expected false with no plan")
	}
}

func TestHasPausedPlan_WithPausedPlan(t *testing.T) {
	m := NewManager(true, true)
	p := NewPlan("test", "desc")
	p.Status = StatusPaused
	m.SetPlan(p)

	if !m.HasPausedPlan() {
		t.Fatal("expected true with paused plan")
	}
}

func TestHasPausedPlan_NoPausedSteps(t *testing.T) {
	m := NewManager(true, true)
	p := NewPlan("test", "desc")
	p.Steps = []*Step{{ID: 1, Status: StatusPending}}
	m.SetPlan(p)

	if m.HasPausedPlan() {
		t.Fatal("expected false with no paused steps")
	}
}

// --- GetLastRejectedPlan / SaveRejectedPlan ---

func TestSaveAndGetLastRejectedPlan(t *testing.T) {
	m := NewManager(true, true)
	p := NewPlan("rejected", "desc")
	m.SaveRejectedPlan(p)

	if got := m.GetLastRejectedPlan(); got != p {
		t.Fatal("GetLastRejectedPlan should return saved plan")
	}
}

// --- SetFeedback ---

func TestSetFeedback(t *testing.T) {
	m := NewManager(true, true)
	m.SetFeedback("please use better variable names")

	m.mu.RLock()
	fb := m.lastFeedback
	m.mu.RUnlock()
	if fb != "please use better variable names" {
		t.Fatalf("expected feedback, got %q", fb)
	}
}
