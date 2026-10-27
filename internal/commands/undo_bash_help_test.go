package commands

import (
	"strings"
	"testing"
)

func TestUndoHelpDisclosesShellWrites(t *testing.T) {
	usage := (&UndoCommand{}).Usage()
	if !strings.Contains(usage, "bash") || !strings.Contains(usage, "not undoable") {
		t.Fatalf("undo help hides shell write limitation: %s", usage)
	}
}
