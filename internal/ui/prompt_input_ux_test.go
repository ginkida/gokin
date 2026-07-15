package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestFreeTextQuestionEscResolvesCancellationAndResumes(t *testing.T) {
	m := NewModel()
	m.state = StateQuestionPrompt
	m.questionRequest = &QuestionRequestMsg{Question: "What should change?"}
	m.questionInputModel = NewInputModel(m.styles, m.workDir)
	var answers []string
	m.onQuestion = func(answer string) { answers = append(answers, answer) }

	_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEscape})

	if m.state != StateProcessing || m.questionRequest != nil {
		t.Fatalf("Esc did not resolve free-text question back to processing: state=%v request=%v", m.state, m.questionRequest)
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

func TestQuestionCustomBackPreservesDraftWithinCurrentPrompt(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.state = StateQuestionPrompt
	m.questionRequest = &QuestionRequestMsg{Question: "Choose", Options: []string{"Default"}}
	m.questionSelectedOption = 1
	_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
	m.questionInputModel.textarea.SetValue("Keep my custom explanation")

	_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if m.questionCustomInput || m.questionInputModel.Value() != "Keep my custom explanation" {
		t.Fatalf("Back discarded custom answer: mode=%v draft=%q", m.questionCustomInput, m.questionInputModel.Value())
	}
	if got := stripAnsi(m.renderQuestionPrompt()); !strings.Contains(got, "draft saved") {
		t.Fatalf("options do not explain that the custom draft is retained:\n%s", got)
	}

	m.questionSelectedOption = 1
	_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.questionCustomInput || m.questionInputModel.Value() != "Keep my custom explanation" || !m.questionInputModel.Focused() {
		t.Fatalf("re-entering Other did not restore draft: mode=%v focused=%v draft=%q", m.questionCustomInput, m.questionInputModel.Focused(), m.questionInputModel.Value())
	}
}

func TestPlanFeedbackBackPreservesDraftWithinCurrentPrompt(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.state = StatePlanApproval
	m.planRequest = &PlanApprovalRequestMsg{Title: "Plan", Steps: []PlanStepInfo{{ID: 1, Title: "Work"}}}
	m.onPlanApprovalWithFeedback = func(PlanApprovalDecision, string) {}
	m.planSelectedOption = int(PlanModifyRequested)
	_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEnter})
	m.planFeedbackInput.textarea.SetValue("Keep rollback support")

	_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if m.planFeedbackMode || m.planFeedbackInput.Value() != "Keep rollback support" || m.planApprovalNotice != "Feedback draft saved" {
		t.Fatalf("Back discarded plan feedback: mode=%v draft=%q notice=%q", m.planFeedbackMode, m.planFeedbackInput.Value(), m.planApprovalNotice)
	}

	m.planSelectedOption = int(PlanModifyRequested)
	_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.planFeedbackMode || m.planFeedbackInput.Value() != "Keep rollback support" || !m.planFeedbackInput.Focused() {
		t.Fatalf("re-entering feedback did not restore draft: mode=%v focused=%v draft=%q", m.planFeedbackMode, m.planFeedbackInput.Focused(), m.planFeedbackInput.Value())
	}
}

func TestPromptDraftsDoNotLeakAcrossRequestsOrTerminalExit(t *testing.T) {
	question := NewModel()
	question.questionInputModel = NewInputModel(question.styles, question.workDir)
	question.questionInputModel.textarea.SetValue("old answer")
	_ = question.handleMessageTypes(QuestionRequestMsg{Question: "New", Options: []string{"One"}})
	if question.questionInputModel.Value() != "" {
		t.Fatalf("new question inherited old draft: %q", question.questionInputModel.Value())
	}

	plan := NewModel()
	plan.planFeedbackInput = NewInputModel(plan.styles, plan.workDir)
	plan.planFeedbackInput.textarea.SetValue("old feedback")
	_ = plan.handleMessageTypes(PlanApprovalRequestMsg{Title: "New plan", Steps: []PlanStepInfo{{ID: 1, Title: "One"}}})
	if plan.planFeedbackInput.Value() != "" {
		t.Fatalf("new plan inherited old feedback: %q", plan.planFeedbackInput.Value())
	}
	plan.planSelectedOption = int(PlanApproved)
	plan.onPlanApproval = func(PlanApprovalDecision) {}
	_ = plan.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if plan.planFeedbackInput.Value() != "" || plan.planFeedbackMode || plan.planApprovalNotice != "" {
		t.Fatalf("terminal plan decision retained feedback state: mode=%v draft=%q notice=%q", plan.planFeedbackMode, plan.planFeedbackInput.Value(), plan.planApprovalNotice)
	}
}
