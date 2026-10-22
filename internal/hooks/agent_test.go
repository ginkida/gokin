package hooks

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAgentHookSchemaAndDecisionContract(t *testing.T) {
	definitions, err := parseClaudeTest(t, "PreToolUse: [{hooks: [{type: agent, prompt: Check proof, model: haiku, once: true}]}]")
	if err != nil {
		t.Fatal(err)
	}
	if definitions[0].Handler != "agent" || definitions[0].Timeout != time.Minute || !definitions[0].Once {
		t.Fatalf("wrong agent hook contract: %+v", definitions)
	}
	for _, document := range []string{
		"PreToolUse: [{hooks: [{type: agent, prompt: Check proof, continueOnBlock: false}]}]",
		"PreToolUse: [{hooks: [{type: agent, prompt: Check proof, onFailure: block}]}]",
		"PreToolUse: [{hooks: [{type: agent, prompt: Check proof, command: true}]}]",
		"PreToolUse: [{hooks: [{type: agent, prompt: Check proof, args: []}]}]",
		"PreToolUse: [{hooks: [{type: agent, prompt: ''}]}]",
	} {
		if _, err := parseClaudeTest(t, document); err == nil {
			t.Fatalf("invalid agent hook accepted: %s", document)
		}
	}
	for _, text := range []string{`{"ok":true,"impossible":false}`, `{"ok":false,"reason":"missing","impossible":true}`, `{"ok":true,"ok":false}`, `{"ok":false}`} {
		if _, _, _, err := parseModelHookDecision(text, false); err == nil {
			t.Fatalf("invalid agent decision accepted: %s", text)
		}
	}
}

func TestModelHookPromptEscapingAndSinglePass(t *testing.T) {
	plugin := &claudePluginRuntime{Options: map[string]string{"token": "$ARGUMENTS ${CLAUDE_PROJECT_DIR}"}}
	input := []byte(`{"file_path":"${user_config.token}"}`)
	prompt := modelHookPrompt(`Verify \$1.00 and $ARGUMENTS using ${user_config.token}`, RuntimeInfo{ProjectDir: "project"}, plugin, input)
	if !strings.Contains(prompt, `Verify $1.00 and {"file_path":"${user_config.token}"} using $ARGUMENTS ${CLAUDE_PROJECT_DIR}`) {
		t.Fatalf("inserted prompt data was expanded again: %s", prompt)
	}
	if got := modelHookPrompt(`Literal \$ARGUMENTS`, RuntimeInfo{}, nil, input); !strings.HasPrefix(got, "Literal $ARGUMENTS\n\nHook input (data):") {
		t.Fatalf("escaped placeholder suppressed event input: %s", got)
	}
}

func TestVerifierSkipsRecursiveAgentHooksAndKeepsOtherChecks(t *testing.T) {
	scope := &Scope{}
	registration, err := scope.Prepare("verification", []ClaudeCommand{{Event: "PreToolUse", Handler: "agent", Prompt: "verify", Timeout: time.Second}, {Event: "PreToolUse", Command: "printf native-check", Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
	ctx := ContextWithScope(context.WithValue(t.Context(), agentHookKey{}, true), scope)
	results := NewManager(true, t.TempDir()).RunPreTool(ctx, "read", nil)
	if len(results) != 1 || results[0].Error != nil || results[0].Output != "native-check" {
		t.Fatalf("verification recursed or lost command checks: %+v", results)
	}
	manager := NewManager(true, t.TempDir())
	hook := &Hook{Type: PreTool, Enabled: true, Claude: &ClaudeCommand{Handler: "agent", Prompt: "nested verifier"}}
	if result := manager.executeHook(ctx, hook, NewContext("read", nil, manager.workDir), time.Second); !result.skipped || result.Error != nil {
		t.Fatalf("directly registered verifier recursed: %+v", result)
	}
}
