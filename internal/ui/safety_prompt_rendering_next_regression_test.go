package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestPermissionDetailsMakeControlsVisibleAndWrapWholeGraphemes(t *testing.T) {
	lines := wrapPermissionField("x", "A👩‍💻BC", 7)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "👩‍💻") {
		t.Fatalf("permission detail split a ZWJ grapheme:\n%s", joined)
	}
	for row, line := range lines {
		if got := lipgloss.Width(line); got > 7 {
			t.Fatalf("row=%d width=%d, want <=7: %q", row, got, line)
		}
	}

	m := NewModel()
	m.width, m.height = 100, 30
	m.permShowDetails = true
	m.permRequest = &PermissionRequestMsg{
		ToolName:  "bash",
		RiskLevel: "high",
		Reason:    "inspect\x1b]52;c;Zm9yZ2Vk\a\tvalue",
		Args: map[string]any{
			"command": "printf '\x1b[2J'\x7f",
		},
	}
	view := m.renderPermissionPrompt()
	plain := stripAnsi(view)
	for _, raw := range []string{"\x1b]52", "\x1b[2J", "\a", "\x7f", "\t"} {
		if strings.Contains(view, raw) {
			t.Fatalf("permission surface executed/retained raw control %q: %q", raw, view)
		}
	}
	for _, visible := range []string{"␛]52", "␛[2J", "␇", "␉", "␡"} {
		if !strings.Contains(plain, visible) {
			t.Fatalf("permission surface hid control meaning %q:\n%s", visible, plain)
		}
	}
}

func TestThreeRowPermissionKeepsRiskToolAndDecisionContext(t *testing.T) {
	for _, width := range []int{8, 20} {
		m := NewModel()
		m.applyResize(&tea.WindowSizeMsg{Width: width, Height: 3})
		m.state = StatePermissionPrompt
		m.permRequest = &PermissionRequestMsg{ID: "req", ToolName: "bash", RiskLevel: "high"}
		m.permSelectedOption = int(PermissionAllow)

		plain := stripAnsi(m.View())
		for _, want := range []string{"bash", "ONCE", "Esc Deny"} {
			if width >= 20 && want == "ONCE" {
				want = "Allow once"
			}
			if !strings.Contains(plain, want) {
				t.Fatalf("width=%d tiny permission lost %q:\n%s", width, want, plain)
			}
		}
		if width >= 20 && !strings.Contains(plain, "HIGH RISK") {
			t.Fatalf("tiny permission lost risk classification:\n%s", plain)
		}
		if width < 20 && !strings.Contains(plain, "H bash") {
			t.Fatalf("narrow permission lost compact risk/tool context:\n%s", plain)
		}
	}

	m := NewModel()
	m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 3})
	m.state = StatePermissionPrompt
	m.permRequest = &PermissionRequestMsg{ID: "req", ToolName: "bash", RiskLevel: "high"}
	m.permNotice = "Unavailable: cannot send permission response"
	plain := stripAnsi(m.View())
	for _, want := range []string{"bash", "HIGH RISK", "Response", "Esc Cancel"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("tiny unavailable permission lost %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "Allow") {
		t.Fatalf("tiny unavailable permission advertised a disabled decision:\n%s", plain)
	}
}

func TestQuestionAndPlanActionsWaitForSemanticTargetRows(t *testing.T) {
	t.Run("question", func(t *testing.T) {
		calls := 0
		m := NewModel()
		m.SetQuestionCallback(func(string) { calls++ })
		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 3})
		_ = m.handleMessageTypes(QuestionRequestMsg{Question: "Choose target", Options: []string{"Selected answer"}})
		if m.questionPrimaryActionReadable() {
			t.Fatal("answer became actionable while its question row was cropped")
		}
		_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if calls != 0 || m.questionRequest == nil {
			t.Fatalf("hidden-question answer submitted: calls=%d request=%v", calls, m.questionRequest != nil)
		}

		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 4})
		plain := stripAnsi(m.View())
		for _, want := range []string{"Choose", "1/2", "Selec", "Enter"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("readable question boundary lost %q:\n%s", want, plain)
			}
		}
		_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if calls != 1 {
			t.Fatalf("readable answer was over-blocked: calls=%d", calls)
		}
	})

	t.Run("plan", func(t *testing.T) {
		calls := 0
		m := NewModel()
		m.SetPlanApprovalCallback(func(PlanApprovalDecision) { calls++ })
		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 7})
		_ = m.handleMessageTypes(PlanApprovalRequestMsg{
			Title: "Migration plan",
			Steps: []PlanStepInfo{{ID: 1, Title: "Prepare"}, {ID: 2, Title: "Apply"}, {ID: 3, Title: "Verify"}},
		})
		if m.planPrimaryActionReadable() {
			t.Fatal("plan became actionable before title, step and decisions fit")
		}
		_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if calls != 0 || m.planRequest == nil {
			t.Fatalf("hidden plan approved: calls=%d request=%v", calls, m.planRequest != nil)
		}

		m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 8})
		plain := stripAnsi(m.View())
		for _, want := range []string{"Migration", "1/3", "Approve", "Enter"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("readable plan boundary lost %q:\n%s", want, plain)
			}
		}
		_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if calls != 1 {
			t.Fatalf("readable plan approval was over-blocked: calls=%d", calls)
		}
	})
}

func TestKeyEntryResizeGateKeepsProviderAndSecretTargetTogether(t *testing.T) {
	calls := 0
	var submitted string
	m := NewModel()
	m.SetKeyEntrySubmitCallback(func(_ string, key string) {
		calls++
		submitted = key
	})
	m.applyResize(&tea.WindowSizeMsg{Width: 60, Height: 6})
	m.openKeyEntry(OpenKeyEntryMsg{Provider: "provider", DisplayName: "Provider"})
	m.keyEntryInput.SetValue("secret-value")
	if m.keyEntryPrimaryActionReadable() {
		t.Fatal("secret became actionable while provider identity was cropped")
	}
	view := m.View()
	if strings.Contains(stripAnsi(view), "secret-value") {
		t.Fatal("unreadable key entry leaked the secret")
	}
	_ = m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if calls != 0 || m.keyEntryInput.Value() != "secret-value" {
		t.Fatalf("resize gate submitted or discarded draft: calls=%d value=%q", calls, m.keyEntryInput.Value())
	}

	m.applyResize(&tea.WindowSizeMsg{Width: 60, Height: 8})
	if !m.keyEntryPrimaryActionReadable() {
		t.Fatal("roomy key-entry geometry stayed blocked")
	}
	plain := stripAnsi(m.View())
	for _, want := range []string{"Set Provider API key", "•", "Enter"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("readable key-entry boundary lost %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "secret-value") {
		t.Fatal("readable key entry leaked the raw secret")
	}
	_ = m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if calls != 1 || submitted != "secret-value" || m.keyEntryInput.Value() != "" {
		t.Fatalf("readable submit mismatch: calls=%d submitted=%q retained=%q", calls, submitted, m.keyEntryInput.Value())
	}

	compact := NewModel()
	compact.SetKeyEntrySubmitCallback(func(string, string) {})
	compact.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 10})
	compact.openKeyEntry(OpenKeyEntryMsg{Provider: "long-provider", DisplayName: "VeryLongProviderIdentity"})
	compact.keyEntryInput.SetValue("another-secret")
	compactView := stripAnsi(compact.View())
	if !compact.keyEntryPrimaryActionReadable() || !strings.Contains(compactView, "Very") {
		t.Fatalf("compact key-entry lost its provider target:\n%s", compactView)
	}
	if strings.Contains(compactView, "another-secret") {
		t.Fatalf("compact key-entry leaked the raw secret:\n%s", compactView)
	}
}
