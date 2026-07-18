package mcp

import (
	"context"
	"testing"
)

func TestServerConfigToolAllowed(t *testing.T) {
	var nilCfg *ServerConfig
	if !nilCfg.ToolAllowed("anything") {
		t.Error("nil config must allow everything")
	}
	open := &ServerConfig{Name: "s"}
	if !open.ToolAllowed("write_file") {
		t.Error("empty whitelist must allow everything")
	}
	locked := &ServerConfig{Name: "s", AllowedTools: []string{"read_file", "list_dir"}}
	if !locked.ToolAllowed("read_file") {
		t.Error("whitelisted tool must pass")
	}
	if locked.ToolAllowed("write_file") {
		t.Error("non-whitelisted tool must be hidden")
	}
}

// AllowedTools filters at registration: a server exposing [echo, danger] with
// a whitelist of [echo] must surface ONLY echo to the registry/model.
// Exercised through RefreshTools — the same path tools/list_changed uses.
func TestRefreshTools_AllowedToolsWhitelistFilters(t *testing.T) {
	tr := newFakeTransport()
	server := startFakeServer(t, tr, []*ToolInfo{
		{Name: "echo", Description: "safe"},
		{Name: "danger", Description: "unsafe"},
	})
	t.Cleanup(func() { server.cancel() })

	cfg := &ServerConfig{Name: "fake", Transport: "stdio", AllowedTools: []string{"echo"}}
	client := newClientWithTransport(context.Background(), cfg, tr)
	if err := client.Initialize(t.Context()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close(); _ = client.Close() })

	mgr := NewManager([]*ServerConfig{cfg})
	mgr.mu.Lock()
	mgr.clients["fake"] = client
	mgr.mu.Unlock()

	if err := mgr.RefreshTools(t.Context(), "fake"); err != nil {
		t.Fatalf("RefreshTools: %v", err)
	}
	tools := mgr.GetTools()
	if len(tools) != 1 {
		names := make([]string, 0, len(tools))
		for _, tool := range tools {
			names = append(names, tool.Name())
		}
		t.Fatalf("tools = %v, want exactly the whitelisted echo", names)
	}
}

// A PAUSED server must not be resurrected by the health-check reconnect loop —
// the same class as the /mcp remove resurrection guard.
func TestTryReconnectUnhealthy_SkipsPausedServer(t *testing.T) {
	mgr := NewManager([]*ServerConfig{{
		Name: "sleepy", Transport: "stdio", Paused: true,
	}})
	mgr.mu.Lock()
	mgr.health["sleepy"] = &ServerHealth{Healthy: false}
	mgr.mu.Unlock()

	mgr.tryReconnectUnhealthy(context.Background())

	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	if _, connected := mgr.clients["sleepy"]; connected {
		t.Fatal("paused server was resurrected by the health loop")
	}
	if attempts := mgr.health["sleepy"].ReconnectAttempts; attempts != 0 {
		t.Fatalf("paused server consumed %d reconnect attempts, want 0", attempts)
	}
}
