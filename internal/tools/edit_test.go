package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
)

// TestEditResultIncludesUpdatedRegion pins the weak-model tuning fix: every
// edit success carries the freshly-written region with line numbers, so a
// verification read after edit is unnecessary (the read→edit→re-read cycle is
// the classic weak-model loop — the v0.86.7 incident class).
func TestEditResultIncludesUpdatedRegion(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	path := filepath.Join(dir, "f.go")
	content := "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{
		"file_path":  path,
		"old_string": "l5",
		"new_string": "l5-changed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("edit failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "no verification read needed") {
		t.Fatalf("result missing updated-region hint: %s", res.Content)
	}
	if !strings.Contains(res.Content, "l5-changed") {
		t.Fatalf("result missing the new content: %s", res.Content)
	}
	// Line numbers around the change (5±4 window).
	if !strings.Contains(res.Content, "5\tl5-changed") {
		t.Fatalf("snippet missing numbered changed line: %s", res.Content)
	}

	// Line-range mode carries the snippet too.
	res, err = tool.Execute(context.Background(), map[string]any{
		"file_path":  path,
		"line_start": float64(2),
		"line_end":   float64(3),
		"new_string": "replaced-block",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success || !strings.Contains(res.Content, "replaced-block") || !strings.Contains(res.Content, "no verification read needed") {
		t.Fatalf("line-range result missing snippet: success=%v %s", res.Success, res.Content)
	}
}
