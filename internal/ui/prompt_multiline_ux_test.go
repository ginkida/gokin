package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestQuestionPromptAltEnterInsertsNewlineWithoutSubmitting(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "free_text", true: "custom_answer"}[custom], func(t *testing.T) {
			m := NewModel()
			m.state = StateQuestionPrompt
			m.questionRequest = &QuestionRequestMsg{Question: "Explain the choice"}
			if custom {
				m.questionRequest.Options = []string{"Default"}
				m.questionCustomInput = true
			}
			m.questionInputModel = NewInputModel(m.styles, m.workDir)
			m.questionInputModel.textarea.SetValue("first paragraph")
			m.questionInputError = "stale validation"
			calls := 0
			m.onQuestion = func(string) { calls++ }

			_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})

			if calls != 0 || m.state != StateQuestionPrompt || m.questionRequest == nil {
				t.Fatalf("Alt+Enter submitted or closed prompt: calls=%d state=%v request=%v", calls, m.state, m.questionRequest)
			}
			if got := m.questionInputModel.textarea.Value(); got != "first paragraph\n" {
				t.Fatalf("Alt+Enter value=%q, want trailing newline", got)
			}
			if m.questionInputError != "" {
				t.Fatalf("editing retained stale validation: %q", m.questionInputError)
			}
		})
	}
}

func TestPromptAltEnterOnBlankDoesNotTriggerRequiredValidation(t *testing.T) {
	question := NewModel()
	question.state = StateQuestionPrompt
	question.questionRequest = &QuestionRequestMsg{Question: "Explain"}
	question.questionInputModel = NewInputModel(question.styles, question.workDir)
	_ = question.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if question.questionInputError != "" || question.questionInputModel.textarea.Value() != "\n" {
		t.Fatalf("blank question newline was validated: error=%q value=%q", question.questionInputError, question.questionInputModel.textarea.Value())
	}

	plan := NewModel()
	plan.state = StatePlanApproval
	plan.planRequest = &PlanApprovalRequestMsg{Title: "Plan"}
	plan.planFeedbackMode = true
	plan.planFeedbackInput = NewInputModel(plan.styles, plan.workDir)
	_ = plan.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if plan.planFeedbackError != "" || plan.planFeedbackInput.textarea.Value() != "\n" {
		t.Fatalf("blank plan newline was validated: error=%q value=%q", plan.planFeedbackError, plan.planFeedbackInput.textarea.Value())
	}
}

func TestPlanFeedbackAltEnterSupportsParagraphs(t *testing.T) {
	m := NewModel()
	m.state = StatePlanApproval
	m.planRequest = &PlanApprovalRequestMsg{Title: "Migration plan"}
	m.planFeedbackMode = true
	m.planFeedbackInput = NewInputModel(m.styles, m.workDir)
	m.planFeedbackInput.textarea.SetValue("Keep compatibility")
	m.planFeedbackError = "stale validation"
	calls := 0
	var feedback string
	m.onPlanApprovalWithFeedback = func(_ PlanApprovalDecision, value string) {
		calls++
		feedback = value
	}

	_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if calls != 0 || !m.planFeedbackMode || m.planRequest == nil {
		t.Fatalf("Alt+Enter submitted or closed feedback: calls=%d mode=%v request=%v", calls, m.planFeedbackMode, m.planRequest)
	}
	if m.planFeedbackError != "" || m.planFeedbackInput.textarea.Value() != "Keep compatibility\n" {
		t.Fatalf("newline/edit recovery failed: error=%q value=%q", m.planFeedbackError, m.planFeedbackInput.textarea.Value())
	}

	m.planFeedbackInput.textarea.InsertString("Preserve rollback")
	_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if calls != 1 || feedback != "Keep compatibility\nPreserve rollback" {
		t.Fatalf("multiline feedback submit: calls=%d feedback=%q", calls, feedback)
	}
}

func TestPromptMultilineActionIsDiscoverable(t *testing.T) {
	question := NewModel()
	question.width = 80
	question.state = StateQuestionPrompt
	question.questionRequest = &QuestionRequestMsg{Question: "Explain"}
	question.questionInputModel = NewInputModel(question.styles, question.workDir)
	if got := stripAnsi(question.renderQuestionPrompt()); !strings.Contains(got, "Alt+Enter New line") {
		t.Fatalf("question footer hides multiline action:\n%s", got)
	}
	if got := plainShortcutHints(question.contextualShortcutHintPairs()); !strings.Contains(got, "Alt+Enter New line") {
		t.Fatalf("question status hints hide multiline action: %q", got)
	}

	plan := NewModel()
	plan.width = 80
	plan.state = StatePlanApproval
	plan.planRequest = &PlanApprovalRequestMsg{Title: "Plan"}
	plan.planFeedbackMode = true
	plan.planFeedbackInput = NewInputModel(plan.styles, plan.workDir)
	if got := stripAnsi(plan.renderPlanApproval()); !strings.Contains(got, "Alt+Enter New line") {
		t.Fatalf("plan footer hides multiline action:\n%s", got)
	}
	if got := plainShortcutHints(plan.contextualShortcutHintPairs()); !strings.Contains(got, "Alt+Enter New line") {
		t.Fatalf("plan status hints hide multiline action: %q", got)
	}
}
