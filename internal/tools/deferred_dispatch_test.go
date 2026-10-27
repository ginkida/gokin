package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gokin/internal/permission"
	"gokin/internal/testkit"
	"google.golang.org/genai"
)

func deferredCall(name string, args map[string]any) *genai.FunctionCall {
	return &genai.FunctionCall{ID: "wire-id", Name: "use_tool", Args: map[string]any{"name": name, "arguments": args}}
}

func TestDeferredSkillPressurePruningKeepsFullResult(t *testing.T) {
	payload := strings.Repeat("workflow ", 2000)
	call := deferredCall("skill", map[string]any{"name": "check"})
	response := &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: NewSuccessResult(payload).ToMap()}
	history := []*genai.Content{
		genai.NewContentFromText("system", genai.RoleUser),
		genai.NewContentFromText("ready", genai.RoleModel),
		genai.NewContentFromText("task", genai.RoleUser),
		{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: call}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: response}}},
		genai.NewContentFromText("recent", genai.RoleModel),
	}
	if freed := pruneOldToolOutputs(history, 1, 1000); freed != 0 || response.Response["content"] != payload {
		t.Fatal("dispatched skill was pruned", freed)
	}
}

func TestDeferredHistorySearchUsesTargetName(t *testing.T) {
	call := deferredCall("skill", map[string]any{"name": "check"})
	history := []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: call}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: call.ID, Name: call.Name}}}},
	}
	result, err := NewHistorySearchTool(func() []*genai.Content { return history }).Execute(t.Context(), map[string]any{"pattern": "Tool Response: skill"})
	if err != nil || !strings.Contains(result.Content, "Tool Response: skill") {
		t.Fatal("target response missing from search", result, err)
	}
	if history[1].Parts[0].FunctionResponse.Name != "use_tool" {
		t.Fatal("search changed wire history")
	}
}

func TestDeferredDispatchRetainsWireAndUsesTargetPolicies(t *testing.T) {
	for _, policy := range []string{"allowed", "empty-capability", "capability", "schema", "plan", "deny", "cancelled"} {
		t.Run(policy, func(t *testing.T) {
			root := testkit.ResolvedTempDir(t)
			registry := NewRegistry()
			registry.MustRegister(NewMkdirTool(root))
			e := NewExecutor(registry, nil, time.Second)
			e.SetWorkDir(root)
			ctx := ContextWithDeferredCatalog(t.Context(), registry, registry.GeminiTools())
			ctx = ContextWithToolSchemaCeiling(ctx, e, []string{"mkdir"})
			switch policy {
			case "empty-capability":
				ctx = ContextWithToolCapabilityCeiling(ctx, []string{})
			case "capability":
				ctx = ContextWithToolCapabilityCeiling(ctx, []string{"read"})
			case "schema":
				ctx = ContextWithToolSchemaCeiling(ctx, e, []string{"read"})
			case "plan":
				e.SetPlanModeCheck(func() bool { return true })
			case "deny":
				rules := permission.DefaultRules()
				rules.SetPolicy("mkdir", permission.LevelDeny)
				e.SetPermissions(permission.NewManager(rules, true))
			case "cancelled":
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelCtx
			}
			path := filepath.Join(root, "created")
			call := deferredCall("mkdir", map[string]any{"path": path})
			before, _ := json.Marshal(call)
			results, err := e.executeTools(ctx, []*genai.FunctionCall{call})
			if err != nil || len(results) != 1 || results[0].Name != "use_tool" || results[0].ID != call.ID {
				t.Fatalf("pair=%v err=%v", results, err)
			}
			want := policy == "allowed"
			if results[0].Response["success"] != want {
				t.Fatalf("result=%+v", results[0])
			}
			_, statErr := os.Stat(path)
			if (statErr == nil) != want {
				t.Fatalf("unexpected filesystem effect: %v", statErr)
			}
			after, _ := json.Marshal(call)
			if string(before) != string(after) {
				t.Fatal("wire envelope mutated")
			}
			if want {
				entries := e.GetCheckpointJournal().Entries()
				if len(entries) != 1 || entries[0].ToolName != "mkdir" || entries[0].CallID != call.ID {
					t.Fatalf("checkpoint lost effective identity: %+v", entries)
				}
				// Retry uses the original ID/target signature and reuses the result.
				e.PrepareSideEffectRecovery(entries)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				replayed, _ := e.executeTools(ctx, []*genai.FunctionCall{call})
				if replayed[0].Response["success"] != true {
					t.Fatal(replayed)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("retry repeated the mutation")
				}
			}
		})
	}
}

func TestDeferredDispatchRejectsMalformedAndRecursiveCalls(t *testing.T) {
	registry := NewRegistry()
	e := NewExecutor(registry, nil, time.Second)
	ctx := ContextWithDeferredCatalog(t.Context(), registry, nil)
	for _, args := range []map[string]any{
		nil, {"name": "read", "arguments": "{}"}, {"name": "use_tool", "arguments": map[string]any{}},
		{"name": "tool_search", "arguments": map[string]any{}}, {"name": " read", "arguments": map[string]any{}},
		{"name": "read", "arguments": map[string]any{}, "extra": true},
	} {
		call := &genai.FunctionCall{ID: "invalid", Name: "use_tool", Args: args}
		results, err := e.executeTools(ctx, []*genai.FunctionCall{call})
		if err != nil || results[0].Response["success"] != false {
			t.Fatalf("malformed call executed: %v %v", results, err)
		}
	}
	call := deferredCall("missing", map[string]any{})
	results, _ := e.executeTools(ctx, []*genai.FunctionCall{call})
	if !strings.Contains(results[0].Response["error"].(string), "unknown tool") {
		t.Fatal(results)
	}
	if resolved := ResolveDeferredCalls(t.Context(), registry, []*genai.FunctionCall{call}); resolved[0] != call {
		t.Fatal("full mode accepted virtual dispatch")
	}
}

func TestDeferredDiscoveryUsesEligibleCatalogAndCompleteSchemas(t *testing.T) {
	registry := DefaultRegistry(testkit.ResolvedTempDir(t))
	ctx := ContextWithDeferredCatalog(t.Context(), registry, registry.GeminiTools())
	ctx = ContextWithToolCapabilityCeiling(ctx, []string{"git_status"})
	search, ok := DeferredMetaTool(ctx, registry, "tool_search")
	if !ok {
		t.Fatal("missing search")
	}
	result, err := search.Execute(ctx, map[string]any{"query": "git"})
	if err != nil || !result.Success {
		t.Fatal(result, err)
	}
	lines := strings.Split(strings.TrimSpace(result.Content), "\n")
	if len(lines) != 2 {
		t.Fatal(result.Content)
	}
	var declaration genai.FunctionDeclaration
	if err := json.Unmarshal([]byte(lines[1]), &declaration); err != nil || declaration.Name != "git_status" || declaration.Parameters == nil {
		t.Fatalf("incomplete schema: %+v %v", declaration, err)
	}
	if _, ok := DeferredMetaTool(ctx, NewRegistry(), "tool_search"); ok {
		t.Fatal("child used the parent's discovery context")
	}
	ctx = ContextWithDeferredCatalog(ctx, registry, nil)
	result, _ = search.Execute(ctx, map[string]any{"query": "git_status"})
	if strings.Contains(result.Content, `"name":"git_status"`) {
		t.Fatal("discovery ignored feature policy")
	}
}

func TestDeferredEffectiveCallsDriveClassificationAndRecovery(t *testing.T) {
	r := NewRegistry()
	ctx := ContextWithDeferredCatalog(t.Context(), r, nil)
	wire := []*genai.FunctionCall{deferredCall("read", map[string]any{"file_path": "a"}), deferredCall("edit", map[string]any{"file_path": "a"}), deferredCall("read", map[string]any{"file_path": "b"})}
	calls := ResolveDeferredCalls(ctx, r, wire)
	groups := ClassifyToolDependencies(calls)
	if len(groups) != 3 || groups[1].Calls[0].Name != "edit" {
		t.Fatalf("mutation ordering lost: %+v", groups)
	}
	if maxStagnationRecoveryAttempts(calls[0].Name) != 3 || !shouldAttemptStagnationRecovery(calls[:1], 0) {
		t.Fatal("read recovery lost")
	}
	if reflect.DeepEqual(calls[0], wire[0]) || stagnationFingerprint(calls[0].Name, calls[0].Args) == stagnationFingerprint(calls[2].Name, calls[2].Args) {
		t.Fatal("effective fingerprints collapsed")
	}
}

func TestDeferredCheckpointAcknowledgesRetainedWirePair(t *testing.T) {
	r := NewRegistry()
	ctx := ContextWithDeferredCatalog(t.Context(), r, nil)
	wire := deferredCall("write", map[string]any{"file_path": "a.go", "content": "package a"})
	journal := NewCheckpointJournal()
	result := NewSuccessResult("written")
	journal.Record(ResolveDeferredCalls(ctx, r, []*genai.FunctionCall{wire})[0], result)
	history := []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: wire}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: wire.ID, Name: wire.Name, Response: result.ToMap()}}}},
	}
	if n := journal.AcknowledgeResults(history); n != 1 || len(journal.Entries()) != 0 {
		t.Fatal("retained dispatcher result left a stale mutation checkpoint", n, journal.Entries())
	}
}

func TestDeferredTargetHookRewritesBeforePermissionAndPreservesEnvelope(t *testing.T) {
	output := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"file_path":"TEST_ROOT/actual.txt","content":"actual"}}}`
	e, _, root, _ := settingsExecutorFixture(t, "printf '%s' '"+output+"'")
	ctx := ContextWithDeferredCatalog(t.Context(), e.registry, e.registry.GeminiTools())
	call := deferredCall("write", map[string]any{"file_path": filepath.Join(root, "original.txt"), "content": "original"})
	var observed string
	e.SetPhaseObserver(func(name string, _ time.Duration, _ bool) { observed = name })
	results, err := e.executeTools(ctx, []*genai.FunctionCall{call})
	if err != nil || results[0].Response["success"] != true || observed != "write" {
		t.Fatal(results, err, observed)
	}
	if call.Args["arguments"].(map[string]any)["content"] != "original" {
		t.Fatal("hook rewrote provider envelope")
	}
	if data, err := os.ReadFile(filepath.Join(root, "actual.txt")); err != nil || string(data) != "actual" {
		t.Fatal(string(data), err)
	}
}
