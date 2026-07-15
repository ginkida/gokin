package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestUnavailablePermissionResponseNeverFakesProcessing(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.state = StatePermissionPrompt
	m.permRequest = &PermissionRequestMsg{ID: "req-1", ToolName: "bash", RiskLevel: "high"}
	cancelled := 0
	m.SetCancelCallback(func() { cancelled++ })

	_ = m.handlePermissionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != StatePermissionPrompt || m.permRequest == nil {
		t.Fatalf("unavailable allow closed prompt or faked processing: state=%v request=%v", m.state, m.permRequest)
	}
	if view := stripAnsi(m.renderPermissionPrompt()); !strings.Contains(view, "Unavailable: cannot send permission response") || !strings.Contains(view, "Esc cancels") || strings.Contains(view, "Enter Confirm") {
		t.Fatalf("permission prompt lacks durable unavailable feedback:\n%s", view)
	}

	_ = m.handlePermissionPromptKeys(tea.KeyMsg{Type: tea.KeyEsc})
	if m.state != StateInput || m.permRequest != nil || cancelled != 1 {
		t.Fatalf("Esc did not recover unavailable permission prompt: state=%v request=%v cancelled=%d", m.state, m.permRequest, cancelled)
	}
	if output := stripAnsi(m.output.Content()); !strings.Contains(output, "response handler unavailable") {
		t.Fatalf("permission cancellation is not durable in output: %q", output)
	}
}

func TestUnavailableQuestionResponsePreservesAnswerUntilCancel(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.state = StateQuestionPrompt
	m.questionRequest = &QuestionRequestMsg{Question: "Choose", Options: []string{"Safe", "Fast"}}
	cancelled := 0
	m.SetCancelCallback(func() { cancelled++ })

	_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != StateQuestionPrompt || m.questionRequest == nil || m.questionSelectedOption != 0 {
		t.Fatalf("unavailable answer closed or mutated prompt: state=%v request=%v selected=%d", m.state, m.questionRequest, m.questionSelectedOption)
	}
	if view := stripAnsi(m.renderQuestionPrompt()); !strings.Contains(view, "Unavailable: cannot submit answer") || !strings.Contains(view, "Esc cancels") || strings.Contains(view, "Enter Confirm") {
		t.Fatalf("question prompt lacks durable unavailable feedback:\n%s", view)
	}
	assertShortcutHints(t, m,
		[]string{"esc Cancel", "↑↓ Navigate"},
		[]string{"Enter Confirm", "PgUp/PgDn Page"},
	)

	_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEsc})
	if m.state != StateInput || m.questionRequest != nil || cancelled != 1 {
		t.Fatalf("Esc did not recover unavailable question: state=%v request=%v cancelled=%d", m.state, m.questionRequest, cancelled)
	}
}

func TestUnavailableFreeTextQuestionKeepsDraft(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.state = StateQuestionPrompt
	m.questionRequest = &QuestionRequestMsg{Question: "Explain"}
	m.questionInputModel = NewInputModel(m.styles, m.workDir)
	m.questionInputModel.textarea.SetValue("keep this answer")

	_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != StateQuestionPrompt || m.questionRequest == nil || m.questionInputModel.Value() != "keep this answer" {
		t.Fatalf("unavailable submission discarded free-text draft: state=%v request=%v draft=%q", m.state, m.questionRequest, m.questionInputModel.Value())
	}
	if view := stripAnsi(m.renderQuestionPrompt()); !strings.Contains(view, "Unavailable: cannot submit answer") || strings.Contains(view, "Enter Submit") {
		t.Fatalf("free-text prompt lacks unavailable feedback:\n%s", view)
	}
}

func TestUnavailablePlanResponseKeepsDecisionModalUntilCancel(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.state = StatePlanApproval
	m.planRequest = &PlanApprovalRequestMsg{Title: "Deploy", Steps: []PlanStepInfo{{ID: 1, Title: "Release"}}}
	cancelled := 0
	m.SetCancelCallback(func() { cancelled++ })

	_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != StatePlanApproval || m.planRequest == nil {
		t.Fatalf("unavailable approval closed prompt or faked processing: state=%v request=%v", m.state, m.planRequest)
	}
	if view := stripAnsi(m.renderPlanApproval()); !strings.Contains(view, "Unavailable: cannot send plan response") || !strings.Contains(view, "Esc cancels") || strings.Contains(view, "Enter Confirm") {
		t.Fatalf("plan prompt lacks durable unavailable feedback:\n%s", view)
	}

	_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEsc})
	if m.state != StateInput || m.planRequest != nil || cancelled != 1 {
		t.Fatalf("Esc did not recover unavailable plan prompt: state=%v request=%v cancelled=%d", m.state, m.planRequest, cancelled)
	}
}

func TestUnavailableOverflowDecisionStillAdvertisesInspectionPaging(t *testing.T) {
	question := NewModel()
	question.width, question.height = 72, 12
	question.state = StateQuestionPrompt
	question.questionRequest = &QuestionRequestMsg{Question: "Choose"}
	for i := 0; i < 12; i++ {
		question.questionRequest.Options = append(question.questionRequest.Options, fmt.Sprintf("Option %d", i+1))
	}
	_ = question.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
	assertShortcutHints(t, question,
		[]string{"esc Cancel", "↑↓ Navigate", "PgUp/PgDn Page"},
		[]string{"Enter Confirm"},
	)
	if view := stripAnsi(question.renderQuestionPrompt()); !strings.Contains(view, "PgUp/PgDn Page") {
		t.Fatalf("unavailable overflowing question hid inspection paging:\n%s", view)
	}

	plan := NewModel()
	plan.width, plan.height = 72, 12
	plan.state = StatePlanApproval
	plan.planRequest = &PlanApprovalRequestMsg{Title: "Deploy"}
	for i := 0; i < 12; i++ {
		plan.planRequest.Steps = append(plan.planRequest.Steps, PlanStepInfo{ID: i + 1, Title: fmt.Sprintf("Step %d", i+1)})
	}
	_ = plan.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEnter})
	assertShortcutHints(t, plan,
		[]string{"esc Cancel", "PgUp/PgDn Steps"},
		[]string{"y Approve", "Enter Confirm"},
	)
	if view := stripAnsi(plan.renderPlanApproval()); !strings.Contains(view, "PgUp/PgDn Steps") {
		t.Fatalf("unavailable overflowing plan hid inspection paging:\n%s", view)
	}
}

func TestOptionOnlyDecisionsDoNotResetUninitializedEditors(t *testing.T) {
	t.Run("question", func(t *testing.T) {
		m := NewModel()
		m.state = StateQuestionPrompt
		m.questionRequest = &QuestionRequestMsg{Question: "Choose", Options: []string{"Safe"}}
		m.SetQuestionCallback(func(string) {})

		_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if m.state != StateProcessing || m.questionRequest != nil {
			t.Fatalf("question decision did not complete: state=%v request=%v", m.state, m.questionRequest)
		}
	})

	t.Run("plan", func(t *testing.T) {
		m := NewModel()
		m.state = StatePlanApproval
		m.planRequest = &PlanApprovalRequestMsg{Title: "Deploy", Steps: []PlanStepInfo{{ID: 1, Title: "Release"}}}
		m.SetPlanApprovalCallback(func(PlanApprovalDecision) {})

		_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
		if m.state != StateProcessing || m.planRequest != nil {
			t.Fatalf("plan decision did not complete: state=%v request=%v", m.state, m.planRequest)
		}
	})
}

func TestUnavailableDecisionFeedbackAndRecoverySurviveConstrainedFrames(t *testing.T) {
	for _, size := range []struct{ width, height int }{{24, 8}, {32, 10}, {40, 12}} {
		for _, prompt := range []string{"permission", "question", "plan"} {
			t.Run(fmt.Sprintf("%s-%dx%d", prompt, size.width, size.height), func(t *testing.T) {
				m := NewModel()
				m.applyResize(&tea.WindowSizeMsg{Width: size.width, Height: size.height})
				switch prompt {
				case "permission":
					m.state = StatePermissionPrompt
					m.permRequest = &PermissionRequestMsg{ToolName: "bash", RiskLevel: "high"}
					_ = m.handlePermissionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
				case "question":
					m.state = StateQuestionPrompt
					m.questionRequest = &QuestionRequestMsg{Question: "Choose", Options: []string{"Safe"}}
					_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
				case "plan":
					m.state = StatePlanApproval
					m.planRequest = &PlanApprovalRequestMsg{Title: "Deploy", Steps: []PlanStepInfo{{ID: 1, Title: "Release"}}}
					_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEnter})
				}

				view := stripAnsi(m.View())
				for _, want := range []string{"Unavailable", "Esc"} {
					if !strings.Contains(view, want) {
						t.Fatalf("constrained %s frame lost %q:\n%s", prompt, want, view)
					}
				}
				if got := strings.Count(view, "\n") + 1; got != size.height {
					t.Fatalf("frame height=%d, want %d:\n%s", got, size.height, view)
				}
			})
		}
	}
}
