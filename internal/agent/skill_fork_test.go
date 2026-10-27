package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/permission"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestHeadlessForkedSkillFreezesChildToolsAfterGrants(t *testing.T) {
	work := testkit.ResolvedTempDir(t)
	root := filepath.Join(work, "skills")
	path := filepath.Join(root, "check", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\ndescription: Check\ncontext: fork\nbackground: false\nallowed-tools: Bash\n---\nRun the check."), 0600); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	skill := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}), work)
	registry.MustRegister(skill)
	registry.MustRegister(tools.NewBashTool(work))
	task := tools.NewTaskTool()
	registry.MustRegister(task)
	mock := testkit.NewMockClient().EnqueueToolCall("bash", map[string]any{"command": "printf checked"}).EnqueueText("Checked.")
	runner := NewRunner(t.Context(), &budgetRunnerClient{MockClient: mock}, registry, work)
	task.SetRunner(budgetTaskRunnerAdapter{runner: runner})
	rules := permission.DefaultRules()
	rules.SetPolicy("task", permission.LevelAllow)
	manager := permission.NewManager(rules, true)
	runner.SetPermissions(manager)
	executor := tools.NewExecutor(registry, nil, time.Second)
	executor.SetPermissions(manager)
	ctx, state := tools.ContextWithHeadlessPermissions(t.Context(), false, "acceptEdits")
	ctx = tools.ContextWithoutSelfReview(executor.ContextWithHeadlessToolPolicy(ctx))
	advertised := false
	for _, declaration := range tools.FilterHeadlessTools(ctx, registry.GeminiTools()) {
		for _, function := range declaration.FunctionDeclarations {
			if function.Name == "bash" {
				advertised = true
			}
		}
	}
	if !advertised || executor.PreflightToolPermission(ctx, "bash", map[string]any{"command": "printf checked"}) {
		t.Fatal("potential child grants must preserve advertisement without authorizing the parent")
	}
	var schemas []string
	mock.OnSend = func(context.Context) {
		schema, err := json.Marshal(mock.GetTools())
		if err != nil {
			t.Error(err)
		}
		schemas = append(schemas, string(schema))
		if !strings.Contains(string(schema), `"name":"bash"`) {
			t.Error("child Bash grant was hidden by inherited parent omissions")
		}
	}
	result, err := executor.InvokeTool(ctx, "skill", map[string]any{"name": "check"})
	if err != nil || !result.Success || len(state.Denials()) != 0 || len(schemas) != 2 || !slices.Equal(schemas[:1], schemas[1:]) {
		t.Fatalf("fork result=%+v err=%v denials=%v schemas=%v", result, err, state.Denials(), schemas)
	}
	if len(skill.ActivePermissionGrants()) != 0 {
		t.Fatal("child grants leaked into the parent")
	}
}

func TestForkedSkillRealRunnerIsolatesInstructionsAndAuthorizesOnlyChild(t *testing.T) {
	workDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(workDir, "skills")
	path := filepath.Join(root, "generate", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\ndescription: Generate code\ncontext: fork\nbackground: false\nallowed-tools: Write\n---\nWrite the requested output. CHILD_WORKFLOW_MARKER"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreground := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}), workDir)
	if _, _, err := foreground.InvocationLedger().Record("private-parent", "PARENT_PRIVATE_CONVERSATION_MARKER", "project", "private/SKILL.md"); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	registry.MustRegister(foreground)
	registry.MustRegister(tools.NewWriteTool(workDir))
	task := tools.NewTaskTool()
	registry.MustRegister(task)
	outputPath := filepath.Join(workDir, "generated.txt")
	mock := testkit.NewMockClient().EnqueueToolCall("write", map[string]any{"file_path": outputPath, "content": "child generated"}).EnqueueText("Child finished.")
	runner := NewRunner(t.Context(), &budgetRunnerClient{MockClient: mock}, registry, workDir)
	task.SetRunner(budgetTaskRunnerAdapter{runner: runner})
	rules := permission.DefaultRules()
	rules.SetPolicy("task", permission.LevelAllow)
	manager := permission.NewManager(rules, true)
	prompts := 0
	manager.SetPromptHandler(func(context.Context, *permission.Request) (permission.Decision, error) {
		prompts++
		return permission.DecisionAllow, nil
	})
	runner.SetPermissions(manager)
	executor := tools.NewExecutor(registry, nil, time.Second)
	executor.SetPermissions(manager)
	result, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "generate"})
	if err != nil || !result.Success || !strings.Contains(result.Content, "Child finished.") || prompts != 0 {
		t.Fatalf("fork=%+v err=%v prompts=%d", result, err, prompts)
	}
	if data, err := os.ReadFile(outputPath); err != nil || string(data) != "child generated" {
		t.Fatalf("child write=%q err=%v", data, err)
	}
	parent := foreground.InvocationLedger().SnapshotNewestFirst()
	if len(parent) != 1 || parent[0].Name != "private-parent" || len(foreground.ActivePermissionGrants()) != 0 {
		t.Fatalf("parent contaminated: %v", parent)
	}
	data := result.Data.(map[string]any)
	id := data["agent_id"].(string)
	child, _ := runner.GetAgent(id)
	if child == nil {
		t.Fatal("child agent missing")
	}
	retained := child.invokedSkillSnapshot()
	if len(retained) != 1 || !strings.Contains(retained[0].Rendered, "CHILD_WORKFLOW_MARKER") {
		t.Fatalf("child lost instructions: %v", retained)
	}
	for _, call := range mock.Calls() {
		encoded, _ := json.Marshal(call)
		if strings.Contains(string(encoded), "PARENT_PRIVATE_CONVERSATION_MARKER") {
			t.Fatal("parent instructions reached child model")
		}
	}
}

func TestForkedSkillRealGLMRequestsUseChildModelAndEffort(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprintf("background=%v", background), func(t *testing.T) {
			t.Setenv("GOKIN_PROVIDER_HEALTH_FILE", filepath.Join(t.TempDir(), "health.json"))
			t.Setenv("CLAUDE_CODE_DISABLE_BACKGROUND_TASKS", "0")
			workDir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(workDir, "skills", "generate", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf("---\ncontext: fork\nbackground: %v\nmodel: haiku\neffort: xhigh\nallowed-tools: Write\n---\nGenerate code.", background)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			outputPath := filepath.Join(workDir, "generated.txt")
			var mu sync.Mutex
			var requests []map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				mu.Lock()
				requests = append(requests, req)
				index := len(requests)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n")
				if index == 1 {
					input, _ := json.Marshal(map[string]any{"file_path": outputPath, "content": "child code"})
					_, _ = io.WriteString(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"write-1\",\"name\":\"write\",\"input\":{}}}\n\n")
					delta, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(input)}})
					fmt.Fprintf(w, "data: %s\n\n", delta)
					_, _ = io.WriteString(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
				} else {
					_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Finished.\"}}\n\n")
				}
				stopReason := "end_turn"
				if index == 1 {
					stopReason = "tool_use"
				}
				fmt.Fprintf(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n", stopReason)
			}))
			defer srv.Close()
			c, err := client.NewAnthropicClient(client.AnthropicConfig{Provider: "glm", Model: "glm-5.3", APIKey: "test", BaseURL: srv.URL, MaxTokens: 4096, ReasoningEffort: "low", StreamIdleTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			registry := tools.NewRegistry()
			skill := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: filepath.Dir(filepath.Dir(path)), Source: "claude-global"}}), workDir)
			registry.MustRegister(skill)
			registry.MustRegister(tools.NewWriteTool(workDir))
			task := tools.NewTaskTool()
			registry.MustRegister(task)
			runner := NewRunner(t.Context(), c, registry, workDir)
			store, err := NewAgentStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			runner.SetStore(store)
			rules := permission.DefaultRules()
			rules.SetPolicy("task", permission.LevelAllow)
			manager := permission.NewManager(rules, true)
			manager.SetPromptHandler(func(context.Context, *permission.Request) (permission.Decision, error) {
				t.Error("forked skill lost its permission grant")
				return permission.DecisionDeny, nil
			})
			runner.SetPermissions(manager)
			task.SetRunner(budgetTaskRunnerAdapter{runner: runner})
			executor := tools.NewExecutor(registry, nil, time.Second)
			executor.SetPermissions(manager)
			result, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "generate"})
			if err != nil || !result.Success {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			id := result.Data.(map[string]any)["agent_id"].(string)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			completed, err := runner.WaitWithContext(ctx, id)
			if err != nil || completed.Status != AgentStatusCompleted {
				t.Fatalf("completed=%+v err=%v", completed, err)
			}
			waitForRunLeaseRelease(t, runner, id)
			if data, err := os.ReadFile(outputPath); err != nil || string(data) != "child code" {
				t.Fatalf("write=%q err=%v completed=%+v requests=%+v", data, err, completed, requests)
			}
			mu.Lock()
			childEnd := len(requests)
			mu.Unlock()
			child, _ := runner.GetAgent(id)
			child.stateMu.RLock()
			oldRunCtx := child.runCtx
			child.stateMu.RUnlock()
			// A child delegated from the skill's context inherits session settings,
			// not this skill's temporary max-effort override.
			if _, err := runner.Spawn(context.WithoutCancel(oldRunCtx), "general", "A separate delegated task.", 3, ""); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			grandchildEnd := len(requests)
			mu.Unlock()
			// Reusing the completed child's context also must not resurrect max.
			if _, err := runner.Resume(context.WithoutCancel(oldRunCtx), id, "A new task in this agent."); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			resumeEnd := len(requests)
			mu.Unlock()
			stream, err := c.SendMessage(t.Context(), "Next parent prompt.")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stream.Collect(); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			// Real agents may also bill a semantic reflection request. Every
			// child request must carry the override, including that auxiliary call.
			if childEnd < 2 || grandchildEnd <= childEnd || resumeEnd <= grandchildEnd || len(requests) != resumeEnd+1 {
				t.Fatalf("requests=%d", len(requests))
			}
			for i, request := range requests {
				model, effort := "glm-5.3-flash", "max"
				if i >= childEnd && i < grandchildEnd || i == resumeEnd {
					model, effort = "glm-5.3", "low"
				} else if i >= grandchildEnd {
					effort = "low"
				}
				if request["model"] != model || request["output_config"].(map[string]any)["effort"] != effort {
					t.Fatalf("request %d settings=%v", i, request)
				}
			}
			if len(skill.InvocationLedger().SnapshotNewestFirst()) != 0 {
				t.Fatal("fork polluted parent ledger")
			}
		})
	}
}

func TestForkedSkillBackgroundSurvivesParentToolCleanupAndPublishesResult(t *testing.T) {
	t.Setenv("CLAUDE_CODE_DISABLE_BACKGROUND_TASKS", "0")
	workDir := t.TempDir()
	path := filepath.Join(workDir, "skills", "review", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\ndescription: Review\ncontext: fork\n---\nReview the code."), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	skill := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: filepath.Dir(filepath.Dir(path)), Source: "claude-global"}}), workDir)
	registry.MustRegister(skill)
	task := tools.NewTaskTool()
	registry.MustRegister(task)
	mock := testkit.NewMockClient()
	mock.EnqueueScript(testkit.ResponseScript{Chunks: []client.ResponseChunk{{Text: "Background review finished.", Done: true}}, DelayBeforeFirstChunk: 75 * time.Millisecond})
	runner := NewRunner(t.Context(), &budgetRunnerClient{MockClient: mock}, registry, workDir)
	task.SetRunner(budgetTaskRunnerAdapter{runner: runner})
	executor := tools.NewExecutor(registry, nil, time.Second)
	result, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "review"})
	if err != nil || !result.Success {
		t.Fatalf("fork=%+v err=%v", result, err)
	}
	id := result.Data.(map[string]any)["agent_id"].(string)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	completed, err := runner.WaitWithContext(ctx, id)
	if err != nil || !completed.Completed || !strings.Contains(completed.Output, "Background review finished.") {
		t.Fatalf("result=%+v err=%v", completed, err)
	}
	if len(skill.InvocationLedger().SnapshotNewestFirst()) != 0 {
		t.Fatal("background fork retained parent instructions")
	}
}
