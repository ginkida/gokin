package tasks

import (
	"strings"
	"testing"

	"gokin/internal/workspace"
)

func TestWorkspaceCompletionPanicRetainsPartialEvidence(t *testing.T) {
	delta := completeWorkspaceWrites(func(bool) workspace.Delta { panic("broken observer") }, true)
	if !delta.Partial || !strings.Contains(delta.Warning, "panicked") || len(delta.Paths) != 0 {
		t.Fatalf("observer panic lost completion evidence: %+v", delta)
	}
}
