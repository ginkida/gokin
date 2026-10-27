package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
	"gokin/internal/tools"
	"gokin/internal/toolschema"
	"google.golang.org/genai"
)

func TestDeferredAgentPressurePruningKeepsFullSkill(t *testing.T) {
	payload := strings.Repeat("workflow ", 2000)
	call := &genai.FunctionCall{ID: "skill-call", Name: "use_tool", Args: map[string]any{"name": "skill", "arguments": map[string]any{"name": "check"}}}
	response := &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: tools.NewSuccessResult(payload).ToMap()}
	a := &Agent{history: []*genai.Content{
		genai.NewContentFromText("system", genai.RoleUser),
		genai.NewContentFromText("ready", genai.RoleModel),
		{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: call}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: response}}},
		genai.NewContentFromText("recent", genai.RoleModel),
	}, summarizeMinMsgs: 4, summarizeProtect: 1, pruneMinOutputSize: 1000}
	if freed := a.pruneToolOutputs(0); freed != 0 || response.Response["content"] != payload {
		t.Fatal("dispatched skill was pruned", freed)
	}
}

func TestRunnerDeferredDefaultRespectsOwningMode(t *testing.T) {
	for _, parentDeferred := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "deferred"}[parentDeferred], func(t *testing.T) {
			root := testkit.ResolvedTempDir(t)
			registry := tools.NewRegistry()
			registry.MustRegister(tools.NewReadTool(root))
			mock := testkit.NewMockClient().EnqueueText("Finished.")
			runner := NewRunner(t.Context(), &budgetRunnerClient{MockClient: mock}, registry, root)
			runner.SetDeferredSchemas(!parentDeferred)
			ctx := tools.ContextWithoutSelfReview(t.Context())
			if parentDeferred {
				ctx = tools.ContextWithDeferredCatalog(ctx, registry, registry.GeminiTools())
			} else {
				ctx = tools.ContextWithoutDeferredCatalog(ctx)
			}
			id, err := runner.Spawn(ctx, "general", "Inspect", 3, "")
			if err != nil {
				t.Fatal(err)
			}
			child, _ := runner.GetAgent(id)
			if child.GetState().ToolSchemas.Deferred != parentDeferred {
				t.Fatal("runner default replaced owning session mode")
			}
		})
	}
}

func TestRunnerDeferredSchemasOwnCatalogAndExecuteTarget(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewMkdirTool(root))
	registry.MustRegister(tools.NewReadTool(root))
	path := filepath.Join(root, "child-directory")
	mock := testkit.NewMockClient().EnqueueToolCall("tool_search", map[string]any{"query": "mkdir"}).
		EnqueueToolCall("use_tool", map[string]any{"name": "mkdir", "arguments": map[string]any{"path": path}}).EnqueueText("Finished.")
	runner := NewRunner(t.Context(), &budgetRunnerClient{MockClient: mock}, registry, root)
	var schemas []string
	mock.OnSend = func(context.Context) { b, _ := json.Marshal(mock.GetTools()); schemas = append(schemas, string(b)) }
	ctx := tools.ContextWithDeferredCatalog(tools.ContextWithoutSelfReview(t.Context()), tools.NewRegistry(), nil)
	id, err := runner.Spawn(ctx, "general", "Create the directory", 5, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.WaitWithContext(t.Context(), id)
	if err != nil || result.Error != "" {
		t.Fatal(result, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("deferred child target did not run", err)
	}
	if len(schemas) != 3 || schemas[0] != schemas[1] || schemas[0] != schemas[2] || strings.Contains(schemas[0], `"name":"mkdir"`) || !strings.Contains(schemas[0], `"name":"use_tool"`) {
		t.Fatal("child did not own a compact, stable publication", schemas)
	}
}

func TestDeferredLateBudgetStopKeepsWireIdentity(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	registry := tools.NewRegistry()
	target := &budgetMutationProbeTool{}
	registry.MustRegister(target)
	mock := testkit.NewMockClient().EnqueueToolCall("use_tool", map[string]any{"name": "write", "arguments": map[string]any{}})
	a := NewAgent(AgentTypeGeneral, &budgetRunnerClient{MockClient: mock}, registry, root, 3, "", nil, nil)
	ctx := tools.ContextWithMaxBudgetUSD(t.Context(), 1)
	ctx = tools.ContextWithDeferredCatalog(ctx, registry, registry.GeminiTools())
	ledger, _ := tools.InvocationBudgetLedgerFromContext(ctx)
	a.SetProgressCallback(func(p *AgentProgress) {
		// A sibling can consume the shared budget after this model response
		// completed but before this agent begins executing its tool calls.
		if strings.HasPrefix(p.CurrentAction, "Executing tools:") {
			ledger.AddSpend(1)
		}
	})
	_, err := a.Run(ctx, "Perform the change")
	if !errors.Is(err, tools.ErrBudgetExceeded) || target.calls.Load() != 0 {
		t.Fatal("late budget stop failed", err, target.calls.Load())
	}
	paired := false
	callCount := 0
	for _, content := range a.history {
		for _, part := range content.Parts {
			if part.FunctionCall != nil {
				callCount++
			}
			if part.FunctionResponse != nil {
				paired = true
				if part.FunctionResponse.Name != "use_tool" {
					t.Fatal("budget result used effective name in wire history", part.FunctionResponse.Name)
				}
			}
		}
	}
	if !paired || callCount != 1 {
		t.Fatal("pending wire call was not paired exactly once", callCount)
	}
}

func TestAgentSchemaStateLegacyAndResume(t *testing.T) {
	a := &Agent{schemaProfile: toolschema.New(true)}
	state := a.GetState()
	b := &Agent{}
	if err := b.RestoreHistory(state); err != nil || !b.schemaProfile.Deferred() || b.schemaProfile == a.schemaProfile {
		t.Fatal("independent profile not restored", err)
	}
	if err := b.RestoreHistory(&AgentState{}); err != nil || b.schemaProfile.Deferred() {
		t.Fatal("legacy agent changed to deferred", err)
	}
	if err := b.RestoreHistory(&AgentState{ToolSchemas: &toolschema.Snapshot{Version: 99}}); err == nil || b.schemaProfile.Deferred() {
		t.Fatal("invalid agent state partially applied")
	}
}
