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

	"google.golang.org/genai"
)

// DeepSeek accepts stable system/tool cache markers but caches conversations
// implicitly. A moving message marker would rewrite an earlier request prefix.
func TestCacheControlMarkersOnTheWire(t *testing.T) {
	const marker = "EPHEMERAL-TURN-STATE"

	var mu sync.Mutex
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(raw, &body)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	// DeepSeek accepts cache markers while caching implicitly. Keep provider
	// identity real so the marker path is exercised through a loopback endpoint.
	c, err := NewAnthropicClient(AnthropicConfig{
		APIKey: "test", BaseURL: srv.URL, Provider: "deepseek", Model: "deepseek-v4-pro",
	})
	if err != nil {
		t.Fatal(err)
	}
	c.SetSystemInstruction("stable system prefix")
	c.SetTurnContext(marker)
	c.SetTools([]*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{
		{Name: "read", Description: "read a file"},
		{Name: "write", Description: "write a file"},
	}}})

	history := []*genai.Content{
		genai.NewContentFromText("turn one", genai.RoleUser),
		genai.NewContentFromText("reply one", genai.RoleModel),
		genai.NewContentFromText("turn two", genai.RoleUser),
		genai.NewContentFromText("reply two", genai.RoleModel),
	}
	ctx := context.Background()
	stream, err := c.SendMessageWithHistory(ctx, history, "turn three")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = stream.CollectContext(ctx)

	mu.Lock()
	got := body
	mu.Unlock()
	if got == nil {
		t.Fatal("no request body captured")
	}

	// 1. The system instruction must be array-of-blocks and carry the marker,
	//    or the largest stable span of the prompt is never cached at all.
	sysBlocks, ok := got["system"].([]any)
	if !ok || len(sysBlocks) == 0 {
		t.Fatalf("system was not converted to cacheable blocks: %T", got["system"])
	}
	if !strings.Contains(marshalString(t, sysBlocks[0]), "cache_control") {
		t.Errorf("system block carries no cache_control: %s", marshalString(t, sysBlocks[0]))
	}

	// 2. The LAST tool carries it, which caches the whole declaration array.
	tools, _ := got["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("no tools reached the request")
	}
	if !strings.Contains(marshalString(t, tools[len(tools)-1]), "cache_control") {
		t.Errorf("the last tool carries no cache_control: %s", marshalString(t, tools[len(tools)-1]))
	}
	for i := 0; i < len(tools)-1; i++ {
		if strings.Contains(marshalString(t, tools[i]), "cache_control") {
			t.Errorf("tool %d carries a marker; only the last one should", i)
		}
	}

	// 3. Implicit conversation caching needs no moving message boundary.
	msgs, _ := got["messages"].([]any)
	for i, raw := range msgs {
		if strings.Contains(marshalString(t, raw), "cache_control") {
			t.Errorf("message %d carries a moving cache marker", i)
		}
	}

	// 4. Fresh turn context must not introduce an explicit message marker.
	for i, raw := range msgs {
		text := marshalString(t, raw)
		if strings.Contains(text, marker) && strings.Contains(text, "cache_control") {
			t.Errorf("message %d carries BOTH the per-turn snapshot and the cache boundary — "+
				"the ephemeral block is inside the cached prefix and re-bills every turn", i)
		}
	}
}
