package tools

import (
	"context"
	"fmt"
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

func TestMCPAdminTool_ResourcesListRoutesToCallback(t *testing.T) {
	tool := NewMCPAdminTool()
	tool.SetCallbacks(
		func() string { return "LIST" },
		func(string) string { return "STATUS" },
		func() bool { return true },
	)
	tool.SetResourceCallbacks(
		func() string { return "RESOURCE-CATALOG" },
		func(context.Context, string) (string, error) { return "SHOULD-NOT-BE-CALLED", nil },
	)
	res, _ := tool.Execute(context.Background(), map[string]any{"action": "resources"})
	if res.Content != "RESOURCE-CATALOG" {
		t.Fatalf("resources (no uri) should list, got %q", res.Content)
	}
}

func TestMCPAdminTool_ResourcesReadPassesURI(t *testing.T) {
	tool := NewMCPAdminTool()
	tool.SetCallbacks(
		func() string { return "LIST" },
		func(string) string { return "STATUS" },
		func() bool { return true },
	)
	var gotURI string
	tool.SetResourceCallbacks(
		func() string { return "CATALOG" },
		func(_ context.Context, uri string) (string, error) { gotURI = uri; return "CONTENT-OF-" + uri, nil },
	)
	res, _ := tool.Execute(context.Background(), map[string]any{
		"action": "resources",
		"uri":    "file:///readme.md",
	})
	if gotURI != "file:///readme.md" {
		t.Fatalf("read callback got uri %q, want file:///readme.md", gotURI)
	}
	if res.Content != "CONTENT-OF-file:///readme.md" {
		t.Fatalf("got %q", res.Content)
	}
}

func TestMCPAdminTool_ResourcesUnwiredReportsCleanly(t *testing.T) {
	tool := NewMCPAdminTool()
	// MCP is ready (list/status wired) but resource callbacks were never set —
	// must report "not wired", not panic on a nil callback.
	tool.SetCallbacks(
		func() string { return "LIST" },
		func(string) string { return "STATUS" },
		func() bool { return true },
	)
	listRes, _ := tool.Execute(context.Background(), map[string]any{"action": "resources"})
	if listRes.Success || !strings.Contains(listRes.Error, "not wired") {
		t.Fatalf("unwired resources list should error cleanly, got success=%v err=%q", listRes.Success, listRes.Error)
	}
	readRes, _ := tool.Execute(context.Background(), map[string]any{"action": "resources", "uri": "x://y"})
	if readRes.Success || !strings.Contains(readRes.Error, "not wired") {
		t.Fatalf("unwired resource read should error cleanly, got success=%v err=%q", readRes.Success, readRes.Error)
	}
}

func TestMCPAdminTool_ValidateAcceptsResources(t *testing.T) {
	tool := NewMCPAdminTool()
	if err := tool.Validate(map[string]any{"action": "resources"}); err != nil {
		t.Fatalf("resources (list) should validate, got %v", err)
	}
	if err := tool.Validate(map[string]any{"action": "resources", "uri": "file:///x"}); err != nil {
		t.Fatalf("resources (read) should validate, got %v", err)
	}
}

func TestMCPAdminTool_ResourceReadCallbackErrorSurfaces(t *testing.T) {
	tool := NewMCPAdminTool()
	tool.SetCallbacks(
		func() string { return "LIST" },
		func(string) string { return "STATUS" },
		func() bool { return true },
	)
	tool.SetResourceCallbacks(
		func() string { return "CATALOG" },
		func(context.Context, string) (string, error) { return "", fmt.Errorf("server refused") },
	)
	res, _ := tool.Execute(context.Background(), map[string]any{"action": "resources", "uri": "x://y"})
	if res.Success || !strings.Contains(res.Error, "server refused") {
		t.Fatalf("read error should surface, got success=%v err=%q", res.Success, res.Error)
	}
}

func TestMCPAdminTool_PromptsListRoutesToCallback(t *testing.T) {
	tool := NewMCPAdminTool()
	tool.SetCallbacks(
		func() string { return "LIST" },
		func(string) string { return "STATUS" },
		func() bool { return true },
	)
	tool.SetPromptCallbacks(
		func() string { return "PROMPT-CATALOG" },
		func(context.Context, string, map[string]any) (string, error) { return "SHOULD-NOT-BE-CALLED", nil },
	)
	res, _ := tool.Execute(context.Background(), map[string]any{"action": "prompts"})
	if res.Content != "PROMPT-CATALOG" {
		t.Fatalf("prompts (no name) should list, got %q", res.Content)
	}
}

func TestMCPAdminTool_PromptsGetPassesNameAndArgs(t *testing.T) {
	tool := NewMCPAdminTool()
	tool.SetCallbacks(
		func() string { return "LIST" },
		func(string) string { return "STATUS" },
		func() bool { return true },
	)
	var gotName string
	var gotArgs map[string]any
	tool.SetPromptCallbacks(
		func() string { return "CATALOG" },
		func(_ context.Context, name string, args map[string]any) (string, error) {
			gotName = name
			gotArgs = args
			return "RENDERED-" + name, nil
		},
	)
	res, _ := tool.Execute(context.Background(), map[string]any{
		"action":      "prompts",
		"name":        "greet",
		"prompt_args": map[string]any{"who": "world"},
	})
	if gotName != "greet" {
		t.Fatalf("get callback got name %q, want greet", gotName)
	}
	if gotArgs["who"] != "world" {
		t.Fatalf("get callback got args %v, want who=world", gotArgs)
	}
	if res.Content != "RENDERED-greet" {
		t.Fatalf("got %q", res.Content)
	}
}

func TestMCPAdminTool_PromptsUnwiredReportsCleanly(t *testing.T) {
	tool := NewMCPAdminTool()
	tool.SetCallbacks(
		func() string { return "LIST" },
		func(string) string { return "STATUS" },
		func() bool { return true },
	)
	listRes, _ := tool.Execute(context.Background(), map[string]any{"action": "prompts"})
	if listRes.Success || !strings.Contains(listRes.Error, "not wired") {
		t.Fatalf("unwired prompts list should error cleanly, got success=%v err=%q", listRes.Success, listRes.Error)
	}
	getRes, _ := tool.Execute(context.Background(), map[string]any{"action": "prompts", "name": "x"})
	if getRes.Success || !strings.Contains(getRes.Error, "not wired") {
		t.Fatalf("unwired prompt get should error cleanly, got success=%v err=%q", getRes.Success, getRes.Error)
	}
}

func TestMCPAdminTool_ValidateAcceptsPrompts(t *testing.T) {
	tool := NewMCPAdminTool()
	if err := tool.Validate(map[string]any{"action": "prompts"}); err != nil {
		t.Fatalf("prompts (list) should validate, got %v", err)
	}
	if err := tool.Validate(map[string]any{"action": "prompts", "name": "greet"}); err != nil {
		t.Fatalf("prompts (get) should validate, got %v", err)
	}
}

func TestMCPAdminTool_PromptGetCallbackErrorSurfaces(t *testing.T) {
	tool := NewMCPAdminTool()
	tool.SetCallbacks(
		func() string { return "LIST" },
		func(string) string { return "STATUS" },
		func() bool { return true },
	)
	tool.SetPromptCallbacks(
		func() string { return "CATALOG" },
		func(context.Context, string, map[string]any) (string, error) { return "", fmt.Errorf("server refused") },
	)
	res, _ := tool.Execute(context.Background(), map[string]any{"action": "prompts", "name": "x"})
	if res.Success || !strings.Contains(res.Error, "server refused") {
		t.Fatalf("get error should surface, got success=%v err=%q", res.Success, res.Error)
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
	wantActions := map[string]bool{
		"list": false, "status": false, "help": false,
		"add": false, "remove": false,
	}
	for _, v := range action.Enum {
		wantActions[v] = true
	}
	for action, present := range wantActions {
		if !present {
			t.Errorf("action %q missing from declaration enum", action)
		}
	}
}

func TestMCPAdminTool_ValidateAdd(t *testing.T) {
	tool := NewMCPAdminTool()
	cases := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"stdio ok", map[string]any{"action": "add", "server": "x", "transport": "stdio", "command": "echo"}, false},
		{"http ok", map[string]any{"action": "add", "server": "x", "transport": "http", "url": "https://e/mcp"}, false},
		{"missing server", map[string]any{"action": "add", "transport": "stdio", "command": "echo"}, true},
		{"missing transport", map[string]any{"action": "add", "server": "x", "command": "echo"}, true},
		{"unknown transport", map[string]any{"action": "add", "server": "x", "transport": "udp", "command": "echo"}, true},
		{"stdio missing command", map[string]any{"action": "add", "server": "x", "transport": "stdio"}, true},
		{"http missing url", map[string]any{"action": "add", "server": "x", "transport": "http"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tool.Validate(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestMCPAdminTool_ValidateRemove(t *testing.T) {
	tool := NewMCPAdminTool()
	if err := tool.Validate(map[string]any{"action": "remove"}); err == nil {
		t.Fatal("remove without server should fail validation")
	}
	if err := tool.Validate(map[string]any{"action": "remove", "server": "x"}); err != nil {
		t.Fatalf("remove with server should validate, got: %v", err)
	}
}

func TestMCPAdminTool_AddRoutesToCallback(t *testing.T) {
	tool := NewMCPAdminTool()
	tool.SetCallbacks(
		func() string { return "" },
		func(string) string { return "" },
		func() bool { return true },
	)
	var captured MCPAddParams
	tool.SetMutationCallbacks(
		func(_ context.Context, p MCPAddParams) (string, error) {
			captured = p
			return "Added " + p.Name, nil
		},
		nil,
	)
	res, err := tool.Execute(context.Background(), map[string]any{
		"action":    "add",
		"server":    "github",
		"transport": "stdio",
		"command":   "npx",
		"args":      []any{"-y", "@modelcontextprotocol/server-github"},
	})
	if err != nil {
		t.Fatalf("Execute returned err: %v", err)
	}
	if !res.Success {
		t.Fatalf("Execute returned failure: %s", res.Error)
	}
	if captured.Name != "github" || captured.Transport != "stdio" || captured.Command != "npx" {
		t.Fatalf("callback got wrong params: %+v", captured)
	}
	want := []string{"-y", "@modelcontextprotocol/server-github"}
	if len(captured.Args) != len(want) || captured.Args[0] != want[0] || captured.Args[1] != want[1] {
		t.Fatalf("captured.Args = %v, want %v", captured.Args, want)
	}
	if !strings.Contains(res.Content, "Added github") {
		t.Fatalf("expected callback result in content, got %q", res.Content)
	}
}

func TestMCPAdminTool_AddSurfacesCallbackError(t *testing.T) {
	tool := NewMCPAdminTool()
	tool.SetCallbacks(
		func() string { return "" },
		func(string) string { return "" },
		func() bool { return true },
	)
	tool.SetMutationCallbacks(
		func(_ context.Context, _ MCPAddParams) (string, error) {
			return "", fmt.Errorf("connect: subprocess exited with code 1")
		},
		nil,
	)
	res, _ := tool.Execute(context.Background(), map[string]any{
		"action":    "add",
		"server":    "broken",
		"transport": "stdio",
		"command":   "/nonexistent",
	})
	if res.Success {
		t.Fatal("expected failure when callback errors, got success")
	}
	if !strings.Contains(res.Error, "subprocess exited") {
		t.Fatalf("error message lost: %q", res.Error)
	}
}

func TestMCPAdminTool_RemoveRoutesToCallback(t *testing.T) {
	tool := NewMCPAdminTool()
	tool.SetCallbacks(
		func() string { return "" },
		func(string) string { return "" },
		func() bool { return true },
	)
	var got string
	tool.SetMutationCallbacks(
		nil,
		func(name string) (string, error) {
			got = name
			return "Removed " + name, nil
		},
	)
	res, _ := tool.Execute(context.Background(), map[string]any{
		"action": "remove",
		"server": "github",
	})
	if got != "github" {
		t.Fatalf("remove callback got %q, want github", got)
	}
	if !strings.Contains(res.Content, "Removed github") {
		t.Fatalf("expected removal result, got %q", res.Content)
	}
}

func TestMCPAdminTool_AddWithoutCallbackErrors(t *testing.T) {
	tool := NewMCPAdminTool()
	tool.SetCallbacks(
		func() string { return "" },
		func(string) string { return "" },
		func() bool { return true },
	)
	// SetMutationCallbacks NOT called — add path should refuse.
	res, _ := tool.Execute(context.Background(), map[string]any{
		"action":    "add",
		"server":    "x",
		"transport": "stdio",
		"command":   "echo",
	})
	if res.Success {
		t.Fatal("add without wired callback should fail")
	}
}

// v0.101 control-plane actions: argument validation + honest unwired hints
// (a build/test that doesn't wire SetControlCallbacks must degrade to a clear
// message, never panic).
func TestMCPAdminControlActions(t *testing.T) {
	tool := NewMCPAdminTool()
	// enabled-gate: give it a live-looking status callback so mcpReady()=true.
	tool.SetCallbacks(func() string { return "srv" }, func(name string) string { return "ok" }, func() bool { return true })

	// Validation table.
	for _, tc := range []struct {
		args    map[string]any
		wantErr bool
	}{
		{map[string]any{"action": "enable"}, false},
		{map[string]any{"action": "disable"}, false},
		{map[string]any{"action": "pause"}, true},
		{map[string]any{"action": "pause", "server": "x"}, false},
		{map[string]any{"action": "resume"}, true},
		{map[string]any{"action": "edit"}, true},
		{map[string]any{"action": "edit", "server": "x"}, false},
		{map[string]any{"action": "preset"}, true},
		{map[string]any{"action": "preset", "server": "github"}, false},
	} {
		err := tool.Validate(tc.args)
		if (err != nil) != tc.wantErr {
			t.Errorf("Validate(%v) err=%v, wantErr=%v", tc.args, err, tc.wantErr)
		}
	}

	// Unwired execution: every control action reports "not wired", not a panic.
	for _, args := range []map[string]any{
		{"action": "enable"},
		{"action": "disable"},
		{"action": "pause", "server": "x"},
		{"action": "resume", "server": "x"},
		{"action": "edit", "server": "x", "command": "y"},
		{"action": "preset", "server": "github"},
	} {
		res, err := tool.Execute(t.Context(), args)
		if err != nil {
			t.Fatalf("Execute(%v) transport err: %v", args, err)
		}
		if res.Success {
			t.Errorf("Execute(%v) unwired must be an error result, got success: %s", args, res.Content)
		}
		if !strings.Contains(res.Error+res.Content, "not wired") {
			t.Errorf("Execute(%v) must carry the unwired hint, got %q / %q", args, res.Error, res.Content)
		}
	}

	// Wired paths route to the callbacks.
	tool.SetControlCallbacks(
		func(enable bool) (string, error) { return fmt.Sprintf("toggled=%v", enable), nil },
		func(name string, paused bool) (string, error) { return fmt.Sprintf("%s paused=%v", name, paused), nil },
		func(ctx context.Context, p MCPAddParams) (string, error) { return "edited " + p.Name, nil },
		func(name string) (string, error) { return "preset " + name, nil },
	)
	res, _ := tool.Execute(t.Context(), map[string]any{"action": "enable"})
	if !res.Success || !strings.Contains(res.Content, "toggled=true") {
		t.Fatalf("enable route: %+v", res)
	}
	res, _ = tool.Execute(t.Context(), map[string]any{"action": "pause", "server": "srv"})
	if !res.Success || !strings.Contains(res.Content, "srv paused=true") {
		t.Fatalf("pause route: %+v", res)
	}
	res, _ = tool.Execute(t.Context(), map[string]any{"action": "preset", "server": "github"})
	if !res.Success || !strings.Contains(res.Content, "preset github") {
		t.Fatalf("preset route: %+v", res)
	}
}
