package tools

import "google.golang.org/genai"

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
