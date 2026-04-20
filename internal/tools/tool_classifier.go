package tools

import (
	"time"

	"google.golang.org/genai"
)

// parallelSerializationSuccessThreshold is the success rate under which a
// tool is considered unreliable enough to serialize the whole group it's in.
// Set at 0.5 (50%) so a batch of 10 reads isn't derailed by a single tool
// that's passing 60% of the time, but is downgraded when something is
// genuinely broken.
const parallelSerializationSuccessThreshold = 0.5

// shouldSerializeGroup reports whether a parallel group should be downgraded
// to sequential execution based on observed per-tool reliability.
// statsLookup is called once per unique tool name in the group; tools with
// not-enough-samples (ok=false) don't count against reliability.
func shouldSerializeGroup(calls []*genai.FunctionCall, statsLookup func(string) (time.Duration, float64, bool)) bool {
	if statsLookup == nil || len(calls) <= 1 {
		return false
	}
	seen := make(map[string]bool, len(calls))
	for _, c := range calls {
		if seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		if _, rate, ok := statsLookup(c.Name); ok && rate < parallelSerializationSuccessThreshold {
			return true
		}
	}
	return false
}

// toolDependencyClassifier determines which tools can run in parallel.
type toolDependencyClassifier struct {
	writeTools map[string]bool
}

// toolGroup represents a group of tool calls that can be executed together.
type toolGroup struct {
	Calls    []*genai.FunctionCall
	Parallel bool
}

var defaultClassifier = &toolDependencyClassifier{
	writeTools: map[string]bool{
		"write":      true,
		"edit":       true,
		"bash":       true,
		"delete":     true,
		"move":       true,
		"copy":       true,
		"mkdir":      true,
		"git_commit": true,
		"git_add":    true,
		"ssh":        true,
		"run_tests":  true,
	},
}

// classifyDependencies groups tool calls by read/write dependency.
// Consecutive read-only tools get Parallel=true; write tools get their own group.
func (c *toolDependencyClassifier) classifyDependencies(calls []*genai.FunctionCall) []toolGroup {
	if len(calls) <= 1 {
		return []toolGroup{{Calls: calls, Parallel: false}}
	}

	var groups []toolGroup
	var readGroup []*genai.FunctionCall

	for _, call := range calls {
		if c.writeTools[call.Name] {
			if len(readGroup) > 0 {
				groups = append(groups, toolGroup{Calls: readGroup, Parallel: true})
				readGroup = nil
			}
			groups = append(groups, toolGroup{Calls: []*genai.FunctionCall{call}, Parallel: false})
		} else {
			readGroup = append(readGroup, call)
		}
	}

	if len(readGroup) > 0 {
		groups = append(groups, toolGroup{Calls: readGroup, Parallel: len(readGroup) > 1})
	}

	return groups
}
