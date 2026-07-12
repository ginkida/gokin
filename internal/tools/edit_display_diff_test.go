package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildDisplayDiff_CountsAndHunks(t *testing.T) {
	oldC := "a\nb\nc\nd\ne\nf\ng\nh\n"
	newC := "a\nb\nc\nX\ne\nf\ng\nh\n" // one line replaced mid-file

	text, added, removed := buildDisplayDiff(oldC, newC)
	if added != 1 || removed != 1 {
		t.Fatalf("added/removed = %d/%d, want 1/1", added, removed)
	}
	if !strings.Contains(text, "- ") || !strings.Contains(text, "+ ") {
		t.Fatalf("diff must carry -/+ markers:\n%s", text)
	}
	if !strings.Contains(text, "X") || !strings.Contains(text, "d") {
		t.Fatalf("diff must show the old and new lines:\n%s", text)
	}
	// Context is bounded: far-away unchanged lines (a, h) must NOT render.
	if strings.Contains(text, "  1  a") || strings.Contains(text, "h") {
		t.Fatalf("far-away context leaked into the hunk:\n%s", text)
	}
}

func TestBuildDisplayDiff_CapDisclosed(t *testing.T) {
	var oldB, newB strings.Builder
	for i := 0; i < 200; i++ {
		oldB.WriteString("same\n")
		newB.WriteString("changed\n")
	}
	text, added, removed := buildDisplayDiff(oldB.String(), newB.String())
	if added != 200 || removed != 200 {
		t.Fatalf("counts must reflect the FULL diff: %d/%d", added, removed)
	}
	if !strings.Contains(text, "diff truncated (+200/-200 total)") {
		t.Fatalf("cap must be disclosed with full totals:\n%s", text)
	}
	if n := strings.Count(text, "\n"); n > maxDisplayDiffLines+2 {
		t.Fatalf("display must be capped, got %d lines", n)
	}
}

func TestEditDisplayData_NilCases(t *testing.T) {
	if d := editDisplayData("same", "same"); d != nil {
		t.Fatal("no-op edit must produce no display data")
	}
	huge := strings.Repeat("x\n", maxDisplayDiffBytes/2+1)
	if d := editDisplayData(huge, huge+"y\n"); d != nil {
		t.Fatal("oversized files must skip diff computation")
	}
}

// End-to-end: a real edit carries the display-diff payload on Data while the
// model-facing Content keeps the "Updated region" snippet.
func TestEditTool_Execute_CarriesDisplayDiff(t *testing.T) {
	dir := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(dir)
	path := filepath.Join(resolved, "f.go")
	if err := os.WriteFile(path, []byte("package x\n\nfunc a() {}\nfunc b() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditTool(resolved)
	res, err := tool.Execute(context.Background(), map[string]any{
		"file_path":  path,
		"old_string": "func a() {}",
		"new_string": "func a() {\n\treturn\n}",
	})
	if err != nil || !res.Success {
		t.Fatalf("edit failed: %v / %+v", err, res)
	}

	d, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("edit result must carry display data, got %T", res.Data)
	}
	diff, _ := d["display_diff"].(string)
	if !strings.Contains(diff, "+") || !strings.Contains(diff, "return") {
		t.Fatalf("display_diff must show the added lines:\n%s", diff)
	}
	if d["diff_added"].(int) < 2 {
		t.Fatalf("diff_added = %v, want >= 2", d["diff_added"])
	}
	// The MODEL-facing content keeps the weak-model region snippet.
	if !strings.Contains(res.Content, "Updated region") {
		t.Fatalf("model-facing region snippet must survive:\n%s", res.Content)
	}
}
