package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
)

func seedEditTarget(t *testing.T, content string) (*EditTool, string) {
	t.Helper()
	dir := testkit.ResolvedTempDir(t)
	target := filepath.Join(dir, "code.go")
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	et := NewEditTool(dir)
	// Register prior read so the read-before-edit invariant doesn't
	// short-circuit (we're testing ambiguity, not that invariant).
	tracker := NewFileReadTracker()
	tracker.CheckAndRecord(target, 1, 100, len(content))
	et.SetReadTracker(tracker)
	et.SetRequireReadBeforeEdit(true)
	return et, target
}

func TestEditTool_AmbiguousMatchReportsLinesAndContext(t *testing.T) {
	content := "package x\n\nfunc Foo() {}\nfunc Bar() {\n\treturn Foo()\n}\nfunc Baz() {\n\treturn Foo()\n}\n"
	et, target := seedEditTarget(t, content)

	result, err := et.Execute(context.Background(), map[string]any{
		"file_path":  target,
		"old_string": "Foo()",
		"new_string": "Foo2()",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Fatal("3-way match must not succeed without replace_all")
	}

	// Lines 3 (Foo() def), 5 (return Foo() in Bar), 8 (return Foo() in Baz).
	for _, needle := range []string{"line", "3", "5", "8"} {
		if !strings.Contains(result.Error, needle) {
			t.Errorf("expected %q in error: %s", needle, result.Error)
		}
	}
	if !strings.Contains(result.Error, "→") {
		t.Errorf("expected arrow marker on target line: %s", result.Error)
	}
	if !strings.Contains(result.Error, "line-range mode") && !strings.Contains(result.Error, "line_start") {
		t.Errorf("error must propose line-range mode as a recovery path: %s", result.Error)
	}
}

func TestEditTool_RegexAmbiguityShowsLineNumbers(t *testing.T) {
	content := "a = 1\nb = 2\nc = 3\nd = 4\n"
	et, target := seedEditTarget(t, content)

	// `(?m)` multi-line flag required so `^` and `$` match at line
	// boundaries, not only string boundaries. Without it the pattern
	// matches 0 times and we'd test the wrong error path.
	result, err := et.Execute(context.Background(), map[string]any{
		"file_path":  target,
		"old_string": `(?m)^\w = \d+$`,
		"new_string": "changed",
		"regex":      true,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Fatal("4-way regex match without replace_all must fail")
	}
	// Expect all four line numbers surfaced.
	for _, n := range []string{"1", "2", "3", "4"} {
		if !strings.Contains(result.Error, n) {
			t.Errorf("missing line %s in regex error: %s", n, result.Error)
		}
	}
	if !strings.Contains(result.Error, "regex pattern") {
		t.Errorf("expected 'regex pattern' label: %s", result.Error)
	}
}

func TestEditTool_AmbiguityCapsContextsAt5(t *testing.T) {
	// 7 identical lines so ambiguous match count exceeds the 5-cap.
	var lines []string
	for i := 0; i < 7; i++ {
		lines = append(lines, "hit = 1")
	}
	content := strings.Join(lines, "\n") + "\n"
	et, target := seedEditTarget(t, content)

	result, err := et.Execute(context.Background(), map[string]any{
		"file_path":  target,
		"old_string": "hit = 1",
		"new_string": "hit = 2",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Fatal("7-way match must fail")
	}
	if !strings.Contains(result.Error, "showing first 5 of 7") {
		t.Errorf("expected 'showing first 5 of 7' truncation marker: %s", result.Error)
	}
}

func TestFindStringOccurrenceLines_SingleLine(t *testing.T) {
	lines := []string{"foo", "bar foo", "baz"}
	hits := findStringOccurrenceLines(lines, "foo")
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d: %v", len(hits), hits)
	}
	if hits[0] != 1 || hits[1] != 2 {
		t.Errorf("expected lines 1,2; got %v", hits)
	}
}

func TestFindStringOccurrenceLines_MultilineSubstring(t *testing.T) {
	content := "a\nfoo\nbar\nc\nfoo\nbar\nd"
	lines := strings.Split(content, "\n")
	hits := findStringOccurrenceLines(lines, "foo\nbar")
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits for multi-line substring, got %d: %v", len(hits), hits)
	}
	if hits[0] != 2 || hits[1] != 5 {
		t.Errorf("expected lines 2,5; got %v", hits)
	}
}

func TestRenderLineContext_Bounds(t *testing.T) {
	lines := []string{"l1", "l2", "l3", "l4", "l5"}
	// target line 1 with 2 lines of context — should clamp start to 1.
	out := renderLineContext(lines, 1, 2)
	if !strings.Contains(out, "→ 1: l1") {
		t.Errorf("missing arrow on target: %s", out)
	}
	if strings.Contains(out, "0:") {
		t.Errorf("should not include non-existent line 0: %s", out)
	}
	// target line 5 (last) — should clamp end at 5.
	out = renderLineContext(lines, 5, 2)
	if !strings.Contains(out, "→ 5: l5") {
		t.Errorf("missing arrow on last line: %s", out)
	}
	if strings.Contains(out, "6:") {
		t.Errorf("should not include non-existent line 6: %s", out)
	}
}

func TestRenderLineContext_OutOfBounds(t *testing.T) {
	lines := []string{"only"}
	if out := renderLineContext(lines, 99, 2); out != "" {
		t.Errorf("out-of-range line must yield empty, got %q", out)
	}
	if out := renderLineContext(lines, 0, 2); out != "" {
		t.Errorf("line 0 must yield empty, got %q", out)
	}
}
