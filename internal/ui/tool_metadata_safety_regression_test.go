package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

const hostileDisplayMetadata = "\x1b]0;owned\a\x1b[31mhello\x1b[0m\nworld\rend"

func assertNoTerminalEnvelope(t *testing.T, rendered string) {
	t.Helper()
	if strings.Contains(rendered, "\x1b]") || strings.ContainsAny(rendered, "\a\r") {
		t.Fatalf("terminal control envelope leaked into display output: %q", rendered)
	}
}

func TestToolMetadataDisplayBoundarySanitizesControls(t *testing.T) {
	arg := formatArgValue(hostileDisplayMetadata)
	if strings.ContainsAny(arg, "\x1b\a\r\n") {
		t.Fatalf("formatArgValue leaked control bytes: %q", arg)
	}
	for _, want := range []string{"hello", "world", "end"} {
		if !strings.Contains(arg, want) {
			t.Errorf("formatArgValue lost readable %q: %q", want, arg)
		}
	}

	args := buildClaudeCodeArgs("bash", map[string]any{"command": hostileDisplayMetadata})
	if strings.ContainsAny(args, "\x1b\a\r\n") {
		t.Fatalf("tool args leaked control bytes: %q", args)
	}

	rendered := DefaultStyles().FormatToolExecutingBlock("mcp\x1b]0;tool\a_name", map[string]any{
		"command": hostileDisplayMetadata,
	})
	assertNoTerminalEnvelope(t, rendered)
	if strings.Contains(stripAnsi(rendered), "\n") {
		t.Fatalf("one-row tool header was split by metadata: %q", stripAnsi(rendered))
	}
}

func TestInlineMetadataSanitizationPreservesMeaningfulSpaces(t *testing.T) {
	command := "printf 'a  b'"
	if got := formatArgValue(command); got != "\""+command+"\"" {
		t.Fatalf("quoted command spacing changed: got %q", got)
	}
	if got := toolStringArg(map[string]any{"path": "  a  b.txt  "}, "path"); got != "a  b.txt" {
		t.Fatalf("path spacing changed: got %q", got)
	}
	if got := compactInline(command, 80); got != command {
		t.Fatalf("compact command spacing changed: got %q", got)
	}
	if got := buildClaudeCodeArgs("bash", map[string]any{"command": command}); !strings.Contains(got, "a  b") {
		t.Fatalf("tool header changed quoted command semantics: %q", got)
	}
	line := stripAnsi(DefaultStyles().FormatToolLine("bash", command, "done  cleanly", time.Second))
	if !strings.Contains(line, "a  b") || !strings.Contains(line, "done  cleanly") {
		t.Fatalf("completed tool line changed meaningful spacing: %q", line)
	}
	horizontalUnicodeSpaces := "dir/a\u00a0\u2003b.txt"
	if got := safeInlineDisplayText(horizontalUnicodeSpaces); got != horizontalUnicodeSpaces {
		t.Fatalf("safe horizontal Unicode spacing changed: got %q", got)
	}

	hostile := "left\nright\t\u0085next\u2028line\u2029\x1b]0;owned\aend"
	got := safeInlineDisplayText(hostile)
	assertNoTerminalEnvelope(t, got)
	if strings.ContainsAny(got, "\n\r\t\u0085\u2028\u2029") || strings.Join(strings.Fields(got), " ") != "left right next line end" {
		t.Fatalf("inline sanitizer did not keep a safe single row: %q", got)
	}
}

func TestPlanStepResultUsesSameCellBudgetForSuccessAndFailure(t *testing.T) {
	styles := DefaultStyles()
	summary := strings.Repeat("请求👍🏽", 80)
	for _, success := range []bool{true, false} {
		got := stripAnsi(styles.FormatPlanStepResult(1, success, summary))
		prefix := "  Step 1 failed "
		if success {
			prefix = "  Step 1 done "
		}
		if cells := lipgloss.Width(got); cells > lipgloss.Width(prefix)+120 {
			t.Errorf("success=%v rendered %d cells beyond summary budget: %q", success, cells, got)
		}
		if strings.Contains(got, "👍") && !strings.Contains(got, "👍🏽") {
			t.Errorf("success=%v split emoji modifier cluster: %q", success, got)
		}
	}
}

func TestToolStringListMetadataSanitizesEverySupportedShape(t *testing.T) {
	spacedPath := "dir/a  b.txt"
	for _, tc := range []struct {
		paths  any
		single any
	}{
		{paths: []string{hostileDisplayMetadata, spacedPath}, single: []string{hostileDisplayMetadata}},
		{paths: []any{hostileDisplayMetadata, spacedPath}, single: []any{hostileDisplayMetadata}},
	} {
		got := toolStringListArg(map[string]any{"paths": tc.paths}, "paths")
		joined := strings.Join(got, "|")
		if strings.ContainsAny(joined, "\x1b\a\r\n") {
			t.Fatalf("git_add paths %T leaked terminal controls: %q", tc.paths, joined)
		}
		for _, want := range []string{"hello", "world", "end"} {
			if !strings.Contains(joined, want) {
				t.Errorf("git_add paths %T lost readable %q: %q", tc.paths, want, joined)
			}
		}
		if len(got) != 2 || got[1] != spacedPath {
			t.Errorf("git_add paths %T rewrote meaningful spacing: %#v", tc.paths, got)
		}

		rendered := buildClaudeCodeArgs("git_add", map[string]any{"paths": tc.single})
		if strings.ContainsAny(rendered, "\x1b\a\r\n") || !strings.Contains(rendered, "hello") {
			t.Errorf("git_add display boundary did not safely preserve one %T path: %q", tc.single, rendered)
		}
	}
}

func TestRuntimeSummariesSanitizeControlsAndKeepCellBudgets(t *testing.T) {
	summary := summarizeSubAgentTask("You are a helper.\n"+hostileDisplayMetadata+" fix the issue", "agent\x1b]0;x\a")
	assertNoTerminalEnvelope(t, summary)
	if strings.Contains(summary, "\n") || !strings.Contains(summary, "hello") {
		t.Fatalf("sub-agent summary is not a safe one-row description: %q", summary)
	}
	tabbedSummary := summarizeSubAgentTask("inspect\t/path without changing it", "explore")
	if strings.ContainsAny(tabbedSummary, "\t\r\n") || !strings.Contains(tabbedSummary, "inspect /path") {
		t.Fatalf("sub-agent summary retained cursor-moving controls: %q", tabbedSummary)
	}

	detail := strings.Repeat("请求👍🏽", 80) + hostileDisplayMetadata
	got := buildClaudeCodeArgs("custom", map[string]any{"query": detail})
	if width := lipgloss.Width(got); width > 120 {
		t.Fatalf("general tool args rendered %d cells, want <=120: %q", width, got)
	}
	assertNoTerminalEnvelope(t, got)

	body := failureBody("first\n+ "+hostileDisplayMetadata, "edit\x1b]0;x\a")
	assertNoTerminalEnvelope(t, body)
	flatBody := strings.Join(strings.Fields(stripAnsi(body)), " ")
	if !strings.Contains(flatBody, "hello") || !strings.Contains(flatBody, "worldend") {
		t.Fatalf("failure body lost readable detail: %q", stripAnsi(body))
	}

	diff := renderEditDiffBody(&editDiffDisplay{Text: "+ " + hostileDisplayMetadata, Added: 1})
	assertNoTerminalEnvelope(t, diff)
	if !strings.Contains(stripAnsi(diff), "hello") {
		t.Fatalf("edit diff lost readable content: %q", stripAnsi(diff))
	}
}

func TestLegacyErrorFormatterSanitizesRuntimeText(t *testing.T) {
	got := DefaultStyles().FormatErrorWithSuggestion(
		hostileDisplayMetadata,
		"retry\x1b]0;suggestion\a now",
		"code\r\nnext",
	)
	assertNoTerminalEnvelope(t, got)
	plain := stripAnsi(got)
	for _, want := range []string{"hello world end", "retry now", "code next"} {
		if !strings.Contains(plain, want) {
			t.Errorf("sanitized error missing %q: %q", want, plain)
		}
	}
}

func TestToolAndAgentLineFormattersSanitizeOneRowMetadata(t *testing.T) {
	styles := DefaultStyles()
	rows := []string{
		styles.FormatToolCall(hostileDisplayMetadata),
		styles.FormatToolCallWithArgs(hostileDisplayMetadata, map[string]any{"query": hostileDisplayMetadata}),
		styles.FormatToolExecuting(hostileDisplayMetadata, map[string]any{"query": hostileDisplayMetadata}),
		styles.FormatToolSuccess(hostileDisplayMetadata, time.Second),
		styles.FormatToolLine(hostileDisplayMetadata, hostileDisplayMetadata, hostileDisplayMetadata, time.Second),
		styles.FormatToolFailureLine(hostileDisplayMetadata, hostileDisplayMetadata, time.Second),
		styles.FormatToolError(hostileDisplayMetadata, errors.New(hostileDisplayMetadata)),
		styles.FormatToolError(hostileDisplayMetadata, nil),
		styles.FormatAgentToolLine(hostileDisplayMetadata, hostileDisplayMetadata, hostileDisplayMetadata, hostileDisplayMetadata, false),
		styles.FormatAgentComplete(hostileDisplayMetadata, time.Second),
		styles.FormatAgentFailed(hostileDisplayMetadata, time.Second),
	}
	for i, row := range rows {
		assertNoTerminalEnvelope(t, row)
		if strings.Contains(stripAnsi(row), "\n") {
			t.Errorf("formatter %d let metadata create another row: %q", i, stripAnsi(row))
		}
	}
}

func TestPlanAndMessageFormattersKeepOnlyStructuralControls(t *testing.T) {
	styles := DefaultStyles()
	outputs := []string{
		styles.FormatUserMessage(hostileDisplayMetadata),
		styles.FormatPlanStepHeader(1, 2, hostileDisplayMetadata),
		styles.FormatPlanStepResult(1, true, hostileDisplayMetadata),
		styles.FormatPlanStepResult(1, false, hostileDisplayMetadata),
		styles.FormatPlanBanner(hostileDisplayMetadata, true),
		styles.FormatMessage("info\x1b]0;x\a", hostileDisplayMetadata, hostileDisplayMetadata),
		styles.FormatHint(hostileDisplayMetadata),
	}
	for _, output := range outputs {
		assertNoTerminalEnvelope(t, output)
		if !strings.Contains(stripAnsi(output), "hello") {
			t.Errorf("sanitization lost readable metadata: %q", stripAnsi(output))
		}
	}
}

func TestToolOutputAndExecutionStatusSanitizeRuntimeControls(t *testing.T) {
	styles := DefaultStyles()
	model := NewToolOutputModel(styles)
	index := model.AddEntry("tool\x1b]0;x\a", hostileDisplayMetadata)
	entry := model.GetEntry(index)
	if entry == nil {
		t.Fatal("sanitized tool output entry missing")
	}
	assertNoTerminalEnvelope(t, entry.ToolName)
	assertNoTerminalEnvelope(t, entry.FullContent)
	if !strings.Contains(entry.FullContent, "hello") {
		t.Fatalf("tool output lost readable content: %q", entry.FullContent)
	}

	formatted := FormatToolOutput(hostileDisplayMetadata, 6, true)
	assertNoTerminalEnvelope(t, formatted)
	direct := model.RenderContent(hostileDisplayMetadata, true)
	assertNoTerminalEnvelope(t, direct)
	if !strings.Contains(stripAnsi(direct), "hello") {
		t.Fatalf("direct tool output lost readable content: %q", stripAnsi(direct))
	}

	renderer := NewExecutionStatusRenderer(styles)
	rows := []string{
		renderer.RenderValidation(hostileDisplayMetadata, []string{hostileDisplayMetadata}),
		renderer.RenderStart(hostileDisplayMetadata, hostileDisplayMetadata),
		renderer.RenderProgress(hostileDisplayMetadata, time.Second),
		renderer.RenderSuccess(hostileDisplayMetadata, time.Second),
		renderer.RenderError(hostileDisplayMetadata, hostileDisplayMetadata),
		renderer.RenderDenied(hostileDisplayMetadata, hostileDisplayMetadata),
		renderer.RenderApproved(hostileDisplayMetadata, hostileDisplayMetadata),
	}
	for _, row := range rows {
		assertNoTerminalEnvelope(t, row)
		if !strings.Contains(stripAnsi(row), "hello") {
			t.Errorf("execution status lost readable metadata: %q", stripAnsi(row))
		}
	}
}

func TestFormatToolOutputHonorsExactRowBudgetAndKeepsSignal(t *testing.T) {
	var source []string
	for i := 1; i <= 10; i++ {
		source = append(source, "line "+itoa(i))
	}
	content := strings.Join(source, "\n") + "\n"
	for budget := 0; budget <= 7; budget++ {
		got := FormatToolOutput(content, budget, false)
		if budget == 0 {
			if got != "" {
				t.Errorf("budget=0 rendered content: %q", got)
			}
			continue
		}
		if rows := len(strings.Split(got, "\n")); rows > budget {
			t.Errorf("budget=%d rendered %d rows:\n%s", budget, rows, stripAnsi(got))
		}
		plain := stripAnsi(got)
		if !strings.Contains(plain, "line 10") {
			t.Errorf("budget=%d lost the newest outcome row: %q", budget, plain)
		}
		if budget >= 2 && !strings.Contains(plain, "line 1") {
			t.Errorf("budget=%d lost the cause row: %q", budget, plain)
		}
		if budget >= 3 && !strings.Contains(plain, "more line") {
			t.Errorf("budget=%d hid truncation disclosure: %q", budget, plain)
		}
	}

	expanded := FormatToolOutput(content, 0, true)
	if strings.HasSuffix(expanded, "\n") || !strings.Contains(expanded, "line 1\n") {
		t.Fatalf("expanded output should keep content but drop decorative trailing rows: %q", expanded)
	}
}

func TestToolOutputModelHonorsCollapsedRowAndCellBudgets(t *testing.T) {
	var source []string
	for i := 1; i <= 12; i++ {
		source = append(source, "line "+itoa(i))
	}
	content := strings.Join(source, "\n") + "\n"
	for budget := 0; budget <= 7; budget++ {
		model := NewToolOutputModel(DefaultStyles())
		model.SetConfig(ToolOutputConfig{
			MaxCollapsedLines: budget,
			MaxCollapsedChars: 1,
			HeadRatio:         0.66,
		})
		got := model.renderTruncated(content)
		if budget == 0 {
			if got != "" {
				t.Errorf("budget=0 rendered content: %q", got)
			}
			continue
		}
		if rows := len(strings.Split(got, "\n")); rows > budget {
			t.Errorf("budget=%d rendered %d rows:\n%s", budget, rows, stripAnsi(got))
		}
		plain := stripAnsi(got)
		if !strings.Contains(plain, "line 12") {
			t.Errorf("budget=%d lost newest outcome: %q", budget, plain)
		}
		if budget >= 2 && !strings.Contains(plain, "line 1") {
			t.Errorf("budget=%d lost first cause: %q", budget, plain)
		}
		if budget >= 3 && !strings.Contains(plain, "more line") {
			t.Errorf("budget=%d hid truncation disclosure: %q", budget, plain)
		}
	}

	wide := strings.Repeat("请求👍🏽", 80)
	model := NewToolOutputModel(DefaultStyles())
	model.SetConfig(ToolOutputConfig{MaxCollapsedLines: 3, MaxCollapsedChars: 1, HeadRatio: 0.5})
	got := model.renderTruncated(wide + "\ncontext\nnewest")
	for row, line := range strings.Split(stripAnsi(got), "\n") {
		if cells := lipgloss.Width(line); cells > 100 {
			t.Errorf("row=%d rendered %d cells: %q", row, cells, line)
		}
		if strings.Contains(line, "👍") && !strings.Contains(line, "👍🏽") {
			t.Errorf("row=%d split emoji modifier cluster: %q", row, line)
		}
	}
}

func TestToolOutputTruncationThresholdUsesNormalizedGraphemes(t *testing.T) {
	model := NewToolOutputModel(DefaultStyles())
	model.SetConfig(ToolOutputConfig{
		MaxCollapsedLines: 10,
		MaxCollapsedChars: 500,
		HeadRatio:         0.66,
	})

	tenLines := strings.TrimSuffix(strings.Repeat("line\n", 10), "\n")
	if model.NeedsTruncation(tenLines + "\n") {
		t.Fatal("decorative trailing newline must not turn ten rows into eleven")
	}
	if !model.NeedsTruncation(tenLines + "\nextra\n") {
		t.Fatal("an actual eleventh row must cross the row threshold")
	}

	if model.NeedsTruncation(strings.Repeat("👍🏽", 500)) {
		t.Fatal("500 user-perceived characters should fit the configured threshold")
	}
	if !model.NeedsTruncation(strings.Repeat("👍🏽", 501)) {
		t.Fatal("501 user-perceived characters should cross the configured threshold")
	}

	styled := "\x1b[31m" + strings.Repeat("x", 500) + "\x1b[0m"
	if model.NeedsTruncation(styled) {
		t.Fatal("ANSI styling bytes must not count as user-visible content")
	}
}

func TestToolOutputExpandsTabsToDeterministicCellStops(t *testing.T) {
	got := safeToolOutputDisplayText("a\tb\n请求\tc")
	if strings.Contains(got, "\t") {
		t.Fatalf("raw tab reached tool output: %q", got)
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 2 || lines[0] != "a   b" || lines[1] != "请求    c" {
		t.Fatalf("unexpected four-cell tab expansion: %#v", lines)
	}
	for _, line := range lines {
		if !strings.HasSuffix(line, "b") && !strings.HasSuffix(line, "c") {
			t.Fatalf("tab expansion lost visible payload: %q", line)
		}
	}
}
