package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestVersionedSessionModeResultRejectsOlderPartialAndFullSnapshots(t *testing.T) {
	t.Run("older partial result", func(t *testing.T) {
		m := NewModel()
		_ = m.handleMessageTypes(SessionModeCycledMsg{Mode: "yolo", Revision: 42})
		before := m.output.Content()

		_ = m.handleMessageTypes(SessionModeCycledMsg{Mode: "normal", Revision: 41})

		if m.sessionMode != "yolo" || m.permissionsEnabled || m.sandboxEnabled {
			t.Fatalf("older session result rolled back revision 42: mode=%q permissions=%v sandbox=%v",
				m.sessionMode, m.permissionsEnabled, m.sandboxEnabled)
		}
		if got := m.output.Content(); got != before {
			t.Fatal("ignored older session result appended duplicate/misleading feedback")
		}
	})

	t.Run("older full snapshot", func(t *testing.T) {
		m := NewModel()
		_ = m.handleMessageTypes(SessionModeCycledMsg{Mode: "yolo", Revision: 42})

		_ = m.handleMessageTypes(ConfigUpdateMsg{
			Revision:            41,
			PermissionsEnabled:  true,
			SandboxEnabled:      true,
			PlanningModeEnabled: false,
		})

		if m.permissionsEnabled || m.sandboxEnabled {
			t.Fatalf("older full snapshot rolled back accepted YOLO result: permissions=%v sandbox=%v",
				m.permissionsEnabled, m.sandboxEnabled)
		}
	})
}

func TestSessionModeResultDeduplicatesButStillAllowsSameRevisionFullSnapshot(t *testing.T) {
	m := NewModel()
	msg := SessionModeCycledMsg{Mode: "plan", Revision: 42}
	_ = m.handleMessageTypes(msg)
	before := m.output.Content()

	_ = m.handleMessageTypes(msg)
	if got := m.output.Content(); got != before {
		t.Fatal("duplicate session completion appended the same feedback twice")
	}

	_ = m.handleMessageTypes(ConfigUpdateMsg{
		Revision:            42,
		PermissionsEnabled:  true,
		SandboxEnabled:      true,
		PlanningModeEnabled: true,
		CompactMode:         true,
		ModelName:           "authoritative-model",
	})
	if !m.CompactMode || m.currentModel != "authoritative-model" || m.lastConfigRevision != 42 {
		t.Fatalf("partial result blocked same-revision full snapshot: compact=%v model=%q revision=%d",
			m.CompactMode, m.currentModel, m.lastConfigRevision)
	}
}

func TestShiftESetsFutureDefaultWithoutRetargetingExistingCard(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.state = StateInput
	m.handleToolResultWithInfo(
		strings.Repeat("ordinary output line\n", 30),
		"bash",
		"go test ./...",
		time.Now(),
	)
	index := m.lastToolOutputIndex
	if index < 0 || m.toolOutput.IsExpanded(index) {
		t.Fatalf("setup: expected a collapsed tool card, index=%d expanded=%v", index, m.toolOutput.IsExpanded(index))
	}

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'E'}})
	if !m.toolOutput.AllExpanded {
		t.Fatal("Shift+E did not set the future expanded default")
	}
	if m.toolOutput.IsExpanded(index) {
		t.Fatal("Shift+E silently changed an already-rendered card that scrollback cannot rerender")
	}

	before := m.output.Content()
	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlE})
	if !m.toolOutput.IsExpanded(index) || len(m.output.Content()) <= len(before) {
		t.Fatal("the promised next Ctrl+E did not append the existing card's full output")
	}
}

func TestClearScreenAlsoClearsHiddenExpansionStore(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.state = StateInput
	m.handleToolResultWithInfo(
		strings.Repeat("sensitive old output\n", 30),
		"bash",
		"print secret",
		time.Now(),
	)
	if m.toolOutput.EntryCount() == 0 || m.lastToolOutputIndex < 0 {
		t.Fatal("setup: expected expandable output")
	}

	m.clearOutputWithFeedback()

	if !m.output.IsEmpty() || m.toolOutput.EntryCount() != 0 || m.lastToolOutputIndex != -1 {
		t.Fatalf("clear left hidden expansion state: output-empty=%v entries=%d last=%d",
			m.output.IsEmpty(), m.toolOutput.EntryCount(), m.lastToolOutputIndex)
	}
	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlE})
	if strings.Contains(m.output.Content(), "sensitive old output") {
		t.Fatal("Ctrl+E resurrected output after Clear Screen")
	}
}

func TestRecoveryHintDoesNotAdvertiseOverlayOwnedHistoryKey(t *testing.T) {
	m := NewModel()
	m.width, m.height = 100, 30
	m.state = StateStreaming
	m.input.AddToHistory("previous request")
	m.openNotificationCenter()

	updated, _ := m.Update(ErrorMsg(errors.New("provider failed")))
	got := updated.(Model)
	plain := stripAnsi(got.output.Content())
	if got.state != StateNotificationCenter {
		t.Fatalf("error unexpectedly closed notification overlay: %v", got.state)
	}
	if strings.Contains(plain, "Press ↑") {
		t.Fatalf("recovery advertised a key owned by the visible overlay:\n%s", plain)
	}
	if !strings.Contains(plain, "Close or resolve the current panel") {
		t.Fatalf("recovery omitted the real route back to the composer:\n%s", plain)
	}
}

func TestQuitConfirmationIsCancelledByModalAndMouseInteraction(t *testing.T) {
	t.Run("modal key", func(t *testing.T) {
		m := *NewModel()
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		armed := updated.(Model)
		if armed.quitConfirmTime.IsZero() {
			t.Fatal("setup: quit confirmation was not armed")
		}

		armed.shortcutsOverlay.Show()
		armed.transientOverlayReturnState = StateInput
		armed.state = StateShortcutsOverlay
		updated, _ = armed.Update(tea.KeyMsg{Type: tea.KeyEsc})
		got := updated.(Model)
		if !got.quitConfirmTime.IsZero() || hasActiveToastTag(got.toastManager, "quit-confirm") {
			t.Fatal("modal interaction left a stale one-key quit confirmation")
		}
	})

	t.Run("mouse", func(t *testing.T) {
		m := *NewModel()
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		armed := updated.(Model)
		updated, _ = armed.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
		got := updated.(Model)
		if !got.quitConfirmTime.IsZero() || hasActiveToastTag(got.toastManager, "quit-confirm") {
			t.Fatal("mouse interaction left a stale one-key quit confirmation")
		}
	})
}

func TestTinyQuitConfirmationIsVisibleAndFailClosed(t *testing.T) {
	m := *NewModel()
	m.applyResize(&tea.WindowSizeMsg{Width: 6, Height: 1})
	quitCalls := 0
	m.SetCallbacks(nil, func() { quitCalls++ })

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	armed := updated.(Model)
	if status := stripAnsi(armed.renderStatusBar()); status != "Resize" {
		t.Fatalf("tiny quit confirmation status=%q, want Resize", status)
	}

	updated, cmd := armed.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	blocked := updated.(Model)
	if quitCalls != 0 || cmd == nil || blocked.quitConfirmTime.IsZero() {
		t.Fatalf("hidden second Ctrl+C was not fail-closed: quits=%d cmd=%v armed=%v", quitCalls, cmd, !blocked.quitConfirmTime.IsZero())
	}

	blocked.applyResize(&tea.WindowSizeMsg{Width: 11, Height: 1})
	if status := stripAnsi(blocked.renderStatusBar()); status != "Ctrl+C Quit" {
		t.Fatalf("readable quit confirmation status=%q", status)
	}
	updated, cmd = blocked.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if quitCalls != 1 || cmd == nil {
		t.Fatalf("readable confirmation did not quit: quits=%d cmd=%v", quitCalls, cmd)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("readable confirmation returned %T, want tea.QuitMsg", cmd())
	}
}

func TestFirstLaunchIsNotPersistedFromACompletelyClippedFrame(t *testing.T) {
	m := *NewModel()
	seen := 0
	m.ShowFirstLaunchWelcome(func() { seen++ })
	m.applyResize(&tea.WindowSizeMsg{Width: 1, Height: 1})
	if strings.Contains(stripAnsi(m.View()), "Gokin") {
		t.Fatal("setup: 1x1 frame unexpectedly exposed the welcome")
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	clipped := updated.(Model)
	if seen != 0 || !clipped.firstLaunchWelcomePending {
		t.Fatalf("clipped frame persisted welcome dismissal: seen=%d pending=%v", seen, clipped.firstLaunchWelcomePending)
	}
	if got := clipped.input.Value(); got != "x" {
		t.Fatalf("visibility gate swallowed ordinary input: %q", got)
	}

	clipped.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 8})
	if !strings.Contains(stripAnsi(clipped.View()), "Gokin") {
		t.Fatal("welcome did not appear after resizing to readable geometry")
	}
	updated, _ = clipped.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	readable := updated.(Model)
	if seen != 1 || readable.firstLaunchWelcomePending || readable.input.Value() != "xy" {
		t.Fatalf("readable dismissal failed: seen=%d pending=%v input=%q", seen, readable.firstLaunchWelcomePending, readable.input.Value())
	}
}
