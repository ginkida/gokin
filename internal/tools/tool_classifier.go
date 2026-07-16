package tools

import (
	"strings"
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
type toolDependencyClassifier struct{}

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

// ClassifyToolDependencies groups tool calls without changing model-provided
// order. Only an explicit allow-list of side-effect-free tools may share a
// parallel group. Unknown/MCP/control tools are serialized conservatively: an
// unknown tool can mutate remote or local state even though it is absent from
// the built-in write-tool set.
func ClassifyToolDependencies(calls []*genai.FunctionCall) []ToolGroup {
	if len(calls) <= 1 {
		return []ToolGroup{{Calls: calls, Parallel: false}}
	}

	var groups []ToolGroup
	var readGroup []*genai.FunctionCall

	for _, call := range calls {
		if !IsParallelSafeTool(call.Name) {
			if len(readGroup) > 0 {
				groups = append(groups, ToolGroup{Calls: readGroup, Parallel: len(readGroup) > 1})
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

var parallelSafeTools = map[string]bool{
	"read": true, "glob": true, "grep": true, "list_dir": true, "tree": true, "diff": true,
	"web_fetch": true, "web_search": true, "env": true, "tools_list": true,
	"git_status": true, "git_diff": true, "git_log": true, "git_blame": true, "review_changes": true,
	"go_to_definition": true, "find_references": true, "go_search": true,
	"history_search": true, "get_plan_status": true,
}

// IsParallelSafeTool reports whether concurrent execution and abandonment are
// safe for a built-in tool. Unknown third-party tools fail closed to false.
func IsParallelSafeTool(name string) bool {
	return parallelSafeTools[strings.ToLower(strings.TrimSpace(name))]
}

// CancelAbandonmentSafe is an explicit opt-in for a custom tool whose work is
// side-effect-free even if its Execute method ignores cancellation. Built-in
// read tools are covered by IsParallelSafeTool; unknown tools otherwise fail
// closed and are joined before the executor returns.
type CancelAbandonmentSafe interface {
	SafeToAbandonOnCancel() bool
}

func canAbandonToolOnCancel(tool Tool, name string) bool {
	if IsParallelSafeTool(name) {
		return true
	}
	capability, ok := tool.(CancelAbandonmentSafe)
	return ok && capability.SafeToAbandonOnCancel()
}

var defaultClassifier = &toolDependencyClassifier{}

// IsWriteTool reports whether a tool may modify local, remote, session, or
// control state. Only the explicit side-effect-free allow-list is safe; unknown
// MCP/plugin tools fail closed as stateful. This one capability boundary drives
// serialization, checkpoint replay, and cancellation abandonment.
func IsWriteTool(name string) bool {
	return !IsParallelSafeTool(name)
}

// classifyDependencies delegates to the shared ClassifyToolDependencies so the
// executor and the sub-agent classifier share one grouping algorithm. Kept as a
// method so defaultClassifier-based call sites (and tests) read unchanged.
func (c *toolDependencyClassifier) classifyDependencies(calls []*genai.FunctionCall) []ToolGroup {
	return ClassifyToolDependencies(calls)
}
