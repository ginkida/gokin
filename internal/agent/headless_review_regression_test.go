package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/hooks"
	"gokin/internal/permission"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"google.golang.org/genai"
)

func TestHeadlessNestedAgentDenialIsTracked(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(map[bool]string{false: "continue", true: "strict"}[strict], func(t *testing.T) {
			work := testkit.ResolvedTempDir(t)
			registry := tools.NewRegistry()
			registry.MustRegister(tools.NewWriteTool(work))
			m := permission.NewManager(nil, true)
			m.SetRunToolRules(nil, []string{"write"})
			a := NewAgent(AgentTypeGeneral, nil, registry, work, 3, "", m, nil)
			ctx, state := tools.ContextWithHeadlessPermissions(t.Context(), strict, "dontAsk")
			r := a.executeNestedToolResultSafely(ctx, &genai.FunctionCall{ID: "hook-denied", Name: "write", Args: map[string]any{"file_path": "denied.txt", "content": "denied"}})
			if r.Success || len(state.Denials()) != 1 || (tools.PermissionStopError(ctx) != nil) != strict || (a.policyBlockSnapshot() != nil) != strict {
				t.Fatalf("nested result=%+v denials=%v block=%v", r, state.Denials(), a.policyBlockSnapshot())
			}
		})
	}
}
func TestHeadlessDenialDoesNotRunReflection(t *testing.T) {
	work := testkit.ResolvedTempDir(t)
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewWriteTool(work))
	m := permission.NewManager(nil, true)
	m.SetRunToolRules(nil, []string{"write"})
	a := NewAgent(AgentTypeGeneral, nil, registry, work, 3, "", m, nil)
	mock := testkit.NewMockClient().EnqueueText(`{"category":"other","suggestion":"retry","should_retry":false}`)
	a.reflector.client = mock
	ctx, _ := tools.ContextWithHeadlessPermissions(t.Context(), false, "dontAsk")
	call := &genai.FunctionCall{ID: "denied", Name: "write", Args: map[string]any{"file_path": "denied.txt", "content": "denied"}}
	r := a.finishToolWithReflection(ctx, call, tools.NewPolicyBlockedResult(tools.PolicyBlockPermission, "denied"), time.Millisecond)
	if r.Response.Response["success"] != false || len(mock.Calls()) != 0 {
		t.Fatalf("permission denial triggered %d reflection requests: %+v", len(mock.Calls()), r)
	}
}

func TestAgentMutationLedgerExcludesCopySourcesAndDryRuns(t *testing.T) {
	work := testkit.ResolvedTempDir(t)
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewCopyTool(work))
	registry.MustRegister(tools.NewBatchTool(work))
	a := NewAgent(AgentTypeGeneral, nil, registry, work, 3, "", nil, nil)
	source := filepath.Join(work, "a.txt")
	if err := os.WriteFile(source, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	for i, call := range []*genai.FunctionCall{
		{Name: "copy", Args: map[string]any{"source": source, "destination": filepath.Join(work, "b.txt")}},
		{Name: "batch", Args: map[string]any{"operation": "replace", "files": []any{source}, "search": "before", "replacement": "after", "dry_run": true}},
	} {
		if i == 0 {
			r := a.executeNestedToolResultSafely(t.Context(), call)
			if !r.Success {
				t.Fatal(r)
			}
		} else {
			r := a.executeToolWithReflection(t.Context(), call)
			if r.Response.Response["success"] != true {
				t.Fatal(r.Response.Response)
			}
		}
	}
	if got := a.GetMutatedPaths(); !reflect.DeepEqual(got, []string{"b.txt"}) {
		t.Fatalf("mutations=%v", got)
	}
	if a.mutationGeneration() != 2 {
		t.Fatal("nested calls lost or duplicated mutation records")
	}
}

type denialHookClient struct {
	*testkit.MockClient
	verifier *testkit.MockClient
}

func (c *denialHookClient) WithModel(string) client.Client { return c }
func (c *denialHookClient) CloneForAuxiliaryClient() (client.Client, bool) {
	return &denialHookClient{MockClient: c.verifier}, true
}
func (*denialHookClient) GetProvider() string { return "glm" }

func TestDelegatedAgentHookPermissionDenialContinuesOrStopsStrictly(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(map[bool]string{false: "continue", true: "strict"}[strict], func(t *testing.T) {
			work := testkit.ResolvedTempDir(t)
			root := t.TempDir()
			path := filepath.Join(root, "guard", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			body := "---\ndescription: Hook verifier\nhooks:\n  PreToolUse:\n    - matcher: Read\n      hooks:\n        - type: agent\n          prompt: Verify evidence\n          timeout: 2\n---\nInspect evidence."
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			proof := filepath.Join(work, "proof.txt")
			if err := os.WriteFile(proof, []byte("proof"), 0600); err != nil {
				t.Fatal(err)
			}
			registry := tools.NewRegistry()
			registry.MustRegister(tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}), work))
			registry.MustRegister(tools.NewReadTool(work))
			registry.MustRegister(tools.NewWriteTool(work))
			worker := testkit.NewMockClient().EnqueueToolCall("skill", map[string]any{"name": "guard"}).EnqueueToolCall("read", map[string]any{"file_path": proof}).EnqueueText("Finished.")
			worker.SetModel("glm-5.3-flash")
			verifier := testkit.NewMockClient().EnqueueToolCall("write", map[string]any{"file_path": filepath.Join(work, "denied.txt"), "content": "denied"}).EnqueueText(`{"ok":true}`)
			verifier.SetModel("glm-5.3-flash")
			c := &denialHookClient{worker, verifier}
			m := permission.NewManager(nil, true)
			m.SetRunToolRules(nil, []string{"write"})
			a := NewAgent(AgentTypeGeneral, c, registry, work, 5, "", m, nil)
			a.SetHooks(hooks.NewManager(true, work))
			ctx, state := tools.ContextWithHeadlessPermissions(t.Context(), strict, "dontAsk")
			ctx = tools.ContextWithoutSelfReview(ctx)
			result, err := a.Run(ctx, "Inspect proof.")
			if len(state.Denials()) != 1 || state.Denials()[0].Tool != "write" {
				t.Fatalf("lost hook refusal: %+v result=%+v err=%v", state.Denials(), result, err)
			}
			if strict {
				if err == nil || result.PolicyBlock == nil || len(verifier.Calls()) != 1 || len(worker.Calls()) != 2 {
					t.Fatalf("strict hook continued: result=%+v err=%v verifier=%d worker=%d", result, err, len(verifier.Calls()), len(worker.Calls()))
				}
			} else if err != nil || result.Status != AgentStatusCompleted || result.PolicyBlock != nil || len(verifier.Calls()) != 2 || len(worker.Calls()) != 3 {
				t.Fatalf("recoverable hook stopped: %+v %v", result, err)
			}
			if _, err := os.Stat(filepath.Join(work, "denied.txt")); !os.IsNotExist(err) {
				t.Fatalf("denied write executed: %v", err)
			}
		})
	}
}

func TestAgentMutationLedgerResetsEachRun(t *testing.T) {
	work := testkit.ResolvedTempDir(t)
	model := &invocationScopeClient{testkit.NewMockClient()}
	model.EnqueueToolCall("write", map[string]any{"file_path": filepath.Join(work, "first.txt"), "content": "first"}).EnqueueText("Written.").EnqueueText("Read-only answer.")
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewWriteTool(work))
	a := NewAgent(AgentTypeGeneral, model, registry, work, 4, "", nil, nil)
	ctx := tools.ContextWithoutSelfReview(t.Context())
	if result, err := a.Run(ctx, "Write first.txt."); err != nil || !reflect.DeepEqual(result.TouchedPaths, []string{"first.txt"}) || !reflect.DeepEqual(result.FilesChanged, []string{"first.txt"}) {
		t.Fatalf("first run=%+v err=%v", result, err)
	}
	if result, err := a.Run(ctx, "Answer without tools."); err != nil || len(result.TouchedPaths) != 0 || len(result.FilesChanged) != 0 {
		t.Fatalf("previous run leaked paths: %+v err=%v", result, err)
	}
}
