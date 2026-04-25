package mcp

import (
	"context"
	"testing"
	"time"
)

// TestClient_Initialize_NotificationBeforeResponse_NoDeadlock guards a
// real deadlock: Initialize used to hold c.mu.Lock() (write) for the
// entire duration of the network round-trip via request(). The
// receiveLoop calls handleMessage on each incoming message, and
// handleMessage's notification path takes c.mu.RLock() to read the
// installed notification handler.
//
// The deadlock chain when the server sent a notification before the
// InitializeResult:
//   1. Initialize holds c.mu.Lock(), waits on respCh inside request().
//   2. receiveLoop reads the notification, enters handleMessage.
//   3. handleMessage tries c.mu.RLock() — blocked behind Initialize's
//      Lock.
//   4. receiveLoop is the sole reader; it can't process the next
//      message, which is the InitializeResult Initialize is waiting
//      for. Initialize times out at 30s.
//
// The MCP spec doesn't forbid server-initiated notifications during
// init. Real servers that emit logs/messages or capability-change
// notifications would intermittently lock up clients.
//
// Fix: Initialize must not hold c.mu across the network call. After
// the fix, this test completes in well under 1s.
func TestClient_Initialize_NotificationBeforeResponse_NoDeadlock(t *testing.T) {
	tr := newFakeTransport()
	server := startFakeServer(t, tr, []*ToolInfo{
		{Name: "echo", Description: "echo"},
	})
	t.Cleanup(func() { server.cancel() })

	cfg := &ServerConfig{Name: "fake"}
	client := newClientWithTransport(t.Context(), cfg, tr)
	t.Cleanup(func() {
		_ = tr.Close()
		_ = client.Close()
	})

	// Install a notification handler so handleMessage takes the
	// full notification path (not strictly required for the
	// deadlock — RLock is taken even with no handler — but mirrors
	// real-world configurations).
	client.SetNotificationHandler(func(method string, params any) {})

	// Hook the server: when the initialize request arrives, feed a
	// notification BEFORE the server's normal handler sends the
	// response. The receiveLoop will pop the notification first and
	// must NOT be blocked by Initialize's lock.
	server.SetHandleHook(func(method string) {
		if method == MethodInitialize {
			server.transport.feed(&JSONRPCMessage{
				JSONRPC: "2.0",
				Method:  "notifications/tools/list_changed",
			})
		}
	})

	// Bound the test: with the bug present, Initialize would block
	// for the full 30s request timeout. 2s is comfortably more than
	// enough for a healthy in-memory exchange.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- client.Initialize(ctx)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Initialize returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Initialize deadlocked: notification before response held c.mu.Lock against receiveLoop's c.mu.RLock")
	}
}
