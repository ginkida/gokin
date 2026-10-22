package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/skills"
)

func TestInlineSkillExecutorWireModelEffortCostAndNextTurnRestore(t *testing.T) {
	t.Setenv("GOKIN_PROVIDER_HEALTH_FILE", t.TempDir()+"/health.json")
	root := t.TempDir()
	writeToolSkill(t, root, "fast", "---\nmodel: haiku\neffort: max\n---\nReview code.")
	writeToolSkill(t, root, "low", "---\nmodel: inherit\neffort: low\n---\nContinue reviewing.")
	var mu sync.Mutex
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		index := len(requests) - 1
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n")
		stop := "end_turn"
		if index <= 1 {
			name := "fast"
			if index == 1 {
				name = "low"
			}
			event, _ := json.Marshal(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("skill-%d", index), "name": "skill", "input": map[string]any{"name": name}}})
			fmt.Fprintf(w, "data: %s\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n", event)
			stop = "tool_use"
		} else {
			_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Review finished.\"}}\n\n")
		}
		fmt.Fprintf(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n", stop)
	}))
	defer srv.Close()
	c, err := client.NewAnthropicClient(client.AnthropicConfig{Provider: "glm", Model: "glm-5.3", APIKey: "test", BaseURL: srv.URL, MaxTokens: 4096, ReasoningEffort: "low", StreamIdleTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	skill := NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}))
	registry.MustRegister(skill)
	c.SetTools(registry.GeminiTools())
	c.SetSystemInstruction("Owner system instructions.")
	executor := NewExecutor(registry, c, time.Second)
	executor.SetCostCalculator(func(provider, model string, _, _, _ int) (float64, bool) {
		if provider != "glm" {
			return 0, false
		}
		if model == "glm-5.3-flash" {
			return 0.02, true
		}
		return 0.01, model == "glm-5.3"
	})
	ctx := ContextWithMaxBudgetUSD(t.Context(), 0.05)
	history, text, err := executor.Execute(ctx, nil, "Review code using the skills.")
	if err != nil || text != "Review finished." {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if cost, tracked := executor.GetLastEstimatedCost(); !tracked || cost < 0.04999 || cost > 0.05001 {
		t.Fatalf("cost=%v tracked=%v", cost, tracked)
	}
	provider, model := executor.GetLastProviderIdentity()
	if provider != "glm" || model != "glm-5.3-flash" {
		t.Fatalf("identity=%s/%s", provider, model)
	}
	if _, _, err = executor.Execute(t.Context(), history, "Next unrelated turn."); err != nil {
		t.Fatal(err)
	}
	loaded, err := skill.ExecuteForUserArgumentsContext(executor.SkillInvocationContext(t.Context()), "fast", nil)
	if err != nil || !loaded.Success {
		t.Fatalf("handoff=%+v err=%v", loaded, err)
	}
	if _, _, err = executor.Execute(t.Context(), nil, loaded.Content); err != nil {
		t.Fatal(err)
	}
	if _, _, err = executor.Execute(t.Context(), nil, "Another unrelated turn."); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	wantModels := []string{"glm-5.3", "glm-5.3-flash", "glm-5.3-flash", "glm-5.3", "glm-5.3-flash", "glm-5.3"}
	wantEfforts := []string{"low", "max", "low", "low", "max", "low"}
	if len(requests) != len(wantModels) {
		t.Fatalf("requests=%d", len(requests))
	}
	for i, request := range requests {
		if request["model"] != wantModels[i] || request["output_config"].(map[string]any)["effort"] != wantEfforts[i] {
			t.Fatalf("request %d model=%v effort=%v", i, request["model"], request["output_config"])
		}
		if request["system"] != "Owner system instructions." || len(request["tools"].([]any)) == 0 {
			t.Fatalf("request %d lost system/schema", i)
		}
		if i == 1 || i == 2 {
			encoded, _ := json.Marshal(request["messages"])
			if !containsWireToolResult(encoded) {
				t.Fatalf("request %d lost tool continuation", i)
			}
		}
	}
	if c.GetModel() != "glm-5.3" {
		t.Fatal("skill changed shared session client")
	}
	if len(skill.InvocationLedger().SnapshotNewestFirst()) != 2 {
		t.Fatal("scope reset lost retained instructions")
	}
}

func containsWireToolResult(encoded []byte) bool {
	var messages []struct{ Content []struct{ Type string } }
	if err := json.Unmarshal(encoded, &messages); err != nil {
		return false
	}
	for _, message := range messages {
		for _, part := range message.Content {
			if part.Type == "tool_result" {
				return true
			}
		}
	}
	return false
}
