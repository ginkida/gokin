package tools

import "google.golang.org/genai"

// planModeReadOnlyTools is the allow-list of tool names that remain available
// when the agent is in Claude Code-style plan mode. Anything not on this list
// is stripped from the model's tool schema so it literally cannot call
// write/execute tools during the exploration phase.
//
// Curated conservatively: a tool appears here only if it cannot mutate the
// user's workspace, filesystem, git state, or remote services. Contrast with
// `workspaceIsolationReadOnlyTools` in agent/runner.go, which includes tools
// like run_tests and git_add — those are safe in an isolated workspace
// *copy*, but the main agent runs against the user's real working tree, so
// we apply stricter rules.
var planModeReadOnlyTools = map[string]bool{
	// Filesystem reads
	"read":     true,
	"glob":     true,
	"grep":     true,
	"list_dir": true,
	"tree":     true,
	"diff":     true,

	// Web reads
	"web_fetch":  true,
	"web_search": true,

	// Environment / metadata (read-only)
	"env":        true,
	"tools_list": true,

	// Read-only git
	"git_status": true,
	"git_diff":   true,
	"git_log":    true,
	"git_blame":  true,
	"git_branch": true,

	// User interaction / todos (surface, don't mutate disk)
	"ask_user":        true,
	"todo":            true,
	"task_output":     true,
	"get_plan_status": true,

	// Memory reads (writes like `memorize` are intentionally absent)
	"memory":         true,
	"history_search": true,
	"pin_context":    true,

	// Plan lifecycle itself — how the model exits plan mode
	"enter_plan_mode":      true,
	"exit_plan_mode":       true,
	"update_plan_progress": true,
}

// IsReadOnlyForPlanMode reports whether a tool can run while the agent is in
// Claude Code-style plan mode. MCP tools and anything not explicitly in the
// allow-list return false — conservative by design, since a third-party MCP
// server could do anything (file mutation, network writes, shell exec).
//
// The name is lowercased before lookup so case differences between registry
// and caller (e.g., "Read" vs "read") don't accidentally block a safe tool.
func IsReadOnlyForPlanMode(name string) bool {
	return planModeReadOnlyTools[name]
}

// PlanModeDeclarations returns the subset of the registry's tool
// declarations that are safe in plan mode. Used to rebuild the
// `client.SetTools` schema when the user toggles planning mode on — the
// model then literally cannot emit a call for a write/execute tool,
// because it doesn't know they exist.
func (r *Registry) PlanModeDeclarations() []*genai.FunctionDeclaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	decls := make([]*genai.FunctionDeclaration, 0, len(r.tools))
	for name, tool := range r.tools {
		if !IsReadOnlyForPlanMode(name) {
			continue
		}
		decls = append(decls, tool.Declaration())
	}
	return decls
}

// PlanModeGeminiTools wraps PlanModeDeclarations in the single-tool envelope
// the client API expects.
func (r *Registry) PlanModeGeminiTools() []*genai.Tool {
	return []*genai.Tool{
		{
			FunctionDeclarations: r.PlanModeDeclarations(),
		},
	}
}
