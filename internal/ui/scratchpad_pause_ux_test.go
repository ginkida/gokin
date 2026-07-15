package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestScratchpadFitsEveryTerminalWidth(t *testing.T) {
	for width := 1; width <= 48; width++ {
		m := panelTestModel()
		m.width = width
		m.height = 30
		m.scratchpad = "first note\n" + strings.Repeat("界 long content ", 20) + "\nlatest note"

		got := m.renderScratchpad()
		for lineNo, line := range strings.Split(got, "\n") {
			if lineWidth := lipgloss.Width(line); lineWidth > width {
				t.Fatalf("width=%d line=%d overflow=%d: %q", width, lineNo, lineWidth, stripAnsi(line))
			}
		}
	}
}

func TestScratchpadShortTerminalShowsBoundedTail(t *testing.T) {
	m := panelTestModel()
	m.width = 42
	m.height = 12
	for i := 0; i < 9; i++ {
		m.scratchpad += fmt.Sprintf("note %d\n", i)
	}

	got := stripAnsi(m.renderScratchpad())
	if !strings.Contains(got, "latest notes") || !strings.Contains(got, "note 8") {
		t.Fatalf("compact scratchpad must explain and retain the latest note:\n%s", got)
	}
	if !strings.Contains(got, "… 6 earlier line(s)") || strings.Contains(got, "note 0") {
		t.Fatalf("compact scratchpad must disclose and remove old rows:\n%s", got)
	}
	if rows := strings.Count(got, "\n") + 1; rows > 7 {
		t.Fatalf("compact scratchpad rendered %d rows, want <=7:\n%s", rows, got)
	}
}

func TestScratchpadWhitespaceOnlyDoesNotRender(t *testing.T) {
	m := panelTestModel()
	m.scratchpad = " \n\t\n "
	if got := m.renderScratchpad(); got != "" {
		t.Fatalf("whitespace-only scratchpad rendered a blank panel: %q", stripAnsi(got))
	}
}

func TestScratchpadFitsEveryTinyTerminalHeight(t *testing.T) {
	for height := 1; height <= 12; height++ {
		m := panelTestModel()
		m.width = 46
		m.height = height
		for i := 0; i < 10; i++ {
			m.scratchpad += fmt.Sprintf("note %d\n", i)
		}

		view := m.renderScratchpad()
		if got, limit := lipgloss.Height(view), scratchpadPanelHeightBudget(height); got > limit {
			t.Fatalf("height=%d rendered %d rows, want <=%d:\n%s", height, got, limit, stripAnsi(view))
		}
		plain := stripAnsi(view)
		if height >= 4 && !strings.Contains(plain, "note 9") {
			t.Fatalf("height=%d lost the newest note:\n%s", height, plain)
		}
		if height >= 4 && !strings.Contains(plain, "earlier") {
			t.Fatalf("height=%d hid old notes without disclosure:\n%s", height, plain)
		}
	}
}

func TestScratchpadSanitizesRuntimeControlSequences(t *testing.T) {
	m := panelTestModel()
	m.width = 60
	m.height = 20
	m.scratchpad = "safe\x1b[2J\nlatest\x1b]0;forged-title\a"

	view := m.renderScratchpad()
	plain := stripAnsi(view)
	if strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\x1b]0;") {
		t.Fatalf("scratchpad emitted runtime control sequence: %q", view)
	}
	for _, want := range []string{"safe", "latest"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("sanitization discarded note %q:\n%s", want, plain)
		}
	}
}

func TestPlanPauseFallbackFitsAndNormalizesContent(t *testing.T) {
	for width := 1; width <= 48; width++ {
		m := panelTestModel()
		m.width = width
		got := m.renderPlanPauseBlock(PlanProgressMsg{
			CurrentStepID: 3,
			CurrentTitle:  "  Verify\n  recovery  ",
			Reason:        strings.Repeat("watchdog requested a safe pause ", 8),
		})
		for lineNo, line := range strings.Split(got, "\n") {
			if lineWidth := lipgloss.Width(line); lineWidth > width {
				t.Fatalf("width=%d line=%d overflow=%d: %q", width, lineNo, lineWidth, stripAnsi(line))
			}
		}
	}
}

func TestPlanPauseUsesOneActionableSurface(t *testing.T) {
	m := NewModel()
	m.state = StateProcessing
	m.applyResize(&tea.WindowSizeMsg{Width: 72, Height: 18})
	m.planProgressPanel.StartPlan("p1", "Recovery plan", "", []PlanStepInfo{{ID: 1, Title: "Verify recovery"}})
	m.planProgressPanel.StartStep(1)
	m.planProgressPanel.PauseStep(1, "watchdog requested pause")
	m.planPauseNotice = &PlanProgressMsg{CurrentStepID: 1, CurrentTitle: "Verify recovery", Reason: "watchdog requested pause", Status: "paused"}

	view := stripAnsi(m.View())
	if count := strings.Count(view, "Plan paused"); count != 1 {
		t.Fatalf("pause state must render once, got %d copies:\n%s", count, view)
	}
	if !strings.Contains(view, "/resume-plan") || !strings.Contains(view, "Continue execution") {
		t.Fatalf("primary plan panel must retain the recovery action:\n%s", view)
	}
	if got := lipgloss.Height(view); got != 18 {
		t.Fatalf("paused frame height=%d want 18", got)
	}
}

func TestPlanPauseNoticeClearsOnEverySupersedingState(t *testing.T) {
	for _, status := range []string{"in_progress", "completed", "failed", "skipped"} {
		t.Run(status, func(t *testing.T) {
			m := NewModel()
			m.planPauseNotice = &PlanProgressMsg{Status: "paused", CurrentStepID: 1}
			updated, _ := m.Update(PlanProgressMsg{Status: status, CurrentStepID: 1})
			got := updated.(Model)
			if got.planPauseNotice != nil {
				t.Fatalf("status %q retained stale pause action", status)
			}
		})
	}
}
