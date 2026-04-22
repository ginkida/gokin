package mcp

import (
	"io"
	"sync"
	"testing"
	"time"
)

// fakeTransport is an in-memory Transport for tests. Send/Receive use
// buffered channels so an integration-test server can drive the exchange.
// Close signals the receive loop to exit with io.EOF.
type fakeTransport struct {
	sent     chan *JSONRPCMessage // client → server
	incoming chan *JSONRPCMessage // server → client
	closeMu  sync.Mutex
	closed   bool
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{
		sent:     make(chan *JSONRPCMessage, 32),
		incoming: make(chan *JSONRPCMessage, 32),
	}
}

func (t *fakeTransport) Send(msg *JSONRPCMessage) error {
	t.closeMu.Lock()
	if t.closed {
		t.closeMu.Unlock()
		return io.ErrClosedPipe
	}
	t.closeMu.Unlock()
	// Blocking send — server may take a moment to drain. 32-slot buffer
	// should handle realistic bursts without ever reaching this path.
	t.sent <- msg
	return nil
}

func (t *fakeTransport) Receive() (*JSONRPCMessage, error) {
	msg, ok := <-t.incoming
	if !ok {
		return nil, io.EOF
	}
	return msg, nil
}

func (t *fakeTransport) Close() error {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	if !t.closed {
		t.closed = true
		close(t.incoming)
		close(t.sent)
	}
	return nil
}

// feed enqueues a message for the client to receive. Safe to call from any
// goroutine; returns false if the transport is already closed.
func (t *fakeTransport) feed(msg *JSONRPCMessage) bool {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	if t.closed {
		return false
	}
	t.incoming <- msg
	return true
}

// ─── SetNotificationHandler fires on incoming notification ──────────────

func TestClient_NotificationHandler_FiresOnIncomingNotification(t *testing.T) {
	tr := newFakeTransport()
	client := newClientWithTransport(t.Context(), &ServerConfig{Name: "test"}, tr)
	// Close transport first — that unblocks Receive so client.Close() doesn't
	// spend 5s timing out on the receiveLoop.
	t.Cleanup(func() {
		_ = tr.Close()
		_ = client.Close()
	})

	gotMethod := make(chan string, 1)
	client.SetNotificationHandler(func(method string, params any) {
		gotMethod <- method
	})

	tr.feed(&JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "notifications/tools/list_changed",
		// ID is nil → IsNotification() returns true.
	})

	select {
	case m := <-gotMethod:
		if m != "notifications/tools/list_changed" {
			t.Errorf("got method %q, want tools/list_changed", m)
		}
	case <-time.After(time.Second):
		t.Fatal("notification handler did not fire within 1s")
	}
}

func TestClient_NotificationHandler_NilHandlerNoPanic(t *testing.T) {
	tr := newFakeTransport()
	client := newClientWithTransport(t.Context(), &ServerConfig{Name: "test"}, tr)
	// Close transport first — that unblocks Receive so client.Close() doesn't
	// spend 5s timing out on the receiveLoop.
	t.Cleanup(func() {
		_ = tr.Close()
		_ = client.Close()
	})

	// No handler installed.
	tr.feed(&JSONRPCMessage{JSONRPC: "2.0", Method: "notifications/foo"})

	// Give it a moment to process — must not panic.
	time.Sleep(50 * time.Millisecond)
}

func TestClient_NotificationHandler_SlowHandlerDoesNotBlockReceive(t *testing.T) {
	tr := newFakeTransport()
	client := newClientWithTransport(t.Context(), &ServerConfig{Name: "test"}, tr)
	// Close transport first — that unblocks Receive so client.Close() doesn't
	// spend 5s timing out on the receiveLoop.
	t.Cleanup(func() {
		_ = tr.Close()
		_ = client.Close()
	})

	handlerFire := make(chan struct{}, 4)
	release := make(chan struct{})
	client.SetNotificationHandler(func(method string, params any) {
		handlerFire <- struct{}{}
		<-release // block until we signal
	})

	// Feed 4 notifications — receiveLoop should not be stalled by slow handler.
	for range 4 {
		tr.feed(&JSONRPCMessage{JSONRPC: "2.0", Method: "notifications/x"})
	}

	// All 4 handler invocations should fire (they run in separate goroutines).
	fired := 0
	timeout := time.After(time.Second)
	for fired < 4 {
		select {
		case <-handlerFire:
			fired++
		case <-timeout:
			t.Fatalf("only %d/4 handlers fired before timeout", fired)
		}
	}

	close(release)
}

func TestClient_Close_WaitsForInFlightNotificationHandlers(t *testing.T) {
	tr := newFakeTransport()
	client := newClientWithTransport(t.Context(), &ServerConfig{Name: "test"}, tr)

	// Handler blocks on a gate — we release it after triggering Close to
	// verify that Close waits for the handler before returning.
	handlerStarted := make(chan struct{})
	handlerFinishing := make(chan struct{})
	release := make(chan struct{})
	client.SetNotificationHandler(func(method string, params any) {
		close(handlerStarted)
		<-release
		close(handlerFinishing)
	})

	tr.feed(&JSONRPCMessage{JSONRPC: "2.0", Method: "notifications/x"})

	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("handler never started")
	}

	// Close the transport first so receiveLoop exits cleanly. Then initiate
	// Close in a goroutine — it must block until we release the handler.
	_ = tr.Close()

	closeDone := make(chan struct{})
	go func() {
		_ = client.Close()
		close(closeDone)
	}()

	select {
	case <-closeDone:
		t.Fatal("Close returned before handler finished")
	case <-time.After(100 * time.Millisecond):
		// Expected — Close is blocked on notifWg.
	}

	close(release)

	select {
	case <-handlerFinishing:
	case <-time.After(time.Second):
		t.Fatal("handler never finished")
	}
	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not complete after handler finished")
	}
}

func TestClient_Close_TimesOutOnHungHandler(t *testing.T) {
	tr := newFakeTransport()
	client := newClientWithTransport(t.Context(), &ServerConfig{Name: "test"}, tr)

	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })

	started := make(chan struct{})
	client.SetNotificationHandler(func(method string, params any) {
		close(started)
		<-stuck // never unblocks during the test
	})

	tr.feed(&JSONRPCMessage{JSONRPC: "2.0", Method: "notifications/x"})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler never started")
	}

	_ = tr.Close()

	// Close should timeout on notifWg (~2s) rather than hang forever.
	start := time.Now()
	done := make(chan struct{})
	go func() {
		_ = client.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked indefinitely on hung handler")
	}
	if elapsed := time.Since(start); elapsed < 1500*time.Millisecond {
		t.Errorf("Close returned too fast (%v) — did the notif-wait logic fire?", elapsed)
	}
}

func TestClient_NotificationHandler_PanicIsContained(t *testing.T) {
	tr := newFakeTransport()
	client := newClientWithTransport(t.Context(), &ServerConfig{Name: "test"}, tr)
	// Close transport first — that unblocks Receive so client.Close() doesn't
	// spend 5s timing out on the receiveLoop.
	t.Cleanup(func() {
		_ = tr.Close()
		_ = client.Close()
	})

	done := make(chan struct{}, 1)
	client.SetNotificationHandler(func(method string, params any) {
		defer func() { done <- struct{}{} }()
		panic("boom")
	})

	tr.feed(&JSONRPCMessage{JSONRPC: "2.0", Method: "notifications/x"})

	select {
	case <-done:
		// good — deferred cleanup ran even inside the panic recover.
	case <-time.After(time.Second):
		t.Fatal("panicking handler never ran deferred cleanup")
	}

	// receiveLoop should still be alive — feed a second notification.
	var fired atomicFlag
	client.SetNotificationHandler(func(method string, params any) {
		fired.set()
	})
	tr.feed(&JSONRPCMessage{JSONRPC: "2.0", Method: "notifications/y"})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if fired.get() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("receiveLoop dead after handler panic")
}

// ─── Manager-level: SetToolsChangedCallback + handleNotification ──────

func TestManager_WireClientNotifications_NilClientNoOp(t *testing.T) {
	m := NewManager(nil)
	// Must not panic.
	m.wireClientNotifications("x", nil)
}

func TestManager_HandleNotification_IgnoresUnknownMethods(t *testing.T) {
	m := NewManager(nil)
	fired := false
	m.SetToolsChangedCallback(func(name string) {
		fired = true
	})
	// Unknown method should silently do nothing — not try to refresh.
	m.handleNotification("ghost", "notifications/not_handled", nil)
	time.Sleep(50 * time.Millisecond)
	if fired {
		t.Error("toolsChanged callback fired for unknown method")
	}
}

// atomicFlag is a tiny one-shot flag used from goroutines — avoids pulling
// sync/atomic into the test file for just a single bool.
type atomicFlag struct {
	mu  sync.Mutex
	val bool
}

func (a *atomicFlag) set() {
	a.mu.Lock()
	a.val = true
	a.mu.Unlock()
}

func (a *atomicFlag) get() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.val
}
