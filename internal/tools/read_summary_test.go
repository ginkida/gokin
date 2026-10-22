package tools

import (
	"context"
	"strings"
	"testing"
)

func TestSummaryResourceReadPagesAllLongTextAndRejectsOtherResources(t *testing.T) {
	read := NewReadTool(t.TempDir())
	id := strings.Repeat("a", 32)
	calls := 0
	read.SetSummaryReader(func(_ context.Context, got string) (string, error) {
		calls++
		if got != id {
			t.Fatal("wrong archive requested")
		}
		return strings.Repeat("界", 2100) + "tail-original", nil
	})
	result, err := read.Execute(t.Context(), map[string]any{"file_path": "gokin://summary/" + id, "offset": 3, "limit": 1})
	if err != nil || !result.Success || !strings.Contains(result.Content, "tail-original") {
		t.Fatalf("long original text cannot be paged: %+v %v", result, err)
	}
	for _, uri := range []string{"gokin://summary/../../outside", "gokin://summary/" + strings.Repeat("x", 32), "gokin://other/" + id} {
		if result, err := read.Execute(t.Context(), map[string]any{"file_path": uri}); err != nil || result.Success {
			t.Fatal("invalid resource accepted", uri)
		}
	}
	if calls != 1 {
		t.Fatal("invalid resources reached session reader")
	}
}
func TestClonedReadCannotSeeParentSummaryArchive(t *testing.T) {
	work := t.TempDir()
	parent := NewReadTool(work)
	parent.SetSummaryReader(func(context.Context, string) (string, error) { return "private parent conversation", nil })
	cloned := CloneToolForWorkDir(parent, work)
	result, err := cloned.Execute(t.Context(), map[string]any{"file_path": "gokin://summary/" + strings.Repeat("b", 32)})
	if err != nil || result.Success || strings.Contains(result.Content, "private parent") {
		t.Fatal("child received parent transcript", result, err)
	}
}
