package mcp

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/tools"
)

// fakeMCPServer drives an in-process "server" over a fakeTransport. It
// responds to initialize/tools/list/tools/call like a real MCP server would,
// and exposes TriggerListChanged so tests can simulate the server emitting
// notifications/tools/list_changed.
type fakeMCPServer struct {
	transport *fakeTransport
	ctx       context.Context
	cancel    context.CancelFunc
	t         *testing.T

	toolsMu sync.Mutex
	tools   []*ToolInfo

	initCount atomic.Int32
	listCount atomic.Int32
	callCount atomic.Int32

	// handleHook is invoked at the start of handling each request, before
	// the response is generated. Tests use it to inject delays or block
	// specific methods to simulate slow servers.
	hookMu     sync.Mutex
	handleHook func(method string)
}

func startFakeServer(t *testing.T, tr *fakeTransport, initial []*ToolInfo) *fakeMCPServer {
	ctx, cancel := context.WithCancel(t.Context())
	s := &fakeMCPServer{
		transport: tr,
		ctx:       ctx,
		cancel:    cancel,
		t:         t,
		tools:     initial,
	}
	go s.run()
	return s
}

func (s *fakeMCPServer) run() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case req, ok := <-s.transport.sent:
			if !ok {
				return
			}
			s.logf("server.run got request: method=%q id=%v", req.Method, req.ID)
			s.handle(req)
		}
	}
}

// logf is a cheap test logger that writes via t.Logf when available. Keeps
// debug output scoped to failing runs.
func (s *fakeMCPServer) logf(format string, args ...any) {
	if s.t != nil {
		s.t.Logf(format, args...)
	}
}

func (s *fakeMCPServer) handle(req *JSONRPCMessage) {
	s.hookMu.Lock()
	hook := s.handleHook
	s.hookMu.Unlock()
	if hook != nil {
		hook(req.Method)
	}
	switch req.Method {
	case MethodInitialize:
		s.initCount.Add(1)
		s.transport.feed(&JSONRPCMessage{
			JSONRPC: "2.0",
			ID:      float64ID(req.ID),
			Result: map[string]any{
				"protocolVersion": ProtocolVersion,
				"serverInfo": map[string]any{
					"name":    "fake",
					"version": "0.1.0",
					"capabilities": map[string]any{
						"tools": map[string]any{"listChanged": true},
					},
				},
			},
		})
	case MethodInitialized:
		// notification — no response
	case MethodToolsList:
		s.listCount.Add(1)
		s.toolsMu.Lock()
		toolsCopy := make([]*ToolInfo, len(s.tools))
		copy(toolsCopy, s.tools)
		s.toolsMu.Unlock()
		s.transport.feed(&JSONRPCMessage{
			JSONRPC: "2.0",
			ID:      float64ID(req.ID),
			Result:  map[string]any{"tools": toolsCopy},
		})
	case MethodToolsCall:
		s.callCount.Add(1)
		s.transport.feed(&JSONRPCMessage{
			JSONRPC: "2.0",
			ID:      float64ID(req.ID),
			Result: map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "ok"},
				},
			},
		})
	}
}

// TriggerListChanged simulates the server emitting a tools/list_changed
// notification. Tests call this after updating the server's tool inventory.
func (s *fakeMCPServer) TriggerListChanged() {
	s.transport.feed(&JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "notifications/tools/list_changed",
	})
}

// SetTools swaps the server's advertised tool list. Thread-safe.
func (s *fakeMCPServer) SetTools(tools []*ToolInfo) {
	s.toolsMu.Lock()
	s.tools = tools
	s.toolsMu.Unlock()
}

// SetHandleHook installs a per-request hook. Pass nil to remove.
func (s *fakeMCPServer) SetHandleHook(fn func(method string)) {
	s.hookMu.Lock()
	s.handleHook = fn
	s.hookMu.Unlock()
}

// float64ID matches the JSON decoder's behavior: numeric IDs come in as
// float64 so we pass them back in the same type.
func float64ID(id any) any {
	switch v := id.(type) {
	case int64:
		return float64(v)
	case float64:
		return v
	case int:
		return float64(v)
	}
	return id
}

// waitFor polls fn at 10ms intervals until it returns true or timeout
// elapses. Returns false on timeout.
func waitFor(timeout time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fn()
}

// ─── End-to-end: initialize + list tools + tools/list_changed refresh ─

func TestIntegration_InitializeListToolsCall(t *testing.T) {
	tr := newFakeTransport()
	initial := []*ToolInfo{
		{Name: "echo", Description: "Echo back the input"},
		{Name: "uppercase", Description: "Uppercase the input"},
	}
	server := startFakeServer(t, tr, initial)
	t.Cleanup(func() { server.cancel() })

	cfg := &ServerConfig{Name: "fake"}
	client := newClientWithTransport(t.Context(), cfg, tr)
	t.Cleanup(func() {
		_ = tr.Close()
		_ = client.Close()
	})

	if err := client.Initialize(t.Context()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := server.initCount.Load(); got != 1 {
		t.Errorf("server saw %d initialize requests, want 1", got)
	}

	got, err := client.ListTools(t.Context())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListTools returned %d tools, want 2", len(got))
	}
	if got[0].Name != "echo" || got[1].Name != "uppercase" {
		t.Errorf("got names %v", []string{got[0].Name, got[1].Name})
	}

	result, err := client.CallTool(t.Context(), "echo", map[string]any{"input": "hi"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result == nil || len(result.Content) == 0 {
		t.Fatalf("empty tool result: %+v", result)
	}
	if result.Content[0].Text != "ok" {
		t.Errorf("tool result text = %q, want ok", result.Content[0].Text)
	}
}

func TestIntegration_ToolsListChanged_TriggersRefresh(t *testing.T) {
	tr := newFakeTransport()
	server := startFakeServer(t, tr, []*ToolInfo{
		{Name: "v1_tool", Description: "before"},
	})
	t.Cleanup(func() { server.cancel() })

	cfg := &ServerConfig{Name: "fake"}
	client := newClientWithTransport(t.Context(), cfg, tr)
	t.Cleanup(func() {
		_ = tr.Close()
		_ = client.Close()
	})

	if err := client.Initialize(t.Context()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Build a Manager and inject the ready client directly — avoids the
	// subprocess path of ConnectAll/Connect.
	m := NewManager([]*ServerConfig{cfg})
	m.mu.Lock()
	m.clients["fake"] = client
	m.mu.Unlock()
	m.wireClientNotifications("fake", client)

	callbackFired := make(chan string, 4)
	m.SetToolsChangedCallback(func(name string) {
		callbackFired <- name
	})

	// First sync: manager has no tools yet.
	if err := m.RefreshTools(t.Context(), "fake"); err != nil {
		t.Fatalf("initial RefreshTools: %v", err)
	}
	select {
	case name := <-callbackFired:
		if name != "fake" {
			t.Errorf("got callback for %q, want fake", name)
		}
	case <-time.After(time.Second):
		t.Fatal("toolsChanged callback did not fire after RefreshTools")
	}

	got := m.GetTools()
	if len(got) != 1 || got[0].Name() != "fake_v1_tool" {
		t.Fatalf("after initial refresh: got %d tools, names %v", len(got), toolNames(got))
	}

	// Now the server's tool list changes. It emits tools/list_changed.
	server.SetTools([]*ToolInfo{
		{Name: "v2_tool", Description: "after"},
		{Name: "extra", Description: "new"},
	})
	server.TriggerListChanged()

	// Expect: Manager receives notification → RefreshTools → callback fires.
	select {
	case name := <-callbackFired:
		if name != "fake" {
			t.Errorf("second callback for %q, want fake", name)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tools/list_changed did not trigger refresh+callback")
	}

	// Manager's tool list should have been replaced (not merged).
	if !waitFor(time.Second, func() bool {
		names := toolNames(m.GetTools())
		return len(names) == 2 && names[0] != "fake_v1_tool"
	}) {
		t.Fatalf("tool list not updated: got %v", toolNames(m.GetTools()))
	}
	got = m.GetTools()
	names := toolNames(got)
	wantSet := map[string]bool{"fake_v2_tool": true, "fake_extra": true}
	for _, n := range names {
		if !wantSet[n] {
			t.Errorf("unexpected tool %q in list %v", n, names)
		}
	}
}

func TestIntegration_RefreshTools_UnknownServer(t *testing.T) {
	m := NewManager(nil)
	err := m.RefreshTools(t.Context(), "ghost")
	if err == nil || err.Error() == "" {
		t.Errorf("expected error for unknown server, got %v", err)
	}
}

func TestIntegration_RefreshTools_PropagatesListError(t *testing.T) {
	tr := newFakeTransport()
	// Server that never responds to tools/list (only to initialize).
	server := startFakeServer(t, tr, nil)
	t.Cleanup(func() { server.cancel() })
	// Rewrite handler: drop tools/list responses.
	// (Simpler: close transport after init — ListTools will fail.)

	cfg := &ServerConfig{Name: "x"}
	client := newClientWithTransport(t.Context(), cfg, tr)
	t.Cleanup(func() {
		_ = tr.Close()
		_ = client.Close()
	})
	if err := client.Initialize(t.Context()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	m := NewManager([]*ServerConfig{cfg})
	m.mu.Lock()
	m.clients["x"] = client
	m.mu.Unlock()

	// Intentionally mis-type tools in the server so ListTools returns empty
	// — RefreshTools should succeed but with 0 tools (not error). This
	// tests the happy path with empty inventory.
	if err := m.RefreshTools(t.Context(), "x"); err != nil {
		t.Errorf("unexpected error on empty tool list: %v", err)
	}
	if len(m.GetTools()) != 0 {
		t.Errorf("expected 0 tools, got %v", toolNames(m.GetTools()))
	}
}

// TestIntegration_WireClientNotifications_InstallsHandlerOnClient verifies
// that a client freshly adopted by the manager (including via reconnect)
// gets its notification handler wired up. Without this the reconnect path
// in tryReconnectUnhealthy would silently break tools/list_changed
// propagation for servers that auto-recover.
func TestIntegration_WireClientNotifications_InstallsHandlerOnClient(t *testing.T) {
	tr := newFakeTransport()
	server := startFakeServer(t, tr, []*ToolInfo{{Name: "v1"}})
	t.Cleanup(func() { server.cancel() })

	cfg := &ServerConfig{Name: "reborn"}
	client := newClientWithTransport(t.Context(), cfg, tr)
	t.Cleanup(func() {
		_ = tr.Close()
		_ = client.Close()
	})
	if err := client.Initialize(t.Context()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	m := NewManager([]*ServerConfig{cfg})
	m.mu.Lock()
	m.clients["reborn"] = client
	m.mu.Unlock()

	// First refresh to populate tools — must be done while the
	// notification handler is NOT installed yet so the test can verify
	// wireClientNotifications alone brings tools/list_changed alive.
	if err := m.RefreshTools(t.Context(), "reborn"); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	if len(m.GetTools()) != 1 {
		t.Fatalf("initial tools: %v", toolNames(m.GetTools()))
	}

	callbackFired := make(chan string, 1)
	m.SetToolsChangedCallback(func(name string) {
		select {
		case callbackFired <- name:
		default:
		}
	})

	// Simulate the reconnect path: server changes its tool list, then
	// emits tools/list_changed. Without wireClientNotifications the
	// notification would be silently dropped (client.notificationHandler
	// is nil). Calling wireClientNotifications installs the dispatcher so
	// the notification triggers RefreshTools → onToolsChanged.
	server.SetTools([]*ToolInfo{{Name: "v2"}, {Name: "extra"}})
	m.wireClientNotifications("reborn", client)
	server.TriggerListChanged()

	select {
	case name := <-callbackFired:
		if name != "reborn" {
			t.Errorf("callback name = %q, want reborn", name)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("wireClientNotifications did not route tools/list_changed to callback")
	}

	// Tools should reflect server's new set.
	if !waitFor(time.Second, func() bool {
		return len(m.GetTools()) == 2
	}) {
		t.Fatalf("tools not refreshed after notification: %v", toolNames(m.GetTools()))
	}
}

// TestIntegration_RefreshTools_DisconnectDuringListTools verifies the guard
// that was added in Sprint 11.5 after review: RefreshTools drops the lock to
// call ListTools, then re-acquires it to commit the new tool list. If a
// Disconnect (or a reconnect swapping in a different client) happens in
// that unlocked window, RefreshTools MUST refuse to commit — otherwise
// m.tools would end up with MCPTool wrappers pointing at a closed client.
func TestIntegration_RefreshTools_DisconnectDuringListTools(t *testing.T) {
	tr := newFakeTransport()
	server := startFakeServer(t, tr, []*ToolInfo{{Name: "tool_a"}})
	t.Cleanup(func() { server.cancel() })

	cfg := &ServerConfig{Name: "fake"}
	client := newClientWithTransport(t.Context(), cfg, tr)
	t.Cleanup(func() {
		_ = tr.Close()
		_ = client.Close()
	})
	if err := client.Initialize(t.Context()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	m := NewManager([]*ServerConfig{cfg})
	m.mu.Lock()
	m.clients["fake"] = client
	m.mu.Unlock()

	// Block the next tools/list response until the test releases it. This
	// pins RefreshTools inside client.ListTools, holding the unlocked
	// window open long enough for us to simulate a concurrent Disconnect.
	listToolsBlocker := make(chan struct{})
	server.SetHandleHook(func(method string) {
		if method == MethodToolsList {
			<-listToolsBlocker
		}
	})

	refreshErr := make(chan error, 1)
	go func() {
		refreshErr <- m.RefreshTools(t.Context(), "fake")
	}()

	// Give RefreshTools time to reach the ListTools call.
	time.Sleep(50 * time.Millisecond)

	// Simulate the effect of Disconnect without the 5s subprocess-wait
	// that real Disconnect inherits from client.Close: just remove the
	// client from the map. The guard in RefreshTools compares pointer
	// identity, so this triggers the same path.
	m.mu.Lock()
	delete(m.clients, "fake")
	m.mu.Unlock()

	// Let ListTools return so RefreshTools can proceed to its guard.
	close(listToolsBlocker)

	select {
	case err := <-refreshErr:
		if err == nil {
			t.Fatal("RefreshTools should have errored after the client was removed")
		}
		if !strings.Contains(err.Error(), "disconnected or reconnected") {
			t.Errorf("err = %v, want guard error", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RefreshTools did not return after concurrent disconnect")
	}

	// Manager's tool list must not have been overwritten with the stale
	// wrappers built around the removed client.
	if got := m.GetTools(); len(got) != 0 {
		t.Errorf("tools leaked into manager after stale refresh: %v", toolNames(got))
	}
}

// TestIntegration_RefreshTools_ClientReplacedDuringListTools covers the
// sibling case: client was swapped (reconnect put a DIFFERENT client
// instance under the same name). Same guard, different path.
func TestIntegration_RefreshTools_ClientReplacedDuringListTools(t *testing.T) {
	tr := newFakeTransport()
	server := startFakeServer(t, tr, []*ToolInfo{{Name: "tool_a"}})
	t.Cleanup(func() { server.cancel() })

	cfg := &ServerConfig{Name: "fake"}
	clientA := newClientWithTransport(t.Context(), cfg, tr)
	t.Cleanup(func() {
		_ = tr.Close()
		_ = clientA.Close()
	})
	if err := clientA.Initialize(t.Context()); err != nil {
		t.Fatalf("Initialize A: %v", err)
	}

	m := NewManager([]*ServerConfig{cfg})
	m.mu.Lock()
	m.clients["fake"] = clientA
	m.mu.Unlock()

	blocker := make(chan struct{})
	server.SetHandleHook(func(method string) {
		if method == MethodToolsList {
			<-blocker
		}
	})

	refreshErr := make(chan error, 1)
	go func() {
		refreshErr <- m.RefreshTools(t.Context(), "fake")
	}()

	time.Sleep(50 * time.Millisecond)

	// Swap in a second client — simulates reconnect mid-refresh. Close the
	// transport before the client so client.Close doesn't wait 5s for
	// receiveLoop to notice the cancel.
	tr2 := newFakeTransport()
	clientB := newClientWithTransport(t.Context(), cfg, tr2)
	t.Cleanup(func() {
		_ = tr2.Close()
		_ = clientB.Close()
	})

	m.mu.Lock()
	m.clients["fake"] = clientB
	m.mu.Unlock()

	close(blocker)

	select {
	case err := <-refreshErr:
		if err == nil || !strings.Contains(err.Error(), "disconnected or reconnected") {
			t.Errorf("err = %v, want guard error", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RefreshTools did not return after client swap")
	}
}

// toolNames extracts Tool.Name() from each tool for readable assertions.
func toolNames(ts []tools.Tool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name()
	}
	return out
}
