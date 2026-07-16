package mcp

import (
	"context"
	"sync"
	"testing"
	"time"
)

// --- Manager: server config CRUD ---

func TestManager_AddServer(t *testing.T) {
	mgr := NewManager(nil)
	if err := mgr.AddServer(&ServerConfig{Name: "alpha", Transport: "stdio"}); err != nil {
		t.Fatalf("AddServer failed: %v", err)
	}
	// Duplicate should error
	if err := mgr.AddServer(&ServerConfig{Name: "alpha"}); err == nil {
		t.Fatal("expected error for duplicate server, got nil")
	}
}

func TestManager_RemoveServer(t *testing.T) {
	mgr := NewManager([]*ServerConfig{{Name: "alpha"}})
	if err := mgr.RemoveServer("alpha"); err != nil {
		t.Fatalf("RemoveServer failed: %v", err)
	}
	if _, ok := mgr.GetServerConfig("alpha"); ok {
		t.Fatal("server should be removed")
	}
	// Removing non-existent should be a no-op (no error)
	if err := mgr.RemoveServer("nonexistent"); err != nil {
		t.Fatalf("RemoveServer non-existent should not error, got %v", err)
	}
}

func TestManager_GetServerConfig(t *testing.T) {
	mgr := NewManager([]*ServerConfig{{Name: "alpha", Transport: "stdio"}})
	cfg, ok := mgr.GetServerConfig("alpha")
	if !ok {
		t.Fatal("expected to find alpha config")
	}
	if cfg.Transport != "stdio" {
		t.Fatalf("expected stdio transport, got %s", cfg.Transport)
	}
	if _, ok := mgr.GetServerConfig("nonexistent"); ok {
		t.Fatal("non-existent server should return ok=false")
	}
}

func TestManager_GetAllServerConfigs(t *testing.T) {
	mgr := NewManager([]*ServerConfig{
		{Name: "alpha"},
		{Name: "beta"},
	})
	configs := mgr.GetAllServerConfigs()
	if len(configs) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(configs))
	}
	// Empty manager returns empty slice, not nil
	mgr2 := NewManager(nil)
	if got := mgr2.GetAllServerConfigs(); len(got) != 0 {
		t.Fatalf("expected empty slice, got %d", len(got))
	}
}

// --- Manager: connected servers ---

func TestManager_GetConnectedServers(t *testing.T) {
	mgr := NewManager([]*ServerConfig{{Name: "alpha"}, {Name: "beta"}})
	mgr.mu.Lock()
	mgr.clients["alpha"] = &Client{serverName: "alpha"}
	mgr.mu.Unlock()

	connected := mgr.GetConnectedServers()
	if len(connected) != 1 {
		t.Fatalf("expected 1 connected server, got %d", len(connected))
	}
	if connected[0] != "alpha" {
		t.Fatalf("expected alpha, got %s", connected[0])
	}
}

func TestManager_Disconnect(t *testing.T) {
	mgr := NewManager([]*ServerConfig{{Name: "alpha"}})
	// Inject a fake client directly
	tr := newFakeTransport()
	client := newClientWithTransport(t.Context(), &ServerConfig{Name: "alpha"}, tr)
	mgr.mu.Lock()
	mgr.clients["alpha"] = client
	mgr.tools = append(mgr.tools, &MCPTool{serverName: "alpha", toolName: "tool-a"})
	mgr.mu.Unlock()

	if err := mgr.Disconnect("alpha"); err != nil {
		t.Fatalf("Disconnect failed: %v", err)
	}
	if servers := mgr.GetConnectedServers(); len(servers) != 0 {
		t.Fatalf("expected 0 connected after disconnect, got %d", len(servers))
	}
	// Tools should be removed
	if tools := mgr.GetTools(); len(tools) != 0 {
		t.Fatalf("expected 0 tools after disconnect, got %d", len(tools))
	}
	// Disconnect non-existent should be a no-op
	if err := mgr.Disconnect("nonexistent"); err != nil {
		t.Fatalf("Disconnect non-existent should not error, got %v", err)
	}
}

// --- Manager: health ---

func TestManager_SetHealthChangeCallback(t *testing.T) {
	mgr := NewManager(nil)
	called := make(chan struct{})
	var once sync.Once
	mgr.SetHealthChangeCallback(func(name string, healthy bool) {
		once.Do(func() { close(called) })
	})
	if mgr.onHealthChange == nil {
		t.Fatal("expected callback to be set")
	}
	// Verify it fires during CheckHealth
	mgr.mu.Lock()
	mgr.clients["alpha"] = &Client{serverName: "alpha"}
	mgr.health["alpha"] = &ServerHealth{Healthy: true}
	mgr.mu.Unlock()

	// Need a client with ConsecutiveFails >= 3 to trigger unhealthy transition
	mgr.mu.Lock()
	mgr.clients["alpha"].reconnectMu.Lock()
	mgr.clients["alpha"].consecutiveFails = 3
	mgr.clients["alpha"].reconnectMu.Unlock()
	mgr.mu.Unlock()

	mgr.CheckHealth(context.Background())
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("expected health change callback to fire")
	}
}

func TestManager_IsHealthy(t *testing.T) {
	mgr := NewManager(nil)
	// Unknown server is considered healthy
	if !mgr.IsHealthy("unknown") {
		t.Fatal("unknown server should be considered healthy")
	}

	mgr.mu.Lock()
	mgr.health["alpha"] = &ServerHealth{Healthy: false}
	mgr.mu.Unlock()
	if mgr.IsHealthy("alpha") {
		t.Fatal("alpha should be unhealthy")
	}

	mgr.mu.Lock()
	mgr.health["beta"] = &ServerHealth{Healthy: true}
	mgr.mu.Unlock()
	if !mgr.IsHealthy("beta") {
		t.Fatal("beta should be healthy")
	}
}

func TestManager_CheckHealth_UnknownServer(t *testing.T) {
	mgr := NewManager(nil)
	mgr.mu.Lock()
	mgr.clients["alpha"] = &Client{serverName: "alpha"}
	mgr.mu.Unlock()

	mgr.CheckHealth(context.Background())
	// After check, alpha should have a health entry
	if !mgr.IsHealthy("alpha") {
		t.Fatal("alpha with 0 fails should be healthy")
	}
}

// --- Manager: shutdown ---

func TestManager_Shutdown(t *testing.T) {
	mgr := NewManager([]*ServerConfig{{Name: "alpha"}})
	tr := newFakeTransport()
	client := newClientWithTransport(t.Context(), &ServerConfig{Name: "alpha"}, tr)
	mgr.mu.Lock()
	mgr.clients["alpha"] = client
	mgr.tools = append(mgr.tools, &MCPTool{serverName: "alpha"})
	mgr.mu.Unlock()

	if err := mgr.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
	if tools := mgr.GetTools(); len(tools) != 0 {
		t.Fatalf("expected 0 tools after shutdown, got %d", len(tools))
	}
	if servers := mgr.GetConnectedServers(); len(servers) != 0 {
		t.Fatalf("expected 0 connected after shutdown, got %d", len(servers))
	}
}

// --- Client: simple accessors (0% coverage) ---

func TestClient_GetServerName(t *testing.T) {
	tr := newFakeTransport()
	client := newClientWithTransport(t.Context(), &ServerConfig{Name: "myserver"}, tr)
	defer client.Close()

	if got := client.GetServerName(); got != "myserver" {
		t.Fatalf("expected 'myserver', got %q", got)
	}
}

func TestClient_IsInitialized(t *testing.T) {
	tr := newFakeTransport()
	client := newClientWithTransport(t.Context(), &ServerConfig{Name: "srv"}, tr)
	defer client.Close()

	if client.IsInitialized() {
		t.Fatal("expected not initialized")
	}

	client.mu.Lock()
	client.initialized = true
	client.mu.Unlock()

	if !client.IsInitialized() {
		t.Fatal("expected initialized after setting flag")
	}
}

func TestClient_ConsecutiveFails(t *testing.T) {
	tr := newFakeTransport()
	client := newClientWithTransport(t.Context(), &ServerConfig{Name: "srv"}, tr)
	defer client.Close()

	if got := client.ConsecutiveFails(); got != 0 {
		t.Fatalf("expected 0 fails, got %d", got)
	}

	client.reconnectMu.Lock()
	client.consecutiveFails = 5
	client.reconnectMu.Unlock()

	if got := client.ConsecutiveFails(); got != 5 {
		t.Fatalf("expected 5 fails, got %d", got)
	}
}

func TestClient_GetServerInfo(t *testing.T) {
	tr := newFakeTransport()
	client := newClientWithTransport(t.Context(), &ServerConfig{Name: "srv"}, tr)
	defer client.Close()

	if info := client.GetServerInfo(); info != nil {
		t.Fatalf("expected nil ServerInfo before init, got %+v", info)
	}

	client.mu.Lock()
	client.serverInfo = &ServerInfo{Name: "srv", Version: "1.0"}
	client.mu.Unlock()

	info := client.GetServerInfo()
	if info == nil || info.Version != "1.0" {
		t.Fatalf("expected ServerInfo with version 1.0, got %+v", info)
	}
}

func TestClient_Ping_NotInitialized(t *testing.T) {
	tr := newFakeTransport()
	client := newClientWithTransport(t.Context(), &ServerConfig{Name: "srv"}, tr)
	defer client.Close()

	if err := client.Ping(context.Background()); err == nil {
		t.Fatal("expected error for ping on uninitialized client")
	}
}

// --- Manager: ConnectAll with no auto-connect servers ---

func TestManager_ConnectAll_NoAutoConnect(t *testing.T) {
	mgr := NewManager([]*ServerConfig{
		{Name: "alpha", Transport: "stdio", AutoConnect: false},
	})
	if err := mgr.ConnectAll(context.Background()); err != nil {
		t.Fatalf("ConnectAll with no auto-connect should succeed, got %v", err)
	}
}

func TestManager_ConnectAll_Empty(t *testing.T) {
	mgr := NewManager(nil)
	if err := mgr.ConnectAll(context.Background()); err != nil {
		t.Fatalf("ConnectAll with no servers should succeed, got %v", err)
	}
}

// --- Manager: ReadResource with no servers ---

func TestManager_ReadResource_NoServers(t *testing.T) {
	mgr := NewManager(nil)
	_, _, err := mgr.ReadResource(context.Background(), "file:///test")
	if err == nil {
		t.Fatal("expected error for ReadResource with no connected servers")
	}
}

// --- JSONRPCMessage methods ---

func TestJSONRPCMessage_IsRequest(t *testing.T) {
	msg := &JSONRPCMessage{JSONRPC: "2.0", ID: 1, Method: "test"}
	if !msg.IsRequest() {
		t.Fatal("expected IsRequest=true")
	}
	msg2 := &JSONRPCMessage{JSONRPC: "2.0", Method: "test"}
	if msg2.IsRequest() {
		t.Fatal("expected IsRequest=false for notification")
	}
}

func TestJSONRPCMessage_IsNotification(t *testing.T) {
	msg := &JSONRPCMessage{JSONRPC: "2.0", Method: "test"}
	if !msg.IsNotification() {
		t.Fatal("expected IsNotification=true")
	}
	msg2 := &JSONRPCMessage{JSONRPC: "2.0", ID: 1, Method: "test"}
	if msg2.IsNotification() {
		t.Fatal("expected IsNotification=false for request")
	}
}

func TestJSONRPCMessage_IsResponse(t *testing.T) {
	msg := &JSONRPCMessage{JSONRPC: "2.0", ID: 1, Result: "ok"}
	if !msg.IsResponse() {
		t.Fatal("expected IsResponse=true")
	}
	msg2 := &JSONRPCMessage{JSONRPC: "2.0", ID: 1, Method: "test"}
	if msg2.IsResponse() {
		t.Fatal("expected IsResponse=false for request")
	}
}

func TestError_Error(t *testing.T) {
	e := &Error{Code: -32600, Message: "invalid request"}
	if got := e.Error(); got != "invalid request" {
		t.Fatalf("expected 'invalid request', got %q", got)
	}
}
