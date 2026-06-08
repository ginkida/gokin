package tools

import (
	"context"
	"os"
	"testing"
)

// These pin the trailing-newline correctness of the line-range and insert edit
// modes — both modify files directly and splice via strings.Split/"\n", where a
// dropped or added newline silently corrupts the file. Previously untested.

func TestEditTool_LineRangeApply(t *testing.T) {
	cases := []struct {
		name string
		seed string
		args map[string]any
		want string
	}{
		// Replacing through the last real line keeps the trailing newline (the
		// phantom split element after the final \n is appended back).
		{"replace last real line keeps trailing nl", "a\nb\nc\n",
			map[string]any{"line_start": 2, "line_end": 3, "new_string": "X"}, "a\nX\n"},
		{"replace middle, multi-line new, keeps trailing nl", "a\nb\nc\nd\n",
			map[string]any{"line_start": 2, "line_end": 3, "new_string": "B\nC"}, "a\nB\nC\nd\n"},
		{"replace last line of file without trailing nl", "a\nb\nc",
			map[string]any{"line_start": 3, "line_end": 3, "new_string": "C"}, "a\nb\nC"},
		{"delete a range (empty new_string)", "a\nb\nc\nd\n",
			map[string]any{"line_start": 2, "line_end": 3, "new_string": ""}, "a\nd\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			et, target := seedEditTarget(t, c.seed)
			c.args["file_path"] = target
			runEditOK(t, et, c.args)
			if got := readFileBack(t, target); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestEditTool_InsertAfterLineApply(t *testing.T) {
	cases := []struct {
		name string
		seed string
		args map[string]any
		want string
	}{
		{"append after last line keeps trailing nl", "a\nb\nc\n",
			map[string]any{"insert_after_line": 3, "new_string": "d"}, "a\nb\nc\nd\n"},
		{"prepend (after line 0)", "a\nb\nc\n",
			map[string]any{"insert_after_line": 0, "new_string": "X"}, "X\na\nb\nc\n"},
		{"insert in the middle", "a\nb\nc\n",
			map[string]any{"insert_after_line": 1, "new_string": "X"}, "a\nX\nb\nc\n"},
		{"append to file without trailing nl", "a\nb\nc",
			map[string]any{"insert_after_line": 3, "new_string": "d"}, "a\nb\nc\nd"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			et, target := seedEditTarget(t, c.seed)
			c.args["file_path"] = target
			runEditOK(t, et, c.args)
			if got := readFileBack(t, target); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func runEditOK(t *testing.T, et *EditTool, args map[string]any) {
	t.Helper()
	res, err := et.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.Success {
		t.Fatalf("edit did not succeed: %s", res.Error)
	}
}

func readFileBack(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	return string(b)
}
