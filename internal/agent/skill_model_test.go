package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/skills"
	"gokin/internal/tools"
)

func TestAgentInlineSkillSettingsCoverAuxiliaryAndRestoreOnNextRun(t *testing.T) {
	t.Setenv("GOKIN_PROVIDER_HEALTH_FILE", filepath.Join(t.TempDir(), "health.json"))
	workDir := t.TempDir()
	path := filepath.Join(workDir, "skills", "fast", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nmodel: haiku\neffort: max\n---\nReview code."), 0o600); err != nil {
		t.Fatal(err)
	}
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
		stop := "end_turn"
		if index == 1 {
			_, _ = io.WriteString(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"skill-1\",\"name\":\"skill\",\"input\":{\"name\":\"fast\"}}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			stop = "tool_use"
		} else {
			_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Completed inline task.\"}}\n\n")
		}
		fmt.Fprintf(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n", stop)
	}))
	defer srv.Close()
	c, err := client.NewAnthropicClient(client.AnthropicConfig{Provider: "glm", Model: "glm-5.3", APIKey: "test", BaseURL: srv.URL, MaxTokens: 4096, ReasoningEffort: "low", StreamIdleTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	skill := tools.NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{Path: filepath.Dir(filepath.Dir(path)), Source: "claude-global"}}))
	registry.MustRegister(skill)
	agent := NewAgent(AgentTypeGeneral, c, registry, workDir, 5, "", nil, nil)
	result, err := agent.Run(t.Context(), "Use the skill for this task.")
	if err != nil || result.Status != AgentStatusCompleted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	mu.Lock()
	firstRunEnd := len(requests)
	mu.Unlock()
	if firstRunEnd < 2 {
		t.Fatal("agent did not continue after loading skill")
	}
	if result.Model != "glm-5.3-flash" {
		t.Fatalf("result model=%q", result.Model)
	}
	result, err = agent.Run(t.Context(), "Next unrelated task.")
	if err != nil || result.Status != AgentStatusCompleted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) <= firstRunEnd {
		t.Fatal("second run did not request a response")
	}
	for i, request := range requests {
		model, effort := "glm-5.3", "low"
		if i > 0 && i < firstRunEnd {
			model, effort = "glm-5.3-flash", "max"
		}
		if request["model"] != model || request["output_config"].(map[string]any)["effort"] != effort {
			t.Fatalf("request %d settings=%v/%v", i, request["model"], request["output_config"])
		}
	}
	if c.GetModel() != "glm-5.3" {
		t.Fatal("agent skill mutated parent's client")
	}
	if len(agent.invokedSkillSnapshot()) != 1 || len(skill.InvocationLedger().SnapshotNewestFirst()) != 0 {
		t.Fatal("inline skill instructions lost isolation or retention")
	}
}
