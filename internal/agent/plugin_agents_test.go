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
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/hooks"
	"gokin/internal/permission"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"google.golang.org/genai"
)

func writePluginAgentFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func pluginAgentFixture(t *testing.T, manifest string) (*AgentTypeRegistry, *hooks.Manager, string, string, string) {
	t.Helper()
	canonical := func() string {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return root
	}
	plugin, user, work := canonical(), canonical(), canonical()
	writePluginAgentFile(t, plugin, ".claude-plugin/plugin.json", manifest)
	manager := hooks.NewManager(false, work)
	manager.ConfigureClaudeManagedSettings(t.TempDir())
	manager.ConfigureClaudeSettings(work, user, false)
	manager.ConfigureClaudePlugins([]string{plugin})
	registry := NewAgentTypeRegistry()
	registry.ConfigureClaudePlugins(manager)
	return registry, manager, plugin, user, work
}

func TestPluginAgentsNamespaceRecursivePathsRefreshAndNativeIsolation(t *testing.T) {
	registry, _, plugin, user, _ := pluginAgentFixture(t, `{"name":"suite","userConfig":{"text":{"type":"string","default":"$ARGUMENTS ${CLAUDE_PLUGIN_ROOT} !`+"`touch never`"+`"}}}`)
	writePluginAgentFile(t, plugin, "agents/review/audit.md", "---\nname: security\ndescription: Audit changes\ntools: Read, Grep\nmodel: haiku\neffort: xhigh\nmaxTurns: 4\n---\nPLUGIN_PROMPT ${CLAUDE_PLUGIN_ROOT} ${user_config.text}")
	writePluginAgentFile(t, plugin, "agents/plain.md", "No frontmatter is required for plugin agents.")
	if err := registry.RegisterDynamic("reviewer", "Native reviewer", []string{"read"}, "Native prompt"); err != nil {
		t.Fatal(err)
	}
	definition, ok := registry.GetDynamic("suite:review:security")
	if !ok || definition.Model != "haiku" || definition.Effort != "xhigh" || definition.MaxTurns != 4 || !reflect.DeepEqual(definition.AllowedTools, []string{"read", "grep"}) {
		t.Fatalf("definition=%+v warnings=%v", definition, registry.PluginWarnings())
	}
	if !strings.Contains(definition.SystemPrompt, plugin) || !strings.Contains(definition.SystemPrompt, "$ARGUMENTS ${CLAUDE_PLUGIN_ROOT} !`touch never`") {
		t.Fatal("plugin expansion was lost or recursive")
	}
	if _, ok := registry.GetDynamic("suite:plain"); !ok {
		t.Fatal("frontmatter-free agent missing")
	}
	before := registry.AgentTypeRevision()
	for i := 0; i < 3; i++ {
		_ = registry.SnapshotAgentTypes()
	}
	if registry.AgentTypeRevision() != before {
		t.Fatal("unchanged catalog invalidated task schema cache")
	}
	definition.AllowedTools[0] = "write"
	if again, _ := registry.GetDynamic("suite:review:security"); again.AllowedTools[0] != "read" {
		t.Fatal("returned definition aliases catalog")
	}
	writePluginAgentFile(t, plugin, "agents/plain.md", "Updated prompt.")
	if registry.AgentTypeRevision() == before || registry.GetPromptForType("suite:plain") != "Updated prompt." {
		t.Fatal("agent edit requires restart")
	}
	writePluginAgentFile(t, user, "settings.json", `{"enabledPlugins":{"suite@inline":false}}`)
	if registry.Exists("suite:plain") || registry.Exists("suite:review:security") || !registry.Exists("reviewer") {
		t.Fatal("revocation lost native types or retained plugin agents")
	}
	writePluginAgentFile(t, user, "settings.json", `{"enabledPlugins":{"suite@inline":true}}`)
	if !registry.Exists("suite:plain") {
		t.Fatal("policy repair did not restore plugin catalog")
	}
	writePluginAgentFile(t, plugin, ".claude-plugin/plugin.json", `{"name":"suite","agents":"./custom/nested/check.md"}`)
	writePluginAgentFile(t, plugin, "custom/nested/check.md", "---\nname: auditor\n---\nCustom agent.")
	if !registry.Exists("suite:auditor") || registry.Exists("suite:plain") || registry.Exists("suite:nested:auditor") {
		t.Fatal("manifest did not replace default scan or flatten path")
	}
}

func TestPluginAgentInvalidDefinitionsStayOutOfCatalog(t *testing.T) {
	for _, tc := range []struct{ name, manifest, body string }{
		{"traversal", `{"name":"suite","agents":"./../outside.md"}`, "Prompt"},
		{"directory", `{"name":"suite","agents":"./agents/"}`, "Prompt"},
		{"unterminated", `{"name":"suite"}`, "---\ntools: [Read]"},
		{"duplicate-key", `{"name":"suite"}`, "---\ntools: [Read]\ntools: [Write]\n---\nPrompt"},
		{"colon-name", `{"name":"suite"}`, "---\nname: other:agent\n---\nPrompt"},
		{"invalid-effort", `{"name":"suite"}`, "---\neffort: ultra\n---\nPrompt"},
		{"invalid-turns", `{"name":"suite"}`, "---\nmaxTurns: 0\n---\nPrompt"},
		{"unsupported-isolation", `{"name":"suite"}`, "---\nisolation: worktree\n---\nPrompt"},
		{"unsupported-skill-preload", `{"name":"suite"}`, "---\nskills: [generate]\n---\nPrompt"},
		{"oversized", `{"name":"suite"}`, strings.Repeat("x", maxAgentTypeBytes+1)},
		{"invalid-utf8", `{"name":"suite"}`, string([]byte{0xff})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry, _, plugin, _, _ := pluginAgentFixture(t, tc.manifest)
			writePluginAgentFile(t, plugin, "agents/invalid.md", tc.body)
			if registry.Exists("suite:invalid") || len(registry.PluginWarnings()) == 0 {
				t.Fatalf("invalid agent loaded: %v", registry.PluginWarnings())
			}
		})
	}
	registry, _, plugin, _, _ := pluginAgentFixture(t, `{"name":"suite","agents":"./escape/agent.md"}`)
	outside := t.TempDir()
	writePluginAgentFile(t, outside, "agent.md", "Outside prompt.")
	if err := os.Symlink(outside, filepath.Join(plugin, "escape")); err != nil {
		t.Fatal(err)
	}
	if registry.Exists("suite:agent") || len(registry.PluginWarnings()) == 0 {
		t.Fatal("agent escaped plugin root through symlink")
	}
}

func TestPluginAgentToolsCannotWidenViaRequestsOrDelegation(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		allowed      []string
	}{
		{"denies-inherited", "disallowedTools: Write, Bash(git push *)", []string{"read", "task", "request_tool"}},
		{"empty", "tools: []", []string{}},
		{"deny-all", "disallowedTools: '*'", []string{}},
		{"allow-read", "tools: Read\ndisallowedTools: Write", []string{"read"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			types, _, plugin, _, work := pluginAgentFixture(t, `{"name":"suite"}`)
			writePluginAgentFile(t, plugin, "agents/guard.md", "---\n"+tc.fields+"\n---\nGuard writes.")
			definition, ok := types.GetDynamic("suite:guard")
			if !ok {
				t.Fatal(types.PluginWarnings())
			}
			base := tools.NewRegistry()
			for _, tool := range []tools.Tool{tools.NewReadTool(work), tools.NewWriteTool(work), tools.NewBashTool(work), tools.NewTaskTool(), tools.NewRequestToolTool()} {
				base.MustRegister(tool)
			}
			agent := NewAgentWithDynamicType(definition, testkit.NewMockClient(), base, work, 3, "", nil, nil)
			sortNames := append([]string{}, agent.registry.Names()...)
			// Registry.Names is sorted.
			want := append([]string{}, tc.allowed...)
			sort.Strings(want)
			if !reflect.DeepEqual(sortNames, want) {
				t.Fatalf("tools=%v want=%v", sortNames, want)
			}
			if err := agent.RequestTool("write"); err == nil {
				t.Fatal("request_tool recovered denied Write")
			}
			if task, ok := agent.registry.Get("task"); ok {
				runner := &taskSelectionRunner{result: tools.AgentResult{AgentID: "nested", Completed: true, Status: "completed"}}
				task.(*tools.TaskTool).SetRunner(runner)
				result, err := task.Execute(t.Context(), map[string]any{"subagent_type": "general", "prompt": "try writing"})
				if err != nil || !result.Success {
					t.Fatalf("nested task=%+v %v", result, err)
				}
				_, _, ceiling := runner.snapshot()
				for _, name := range ceiling {
					if name == "write" || name == "bash" {
						t.Fatal("nested agent regained denied capabilities")
					}
				}
			}
		})
	}
}

func TestPluginAgentRealGLMTaskUsesModelEffortPromptAndLiveAuthority(t *testing.T) {
	t.Setenv("CLAUDE_CODE_DISABLE_BACKGROUND_TASKS", "0")
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprintf("background=%v", background), func(t *testing.T) { testPluginAgentGLMTask(t, background) })
	}
}

func testPluginAgentGLMTask(t *testing.T, background bool) {
	t.Helper()
	t.Setenv("GOKIN_PROVIDER_HEALTH_FILE", filepath.Join(t.TempDir(), "health.json"))
	types, manager, plugin, user, work := pluginAgentFixture(t, `{"name":"suite"}`)
	writePluginAgentFile(t, plugin, "agents/writer.md", "---\ndescription: Writes reviewed output\ntools: Write\nmodel: haiku\neffort: xhigh\nmaxTurns: 4\nhooks: {PreToolUse: invalid-ignored}\npermissionMode: bypassPermissions\nmcpServers: [ignored]\n---\nPLUGIN_AGENT_PROMPT")
	var mu sync.Mutex
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		requests = append(requests, request)
		index := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n")
		stop := "end_turn"
		if index == 1 {
			input, _ := json.Marshal(map[string]any{"file_path": filepath.Join(work, "generated.txt"), "content": "plugin output"})
			fmt.Fprintf(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"write-1\",\"name\":\"write\",\"input\":%s}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n", input)
			stop = "tool_use"
		} else {
			_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Completed.\"}}\n\n")
		}
		fmt.Fprintf(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n", stop)
	}))
	defer srv.Close()
	c, err := client.NewAnthropicClient(client.AnthropicConfig{Provider: "glm", Model: "glm-5.3", APIKey: "test", BaseURL: srv.URL, MaxTokens: 4096, ReasoningEffort: "low", StreamIdleTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	base := tools.NewRegistry()
	task := tools.NewTaskTool()
	base.MustRegister(task)
	base.MustRegister(tools.NewWriteTool(work))
	base.MustRegister(tools.NewBashTool(work))
	c.SetTools(base.GeminiTools())
	runner := NewRunner(t.Context(), c, base, work)
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner.SetStore(store)
	runner.SetTypeRegistry(types)
	runner.SetHooks(manager)
	task.SetRunner(budgetTaskRunnerAdapter{runner: runner})
	task.SetAgentTypeProvider(types)
	if err := task.Validate(map[string]any{"prompt": "Generate output", "subagent_type": "suite:writer"}); err != nil {
		t.Fatal(err)
	}
	result, err := task.Execute(t.Context(), map[string]any{"prompt": "Generate output", "subagent_type": "suite:writer", "run_in_background": background})
	if err != nil || !result.Success {
		t.Fatalf("task=%+v %v", result, err)
	}
	id := result.Data.(map[string]any)["agent_id"].(string)
	if background {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		completed, err := runner.WaitWithContext(ctx, id)
		if err != nil || completed.Status != AgentStatusCompleted {
			t.Fatalf("background completion=%+v %v", completed, err)
		}
		waitForRunLeaseRelease(t, runner, id)
	}
	if data, err := os.ReadFile(filepath.Join(work, "generated.txt")); err != nil || string(data) != "plugin output" {
		encoded, _ := json.Marshal(requests)
		t.Fatalf("write=%q %v task=%+v requests=%s", data, err, result, encoded)
	}
	child, _ := runner.GetAgent(id)
	if child.maxTurns != 4 || child.Model != "glm-5.3-flash" {
		t.Fatalf("child defaults=%d %s", child.maxTurns, child.Model)
	}
	child.SetProjectContext("PROJECT_PROMPT")
	if !strings.Contains(child.projectContext, "PLUGIN_AGENT_PROMPT") || !strings.Contains(child.projectContext, "PROJECT_PROMPT") {
		t.Fatal("project context replaced plugin instructions")
	}
	mu.Lock()
	childEnd := len(requests)
	for _, request := range requests {
		if request["model"] != "glm-5.3-flash" || request["output_config"].(map[string]any)["effort"] != "max" {
			t.Fatalf("child request settings=%v", request)
		}
	}
	first, _ := json.Marshal(requests[0])
	mu.Unlock()
	if !strings.Contains(string(first), "PLUGIN_AGENT_PROMPT") || !strings.Contains(string(first), `"name":"write"`) || strings.Contains(string(first), `"name":"bash"`) || strings.Contains(string(first), `"name":"task"`) {
		t.Fatal("plugin prompt missing or tool ceiling widened")
	}
	aux, isolated := client.CloneForAuxiliary(child.client)
	if !isolated {
		t.Fatal("auxiliary client not isolated")
	}
	defer aux.Close()
	ctx := context.WithoutCancel(child.runCtx)
	writePluginAgentFile(t, user, "settings.json", `{"enabledPlugins":{"suite@inline":false}}`)
	if _, err := aux.SendMessage(ctx, "must not send"); !tools.IsHookStopError(err) {
		t.Fatalf("revoked auxiliary request=%v", err)
	}
	if _, err := aux.SendMessage(t.Context(), "must not send even without run context"); !tools.IsHookStopError(err) {
		t.Fatalf("auxiliary clone lost its plugin owner: %v", err)
	}
	if _, err := child.client.CountTokens(ctx, nil); !tools.IsHookStopError(err) {
		t.Fatalf("revoked count request=%v", err)
	}
	blocked := child.executeTool(ctx, &genai.FunctionCall{Name: "write", Args: map[string]any{"file_path": filepath.Join(work, "forbidden.txt"), "content": "forbidden"}})
	if blocked.HookStopReason == "" || blocked.Success {
		t.Fatalf("revoked write=%+v", blocked)
	}
	if _, err := os.Stat(filepath.Join(work, "forbidden.txt")); !os.IsNotExist(err) {
		t.Fatal("revoked agent wrote")
	}
	mu.Lock()
	after := len(requests)
	mu.Unlock()
	if after != childEnd {
		t.Fatal("revoked request reached HTTP provider")
	}
	if _, err := runner.Spawn(ctx, "general", "must inherit revoked ancestor", 2, ""); !tools.IsHookStopError(err) {
		t.Fatalf("descendant escaped plugin revocation: %v", err)
	}
	if _, err := runner.Resume(t.Context(), id, "must not resume"); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("disabled plugin resume bypassed current definitions: %v", err)
	}
	if c.GetModel() != "glm-5.3" {
		t.Fatal("plugin changed parent model")
	}
}

func TestPluginAgentRevocationWhilePermissionPromptWaitsBlocksMutation(t *testing.T) {
	types, manager, plugin, user, work := pluginAgentFixture(t, `{"name":"suite"}`)
	writePluginAgentFile(t, plugin, "agents/writer.md", "---\ntools: Write\n---\nWrite reviewed output.")
	definition, _ := types.GetDynamic("suite:writer")
	rules := permission.DefaultRules()
	rules.SetPolicy("write", permission.LevelAsk)
	permissions := permission.NewManager(rules, true)
	permissions.SetPromptHandler(func(context.Context, *permission.Request) (permission.Decision, error) {
		writePluginAgentFile(t, user, "settings.json", `{"enabledPlugins":{"suite@inline":false}}`)
		return permission.DecisionAllow, nil
	})
	base := tools.NewRegistry()
	base.MustRegister(tools.NewWriteTool(work))
	agent := NewAgentWithDynamicType(definition, testkit.NewMockClient(), base, work, 3, "", permissions, nil)
	agent.SetHooks(manager)
	ctx := contextWithPluginAgentAuthority(t.Context(), definition)
	result := agent.executeTool(ctx, &genai.FunctionCall{Name: "write", Args: map[string]any{"file_path": filepath.Join(work, "blocked.txt"), "content": "blocked"}})
	if result.Success || result.HookStopReason == "" {
		t.Fatalf("permission wait bypassed revocation: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(work, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatal("revoked permission wait wrote")
	}
}

func TestPluginAgentTurnLimitStopsAfterMutationWithoutAnotherModelRound(t *testing.T) {
	types, manager, plugin, _, work := pluginAgentFixture(t, `{"name":"suite"}`)
	writePluginAgentFile(t, plugin, "agents/writer.md", "---\ntools: Write\nmaxTurns: 1\n---\nWrite one reviewed file.")
	base := tools.NewRegistry()
	base.MustRegister(tools.NewWriteTool(work))
	mock := testkit.NewMockClient().EnqueueToolCall("write", map[string]any{"file_path": filepath.Join(work, "one.txt"), "content": "one"}).EnqueueText("must not be called")
	mock.SetModel("glm-5.3")
	runner := NewRunner(t.Context(), &budgetRunnerClient{MockClient: mock}, base, work)
	runner.SetTypeRegistry(types)
	runner.SetHooks(manager)
	id, err := runner.Spawn(t.Context(), "suite:writer", "Write one file", 30, "")
	if err == nil || !strings.Contains(err.Error(), "maximum turn limit (1 turns)") {
		t.Fatalf("configured limit was extended: %v", err)
	}
	result, _ := runner.GetResult(id)
	if result == nil || result.Status != AgentStatusFailed || len(mock.Calls()) != 1 {
		t.Fatalf("limit=%+v calls=%d", result, len(mock.Calls()))
	}
	if data, err := os.ReadFile(filepath.Join(work, "one.txt")); err != nil || string(data) != "one" {
		t.Fatalf("first action was lost: %q %v", data, err)
	}
}

func TestPluginAgentInvalidModelAndParentCeilingStopBeforeProvider(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		restricted   bool
	}{
		{"foreign-model", "model: claude-opus-5-5", false},
		{"blocked-tools", "tools: Write", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			types, manager, plugin, _, work := pluginAgentFixture(t, `{"name":"suite"}`)
			writePluginAgentFile(t, plugin, "agents/guard.md", "---\n"+tc.fields+"\n---\nMust not run.")
			base := tools.NewRegistry()
			base.MustRegister(tools.NewWriteTool(work))
			mock := testkit.NewMockClient().EnqueueText("must not be called")
			mock.SetModel("glm-5.3")
			runner := NewRunner(t.Context(), &budgetRunnerClient{MockClient: mock}, base, work)
			runner.SetTypeRegistry(types)
			runner.SetHooks(manager)
			ctx := t.Context()
			if tc.restricted {
				ctx = tools.ContextWithAgentToolCapabilityCeiling(ctx, []string{})
			}
			if _, err := runner.Spawn(ctx, "suite:guard", "do work", 3, ""); err == nil || len(mock.Calls()) != 0 {
				t.Fatalf("invalid launch reached provider: %v calls=%d", err, len(mock.Calls()))
			}
		})
	}
}
