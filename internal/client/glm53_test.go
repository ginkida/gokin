package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gokin/internal/config"

	"google.golang.org/genai"
)

func TestGLM53ReasoningOnPromptAndToolContinuation(t *testing.T) {
	for _, model := range []string{"glm-5.3", "glm-5.3-flash", "glm-5.3-flashx", "glm-5.3-flash[1m]"} {
		for _, tc := range []struct {
			name           string
			budget         int32
			explicit, want string
		}{
			{"off-is-low", 0, "", "low"},
			{"small", 2048, "auto", "low"},
			{"normal", 4096, "", "high"},
			{"coding", 16384, "", "max"},
			{"explicit-max-survives-off", 0, "max", "max"},
			{"explicit-low-survives-budget", 16384, "low", "low"},
		} {
			t.Run(model+"/"+tc.name, func(t *testing.T) {
				requests := make(chan map[string]any, 2)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode: %v", err)
						w.WriteHeader(400)
						return
					}
					requests <- body
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n"+
						"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"+
						"data: {\"type\":\"message_stop\"}\n\n")
				}))
				defer srv.Close()
				c := &AnthropicClient{config: AnthropicConfig{
					Model: model, Provider: "glm", APIKey: "test", BaseURL: srv.URL,
					MaxTokens: 4096, ReasoningEffort: tc.explicit, StreamIdleTimeout: time.Second,
				}, httpClient: srv.Client()}
				c.SetThinkingBudget(tc.budget)
				stream, err := c.SendMessage(context.Background(), "inspect")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := stream.Collect(); err != nil {
					t.Fatal(err)
				}
				history := []*genai.Content{
					genai.NewContentFromText("inspect", genai.RoleUser),
					{Role: genai.RoleModel, Parts: []*genai.Part{
						{Thought: true, Text: "reasoning\n preserved byte-for-byte"},
						{FunctionCall: &genai.FunctionCall{ID: "call-1", Name: "read", Args: map[string]any{"file_path": "main.go"}}},
					}},
				}
				stream, err = c.SendFunctionResponse(context.Background(), history,
					[]*genai.FunctionResponse{{ID: "call-1", Name: "read", Response: map[string]any{"content": "package main"}}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := stream.Collect(); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					body := <-requests
					thinking, _ := body["thinking"].(map[string]any)
					if thinking["type"] != "enabled" || thinking["clear_thinking"] != false {
						t.Fatalf("request %d: thinking = %#v", i, thinking)
					}
					if _, ok := thinking["budget_tokens"]; ok {
						t.Fatal("GLM-5.3 must use effort, not numeric budget")
					}
					output, _ := body["output_config"].(map[string]any)
					if output["effort"] != tc.want {
						t.Fatalf("request %d effort = %#v, want %s", i, output, tc.want)
					}
					if i == 1 {
						messages := body["messages"].([]any)
						assistant := messages[1].(map[string]any)
						blocks := assistant["content"].([]any)
						if blocks[0].(map[string]any)["thinking"] != history[1].Parts[0].Text {
							t.Fatalf("tool continuation lost reasoning: %#v", blocks)
						}
					}
				}
			})
		}
	}
}

func TestGLM53FactoryEffortAndPoolIdentity(t *testing.T) {
	cfg := newGLMTestConfig()
	cfg.Model.Name = config.DefaultGLMModel
	for _, mode := range []string{"auto", "off"} {
		cfg.Model.ThinkingMode = mode
		c, err := newGLMClient(cfg, cfg.Model.Name)
		if err != nil {
			t.Fatal(err)
		}
		ac := c.(*AnthropicClient)
		if !ac.config.EnableThinking {
			t.Fatal("GLM-5.3 reasoning disabled")
		}
		want := "max"
		if mode == "off" {
			want = "low"
		}
		if got := config.GLMEffortForBudget(ac.config.ThinkingBudget); got != want {
			t.Fatalf("%s: effort=%s, want %s", mode, got, want)
		}
		_ = c.Close()
	}
	before := clientConfigFingerprint(cfg, "glm", cfg.Model.Name)
	cfg.Model.ReasoningEffort = "high"
	if before == clientConfigFingerprint(cfg, "glm", cfg.Model.Name) {
		t.Fatal("effort changes must invalidate pooled config")
	}
	cfg.Model.ReasoningEffort = "typo"
	if _, err := newGLMClient(cfg, cfg.Model.Name); err == nil {
		t.Fatal("invalid effort accepted")
	}
}
