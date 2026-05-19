package tools

import (
	"context"
	"strings"
	"testing"
)

func TestMCPAdminTool_DisabledHintWhenCallbacksUnset(t *testing.T) {
	tool := NewMCPAdminTool()
	res, err := tool.Execute(context.Background(), map[string]any{"action": "list"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success result with disabled-hint, got error %q", res.Error)
	}
	if !strings.Contains(res.Content, "mcp.enabled") {
		t.Fatalf("disabled hint should tell user to set mcp.enabled, got:\n%s", res.Content)
	}
}

func TestMCPAdminTool_DisabledHintWhenEnabledPredicateFalse(t *testing.T) {
	tool := NewMCPAdminTool()
	// Callbacks set but enabled returns false (e.g., manager wired then user
	// disabled MCP later). Treat as disabled.
	tool.SetCallbacks(
		func() string { return "ANY-LIST-OUTPUT" },
		func(string) string { return "ANY-STATUS-OUTPUT" },
		func() bool { return false },
	)
	res, _ := tool.Execute(context.Background(), map[string]any{"action": "list"})
	if strings.Contains(res.Content, "ANY-LIST-OUTPUT") {
		t.Fatal("with enabled=false, list output should be suppressed in favour of disabled-hint")
	}
	if !strings.Contains(res.Content, "mcp.enabled") {
		t.Fatalf("expected disabled hint, got:\n%s", res.Content)
	}
}

func TestMCPAdminTool_ListRoutesToCallback(t *testing.T) {
	tool := NewMCPAdminTool()
	tool.SetCallbacks(
		func() string { return "LIST-CONTENT" },
		func(string) string { return "STATUS-CONTENT" },
		func() bool { return true },
	)
	res, _ := tool.Execute(context.Background(), map[string]any{"action": "list"})
	if res.Content != "LIST-CONTENT" {
		t.Fatalf("list action should return list callback output, got %q", res.Content)
	}
}

func TestMCPAdminTool_DefaultActionIsList(t *testing.T) {
	tool := NewMCPAdminTool()
	tool.SetCallbacks(
		func() string { return "LIST" },
		func(string) string { return "STATUS" },
		func() bool { return true },
	)
	// No action arg.
	res, _ := tool.Execute(context.Background(), map[string]any{})
	if res.Content != "LIST" {
		t.Fatalf("default action should be list, got %q", res.Content)
	}
}

func TestMCPAdminTool_StatusPassesServerName(t *testing.T) {
	tool := NewMCPAdminTool()
	var got string
	tool.SetCallbacks(
		func() string { return "LIST" },
		func(name string) string { got = name; return "STATUS-" + name },
		func() bool { return true },
	)
	res, _ := tool.Execute(context.Background(), map[string]any{
		"action": "status",
		"server": "github",
	})
	if got != "github" {
		t.Fatalf("expected status callback to receive server=github, got %q", got)
	}
	if res.Content != "STATUS-github" {
		t.Fatalf("got %q, want STATUS-github", res.Content)
	}
}

func TestMCPAdminTool_HelpWorksEvenWhenDisabled(t *testing.T) {
	tool := NewMCPAdminTool()
	// No callbacks → still disabled. But help must work, because the model
	// needs to be able to share the cheatsheet with the user BEFORE they
	// flip the flag.
	res, _ := tool.Execute(context.Background(), map[string]any{"action": "help"})
	if !res.Success {
		t.Fatalf("help should succeed even when MCP is disabled, got error %q", res.Error)
	}
	if !strings.Contains(res.Content, "/mcp add") {
		t.Fatalf("help text should document /mcp add, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "enabled: true") {
		t.Fatalf("help text should show how to enable MCP, got:\n%s", res.Content)
	}
}

func TestMCPAdminTool_ValidateRejectsUnknownAction(t *testing.T) {
	tool := NewMCPAdminTool()
	if err := tool.Validate(map[string]any{"action": "delete"}); err == nil {
		t.Fatal("unknown action should fail validation")
	}
}

func TestMCPAdminTool_ValidateAcceptsBlankActionAsList(t *testing.T) {
	tool := NewMCPAdminTool()
	if err := tool.Validate(map[string]any{}); err != nil {
		t.Fatalf("blank action should validate (defaults to list), got: %v", err)
	}
}

// TestMCPAdminTool_DeclarationHasEnumOfActions pins the schema so registry-
// drift tests catch silent renames. The model relies on the enum to discover
// which actions are valid without trial-and-error.
func TestMCPAdminTool_DeclarationHasEnumOfActions(t *testing.T) {
	decl := MCPAdminToolDeclaration()
	if decl.Name != "mcp_admin" {
		t.Fatalf("declaration name = %q, want mcp_admin", decl.Name)
	}
	action, ok := decl.Parameters.Properties["action"]
	if !ok {
		t.Fatal("declaration missing `action` property")
	}
	if len(action.Enum) == 0 {
		t.Fatal("`action` should have an enum of allowed values")
	}
	wantActions := map[string]bool{"list": false, "status": false, "help": false}
	for _, v := range action.Enum {
		wantActions[v] = true
	}
	for action, present := range wantActions {
		if !present {
			t.Errorf("action %q missing from declaration enum", action)
		}
	}
}
