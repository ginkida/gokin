package app

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"gokin/internal/permission"
	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

func TestResolveToolCapabilityCeiling(t *testing.T) {
	available := []string{"write", "read", "bash"}
	tests := []struct {
		name    string
		allowed []string
		denied  []string
		want    []string
		errSub  string
	}{
		{name: "explicit allow", allowed: []string{"read", "bash", "read"}, want: []string{"bash", "read"}},
		{name: "explicit empty", allowed: []string{}, want: []string{}},
		{name: "deny from all", denied: []string{"write"}, want: []string{"bash", "read"}},
		{name: "deny wins", allowed: []string{"read", "write"}, denied: []string{"write"}, want: []string{"read"}},
		{name: "unknown allow", allowed: []string{"reed"}, errSub: "reed"},
		{name: "unknown deny", denied: []string{"dangerous_typo"}, errSub: "dangerous_typo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveToolCapabilityCeiling(available, tt.allowed, tt.denied)
			if tt.errSub != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errSub) {
					t.Fatalf("error = %v, want substring %q", err, tt.errSub)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ceiling = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigureRunPermissionRulesUsesSharedManager(t *testing.T) {
	manager := permission.NewManager(permission.DefaultRules(), true)
	application := &App{permManager: manager}
	if err := application.ConfigureRunPermissionRules(
		[]string{"Write", "Bash(git status *)"},
		[]string{"Bash(git push *)"},
	); err != nil {
		t.Fatal(err)
	}
	allows, denies := manager.GetRunToolRules()
	if !reflect.DeepEqual(allows, []string{"write", "bash(git status *)"}) ||
		!reflect.DeepEqual(denies, []string{"bash(git push *)"}) {
		t.Fatalf("run rules = allow %#v deny %#v", allows, denies)
	}
	if err := application.ConfigureRunPermissionRules(
		[]string{"Bash("}, nil,
	); err == nil {
		t.Fatal("malformed run rule was accepted")
	}
	afterAllows, afterDenies := manager.GetRunToolRules()
	if !reflect.DeepEqual(afterAllows, allows) || !reflect.DeepEqual(afterDenies, denies) {
		t.Fatalf("failed reconfiguration mutated run rules: %#v %#v",
			afterAllows, afterDenies)
	}
}

func TestConfigureToolCapabilityFiltersSchemaAndBlocksHallucinatedTool(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueToolCall("write", map[string]any{"file_path": "blocked.go"}).
		EnqueueText("write was unavailable").
		EnqueueToolCall("write", map[string]any{"file_path": "allowed.go"}).
		EnqueueText("write completed")
	writeTool := &appHeadlessScriptedTool{
		name:    "write",
		results: []tools.ToolResult{tools.NewSuccessResult("written")},
	}
	application, _ := newHeadlessPolicyTestApp(t, mock, writeTool)
	readTool := &appHeadlessScriptedTool{name: "read"}
	if err := application.registry.Register(readTool); err != nil {
		t.Fatal(err)
	}

	if err := application.ConfigureToolCapability([]string{"read"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := namesFromToolSchema(mock.GetTools()); !reflect.DeepEqual(got, []string{"read"}) {
		t.Fatalf("advertised tools = %v, want [read]", got)
	}

	var stdout bytes.Buffer
	result, err := application.RunHeadlessWithOptions(context.Background(), "do not write", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       &stdout,
		Stderr:       io.Discard,
	})
	if err == nil || result.Status != "policy_blocked" || result.Error == nil ||
		result.Error.Tool != "write" || result.Error.PolicyKind != "permission" {
		t.Fatalf("restricted result=%+v err=%v", result, err)
	}
	if writeTool.CallCount() != 0 {
		t.Fatalf("hallucinated denied tool executed %d times", writeTool.CallCount())
	}

	// Clearing the process-scoped ceiling restores both the schema and runtime
	// authority; the prior restricted turn must not poison later configuration.
	if err := application.ConfigureToolCapability(nil, nil); err != nil {
		t.Fatal(err)
	}
	second, err := application.RunHeadlessWithOptions(context.Background(), "write now", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})
	if err != nil || second.Status != "success" || writeTool.CallCount() != 1 {
		t.Fatalf("cleared ceiling result=%+v err=%v calls=%d", second, err, writeTool.CallCount())
	}
}

// A deny-only ceiling means "everything the registry has, minus these". It was
// materialized once at startup, so a tool the registry gained afterwards (an
// MCP server connecting, reconnecting, or announcing tools/list_changed) fell
// outside the frozen allow-set and was blocked even though the user never
// denied it. Recomputing against the live registry keeps the request honest.
func TestRefreshToolCapabilityCeilingAdoptsLaterRegisteredTools(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueText("idle")
	writeTool := &appHeadlessScriptedTool{name: "write"}
	application, _ := newHeadlessPolicyTestApp(t, mock, writeTool)
	if err := application.registry.Register(&appHeadlessScriptedTool{name: "read"}); err != nil {
		t.Fatal(err)
	}

	if err := application.ConfigureToolCapability(nil, []string{"write"}); err != nil {
		t.Fatal(err)
	}
	ceiling, restricted := application.toolCapabilitySnapshot()
	if !restricted || !reflect.DeepEqual(ceiling, []string{"read"}) {
		t.Fatalf("initial ceiling = %v (restricted=%v), want [read]", ceiling, restricted)
	}

	// A late arrival the user never denied.
	if err := application.registry.Register(&appHeadlessScriptedTool{name: "github_create_issue"}); err != nil {
		t.Fatal(err)
	}
	application.refreshToolCapabilityCeiling()
	ceiling, restricted = application.toolCapabilitySnapshot()
	if !restricted || !reflect.DeepEqual(ceiling, []string{"github_create_issue", "read"}) {
		t.Fatalf("refreshed ceiling = %v, want [github_create_issue read]", ceiling)
	}

	// An explicit --tools allowlist is an exact set and must NOT grow.
	if err := application.ConfigureToolCapability([]string{"read"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := application.registry.Register(&appHeadlessScriptedTool{name: "github_list_prs"}); err != nil {
		t.Fatal(err)
	}
	application.refreshToolCapabilityCeiling()
	ceiling, _ = application.toolCapabilitySnapshot()
	if !reflect.DeepEqual(ceiling, []string{"read"}) {
		t.Fatalf("explicit allowlist grew to %v, want [read]", ceiling)
	}
}

func TestConfigureToolCapabilityUnknownNameFailsWithoutMutatingPolicy(t *testing.T) {
	mock := testkit.NewMockClient()
	application, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "read"})
	if err := application.ConfigureToolCapability([]string{"read"}, nil); err != nil {
		t.Fatal(err)
	}
	before, restricted := application.toolCapabilitySnapshot()

	err := application.ConfigureToolCapability([]string{"reed"}, nil)
	if err == nil {
		t.Fatal("unknown tool name was accepted")
	}
	after, afterRestricted := application.toolCapabilitySnapshot()
	if restricted != afterRestricted || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed reconfiguration mutated policy: before=%v/%v after=%v/%v",
			before, restricted, after, afterRestricted)
	}
}

func TestConfigureToolCapabilityExplicitEmptyBlocksEveryTool(t *testing.T) {
	mock := testkit.NewMockClient().
		EnqueueToolCall("read", map[string]any{"file_path": "main.go"}).
		EnqueueText("no tools available")
	readTool := &appHeadlessScriptedTool{
		name:    "read",
		results: []tools.ToolResult{tools.NewSuccessResult("must not execute")},
	}
	application, _ := newHeadlessPolicyTestApp(t, mock, readTool)

	if err := application.ConfigureToolCapability([]string{}, nil); err != nil {
		t.Fatal(err)
	}
	ceiling, restricted := application.toolCapabilitySnapshot()
	if !restricted || ceiling == nil || len(ceiling) != 0 {
		t.Fatalf("empty ceiling=%v restricted=%v", ceiling, restricted)
	}
	if schema := mock.GetTools(); len(schema) != 0 {
		t.Fatalf("empty ceiling still advertised schema: %+v", schema)
	}

	result, err := application.RunHeadlessWithOptions(context.Background(), "try to read", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})
	if err == nil || result.Status != "policy_blocked" || readTool.CallCount() != 0 {
		t.Fatalf("empty ceiling result=%+v err=%v calls=%d", result, err, readTool.CallCount())
	}
}

func namesFromToolSchema(schema []*genai.Tool) []string {
	var names []string
	for _, envelope := range schema {
		if envelope == nil {
			continue
		}
		for _, declaration := range envelope.FunctionDeclarations {
			if declaration != nil {
				names = append(names, declaration.Name)
			}
		}
	}
	return names
}
