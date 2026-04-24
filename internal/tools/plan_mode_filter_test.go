package tools

import (
	"context"
	"testing"

	"google.golang.org/genai"
)

// stubTool is a minimal Tool impl used to populate a Registry in tests
// without pulling in real tool dependencies.
type stubTool struct{ name string }

func (t stubTool) Name() string        { return t.name }
func (t stubTool) Description() string { return "stub " + t.name }
func (t stubTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.name, Description: t.Description()}
}
func (t stubTool) Validate(_ map[string]any) error { return nil }
func (t stubTool) Execute(_ context.Context, _ map[string]any) (ToolResult, error) {
	return NewSuccessResult("stub"), nil
}

// namesFromDeclarations pulls the Name field out of a slice of function
// declarations. Convenience for assertions.
func namesFromDeclarations(decls []*genai.FunctionDeclaration) []string {
	names := make([]string, 0, len(decls))
	for _, d := range decls {
		if d != nil {
			names = append(names, d.Name)
		}
	}
	return names
}

// containsName reports whether `list` contains `target` (linear scan; tests only).
// Named to avoid collision with the package-local `containsName(string, string) bool`
// that already exists in validator_test_quality_test.go.
func containsName(list []string, target string) bool {
	for _, v := range list {
		if v == target {
			return true
		}
	}
	return false
}

// TestIsReadOnlyForPlanMode pins the allow-list used by Claude Code-style
// plan mode. Any change here moves what the model can physically see (and
// therefore call) while the user is still exploring — strict scrutiny.
func TestIsReadOnlyForPlanMode(t *testing.T) {
	// Must allow: read/search + metadata + non-mutating git + plan lifecycle.
	allowed := []string{
		"read", "glob", "grep", "list_dir", "tree", "diff",
		"web_fetch", "web_search",
		"env", "tools_list",
		"git_status", "git_diff", "git_log", "git_blame", "git_branch",
		"ask_user", "todo", "task_output", "get_plan_status",
		"memory", "history_search", "pin_context",
		"enter_plan_mode", "exit_plan_mode", "update_plan_progress",
	}
	for _, name := range allowed {
		if !IsReadOnlyForPlanMode(name) {
			t.Errorf("%q must be allowed in plan mode (read/meta/plan-lifecycle tool)", name)
		}
	}

	// Must BLOCK: anything that mutates state (files, git, shell, subagents).
	blocked := []string{
		// Filesystem writes
		"write", "edit", "delete", "move", "copy", "mkdir",
		// Shell / execution
		"bash", "ssh", "kill_shell", "run_tests",
		// Git writes
		"git_add", "git_commit", "git_pr",
		// Sub-agents / delegation
		"ask_agent", "coordinate", "shared_memory", "update_scratchpad",
		"request_tool", "task", "task_stop",
		// Memory writes + batched ops
		"memorize", "batch", "refactor", "verify_code", "check_impact",
		// Plan state mutation (undo/redo are not part of exploration)
		"undo_plan", "redo_plan",
	}
	for _, name := range blocked {
		if IsReadOnlyForPlanMode(name) {
			t.Errorf("%q must be BLOCKED in plan mode (write/exec/delegation tool)", name)
		}
	}

	// MCP-style unknown tools default to blocked — conservative by design,
	// since a third-party MCP server could do anything (file mutation,
	// network writes, shell exec). User explicitly vets via permissions
	// system before we'd trust them.
	unknown := []string{"", "some_mcp_tool", "custom_scraper", "unknown"}
	for _, name := range unknown {
		if IsReadOnlyForPlanMode(name) {
			t.Errorf("%q (unknown) must be BLOCKED — conservative default for MCP and custom tools", name)
		}
	}
}

// TestPlanModeDeclarations_FiltersRegistry proves that plan-mode tool
// filtering at the registry level only surfaces tools in the allow-list.
// This is the contract between the registry and the client schema push
// in App.toolsForCurrentMode.
func TestPlanModeDeclarations_FiltersRegistry(t *testing.T) {
	reg := NewRegistry()

	// Register a mix of read-only and write tools.
	reg.MustRegister(stubTool{name: "read"})
	reg.MustRegister(stubTool{name: "write"})
	reg.MustRegister(stubTool{name: "edit"})
	reg.MustRegister(stubTool{name: "bash"})
	reg.MustRegister(stubTool{name: "grep"})
	reg.MustRegister(stubTool{name: "enter_plan_mode"})

	fullNames := namesFromDeclarations(reg.Declarations())
	if !containsName(fullNames, "write") || !containsName(fullNames, "read") {
		t.Fatalf("Declarations() should list everything registered, got %v", fullNames)
	}

	planNames := namesFromDeclarations(reg.PlanModeDeclarations())
	wantAllowed := []string{"read", "grep", "enter_plan_mode"}
	wantBlocked := []string{"write", "edit", "bash"}

	for _, name := range wantAllowed {
		if !containsName(planNames, name) {
			t.Errorf("PlanModeDeclarations must include read-only %q, got %v", name, planNames)
		}
	}
	for _, name := range wantBlocked {
		if containsName(planNames, name) {
			t.Errorf("PlanModeDeclarations must EXCLUDE mutating %q, got %v", name, planNames)
		}
	}
}

// TestPlanModeGeminiTools_Wraps verifies the Gemini envelope is built
// correctly (single Tool with FunctionDeclarations) — the client
// schema-push API expects exactly this shape.
func TestPlanModeGeminiTools_Wraps(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(stubTool{name: "read"})
	reg.MustRegister(stubTool{name: "bash"})

	tools := reg.PlanModeGeminiTools()
	if len(tools) != 1 {
		t.Fatalf("expected single Tool envelope, got %d", len(tools))
	}
	if len(tools[0].FunctionDeclarations) != 1 {
		t.Errorf("expected 1 filtered declaration (read only), got %d", len(tools[0].FunctionDeclarations))
	}
	if tools[0].FunctionDeclarations[0].Name != "read" {
		t.Errorf("expected `read` in plan-mode envelope, got %q", tools[0].FunctionDeclarations[0].Name)
	}
}
