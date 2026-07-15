package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

func newTinyPlanAuditModel(t *testing.T, decisions *[]PlanApprovalDecision) *Model {
	t.Helper()
	m := NewModel()
	m.SetPlanApprovalCallback(func(decision PlanApprovalDecision) {
		*decisions = append(*decisions, decision)
	})
	m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 2})
	_ = m.handleMessageTypes(PlanApprovalRequestMsg{
		ID:    "plan-audit",
		Title: "Release plan",
		Steps: []PlanStepInfo{{ID: 1, Title: "Deploy"}},
	})
	if m.planPrimaryActionReadable() {
		t.Fatal("test setup unexpectedly produced readable plan actions")
	}
	return m
}

func TestUnreadablePlanPromptRejectsEveryHiddenDecisionKey(t *testing.T) {
	keys := []struct {
		name string
		key  tea.KeyMsg
	}{
		{name: "down", key: tea.KeyMsg{Type: tea.KeyDown}},
		{name: "enter", key: tea.KeyMsg{Type: tea.KeyEnter}},
		{name: "space", key: tea.KeyMsg{Type: tea.KeySpace}},
		{name: "approve-y", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}},
		{name: "approve-1", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}}},
		{name: "reject-n", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}},
		{name: "reject-2", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}}},
		{name: "modify-m", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}}},
		{name: "modify-3", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}}},
	}

	for _, tt := range keys {
		t.Run(tt.name, func(t *testing.T) {
			var decisions []PlanApprovalDecision
			m := newTinyPlanAuditModel(t, &decisions)
			selected := m.planSelectedOption

			_ = m.handlePlanApprovalKeys(tt.key)

			if len(decisions) != 0 || m.planRequest == nil || m.state != StatePlanApproval {
				t.Fatalf("hidden key resolved plan: decisions=%v request-open=%v state=%v", decisions, m.planRequest != nil, m.state)
			}
			if m.planFeedbackMode {
				t.Fatal("hidden key entered the invisible feedback editor")
			}
			if m.planSelectedOption != selected {
				t.Fatalf("hidden key changed selection: got=%d want=%d", m.planSelectedOption, selected)
			}
		})
	}

	t.Run("down-then-enter", func(t *testing.T) {
		var decisions []PlanApprovalDecision
		m := newTinyPlanAuditModel(t, &decisions)
		_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyDown})
		_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if len(decisions) != 0 || m.planRequest == nil || m.planSelectedOption != int(PlanApproved) {
			t.Fatalf("invisible Down+Enter resolved or retargeted plan: decisions=%v request-open=%v selected=%d", decisions, m.planRequest != nil, m.planSelectedOption)
		}
	})
}

func TestUnreadablePlanPromptKeepsEscRecoveryAvailable(t *testing.T) {
	var decisions []PlanApprovalDecision
	m := newTinyPlanAuditModel(t, &decisions)
	cancelled := 0
	m.SetCancelCallback(func() { cancelled++ })

	_ = m.handlePlanApprovalKeys(tea.KeyMsg{Type: tea.KeyEsc})

	if cancelled != 1 || len(decisions) != 0 || m.planRequest != nil || m.state != StateInput {
		t.Fatalf("Esc did not recover tiny plan: cancelled=%d decisions=%v request-open=%v state=%v", cancelled, decisions, m.planRequest != nil, m.state)
	}
}

func TestPendingResizeBecomesAuthoritativeBeforePlanInput(t *testing.T) {
	var decisions []PlanApprovalDecision
	m := NewModel()
	m.SetPlanApprovalCallback(func(decision PlanApprovalDecision) {
		decisions = append(decisions, decision)
	})
	m.applyResize(&tea.WindowSizeMsg{Width: 100, Height: 30})
	_ = m.handleMessageTypes(PlanApprovalRequestMsg{
		ID:    "resize-plan",
		Title: "Release plan",
		Steps: []PlanStepInfo{{ID: 1, Title: "Deploy"}},
	})

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 20, Height: 2})
	resized := updated.(Model)
	if resized.pendingResize == nil || resized.width != 100 || resized.height != 30 {
		t.Fatalf("resize was not buffered as expected: pending=%v geometry=%dx%d", resized.pendingResize != nil, resized.width, resized.height)
	}

	updated, _ = resized.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	got := updated.(Model)
	if got.pendingResize != nil || got.width != 20 || got.height != 2 {
		t.Fatalf("input did not flush announced geometry: pending=%v geometry=%dx%d", got.pendingResize != nil, got.width, got.height)
	}
	if len(decisions) != 0 || got.planRequest == nil || got.state != StatePlanApproval {
		t.Fatalf("key used stale wide geometry to reject hidden plan: decisions=%v request-open=%v state=%v", decisions, got.planRequest != nil, got.state)
	}
}

func seedTerminalSurfaceAuditModel() *Model {
	m := NewModel()
	m.width, m.height = 100, 30
	m.state = StateStreaming
	m.processingLabel = "Quality gate"
	m.currentActivity = "Reviewing files"
	m.streamIdleMsg = "Stream idle"
	m.toolProgressBar.Show("download")
	m.filePeek.ShowPeek(FilePeekMsg{FilePath: "internal/ui/tui.go", Action: "reading", Content: "package ui"})
	m.planProgressPanel.StartPlan("plan", "Audit", "", []PlanStepInfo{{ID: 1, Title: "Inspect"}})
	m.planProgressPanel.SetCurrentTool("read", "internal/ui/tui.go")
	m.output.AppendThinkingStream("old reasoning")
	return m
}

func assertTerminalSurfacesSettled(t *testing.T, m *Model) {
	t.Helper()
	if m.processingLabel != "" || m.currentActivity != "" || m.streamIdleMsg != "" {
		t.Errorf("terminal labels remained: processing=%q activity=%q idle=%q", m.processingLabel, m.currentActivity, m.streamIdleMsg)
	}
	if m.toolProgressBar.IsVisible() {
		t.Error("terminal transition left tool progress visible")
	}
	if m.filePeek.IsVisible() {
		t.Error("terminal transition left file peek visible")
	}
	if m.planProgressPanel.currentTool != "" || m.planProgressPanel.currentInfo != "" {
		t.Errorf("terminal transition left plan tool=%q info=%q", m.planProgressPanel.currentTool, m.planProgressPanel.currentInfo)
	}
	if m.output.IsThinkingActive() {
		t.Error("terminal transition left thinking block open")
	}
}

func TestWatchdogAndInterruptSettleEveryTransientTurnSurface(t *testing.T) {
	t.Run("watchdog", func(t *testing.T) {
		m := seedTerminalSurfaceAuditModel()
		m.streamTimeout = time.Second
		m.streamStartTime = time.Now().Add(-2 * m.streamTimeout)
		m.lastActivityTime = m.streamStartTime

		updated, _ := m.Update(spinner.TickMsg{})
		got := updated.(Model)
		assertTerminalSurfacesSettled(t, &got)

		updated, _ = got.Update(StreamThinkingMsg("fresh reasoning"))
		fresh := updated.(Model)
		plain := stripAnsi(fresh.output.Content())
		if strings.Count(plain, "Thinking") != 2 {
			t.Fatalf("new reasoning reused the timed-out thinking block:\n%s", plain)
		}
	})

	t.Run("interrupt", func(t *testing.T) {
		m := seedTerminalSurfaceAuditModel()
		m.state = StateProcessing

		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		got := updated.(Model)
		assertTerminalSurfacesSettled(t, &got)
	})
}

func TestReadOnlyOverlayKeepsUnderlyingEngineStatusTruthful(t *testing.T) {
	m := NewModel()
	m.width, m.height = 140, 30
	m.state = StateStreaming
	m.openNotificationCenter()

	if got := stripAnsi(m.renderEngineStatus()); !strings.Contains(got, "WRITING") {
		t.Fatalf("overlay hid underlying streaming status: %q", got)
	}
	status := stripAnsi(m.renderStatusBar())
	if !strings.Contains(status, "WRITING") || !strings.Contains(status, "esc Back") {
		t.Fatalf("status lost live state or overlay recovery: %q", status)
	}
	if strings.Contains(strings.ToLower(status), "interrupt") {
		t.Fatalf("overlay status falsely claimed Esc would interrupt: %q", status)
	}
}

func TestSubAgentAutoFixLineSanitizesRuntimeMetadata(t *testing.T) {
	m := NewModel()
	hostileTool := "read\x1b]0;owned\a\nFORGED TOOL"
	hostileReason := "retry\r\nFORGED REASON\x1b[31m red\x1b[0m"

	_ = m.handleMessageTypes(SubAgentActivityMsg{
		AgentType: "worker\nFORGED AGENT",
		ToolName:  hostileTool,
		ToolArgs:  map[string]any{"reason": hostileReason},
		Status:    "tool_recovery",
	})

	assertNoTerminalEnvelope(t, m.output.Content())
	if strings.ContainsAny(m.processingLabel, "\r\n\a") || strings.Contains(m.processingLabel, "\x1b") {
		t.Fatalf("recovery status retained terminal controls: %q", m.processingLabel)
	}
	plain := strings.TrimSpace(stripAnsi(m.output.Content()))
	if strings.Contains(plain, "\n") {
		t.Fatalf("Auto-Fixing metadata forged another output row: %q", plain)
	}
	for _, want := range []string{"read", "FORGED TOOL", "retry", "FORGED REASON", "red"} {
		if !strings.Contains(plain, want) {
			t.Errorf("sanitization lost readable %q: %q", want, plain)
		}
	}
}
