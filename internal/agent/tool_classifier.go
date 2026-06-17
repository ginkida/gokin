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

// OptimizeForParallelism reorders calls to maximize parallel execution.
// Moves all read-only calls to the front, then write calls.
func (c *ToolDependencyClassifier) OptimizeForParallelism(calls []*genai.FunctionCall) []*genai.FunctionCall {
	if len(calls) <= 1 {
		return calls
	}

	var readCalls, writeCalls []*genai.FunctionCall
	for _, call := range calls {
		if c.IsWriteTool(call.Name) {
			writeCalls = append(writeCalls, call)
		} else {
			readCalls = append(readCalls, call)
		}
	}

	// Return read calls first (can be parallel), then write calls (sequential)
	result := make([]*genai.FunctionCall, 0, len(calls))
	result = append(result, readCalls...)
	result = append(result, writeCalls...)
	return result
}
