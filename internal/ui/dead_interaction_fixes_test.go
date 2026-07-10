package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// --- Round: Ctrl+O-class dead/blocked interaction fixes. ---
// A 5-lens hunt over internal/ui found sibling instances of the Ctrl+O bug
// (user action flips state, render gate silently eats the effect / key gated
// to StateInput when its surface is a during-work one). Each test here pins
// one confirmed fix.

// lastToastMessage returns the most recent ACTIVE toast's message ("" if none).
func lastToastMessage(m Model) string {
	if m.toastManager == nil || len(m.toastManager.toasts) == 0 {
		return ""
	}
	return m.toastManager.toasts[len(m.toastManager.toasts)-1].Message
}

// TestCtrlJInsertsNewlineDuringStreaming: Ctrl+J (the advertised universal
// newline) was StateInput-only — during type-ahead composing it fell through
// to the textarea, which does not bind ctrl+j, so multi-line composing was
// impossible exactly while the agent worked.
func TestCtrlJInsertsNewlineDuringStreaming(t *testing.T) {
	m := *NewModel()
	m.width = 100
	m.height = 40
	m.state = StateStreaming
	m.input.textarea.SetValue("first line")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m2 := updated.(Model)
	raw := m2.input.textarea.Value()
	if !strings.Contains(raw, "\n") {
		t.Fatalf("Ctrl+J during streaming must insert a newline into the type-ahead buffer, got %q", raw)
	}
}

// TestHalfPageScrollDuringStreaming: Ctrl+U/Ctrl+D (empty input) were
// StateInput-only while their 3-line siblings Ctrl+B/Ctrl+F already worked
// during work — an inconsistent scroll set mid-stream.
func TestHalfPageScrollDuringStreaming(t *testing.T) {
	m := *NewModel()
	m.width = 100
	m.height = 10
	m.state = StateStreaming
	m.output.SetSize(80, 10)
	for i := 0; i < 80; i++ {
		m.output.AppendLine("line")
	}
	m.output.viewport.SetYOffset(40)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	m2 := updated.(Model)
	if cmd == nil {
		t.Fatal("Ctrl+U (empty input) during streaming must be consumed")
	}
	if got := m2.output.viewport.YOffset; got >= 40 {
		t.Fatalf("Ctrl+U during streaming must scroll up, offset still %d", got)
	}
	if !m2.output.IsFrozen() {
		t.Fatal("scrolling up mid-stream must freeze auto-follow")
	}

	updated, cmd = m2.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	m3 := updated.(Model)
	if cmd == nil {
		t.Fatal("Ctrl+D (empty input) during streaming must be consumed")
	}
	if m3.output.viewport.YOffset <= m2.output.viewport.YOffset {
		t.Fatal("Ctrl+D during streaming must scroll down")
	}
}

// TestAltCConsumedDuringStreaming: alt+c (copy last response) was
// StateInput-only — during a new turn's streaming the user could not copy the
// PRIOR response; worse, the key leaked into the textarea as
// CapitalizeWordForward. lastResponseText is deliberately left EMPTY (the
// round-8 lesson: a non-empty value writes a real OSC52 escape and clobbers
// the developer's system clipboard) — the gate + consumption is what changed.
func TestAltCConsumedDuringStreaming(t *testing.T) {
	m := *NewModel()
	m.width = 100
	m.state = StateProcessing
	m.input.textarea.SetValue("compose text")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}, Alt: true})
	m2 := updated.(Model)
	if cmd == nil {
		t.Fatal("alt+c during processing must be consumed (was leaking CapitalizeWordForward into the textarea)")
	}
	if got := m2.input.textarea.Value(); got != "compose text" {
		t.Fatalf("alt+c must not mutate the compose buffer, got %q", got)
	}
}

// TestShortcutsOverlayFilterAcceptsQKJ: the overlay's free-text filter could
// not contain q/k/j — 'q' closed the overlay mid-word ("quit" dismissed it on
// the first letter), k/j scrolled instead of typing. Nav keys now apply only
// while the query is empty.
func TestShortcutsOverlayFilterAcceptsQKJ(t *testing.T) {
	m := *NewModel()
	m.width = 100
	m.height = 40
	m.shortcutsOverlay.Show()
	m.state = StateShortcutsOverlay

	for _, r := range "quick jk" {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}
	if m.state != StateShortcutsOverlay {
		t.Fatal("typing a filter containing 'q' must not close the overlay")
	}
	if got := m.shortcutsOverlay.GetSearch(); got != "quick jk" {
		t.Fatalf("filter must accumulate ALL typed runes (filter-first, no letter nav), got %q", got)
	}

	// Esc clears the filter first, then a second Esc closes.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m2 := updated.(Model)
	if m2.shortcutsOverlay.GetSearch() != "" || m2.state != StateShortcutsOverlay {
		t.Fatal("first Esc must clear the filter and keep the overlay open")
	}
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m3 := updated.(Model)
	if m3.state == StateShortcutsOverlay {
		t.Fatal("second Esc must close the overlay")
	}
}

// TestCtrlHClosesObservatory: the key that OPENS the observatory could not
// close it (only Esc could), while the panel footer advertised keys that
// didn't work.
func TestCtrlHClosesObservatory(t *testing.T) {
	m := *NewModel()
	m.width = 100
	m.height = 40
	m.state = StateInput

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlH})
	m2 := updated.(Model)
	if m2.state != StateContextObservatory {
		t.Fatalf("Ctrl+H must open the observatory, state=%v", m2.state)
	}

	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyCtrlH})
	m3 := updated.(Model)
	if m3.state != StateInput {
		t.Fatalf("Ctrl+H must CLOSE the observatory it opened (toggle symmetry), state=%v", m3.state)
	}
	if m3.observatoryPanel.IsVisible() {
		t.Fatal("observatory panel must be hidden after the closing Ctrl+H")
	}
}

// TestPaletteTodosHonestToastWhenEmpty: the palette "Toggle Task List" kept
// the lying "Todos shown" toast after the Ctrl+T key handler got the honest
// empty-state label — both now share toggleTodosPanel so they can't drift.
func TestPaletteTodosHonestToastWhenEmpty(t *testing.T) {
	m := *NewModel()
	m.width = 100
	m.state = StateInput

	m.dispatchPaletteAction(paletteActionTodos)
	if got := lastToastMessage(m); !strings.Contains(got, "appears when") {
		t.Fatalf("palette todos toggle with zero items must use the honest empty-state toast, got %q", got)
	}

	// With items present, the plain "shown" label applies.
	m2 := *NewModel()
	m2.width = 100
	m2.state = StateInput
	m2.todoItems = []string{"○ real task"}
	m2.dispatchPaletteAction(paletteActionTodos)
	if got := lastToastMessage(m2); got != "Todos shown" {
		t.Fatalf("palette todos toggle with items must say 'Todos shown', got %q", got)
	}
}

// TestPaletteAgentTreeHonestToastWhenEmpty: same drift for "Toggle Agent Tree".
func TestPaletteAgentTreeHonestToastWhenEmpty(t *testing.T) {
	m := *NewModel()
	m.width = 100
	m.state = StateInput

	m.dispatchPaletteAction(paletteActionAgentTree)
	if got := lastToastMessage(m); !strings.Contains(got, "appears when") {
		t.Fatalf("palette agent-tree toggle with zero nodes must use the honest empty-state toast, got %q", got)
	}
}

// TestPalettePlanPanelNoSilentNoOp: "Toggle Plan Panel" with no plan panel
// visible was a TOTAL silent no-op (no toast, no render change, no feedback).
func TestPalettePlanPanelNoSilentNoOp(t *testing.T) {
	m := *NewModel()
	m.width = 100
	m.state = StateInput

	m.dispatchPaletteAction(paletteActionPlanPanel)
	if got := lastToastMessage(m); !strings.Contains(got, "plan") {
		t.Fatalf("palette plan-panel action with no plan must give honest feedback, got %q", got)
	}
}

// TestStreamWatchdogCancelsBackend: the 15-minute stream watchdog reset the
// UI to StateInput but called only onInterrupt — a callback with ZERO
// production wiring (SetInterruptCallback has no callers) — so the backend
// request kept running behind a UI that claimed the turn was over. It must
// call onCancel, the wired callback (app.CancelProcessing) the Esc path uses.
func TestStreamWatchdogCancelsBackend(t *testing.T) {
	m := *NewModel()
	m.width = 100
	m.height = 40
	m.state = StateStreaming
	m.streamStartTime = time.Now().Add(-m.streamTimeout - time.Minute)

	cancelled := false
	m.SetCancelCallback(func() { cancelled = true })

	updated, _ := m.Update(spinner.TickMsg{})
	m2 := updated.(Model)

	if m2.state != StateInput {
		t.Fatalf("watchdog must reset the UI, state=%v", m2.state)
	}
	if !cancelled {
		t.Fatal("watchdog must cancel the BACKEND request via onCancel — not only reset the UI")
	}
}
