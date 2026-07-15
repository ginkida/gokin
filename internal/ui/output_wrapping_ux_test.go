package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func assertWrappedOutputFitsAndPreserves(t *testing.T, wrapped, original string, width int) {
	t.Helper()
	plain := stripAnsi(wrapped)
	var rebuilt strings.Builder
	for row, line := range strings.Split(plain, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("wrapped row %d width=%d, want <=%d: %q", row, got, width, line)
		}
		rebuilt.WriteString(strings.TrimRight(line, " "))
	}
	if got := rebuilt.String(); got != original {
		t.Fatalf("wrapping changed streamed content:\n got %q\nwant %q\nrendered:\n%s", got, original, plain)
	}
}

func TestOutputRewrapsIncompleteLineAcrossStreamChunks(t *testing.T) {
	output := NewOutputModel(DefaultStyles())
	output.SetSize(30, 6) // content width = 26
	first := "abcdefghijklmnopqrstuvwx"
	second := "YZ0123456789"

	output.AppendText(first)
	output.AppendText(second)

	assertWrappedOutputFitsAndPreserves(t, output.state.cachedWrapped, first+second, 26)
}

func TestOutputWrapUsesTerminalCellsForWideUnicode(t *testing.T) {
	output := NewOutputModel(DefaultStyles())
	output.SetSize(30, 6) // content width = 26
	first := strings.Repeat("界", 10)
	second := strings.Repeat("面", 10)

	output.AppendText(first)
	output.AppendText(second)

	assertWrappedOutputFitsAndPreserves(t, output.state.cachedWrapped, first+second, 26)
}

func TestOutputWrapsEveryWidthWithASCIIWideUnicodeAndANSI(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{name: "ascii", text: strings.Repeat("a", 55), want: strings.Repeat("a", 55)},
		{name: "wide unicode", text: strings.Repeat("界", 30), want: strings.Repeat("界", 30)},
		{name: "ansi", text: "\x1b[31m" + strings.Repeat("r", 55) + "\x1b[0m", want: strings.Repeat("r", 55)},
	}
	for _, width := range []int{10, 20, 21, 40} {
		for _, tc := range cases {
			t.Run(tc.name+"/"+itoa(width), func(t *testing.T) {
				assertWrappedOutputFitsAndPreserves(t, wrapText(tc.text, width), tc.want, width)
			})
		}
	}
}

func TestOutputModelWrapsNarrowIncompleteChunks(t *testing.T) {
	output := NewOutputModel(DefaultStyles())
	output.SetSize(14, 6) // content width = 10
	first := strings.Repeat("界", 4)
	second := strings.Repeat("面", 4)

	output.AppendText(first)
	output.AppendText(second)

	assertWrappedOutputFitsAndPreserves(t, output.state.cachedWrapped, first+second, 10)
}
