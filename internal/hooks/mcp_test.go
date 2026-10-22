package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func parseMCPTest(t *testing.T, handler string) ([]ClaudeCommand, error) {
	t.Helper()
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(`{"PreToolUse":[{"hooks":[`+handler+`]}]}`), &document); err != nil {
		t.Fatal(err)
	}
	return ParseClaudeHooks(*document.Content[0])
}

func TestMCPHookSchemaCloneAndFingerprint(t *testing.T) {
	for _, handler := range []string{
		`{"type":"mcp_tool","tool":"check"}`,
		`{"type":"mcp_tool","server":"my server","tool":"check"}`,
		`{"type":"mcp_tool","server":"fixture","tool":1}`,
		`{"type":"mcp_tool","server":"fixture","tool":"check","command":"true"}`,
		`{"type":"mcp_tool","server":"fixture","tool":"check","args":[]}`,
		`{"type":"mcp_tool","server":"fixture","tool":"check","headers":{}}`,
		`{"type":"mcp_tool","server":"fixture","tool":"check","onFailure":"block"}`,
		`{"type":"mcp_tool","server":"fixture","tool":"check","input":null}`,
		`{"type":"mcp_tool","server":"fixture","tool":"check","input":[]}`,
		`{"type":"mcp_tool","server":"fixture","tool":"check","input":{"x":1,"x":2}}`,
		`{"type":"command","command":"true","input":{}}`,
	} {
		if _, err := parseMCPTest(t, handler); err == nil {
			t.Errorf("accepted invalid MCP hook: %s", handler)
		}
	}
	definitions, err := parseMCPTest(t, `{"type":"mcp_tool","server":"plugin:suite:db","tool":"check","input":{"nested":{"values":[true,null,2,"${tool_input}"]}},"once":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if definitions[0].Handler != "mcp_tool" || definitions[0].Timeout != 600*time.Second || !definitions[0].Once {
		t.Fatalf("wrong MCP defaults: %+v", definitions)
	}
	hash := CommandFingerprint(definitions)
	for _, mutate := range []func(*ClaudeCommand){
		func(h *ClaudeCommand) { h.MCPServer = "other" }, func(h *ClaudeCommand) { h.MCPTool = "other" }, func(h *ClaudeCommand) { h.MCPInput["nested"].(map[string]any)["values"].([]any)[0] = false },
	} {
		copy := CloneClaudeCommands(definitions)
		mutate(&copy[0])
		if CommandFingerprint(copy) == hash || CommandFingerprint(definitions) != hash {
			t.Fatal("MCP fingerprint or deep copy lost a field")
		}
	}
}

func TestMCPHookInputTemplatesSinglePassAndTypes(t *testing.T) {
	definitions, err := parseMCPTest(t, `{"type":"mcp_tool","server":"fixture","tool":"check","input":{"id":9007199254740993}}`)
	if err != nil || definitions[0].MCPInput["id"] != json.Number("9007199254740993") {
		t.Fatalf("MCP input integer lost precision: %+v %v", definitions, err)
	}
	input := map[string]any{"path": "prefix ${tool_input.file_path}", "whole": "${tool_input}", "enabled": "${tool_input.enabled}", "number": "${tool_input.count}", "nested": []any{"${session_id}", nil, true}, "option": "${user_config.secret}"}
	event := map[string]any{"session_id": "session", "tool_input": map[string]any{"file_path": "${session_id}", "enabled": true, "count": float64(2)}}
	got, err := expandMCPHookInput(input, event, RuntimeInfo{}, &claudePluginRuntime{Options: map[string]string{"secret": "${tool_input.file_path}"}})
	if err != nil {
		t.Fatal(err)
	}
	if got["path"] != "prefix ${session_id}" || got["enabled"] != true || got["number"] != float64(2) || got["option"] != "${tool_input.file_path}" || !reflect.DeepEqual(got["whole"], event["tool_input"]) {
		t.Fatalf("templates lost types or expanded inserted data: %+v", got)
	}
	for _, text := range []string{"${missing}", "prefix ${tool_input.missing}", "${tool_input.enabled.value}"} {
		if _, err := expandMCPHookInput(map[string]any{"x": text}, event, RuntimeInfo{}, nil); err == nil {
			t.Fatal("missing event field accepted")
		}
	}
	if _, err := expandMCPHookInput(map[string]any{"x": "${session_id}"}, map[string]any{"session_id": strings.Repeat("x", maxHookOutputBytes+1)}, RuntimeInfo{}, nil); err == nil {
		t.Fatal("expanded input exceeded limit")
	}
	if _, err := expandMCPHookInput(map[string]any{"x": strings.Repeat("${session_id}", 1000)}, map[string]any{"session_id": strings.Repeat("x", 32<<10)}, RuntimeInfo{}, nil); err == nil {
		t.Fatal("repeated placeholders bypassed bounded expansion")
	}
}

func TestMCPHookControlsOnceAndFailures(t *testing.T) {
	for _, test := range []struct {
		name, text                   string
		isError, failed, block, stop bool
	}{
		{"empty", "", false, false, false, false},
		{"plain", "checked", false, false, false, false},
		{"deny", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"review"}}`, false, false, true, false},
		{"stop", `{"continue":false,"stopReason":"stop"}`, false, false, false, true},
		{"tool-error", `{"continue":false}`, true, true, false, false},
		{"invalid", `{"continue":true,"continue":false}`, false, true, false, false},
		{"large", strings.Repeat("x", maxHookOutputBytes+1), false, true, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := NewManager(true, t.TempDir())
			var calls atomic.Int32
			m.SetMCPHookInvoker(func(ctx context.Context, server, tool string, args map[string]any) (MCPHookOutput, error) {
				calls.Add(1)
				if server != "fixture" || tool != "check" || args["path"] != "proof" {
					t.Errorf("wrong MCP invocation: %s %s %+v", server, tool, args)
				}
				return MCPHookOutput{test.text, test.isError}, nil
			})
			scope := &Scope{}
			registration, err := scope.Prepare("mcp", []ClaudeCommand{{Event: "PreToolUse", Handler: "mcp_tool", MCPServer: "fixture", MCPTool: "check", MCPInput: map[string]any{"path": "${tool_input.file_path}"}, Timeout: time.Second, Once: true}})
			if err != nil {
				t.Fatal(err)
			}
			registration.Commit()
			ctx := ContextWithScope(t.Context(), scope)
			results := m.RunPreTool(ctx, "read", map[string]any{"file_path": "proof"})
			if len(results) != 1 {
				t.Fatalf("missing result: %+v", results)
			}
			result := results[0]
			if (result.Error != nil) != test.failed || (result.BlockReason != "") != test.block || (result.StopReason != "") != test.stop || registration.Consumed()[0] == test.failed {
				t.Fatalf("wrong MCP controls: %+v", result)
			}
			m.RunPreTool(ctx, "read", map[string]any{"file_path": "proof"})
			expected := int32(1)
			if test.failed {
				expected = 2
			}
			if calls.Load() != expected {
				t.Fatal("MCP once state was not preserved")
			}
		})
	}
}

func TestMCPHookCancellationAndOwnerGuard(t *testing.T) {
	m := NewManager(true, t.TempDir())
	var calls atomic.Int32
	m.SetMCPHookInvoker(func(ctx context.Context, server, tool string, args map[string]any) (MCPHookOutput, error) {
		calls.Add(1)
		<-ctx.Done()
		return MCPHookOutput{}, ctx.Err()
	})
	hook := &Hook{Enabled: true, Type: PreTool, Claude: &ClaudeCommand{Event: "PreToolUse", Handler: "mcp_tool", MCPServer: "fixture", MCPTool: "check"}}
	result := m.executeHook(t.Context(), hook, NewContext("read", nil, m.workDir), 10*time.Millisecond)
	if !errors.Is(result.Error, context.DeadlineExceeded) || result.StopReason != "" || result.BlockReason != "" {
		t.Fatalf("MCP timeout lost nonblocking contract: %+v", result)
	}
	ctx := ContextWithPromptClient(t.Context(), nil, func() error { return errors.New("revoked") })
	result = m.executeHook(ctx, hook, NewContext("read", nil, m.workDir), time.Second)
	if result.Error == nil || calls.Load() != 1 {
		t.Fatal("MCP hook bypassed owner guard")
	}
}
