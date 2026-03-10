package ui

import (
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

func TestMultiDiffPreviewAcceptsWholeBatch(t *testing.T) {
	model := NewMultiDiffPreviewModel(DefaultStyles())
	model.SetFiles([]DiffFile{
		{FilePath: "a.txt", OldContent: "old\n", NewContent: "new\n"},
		{FilePath: "b.txt", OldContent: "", NewContent: "created\n", IsNewFile: true},
	})

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("expected accept-all command")
	}

	msg := cmd()
	response, ok := msg.(MultiDiffPreviewResponseMsg)
	if !ok {
		t.Fatalf("response type = %T, want MultiDiffPreviewResponseMsg", msg)
	}
	if updated.GetDecisions()["a.txt"] != DiffApply || updated.GetDecisions()["b.txt"] != DiffApply {
		t.Fatalf("updated decisions = %+v, want all apply", updated.GetDecisions())
	}
	if response.Decisions["a.txt"] != DiffApply || response.Decisions["b.txt"] != DiffApply {
		t.Fatalf("response decisions = %+v, want all apply", response.Decisions)
	}
}

func TestMultiDiffPreviewRejectsWholeBatch(t *testing.T) {
	model := NewMultiDiffPreviewModel(DefaultStyles())
	model.SetFiles([]DiffFile{
		{FilePath: "a.txt", OldContent: "old\n", NewContent: "new\n"},
		{FilePath: "b.txt", OldContent: "", NewContent: "created\n", IsNewFile: true},
	})

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd == nil {
		t.Fatal("expected reject-all command")
	}

	msg := cmd()
	response, ok := msg.(MultiDiffPreviewResponseMsg)
	if !ok {
		t.Fatalf("response type = %T, want MultiDiffPreviewResponseMsg", msg)
	}
	if updated.GetDecisions()["a.txt"] != DiffReject || updated.GetDecisions()["b.txt"] != DiffReject {
		t.Fatalf("updated decisions = %+v, want all reject", updated.GetDecisions())
	}
	if response.Decisions["a.txt"] != DiffReject || response.Decisions["b.txt"] != DiffReject {
		t.Fatalf("response decisions = %+v, want all reject", response.Decisions)
	}
}
