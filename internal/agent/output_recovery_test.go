package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestAgentDegenerateRecoveryAfterToolResults(t *testing.T) {
	t.Setenv("GOKIN_PROVIDER_HEALTH_FILE", filepath.Join(t.TempDir(), "health.json"))
	for _, recover := range []bool{true, false} {
		t.Run(fmt.Sprint(recover), func(t *testing.T) {
			workDir := testkit.ResolvedTempDir(t)
			path := filepath.Join(workDir, "fixture.txt")
			if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var requests []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				requests = append(requests, req)
				index := len(requests)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				emit := func(event any) { data, _ := json.Marshal(event); fmt.Fprintf(w, "data: %s\n\n", data) }
				if index == 1 {
					emit(map[string]any{"type": "content_block_start", "content_block": map[string]any{"type": "tool_use", "id": "read-1", "name": "read", "input": map[string]any{"file_path": path}}})
					emit(map[string]any{"type": "content_block_stop"})
				} else if index == 3 && recover {
					emit(map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": "Inspection finished."}})
				} else {
					emit(map[string]any{"type": "content_block_start", "content_block": map[string]any{"type": "tool_use", "id": "bad", "name": "read", "input": map[string]any{}}})
					emit(map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "input_json_delta", "partial_json": strings.Repeat("data = context.artifact_get('x'); ", 2000)}})
				}
				emit(map[string]any{"type": "message_stop"})
			}))
			defer server.Close()
			c, err := client.NewAnthropicClient(client.AnthropicConfig{Model: "glm-5", Provider: "glm", APIKey: "test", BaseURL: server.URL, Temperature: 1})
			if err != nil {
				t.Fatal(err)
			}
			registry := tools.NewRegistry()
			registry.MustRegister(tools.NewReadTool(workDir))
			agent := NewAgent(AgentTypeGeneral, c, registry, workDir, 5, "", nil, nil)
			result, err := agent.Run(t.Context(), "inspect fixture.txt")
			if recover && (err != nil || result.Status != AgentStatusCompleted) {
				t.Fatalf("recovery result=%+v err=%v", result, err)
			}
			if !recover {
				failure, ok := client.OutputRecoveryError(err)
				if !ok || !failure.Exhausted || result.Status != AgentStatusFailed {
					t.Fatalf("second trip result=%+v err=%v", result, err)
				}
			}
			optimizer := NewStrategyOptimizer(t.TempDir())
			(&Runner{}).recordAgentExecutionLearning(runnerAgentDeps{strategyOptimizer: optimizer}, "general", "inspect", result, result.Duration, "spawn")
			metric, _ := optimizer.GetMetrics("general")
			if metric.SuccessCount+metric.FailureCount != 1 || (recover && metric.SuccessCount != 1) || (!recover && metric.FailureCount != 1) {
				t.Fatalf("terminal accounting=%+v", metric)
			}
			if agent.ToolsUsedCount() != 1 {
				t.Fatalf("tools executed=%d", agent.ToolsUsedCount())
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requests) != 3 {
				t.Fatalf("requests=%d", len(requests))
			}
			prior := requests[1]["messages"].([]any)
			retried := requests[2]["messages"].([]any)
			if len(retried) != len(prior)+2 || !reflect.DeepEqual(prior, retried[:len(prior)]) || !reflect.DeepEqual(requests[1]["system"], requests[2]["system"]) {
				t.Fatal("retry changed cached prefix")
			}
			if requests[2]["temperature"] != float64(0.2) {
				t.Fatal("retry did not lower temperature")
			}
		})
	}
}
