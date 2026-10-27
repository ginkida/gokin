package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"gokin/internal/config"
)

func TestOutputGuardLargeLegitimateWrites(t *testing.T) {
	var table strings.Builder
	table.WriteString("package fixture\nvar tests = []struct{name string; want int}{\n")
	for i := range 10000 {
		fmt.Fprintf(&table, "{name: %q, want: %d},\n", fmt.Sprint("case-", i), i)
	}
	table.WriteString("}\n")
	for name, content := range map[string]string{
		"varied Go test table":   table.String(),
		"identical fixture rows": strings.Repeat("{name: \"fixture\", want: 123},\n", 10000),
		"base64":                 base64.StdEncoding.EncodeToString([]byte(strings.Repeat("binary fixture", 30000))),
		"markdown table":         strings.Repeat("| fixture | 123 | true |\n", 12000),
	} {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(map[string]any{"file_path": "fixture.go", "content": content})
			if err != nil {
				t.Fatal(err)
			}
			if len(data) < 256<<10 {
				t.Fatalf("fixture too small: %d", len(data))
			}
			var guard outputGuard
			for offset := 0; offset < len(data); offset += 137 {
				if err := guard.check("glm-5", "write", string(data[offset:min(offset+137, len(data))]), DefaultMaxToolArgumentBytes); err != nil {
					t.Fatalf("legitimate %d-byte write rejected: %v", len(data), err)
				}
			}
			var args map[string]any
			if err := json.Unmarshal(data, &args); err != nil {
				t.Fatal(err)
			}
			if err := checkToolArgumentRepetition("glm-5", "write", args); err != nil {
				t.Fatalf("completed legitimate write rejected: %v", err)
			}
			if len(guard.tail) > 128<<10 {
				t.Fatal("guard retains unbounded output")
			}
		})
	}
}

func TestOutputGuardChunkBoundariesAndRetryBudget(t *testing.T) {
	for _, size := range []int{1, 37, 512, 65536} {
		var guard outputGuard
		text := "prefix " + strings.Repeat("data = context.artifact_get('x'); ", 3000)
		var err error
		for offset := 0; offset < len(text) && err == nil; offset += size {
			err = guard.check("glm-5", "repl_exec", text[offset:min(offset+size, len(text))], DefaultMaxToolArgumentBytes)
		}
		failure, ok := OutputRecoveryError(err)
		if !ok || failure.Period == 0 || !IsRetryableError(err) {
			t.Fatalf("chunk size %d: error=%v", size, err)
		}
		if shouldFallbackToNextProvider(err) {
			t.Fatal("transparent failover bypassed owning retry")
		}
		ctx := ContextWithOutputRecoveryBudget(context.Background())
		if retry, _ := ClaimOutputRecovery(ctx, err); !retry {
			t.Fatal("first retry refused")
		}
		if recoveryTemperature(ctx, 1) != 0.2 || recoveryTemperature(ctx, 0) != 0.2 {
			t.Fatal("retry temperature not lowered")
		}
		retry, terminal := ClaimOutputRecovery(ContextWithOutputRecoveryBudget(ctx), err)
		if retry || IsRetryableError(terminal) || DetectFailureTelemetry(terminal).Reason != "degenerate_output" {
			t.Fatalf("second trip: retry=%t err=%v", retry, terminal)
		}
		if retry, _ := ClaimOutputRecovery(NewOutputRecoveryBudget(ctx), err); !retry {
			t.Fatal("child inherited exhausted parent budget")
		}
		cancelled, cancel := context.WithCancel(ContextWithOutputRecoveryBudget(context.Background()))
		cancel()
		if retry, terminal := ClaimOutputRecovery(cancelled, err); retry || !errors.Is(terminal, context.Canceled) || outputRecoveryActive(cancelled) {
			t.Fatalf("cancelled turn was attributed to output degeneration: retry=%t err=%v", retry, terminal)
		}
	}
}

func TestToolArgumentCapStreamingAndInline(t *testing.T) {
	for _, inline := range []bool{true, false} {
		for _, limit := range []int{512, 2048} {
			t.Run(fmt.Sprint(inline, "/", limit), func(t *testing.T) {
				input := map[string]any{"content": strings.Repeat("x", 1000)}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					emit := func(event any) { data, _ := json.Marshal(event); fmt.Fprintf(w, "data: %s\n\n", data) }
					block := map[string]any{"type": "tool_use", "id": "call", "name": "write", "input": map[string]any{}}
					if inline {
						block["input"] = input
					}
					emit(map[string]any{"type": "content_block_start", "content_block": block})
					if !inline {
						data, _ := json.Marshal(input)
						for offset := 0; offset < len(data); offset += 111 {
							emit(map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "input_json_delta", "partial_json": string(data[offset:min(offset+111, len(data))])}})
						}
					}
					emit(map[string]any{"type": "content_block_stop"})
					emit(map[string]any{"type": "message_stop"})
				}))
				defer server.Close()
				c, err := NewAnthropicClient(AnthropicConfig{Model: "glm-5", Provider: "glm", APIKey: "test", BaseURL: server.URL, MaxToolArgumentBytes: limit})
				if err != nil {
					t.Fatal(err)
				}
				stream, err := c.SendMessage(t.Context(), "write")
				if err != nil {
					t.Fatal(err)
				}
				response, err := stream.CollectContext(t.Context())
				if limit == 512 {
					failure, ok := OutputRecoveryError(err)
					if !ok || !failure.SizeLimit || len(response.FunctionCalls) != 0 {
						t.Fatalf("oversized call accepted: %+v, %v", response, err)
					}
				} else if err != nil || len(response.FunctionCalls) != 1 {
					t.Fatalf("configured cap ignored: %v", err)
				}
			})
		}
	}
}

func TestOutputGuardClosesActiveResponseBody(t *testing.T) {
	closed := make(chan struct{}, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		data, _ := json.Marshal(map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": strings.Repeat("Let me reconsider this again. ", 2000)}})
		fmt.Fprintf(w, "data: %s\n\n", data)
		w.(http.Flusher).Flush()
		// Connection: close can disable net/http's background peer-read. Keep
		// streaming heartbeats too, so a closed client body is observable by
		// the server through either cancellation or a failed flush.
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case <-r.Context().Done():
				closed <- struct{}{}
				return
			case <-ticker.C:
				fmt.Fprint(w, ": heartbeat\n\n")
				if http.NewResponseController(w).Flush() != nil {
					closed <- struct{}{}
					return
				}
			case <-deadline.C:
				return
			}
		}
	}))
	defer server.Close()
	c, err := NewAnthropicClient(AnthropicConfig{Model: "glm-5", Provider: "glm", APIKey: "test", BaseURL: server.URL, StreamIdleTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	// Disable pooled HTTP workers: only leaked scanner/consumer goroutines
	// should contribute to permanent per-response growth, as in the existing
	// scanner-leak regression. The caller's context stays live throughout.
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	c.httpClient = &http.Client{Transport: transport}
	run := func() {
		stream, err := c.SendMessage(t.Context(), "answer")
		if err != nil {
			t.Fatal(err)
		}
		_, err = stream.CollectContext(t.Context())
		var failure *DegenerateOutputError
		if !errors.As(err, &failure) {
			t.Fatalf("error=%v", err)
		}
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Fatal("guard did not close active response body promptly")
		}
		select {
		case <-stream.Done:
		case <-time.After(2 * time.Second):
			t.Fatal("stream goroutine did not finish")
		}
	}
	run()
	run()
	baseline := runtime.NumGoroutine()
	for end := time.Now().Add(100 * time.Millisecond); time.Now().Before(end); {
		time.Sleep(5 * time.Millisecond)
		baseline = min(baseline, runtime.NumGoroutine())
	}
	for range 8 {
		run()
	}
	for end := time.Now().Add(10 * time.Second); runtime.NumGoroutine() > baseline+2 && time.Now().Before(end); {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > baseline+2 {
		t.Fatalf("guard leaked scanner goroutines: baseline=%d after=%d", baseline, after)
	}
}

func TestToolArgumentLimitFactoryAndClone(t *testing.T) {
	for _, provider := range []string{"glm", "deepseek", "ollama"} {
		for _, name := range config.GetProvider(provider).EnvVars {
			t.Setenv(name, "")
		}
	}
	cfg := config.DefaultConfig()
	cfg.API.GLMKey, cfg.API.DeepSeekKey = "offline-test", "offline-test"
	before := clientConfigFingerprint(cfg, "glm", "glm-5")
	cfg.Model.MaxToolArgumentBytes = 2 << 20
	if before == clientConfigFingerprint(cfg, "glm", "glm-5") {
		t.Fatal("pool fingerprint ignored argument limit")
	}
	for _, provider := range []string{"glm", "deepseek", "ollama"} {
		var c Client
		var err error
		switch provider {
		case "glm":
			c, err = newGLMClient(cfg, "glm-5")
		case "deepseek":
			c, err = newDeepSeekClient(cfg, "deepseek-v4-pro")
		case "ollama":
			c, err = newOllamaClient(cfg, "llama3.2")
		}
		if err != nil {
			t.Fatal(err)
		}
		clone := c.WithModel(c.GetModel())
		switch copy := clone.(type) {
		case *AnthropicClient:
			if copy.config.MaxToolArgumentBytes != 2<<20 {
				t.Fatalf("%s limit lost", provider)
			}
		case *OllamaClient:
			if copy.config.MaxToolArgumentBytes != 2<<20 {
				t.Fatal("Ollama limit lost")
			}
		default:
			t.Fatalf("unexpected clone %T", clone)
		}
		_ = clone.Close()
		_ = c.Close()
	}
}

func TestOllamaOutputGuard(t *testing.T) {
	for _, mode := range []string{"text", "tool", "repeated_tool", "repeated_text_tool"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/x-ndjson")
				if mode == "text" {
					_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"role": "assistant", "content": strings.Repeat("Let me reconsider this again. ", 2000)}})
				} else if mode == "repeated_text_tool" {
					payload := `{"tool":"repl_exec","args":{"code":"` + strings.Repeat("data = context.artifact_get('x'); ", 2000) + `"}}`
					_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"role": "assistant", "content": payload}})
				} else if mode == "repeated_tool" {
					_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"function": map[string]any{"name": "repl_exec", "arguments": map[string]any{"code": strings.Repeat("data = context.artifact_get('x'); ", 2000)}}}}}})
				} else {
					_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"function": map[string]any{"name": "write", "arguments": map[string]any{"content": strings.Repeat("x", 4096)}}}}}})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"done": true})
			}))
			defer server.Close()
			limit := 1024
			model := "llama3.2"
			if mode == "repeated_tool" || mode == "repeated_text_tool" {
				limit = DefaultMaxToolArgumentBytes
			}
			if mode == "repeated_text_tool" {
				model = "text-only-fixture"
			}
			c, err := NewOllamaClient(OllamaConfig{Model: model, BaseURL: server.URL, MaxToolArgumentBytes: limit})
			if err != nil {
				t.Fatal(err)
			}
			stream, err := c.SendMessage(t.Context(), "inspect")
			if err != nil {
				t.Fatal(err)
			}
			response, err := stream.CollectContext(t.Context())
			if failure, ok := OutputRecoveryError(err); !ok || len(response.FunctionCalls) != 0 || (strings.HasPrefix(mode, "repeated_") && failure.Period == 0) {
				t.Fatalf("unguarded Ollama %s: text bytes=%d, calls=%d, error=%v", mode, len(response.Text), len(response.FunctionCalls), err)
			}
		})
	}
}

func TestOllamaTextFallbackArgumentCap(t *testing.T) {
	for _, model := range []string{"text-only-fixture", "llama3.2"} {
		for _, limit := range []int{512, 2048} {
			t.Run(fmt.Sprint(model, "/", limit), func(t *testing.T) {
				payload := `{"tool":"write","args":{"content":"` + strings.Repeat("x", 1000) + `"}}`
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/x-ndjson")
					for offset := 0; offset < len(payload); offset += 111 {
						_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"role": "assistant", "content": payload[offset:min(offset+111, len(payload))]}})
						w.(http.Flusher).Flush()
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"done": true})
				}))
				defer server.Close()
				c, err := NewOllamaClient(OllamaConfig{Model: model, BaseURL: server.URL, MaxToolArgumentBytes: limit})
				if err != nil {
					t.Fatal(err)
				}
				stream, err := c.SendMessage(t.Context(), "write")
				if err != nil {
					t.Fatal(err)
				}
				response, err := stream.CollectContext(t.Context())
				if model == "text-only-fixture" && limit == 512 {
					if failure, ok := OutputRecoveryError(err); !ok || !failure.SizeLimit || failure.Bytes >= len(payload) {
						t.Fatalf("text-encoded tool arguments bypassed the cap: %v", err)
					}
				} else if err != nil || response.Text != payload {
					t.Fatalf("allowed output rejected: %v", err)
				}
			})
		}
	}
}

func TestOutputGuardPlainWordsAndWrappedBase64(t *testing.T) {
	for _, phrase := range []string{"Hello world ", "Yes ", "Продолжаю работу ", "data = context.artifact_get('x');\n"} {
		for phase := range len(phrase) {
			var guard outputGuard
			if err := guard.check("glm-5", "", strings.Repeat(phrase, (64<<10)/len(phrase)+2)[phase:], 0); err == nil {
				t.Fatalf("plain repeating output accepted at phase %d: %q", phase, phrase)
			}
		}
	}
	var guard outputGuard
	wrapped := strings.Repeat("QmFzZTY0IGZpeHR1cmUgYmxvYiB3aXRoIGFjdHVhbCBsaW5lIHdyYXBwaW5nLg==\n", 2000)
	if err := guard.check("glm-5", "write", wrapped, DefaultMaxToolArgumentBytes); err != nil {
		t.Fatalf("wrapped base64 rejected: %v", err)
	}
}

func TestCompleteToolArgumentsRepetition(t *testing.T) {
	for _, inline := range []bool{true, false} {
		t.Run(fmt.Sprint(inline), func(t *testing.T) {
			c := &AnthropicClient{config: AnthropicConfig{Model: "glm-5"}}
			acc := &toolCallAccumulator{}
			args := map[string]any{"code": strings.Repeat("data = context.artifact_get('x'); ", 2000)}
			block := map[string]any{"type": "tool_use", "id": "complete", "name": "repl_exec", "input": map[string]any{}}
			if inline {
				block["input"] = args
			}
			chunk := c.processStreamEvent(map[string]any{"type": "content_block_start", "content_block": block}, acc)
			if !inline {
				data, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				chunk = c.processStreamEvent(map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "input_json_delta", "partial_json": string(data)}}, acc)
				if chunk.Error == nil {
					chunk = c.processStreamEvent(map[string]any{"type": "content_block_stop"}, acc)
				}
			}
			failure, ok := OutputRecoveryError(chunk.Error)
			if !ok || failure.Period == 0 || len(acc.completedCalls) != 0 {
				t.Fatalf("completed JSON concealed repeating string argument: error=%v", chunk.Error)
			}
		})
	}
}

func TestOutputGuardRetainsStreamingModelIdentity(t *testing.T) {
	c := &AnthropicClient{config: AnthropicConfig{Model: "model-at-request"}}
	acc := &toolCallAccumulator{}
	c.processStreamEvent(map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": "Initial text. "}}, acc)
	c.SetModel("next-model")
	updated := make(chan struct{})
	go func() {
		defer close(updated)
		for range 1000 {
			c.SetModel("next-model")
		}
	}()
	chunk := c.processStreamEvent(map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": strings.Repeat("Let me reconsider this again. ", 2000)}}, acc)
	<-updated
	failure, ok := OutputRecoveryError(chunk.Error)
	if !ok || failure.Model != "model-at-request" {
		t.Fatalf("guard attributed an active stream to a later model selection: %v", chunk.Error)
	}
}
