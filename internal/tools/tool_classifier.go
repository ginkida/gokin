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

// ToolGroup is a batch of tool calls to execute together: when Parallel is true
// the calls have no inter-dependencies and may run concurrently; otherwise they
// run sequentially. Shared by BOTH agentic loops' classifiers (the foreground
// executor and the sub-agent), so the read/write grouping algorithm — which is
// correctness-critical (a write must never run in parallel with reads) — lives
// in exactly one place. The write-SET (IsWriteTool) was already unified after it
// drifted (v0.100.6); the grouping ALGORITHM is the same drift surface, now also
// single-sourced (Tier-4).
type ToolGroup struct {
	Calls    []*genai.FunctionCall
	Parallel bool
}

// ClassifyToolDependencies groups tool calls by read/write dependency. A run of
// consecutive read-only tools becomes one group (Parallel when it holds more
// than one call — the mid-run flush before a write marks a single read Parallel
// too, matching the long-standing contract); every write tool (IsWriteTool) gets
// its own sequential group, flushing any pending read group first so a write
// never executes alongside reads. Empty or single input returns one
// non-parallel group (callers only pass non-empty batches; this preserves the
// executor's tested empty-input behavior).
func ClassifyToolDependencies(calls []*genai.FunctionCall) []ToolGroup {
	if len(calls) <= 1 {
		return []ToolGroup{{Calls: calls, Parallel: false}}
	}

	var groups []ToolGroup
	var readGroup []*genai.FunctionCall

	for _, call := range calls {
		if IsWriteTool(call.Name) {
			if len(readGroup) > 0 {
				groups = append(groups, ToolGroup{Calls: readGroup, Parallel: true})
				readGroup = nil
			}
			groups = append(groups, ToolGroup{Calls: []*genai.FunctionCall{call}, Parallel: false})
		} else {
			readGroup = append(readGroup, call)
		}
	}

	if len(readGroup) > 0 {
		groups = append(groups, ToolGroup{Calls: readGroup, Parallel: len(readGroup) > 1})
	}

	return groups
}

var defaultClassifier = &toolDependencyClassifier{
	writeTools: map[string]bool{
		"write":       true,
		"edit":        true,
		"bash":        true,
		"delete":      true,
		"move":        true,
		"copy":        true,
		"mkdir":       true,
		"git_commit":  true,
		"git_add":     true,
		"ssh":         true,
		"run_tests":   true,
		"batch":       true,
		"refactor":    true,
		"atomicwrite": true,
	},
}

// IsWriteTool reports whether a tool modifies state and must run SEQUENTIALLY
// (never in parallel with reads or other writes). This is the SINGLE source of
// truth for write-vs-read classification: the sub-agent classifier
// (internal/agent) delegates here so the two can't drift. They DID drift —
// run_tests/batch/refactor/atomicwrite were missing from the agent copy, which
// let sub-agents parallelize a refactor/batch against concurrent reads.
func IsWriteTool(name string) bool {
	return defaultClassifier.writeTools[name]
}

// classifyDependencies delegates to the shared ClassifyToolDependencies so the
// executor and the sub-agent classifier share one grouping algorithm. Kept as a
// method so defaultClassifier-based call sites (and tests) read unchanged.
func (c *toolDependencyClassifier) classifyDependencies(calls []*genai.FunctionCall) []ToolGroup {
	return ClassifyToolDependencies(calls)
}
