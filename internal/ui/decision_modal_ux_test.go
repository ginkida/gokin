package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestQuestionRequestSelectsDeclaredDefault(t *testing.T) {
	m := NewModel()
	updated, _ := m.Update(QuestionRequestMsg{
		Question: "Choose a strategy",
		Options:  []string{"Fast", " Safe\n mode ", "Thorough"},
		Default:  "Safe mode",
	})
	got := updated.(Model)
	if got.questionSelectedOption != 1 {
		t.Fatalf("selected=%d want declared default index 1", got.questionSelectedOption)
	}
	view := stripAnsi(got.renderQuestionPrompt())
	if !strings.Contains(view, "> 2. Safe mode (default)") {
		t.Fatalf("default is not visibly selected:\n%s", view)
	}
}

func TestQuestionPromptKeepsSelectedAnswerAndFooterVisible(t *testing.T) {
	m := NewModel()
	m.applyResize(&tea.WindowSizeMsg{Width: 72, Height: 14})
	m.state = StateQuestionPrompt
	m.questionSelectedOption = 11
	m.questionRequest = &QuestionRequestMsg{Question: strings.Repeat("Which deployment strategy should be used? ", 8)}
	for i := 0; i < 14; i++ {
		m.questionRequest.Options = append(m.questionRequest.Options, fmt.Sprintf("Option %02d", i+1))
	}

	view := stripAnsi(m.View())
	for _, want := range []string{"> 12. Option 12", "↑", "↓", "PgUp/PgDn Page", "Enter Confirm", "Esc Cancel"} {
		if !strings.Contains(view, want) {
			t.Fatalf("question frame clipped %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "1. Option 01") {
		t.Fatalf("off-window answer consumed compact modal height:\n%s", view)
	}
	if lipgloss.Height(view) != 14 {
		t.Fatalf("frame height=%d want 14", lipgloss.Height(view))
	}
}

func TestQuestionPromptMiddlePageFitsTerminalHeightWithoutCropping(t *testing.T) {
	for _, height := range []int{12, 14, 18, 22} {
		m := NewModel()
		m.width = 72
		m.height = height
		m.questionSelectedOption = 7
		m.questionRequest = &QuestionRequestMsg{Question: strings.Repeat("Which deployment strategy should be used? ", 8)}
		for i := range 14 {
			m.questionRequest.Options = append(m.questionRequest.Options, fmt.Sprintf("Option %02d", i+1))
		}

		view := m.renderQuestionPrompt()
		if got := lipgloss.Height(view); got > height {
			t.Fatalf("height=%d question modal rendered %d rows:\n%s", height, got, stripAnsi(view))
		}
		plain := stripAnsi(view)
		for _, want := range []string{"> 8. Option 08", "Esc Cancel", "Enter Confirm", "PgUp/PgDn Page"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("height=%d question modal missing %q:\n%s", height, want, plain)
			}
		}
	}
}

func TestQuestionNavigationSupportsPagingAndBounds(t *testing.T) {
	m := NewModel()
	m.height = 13
	m.state = StateQuestionPrompt
	m.questionRequest = &QuestionRequestMsg{Question: "Choose"}
	for i := 0; i < 20; i++ {
		m.questionRequest.Options = append(m.questionRequest.Options, fmt.Sprintf("Option %d", i+1))
	}

	_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.questionSelectedOption != 6 {
		t.Fatalf("page down selected=%d", m.questionSelectedOption)
	}
	_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEnd})
	if m.questionSelectedOption != 20 {
		t.Fatalf("End must select custom answer, got %d", m.questionSelectedOption)
	}
	_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.questionSelectedOption != 20 {
		t.Fatalf("selection escaped bounds: %d", m.questionSelectedOption)
	}
	assertShortcutHints(t, m,
		[]string{"↑↓ Navigate", "PgUp/PgDn Page", "Enter Confirm", "esc Cancel"},
		nil,
	)
}

func TestQuestionSingleAnswerKeepsCustomAnswerNavigationDiscoverable(t *testing.T) {
	m := NewModel()
	m.width, m.height = 90, 24
	m.state = StateQuestionPrompt
	m.questionRequest = &QuestionRequestMsg{Question: "Use the recommended strategy?", Options: []string{"Yes"}}

	view := stripAnsi(m.renderQuestionPrompt())
	if !strings.Contains(view, "↑/↓ Navigate") || !strings.Contains(view, "Other (custom answer)") {
		t.Fatalf("single answer hid navigation to custom input:\n%s", view)
	}
	assertShortcutHints(t, m,
		[]string{"↑↓ Navigate", "Enter Confirm", "esc Cancel"},
		[]string{"PgUp/PgDn Page"},
	)

	_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyDown})
	_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.questionCustomInput {
		t.Fatal("advertised navigation did not reach the custom-answer editor")
	}
}

func TestQuestionCustomAnswerIsTrimmed(t *testing.T) {
	m := NewModel()
	m.state = StateQuestionPrompt
	m.questionRequest = &QuestionRequestMsg{Question: "Why?", Options: []string{"Known"}}
	m.questionCustomInput = true
	m.questionInputModel = NewInputModel(m.styles, m.workDir)
	m.questionInputModel.textarea.SetValue("  explain this choice  \n")
	var answer string
	m.onQuestion = func(value string) { answer = value }

	_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if answer != "explain this choice" {
		t.Fatalf("custom answer retained accidental edge whitespace: %q", answer)
	}
}

func TestPlanApprovalPagesStepsWithoutMovingDecision(t *testing.T) {
	m := NewModel()
	m.width = 72
	m.height = 14
	m.state = StatePlanApproval
	m.planSelectedOption = int(PlanRejected)
	m.planRequest = &PlanApprovalRequestMsg{Title: "Large plan"}
	for i := 0; i < 12; i++ {
		m.planRequest.Steps = append(m.planRequest.Steps, PlanStepInfo{ID: i + 1, Title: fmt.Sprintf("Step title %02d", i+1)})
	}

	first := stripAnsi(m.renderPlanApproval())
	if !strings.Contains(first, "Step 1: Step title 01") || !strings.Contains(first, "↓") || !strings.Contains(first, "more step(s)") {
		t.Fatalf("initial plan page is not honest:\n%s", first)
	}
	assertShortcutHints(t, m,
		[]string{"PgUp/PgDn Steps", "↑↓ Navigate"},
		nil,
	)
	_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.planStepScroll != 3 || m.planSelectedOption != int(PlanRejected) {
		t.Fatalf("step paging changed decision or wrong scroll: scroll=%d decision=%d", m.planStepScroll, m.planSelectedOption)
	}
	second := stripAnsi(m.renderPlanApproval())
	if !strings.Contains(second, "Step 4: Step title 04") || !strings.Contains(second, "↑ 3 earlier step(s)") {
		t.Fatalf("next plan page missing navigation context:\n%s", second)
	}
}

func TestPlanApprovalMiddlePageFitsTerminalHeightWithoutCropping(t *testing.T) {
	for _, height := range []int{12, 14, 18, 22} {
		m := NewModel()
		m.width = 72
		m.height = height
		m.planSelectedOption = int(PlanRejected)
		m.planStepScroll = 5
		m.planRequest = &PlanApprovalRequestMsg{
			Title:        "Ship the reliability improvements",
			Description:  strings.Repeat("Preserve user work while updating every affected flow. ", 6),
			ContractName: "Safe delivery",
			Intent:       "No silent data loss",
			Boundaries:   []string{"no destructive fallback"},
			Invariants:   []string{"footer remains reachable"},
			Examples:     []string{"rollback"},
		}
		for i := range 14 {
			m.planRequest.Steps = append(m.planRequest.Steps, PlanStepInfo{ID: i + 1, Title: fmt.Sprintf("Step title %02d", i+1), Description: "Detailed verification work"})
		}

		view := m.renderPlanApproval()
		if got := lipgloss.Height(view); got > height {
			t.Fatalf("height=%d plan modal rendered %d rows:\n%s", height, got, stripAnsi(view))
		}
		plain := stripAnsi(view)
		for _, want := range []string{"Step 6: Step title 06", "Esc Cancel", "Enter Confirm", "PgUp/PgDn Steps"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("height=%d plan modal missing %q:\n%s", height, want, plain)
			}
		}
	}
}

func TestPlanApprovalResizeReanchorsObsoleteMiddlePage(t *testing.T) {
	m := NewModel()
	m.width = 72
	m.height = 12
	m.planStepScroll = 5
	m.planRequest = &PlanApprovalRequestMsg{Title: "Resizable plan"}
	for i := range 14 {
		m.planRequest.Steps = append(m.planRequest.Steps, PlanStepInfo{ID: i + 1, Title: fmt.Sprintf("Step title %02d", i+1)})
	}
	if compact := stripAnsi(m.renderPlanApproval()); !strings.Contains(compact, "Step 6: Step title 06") {
		t.Fatalf("compact setup did not render the requested middle page:\n%s", compact)
	}

	m.height = 40
	view := stripAnsi(m.renderPlanApproval())
	if !strings.Contains(view, "Step 1: Step title 01") || !strings.Contains(view, "Step 14: Step title 14") {
		t.Fatalf("expanded modal retained an obsolete middle-page anchor:\n%s", view)
	}
}

func TestPlanApprovalEmptyAndContractStatesAreExplicit(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.height = 24
	m.planRequest = &PlanApprovalRequestMsg{
		Title:        " ",
		ContractName: " Safety contract ",
		Intent:       " Preserve user data ",
		Boundaries:   []string{"no deletes"},
		Invariants:   []string{"atomic"},
		Examples:     []string{"rollback"},
	}
	got := stripAnsi(m.renderPlanApproval())
	for _, want := range []string{"Untitled plan", "No executable steps supplied", "Contract: Safety contract · Preserve user data", "2 guardrail(s) · 1 example(s)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("plan approval missing %q:\n%s", want, got)
		}
	}
}

func TestEmptyPlanCannotBeApprovedAndDefaultsToSafeDecision(t *testing.T) {
	m := NewModel()
	m.width, m.height = 80, 24
	var decisions []PlanApprovalDecision
	m.SetPlanApprovalCallback(func(decision PlanApprovalDecision) { decisions = append(decisions, decision) })

	updated, _ := m.Update(PlanApprovalRequestMsg{Title: "Empty plan"})
	got := updated.(Model)
	if got.state != StatePlanApproval || got.planSelectedOption != int(PlanRejected) {
		t.Fatalf("empty plan default state=%v selection=%d", got.state, got.planSelectedOption)
	}
	view := stripAnsi(got.renderPlanApproval())
	for _, want := range []string{"No executable steps supplied · request changes or reject", "1. Approve (unavailable · no steps)", "> 2. Reject", "2-3 Select"} {
		if !strings.Contains(view, want) {
			t.Fatalf("empty plan missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "1-3 Select") {
		t.Fatalf("empty plan advertises unavailable approval shortcut:\n%s", view)
	}

	// Up cannot move onto the disabled approval row; y/1 explain the block and
	// never resolve the backend approval channel as approved.
	_ = got.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyUp})
	if got.planSelectedOption != int(PlanRejected) {
		t.Fatalf("navigation selected disabled approval: %d", got.planSelectedOption)
	}
	_ = got.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if len(decisions) != 0 || got.state != StatePlanApproval || got.planRequest == nil {
		t.Fatalf("quick approve escaped empty-plan guard: decisions=%v state=%v request=%v", decisions, got.state, got.planRequest)
	}
	if blocked := stripAnsi(got.renderPlanApproval()); !strings.Contains(blocked, "Cannot approve an empty plan") {
		t.Fatalf("blocked approval lacks recovery guidance:\n%s", blocked)
	}

	_ = got.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	if !got.planFeedbackMode || got.state != StatePlanApproval || len(decisions) != 0 {
		t.Fatalf("request-changes recovery unavailable: feedback=%v state=%v decisions=%v", got.planFeedbackMode, got.state, decisions)
	}
}

func TestPlanFeedbackSubmitReturnsToProcessing(t *testing.T) {
	m := NewModel()
	m.state = StatePlanApproval
	m.planRequest = &PlanApprovalRequestMsg{Title: "Plan"}
	m.planFeedbackMode = true
	m.planFeedbackInput = NewInputModel(m.styles, m.workDir)
	m.planFeedbackInput.textarea.SetValue("  Keep rollback support  ")
	var feedback string
	m.onPlanApprovalWithFeedback = func(_ PlanApprovalDecision, value string) { feedback = value }

	_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != StateProcessing || feedback != "Keep rollback support" {
		t.Fatalf("feedback submit state=%v feedback=%q", m.state, feedback)
	}
}

func TestDecisionPromptsNeverOverflowNarrowWidths(t *testing.T) {
	for width := 10; width <= 48; width++ {
		question := NewModel()
		question.width = width
		question.height = 12
		question.questionRequest = &QuestionRequestMsg{Question: strings.Repeat("question 界 ", 20), Options: []string{strings.Repeat("answer ", 20)}}

		plan := NewModel()
		plan.width = width
		plan.height = 12
		plan.planRequest = &PlanApprovalRequestMsg{Title: strings.Repeat("plan 界 ", 20), Steps: []PlanStepInfo{{Title: strings.Repeat("step ", 20)}}}

		for name, rendered := range map[string]string{"question": question.renderQuestionPrompt(), "plan": plan.renderPlanApproval()} {
			for row, line := range strings.Split(rendered, "\n") {
				if got := lipgloss.Width(line); got > width {
					t.Fatalf("%s width=%d row=%d overflow=%d: %q", name, width, row, got, stripAnsi(line))
				}
			}
		}
	}
}
