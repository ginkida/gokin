package tools

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/genai"
)

// fakeMCPTool is a minimal Tool that satisfies mcpServerSource for filter
// tests. It lives in the test file so we don't drag real mcp.MCPTool
// into the tools package (which would be a cyclic import).
type fakeMCPTool struct {
	name       string
	desc       string
	serverName string
}

func (f *fakeMCPTool) Name() string        { return f.name }
func (f *fakeMCPTool) Description() string { return f.desc }
func (f *fakeMCPTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: f.name, Description: f.desc}
}
func (f *fakeMCPTool) Validate(map[string]any) error { return nil }
func (f *fakeMCPTool) Execute(context.Context, map[string]any) (ToolResult, error) {
	return NewSuccessResult(""), nil
}
func (f *fakeMCPTool) GetServerName() string { return f.serverName }

// builtinTool is a tool without server origin (no GetServerName).
type builtinTool struct {
	name string
	desc string
}

func (b *builtinTool) Name() string        { return b.name }
func (b *builtinTool) Description() string { return b.desc }
func (b *builtinTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: b.name, Description: b.desc}
}
func (b *builtinTool) Validate(map[string]any) error { return nil }
func (b *builtinTool) Execute(context.Context, map[string]any) (ToolResult, error) {
	return NewSuccessResult(""), nil
}

func newRegistryWithToolsForTest(tools ...Tool) *Registry {
	r := NewRegistry()
	for _, tool := range tools {
		_ = r.Register(tool)
	}
	return r
}

func TestToolsListTool_NoFilterListsAll(t *testing.T) {
	reg := newRegistryWithToolsForTest(
		&builtinTool{name: "read", desc: "Read files"},
		&fakeMCPTool{name: "gh_repo", desc: "GitHub repo tool", serverName: "github"},
		&fakeMCPTool{name: "fs_list", desc: "FS list", serverName: "firebase"},
	)
	tool := NewToolsListTool(reg)

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Content
	for _, name := range []string{"read", "gh_repo", "fs_list"} {
		if !strings.Contains(out, name) {
			t.Errorf("unfiltered list missing %q: %s", name, out)
		}
	}
}

func TestToolsListTool_ServerFilterKeepsOnlyMatching(t *testing.T) {
	reg := newRegistryWithToolsForTest(
		&builtinTool{name: "read", desc: "Read files"},
		&fakeMCPTool{name: "gh_repo", desc: "GitHub repo tool", serverName: "github"},
		&fakeMCPTool{name: "gh_pr", desc: "GitHub PR tool", serverName: "github"},
		&fakeMCPTool{name: "fs_list", desc: "FS list", serverName: "firebase"},
	)
	tool := NewToolsListTool(reg)

	res, err := tool.Execute(context.Background(), map[string]any{"server": "github"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Content
	if !strings.Contains(out, "gh_repo") || !strings.Contains(out, "gh_pr") {
		t.Errorf("github-filtered list missing github tools: %s", out)
	}
	for _, shouldNotAppear := range []string{"read", "fs_list"} {
		if strings.Contains(out, shouldNotAppear) {
			t.Errorf("filtered output leaked %q: %s", shouldNotAppear, out)
		}
	}
}

func TestToolsListTool_ServerFilterEmptyWhenNoMatch(t *testing.T) {
	reg := newRegistryWithToolsForTest(
		&builtinTool{name: "read", desc: "Read files"},
		&fakeMCPTool{name: "gh_repo", desc: "GitHub", serverName: "github"},
	)
	tool := NewToolsListTool(reg)

	res, err := tool.Execute(context.Background(), map[string]any{"server": "nonexistent"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "No tools found") {
		t.Errorf("expected helpful 'No tools found' message, got: %s", res.Content)
	}
}

func TestToolsListTool_ServerFilterTrimsWhitespace(t *testing.T) {
	reg := newRegistryWithToolsForTest(
		&fakeMCPTool{name: "gh_repo", desc: "GitHub", serverName: "github"},
	)
	tool := NewToolsListTool(reg)

	res, err := tool.Execute(context.Background(), map[string]any{"server": "  github  "})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "gh_repo") {
		t.Errorf("whitespace should be trimmed; got: %s", res.Content)
	}
}

func TestToolsListTool_BuiltinToolsSkippedWhenFiltering(t *testing.T) {
	// Even if the filter matches a built-in tool's name coincidentally,
	// a built-in without GetServerName() must be excluded from per-server
	// results. Otherwise a built-in "github" tool would pollute the list.
	reg := newRegistryWithToolsForTest(
		&builtinTool{name: "github", desc: "some unrelated built-in"},
	)
	tool := NewToolsListTool(reg)

	res, err := tool.Execute(context.Background(), map[string]any{"server": "github"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(res.Content, "some unrelated") {
		t.Errorf("built-in must not leak into server-filtered output: %s", res.Content)
	}
}

func TestToolsListTool_DeclarationExposesServerArg(t *testing.T) {
	reg := NewRegistry()
	tool := NewToolsListTool(reg)
	decl := tool.Declaration()
	if decl.Parameters == nil || decl.Parameters.Properties == nil {
		t.Fatal("declaration has no parameters")
	}
	if _, ok := decl.Parameters.Properties["server"]; !ok {
		t.Error("declaration missing 'server' parameter")
	}
}
