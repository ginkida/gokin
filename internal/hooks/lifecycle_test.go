package hooks

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPromptSubmitAndSubagentStartSchemaAndPayload(t *testing.T) {
	definitions, err := parseClaudeTest(t, `UserPromptSubmit: [{matcher: Bash, hooks: [{type: command, command: echo context}]}]
SubagentStart: [{matcher: Explore, hooks: [{type: command, command: echo ignored}]}]`)
	if err != nil || len(definitions) != 2 || definitions[0].Matcher != "" || definitions[0].Timeout != 30*time.Second {
		t.Fatalf("lifecycle schema: %+v %v", definitions, err)
	}
	for _, kind := range []string{"prompt", "agent"} {
		if _, err := parseClaudeTest(t, `SubagentStart: [{hooks: [{type: `+kind+`, prompt: verify}]}]`); err == nil {
			t.Fatal("SubagentStart accepted model handler")
		}
	}
	ctx := ContextWithRuntimeInfo(t.Context(), RuntimeInfo{SessionID: "session", AgentID: "child", AgentType: "explore", SessionTitle: "Review"})
	for _, definition := range definitions {
		hook := &Hook{Type: claudeEventTypes[definition.Event], Claude: &definition}
		hctx := NewContext("unexpected-tool", nil, "/workspace")
		hctx.Prompt = "/deploy '$HOME'"
		data, _, err := claudeInput(ctx, hook, hctx)
		var input map[string]any
		if err != nil || json.Unmarshal(data, &input) != nil || input["tool_name"] != nil || input["hook_event_name"] != definition.Event {
			t.Fatalf("lifecycle input: %s %v", data, err)
		}
		if definition.Event == "UserPromptSubmit" && (input["prompt"] != hctx.Prompt || input["session_title"] != "Review") {
			t.Fatalf("lost prompt/title: %s", data)
		}
		if definition.Event == "SubagentStart" && input["agent_type"] != "Explore" {
			t.Fatalf("wrong child identity: %s", data)
		}
	}
}

func TestPromptSubmitBlockingAndContext(t *testing.T) {
	for _, test := range []struct {
		stdout       string
		exit         int
		block, notes string
		suppress     bool
	}{
		{"plain evidence", 0, "", "plain evidence", false},
		{`{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"JSON evidence","sessionTitle":"New title"}}`, 0, "", "JSON evidence", false},
		{`{"decision":"block","reason":"policy","hookSpecificOutput":{"suppressOriginalPrompt":true}}`, 0, "policy", "", true},
		{`{"hookSpecificOutput":{"suppressOriginalPrompt":true}}`, 2, "stderr policy", "", true},
		{`{broken`, 2, "stderr policy", "", false},
	} {
		hook := &Hook{Type: UserPromptSubmit, Claude: &ClaudeCommand{Event: "UserPromptSubmit"}}
		result := interpretClaudeResult(Result{Hook: hook}, test.stdout, "stderr policy", test.exit)
		if result.BlockReason != test.block || result.AdditionalContext != test.notes || result.SuppressOriginalPrompt != test.suppress {
			t.Fatalf("prompt verdict: %+v", result)
		}
	}
}

func TestSubagentStartMatchesChildAndIgnoresBlockingControls(t *testing.T) {
	root, user := t.TempDir(), t.TempDir()
	m := NewManager(true, root)
	m.ConfigureClaudeSettings(root, user, true)
	writeClaudeSettingsTestFile(t, user+"/settings.json", map[string]any{"hooks": map[string]any{"SubagentStart": []any{
		map[string]any{"matcher": "Explore", "hooks": []any{map[string]any{"type": "command", "command": `printf '%s' '{"continue":false,"decision":"block","hookSpecificOutput":{"additionalContext":"child evidence"}}'`}}},
		map[string]any{"matcher": "Plan", "hooks": []any{map[string]any{"type": "command", "command": "exit 2"}}},
	}}})
	ctx := ContextWithRuntimeInfo(t.Context(), RuntimeInfo{AgentID: "child", AgentType: "explore"})
	results := m.RunSubagentStart(ctx, root)
	if len(results) != 1 || results[0].BlockReason != "" || results[0].StopReason != "" || !strings.Contains(AdditionalContext(results), "child evidence") {
		t.Fatalf("start matcher/control: %+v", results)
	}
}
