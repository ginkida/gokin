package client

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"gokin/internal/config"

	"google.golang.org/genai"
)

// Opt in with GOKIN_LIVE_GLM_CONFIG=/path/to/config.yaml. The test sends only
// synthetic prompts and tool results, never repository content. Config is
// read without Save; TestMain redirects persisted provider health to temp.
func TestLiveGLM53ToolContinuation(t *testing.T) {
	path := os.Getenv("GOKIN_LIVE_GLM_CONFIG")
	if path == "" {
		t.Skip("set GOKIN_LIVE_GLM_CONFIG to run paid provider checks")
	}
	base, err := config.LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{config.GLMFlagshipModel, config.GLMFlashModel} {
		for _, effort := range []string{"low", "high", "max"} {
			t.Run(model+"/"+effort, func(t *testing.T) {
				cfg := base.Clone()
				cfg.Model.CustomBaseURL = ""
				cfg.Model.Name = model
				cfg.Model.ReasoningEffort = effort
				cfg.Model.MaxOutputTokens = 4096
				c, err := newGLMClient(cfg, model)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				c.SetThinkingBudget(0) // explicit effort must survive adaptive-off
				c.SetSystemInstruction("You are testing a tool protocol. Call probe exactly once. After its result, reply with the received value and stop. Be concise.")
				c.SetTools([]*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{
					Name: "probe", Description: "Returns a test value.",
					Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{}},
				}}}})
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				history := []*genai.Content{genai.NewContentFromText("Call probe now.", genai.RoleUser)}
				stream, err := c.SendMessageWithHistory(ctx, history, "")
				if err != nil {
					t.Fatal(err)
				}
				first, err := ProcessStream(ctx, stream, &StreamHandler{})
				if err != nil {
					t.Fatal(err)
				}
				if len(first.FunctionCalls) != 1 || first.FunctionCalls[0].Name != "probe" {
					t.Fatalf("expected exactly one probe call, got %d calls", len(first.FunctionCalls))
				}
				history = append(history, genai.NewContentFromParts(first.Parts, genai.RoleModel))
				stream, err = c.SendFunctionResponse(ctx, history, []*genai.FunctionResponse{{
					ID: first.FunctionCalls[0].ID, Name: "probe", Response: map[string]any{"value": "gokin-glm-probe-ok"},
				}})
				if err != nil {
					t.Fatal(err)
				}
				second, err := ProcessStream(ctx, stream, &StreamHandler{})
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(second.Text, "gokin-glm-probe-ok") || len(second.FunctionCalls) > 0 {
					t.Fatalf("tool result was not used in a terminal answer: text=%q calls=%d finish=%q input=%d output=%d reasoning_chars=%d", second.Text, len(second.FunctionCalls), second.FinishReason, second.InputTokens, second.OutputTokens, len(second.Thinking))
				}
				if first.InputTokens+first.CacheReadInputTokens+first.CacheCreationInputTokens == 0 || second.InputTokens+second.CacheReadInputTokens+second.CacheCreationInputTokens == 0 {
					t.Fatalf("provider input usage is missing: first=%d/%d/%d second=%d/%d/%d", first.InputTokens, first.CacheReadInputTokens, first.CacheCreationInputTokens, second.InputTokens, second.CacheReadInputTokens, second.CacheCreationInputTokens)
				}
				t.Logf("tool continuation passed; input=%d output=%d reasoning_chars=%d", first.InputTokens+second.InputTokens, first.OutputTokens+second.OutputTokens, len(first.Thinking)+len(second.Thinking))
			})
		}
	}
}
