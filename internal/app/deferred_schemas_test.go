package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/client"
	"gokin/internal/commands"
	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/permission"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"gokin/internal/toolschema"

	"google.golang.org/genai"
)

func TestDeferredConfigPermissionsAllowDiscoveryWithoutGrantingDispatch(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueToolCall("tool_search", map[string]any{"query": "env"}).
		EnqueueToolCall("use_tool", map[string]any{"name": "env", "arguments": map[string]any{}}).EnqueueText("Finished.")
	target := &appHeadlessScriptedTool{name: "env", results: []tools.ToolResult{tools.NewSuccessResult("environment")}}
	a, executor := newHeadlessPolicyTestApp(t, worker, target)
	a.session.SetToolSchemaProfile(toolschema.New(true))
	cfg := config.DefaultConfig().Permission
	executor.SetPermissions(permission.NewManager(permission.NewRulesFromConfig(cfg.DefaultPolicy, cfg.Rules), true))
	result, err := a.RunHeadlessWithOptions(t.Context(), "Inspect the environment", HeadlessOptions{PermissionMode: "dontAsk", StrictPermissions: true, NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.Status != "success" || target.CallCount() != 1 || len(result.PermissionDenials) != 0 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, target.CallCount())
	}
}

func TestDeferredDispatcherPolicyCannotGrantTargetPermission(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueToolCall("use_tool", map[string]any{"name": "mkdir", "arguments": map[string]any{"path": "denied"}})
	target := &appHeadlessScriptedTool{name: "mkdir"}
	a, executor := newHeadlessPolicyTestApp(t, worker, target)
	a.session.SetToolSchemaProfile(toolschema.New(true))
	cfg := config.DefaultConfig().Permission
	cfg.Rules["use_tool"] = "allow"
	executor.SetPermissions(permission.NewManager(permission.NewRulesFromConfig(cfg.DefaultPolicy, cfg.Rules), true))
	result, err := a.RunHeadlessWithOptions(t.Context(), "Create directory", HeadlessOptions{PermissionMode: "dontAsk", StrictPermissions: true, NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || result.Status != "policy_blocked" || target.CallCount() != 0 || len(result.PermissionDenials) != 1 || result.PermissionDenials[0].Tool != "mkdir" {
		t.Fatalf("dispatcher granted target authority: result=%+v err=%v calls=%d", result, err, target.CallCount())
	}
}

func TestDeferredSessionConversationDisplaysTarget(t *testing.T) {
	history := []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "call", Name: "use_tool", Args: map[string]any{"name": "mkdir", "arguments": map[string]any{"path": "created"}}}}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "call", Name: "use_tool", Response: tools.NewSuccessResult("created").ToMap()}}}},
	}
	conversation := sessionConversation(history)
	if len(conversation.Entries) != 1 || conversation.Entries[0].Tool.Name != "mkdir" || conversation.Entries[0].Tool.Args["path"] != "created" {
		t.Fatal("restored UI lost target identity", conversation)
	}
	if history[1].Parts[0].FunctionResponse.Name != "use_tool" {
		t.Fatal("display rewrote provider history")
	}
}

func TestDeferredClearCommandKeepsSchemaMode(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "deferred"}[deferred], func(t *testing.T) {
			a := newRewindApp(t)
			a.client = testkit.NewMockClient()
			a.session.SetToolSchemaProfile(toolschema.New(deferred))
			a.session.AddUserMessage("earlier conversation")
			if _, err := (&commands.ClearCommand{}).Execute(t.Context(), nil, a); err != nil {
				t.Fatal(err)
			}
			state := a.session.GetState()
			if state.ToolSchemas == nil || state.ToolSchemas.Deferred != deferred {
				t.Fatalf("clear changed schema mode: %+v", state.ToolSchemas)
			}
		})
	}
}

type deferredLoopClient struct{ *testkit.MockClient }

func (c *deferredLoopClient) WithModel(string) client.Client { return c }

func TestDeferredLoopWorkerUsesConfiguredMode(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	cfg := config.DefaultConfig()
	cfg.Tools.DeferredSchemas = true
	cfg.DoneGate.Enabled = false
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewEnvTool())
	worker := testkit.NewMockClient().EnqueueText("Finished.")
	b := &Builder{cfg: cfg, ctx: t.Context(), workDir: root, mainClient: &deferredLoopClient{worker}, registry: registry, executor: tools.NewExecutor(registry, worker, 0)}
	if err := b.initBareManagers(); err != nil {
		t.Fatal(err)
	}
	defer b.agentRunner.Close()
	a := &App{config: cfg, ctx: t.Context(), client: worker, agentRunner: b.agentRunner}
	result, err := newLoopSpawner(a)(tools.ContextWithoutSelfReview(t.Context()), "Inspect the project")
	if err != nil || !result.OK {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	state := b.agentRunner.ListAgents()
	if len(state) != 1 {
		t.Fatal("missing loop worker")
	}
	child, _ := b.agentRunner.GetAgent(state[0])
	if !child.GetState().ToolSchemas.Deferred {
		t.Fatal("loop worker published full schemas")
	}
}

func TestDeferredSkillRetainedAfterFaultAcknowledgesMutation(t *testing.T) {
	worker := testkit.NewMockClient()
	for i, target := range []string{"skill", "mkdir", "mkdir"} {
		args := map[string]any{"path": []string{"", "first", "second"}[i]}
		if i == 0 {
			args = map[string]any{"name": "check"}
		}
		worker.EnqueueScript(testkit.ResponseScript{Chunks: []client.ResponseChunk{{FunctionCalls: []*genai.FunctionCall{{ID: fmt.Sprintf("call-%d", i), Name: "use_tool", Args: map[string]any{"name": target, "arguments": args}}}}, {Done: true}}})
		if i == 1 {
			worker.EnqueueError(errors.New("retryable stream fault"))
		}
	}
	worker.EnqueueText("Finished.")
	target := &appHeadlessScriptedTool{name: "mkdir", results: []tools.ToolResult{tools.NewSuccessResult("first"), tools.NewSuccessResult("second")}}
	a, executor := newHeadlessPolicyTestApp(t, worker, target)
	a.session.SetToolSchemaProfile(toolschema.New(true))
	a.session.AddUserMessage("earlier task")
	sdkTestFile(t, filepath.Join(a.workDir, "check", "SKILL.md"), "---\ndescription: Check\n---\nFollow the workflow carefully.")
	skill := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: a.workDir, Source: "claude-global"}}), a.workDir)
	a.registry.MustRegister(skill)
	tools.BindSkillInvocationLedger(a.registry, a.session.InvocationLedger())
	ctx := tools.ContextWithDeferredCatalog(tools.ContextWithoutSelfReview(t.Context()), a.registry, a.registry.GeminiTools())
	before := a.session.GetHistory()
	retained, _, err := executor.Execute(ctx, before, "Load skill and create directories")
	if err == nil || target.CallCount() != 1 {
		t.Fatalf("first attempt err=%v calls=%d", err, target.CallCount())
	}
	if executor.GetCheckpointJournal().Len() == 0 {
		t.Fatal("mutation was not checkpointed")
	}
	a.setRetainedToolHistory(before, retained)
	if n := executor.GetCheckpointJournal().Len(); n != 0 {
		t.Fatalf("retained mutation remains pending: %d", n)
	}
	executor.PrepareSideEffectRecovery(executor.GetCheckpointJournal().Entries())
	_, _, err = executor.Execute(ctx, a.session.GetHistory(), "Continue")
	if err != nil || target.CallCount() != 2 {
		t.Fatalf("continuation err=%v mutations=%d", err, target.CallCount())
	}
}

func TestHeadlessDeferredDiscoveryAndTargetEvidence(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueToolCall("tool_search", map[string]any{"query": "env"}).
		EnqueueToolCall("use_tool", map[string]any{"name": "env", "arguments": map[string]any{}}).EnqueueText("Finished.")
	target := &appHeadlessScriptedTool{name: "env", results: []tools.ToolResult{tools.NewSuccessResult("target evidence")}}
	a, _ := newHeadlessPolicyTestApp(t, worker, target)
	a.session.SetToolSchemaProfile(toolschema.New(true))
	var schemas []string
	worker.OnSend = func(context.Context) {
		encoded, _ := json.Marshal(worker.GetTools())
		schemas = append(schemas, string(encoded))
	}
	var output strings.Builder
	result, err := a.RunHeadlessWithOptions(t.Context(), "Inspect the environment", HeadlessOptions{PermissionMode: "bypassPermissions", NoSelfReview: true, OutputFormat: HeadlessOutputStreamJSON, Stdout: &output, Stderr: io.Discard})
	if err != nil || result.Status != "success" || target.CallCount() != 1 || len(schemas) != 3 {
		t.Fatalf("result=%+v err=%v calls=%d schemas=%d", result, err, target.CallCount(), len(schemas))
	}
	if schemas[0] != schemas[1] || schemas[0] != schemas[2] || strings.Contains(schemas[0], `"name":"env"`) {
		t.Fatal("deferred prefix changed", schemas)
	}
	if !strings.Contains(output.String(), `"name":"env"`) && !strings.Contains(output.String(), `"tool":"env"`) {
		t.Fatal("headless evidence lost the real name: ", output.String())
	}
}

func TestHeadlessDeferredStrictDenialUsesRealTarget(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueToolCall("use_tool", map[string]any{"name": "mkdir", "arguments": map[string]any{"path": "denied"}})
	target := &appHeadlessScriptedTool{name: "mkdir"}
	a, executor := newHeadlessPolicyTestApp(t, worker, target)
	a.session.SetToolSchemaProfile(toolschema.New(true))
	rules := permission.DefaultRules()
	rules.SetPolicy("mkdir", permission.LevelDeny)
	executor.SetPermissions(permission.NewManager(rules, true))
	result, err := a.RunHeadlessWithOptions(t.Context(), "Create a directory", HeadlessOptions{PermissionMode: "acceptEdits", StrictPermissions: true, NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || result.Status != "policy_blocked" || target.CallCount() != 0 || len(result.PermissionDenials) != 1 || result.PermissionDenials[0].Tool != "mkdir" || len(worker.Calls()) != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, target.CallCount())
	}
}

func TestHeadlessDeferredSkillGrantsKeepPublicationStable(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueToolCall("use_tool", map[string]any{"name": "skill", "arguments": map[string]any{"name": "check"}}).
		EnqueueToolCall("use_tool", map[string]any{"name": "mkdir", "arguments": map[string]any{"path": "granted"}}).EnqueueText("Finished.")
	target := &appHeadlessScriptedTool{name: "mkdir", results: []tools.ToolResult{tools.NewSuccessResult("created")}}
	a, executor := newHeadlessPolicyTestApp(t, worker, target)
	a.session.SetToolSchemaProfile(toolschema.New(true))
	payload := strings.Repeat("Follow the workflow carefully. ", 600) + "SKILL_DELIVERY_TAIL"
	sdkTestFile(t, filepath.Join(a.workDir, "check", "SKILL.md"), "---\ndescription: Check\nallowed-tools: mkdir\n---\n"+payload)
	executor.SetCompactor(appcontext.NewResultCompactor(256))
	executor.SetResponseCompressor(appcontext.NewResponseCompressor(256))
	skill := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: a.workDir, Source: "claude-global"}}), a.workDir)
	a.registry.MustRegister(skill)
	executor.SetPermissions(permission.NewManager(permission.DefaultRules(), true))
	var schemas []string
	worker.OnSend = func(context.Context) { b, _ := json.Marshal(worker.GetTools()); schemas = append(schemas, string(b)) }
	result, err := a.RunHeadlessWithOptions(t.Context(), "Load the skill and create the directory", HeadlessOptions{PermissionMode: "acceptEdits", NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.Status != "success" || target.CallCount() != 1 || len(result.PermissionDenials) != 0 || len(schemas) != 3 {
		t.Fatalf("result=%+v err=%v calls=%d schemas=%d", result, err, target.CallCount(), len(schemas))
	}
	if schemas[0] != schemas[1] || schemas[0] != schemas[2] {
		t.Fatal("skill grants changed the tools prefix")
	}
	responses := worker.Calls()[1].Responses
	if len(responses) != 1 || responses[0].Name != "use_tool" || !strings.Contains(responses[0].Response["content"].(string), "SKILL_DELIVERY_TAIL") {
		t.Fatal("dispatched skill was truncated before delivery", responses)
	}
}
