package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func hasActiveToastTag(manager *ToastManager, tag string) bool {
	if manager == nil {
		return false
	}
	for _, toast := range manager.toasts {
		if toast.Tag == tag {
			return true
		}
	}
	return false
}

func TestCtrlCInterruptsActiveRequestAndPreservesDraft(t *testing.T) {
	for _, state := range []State{StateProcessing, StateStreaming} {
		t.Run(state.String(), func(t *testing.T) {
			m := NewModel()
			m.state = state
			m.streamStartTime = time.Now().Add(-2 * time.Second)
			m.currentTool = "bash"
			m.currentToolInfo = "running tests"
			m.input.textarea.SetValue("follow-up draft")
			cancelled := 0
			interrupted := 0
			quit := 0
			m.SetCancelCallback(func() { cancelled++ })
			m.SetInterruptCallback(func() { interrupted++ })
			m.SetCallbacks(nil, func() { quit++ })

			_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlC})
			if cancelled != 1 || interrupted != 1 || quit != 0 {
				t.Fatalf("callbacks cancel=%d interrupt=%d quit=%d", cancelled, interrupted, quit)
			}
			if m.state != StateInput || m.currentTool != "" || !m.streamStartTime.IsZero() {
				t.Fatalf("interrupt left transient state: state=%v tool=%q started=%v", m.state, m.currentTool, m.streamStartTime)
			}
			if got := m.input.Value(); got != "follow-up draft" {
				t.Fatalf("interrupt lost type-ahead draft: %q", got)
			}
			if !m.quitConfirmTime.IsZero() {
				t.Fatal("interrupt armed application quit")
			}
			if output := stripAnsi(m.output.state.content.String()); !strings.Contains(output, "stopped after") {
				t.Fatalf("interrupt lacks durable elapsed feedback: %q", output)
			}
		})
	}
}

func TestIdleCtrlCRequiresConsecutiveConfirmationAndPreservesDraft(t *testing.T) {
	m := NewModel()
	m.state = StateInput
	m.input.textarea.SetValue("unsent draft")
	quit := 0
	m.SetCallbacks(nil, func() { quit++ })

	first := m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlC})
	if first == nil || first() != nil {
		t.Fatal("first Ctrl+C should be consumed without quitting")
	}
	if quit != 0 || m.input.Value() != "unsent draft" || m.quitConfirmTime.IsZero() {
		t.Fatalf("first Ctrl+C quit=%d draft=%q confirm=%v", quit, m.input.Value(), m.quitConfirmTime)
	}
	if toast := stripAnsi(m.toastManager.View(80)); !strings.Contains(toast, "Draft preserved") {
		t.Fatalf("draft-safe quit confirmation missing: %q", toast)
	}

	second := m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlC})
	if quit != 1 || second == nil {
		t.Fatalf("second consecutive Ctrl+C quit=%d cmd=%v", quit, second)
	}
	if _, ok := second().(tea.QuitMsg); !ok {
		t.Fatalf("second Ctrl+C returned %T, want tea.QuitMsg", second())
	}
	if !m.quitConfirmTime.IsZero() || hasActiveToastTag(m.toastManager, "quit-confirm") {
		t.Fatal("completed quit retained confirmation state")
	}
}

func TestOtherInputCancelsPendingQuitConfirmation(t *testing.T) {
	m := NewModel()
	quit := 0
	m.SetCallbacks(nil, func() { quit++ })
	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.quitConfirmTime.IsZero() || !hasActiveToastTag(m.toastManager, "quit-confirm") {
		t.Fatal("first Ctrl+C did not arm confirmation")
	}

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if !m.quitConfirmTime.IsZero() || hasActiveToastTag(m.toastManager, "quit-confirm") {
		t.Fatal("ordinary input left stale quit confirmation or toast")
	}
	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlC})
	if quit != 0 {
		t.Fatal("non-consecutive Ctrl+C unexpectedly quit")
	}
}

func TestCtrlCInHistorySearchOnlyCancelsSearch(t *testing.T) {
	m := NewModel()
	m.state = StateInput
	m.input.historySearchMode = true
	m.input.historySearchQuery = "draft"

	if cmd := m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd != nil {
		t.Fatal("history Ctrl+C should fall through to the input model")
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	_ = cmd
	if m.input.historySearchMode || !m.quitConfirmTime.IsZero() {
		t.Fatalf("history Ctrl+C mode=%v quitConfirm=%v", m.input.historySearchMode, m.quitConfirmTime)
	}
}

func (s State) String() string {
	switch s {
	case StateProcessing:
		return "processing"
	case StateStreaming:
		return "streaming"
	default:
		return "state"
	}
}
