package tools

import (
	"strings"
	"testing"
)

func TestTodoToolDeclaration_ExposesClaudeCodeContract(t *testing.T) {
	decl := TodoToolDeclaration()
	if decl == nil {
		t.Fatal("TodoToolDeclaration() returned nil")
	}

	for _, needle := range []string{"before editing", "full list", "exactly one", "in_progress"} {
		if !strings.Contains(decl.Description, needle) {
			t.Fatalf("todo declaration description missing %q: %q", needle, decl.Description)
		}
	}

	todos := decl.Parameters.Properties["todos"]
	if todos == nil {
		t.Fatal("todo declaration missing todos property")
	}
	for _, needle := range []string{"complete updated list", "replaces the full list", "Exactly one", "in_progress"} {
		if !strings.Contains(todos.Description, needle) {
			t.Fatalf("todos property description missing %q: %q", needle, todos.Description)
		}
	}

	status := todos.Items.Properties["status"]
	if status == nil {
		t.Fatal("todo declaration missing status property")
	}
	if !strings.Contains(status.Description, "exactly one in_progress") {
		t.Fatalf("status description should pin single in_progress invariant: %q", status.Description)
	}
}
