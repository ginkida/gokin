package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/toolschema"
)

func TestDeferredWirePrefixAndEffectiveExecution(t *testing.T) {
	for _, provider := range []string{"glm", "deepseek"} {
		t.Run(provider, func(t *testing.T) {
			r := NewRegistry()
			target := &scriptedStaticTool{name: "env", content: "target executed"}
			r.MustRegister(target)
			var mu sync.Mutex
			var requests []map[string]json.RawMessage
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				mu.Lock()
				requests = append(requests, body)
				round := len(requests)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"preserved reasoning\"}}\n\n")
				if provider == "deepseek" {
					fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"stable-signature\"}}\n\n")
				}
				fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
				switch round {
				case 1:
					fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"search\",\"name\":\"tool_search\",\"input\":{\"query\":\"env\"}}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
				case 2:
					fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"dispatch\",\"name\":\"use_tool\",\"input\":{\"name\":\"env\",\"arguments\":{}}}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
				default:
					fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Done.\"}}\n\n")
				}
				fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
			}))
			defer server.Close()
			c, err := client.NewAnthropicClient(client.AnthropicConfig{APIKey: "test", BaseURL: server.URL, Model: provider + "-test", Provider: provider, EnableThinking: true})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			toolschema.Install(c, toolschema.New(true))
			c.SetTools(r.GeminiTools())
			e := NewExecutor(r, c, time.Second)
			var observed []string
			e.SetPhaseObserver(func(name string, _ time.Duration, _ bool) { observed = append(observed, name) })
			ctx := ContextWithDeferredCatalog(t.Context(), r, r.GeminiTools())
			ctx = ContextWithToolSchemaCeiling(ctx, e, r.Names())
			history, response, err := e.Execute(ctx, nil, "Inspect the environment using discovery.")
			mu.Lock()
			defer mu.Unlock()
			if err != nil || response != "Done." || target.calls != 1 || len(requests) != 3 {
				t.Fatalf("response=%q err=%v target=%d rounds=%d", response, err, target.calls, len(requests))
			}
			if strings.Join(observed, ",") != "tool_search,env" {
				t.Fatalf("execution evidence=%v", observed)
			}
			for i := 1; i < len(requests); i++ {
				if !bytes.Equal(requests[0]["tools"], requests[i]["tools"]) {
					t.Fatal("discovery changed wire tools")
				}
			}
			if bytes.Contains(requests[0]["tools"], []byte(`"name":"env"`)) {
				t.Fatal("deferred native name was declared")
			}
			paired := false
			for _, message := range history {
				for _, part := range message.Parts {
					if call := part.FunctionCall; call != nil && call.ID == "dispatch" && call.Name != "use_tool" {
						t.Fatal("provider call rewritten")
					}
					if result := part.FunctionResponse; result != nil && result.ID == "dispatch" {
						paired = result.Name == "use_tool"
					}
				}
			}
			if !bytes.Contains(requests[1]["messages"], []byte("preserved reasoning")) || provider == "deepseek" && !bytes.Contains(requests[1]["messages"], []byte("stable-signature")) {
				t.Fatal("thinking replay changed", string(requests[1]["messages"]))
			}
			if !paired {
				t.Fatal("original wire pair lost")
			}
		})
	}
}

func TestDeferredSerializedSchemaSavings(t *testing.T) {
	r := DefaultRegistry(testkit.ResolvedTempDir(t))
	// Match the cloud/headless tools mode: the ordinary sets, without optional
	// hybrid/harness tools or runtime-provided MCP/plugin tools.
	base := r.FilteredGeminiTools(ToolSetCore, ToolSetFileOps, ToolSetWeb, ToolSetGit, ToolSetPlanning, ToolSetAgent, ToolSetAdvanced, ToolSetMemory)
	var mu sync.Mutex
	var payloads []json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		payloads = append(payloads, body["tools"])
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	c, err := client.NewAnthropicClient(client.AnthropicConfig{APIKey: "test", BaseURL: server.URL, Model: "glm-test", Provider: "glm"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, deferred := range []bool{false, true} {
		toolschema.Install(c, toolschema.New(deferred))
		c.SetTools(base)
		stream, err := c.SendMessageWithHistory(t.Context(), nil, "Measure schemas.")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.CollectContext(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	var full, small []any
	if err := json.Unmarshal(payloads[0], &full); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payloads[1], &small); err != nil {
		t.Fatal(err)
	}
	if len(small) != 10 || len(payloads[1])*3 >= len(payloads[0]) {
		t.Fatalf("insufficient reduction: %d/%d tools %d/%d bytes", len(small), len(full), len(payloads[1]), len(payloads[0]))
	}
	saved := utf8.RuneCount(payloads[0]) - utf8.RuneCount(payloads[1])
	t.Logf("actual Anthropic serializer: full=%d tools/%d bytes, deferred=%d tools/%d bytes; saved=%d bytes (%.1f%%), estimated DeepSeek=%.0f and GLM=%.0f tokens (runes/4 and /3.5)", len(full), len(payloads[0]), len(small), len(payloads[1]), len(payloads[0])-len(payloads[1]), 100*float64(len(payloads[0])-len(payloads[1]))/float64(len(payloads[0])), float64(saved)/4, float64(saved)/3.5)
}
