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

// ToolGroup represents a group of tool calls that can be executed together.
type ToolGroup struct {
	Calls    []*genai.FunctionCall
	Parallel bool // If true, calls in this group can run in parallel
}

// ClassifyDependencies groups tool calls by dependency.
// Returns groups that should be executed sequentially, where each group's
// internal calls can be executed in parallel (if Parallel is true).
func (c *ToolDependencyClassifier) ClassifyDependencies(calls []*genai.FunctionCall) []ToolGroup {
	if len(calls) == 0 {
		return nil
	}

	if len(calls) == 1 {
		return []ToolGroup{{Calls: calls, Parallel: false}}
	}

	var groups []ToolGroup
	var currentReadGroup []*genai.FunctionCall

	for _, call := range calls {
		if c.IsWriteTool(call.Name) {
			// Flush any pending read-only tools as a parallel group
			if len(currentReadGroup) > 0 {
				groups = append(groups, ToolGroup{
					Calls:    currentReadGroup,
					Parallel: true,
				})
				currentReadGroup = nil
			}
			// Add write tool as its own sequential group
			groups = append(groups, ToolGroup{
				Calls:    []*genai.FunctionCall{call},
				Parallel: false,
			})
		} else {
			// Accumulate read-only tools
			currentReadGroup = append(currentReadGroup, call)
		}
	}

	// Flush remaining read-only group
	if len(currentReadGroup) > 0 {
		groups = append(groups, ToolGroup{
			Calls:    currentReadGroup,
			Parallel: len(currentReadGroup) > 1,
		})
	}

	return groups
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
