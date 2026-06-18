package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestExtractSSEPayloads(t *testing.T) {
	body := "event: message\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n" +
		"\n" +
		": a comment\n" +
		"event: message\n" +
		"data: {\"jsonrpc\":\"2.0\",\"method\":\"notify\"}\n" +
		"\n"
	got := extractSSEPayloads([]byte(body))
	if len(got) != 2 {
		t.Fatalf("payloads = %d, want 2: %q", len(got), got)
	}
	if !strings.Contains(string(got[0]), `"id":1`) {
		t.Errorf("first payload = %q", got[0])
	}
	if !strings.Contains(string(got[1]), `"method":"notify"`) {
		t.Errorf("second payload = %q", got[1])
	}
	// Multi-line data within one event is joined.
	multi := "data: line1\ndata: line2\n\n"
	m := extractSSEPayloads([]byte(multi))
	if len(m) != 1 || string(m[0]) != "line1\nline2" {
		t.Errorf("multi-line join = %q", m)
	}
}

// TestHTTPTransport_StreamableHTTP pins the streamable-HTTP handshake against a
// mock server that behaves like Z.AI: it requires the text/event-stream Accept,
// replies as SSE, assigns a session id on initialize, and expects that id echoed
// back on subsequent requests.
func TestHTTPTransport_StreamableHTTP(t *testing.T) {
	var sawSessionOnSecond string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			http.Error(w, "missing event-stream Accept", http.StatusBadRequest)
			return
		}
		calls++
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if calls == 1 {
			w.Header().Set("Mcp-Session-Id", "sess-123")
		} else {
			sawSessionOnSecond = r.Header.Get("Mcp-Session-Id")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"call\":%d}}\n\n", msg.ID, calls)
	}))
	defer srv.Close()

	tr, err := NewHTTPTransport(context.Background(), srv.URL, map[string]string{"Authorization": "Bearer x"}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	// initialize → captures session id
	if err := tr.Send(&JSONRPCMessage{JSONRPC: "2.0", ID: 1, Method: "initialize"}); err != nil {
		t.Fatalf("initialize Send: %v", err)
	}
	if m, err := tr.Receive(); err != nil || m.Result == nil {
		t.Fatalf("initialize Receive: %v / %+v", err, m)
	}

	// second request → must echo the session id
	if err := tr.Send(&JSONRPCMessage{JSONRPC: "2.0", ID: 2, Method: "tools/list"}); err != nil {
		t.Fatalf("tools/list Send: %v", err)
	}
	if _, err := tr.Receive(); err != nil {
		t.Fatalf("tools/list Receive: %v", err)
	}
	if sawSessionOnSecond != "sess-123" {
		t.Errorf("Mcp-Session-Id echoed = %q, want sess-123", sawSessionOnSecond)
	}
}

// TestHTTPTransport_LiveZAIWebSearch hits the REAL Z.AI Coding-Plan web search
// MCP server. Skipped unless GLM_KEY is set (network + key required).
func TestHTTPTransport_LiveZAIWebSearch(t *testing.T) {
	key := os.Getenv("GLM_KEY")
	if key == "" {
		t.Skip("set GLM_KEY to run the live Z.AI web search MCP test")
	}
	tr, err := NewHTTPTransport(context.Background(),
		"https://api.z.ai/api/mcp/web_search_prime/mcp",
		map[string]string{"Authorization": "Bearer " + key}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	send := func(id int, method string, params any) *JSONRPCMessage {
		if err := tr.Send(&JSONRPCMessage{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
			t.Fatalf("%s Send: %v", method, err)
		}
		m, err := tr.Receive()
		if err != nil {
			t.Fatalf("%s Receive: %v", method, err)
		}
		return m
	}

	init := send(1, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "gokin", "version": "1"},
	})
	if init.Result == nil {
		t.Fatalf("initialize returned no result: %+v", init)
	}
	tools := send(2, "tools/list", map[string]any{})
	raw, _ := json.Marshal(tools.Result)
	if !strings.Contains(string(raw), "web_search_prime") {
		t.Fatalf("tools/list missing web_search_prime: %s", raw)
	}
	t.Logf("live Z.AI MCP OK — tools: %s", raw)
}
