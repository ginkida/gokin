package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMultiDiffPreviewStateAndCallback(t *testing.T) {
	model := NewModel()

	files := []DiffFile{
		{FilePath: "a.txt", OldContent: "old\n", NewContent: "new\n"},
		{FilePath: "b.txt", OldContent: "", NewContent: "created\n", IsNewFile: true},
	}

	var callbackDecisions map[string]DiffDecision
	model.SetMultiDiffDecisionCallback(func(decisions map[string]DiffDecision) {
		callbackDecisions = decisions
	})

	updatedAny, _ := model.Update(MultiDiffPreviewRequestMsg{Files: files})
	updated, ok := updatedAny.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want ui.Model", updatedAny)
	}

	if updated.state != StateMultiDiffPreview {
		t.Fatalf("state = %v, want %v", updated.state, StateMultiDiffPreview)
	}
	if updated.multiDiffRequest == nil {
		t.Fatal("expected multi diff request to be stored")
	}

	decisions := map[string]DiffDecision{
		"a.txt": DiffApply,
		"b.txt": DiffReject,
	}
	updatedAny, _ = updated.Update(MultiDiffPreviewResponseMsg{Decisions: decisions})
	updated, ok = updatedAny.(Model)
	if !ok {
		t.Fatalf("updated model type after response = %T, want ui.Model", updatedAny)
	}

	if updated.state != StateInput {
		t.Fatalf("state after mixed decisions = %v, want %v", updated.state, StateInput)
	}
	if updated.multiDiffRequest != nil {
		t.Fatal("expected multi diff request to be cleared after response")
	}
	if len(callbackDecisions) != 2 {
		t.Fatalf("callback decisions len = %d, want 2", len(callbackDecisions))
	}
	if callbackDecisions["b.txt"] != DiffReject {
		t.Fatalf("callback decision for b.txt = %v, want %v", callbackDecisions["b.txt"], DiffReject)
	}
}

func TestMultiDiffPreview_LowerYAdvancesInsteadOfFinishing(t *testing.T) {
	// Semantic change: `y` now marks the CURRENT file apply and advances.
	// It only finishes when all files are resolved. Previously `y` was
	// apply-all-and-finish, which forced users through multiple review
	// rounds for mixed outcomes.
	model := NewMultiDiffPreviewModel(DefaultStyles())
	model.SetFiles([]DiffFile{
		{FilePath: "a.txt", OldContent: "old\n", NewContent: "new\n"},
		{FilePath: "b.txt", OldContent: "", NewContent: "created\n", IsNewFile: true},
	})

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd != nil {
		t.Fatal("y on file 1 of 2 should not yet finish")
	}
	if updated.GetDecisions()["a.txt"] != DiffApply {
		t.Errorf("a.txt decision = %v, want Apply", updated.GetDecisions()["a.txt"])
	}
	if updated.GetDecisions()["b.txt"] != DiffPending {
		t.Errorf("b.txt decision = %v, want Pending (only current file acted on)", updated.GetDecisions()["b.txt"])
	}
}

func TestMultiDiffPreview_UpperYBulkApplies(t *testing.T) {
	// Bulk apply still available via Shift+Y (or A).
	model := NewMultiDiffPreviewModel(DefaultStyles())
	model.SetFiles([]DiffFile{
		{FilePath: "a.txt", OldContent: "old\n", NewContent: "new\n"},
		{FilePath: "b.txt", OldContent: "", NewContent: "created\n", IsNewFile: true},
	})

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Y'}})
	if cmd == nil {
		t.Fatal("Y (bulk) must finish immediately")
	}
	response := cmd().(MultiDiffPreviewResponseMsg)
	if updated.GetDecisions()["a.txt"] != DiffApply || updated.GetDecisions()["b.txt"] != DiffApply {
		t.Fatalf("decisions = %+v, want all apply", updated.GetDecisions())
	}
	if response.Decisions["a.txt"] != DiffApply || response.Decisions["b.txt"] != DiffApply {
		t.Fatalf("response = %+v, want all apply", response.Decisions)
	}
}

func TestMultiDiffPreview_EscRejectsAllForSafety(t *testing.T) {
	// Esc always means "reject everything" — treated as explicit bail-out,
	// unlike Enter which only rejects the currently-pending.
	model := NewMultiDiffPreviewModel(DefaultStyles())
	model.SetFiles([]DiffFile{
		{FilePath: "a.txt", OldContent: "old\n", NewContent: "new\n"},
		{FilePath: "b.txt", OldContent: "", NewContent: "created\n", IsNewFile: true},
	})
	// Pre-approve file A to confirm Esc overrides explicit decisions.
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("Esc must finish")
	}
	_ = cmd()
	if updated.GetDecisions()["a.txt"] != DiffReject || updated.GetDecisions()["b.txt"] != DiffReject {
		t.Errorf("Esc should reject ALL regardless of prior decisions: %+v", updated.GetDecisions())
	}
}

func TestMultiDiffPreview_PerFileMixedDecisionsSucceed(t *testing.T) {
	model := NewMultiDiffPreviewModel(DefaultStyles())
	model.SetFiles([]DiffFile{
		{FilePath: "a.txt", OldContent: "old\n", NewContent: "new\n"},
		{FilePath: "b.txt", OldContent: "", NewContent: "created\n", IsNewFile: true},
		{FilePath: "c.txt", OldContent: "c", NewContent: "c2"},
	})

	// Bubble Tea Update is value-semantic: must capture the updated model
	// and pass it into the next Update, otherwise currentIndex is stuck at 0.
	var cmd tea.Cmd
	model, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd != nil {
		t.Fatal("no finish after first decision")
	}
	model, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd != nil {
		t.Fatal("no finish after second decision")
	}
	model, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("auto-finish after last file resolved")
	}

	want := map[string]DiffDecision{
		"a.txt": DiffApply,
		"b.txt": DiffReject,
		"c.txt": DiffApply,
	}
	for path, expected := range want {
		if got := model.GetDecisions()[path]; got != expected {
			t.Errorf("%s decision = %v, want %v", path, got, expected)
		}
	}
}

func TestMultiDiffPreview_EnterFinishesWithPendingAsReject(t *testing.T) {
	// Enter treats "pending" as reject for safety — don't apply what the
	// user didn't explicitly approve.
	model := NewMultiDiffPreviewModel(DefaultStyles())
	model.SetFiles([]DiffFile{
		{FilePath: "a.txt", OldContent: "old\n", NewContent: "new\n"},
		{FilePath: "b.txt", OldContent: "", NewContent: "created\n", IsNewFile: true},
	})
	// Approve a explicitly.
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	// Enter with b still pending.
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should finish")
	}
	_ = cmd()
	if updated.GetDecisions()["a.txt"] != DiffApply {
		t.Errorf("a.txt should stay Apply after Enter: %v", updated.GetDecisions()["a.txt"])
	}
	if updated.GetDecisions()["b.txt"] != DiffReject {
		t.Errorf("b.txt should become Reject on Enter (pending→reject safety): %v", updated.GetDecisions()["b.txt"])
	}
}

func TestDiffPreview_ResizeCrossingThresholdRerendersContent(t *testing.T) {
	// Regression: SetSize used to only update viewport dims without
	// calling refreshDiffView. A user opening a diff on a wide terminal
	// and then narrowing the window would see stale split-mode content
	// instead of the auto-fallback unified view.
	m := NewDiffPreviewModel(DefaultStyles())

	// Start wide — split mode renders with "OLD" / "NEW" column headers.
	m.SetSize(120, 20)
	m.SetContent("x.go", "alpha\nbeta", "alpha\ngamma", "edit", false)
	if !strings.Contains(m.viewport.View(), "OLD") {
		t.Fatalf("wide viewport should render split columns with OLD header; got:\n%s", m.viewport.View())
	}

	// Narrow — must flip to unified (no OLD/NEW headers, uses +/- prefixes).
	m.SetSize(60, 20)
	narrow := m.viewport.View()
	if strings.Contains(narrow, "OLD") {
		t.Errorf("narrow viewport still shows split OLD header (stale content):\n%s", narrow)
	}
	// Unified diff shows removed/added markers.
	if !strings.Contains(narrow, "-") || !strings.Contains(narrow, "+") {
		t.Errorf("narrow viewport should show unified diff markers; got:\n%s", narrow)
	}

	// Widen again — must flip back.
	m.SetSize(120, 20)
	if !strings.Contains(m.viewport.View(), "OLD") {
		t.Errorf("re-widened viewport should return to split mode; got:\n%s", m.viewport.View())
	}
}

func TestDiffPreview_WidthBelowThresholdFallsBackToUnified(t *testing.T) {
	// Split mode is user's preference, but on narrow terminals (viewport
	// width < 80) the two columns shrink to the 20-char floor and truncate
	// context. Auto-fallback to unified keeps the content readable.
	//
	// Note: SetSize subtracts 4 chars of chrome from window width for the
	// viewport, so terminal-width 60 → viewport-width 56 (well below threshold).
	m := NewDiffPreviewModel(DefaultStyles())
	m.SetContent("file.go", "old line 1\nold line 2", "new line 1\nnew line 2", "edit", false)

	// Clearly narrow: should fall back to unified.
	m.SetSize(60, 20)
	if m.effectiveSideBySide() {
		t.Error("at terminal width 60 (viewport 56, below 80 threshold) split must fall back")
	}

	// Clearly wide: honor user preference.
	m.SetSize(120, 20)
	if !m.effectiveSideBySide() {
		t.Error("at terminal width 120 split must be active")
	}

	// Just above boundary: terminal 84 → viewport 80 → at threshold.
	m.SetSize(84, 20)
	if !m.effectiveSideBySide() {
		t.Error("at viewport width 80 (strict < threshold) split must be active")
	}

	// Just below boundary: terminal 83 → viewport 79 → below threshold.
	m.SetSize(83, 20)
	if m.effectiveSideBySide() {
		t.Error("at viewport width 79 split must fall back")
	}
}

func TestMultiDiffPreview_BulkApplyPreservesExplicitRejects(t *testing.T) {
	// A (bulk apply) only touches pending — files already rejected keep
	// that decision.
	model := NewMultiDiffPreviewModel(DefaultStyles())
	model.SetFiles([]DiffFile{
		{FilePath: "a.txt", OldContent: "old\n", NewContent: "new\n"},
		{FilePath: "b.txt", OldContent: "", NewContent: "created\n", IsNewFile: true},
	})
	// Reject a explicitly.
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})

	// Bulk apply remaining.
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}})
	if cmd == nil {
		t.Fatal("A should finish")
	}
	_ = cmd()
	if updated.GetDecisions()["a.txt"] != DiffReject {
		t.Errorf("a.txt should stay Reject: %v", updated.GetDecisions()["a.txt"])
	}
	if updated.GetDecisions()["b.txt"] != DiffApply {
		t.Errorf("b.txt should become Apply: %v", updated.GetDecisions()["b.txt"])
	}
}
