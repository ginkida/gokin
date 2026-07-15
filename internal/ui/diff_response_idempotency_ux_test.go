package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSingleDiffDecisionEmitsOnlyOnceWhileParentResponseIsPending(t *testing.T) {
	m := NewDiffPreviewModel(DefaultStyles())
	m.SetContent("main.go", "old\n", "new\n", "edit", false)
	callbackCalls := 0
	m.SetDecisionCallback(func(DiffDecision) { callbackCalls++ })

	first, firstCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	_, duplicateCmd := first.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if firstCmd == nil {
		t.Fatal("first diff decision did not emit a response")
	}
	if duplicateCmd != nil || callbackCalls != 1 {
		t.Fatalf("rapid duplicate emitted twice: duplicate=%v callbacks=%d", duplicateCmd != nil, callbackCalls)
	}
}

func TestMultiDiffDecisionEmitsOnlyOnceWhileParentResponseIsPending(t *testing.T) {
	decisionKeys := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'y'}},
		{Type: tea.KeyRunes, Runes: []rune{'n'}},
		{Type: tea.KeyRunes, Runes: []rune{'A'}},
		{Type: tea.KeyRunes, Runes: []rune{'R'}},
		{Type: tea.KeyEsc},
		{Type: tea.KeyEnter},
	}

	for _, key := range decisionKeys {
		t.Run(key.String(), func(t *testing.T) {
			m := NewMultiDiffPreviewModel(DefaultStyles())
			m.SetFiles([]DiffFile{{FilePath: "main.go", OldContent: "old\n", NewContent: "new\n"}})
			callbackCalls := 0
			m.SetCompleteCallback(func(map[string]DiffDecision) { callbackCalls++ })

			pending, firstCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
			if firstCmd == nil {
				t.Fatal("first multi-diff decision did not emit a response")
			}
			emitted := firstCmd().(MultiDiffPreviewResponseMsg)
			after, duplicateCmd := pending.Update(key)

			if duplicateCmd != nil || callbackCalls != 1 {
				t.Fatalf("pending key %q emitted twice: duplicate=%v callbacks=%d", key.String(), duplicateCmd != nil, callbackCalls)
			}
			if got := after.GetDecisions()["main.go"]; got != DiffApply {
				t.Fatalf("pending key %q changed visible decision to %v; emitted snapshot remains %v", key.String(), got, emitted.Decisions["main.go"])
			}
			if emitted.Decisions["main.go"] != DiffApply || !after.responsePending || after.confirmFinish {
				t.Fatalf("pending key %q corrupted lifecycle: emitted=%v pending=%v confirm=%v", key.String(), emitted.Decisions, after.responsePending, after.confirmFinish)
			}
		})
	}
}

func TestStaleSingleDiffResponseCannotClobberANewerPrompt(t *testing.T) {
	m := NewModel()
	diffCalls := 0
	m.SetDiffDecisionCallback(func(DiffDecision) { diffCalls++ })
	m.SetPermissionCallback(func(string, PermissionDecision) {})

	opened, _ := m.Update(DiffPreviewRequestMsg{
		FilePath: "main.go", OldContent: "old\n", NewContent: "new\n", ToolName: "edit",
	})
	review := opened.(Model)
	decided, responseCmd := review.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if responseCmd == nil {
		t.Fatal("diff decision did not emit a parent response")
	}
	settled, _ := decided.(Model).Update(responseCmd())
	got := settled.(Model)
	if got.state != StateProcessing || diffCalls != 1 || got.diffRequest != nil {
		t.Fatalf("first response did not settle once: state=%v calls=%d request=%v", got.state, diffCalls, got.diffRequest)
	}

	prompted, _ := got.Update(PermissionRequestMsg{ID: "next", ToolName: "bash"})
	got = prompted.(Model)
	if got.state != StatePermissionPrompt || got.permRequest == nil {
		t.Fatalf("newer permission prompt did not open: state=%v request=%v", got.state, got.permRequest)
	}

	replayed, _ := got.Update(responseCmd())
	got = replayed.(Model)
	if got.state != StatePermissionPrompt || got.permRequest == nil || diffCalls != 1 {
		t.Fatalf("stale diff response clobbered newer prompt: state=%v request=%v calls=%d", got.state, got.permRequest, diffCalls)
	}
}

func TestStaleMultiDiffResponseCannotClobberANewerPrompt(t *testing.T) {
	m := NewModel()
	diffCalls := 0
	m.SetMultiDiffDecisionCallback(func(map[string]DiffDecision) { diffCalls++ })
	m.SetPermissionCallback(func(string, PermissionDecision) {})

	opened, _ := m.Update(MultiDiffPreviewRequestMsg{Files: []DiffFile{{
		FilePath: "main.go", OldContent: "old\n", NewContent: "new\n",
	}}})
	review := opened.(Model)
	decided, responseCmd := review.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if responseCmd == nil {
		t.Fatal("multi-diff decision did not emit a parent response")
	}
	settled, _ := decided.(Model).Update(responseCmd())
	got := settled.(Model)
	if got.state != StateProcessing || diffCalls != 1 || got.multiDiffRequest != nil {
		t.Fatalf("first response did not settle once: state=%v calls=%d request=%v", got.state, diffCalls, got.multiDiffRequest)
	}

	prompted, _ := got.Update(PermissionRequestMsg{ID: "next", ToolName: "bash"})
	got = prompted.(Model)
	replayed, _ := got.Update(responseCmd())
	got = replayed.(Model)
	if got.state != StatePermissionPrompt || got.permRequest == nil || diffCalls != 1 {
		t.Fatalf("stale multi-diff response clobbered newer prompt: state=%v request=%v calls=%d", got.state, got.permRequest, diffCalls)
	}
}
