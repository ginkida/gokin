package agent

import (
	"google.golang.org/genai"

	"gokin/internal/tools"
)

// ToolDependencyClassifier determines which tools can run in parallel.
type ToolDependencyClassifier struct{}

// NewToolDependencyClassifier creates a new classifier.
func NewToolDependencyClassifier() *ToolDependencyClassifier {
	return &ToolDependencyClassifier{}
}

// IsWriteTool returns true if the tool modifies state. Delegates to the SINGLE
// shared write-tool set in internal/tools so the foreground (executor) and
// sub-agent classifiers can never drift — they previously did, dropping
// run_tests/batch/refactor/atomicwrite here and letting sub-agents run a
// refactor/batch in parallel with reads.
func (c *ToolDependencyClassifier) IsWriteTool(name string) bool {
	return tools.IsWriteTool(name)
}

// ClassifyDependencies delegates to the shared tools.ClassifyToolDependencies so
// the sub-agent and the foreground executor share ONE grouping algorithm — a
// write never lands in a parallel group with reads, and the two loops can't
// drift (they did once, on the write-set). Returns groups to run sequentially,
// each group's calls runnable in parallel when Parallel is true.
func (c *ToolDependencyClassifier) ClassifyDependencies(calls []*genai.FunctionCall) []tools.ToolGroup {
	return tools.ClassifyToolDependencies(calls)
}
