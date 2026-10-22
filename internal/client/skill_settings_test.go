package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestResolveSkillModelSettings(t *testing.T) {
	for _, tc := range []struct {
		provider, current, selection, effort, wantModel, wantEffort string
		wantError                                                   bool
	}{
		{"glm", "glm-5.3-flash", "inherit", "", "glm-5.3-flash", "", false},
		{"glm", "glm-5.3", "haiku", "low", "glm-5.3-flash", "low", false},
		{"glm", "glm-5.3-flash", "sonnet", "medium", "glm-5.3", "high", false},
		{"glm", "glm-5.3-flash", "opus", "xhigh", "glm-5.3", "max", false},
		{"glm", "glm-5.3", "glm-5.3-flashx", "max", "glm-5.3-flashx", "max", false},
		{"glm", "glm-5.3", "GLM-5.3-FLASH", "high", "glm-5.3-flash", "high", false},
		{"glm", "glm-5.3", "glm-4.7", "", "glm-4.7", "", false},
		{"glm", "glm-5.3", "deepseek-v4-pro", "", "", "", true},
		{"glm", "glm-5.3", "made-up", "", "", "", true},
		{"glm", "glm-5.3", "glm-4.7", "max", "", "", true},
		{"glm", "glm-5.3", "inherit", "garbage", "", "", true},
		{"deepseek", "deepseek-v4-pro", "sonnet", "", "", "", true},
		{"deepseek", "deepseek-v4-pro", "inherit", "high", "", "", true},
		{"deepseek", "deepseek-v4-pro", "inherit", "", "deepseek-v4-pro", "", false},
	} {
		t.Run(tc.provider+"/"+tc.selection+"/"+tc.effort, func(t *testing.T) {
			c := &AnthropicClient{config: AnthropicConfig{Provider: tc.provider, Model: tc.current}}
			model, effort, err := ResolveSkillModelSettings(c, tc.selection, tc.effort)
			if (err != nil) != tc.wantError || model != tc.wantModel || effort != tc.wantEffort {
				t.Fatalf("model=%q effort=%q err=%v", model, effort, err)
			}
		})
	}
}

func TestRequestEffortOverrideIsConcurrentLocalAndCoversToolResults(t *testing.T) {
	var mu sync.Mutex
	requests := make(map[string]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		messages, _ := json.Marshal(body["messages"])
		key := "parent"
		if strings.Contains(string(messages), "child-max") {
			key = "child-max"
		}
		if strings.Contains(string(messages), "child-low") {
			key = "child-low"
		}
		if strings.Contains(string(messages), "tool-output") {
			key = "tool-output"
		}
		mu.Lock()
		requests[key] = body["output_config"].(map[string]any)["effort"].(string)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()
	c := &AnthropicClient{config: AnthropicConfig{Provider: "glm", Model: "glm-5.3", APIKey: "test", BaseURL: srv.URL, MaxTokens: 4096, ReasoningEffort: "high", StreamIdleTimeout: time.Second}, httpClient: srv.Client()}
	var wg sync.WaitGroup
	for _, effort := range []string{"low", "max"} {
		wg.Go(func() {
			stream, err := c.SendMessage(ContextWithReasoningEffort(t.Context(), effort), "child-"+effort)
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := stream.Collect(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	stream, err := c.SendFunctionResponse(ContextWithReasoningEffort(t.Context(), "max"), []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "read-1", Name: "read"}}}},
	}, []*genai.FunctionResponse{{ID: "read-1", Name: "read", Response: map[string]any{"content": "tool-output"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Collect(); err != nil {
		t.Fatal(err)
	}
	stream, err = c.SendMessage(context.Background(), "parent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Collect(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests["child-max"] != "max" || requests["child-low"] != "low" || requests["tool-output"] != "max" || requests["parent"] != "high" {
		t.Fatalf("requests=%v", requests)
	}
	if c.config.ReasoningEffort != "high" {
		t.Fatal("request override changed client config")
	}
}

func TestUnsupportedRequestEffortFailsBeforeNetwork(t *testing.T) {
	for _, tc := range []struct{ provider, model, effort string }{
		{"deepseek", "deepseek-v4-pro", "high"}, {"glm", "glm-4.7", "max"}, {"glm", "glm-5.3", "medium"},
	} {
		c := &AnthropicClient{config: AnthropicConfig{Provider: tc.provider, Model: tc.model}}
		ctx := ContextWithReasoningEffort(t.Context(), tc.effort)
		if _, err := c.SendMessage(ctx, "hello"); err == nil {
			t.Fatal("unsupported prompt effort accepted")
		}
		if _, err := c.SendFunctionResponse(ctx, nil, nil); err == nil {
			t.Fatal("unsupported tool effort accepted")
		}
	}
}
