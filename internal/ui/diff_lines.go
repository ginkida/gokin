package ui

import (
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

func reviewHeader(line string, index int) bool {
	return index < 2 && (strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ "))
}

// reviewLines diffs line identities before expanding them back to text. A
// character diff with checklines=true still refines edits within a line; those
// fragments must never be presented as if they were complete source lines.
func reviewLines(oldContent, newContent string) []diffLine {
	dmp := diffmatchpatch.New()
	old, proposed, dictionary := dmp.DiffLinesToRunes(oldContent, newContent)
	changes := dmp.DiffCharsToLines(dmp.DiffMainRunes(old, proposed, false), dictionary)
	var lines []diffLine
	for _, change := range changes {
		text := change.Text
		for text != "" {
			line, rest, terminated := strings.Cut(text, "\n")
			lines = append(lines, diffLine{Type: change.Type, Text: line, NoNewline: !terminated})
			text = rest
		}
	}
	return lines
}

// A zero line number represents an absent side, not an empty source line.
type splitReviewRow struct {
	old, new       diffLine
	oldNum, newNum int
}

func alignReviewLines(lines []diffLine) []splitReviewRow {
	var rows []splitReviewRow
	oldNum, newNum := 1, 1
	for i := 0; i < len(lines); {
		if lines[i].Type == diffmatchpatch.DiffEqual {
			rows = append(rows, splitReviewRow{lines[i], lines[i], oldNum, newNum})
			oldNum++
			newNum++
			i++
			continue
		}
		var removed, added []diffLine
		for i < len(lines) && lines[i].Type != diffmatchpatch.DiffEqual {
			if lines[i].Type == diffmatchpatch.DiffDelete {
				removed = append(removed, lines[i])
			} else {
				added = append(added, lines[i])
			}
			i++
		}
		for j := 0; j < max(len(removed), len(added)); j++ {
			var row splitReviewRow
			if j < len(removed) {
				row.old, row.oldNum = removed[j], oldNum
				oldNum++
			}
			if j < len(added) {
				row.new, row.newNum = added[j], newNum
				newNum++
			}
			rows = append(rows, row)
		}
	}
	return rows
}
