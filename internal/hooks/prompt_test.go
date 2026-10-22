package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/config"
	"gopkg.in/yaml.v3"
)

func TestPromptDecisionRejectsAmbiguousOrIncompleteOutput(t *testing.T) {
	for _, text := range []string{`{}`, `{"ok":false}`, `{"ok":null}`, `{"ok":true,"ok":false,"reason":"ambiguous"}`, `{"ok":true} {"ok":false}`, `{"ok":true,"extra":1}`, `[]`, `{"ok":true,"impossible":null}`} {
		if _, _, _, err := parsePromptDecision(text); err == nil {
			t.Fatalf("accepted unsafe decision %s", text)
		}
	}
}

func TestPromptSupportKeepsLegacyCommandFingerprint(t *testing.T) {
	definition := ClaudeCommand{Event: "PreToolUse", Matcher: "Read", Command: "echo proof", Timeout: time.Second, Once: true}
	if got := CommandFingerprint([]ClaudeCommand{definition}); got != "afb31adad716c7d90e4a7e436047662930afc53889f99bd577f4ab36285ae2db" {
		t.Fatalf("command-only saved sessions changed their fingerprint: %s", got)
	}
}

func TestPromptHookSchemaAndFingerprint(t *testing.T) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("PreToolUse:\n- hooks:\n  - type: prompt\n    prompt: Check $ARGUMENTS\n    model: glm-4.7\n    continueOnBlock: true\n    once: true\n"), &node); err != nil {
		t.Fatal(err)
	}
	definitions, err := ParseClaudeHooks(*node.Content[0])
	if err != nil {
		t.Fatal(err)
	}
	d := definitions[0]
	if d.Handler != "prompt" || d.Timeout != 30*time.Second || !d.ContinueOnBlock || !d.Once {
		t.Fatalf("wrong prompt defaults: %+v", d)
	}
	for _, change := range []func(*ClaudeCommand){func(d *ClaudeCommand) { d.Prompt += "changed" }, func(d *ClaudeCommand) { d.Model = "glm-5.2" }, func(d *ClaudeCommand) { d.ContinueOnBlock = false }} {
		copy := d
		change(&copy)
		if CommandFingerprint([]ClaudeCommand{copy}) == CommandFingerprint(definitions) {
			t.Fatal("prompt control was omitted from durable fingerprint")
		}
	}
}

func TestPromptHookRuntimeDecisionsAndIsolatedModel(t *testing.T) {
	for _, test := range []struct {
		name, event, answer  string
		continued            bool
		stop, block, context bool
	}{
		{name: "allow", event: "PreToolUse", answer: `{"ok":true}`},
		{name: "terminal-deny", event: "PreToolUse", answer: `{"ok":false,"reason":"review required"}`, stop: true},
		{name: "adjustable-deny", event: "PreToolUse", answer: `{"ok":false,"reason":"review required"}`, continued: true, block: true},
		{name: "post-stop", event: "PostToolUse", answer: `{"ok":false,"reason":"review required"}`, stop: true},
		{name: "post-context", event: "PostToolUse", answer: `{"ok":false,"reason":"review required"}`, continued: true, context: true},
		{name: "failure-context", event: "PostToolUseFailure", answer: `{"ok":false,"reason":"review required"}`, context: true},
		{name: "stop-continue", event: "Stop", answer: `{"ok":false,"reason":"review required"}`, block: true},
		{name: "impossible-stop", event: "Stop", answer: `{"ok":false,"reason":"cannot be satisfied","impossible":true}`},
		{name: "invalid-output", event: "PreToolUse", answer: `{"ok":true,"ok":false}`, stop: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GOKIN_SMALL_FAST_MODEL", "deepseek-v4-flash") // Explicit hook model wins over an invalid default.
			requests := make(chan map[string]any, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				requests <- request
				w.Header().Set("Content-Type", "text/event-stream")
				chunk, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": test.answer}})
				fmt.Fprintf(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\ndata: %s\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\ndata: {\"type\":\"message_stop\"}\n\n", chunk)
			}))
			defer server.Close()
			base, err := client.NewAnthropicClient(client.AnthropicConfig{APIKey: "synthetic", BaseURL: server.URL, Provider: "glm", Model: config.DefaultGLMModel})
			if err != nil {
				t.Fatal(err)
			}
			manager := NewManager(true, t.TempDir())
			scope := &Scope{}
			registration, err := scope.Prepare("fixture", []ClaudeCommand{{Event: test.event, Handler: "prompt", Prompt: "Check condition $ARGUMENTS", Model: "glm-4.7", Timeout: time.Second, ContinueOnBlock: test.continued, Once: test.name == "allow" || test.name == "terminal-deny"}})
			if err != nil {
				t.Fatal(err)
			}
			registration.Commit()
			counter := &client.TokenUsageCounter{}
			ctx := ContextWithPromptClient(ContextWithScope(client.ContextWithTokenUsageCounter(context.Background(), counter), scope), base, nil)
			results := manager.Run(ctx, claudeEventTypes[test.event], NewContext("write", map[string]any{"file_path": "proof.txt"}, manager.workDir))
			if len(results) != 1 {
				t.Fatalf("missing prompt result: %+v", results)
			}
			result := results[0]
			if (result.StopReason != "") != test.stop || (result.BlockReason != "") != test.block || (result.AdditionalContext != "") != test.context {
				t.Fatalf("wrong prompt control: %+v", result)
			}
			request := <-requests
			if test.name == "allow" || test.name == "terminal-deny" {
				if again := manager.Run(ctx, claudeEventTypes[test.event], NewContext("write", nil, manager.workDir)); len(again) != 0 {
					t.Fatal("successful prompt execution did not consume once state")
				}
			}
			input, _ := counter.Snapshot()
			if input != 10 || request["model"] != "glm-4.7" || request["tools"] != nil || base.GetModel() != config.DefaultGLMModel || !strings.Contains(fmt.Sprint(request["messages"]), "hook_event_name") {
				t.Fatalf("prompt lost isolation, payload or usage: %+v input=%d", request, input)
			}
		})
	}
}
