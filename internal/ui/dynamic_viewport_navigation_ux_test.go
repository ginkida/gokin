package ui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

var transcriptLineNumberRE = regexp.MustCompile(`TRANSCRIPT-(\d{3})`)

func dynamicTranscriptModel(t *testing.T) *Model {
	t.Helper()
	m := NewModel()
	m.applyResize(&tea.WindowSizeMsg{Width: 80, Height: 20})
	m.SetReducedMotion(true)
	for i := range 80 {
		m.output.AppendLine(fmt.Sprintf("TRANSCRIPT-%03d", i))
	}
	m.output.ForceUpdateViewport()
	return m
}

func visibleTranscriptRange(t *testing.T, view string) (int, int) {
	t.Helper()
	matches := transcriptLineNumberRE.FindAllStringSubmatch(ansi.Strip(view), -1)
	if len(matches) == 0 {
		t.Fatalf("rendered frame contains no transcript rows:\n%s", ansi.Strip(view))
	}
	first, err := strconv.Atoi(matches[0][1])
	if err != nil {
		t.Fatal(err)
	}
	last, err := strconv.Atoi(matches[len(matches)-1][1])
	if err != nil {
		t.Fatal(err)
	}
	return first, last
}

func TestPageUpUsesTheActuallyRenderedTranscriptHeight(t *testing.T) {
	m := dynamicTranscriptModel(t)
	// A tall draft leaves substantially less room than the resize-time
	// terminalHeight-5 viewport. Navigation must page from what is on screen,
	// not from that stale optimistic geometry.
	m.input.textarea.SetValue(strings.Repeat("draft\n", 6) + "draft")
	m.input.syncTextareaHeight()

	beforeFirst, beforeLast := visibleTranscriptRange(t, m.View())
	if beforeLast != 79 {
		t.Fatalf("setup did not render the transcript bottom: range=%d..%d", beforeFirst, beforeLast)
	}

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyPgUp})
	afterFirst, afterLast := visibleTranscriptRange(t, m.View())
	if afterLast != beforeFirst-1 {
		t.Fatalf("PgUp skipped unseen rows: before=%d..%d after=%d..%d; pages should meet at the boundary",
			beforeFirst, beforeLast, afterFirst, afterLast)
	}
}

func TestGrowingComposerExposesFrozenViewportAndScrollIndicator(t *testing.T) {
	m := dynamicTranscriptModel(t)
	// Freeze while the resize-time viewport is at its bottom, then grow the
	// composer. The same offset is no longer the bottom of the smaller visible
	// viewport and the status bar must say so.
	m.output.viewport.GotoBottom()
	m.output.SetFrozen(true)
	m.input.textarea.SetValue(strings.Repeat("draft\n", 6) + "draft")
	m.input.syncTextareaHeight()

	view := ansi.Strip(m.View())
	_, last := visibleTranscriptRange(t, view)
	if last == 79 {
		t.Fatal("setup unexpectedly remained at the true transcript bottom")
	}
	status := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if !strings.Contains(status[len(status)-1], "↑") {
		t.Fatalf("status bar hid the off-bottom/frozen state after composer growth:\n%s", view)
	}
}

func TestMouseWheelMovesFromTheRenderedDynamicViewport(t *testing.T) {
	m := dynamicTranscriptModel(t)
	m.input.textarea.SetValue(strings.Repeat("draft\n", 6) + "draft")
	m.input.syncTextareaHeight()
	beforeFirst, beforeLast := visibleTranscriptRange(t, m.View())

	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	after := updated.(Model)
	afterFirst, afterLast := visibleTranscriptRange(t, after.View())
	const wheelDelta = 3 // bubbles/viewport default MouseWheelDelta
	if afterFirst != beforeFirst-wheelDelta || afterLast >= beforeLast {
		t.Fatalf("one wheel notch jumped by stale viewport geometry: before=%d..%d after=%d..%d",
			beforeFirst, beforeLast, afterFirst, afterLast)
	}
	if !after.output.IsFrozen() {
		t.Fatal("wheel-up must retain ownership of the transcript while output streams")
	}
}

func TestSmoothScrollTickChangesTheFrameInsteadOfAHiddenCopy(t *testing.T) {
	m := NewModel()
	m.applyResize(&tea.WindowSizeMsg{Width: 80, Height: 20})
	m.SetReducedMotion(true)
	for i := range 40 {
		m.output.AppendLine(fmt.Sprintf("TRANSCRIPT-%03d", i))
	}
	m.output.ForceUpdateViewport()
	_, oldLast := visibleTranscriptRange(t, m.View())
	if oldLast != 39 {
		t.Fatalf("setup bottom=%d, want 39", oldLast)
	}

	m.SetReducedMotion(false)
	var burst strings.Builder
	for i := 40; i < 60; i++ {
		fmt.Fprintf(&burst, "TRANSCRIPT-%03d\n", i)
	}
	m.output.AppendText(burst.String())
	m.output.ForceUpdateViewport()
	_, beforeTickLast := visibleTranscriptRange(t, m.View())
	if beforeTickLast >= 59 {
		t.Fatalf("normal motion jumped directly to its target before a tick: old=%d now=%d", oldLast, beforeTickLast)
	}

	if !m.output.TickSmoothScroll() {
		t.Fatal("large following gap did not start smooth scrolling")
	}
	_, afterTickLast := visibleTranscriptRange(t, m.View())
	if afterTickLast <= beforeTickLast || afterTickLast >= 59 {
		t.Fatalf("tick was not visible as an intermediate frame: before=%d after=%d target=59", beforeTickLast, afterTickLast)
	}
}
