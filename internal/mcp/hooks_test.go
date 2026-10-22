package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func hookMCPServer(t *testing.T, handle func(*JSONRPCMessage) any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request JSONRPCMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(400)
			return
		}
		if request.ID == nil {
			w.WriteHeader(202)
			return
		}
		var result any
		if handle != nil {
			result = handle(&request)
		}
		if result == nil {
			switch request.Method {
			case MethodInitialize:
				result = map[string]any{"protocolVersion": ProtocolVersion, "serverInfo": map[string]any{"name": "hook-fixture", "version": "1"}}
			case MethodToolsList:
				result = map[string]any{"tools": []any{map[string]any{"name": "check", "inputSchema": map[string]any{"type": "object", "required": []string{"path"}, "properties": map[string]any{"path": map[string]any{"type": "string"}}}}}}
			case MethodToolsCall:
				result = &CallToolResult{Content: []*ContentBlock{{Type: "text", Text: `{"continue":true}`}}}
			default:
				result = map[string]any{}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
}

func TestMCPHookLazyConnectionExactIdentityAndPolicy(t *testing.T) {
	var requests, calls atomic.Int32
	server := hookMCPServer(t, func(request *JSONRPCMessage) any {
		requests.Add(1)
		if request.Method == MethodToolsCall {
			calls.Add(1)
		}
		return nil
	})
	defer server.Close()
	cfg := &ServerConfig{Name: "plugin:suite:db", Transport: "http", URL: server.URL, AllowedTools: []string{"check"}}
	m := NewManager([]*ServerConfig{cfg})
	defer m.Shutdown(t.Context())
	for _, name := range []string{"db", "suite_db", "other"} {
		if _, err := m.CallHookTool(t.Context(), name, "check", nil); err == nil {
			t.Fatal("MCP hook fell back across server namespace")
		}
	}
	if _, err := m.CallHookTool(t.Context(), cfg.Name, "danger", nil); err == nil || requests.Load() != 0 {
		t.Fatal("MCP allowlist bypassed before connect")
	}
	if err := m.SetPaused(cfg.Name, true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CallHookTool(t.Context(), cfg.Name, "check", nil); err == nil || requests.Load() != 0 {
		t.Fatal("paused MCP server connected")
	}
	_ = m.SetPaused(cfg.Name, false)
	if _, err := m.CallHookTool(t.Context(), cfg.Name, "check", nil); err == nil || calls.Load() != 0 {
		t.Fatal("MCP required args were not validated")
	}
	output, err := m.CallHookTool(t.Context(), cfg.Name, "check", map[string]any{"path": "proof"})
	if err != nil || output.IsError || output.Text != `{"continue":true}` || calls.Load() != 1 {
		t.Fatalf("lazy hook failed: %+v %v", output, err)
	}
	if err := m.SetPaused(cfg.Name, true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CallHookTool(t.Context(), cfg.Name, "check", map[string]any{"path": "proof"}); err == nil || calls.Load() != 1 {
		t.Fatal("connected paused server ran")
	}
	_ = m.SetPaused(cfg.Name, false)
	copy := *cfg
	copy.AllowedTools = []string{"other"}
	if err := m.UpdateServer(&copy); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CallHookTool(t.Context(), cfg.Name, "check", map[string]any{"path": "proof"}); err == nil || calls.Load() != 1 {
		t.Fatal("updated allowlist retained old tool authority")
	}
}

func TestMCPHookWaitsForStartupWithoutHoldingLockOrDuplicatingConnection(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	var initializations atomic.Int32
	server := hookMCPServer(t, func(request *JSONRPCMessage) any {
		if request.Method == MethodInitialize {
			initializations.Add(1)
			close(started)
			<-release
		}
		return nil
	})
	defer server.Close()
	defer releaseOnce.Do(func() { close(release) })
	m := NewManager([]*ServerConfig{{Name: "fixture", Transport: "http", URL: server.URL, AutoConnect: true}})
	defer m.Shutdown(t.Context())
	startup := make(chan error, 1)
	go func() { startup <- m.ConnectAll(t.Context()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("startup never began")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := m.CallHookTool(ctx, "fixture", "check", map[string]any{"path": "proof"})
	if !errors.Is(err, context.DeadlineExceeded) || initializations.Load() != 1 {
		t.Fatalf("hook deadline lost or duplicate handshake: %v %d", err, initializations.Load())
	}
	readDone := make(chan struct{})
	go func() { _ = m.GetTools(); close(readDone) }()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("startup holds MCP manager lock")
	}
	results := make(chan error, 8)
	for range 8 {
		go func() {
			_, err := m.CallHookTool(t.Context(), "fixture", "check", map[string]any{"path": "proof"})
			results <- err
		}()
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-startup; err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if initializations.Load() != 1 {
		t.Fatal("parallel MCP hooks opened duplicate connections")
	}
}

func TestMCPHookReadsOnlyTextAndRetainsErrorFlag(t *testing.T) {
	for _, test := range []struct {
		name   string
		result *CallToolResult
		text   string
		failed bool
	}{
		{"text-only", &CallToolResult{Content: []*ContentBlock{nil, {Type: "image", Data: "not text"}, {Type: "text", Text: "first"}, {Type: "resource", Text: "ignored"}, {Type: "text", Text: "second"}}}, "first\nsecond", false},
		{"server-error", &CallToolResult{IsError: true, Content: []*ContentBlock{{Type: "text", Text: `{"continue":false}`}}}, `{"continue":false}`, false},
		{"limit", &CallToolResult{Content: []*ContentBlock{{Type: "text", Text: strings.Repeat("x", 64<<10)}}}, strings.Repeat("x", 64<<10), false},
		{"over-limit", &CallToolResult{Content: []*ContentBlock{{Type: "text", Text: strings.Repeat("x", (64<<10)+1)}}}, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := hookMCPServer(t, func(request *JSONRPCMessage) any {
				if request.Method == MethodToolsCall {
					return test.result
				}
				return nil
			})
			defer server.Close()
			m := NewManager([]*ServerConfig{{Name: "fixture", Transport: "http", URL: server.URL}})
			defer m.Shutdown(t.Context())
			output, err := m.CallHookTool(t.Context(), "fixture", "check", map[string]any{"path": "proof"})
			if (err != nil) != test.failed || output.Text != test.text || output.IsError != test.result.IsError {
				t.Fatalf("wrong MCP output: size=%d err=%v flag=%t", len(output.Text), err, output.IsError)
			}
		})
	}
}

func TestMCPHookPluginGuardRechecksBeforeCall(t *testing.T) {
	var allowed atomic.Bool
	allowed.Store(true)
	var calls atomic.Int32
	server := hookMCPServer(t, func(request *JSONRPCMessage) any {
		if request.Method == MethodToolsCall {
			calls.Add(1)
		}
		return nil
	})
	defer server.Close()
	cfg := &ServerConfig{Name: "fixture", Transport: "http", URL: server.URL, guard: func(context.Context) error {
		if !allowed.Load() {
			return errors.New("plugin disabled")
		}
		return nil
	}}
	m := NewManager([]*ServerConfig{cfg})
	defer m.Shutdown(t.Context())
	if _, err := m.CallHookTool(t.Context(), "fixture", "check", map[string]any{"path": "proof"}); err != nil {
		t.Fatal(err)
	}
	allowed.Store(false)
	if _, err := m.CallHookTool(t.Context(), "fixture", "check", map[string]any{"path": "proof"}); err == nil || calls.Load() != 1 {
		t.Fatal("revoked MCP plugin reached server")
	}
}
