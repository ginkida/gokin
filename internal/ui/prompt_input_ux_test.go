package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestFreeTextQuestionEscCancels(t *testing.T) {
	m := NewModel()
	m.state = StateQuestionPrompt
	m.questionRequest = &QuestionRequestMsg{Question: "What should change?"}
	m.questionInputModel = NewInputModel(m.styles, m.workDir)
	var answers []string
	m.onQuestion = func(answer string) { answers = append(answers, answer) }

	_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEscape})

	if m.state != StateInput || m.questionRequest != nil {
		t.Fatalf("Esc did not close free-text question: state=%v request=%v", m.state, m.questionRequest)
	}
	if len(answers) != 1 || answers[0] != "" {
		t.Fatalf("cancel callback = %v, want one empty cancellation response", answers)
	}
	if got := stripAnsi(m.output.state.content.String()); !strings.Contains(got, "Question cancelled") {
		t.Fatalf("cancellation feedback missing from output: %q", got)
	}
}

func TestQuestionInputRejectsBlankAnswerAndRecovers(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "free text", true: "custom option"}[custom], func(t *testing.T) {
			m := NewModel()
			m.state = StateQuestionPrompt
			m.questionRequest = &QuestionRequestMsg{Question: "Answer?"}
			if custom {
				m.questionRequest.Options = []string{"One"}
				m.questionCustomInput = true
			}
			m.questionInputModel = NewInputModel(m.styles, m.workDir)
			calls := 0
			var answer string
			m.onQuestion = func(value string) { calls++; answer = value }

			_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
			if m.state != StateQuestionPrompt || m.questionRequest == nil || calls != 0 {
				t.Fatalf("blank answer escaped prompt: state=%v request=%v calls=%d", m.state, m.questionRequest, calls)
			}
			if got := stripAnsi(m.renderQuestionPrompt()); !strings.Contains(got, "Answer cannot be empty") {
				t.Fatalf("inline validation missing:\n%s", got)
			}

			m.questionInputModel.textarea.SetValue("Use the safer option")
			_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
			if calls != 1 || answer != "Use the safer option" || m.state != StateProcessing {
				t.Fatalf("valid recovery failed: calls=%d answer=%q state=%v", calls, answer, m.state)
			}
		})
	}
}

func TestFreeTextQuestionFooterAdvertisesCancel(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.questionRequest = &QuestionRequestMsg{Question: "Answer?"}
	m.questionInputModel = NewInputModel(m.styles, m.workDir)

	got := stripAnsi(m.renderQuestionPrompt())
	if !strings.Contains(got, "Esc Cancel") {
		t.Fatalf("free-text question hides cancellation binding:\n%s", got)
	}
}

func TestQuestionOptionFooterUsesActualQuickKeys(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.questionRequest = &QuestionRequestMsg{Question: "Choose", Options: []string{"Safe", "Fast"}}

	got := stripAnsi(m.renderQuestionPrompt())
	for _, want := range []string{"1-2 Select", "Esc Cancel"} {
		if !strings.Contains(got, want) {
			t.Fatalf("question footer missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "1-9 Select") || strings.Contains(got, "Esc Close") {
		t.Fatalf("question footer advertises stale actions:\n%s", got)
	}
}

func TestPlanFeedbackRejectsBlankAndRecovers(t *testing.T) {
	m := NewModel()
	m.state = StatePlanApproval
	m.planRequest = &PlanApprovalRequestMsg{Title: "Plan"}
	m.planFeedbackMode = true
	m.planFeedbackInput = NewInputModel(m.styles, m.workDir)
	var gotFeedback string
	calls := 0
	m.onPlanApprovalWithFeedback = func(decision PlanApprovalDecision, feedback string) {
		calls++
		gotFeedback = feedback
	}

	_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != StatePlanApproval || m.planRequest == nil || !m.planFeedbackMode || calls != 0 {
		t.Fatalf("blank feedback escaped prompt: state=%v request=%v mode=%v calls=%d", m.state, m.planRequest, m.planFeedbackMode, calls)
	}
	if got := stripAnsi(m.renderPlanApproval()); !strings.Contains(got, "Describe what should change") {
		t.Fatalf("plan feedback validation missing:\n%s", got)
	}

	m.planFeedbackInput.textarea.SetValue("Keep the public API compatible")
	_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if calls != 1 || gotFeedback != "Keep the public API compatible" {
		t.Fatalf("valid feedback not submitted: calls=%d feedback=%q", calls, gotFeedback)
	}
	if m.planFeedbackError != "" || m.planFeedbackMode || m.planRequest != nil {
		t.Fatalf("feedback state not cleared after submit: error=%q mode=%v request=%v", m.planFeedbackError, m.planFeedbackMode, m.planRequest)
	}
}
