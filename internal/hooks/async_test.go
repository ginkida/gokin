package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genai"
)

func awaitAsyncTest(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("async condition did not become true")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAsyncSchemaFingerprintAndOutputFields(t *testing.T) {
	definitions, err := parseClaudeTest(t, `PreToolUse: [{hooks: [{type: command, command: echo test, async: true, asyncRewake: true, once: true}]}]`)
	if err != nil || !definitions[0].Async || !definitions[0].AsyncRewake {
		t.Fatalf("async schema: %+v %v", definitions, err)
	}
	for _, field := range []string{"async", "asyncRewake"} {
		if _, err := parseClaudeTest(t, `PreToolUse: [{hooks: [{type: prompt, prompt: check, `+field+`: false}]}]`); err == nil {
			t.Fatal("model hook accepted async field")
		}
		copy := CloneClaudeCommands(definitions)
		if field == "async" {
			copy[0].Async = false
		} else {
			copy[0].AsyncRewake = false
		}
		if CommandFingerprint(copy) == CommandFingerprint(definitions) {
			t.Fatal("async contract omitted from fingerprint")
		}
	}
	for _, test := range []struct{ stdout, want string }{
		{`{"continue":false,"decision":"block","systemMessage":"notice","hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","additionalContext":"context"}}`, "notice\ncontext"},
		{"log line\n{\"systemMessage\":\"last report\"}\nlog trailing", "last report"},
		{"{\n\"systemMessage\":\"pretty report\"\n}", "pretty report"},
		{`{"systemMessage":false,"hookSpecificOutput":{"additionalContext":"valid sibling"}}`, "valid sibling"},
		{`{"systemMessage":"first","systemMessage":"second"}`, ""},
		{`{"hookSpecificOutput":{"hookEventName":"Stop","additionalContext":"wrong event"}}`, ""},
	} {
		if got := parseAsyncOutput(test.stdout, "PreToolUse"); got != test.want {
			t.Fatalf("async output=%q want=%q", got, test.want)
		}
	}
}

func TestAsyncLaunchDoesNotWaitOrBlockAndIgnoresTimeout(t *testing.T) {
	root := t.TempDir()
	scope := NewAsyncScope(t.Context(), nil)
	defer scope.Close()
	ctx := ContextWithAsyncScope(t.Context(), scope)
	m := NewManager(true, root)
	hooksScope := &Scope{}
	command := `while [ ! -f release ]; do sleep 0.01; done; printf '%s' '{"continue":false,"decision":"block","systemMessage":"ASYNC_NOTICE","hookSpecificOutput":{"additionalContext":"ASYNC_CONTEXT"}}'; exit 2`
	registration, err := hooksScope.Prepare("async", []ClaudeCommand{{Event: "PreToolUse", Command: command, Async: true, Timeout: 10 * time.Millisecond, OnFailure: "block"}})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
	ctx = ContextWithScope(ctx, hooksScope)
	results := m.RunPreTool(ctx, "write", map[string]any{"file_path": "proof"})
	if len(results) != 1 || results[0].Error != nil || results[0].asyncJob == nil || results[0].StopReason != "" || results[0].BlockReason != "" {
		t.Fatalf("async action was blocked or awaited: %+v", results)
	}
	time.Sleep(30 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(root, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	var notes string
	awaitAsyncTest(t, func() bool { notes = TakeAsyncNotes(ctx); return notes != "" })
	if !strings.Contains(notes, "ASYNC_NOTICE") || !strings.Contains(notes, "ASYNC_CONTEXT") || strings.Contains(notes, "continue") || scope.HasRewake() {
		t.Fatalf("async result leaked decisions or lost notes: %s", notes)
	}
	if again := TakeAsyncNotes(ctx); again != "" {
		t.Fatal("async notes delivered more than once")
	}
}

func TestAsyncOnceIsClaimedUntilSuccessfulCompletion(t *testing.T) {
	root := t.TempDir()
	scope := NewAsyncScope(t.Context(), nil)
	defer scope.Close()
	ctx := ContextWithAsyncScope(t.Context(), scope)
	m := NewManager(true, root)
	owned := &Scope{}
	command := `while [ ! -f release ]; do sleep 0.01; done; if [ ! -f retry ]; then exit 1; fi; printf '%s' '{"systemMessage":"success"}'`
	registration, err := owned.Prepare("once", []ClaudeCommand{{Event: "PreToolUse", Command: command, Async: true, Once: true, Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
	ctx = ContextWithScope(ctx, owned)
	first := m.RunPreTool(ctx, "write", nil)
	if len(first) != 1 || registration.Consumed()[0] {
		t.Fatalf("async once consumed at launch: %+v", first)
	}
	if again := m.RunPreTool(ctx, "write", nil); len(again) != 0 {
		t.Fatal("in-flight once launched again")
	}
	_ = os.WriteFile(filepath.Join(root, "release"), nil, 0600)
	failed := make(chan Result, 1)
	first[0].asyncJob.observe(func(result Result) { failed <- result })
	if result := <-failed; result.Error == nil {
		t.Fatal("first async run should fail")
	}
	awaitAsyncTest(t, func() bool { owned.mu.Lock(); defer owned.mu.Unlock(); return !owned.groups["once"][0].claimed })
	if registration.Consumed()[0] {
		t.Fatal("failed async run consumed once")
	}
	_ = os.WriteFile(filepath.Join(root, "retry"), nil, 0600)
	second := m.RunPreTool(ctx, "write", nil)
	if len(second) != 1 {
		t.Fatal("failed async once did not retry")
	}
	awaitAsyncTest(t, func() bool { return registration.Consumed()[0] })
	if again := m.RunPreTool(ctx, "write", nil); len(again) != 0 {
		t.Fatal("successful async once rearmed")
	}
}

func TestAsyncRewakeExitTwoAndTimeoutCancellation(t *testing.T) {
	for _, mode := range []string{"rewake", "timeout", "cancel", "revoke"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			var wakes atomic.Int32
			scope := NewAsyncScope(t.Context(), func() { wakes.Add(1) })
			defer scope.Close()
			ctx := ContextWithAsyncScope(t.Context(), scope)
			m := NewManager(true, root)
			command := `printf 'attention' >&2; exit 2`
			timeout := time.Second
			if mode != "rewake" {
				command = `touch started; sleep 2; printf leaked > leaked`
				if mode == "timeout" {
					timeout = 20 * time.Millisecond
				}
			}
			hook := &Hook{Name: "async", Type: PreTool, Enabled: true, Claude: &ClaudeCommand{Event: "PreToolUse", Command: command, AsyncRewake: true, Timeout: timeout}, Command: command}
			result := m.executeHook(ctx, hook, NewContext("write", nil, root), timeout)
			if result.Error != nil || result.asyncJob == nil {
				t.Fatalf("async start: %+v", result)
			}
			if mode == "cancel" {
				scope.Cancel()
			}
			if mode == "revoke" {
				m.SetEnabled(false)
			}
			finished := make(chan Result, 1)
			result.asyncJob.observe(func(result Result) { finished <- result })
			select {
			case result := <-finished:
				if mode == "timeout" && !errors.Is(result.Error, context.DeadlineExceeded) {
					t.Fatalf("rewake timeout lost: %v", result.Error)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("async process did not finish")
			}
			if mode == "rewake" {
				awaitAsyncTest(t, func() bool { return wakes.Load() == 1 })
				if !scope.HasRewake() || !strings.Contains(TakeAsyncNotes(ctx), "attention") {
					t.Fatal("rewake result not queued")
				}
			} else {
				if wakes.Load() != 0 {
					t.Fatal("cancelled hook woke model")
				}
				if _, err := os.Stat(filepath.Join(root, "leaked")); !os.IsNotExist(err) {
					t.Fatal("cancelled hook continued its side effect")
				}
			}
		})
	}
}

func TestAsyncSettingsResultRetainsOnlyCurrentHandlerAuthority(t *testing.T) {
	for _, change := range []string{"unrelated", "removed", "changed"} {
		t.Run(change, func(t *testing.T) {
			root, user := t.TempDir(), t.TempDir()
			manager := NewManager(true, root)
			manager.ConfigureClaudeSettings(root, user, true)
			path := filepath.Join(user, "settings.json")
			handler := map[string]any{"type": "command", "command": `printf '%s' '{"systemMessage":"current evidence"}'; exit 2`, "asyncRewake": true}
			document := map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Read", "hooks": []any{handler}}}}}
			writeClaudeSettingsTestFile(t, path, document)
			scope := NewAsyncScope(t.Context(), nil)
			defer scope.Close()
			ctx := ContextWithAsyncScope(t.Context(), scope)
			results := manager.RunPreTool(ctx, "read", nil)
			if len(results) != 1 || results[0].Error != nil {
				t.Fatalf("async settings launch: %+v", results)
			}
			awaitAsyncTest(t, scope.HasRewake)
			guard := scope.RewakeGuard()
			switch change {
			case "unrelated":
				document["env"] = map[string]string{"UNRELATED": "value"}
			case "removed":
				delete(document, "hooks")
			case "changed":
				handler["command"] = "printf replacement"
			}
			writeClaudeSettingsTestFile(t, path, document)
			guardCtx := ContextWithAsyncRewakeGuard(ctx, guard)
			if change == "unrelated" {
				if !scope.HasRewake() || AsyncRewakeRequestGuard(guardCtx) != nil || !strings.Contains(TakeAsyncNotes(ctx), "current evidence") {
					t.Fatal("unrelated settings edit discarded a current handler")
				}
			} else if scope.HasRewake() || AsyncRewakeRequestGuard(guardCtx) == nil || TakeAsyncNotes(ctx) != "" {
				t.Fatal("stale handler retained notes or automatic request authority")
			}
		})
	}
}

func TestAsyncInjectPreservesHistoryAndToolPairs(t *testing.T) {
	prior := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "call", Name: "read", Response: map[string]any{"content": "proof"}}}}}
	history := []*genai.Content{genai.NewContentFromText("original", genai.RoleModel), prior}
	updated := InjectAsyncText(history, "background evidence")
	if updated[1].Parts[0].FunctionResponse.ID != "call" || updated[1].Parts[0].FunctionResponse.Response["async_hook_context"] != "background evidence" || prior.Parts[0].FunctionResponse.Response["async_hook_context"] != nil {
		t.Fatal("async delivery mutated prior history or broke pairing")
	}
}
