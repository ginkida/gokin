package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestTinyBatchCannotPauseWithoutVisiblePauseAffordance(t *testing.T) {
	for width := 1; width <= 12; width++ {
		for height := 1; height <= 5; height++ {
			var actions []ProgressAction
			progress := NewProgressModel(DefaultStyles())
			progress.SetSize(width, height)
			progress.Start("Index workspace", 3)
			progress.SetActionCallback(func(action ProgressAction) {
				actions = append(actions, action)
			})
			if view := stripAnsi(progress.View()); strings.Contains(view, "p Pause") {
				t.Fatalf("%dx%d audit setup unexpectedly shows Pause: %q", width, height, view)
			}

			updated, _ := progress.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
			if updated.isPaused || len(actions) != 0 {
				t.Errorf("%dx%d hidden p paused batch: paused=%v actions=%v", width, height, updated.isPaused, actions)
			}
		}
	}

	wideOneRowActions := 0
	wideOneRow := NewProgressModel(DefaultStyles())
	wideOneRow.SetSize(80, 1)
	wideOneRow.Start("Index workspace", 3)
	wideOneRow.SetActionCallback(func(ProgressAction) { wideOneRowActions++ })
	if view := stripAnsi(wideOneRow.View()); strings.Contains(view, "p Pause") {
		t.Fatalf("80x1 batch advertised a pause action outside the usable body: %q", view)
	}
	wideOneRow, _ = wideOneRow.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if wideOneRow.isPaused || wideOneRowActions != 0 {
		t.Fatalf("80x1 hidden p paused batch: paused=%v calls=%d", wideOneRow.isPaused, wideOneRowActions)
	}

	visibleActions := 0
	visible := NewProgressModel(DefaultStyles())
	visible.SetSize(80, 2)
	visible.Start("Index workspace", 3)
	visible.SetActionCallback(func(action ProgressAction) {
		if action == ProgressActionPause {
			visibleActions++
		}
	})
	if view := stripAnsi(visible.View()); !strings.Contains(view, "p Pause") {
		t.Fatalf("readable batch lost Pause affordance: %q", view)
	}
	visible, _ = visible.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if !visible.isPaused || visibleActions != 1 {
		t.Fatalf("visible Pause action was blocked: paused=%v calls=%d", visible.isPaused, visibleActions)
	}

	sentinel := NewProgressModel(DefaultStyles())
	sentinel.Start("Index workspace", 3)
	sentinelActions := 0
	sentinel.SetActionCallback(func(ProgressAction) { sentinelActions++ })
	sentinel, _ = sentinel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if !sentinel.isPaused || sentinelActions != 1 {
		t.Fatalf("unspecified batch geometry was incorrectly gated: paused=%v calls=%d", sentinel.isPaused, sentinelActions)
	}
}

func TestBatchContextHintsMatchPauseVisibilityBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		height    int
		wantPause bool
	}{
		{name: "one row", height: 1, wantPause: false},
		{name: "two rows", height: 2, wantPause: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel()
			m.state = StateBatchProgress
			m.progressModel.Start("Index workspace", 3)
			m.progressModel.SetSize(80, tc.height)
			m.progressModel.SetActionCallback(func(ProgressAction) {})
			hints := plainShortcutHints(m.contextualShortcutHintPairs())
			if got := strings.Contains(hints, "p Pause"); got != tc.wantPause {
				t.Fatalf("height=%d pause hint visible=%v want=%v: %q", tc.height, got, tc.wantPause, hints)
			}
			if !strings.Contains(hints, "esc Cancel") {
				t.Fatalf("height=%d lost visible cancel hint: %q", tc.height, hints)
			}
		})
	}
}

func TestBatchPauseAffordanceAlwaysLeavesAReadableResume(t *testing.T) {
	pauseWidth := lipgloss.Width("Esc Cancel  │  p Pause")
	resumeWidth := lipgloss.Width("Esc Cancel  │  p Resume")
	if pauseWidth >= resumeWidth {
		t.Fatalf("audit setup requires Resume to be wider: pause=%d resume=%d", pauseWidth, resumeWidth)
	}

	tooNarrow := NewProgressModel(DefaultStyles())
	tooNarrow.SetSize(pauseWidth, 2)
	tooNarrow.Start("Index workspace", 3)
	tooNarrowCalls := 0
	tooNarrow.SetActionCallback(func(ProgressAction) { tooNarrowCalls++ })
	if view := stripAnsi(tooNarrow.View()); strings.Contains(view, "p Pause") {
		t.Fatalf("Pause was offered without room for the later Resume action: %q", view)
	}
	tooNarrow, _ = tooNarrow.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if tooNarrow.isPaused || tooNarrowCalls != 0 {
		t.Fatalf("irreversible narrow Pause was accepted: paused=%v calls=%d", tooNarrow.isPaused, tooNarrowCalls)
	}

	var actions []ProgressAction
	reversible := NewProgressModel(DefaultStyles())
	reversible.SetSize(resumeWidth, 2)
	reversible.Start("Index workspace", 3)
	reversible.SetActionCallback(func(action ProgressAction) { actions = append(actions, action) })
	if view := stripAnsi(reversible.View()); !strings.Contains(view, "p Pause") {
		t.Fatalf("exact reversible width lost Pause: %q", view)
	}
	reversible, _ = reversible.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if view := stripAnsi(reversible.View()); !strings.Contains(view, "p Resume") {
		t.Fatalf("paused batch lost its Resume action: %q", view)
	}
	reversible, _ = reversible.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if reversible.isPaused || len(actions) != 2 || actions[0] != ProgressActionPause || actions[1] != ProgressActionResume {
		t.Fatalf("reversible boundary actions=%v paused=%v", actions, reversible.isPaused)
	}
}

func TestPausedBatchResizeFailsClosedUntilResumeGeometryReturns(t *testing.T) {
	resumeWidth := lipgloss.Width("Esc Cancel  │  p Resume")
	var actions []ProgressAction
	progress := NewProgressModel(DefaultStyles())
	progress.SetSize(resumeWidth, 2)
	progress.Start("Index workspace", 3)
	progress.SetActionCallback(func(action ProgressAction) {
		actions = append(actions, action)
	})

	progress, _ = progress.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if !progress.isPaused || len(actions) != 1 || actions[0] != ProgressActionPause {
		t.Fatalf("setup did not pause at reversible geometry: paused=%v actions=%v", progress.isPaused, actions)
	}

	assertResizeRecovery := func(label string, candidate ProgressModel) {
		t.Helper()
		footer := stripAnsi(candidate.View())
		if !strings.Contains(footer, "Resize") || !strings.Contains(footer, "Esc") || strings.Contains(footer, "p Resume") {
			t.Fatalf("%s footer did not expose resize/cancel recovery: %q", label, footer)
		}

		parent := NewModel()
		parent.state = StateBatchProgress
		parent.progressModel = candidate
		hints := plainShortcutHints(parent.contextualShortcutHintPairs())
		if !strings.Contains(hints, "↔ Resize to resume") || !strings.Contains(hints, "esc Cancel") || strings.Contains(hints, "p Resume") {
			t.Fatalf("%s contextual recovery mismatch: %q", label, hints)
		}
	}

	progress.SetSize(resumeWidth-1, 2)
	assertResizeRecovery("narrow", progress)
	progress, _ = progress.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if !progress.isPaused || len(actions) != 1 {
		t.Fatalf("narrow hidden Resume was accepted: paused=%v actions=%v", progress.isPaused, actions)
	}

	progress.SetSize(resumeWidth, 1)
	assertResizeRecovery("one-row", progress)
	progress, _ = progress.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if !progress.isPaused || len(actions) != 1 {
		t.Fatalf("one-row hidden Resume was accepted: paused=%v actions=%v", progress.isPaused, actions)
	}

	progress.SetSize(resumeWidth, 2)
	if view := stripAnsi(progress.View()); !strings.Contains(view, "p Resume") || strings.Contains(view, "Resize") {
		t.Fatalf("restored exact geometry did not restore Resume: %q", view)
	}
	progress, _ = progress.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if progress.isPaused || len(actions) != 2 || actions[1] != ProgressActionResume {
		t.Fatalf("restored Resume did not work: paused=%v actions=%v", progress.isPaused, actions)
	}
}
