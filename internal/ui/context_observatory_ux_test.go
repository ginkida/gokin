package ui

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestContextObservatoryEmptyStateIsHonestAndAdaptive(t *testing.T) {
	panel := NewContextObservatoryPanel(DefaultStyles())
	panel.Show()

	for _, width := range []int{1, 4, 20, 60, 160} {
		view := panel.View(width)
		plain := stripAnsi(view)
		if width >= 4 {
			for _, want := range []string{"No context health data yet", "Metrics appear after", "Close"} {
				// Very narrow panels may truncate copy, but must still fit.
				if width >= 30 && !strings.Contains(plain, want) {
					t.Fatalf("width=%d empty observatory missing %q:\n%s", width, want, plain)
				}
			}
		}
		for row, line := range strings.Split(view, "\n") {
			limit := width
			if limit > 120 {
				limit = 120
			}
			if got := lipgloss.Width(line); got > limit {
				t.Fatalf("width=%d row=%d rendered %d cells, limit=%d:\n%s", width, row, got, limit, plain)
			}
		}
		if strings.Contains(plain, "Context healthy") || strings.Contains(plain, "100%") {
			t.Fatalf("width=%d empty snapshot claimed healthy/full metrics:\n%s", width, plain)
		}
	}
}

func TestContextObservatoryNormalizesInvalidMetrics(t *testing.T) {
	panel := NewContextObservatoryPanel(DefaultStyles())
	panel.Show()
	panel.UpdateHealth(ContextHealthMsg{
		TotalTokens:       150,
		MaxTokens:         100,
		PercentUsed:       math.NaN(),
		SystemTokens:      -10,
		InstructionTokens: 20,
		HistoryTokens:     80,
		ToolTokens:        50,
		RequestsRemaining: -5,
		RequestsLimit:     -10,
		TokensRemaining:   300,
		TokensLimit:       200,
		LastPruningTime:   time.Now().Add(time.Minute),
	})

	view := stripAnsi(panel.View(80))
	for _, want := range []string{"150 / 100 tokens (100.0%)", "Requests: unavailable", "Tokens: 200 / 200", "0s ago"} {
		if !strings.Contains(view, want) {
			t.Fatalf("normalized observatory missing %q:\n%s", want, view)
		}
	}
	for _, invalid := range []string{"NaN", "Inf", "-5", "-10", "300 / 200", "-1m"} {
		if strings.Contains(view, invalid) {
			t.Fatalf("observatory retained invalid metric %q:\n%s", invalid, view)
		}
	}
}

func TestContextObservatoryCapsFilesAndTruncatesEveryRow(t *testing.T) {
	panel := NewContextObservatoryPanel(DefaultStyles())
	panel.Show()
	files := make([]string, 9)
	for i := range files {
		files[i] = fmt.Sprintf("/very/long/project/path/with/unicode/请求/file-%02d.go", i)
	}
	panel.UpdateHealth(ContextHealthMsg{
		TotalTokens:       50,
		MaxTokens:         100,
		ActiveFiles:       files,
		PruningAlert:      strings.Repeat("context pressure warning ", 10),
		RequestsRemaining: 42,
	})

	view := panel.View(40)
	plain := stripAnsi(view)
	if !strings.Contains(plain, "… 4 more files") {
		t.Fatalf("file inventory did not expose folded count:\n%s", plain)
	}
	if strings.Contains(plain, "file-05.go") {
		t.Fatalf("file inventory rendered beyond its five-row cap:\n%s", plain)
	}
	if !strings.Contains(plain, "42 remaining") || !strings.Contains(plain, "limit unavai") {
		t.Fatalf("partial provider metadata was presented dishonestly:\n%s", plain)
	}
	for row, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > 40 {
			t.Fatalf("row %d width=%d, want <=40:\n%s", row, got, plain)
		}
	}
}

func TestContextObservatoryUsageFallsBackToTokenRatio(t *testing.T) {
	panel := NewContextObservatoryPanel(DefaultStyles())
	panel.UpdateHealth(ContextHealthMsg{TotalTokens: 25, MaxTokens: 100})
	if got := panel.normalizedUsage(25, 100); got != .25 {
		t.Fatalf("normalizedUsage=%v, want .25", got)
	}
	panel.health.PercentUsed = 3
	if got := panel.normalizedUsage(25, 100); got != 1 {
		t.Fatalf("normalizedUsage high clamp=%v, want 1", got)
	}
	panel.health.PercentUsed = -2
	if got := panel.normalizedUsage(25, 100); got != .25 {
		t.Fatalf("normalizedUsage invalid fallback=%v, want .25", got)
	}
}

func TestContextObservatoryShortTerminalKeepsCloseFooter(t *testing.T) {
	panel := NewContextObservatoryPanel(DefaultStyles())
	panel.Show()
	panel.UpdateHealth(ContextHealthMsg{
		TotalTokens:       75,
		MaxTokens:         100,
		RequestsRemaining: 8,
		RequestsLimit:     10,
		TokensRemaining:   400,
		TokensLimit:       1000,
		ActiveFiles:       []string{"a.go", "b.go"},
		PruningAlert:      "Context pressure is elevated",
	})

	for _, height := range []int{8, 10, 16, 23} {
		view := panel.View(50, height)
		plain := stripAnsi(view)
		if got := lipgloss.Height(view); got > max(height-1, 7) {
			t.Fatalf("height=%d compact panel height=%d:\n%s", height, got, plain)
		}
		for _, want := range []string{"Context: 75/100 tokens", "Close"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("height=%d compact panel missing %q:\n%s", height, want, plain)
			}
		}
	}
}

func TestContextObservatoryModelFrameFitsShortTerminal(t *testing.T) {
	m := NewModel()
	m.state = StateContextObservatory
	m.observatoryPanel.Show()
	m.observatoryPanel.UpdateHealth(ContextHealthMsg{TotalTokens: 50, MaxTokens: 100})
	m.applyResize(&tea.WindowSizeMsg{Width: 50, Height: 12})

	view := m.View()
	if got := lipgloss.Height(view); got != 12 {
		t.Fatalf("frame height=%d, want 12:\n%s", got, stripAnsi(view))
	}
	for row, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > 50 {
			t.Fatalf("frame row %d width=%d, want <=50:\n%s", row, got, stripAnsi(view))
		}
	}
}
