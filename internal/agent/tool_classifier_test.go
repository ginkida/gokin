package agent

import (
	"testing"

	"google.golang.org/genai"
)

// TestToolDependencyClassifier_NoDriftFromForeground pins the fix for the
// classifier-drift bug: the sub-agent write-tool set had dropped
// run_tests/batch/refactor/atomicwrite, so sub-agents could run a refactor/batch
// in parallel with reads. It now delegates to the single shared set in
// internal/tools, so the four are classified write again.
func TestToolDependencyClassifier_NoDriftFromForeground(t *testing.T) {
	c := NewToolDependencyClassifier()

	for _, name := range []string{"run_tests", "batch", "refactor", "atomicwrite", "edit", "write", "bash"} {
		if !c.IsWriteTool(name) {
			t.Errorf("%q must be classified as a write tool", name)
		}
	}
	for _, name := range []string{"read", "grep", "glob", "list_dir"} {
		if c.IsWriteTool(name) {
			t.Errorf("%q must NOT be a write tool", name)
		}
	}

	// A refactor must land in its own sequential group, never parallel with reads.
	groups := c.ClassifyDependencies([]*genai.FunctionCall{
		{Name: "read"}, {Name: "refactor"}, {Name: "grep"},
	})
	for _, g := range groups {
		if !g.Parallel {
			continue
		}
		for _, call := range g.Calls {
			if c.IsWriteTool(call.Name) {
				t.Errorf("write tool %q must not be in a PARALLEL group", call.Name)
			}
		}
	}
}
