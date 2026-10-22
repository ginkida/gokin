package ui

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func BenchmarkTranscriptSearchDenseToolLine(b *testing.B) {
	for _, count := range []int{2000, 20000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			t := transcriptView{conversation: strings.Repeat("needle ", count)}
			t.reflow(100)
			t.query = "needle"
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				t.findMatches()
			}
		})
	}
}

func TestTranscriptHighlightsExactSearchResultAndPreservesGraphemes(t *testing.T) {
	original := lipgloss.DefaultRenderer()
	renderer := lipgloss.NewRenderer(io.Discard)
	renderer.SetColorProfile(termenv.TrueColor)
	lipgloss.SetDefaultRenderer(renderer)
	t.Cleanup(func() { lipgloss.SetDefaultRenderer(original) })
	for _, tc := range []struct {
		text, query string
		selected    int
		visible     string
	}{
		{"needle needle", "needle", 0, "needle"},
		{"needle needle", "needle", 1, "needle"},
		{"abc👩🏽‍💻 end", "💻", 0, "👩🏽‍💻"},
		{"á́ end", "́", 1, "á́"},
	} {
		v := transcriptView{conversation: tc.text, query: tc.query}
		v.reflow(80)
		v.matchIndex = tc.selected
		got := v.highlightRow(0)
		selected := lipgloss.NewStyle().Foreground(ColorBg).Background(ColorWarning).Bold(true).Render(tc.visible)
		if stripAnsi(got) != tc.text || !strings.Contains(got, selected) || lipgloss.Width(got) != lipgloss.Width(tc.text) {
			t.Fatalf("search styling changed content or missed selection: %q", got)
		}
		if tc.text == "needle needle" && tc.selected == 1 && !strings.HasSuffix(got, selected) {
			t.Fatalf("second occurrence was not selected: %q", got)
		}
	}
	// A match split over visual rows must be highlighted on both sides.
	v := transcriptView{conversation: "123456TARGET PHRASE", query: "target phrase"}
	v.reflow(10)
	for row := 0; row < 3; row++ {
		if !strings.Contains(v.highlightRow(row), "\x1b[") {
			t.Fatalf("wrapped match row %d has no highlight", row)
		}
	}
	m := claudeComposer(t)
	m.transcript = &transcriptView{conversation: "needle needle", query: "needle"}
	m.transcript.reflow(m.width)
	m.transcript.jumpMatch(1)
	if !strings.Contains(stripAnsi(m.renderTranscript()), "2/2 matches") {
		t.Fatal("search footer does not identify the selected occurrence")
	}
}

func TestTranscriptSearchMapsUnicodeMatchesToWrappedSourceRows(t *testing.T) {
	for _, tc := range []struct {
		text, query string
		width, row  int
	}{
		{"12345678TARGET", "target", 10, 1},
		{"界界😀needle", "needle", 8, 1},
		{"áááneedle", "needle", 5, 1},
		{"abc👩🏽‍💻needle", "💻", 6, 1},
		{"12345Kelvin", "kelvin", 7, 1},
		{"123456TARGET PHRASE", "target phrase", 10, 0},
	} {
		t.Run(tc.text, func(t *testing.T) {
			v := transcriptView{conversation: tc.text, query: tc.query}
			v.reflow(tc.width)
			if len(v.matches) != 1 || v.matches[0] != tc.row {
				t.Fatalf("matches=%v, want row %d; rows=%q", v.matches, tc.row, v.lines)
			}
		})
	}
}

func TestTranscriptSearchDenseToolOutputRetainsEveryOccurrence(t *testing.T) {
	v := transcriptView{conversation: strings.Repeat("needle ", 20000), query: "needle"}
	v.reflow(100)
	if len(v.matches) != 20000 {
		t.Fatalf("lost search results: %d", len(v.matches))
	}
	for i, row := range v.matches {
		if row != i*7/98 {
			t.Fatalf("match %d maps to row %d", i, row)
		}
	}
}

func TestTranscriptResizePreservesReaderSourcePosition(t *testing.T) {
	var content strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&content, "row %03d %s\n", i, strings.Repeat("abcdef", 20))
	}
	v := transcriptView{conversation: content.String()}
	v.reflow(40)
	v.offset = v.logicalRows[40] + 2
	anchor := 2 * 38
	v.reflow(80)
	if v.offset != v.logicalRows[40]+anchor/78 {
		t.Fatalf("widening moved away from source line 40: offset %d, line starts %v", v.offset, v.logicalRows[40:42])
	}
	v.offset = v.logicalRows[63] + 1
	v.reflow(24)
	if v.offset != v.logicalRows[63]+78/22 {
		t.Fatalf("narrowing moved away from source line 63: offset %d", v.offset)
	}
}
