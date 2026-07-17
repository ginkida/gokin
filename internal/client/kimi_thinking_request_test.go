package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/genai"
)

// K2.x models support disabling reasoning — `/thinking off` and adaptive easy
// turns send the explicit disabled marker (omission would leave server-side
// reasoning ON and make the UI/config lie).
func TestKimiThinkingOffIsExplicitOnBothRequestPaths(t *testing.T) {
	requests := make(chan map[string]any, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	c := &AnthropicClient{
		config: AnthropicConfig{
			Model: "kimi-for-coding", BaseURL: srv.URL, APIKey: "test", Provider: "kimi",
			EnableThinking: false, ThinkingBudget: 8192,
			StreamIdleTimeout: 5 * time.Second,
		},
		httpClient: srv.Client(),
	}

	history := []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}
	stream, err := c.SendMessageWithHistory(context.Background(), history, "")
	if err != nil {
		t.Fatalf("SendMessageWithHistory: %v", err)
	}
	if _, err := stream.Collect(); err != nil {
		t.Fatalf("collect message response: %v", err)
	}

	stream, err = c.SendFunctionResponse(context.Background(), history, []*genai.FunctionResponse{{
		ID: "tool-1", Name: "read", Response: map[string]any{"result": "ok"},
	}})
	if err != nil {
		t.Fatalf("SendFunctionResponse: %v", err)
	}
	if _, err := stream.Collect(); err != nil {
		t.Fatalf("collect function response: %v", err)
	}

	for i := 0; i < 2; i++ {
		body := <-requests
		thinking, ok := body["thinking"].(map[string]any)
		if !ok {
			t.Fatalf("request %d omitted explicit Kimi thinking config: %#v", i+1, body["thinking"])
		}
		if got := thinking["type"]; got != "disabled" {
			t.Fatalf("request %d thinking.type = %#v, want disabled", i+1, got)
		}
		if _, exists := thinking["budget_tokens"]; exists {
			t.Fatalf("request %d disabled thinking retained budget_tokens", i+1)
		}
	}
}

// K3 is an ALWAYS-ON reasoning flagship (reasoning_effort supports only
// "max"): the {"type":"disabled"} marker put it into a degraded no-thinking
// mode that re-emitted its narration verbatim before every tool call
// (v0.100.101 field report). For K3 the thinking field must be OMITTED when
// the local flag is off — server-side reasoning stays on.
func TestKimiK3NeverReceivesDisabledThinkingMarker(t *testing.T) {
	requests := make(chan map[string]any, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	c := &AnthropicClient{
		config: AnthropicConfig{
			Model: "k3", BaseURL: srv.URL, APIKey: "test", Provider: "kimi",
			EnableThinking: false, ThinkingBudget: 8192,
			StreamIdleTimeout: 5 * time.Second,
		},
		httpClient: srv.Client(),
	}

	history := []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}
	stream, err := c.SendMessageWithHistory(context.Background(), history, "")
	if err != nil {
		t.Fatalf("SendMessageWithHistory: %v", err)
	}
	if _, err := stream.Collect(); err != nil {
		t.Fatalf("collect message response: %v", err)
	}
	stream, err = c.SendFunctionResponse(context.Background(), history, []*genai.FunctionResponse{{
		ID: "tool-1", Name: "read", Response: map[string]any{"result": "ok"},
	}})
	if err != nil {
		t.Fatalf("SendFunctionResponse: %v", err)
	}
	if _, err := stream.Collect(); err != nil {
		t.Fatalf("collect function response: %v", err)
	}

	for i := 0; i < 2; i++ {
		body := <-requests
		if thinking, exists := body["thinking"]; exists {
			t.Fatalf("request %d must OMIT the thinking field for K3 when local thinking is off, got %#v", i+1, thinking)
		}
	}

	// With thinking ON, K3 still gets the normal enabled+budget config.
	if !kimiAlwaysOnReasoningModel("k3") || !kimiAlwaysOnReasoningModel("kimi-k3-preview") {
		t.Fatal("k3 family must classify as always-on reasoning")
	}
	if kimiAlwaysOnReasoningModel("kimi-for-coding") || kimiAlwaysOnReasoningModel("kimi-for-coding-highspeed") {
		t.Fatal("K2.x models are NOT always-on — they keep the disabled marker")
	}
}

// Fail-loud drift guard: every registered Kimi model must be EXPLICITLY
// classified — either always-on reasoning (K3 family: the disabled marker is
// never sent) or a known K2.x id (disable IS supported). A new Kimi model
// (K4, a renamed flagship, …) added to AvailableModels without updating
// kimiAlwaysOnReasoningModel would SILENTLY receive the disabled marker on
// adaptive easy turns — the exact degraded-repetition failure the K3 field
// report exposed. This test forces that decision to be made consciously.
func TestEveryKimiModelIsExplicitlyClassifiedForThinkingDisable(t *testing.T) {
	knownDisableSupported := map[string]bool{
		"kimi-for-coding":           true, // K2.7
		"kimi-for-coding-highspeed": true, // K2.7 highspeed
	}
	seen := 0
	for _, m := range AvailableModels {
		if m.Provider != "kimi" {
			continue
		}
		seen++
		if kimiAlwaysOnReasoningModel(m.ID) {
			continue // always-on family — never receives the disabled marker
		}
		if !knownDisableSupported[m.ID] {
			t.Errorf("Kimi model %q is not classified: add it to kimiAlwaysOnReasoningModel (always-on reasoning) or to this test's knownDisableSupported set (disable genuinely supported) — an unclassified model silently gets the disabled marker and can degrade like the K3 field report", m.ID)
		}
	}
	if seen == 0 {
		t.Fatal("no Kimi models found in AvailableModels — registry moved?")
	}
}
