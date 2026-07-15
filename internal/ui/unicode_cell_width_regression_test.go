package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func assertRowsFitWidth(t *testing.T, rendered string, width int) {
	t.Helper()
	for row, line := range strings.Split(stripAnsi(rendered), "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("row %d rendered %d cells, want <= %d: %q\n%s", row, got, width, line, stripAnsi(rendered))
		}
	}
}

func TestShortenPathUsesTerminalCellsAndKeepsEmojiClusters(t *testing.T) {
	path := "/项目/👍🏽/非常非常长的文件名.go"
	for width := 1; width <= 24; width++ {
		got := shortenPath(path, width)
		if cells := lipgloss.Width(got); cells > width {
			t.Errorf("width=%d rendered %d cells: %q", width, cells, got)
		}
		if strings.Contains(got, "👍") && !strings.Contains(got, "👍🏽") {
			t.Errorf("width=%d split emoji modifier cluster: %q", width, got)
		}
		if strings.Contains(got, "🏽") && !strings.Contains(got, "👍🏽") {
			t.Errorf("width=%d rendered a detached emoji modifier: %q", width, got)
		}
	}

	if got := shortenPath(path, 16); !strings.HasSuffix(got, ".go") {
		t.Fatalf("suffix-significant filename was lost: %q", got)
	}
}

func TestCompactInlineUsesTerminalCellsAndKeepsEmojiClusters(t *testing.T) {
	text := strings.Repeat("请求👍🏽", 10)
	for width := 1; width <= 24; width++ {
		got := compactInline(text, width)
		if cells := lipgloss.Width(got); cells > width {
			t.Errorf("width=%d rendered %d cells: %q", width, cells, got)
		}
		if strings.Contains(got, "👍") && !strings.Contains(got, "👍🏽") {
			t.Errorf("width=%d split emoji modifier cluster: %q", width, got)
		}
		if strings.Contains(got, "🏽") && !strings.Contains(got, "👍🏽") {
			t.Errorf("width=%d rendered a detached emoji modifier: %q", width, got)
		}
	}
}

func TestPathPairHonorsItsWholeCellBudget(t *testing.T) {
	source := "/项目/👍🏽/非常长的源文件.go"
	destination := "/目标/👨‍👩‍👧‍👦/非常长的目标文件.go"
	for width := 1; width <= 60; width++ {
		got := formatPathPair(source, destination, width)
		if cells := lipgloss.Width(got); cells > width {
			t.Errorf("width=%d rendered %d cells: %q", width, cells, got)
		}
		if strings.Contains(got, "👍") && !strings.Contains(got, "👍🏽") {
			t.Errorf("width=%d split emoji modifier cluster: %q", width, got)
		}
		if strings.Contains(got, "👨") && !strings.Contains(got, "👨‍👩‍👧‍👦") {
			t.Errorf("width=%d split ZWJ family cluster: %q", width, got)
		}
	}
}

func TestToolSummariesUseCellBudgetsWithoutSplittingClusters(t *testing.T) {
	detail := strings.Repeat("请求👍🏽", 30) + " /retry"
	for width := 1; width <= 56; width++ {
		got := summarizeToolDetail(detail, width)
		if cells := lipgloss.Width(got); cells > width {
			t.Errorf("detail width=%d rendered %d cells: %q", width, cells, got)
		}
		if strings.Contains(got, "👍") && !strings.Contains(got, "👍🏽") {
			t.Errorf("detail width=%d split emoji modifier cluster: %q", width, got)
		}
	}

	summary := summarizeSubAgentTask(strings.Repeat("修复👍🏽", 100), "general")
	body := strings.TrimPrefix(summary, "general · ")
	if got := lipgloss.Width(body); got > 70 {
		t.Fatalf("sub-agent body rendered %d cells, want <=70: %q", got, body)
	}
	if strings.Contains(body, "👍") && !strings.Contains(body, "👍🏽") {
		t.Fatalf("sub-agent summary split emoji modifier cluster: %q", body)
	}
}

func TestLiveToolInfoUsesCellAwareCompactText(t *testing.T) {
	m := NewModel()
	command := strings.Repeat("请求👍🏽", 30)
	got := m.extractToolInfoFromArgs("bash", map[string]any{"command": command})
	if cells := lipgloss.Width(got); cells > 62 { // "$ " plus the 60-cell command budget
		t.Fatalf("bash tool info rendered %d cells: %q", cells, got)
	}
	if strings.Contains(got, "👍") && !strings.Contains(got, "👍🏽") {
		t.Fatalf("bash tool info split emoji modifier cluster: %q", got)
	}

	got = m.extractToolInfoFromArgs("grep", map[string]any{"pattern": command})
	if cells := lipgloss.Width(got); cells > 30 {
		t.Fatalf("grep tool info rendered %d cells: %q", cells, got)
	}
}

func TestDisplayErrorLinesUsesCellWidthsAndHardWrapsLongTokens(t *testing.T) {
	msg := strings.Repeat("请求", 20) + "/retry"
	lines := displayErrorLines(msg, 16)
	for i, line := range lines {
		if got := lipgloss.Width(line); got > 16 {
			t.Fatalf("line %d rendered %d cells: %q", i, got, line)
		}
	}
	if joined := strings.Join(lines, ""); !strings.Contains(joined, "/retry") {
		t.Fatalf("actionable tail must survive hard wrapping and line cap: %v", lines)
	}
}

func TestErrorGuidanceFitsAnnouncedTerminalWidth(t *testing.T) {
	errMsg := "failed to start MCP server: executable file not found 请求请求请求"
	for _, width := range []int{1, 2, 4, 8, 12, 18, 24, 32, 48} {
		got := FormatErrorWithGuidanceWidth(DefaultStyles(), errMsg, width)
		assertRowsFitWidth(t, got, width)
		identity := strings.Join(strings.Fields(stripAnsi(got)), "")
		if !strings.Contains(identity, "Error") {
			t.Errorf("width=%d hid the error identity: %q", width, identity)
		}
	}
}

func TestDisplayErrorLinesStripsTerminalControls(t *testing.T) {
	lines := displayErrorLines("\x1b]0;owned\a\x1b[31mfailed\x1b[0m\rnext", 40)
	got := strings.Join(lines, " ")
	if strings.ContainsAny(got, "\x1b\a\r") || strings.Contains(got, "[31m") {
		t.Fatalf("terminal control sequence leaked into error display: %q", got)
	}
	if !strings.Contains(got, "failed") {
		t.Fatalf("human-readable error content was lost: %q", got)
	}
}

func TestInlineDiffCardFitsWideCellMetadataAndContent(t *testing.T) {
	const width = 60
	card := renderInlineDiffCard(width, DiffPreviewRequestMsg{
		ToolName:   strings.Repeat("工具👍🏽", 20),
		FilePath:   "/项目/非常非常长的目录/👍🏽/文件.go",
		OldContent: strings.Repeat("旧内容👍🏽", 30),
		NewContent: strings.Repeat("新内容👨‍👩‍👧‍👦", 30),
	})
	if card == "" {
		t.Fatal("card unexpectedly hidden at its supported minimum width")
	}
	assertRowsFitWidth(t, card, width)
}
