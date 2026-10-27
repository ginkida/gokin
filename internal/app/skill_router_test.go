package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/agent"
	"gokin/internal/client"
	"gokin/internal/commands"
	"gokin/internal/router"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestInteractiveSlashSkillSettingsStayWithOwnerAcrossSubAgentRoute(t *testing.T) {
	var mu sync.Mutex
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Workflow complete.\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	c, err := client.NewAnthropicClient(client.AnthropicConfig{Provider: "glm", Model: "glm-5.3", APIKey: "test", BaseURL: server.URL, MaxTokens: 4096, ReasoningEffort: "low", StreamIdleTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	work := testkit.ResolvedTempDir(t)
	const prompt = "Refactor authentication across packages. Analyze every caller and update interfaces. Add migration tests and optimize error handling for all providers."
	sdkTestFile(t, filepath.Join(work, "refactor", "SKILL.md"), "---\ndescription: Refactor\nmodel: haiku\neffort: max\nallowed-tools: Read\n---\n"+prompt)
	skill := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: work, Source: "claude-global"}}), work)
	a, executor := newHeadlessPolicyTestApp(t, testkit.NewMockClient(), skill)
	a.workDir, a.client = work, c
	a.config.Model.Provider, a.config.Model.Name = "glm", "glm-5.3"
	executor.SetClient(c)
	trap := &routedUnsafeRunner{cause: errors.New("skill workflow left its owning executor"), result: &agent.AgentResult{Status: agent.AgentStatusFailed, Completed: true, StatefulToolAttempts: 1}}
	a.taskRouter = router.NewRouter(&router.RouterConfig{Enabled: true, DecomposeThreshold: 100, ParallelThreshold: 100}, executor, trap, c, a.registry, false, work)
	a.commandHandler = commands.NewHandler()
	output, err := a.commandHandler.Execute(commands.WithRawInvocation(t.Context(), "/skill refactor"), "skill", []string{"refactor"}, a)
	if err != nil || !tools.HasPendingSkillModelSettings(a.registry) {
		t.Fatalf("slash handoff=%q err=%v", output, err)
	}
	expanded, ok := strings.CutPrefix(output, commands.PromptMarker)
	if !ok || a.taskRouter.Route(expanded).Handler != router.HandlerSubAgent {
		t.Fatal("expanded workflow must ordinarily select a sub-agent", expanded)
	}
	a.processMessageWithContext(t.Context(), expanded)
	if trap.spawnCalls.Load() != 0 {
		t.Fatalf("pending slash settings were lost to delegation: spawns=%d settings=%+v", trap.spawnCalls.Load(), tools.SkillModelSettings(a.registry))
	}
	a.processMessageWithContext(t.Context(), "Hello again")
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("provider requests=%d, want skill turn and unrelated next turn", len(requests))
	}
	for i, request := range requests {
		model, effort := "glm-5.3-flash", "max"
		if i == 1 {
			model, effort = "glm-5.3", "low"
		}
		if request["model"] != model || request["output_config"].(map[string]any)["effort"] != effort {
			t.Fatalf("request %d lost owning settings: %v", i, request)
		}
	}
}
