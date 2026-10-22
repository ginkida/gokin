package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDiffReviewKeepsWholeChangedLines(t *testing.T) {
	old := "package main\n\nfunc value() int {\n\treturn 10\n}\n"
	new := strings.Replace(old, "return 10", "return 20", 1)
	m := NewDiffPreviewModel(DefaultStyles())
	m.SetSize(60, 24)
	m.SetContent("sample.go", old, new, "edit", false)
	for _, want := range []string{"-\treturn 10\n", "+\treturn 20\n", "@@ -1,5 +1,5 @@"} {
		if !strings.Contains(m.diff, want) {
			t.Errorf("unified review missing complete line %q:\n%s", want, m.diff)
		}
	}
	multi := NewMultiDiffPreviewModel(DefaultStyles())
	multi.SetFiles([]DiffFile{{FilePath: "sample.go", OldContent: old, NewContent: new}})
	for _, want := range []string{"-\treturn 10\n", "+\treturn 20\n"} {
		if !strings.Contains(multi.files[0].Diff, want) {
			t.Errorf("multi-file review missing complete line %q:\n%s", want, multi.files[0].Diff)
		}
	}
}

func TestSplitReviewAlignsInsertionsDeletionsAndBlankLines(t *testing.T) {
	for _, tc := range []struct {
		name, old, new string
		changes        int
		aligned        string
	}{
		{"insert", "alpha\nbeta\ngamma\n", "alpha\ninserted\nbeta\ngamma\n", 1, "beta"},
		{"delete", "alpha\nremoved\nbeta\ngamma\n", "alpha\nbeta\ngamma\n", 1, "beta"},
		{"blank insert", "alpha\nbeta\n", "\nalpha\nbeta\n", 1, "alpha"},
		{"blank delete", "\nalpha\nbeta\n", "alpha\nbeta\n", 1, "alpha"},
		{"uneven replacement", "alpha\nold\nbeta\n", "alpha\nnew\nextra\nbeta\n", 2, "beta"},
		{"empty new file", "", "\n", 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewDiffPreviewModel(DefaultStyles())
			m.SetSize(100, 24)
			m.SetContent("sample.txt", tc.old, tc.new, "edit", tc.old == "")
			split := stripAnsi(m.renderSideBySide())
			if len(m.changeOffsets) != tc.changes {
				t.Errorf("want %d changed rows, got %d:\n%s", tc.changes, len(m.changeOffsets), split)
			}
			if tc.aligned != "" {
				found := false
				for _, line := range strings.Split(split, "\n") {
					if strings.Count(line, tc.aligned) == 2 && strings.TrimSpace(line[strings.LastIndex(line, " | ")+3:]) == "" {
						found = true
					}
				}
				if !found {
					t.Errorf("unchanged line %q must align across columns:\n%s", tc.aligned, split)
				}
			}
			// Approval always carries the original proposed bytes, including blank lines.
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
			if cmd == nil || cmd().(DiffPreviewResponseMsg).NewContent != tc.new {
				t.Fatal("review changed approval payload")
			}
		})
	}
}

func TestReviewShowsFinalNewlineChange(t *testing.T) {
	for _, width := range []int{60, 100} {
		m := NewDiffPreviewModel(DefaultStyles())
		m.SetSize(width, 24)
		m.SetContent("sample.txt", "same", "same\n", "edit", false)
		if !m.hasVisibleChanges() || !strings.Contains(stripAnsi(m.viewport.View()), "no newline") && !strings.Contains(stripAnsi(m.viewport.View()), "No newline") {
			t.Errorf("width %d hides newline-only edit:\n%s", width, stripAnsi(m.View()))
		}
	}
}

func TestReviewCountsBlankAndHeaderLikeSourceLines(t *testing.T) {
	for _, tc := range []struct {
		old, new      string
		adds, removes int
	}{
		{"", "\n", 1, 0},
		{"\n", "", 0, 1},
		{"same", "same\n", 1, 1},
		{"-- source\n", "++ source\n", 1, 1},
	} {
		adds, removes := countDiffLines(tc.old, tc.new)
		if adds != tc.adds || removes != tc.removes {
			t.Errorf("inline counts %d/%d, want %d/%d", adds, removes, tc.adds, tc.removes)
		}
		m := NewDiffPreviewModel(DefaultStyles())
		m.SetSize(60, 24)
		m.SetContent("sample.txt", tc.old, tc.new, "edit", false)
		adds, removes = m.countChanges()
		if adds != tc.adds || removes != tc.removes || !m.hasVisibleChanges() || len(m.changeOffsets) != tc.adds+tc.removes {
			t.Errorf("unified review lost source lines: %d/%d, offsets %v:\n%s", adds, removes, m.changeOffsets, m.diff)
		}
	}
}

func TestDiffReviewHunkNumbersForEmptySides(t *testing.T) {
	for _, tc := range []struct{ old, new, header string }{
		{"", "added\n", "@@ -0,0 +1,1 @@"},
		{"removed\n", "", "@@ -1,1 +0,0 @@"},
		{"alpha\nbeta\n", "alpha\nadded\nbeta\n", "@@ -1,0 +2,1 @@"},
		{"alpha\nremoved\nbeta\n", "alpha\nbeta\n", "@@ -2,1 +1,0 @@"},
	} {
		m := NewDiffPreviewModel(DefaultStyles())
		m.contextLines = 0
		m.SetContent("sample.txt", tc.old, tc.new, "edit", false)
		if !strings.Contains(m.diff, tc.header) {
			t.Errorf("missing %s:\n%s", tc.header, m.diff)
		}
	}
}
