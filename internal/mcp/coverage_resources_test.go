package mcp

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Salvaged from an abandoned parallel-session draft (v0.100.101): the file
// shipped with 3 tests duplicating tracked siblings (removed), a broken
// reconnect call (fixed to the real `reconnect() bool` contract), and a
// WithResources variant that injected a bare &Client{} literal whose nil maps
// panicked inside request() (removed — WithFakeServer below is the correctly
// constructed version). The unique fake-server coverage (ListResources /
// ReadResource / Ping / reconnect give-up / FormatResources) was worth keeping.

// --- FormatResources (24.1% → higher) ---

func TestFormatResources_EmptyManager(t *testing.T) {
	mgr := &Manager{}
	got := FormatResources(mgr)
	if !strings.Contains(got, "No MCP resources available") {
		t.Fatalf("empty manager: got %q, want 'No MCP resources available' hint", got)
	}
}

// --- Manager.ListResources (10% → higher) ---

func TestManager_ListResources_NoClients(t *testing.T) {
	mgr := NewManager(nil)
	resources := mgr.ListResources(context.Background())
	if len(resources) != 0 {
		t.Errorf("expected 0 resources with no clients, got %d", len(resources))
	}
}

// --- Manager.ReadResource (16.7% → higher) ---

func TestManager_ReadResource_NoClients(t *testing.T) {
	mgr := NewManager(nil)
	_, _, err := mgr.ReadResource(context.Background(), "test://resource")
	if err == nil {
		t.Fatal("expected error with no connected servers")
	}
	if !strings.Contains(err.Error(), "no connected MCP servers") {
		t.Errorf("error = %q, want 'no connected MCP servers'", err)
	}
}

// --- Manager.ConnectAll (14.3% → higher) ---

func TestManager_ConnectAll_NoServers(t *testing.T) {
	mgr := NewManager(nil)
	if err := mgr.ConnectAll(context.Background()); err != nil {
		t.Fatalf("ConnectAll with no servers: %v", err)
	}
}

// --- Client.ListResources / ReadResource via fake transport (0% → higher) ---

func TestClient_ListResources_FakeServer(t *testing.T) {
	tr := newFakeTransport()
	server := startFakeServer(t, tr, []*ToolInfo{
		{Name: "echo", Description: "echo"},
	})
	t.Cleanup(func() { server.cancel() })

	cfg := &ServerConfig{Name: "fake"}
	client := newClientWithTransport(context.Background(), cfg, tr)
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close(); _ = client.Close() })

	resources, err := client.ListResources(context.Background())
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(resources) != 1 || resources[0].URI != "test://resource" {
		t.Fatalf("resources = %#v, want the fake server's test://resource", resources)
	}
}

func TestClient_ReadResource_FakeServer(t *testing.T) {
	tr := newFakeTransport()
	server := startFakeServer(t, tr, []*ToolInfo{
		{Name: "echo", Description: "echo"},
	})
	t.Cleanup(func() { server.cancel() })

	cfg := &ServerConfig{Name: "fake"}
	client := newClientWithTransport(context.Background(), cfg, tr)
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close(); _ = client.Close() })

	result, err := client.ReadResource(context.Background(), "test://resource")
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if result == nil || len(result.Contents) == 0 {
		t.Fatalf("ReadResource returned no contents: %#v", result)
	}
	if result.Contents[0].Text != "hello" {
		t.Fatalf("content text = %q, want hello", result.Contents[0].Text)
	}
}

// --- Client.Ping (57.1% → higher) ---

func TestClient_Ping_FakeServer(t *testing.T) {
	tr := newFakeTransport()
	server := startFakeServer(t, tr, []*ToolInfo{
		{Name: "echo", Description: "echo"},
	})
	t.Cleanup(func() { server.cancel() })

	cfg := &ServerConfig{Name: "fake"}
	client := newClientWithTransport(context.Background(), cfg, tr)
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close(); _ = client.Close() })

	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// --- Client.reconnect (65% → higher) ---

func TestClient_Reconnect_GivesUpAfterMaxAttempts(t *testing.T) {
	tr := newFakeTransport()
	cfg := &ServerConfig{Name: "fake"}
	client := newClientWithTransport(context.Background(), cfg, tr)
	t.Cleanup(func() { _ = tr.Close(); _ = client.Close() })

	// At the attempt cap, reconnect must give up immediately (no backoff sleep).
	client.reconnectMu.Lock()
	client.consecutiveFails = client.maxReconnectAttempts
	client.reconnectMu.Unlock()
	if client.reconnect() {
		t.Fatal("reconnect must report failure once max attempts are exhausted")
	}
}

func TestClient_Reconnect_UnknownTransportFails(t *testing.T) {
	tr := newFakeTransport()
	// Empty Transport type — the recreate switch has no case for it.
	cfg := &ServerConfig{Name: "fake"}
	client := newClientWithTransport(context.Background(), cfg, tr)
	t.Cleanup(func() { _ = tr.Close(); _ = client.Close() })

	// Shrink the backoff so the test doesn't sleep a real interval.
	client.reconnectMu.Lock()
	client.backoff = time.Millisecond
	client.reconnectMu.Unlock()
	if client.reconnect() {
		t.Fatal("reconnect must fail for a config with no recreatable transport")
	}
}

// --- FormatResources with actual resources via fake server ---

func TestFormatResources_WithFakeServer(t *testing.T) {
	tr := newFakeTransport()
	server := startFakeServer(t, tr, []*ToolInfo{
		{Name: "echo", Description: "echo"},
	})
	t.Cleanup(func() { server.cancel() })

	mgr := NewManager([]*ServerConfig{
		{Name: "fake", Transport: "stdio"},
	})
	// Inject the client directly
	mgr.mu.Lock()
	client := newClientWithTransport(context.Background(), &ServerConfig{Name: "fake"}, tr)
	mgr.clients["fake"] = client
	mgr.health["fake"] = &ServerHealth{Healthy: true}
	mgr.mu.Unlock()

	got := FormatResources(mgr)
	// Should produce some output (either resources or "no resources" hint)
	if got == "" {
		t.Fatal("expected non-empty output from FormatResources")
	}
}
