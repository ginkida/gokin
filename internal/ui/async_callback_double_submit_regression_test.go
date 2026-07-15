package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Model selection resolves on an App worker, but the selector closes to the
// composer as soon as the callback is launched. A repeated/auto-repeated Enter
// must not leak through to the newly revealed composer and submit an unrelated
// draft while the model switch is still pending.
func TestRapidModelSelectionEnterDoesNotSubmitUnderlyingDraft(t *testing.T) {
	m := *NewModel()
	m.input.textarea.SetValue("draft must remain unsent")
	m.SetAvailableModels([]ModelInfo{{ID: "fast"}, {ID: "reasoning"}})
	m.SetCurrentModel("fast")
	modelRequests := 0
	submissions := 0
	m.SetModelSelectCallbackWithID(func(string, string) { modelRequests++ })
	m.SetCallbacks(func(string) { submissions++ }, nil)
	m.openModelSelector()
	m.modelSelectedIndex = 1

	selected, _ := updateWorkspaceOwnershipModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if modelRequests != 1 || selected.modelSwitchPending != "reasoning" || selected.state != StateInput {
		t.Fatalf("setup: model request=%d pending=%q state=%v", modelRequests, selected.modelSwitchPending, selected.state)
	}
	afterRepeat, _ := updateWorkspaceOwnershipModel(t, selected, tea.KeyMsg{Type: tea.KeyEnter})

	if submissions != 0 {
		t.Fatalf("repeated selector Enter submitted the underlying composer %d time(s)", submissions)
	}
	if got := afterRepeat.input.Value(); got != "draft must remain unsent" {
		t.Fatalf("repeated selector Enter changed the underlying draft: %q", got)
	}
}

// Masked key submission has the same async-close lifecycle. The secret action
// and a composer submission are distinct user intents; one rapid Enter burst
// must not launch both while login/config reconstruction is pending.
func TestRapidKeyEntryEnterDoesNotSubmitUnderlyingDraft(t *testing.T) {
	m := *NewModel()
	m.input.textarea.SetValue("draft must remain unsent")
	keyRequests := 0
	submissions := 0
	m.SetKeyEntrySubmitCallbackWithID(func(string, string, string) { keyRequests++ })
	m.SetCallbacks(func(string) { submissions++ }, nil)
	m.openKeyEntry(OpenKeyEntryMsg{Provider: "glm", DisplayName: "GLM"})
	m.keyEntryInput.SetValue("sk-secret")

	submitted, _ := updateWorkspaceOwnershipModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if keyRequests != 1 || submitted.keyEntryPendingProvider != "glm" || submitted.state != StateInput {
		t.Fatalf("setup: key request=%d pending=%q state=%v", keyRequests, submitted.keyEntryPendingProvider, submitted.state)
	}
	afterRepeat, _ := updateWorkspaceOwnershipModel(t, submitted, tea.KeyMsg{Type: tea.KeyEnter})

	if submissions != 0 {
		t.Fatalf("repeated key-entry Enter submitted the underlying composer %d time(s)", submissions)
	}
	if got := afterRepeat.input.Value(); got != "draft must remain unsent" {
		t.Fatalf("repeated key-entry Enter changed the underlying draft: %q", got)
	}
}

// Palette actions such as session-mode cycling close directly back to the
// composer. A repeated confirmation Enter belongs to the palette interaction,
// not to the draft that becomes visible after the first keypress.
func TestRapidPaletteActionEnterDoesNotSubmitUnderlyingDraft(t *testing.T) {
	m := *NewModel()
	m.width, m.height = 80, 24
	m.input.textarea.SetValue("draft must remain unsent")
	cycleRequests := 0
	submissions := 0
	m.SetSessionModeCycleCallback(func() { cycleRequests++ })
	m.SetCallbacks(func(string) { submissions++ }, nil)
	m.ShowCommandPalette()
	m.commandPalette.SetQuery("Cycle Session Mode")

	selected := m.commandPalette.GetSelected()
	if selected == nil || selected.ActionID != paletteActionPlanningMode {
		t.Fatalf("setup: selected palette action=%+v", selected)
	}
	closed, _ := updateWorkspaceOwnershipModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cycleRequests != 1 || closed.state != StateInput {
		t.Fatalf("setup: cycle requests=%d state=%v", cycleRequests, closed.state)
	}
	afterRepeat, _ := updateWorkspaceOwnershipModel(t, closed, tea.KeyMsg{Type: tea.KeyEnter})

	if submissions != 0 {
		t.Fatalf("repeated palette Enter submitted the underlying composer %d time(s)", submissions)
	}
	if got := afterRepeat.input.Value(); got != "draft must remain unsent" {
		t.Fatalf("repeated palette Enter changed the underlying draft: %q", got)
	}

	// The guard is a one-shot interaction boundary, not a blanket 500ms send
	// delay: typing any new key releases it and the next Enter is intentional.
	afterTyping, _ := updateWorkspaceOwnershipModel(t, afterRepeat, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'!'}})
	_, _ = updateWorkspaceOwnershipModel(t, afterTyping, tea.KeyMsg{Type: tea.KeyEnter})
	if submissions != 1 {
		t.Fatalf("new text intent did not release the palette Enter guard: submissions=%d", submissions)
	}
}

// Blocking decisions return directly to the active Processing turn after the
// callback accepts a response. If the user had already composed type-ahead
// underneath the modal, an auto-repeated confirmation Enter must not also
// submit that draft as a second, unrelated action.
func TestRapidDecisionEnterDoesNotSubmitUnderlyingDraft(t *testing.T) {
	tests := []struct {
		name string
		open func(Model) Model
	}{
		{
			name: "permission",
			open: func(m Model) Model {
				m.SetPermissionCallback(func(string, PermissionDecision) {})
				opened, _ := updateWorkspaceOwnershipModel(t, m, PermissionRequestMsg{
					ID: "permission-1", ToolName: "bash", RiskLevel: "high",
				})
				return opened
			},
		},
		{
			name: "question",
			open: func(m Model) Model {
				m.SetQuestionCallbackWithID(func(string, string) {})
				opened, _ := updateWorkspaceOwnershipModel(t, m, QuestionRequestMsg{
					ID: "question-1", Question: "Continue?", Options: []string{"Yes", "No"},
				})
				return opened
			},
		},
		{
			name: "plan",
			open: func(m Model) Model {
				m.SetPlanApprovalCallbackWithID(func(string, PlanApprovalDecision) {})
				opened, _ := updateWorkspaceOwnershipModel(t, m, PlanApprovalRequestMsg{
					ID: "plan-1", Title: "Plan", Steps: []PlanStepInfo{{ID: 1, Title: "Step"}},
				})
				return opened
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := *NewModel()
			m.width, m.height = 80, 24
			m.state = StateProcessing
			m.input.textarea.SetValue("draft must remain unsent")
			submissions := 0
			m.SetCallbacks(func(string) { submissions++ }, nil)

			opened := tc.open(m)
			decided, _ := updateWorkspaceOwnershipModel(t, opened, tea.KeyMsg{Type: tea.KeyEnter})
			if decided.state != StateProcessing {
				t.Fatalf("setup: decision did not return to processing: %v", decided.state)
			}
			afterRepeat, _ := updateWorkspaceOwnershipModel(t, decided, tea.KeyMsg{Type: tea.KeyEnter})
			if submissions != 0 {
				t.Fatalf("repeated decision Enter submitted the underlying composer %d time(s)", submissions)
			}
			if got := afterRepeat.input.Value(); got != "draft must remain unsent" {
				t.Fatalf("repeated decision Enter changed the underlying draft: %q", got)
			}
		})
	}
}
