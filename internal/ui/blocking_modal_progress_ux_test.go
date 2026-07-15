package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func addCompletedProgressBehindModal(t *testing.T, m *Model) Model {
	t.Helper()
	modalState := m.state
	updated, _ := m.Update(ProgressCompleteMsg{TotalItems: 2, SuccessCount: 2})
	got := updated.(Model)
	if got.state != modalState || !got.progressActive {
		t.Fatalf("progress clobbered blocking modal or was lost: state=%v want=%v active=%v", got.state, modalState, got.progressActive)
	}
	return got
}

func assertPendingProgressRevealed(t *testing.T, m Model, returnState State) {
	t.Helper()
	if m.state != StateBatchProgress || !m.progressActive || m.progressReturnState != returnState {
		t.Fatalf("decision hid pending progress: state=%v active=%v return=%v want=%v", m.state, m.progressActive, m.progressReturnState, returnState)
	}
}

func TestPermissionDecisionRevealsPendingProgressWithCorrectReturn(t *testing.T) {
	for _, tc := range []struct {
		name        string
		key         tea.KeyMsg
		decision    PermissionDecision
		returnState State
	}{
		{name: "allow", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}, decision: PermissionAllow, returnState: StateProcessing},
		{name: "deny", key: tea.KeyMsg{Type: tea.KeyEscape}, decision: PermissionDeny, returnState: StateProcessing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel()
			m.state = StatePermissionPrompt
			m.permRequest = &PermissionRequestMsg{ID: "permission", ToolName: "bash"}
			var gotDecision PermissionDecision
			calls := 0
			m.SetPermissionCallback(func(_ string, decision PermissionDecision) { calls++; gotDecision = decision })
			got := addCompletedProgressBehindModal(t, m)
			_ = got.handlePermissionPromptKeys(tc.key)
			assertPendingProgressRevealed(t, got, tc.returnState)
			if calls != 1 || gotDecision != tc.decision || got.permRequest != nil {
				t.Fatalf("permission callback/lifecycle changed: calls=%d decision=%v request=%v", calls, gotDecision, got.permRequest)
			}
		})
	}
}

func TestQuestionDecisionRevealsPendingProgressWithCorrectReturn(t *testing.T) {
	for _, tc := range []struct {
		name        string
		key         tea.KeyMsg
		wantAnswer  string
		returnState State
	}{
		{name: "submit", key: tea.KeyMsg{Type: tea.KeyEnter}, wantAnswer: "Safe", returnState: StateProcessing},
		{name: "cancel", key: tea.KeyMsg{Type: tea.KeyEscape}, wantAnswer: "", returnState: StateProcessing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel()
			m.state = StateQuestionPrompt
			m.questionRequest = &QuestionRequestMsg{Question: "Choose", Options: []string{"Safe"}}
			var answer string
			calls := 0
			m.SetQuestionCallback(func(value string) { calls++; answer = value })
			got := addCompletedProgressBehindModal(t, m)
			_ = got.handleQuestionPromptKeys(tc.key)
			assertPendingProgressRevealed(t, got, tc.returnState)
			if calls != 1 || answer != tc.wantAnswer || got.questionRequest != nil {
				t.Fatalf("question callback/lifecycle changed: calls=%d answer=%q request=%v", calls, answer, got.questionRequest)
			}
		})
	}
}

func TestPlanDecisionRevealsPendingProgressWithCorrectReturn(t *testing.T) {
	for _, tc := range []struct {
		name        string
		key         tea.KeyMsg
		returnState State
		wantCalls   int
	}{
		{name: "approve", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}, returnState: StateProcessing, wantCalls: 1},
		{name: "reject", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}, returnState: StateProcessing, wantCalls: 1},
		{name: "cancel", key: tea.KeyMsg{Type: tea.KeyEscape}, returnState: StateInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel()
			m.state = StatePlanApproval
			m.planRequest = &PlanApprovalRequestMsg{Title: "Plan", Steps: []PlanStepInfo{{ID: 1, Title: "Work"}}}
			calls, cancels := 0, 0
			m.SetPlanApprovalCallback(func(PlanApprovalDecision) { calls++ })
			m.SetCancelCallback(func() { cancels++ })
			got := addCompletedProgressBehindModal(t, m)
			_ = got.handlePlanApprovalKeys(tc.key)
			assertPendingProgressRevealed(t, got, tc.returnState)
			if calls != tc.wantCalls || got.planRequest != nil || (tc.name == "cancel" && cancels != 1) {
				t.Fatalf("plan callback/lifecycle changed: calls=%d cancels=%d request=%v", calls, cancels, got.planRequest)
			}
		})
	}
}

func TestDiffDecisionsRevealPendingProgressWithCorrectReturn(t *testing.T) {
	for _, tc := range []struct {
		name        string
		decision    DiffDecision
		returnState State
	}{
		{name: "apply", decision: DiffApply, returnState: StateProcessing},
		{name: "reject", decision: DiffReject, returnState: StateProcessing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel()
			m.state = StateDiffPreview
			m.diffRequest = &DiffPreviewRequestMsg{FilePath: "main.go"}
			calls := 0
			m.SetDiffDecisionCallback(func(DiffDecision) { calls++ })
			got := addCompletedProgressBehindModal(t, m)
			updated, _ := got.Update(DiffPreviewResponseMsg{Decision: tc.decision})
			got = updated.(Model)
			assertPendingProgressRevealed(t, got, tc.returnState)
			if calls != 1 || got.diffRequest != nil {
				t.Fatalf("diff callback/lifecycle changed: calls=%d request=%v", calls, got.diffRequest)
			}
		})
	}
}

func TestMultiDiffDecisionsRevealPendingProgressWithCorrectReturn(t *testing.T) {
	for _, tc := range []struct {
		name        string
		decisions   map[string]DiffDecision
		returnState State
	}{
		{name: "apply", decisions: map[string]DiffDecision{"main.go": DiffApply}, returnState: StateProcessing},
		{name: "reject", decisions: map[string]DiffDecision{"main.go": DiffReject}, returnState: StateProcessing},
		{name: "empty", decisions: map[string]DiffDecision{}, returnState: StateInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel()
			m.state = StateMultiDiffPreview
			m.multiDiffRequest = &MultiDiffPreviewRequestMsg{Files: []DiffFile{{FilePath: "main.go"}}}
			calls := 0
			m.SetMultiDiffDecisionCallback(func(map[string]DiffDecision) { calls++ })
			got := addCompletedProgressBehindModal(t, m)
			updated, _ := got.Update(MultiDiffPreviewResponseMsg{Decisions: tc.decisions})
			got = updated.(Model)
			assertPendingProgressRevealed(t, got, tc.returnState)
			if calls != 1 || got.multiDiffRequest != nil {
				t.Fatalf("multi-diff callback/lifecycle changed: calls=%d request=%v", calls, got.multiDiffRequest)
			}
		})
	}
}

func TestPlanFeedbackSubmitRevealsPendingProgress(t *testing.T) {
	m := NewModel()
	m.state = StatePlanApproval
	m.planRequest = &PlanApprovalRequestMsg{Title: "Plan", Steps: []PlanStepInfo{{ID: 1, Title: "Work"}}}
	m.planFeedbackMode = true
	m.planFeedbackInput = NewInputModel(m.styles, m.workDir)
	m.planFeedbackInput.textarea.SetValue("Keep rollback support")
	var feedback string
	m.SetPlanApprovalWithFeedbackCallback(func(_ PlanApprovalDecision, value string) { feedback = value })
	got := addCompletedProgressBehindModal(t, m)
	_ = got.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEnter})
	assertPendingProgressRevealed(t, got, StateProcessing)
	if feedback != "Keep rollback support" || got.planRequest != nil || got.planFeedbackInput.Value() != "" {
		t.Fatalf("feedback callback/lifecycle changed: feedback=%q request=%v draft=%q", feedback, got.planRequest, got.planFeedbackInput.Value())
	}
}
