package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestClaudeOnceCancellationReleasesClaimAndKillsCommand(t *testing.T) {
	root := t.TempDir()
	scope := &Scope{}
	registration, err := scope.Prepare("cancel", []ClaudeCommand{{Event: "PreToolUse", Once: true, Timeout: time.Minute,
		Command: "if [ ! -f started ]; then touch started; sleep 30; printf leaked > leaked; fi; printf retried"}})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
	manager := NewManager(true, root)
	ctx, cancel := context.WithCancel(ContextWithScope(t.Context(), scope))
	defer cancel()
	finished := make(chan []Result, 1)
	go func() { finished <- manager.RunPreTool(ctx, "bash", nil) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hook did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case results := <-finished:
		if len(results) != 1 || results[0].Error == nil {
			t.Fatalf("cancelled result=%+v", results)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled command did not exit")
	}
	results := manager.RunPreTool(ContextWithScope(t.Context(), scope), "bash", nil)
	if len(results) != 1 || results[0].Error != nil || results[0].Output != "retried" {
		t.Fatalf("retry=%+v", results)
	}
	if _, err := os.Stat(filepath.Join(root, "leaked")); !os.IsNotExist(err) {
		t.Fatal("cancelled hook continued its side effect")
	}
}

func TestClaudeEffortEnvironmentDoesNotInheritAnotherInvocation(t *testing.T) {
	t.Setenv("CLAUDE_EFFORT", "max")
	root := t.TempDir()
	scope := &Scope{}
	registration, err := scope.Prepare("env", []ClaudeCommand{{Event: "PreToolUse", Command: `printf '%s' "${CLAUDE_EFFORT-unset}"`, Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
	results := NewManager(true, root).RunPreTool(ContextWithScope(t.Context(), scope), "bash", nil)
	if len(results) != 1 || results[0].Output != "unset" {
		t.Fatalf("stale environment: %+v", results)
	}
}

func TestClaudeInheritedOnceClaimIsSharedAndParentStopStaysLocal(t *testing.T) {
	root := t.TempDir()
	parent, child := &Scope{}, &Scope{}
	registration, err := parent.Prepare("parent", []ClaudeCommand{
		{Event: "PreToolUse", Command: "printf x >> runs", Once: true, Timeout: time.Second},
		{Event: "Stop", Command: "touch parent-stop", Timeout: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
	child.Inherit(parent)
	manager := NewManager(true, root)
	var work sync.WaitGroup
	for i := range 20 {
		scope := parent
		if i%2 == 0 {
			scope = child
		}
		work.Add(1)
		go func() { defer work.Done(); manager.RunPreTool(ContextWithScope(t.Context(), scope), "bash", nil) }()
	}
	work.Wait()
	data, err := os.ReadFile(filepath.Join(root, "runs"))
	if err != nil || string(data) != "x" {
		t.Fatalf("once executions=%q err=%v", data, err)
	}
	if results := manager.RunStop(ContextWithScope(t.Context(), child), "Child finished."); len(results) != 0 {
		t.Fatal("child executed the parent's Stop hook")
	}
	child.Clear()
	if results := manager.RunStop(ContextWithScope(t.Context(), parent), "Parent finished."); len(results) != 1 || results[0].Error != nil {
		t.Fatalf("parent Stop was cleared: %+v", results)
	}
}

func parseClaudeTest(t *testing.T, document string) ([]ClaudeCommand, error) {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(document), &node); err != nil {
		return nil, err
	}
	return ParseClaudeHooks(*node.Content[0])
}

func TestClaudeHookValidationRejectsUnimplementedControls(t *testing.T) {
	for _, document := range []string{
		"[]", "PreToolUse: {}", "PreToolUse: [{hooks: []}]",
		"UnsupportedEvent: [{hooks: [{type: command, command: echo x}]}]",
		"PreToolUse: [{matcher: '[', hooks: [{type: command, command: echo x}]}]",
		"Stop: [{matcher: Bash, hooks: [{type: command, command: echo x}]}]",
		"PreToolUse: [{hooks: [{type: unsupported-handler, prompt: Check this}]}]",
		"PreToolUse: [{hooks: [{type: prompt, prompt: Check this, command: echo x}]}]",
		"PreToolUse: [{hooks: [{type: command, command: echo x, unknownControl: true}]}]",
		"PreToolUse: [{hooks: [{type: command, command: echo x, timeout: .nan}]}]",
		"PreToolUse: [{hooks: [{type: command, command: echo x, timeout: 0}]}]",
	} {
		if _, err := parseClaudeTest(t, document); err == nil {
			t.Errorf("accepted unsupported definition %s", document)
		}
	}
	definitions, err := parseClaudeTest(t, "PreToolUse: [{matcher: 'Write|Edit', hooks: [{type: command, command: sh, args: [], timeout: 1.5, once: true}]}]")
	if err != nil || len(definitions) != 1 || definitions[0].Args == nil || definitions[0].Timeout != 1500*time.Millisecond || !definitions[0].Once {
		t.Fatalf("definitions=%+v err=%v", definitions, err)
	}
}

func TestClaudeHookReceivesJSONAndPreservesShellScriptSemantics(t *testing.T) {
	root := t.TempDir()
	scope := &Scope{}
	definition := ClaudeCommand{Event: "PreToolUse", Matcher: "^Bash$", Command: `cat > input.json; value=hook-value; printf '%s' "$value" > shell-value; printf '%s' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"blocked correctly","additionalContext":"Review the result"}}'`, Timeout: time.Second}
	registration, err := scope.Prepare("review", []ClaudeCommand{definition})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
	manager := NewManager(true, root)
	ctx := ContextWithRuntimeInfo(ContextWithScope(t.Context(), scope), RuntimeInfo{SessionID: "session-1", ToolUseID: "call-1", Effort: "max"})
	args := map[string]any{"command": "printf '%s' '$(touch forbidden)'"}
	results := manager.RunPreTool(ctx, "bash", args)
	blocked, ok := Blocked(results)
	if !ok || blocked.Error != nil || blocked.Output != "blocked correctly" || AdditionalContext(results) != "Review the result" {
		t.Fatalf("results=%+v", results)
	}
	data, err := os.ReadFile(filepath.Join(root, "input.json"))
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	if input["session_id"] != "session-1" || input["tool_use_id"] != "call-1" || input["tool_name"] != "Bash" || input["hook_event_name"] != "PreToolUse" || input["cwd"] != root {
		t.Fatalf("input=%v", input)
	}
	if input["tool_input"].(map[string]any)["command"] != args["command"] {
		t.Fatal("input was expanded as shell")
	}
	if value, err := os.ReadFile(filepath.Join(root, "shell-value")); err != nil || string(value) != "hook-value" {
		t.Fatalf("shell expansion=%q err=%v", value, err)
	}
	if _, err := os.Stat(filepath.Join(root, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("tool input executed as shell")
	}
	if len(manager.GetHooks()) != 0 {
		t.Fatal("registration changed global hooks")
	}
	if unmatched := manager.RunPreTool(ctx, "read", nil); len(unmatched) != 0 {
		t.Fatal("matcher did not filter tools")
	}
}

func TestClaudeOnceHookClaimsConcurrentCallsAndRetriesFailures(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(true, root)
	scope := &Scope{}
	definition := ClaudeCommand{Event: "PreToolUse", Command: "printf x >> runs", Timeout: time.Second, Once: true}
	registration, err := scope.Prepare("once", []ClaudeCommand{definition})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
	ctx := ContextWithScope(t.Context(), scope)
	var work sync.WaitGroup
	for range 20 {
		work.Add(1)
		go func() { defer work.Done(); manager.RunPreTool(ctx, "bash", nil) }()
	}
	work.Wait()
	data, err := os.ReadFile(filepath.Join(root, "runs"))
	if err != nil || string(data) != "x" {
		t.Fatalf("once executions=%q err=%v", data, err)
	}
	definition.Command = "if [ ! -f first-failure ]; then touch first-failure; exit 1; fi; printf success"
	registration, err = scope.Prepare("once", []ClaudeCommand{definition})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
	first := manager.RunPreTool(ctx, "bash", nil)
	second := manager.RunPreTool(ctx, "bash", nil)
	third := manager.RunPreTool(ctx, "bash", nil)
	if len(first) != 1 || first[0].Error == nil || len(second) != 1 || second[0].Error != nil || len(third) != 0 {
		t.Fatalf("first=%+v second=%+v third=%+v", first, second, third)
	}
}

func TestClaudeHookReservationsAndClearCannotActivateAbandonedLoads(t *testing.T) {
	scope := &Scope{}
	definition := []ClaudeCommand{{Event: "PreToolUse", Command: "exit 0", Timeout: time.Second}}
	registration, err := scope.Prepare("test", definition)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope.Hooks()) != 0 {
		t.Fatal("preparation activated hook")
	}
	registration.Cancel()
	registration.Commit()
	if len(scope.Hooks()) != 0 {
		t.Fatal("cancelled preparation activated hook")
	}
	registration, err = scope.Prepare("test", definition)
	if err != nil {
		t.Fatal(err)
	}
	scope.Clear()
	registration.Commit()
	if len(scope.Hooks()) != 0 {
		t.Fatal("pre-clear preparation activated in new session")
	}
}

func TestClaudeExitStatusAndExecForm(t *testing.T) {
	for _, tc := range []struct {
		script  string
		blocked bool
		reason  string
	}{
		{`printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"json denial"}}'; exit 1`, true, "json denial"},
		{`printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'; printf 'exit denial' >&2; exit 2`, true, "exit denial"},
		{"printf error >&2; exit 1", false, ""},
		{`printf '{invalid JSON'; printf 'exit denial' >&2; exit 2`, true, "exit denial"},
	} {
		root := t.TempDir()
		scope := &Scope{}
		registration, err := scope.Prepare("test", []ClaudeCommand{{Event: "PreToolUse", Command: "sh", Args: []string{"-c", tc.script}, Timeout: time.Second}})
		if err != nil {
			t.Fatal(err)
		}
		registration.Commit()
		results := NewManager(true, root).RunPreTool(ContextWithScope(t.Context(), scope), "bash", nil)
		blocked, ok := Blocked(results)
		if ok != tc.blocked || (ok && !strings.Contains(blocked.Output, tc.reason)) {
			t.Fatalf("script=%q results=%+v", tc.script, results)
		}
	}
}
