package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Decision editors reuse InputModel for multiline text, but unlike the main
// composer they have no command history to navigate. Up/Down must therefore
// move the textarea caret: swallowing those keys in the empty-history branch
// leaves users unable to revisit an earlier line of an answer or plan feedback.
func TestMultilineDecisionEditorsKeepVerticalCaretNavigation(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*Model) *InputModel
		handle func(*Model, tea.KeyMsg)
	}{
		{
			name: "free-text question",
			setup: func(m *Model) *InputModel {
				m.state = StateQuestionPrompt
				m.questionRequest = &QuestionRequestMsg{Question: "Explain the trade-off"}
				m.questionInputModel = NewInputModel(m.styles, m.workDir)
				return &m.questionInputModel
			},
			handle: func(m *Model, key tea.KeyMsg) { _ = m.handleQuestionPromptKeys(key) },
		},
		{
			name: "custom question answer",
			setup: func(m *Model) *InputModel {
				m.state = StateQuestionPrompt
				m.questionRequest = &QuestionRequestMsg{Question: "Explain the trade-off", Options: []string{"Use default"}}
				m.questionCustomInput = true
				m.questionInputModel = NewInputModel(m.styles, m.workDir)
				return &m.questionInputModel
			},
			handle: func(m *Model, key tea.KeyMsg) { _ = m.handleQuestionPromptKeys(key) },
		},
		{
			name: "plan modification feedback",
			setup: func(m *Model) *InputModel {
				m.state = StatePlanApproval
				m.planRequest = &PlanApprovalRequestMsg{Title: "Migration", Steps: []PlanStepInfo{{ID: 1, Title: "Migrate"}}}
				m.planFeedbackMode = true
				m.planFeedbackInput = NewInputModel(m.styles, m.workDir)
				return &m.planFeedbackInput
			},
			handle: func(m *Model, key tea.KeyMsg) { _ = m.handlePlanApprovalKeys(key) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel()
			input := tt.setup(m)
			input.textarea.SetValue("first line\nsecond line\nthird line")
			input.textarea.CursorEnd()
			before := input.textarea.Line()
			if before != 2 {
				t.Fatalf("fixture cursor line=%d, want final line 2", before)
			}

			tt.handle(m, tea.KeyMsg{Type: tea.KeyUp})
			if got := input.textarea.Line(); got != before-1 {
				t.Fatalf("Up was swallowed by empty composer history: cursor line=%d, want %d", got, before-1)
			}
		})
	}
}
