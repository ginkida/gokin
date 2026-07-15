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

func TestContextObservatoryEmptyShortTerminalKeepsRecovery(t *testing.T) {
	panel := NewContextObservatoryPanel(DefaultStyles())
	panel.Show()

	for _, height := range []int{7, 8, 10} {
		view := panel.View(50, height)
		plain := stripAnsi(view)
		for _, want := range []string{"No context health data yet", "Close"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("height=%d empty observatory missing %q:\n%s", height, want, plain)
			}
		}
		if got := lipgloss.Height(view); got > max(height-1, 7) {
			t.Fatalf("height=%d empty observatory rendered %d rows:\n%s", height, got, plain)
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

func TestContextObservatoryDerivesHonestPressureStatus(t *testing.T) {
	for _, tc := range []struct {
		name      string
		total     int
		maximum   int
		want      string
		notWant   string
		wantColor lipgloss.Color
	}{
		{name: "healthy", total: 50, maximum: 100, want: "Context healthy", notWant: "pressure", wantColor: ColorSuccess},
		{name: "elevated", total: 80, maximum: 100, want: "Context pressure elevated", notWant: "Context healthy", wantColor: ColorWarning},
		{name: "critical", total: 95, maximum: 100, want: "Context critical · pruning likely soon", notWant: "Context healthy", wantColor: ColorError},
		{name: "partial", total: 50, maximum: 0, want: "Context limit unavailable", notWant: "Context healthy", wantColor: ColorMuted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			panel := NewContextObservatoryPanel(DefaultStyles())
			panel.Show()
			panel.UpdateHealth(ContextHealthMsg{TotalTokens: tc.total, MaxTokens: tc.maximum})
			view := stripAnsi(panel.View(80))
			if !strings.Contains(view, tc.want) || strings.Contains(view, tc.notWant) {
				t.Fatalf("derived status is dishonest:\n%s", view)
			}
			_, color := contextHealthSummary(panel.normalizedUsage(tc.total, tc.maximum), tc.maximum > 0, "")
			if color != tc.wantColor {
				t.Fatalf("status color=%v want %v", color, tc.wantColor)
			}
			if tc.maximum > 0 {
				usageColor := contextUsageColor(panel.normalizedUsage(tc.total, tc.maximum))
				if usageColor != tc.wantColor {
					t.Fatalf("usage gauge color=%v want %v", usageColor, tc.wantColor)
				}
			}
		})
	}
}

func TestContextObservatoryPreservesSafeFileIdentityAndSnapshot(t *testing.T) {
	files := []string{"/very/long/project/path/with/shared/prefix/]0;hijack\afile-important.go"}
	panel := NewContextObservatoryPanel(DefaultStyles())
	panel.Show()
	panel.UpdateHealth(ContextHealthMsg{TotalTokens: 50, MaxTokens: 100, ActiveFiles: files})
	files[0] = "mutated-after-update.go"

	view := panel.View(40)
	plain := stripAnsi(view)
	if !strings.Contains(plain, "file-important.go") || strings.Contains(plain, "mutated-after-update") || strings.Contains(plain, "hijack") || strings.Contains(view, "\x1b]") {
		t.Fatalf("active-file snapshot is unsafe or lost identity:\n%s", plain)
	}
	for row, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > 40 {
			t.Fatalf("row %d width=%d, want <=40:\n%s", row, got, plain)
		}
	}
}

func TestContextObservatoryStatusHintMatchesBothCloseBindings(t *testing.T) {
	m := NewModel()
	m.state = StateContextObservatory
	m.observatoryPanel.Show()
	assertShortcutHints(t, m,
		[]string{"Esc/Ctrl+H Close"},
		nil,
	)
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
		for _, want := range []string{"Status: Context pressure is elevated", "Close"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("height=%d compact panel missing %q:\n%s", height, want, plain)
			}
		}
		if height >= 10 && !strings.Contains(plain, "Context: 75/100 tokens") {
			t.Fatalf("height=%d compact panel lost usage after status:\n%s", height, plain)
		}
	}
}

func TestContextObservatoryCriticalStatusWinsSmallestRowBudget(t *testing.T) {
	panel := NewContextObservatoryPanel(DefaultStyles())
	panel.Show()
	panel.UpdateHealth(ContextHealthMsg{
		TotalTokens:       95,
		MaxTokens:         100,
		RequestsRemaining: 80,
		RequestsLimit:     100,
		ActiveFiles:       []string{"a.go", "b.go"},
		PruningAlert:      "Pruning required before the next request",
	})

	view := panel.View(54, 8)
	plain := stripAnsi(view)
	for _, want := range []string{"Status: Pruning required", "Close"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("smallest observatory lost %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "Limits:") || strings.Contains(plain, "Active files:") {
		t.Fatalf("secondary metrics displaced critical status:\n%s", plain)
	}
	if got := lipgloss.Height(view); got > 7 {
		t.Fatalf("smallest observatory height=%d want <=7:\n%s", got, plain)
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
