package mcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type rejectingReader struct {
	err error
}

func (r rejectingReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadLimitedHTTPBodyRejectsDeclaredOversizeWithoutReading(t *testing.T) {
	readErr := errors.New("reader must not be touched")
	_, err := readLimitedHTTPBody(rejectingReader{err: readErr}, 33, 32)
	if err == nil || !strings.Contains(err.Error(), "exceeds 32-byte limit") {
		t.Fatalf("error = %v, want declared-size rejection", err)
	}
	if errors.Is(err, readErr) {
		t.Fatalf("oversized declared body was read: %v", err)
	}
}

func TestReadLimitedHTTPBodyEnforcesStreamingLimit(t *testing.T) {
	data, err := readLimitedHTTPBody(strings.NewReader(strings.Repeat("x", 32)), -1, 32)
	if err != nil {
		t.Fatalf("exact-limit body: %v", err)
	}
	if len(data) != 32 {
		t.Fatalf("body length = %d, want 32", len(data))
	}

	_, err = readLimitedHTTPBody(strings.NewReader(strings.Repeat("x", 33)), -1, 32)
	if err == nil || !strings.Contains(err.Error(), "exceeds 32-byte limit") {
		t.Fatalf("error = %v, want streaming-size rejection", err)
	}
}

func TestReadLimitedHTTPBodyPreservesReadFailure(t *testing.T) {
	readErr := errors.New("broken response stream")
	_, err := readLimitedHTTPBody(rejectingReader{err: readErr}, -1, 32)
	if !errors.Is(err, readErr) {
		t.Fatalf("error = %v, want wrapped read failure", err)
	}

	if _, err := readLimitedHTTPBody(strings.NewReader("x"), -1, 0); err == nil {
		t.Fatal("non-positive response limit was accepted")
	}
}

func TestHTTPTransportRejectsOversizedDeclaredResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.FormatInt(maxHTTPResponseBodyBytes+1, 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport, err := NewHTTPTransport(context.Background(), server.URL, nil, time.Second)
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}
	defer transport.Close()

	err = transport.Send(&JSONRPCMessage{ID: 1, Method: "tools/list"})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Send error = %v, want response-size rejection", err)
	}
}

func TestHTTPTransportSnapshotsHeaders(t *testing.T) {
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer server.Close()

	headers := map[string]string{"Authorization": "Bearer original"}
	transport, err := NewHTTPTransport(context.Background(), server.URL, headers, time.Second)
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}
	defer transport.Close()
	headers["Authorization"] = "Bearer mutated"

	if err := transport.Send(&JSONRPCMessage{ID: 1, Method: "tools/list"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := <-received; got != "Bearer original" {
		t.Fatalf("Authorization = %q, want constructor snapshot", got)
	}
}

func TestHTTPTransportRejectsOversizedSSEBatchWithoutPartialDelivery(t *testing.T) {
	var body strings.Builder
	for i := 0; i < maxHTTPQueuedMessages+1; i++ {
		body.WriteString("data: {\"jsonrpc\":\"2.0\",\"method\":\"notify\"}\n\n")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body.String())
	}))
	defer server.Close()

	transport, err := NewHTTPTransport(context.Background(), server.URL, nil, time.Second)
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}
	defer transport.Close()

	done := make(chan error, 1)
	go func() {
		done <- transport.Send(&JSONRPCMessage{ID: 1, Method: "tools/list"})
	}()
	select {
	case err = <-done:
		if err == nil || !strings.Contains(err.Error(), "queue capacity exceeded") {
			t.Fatalf("Send error = %v, want queue-capacity rejection", err)
		}
	case <-time.After(2 * time.Second):
		_ = transport.Close()
		t.Fatal("oversized SSE batch blocked Send")
	}
	if got := len(transport.recvChan); got != 0 {
		t.Fatalf("oversized SSE batch partially dispatched %d messages", got)
	}
}

func TestHTTPTransportQueuesMaximumSSEBatch(t *testing.T) {
	var body strings.Builder
	for i := 0; i < maxHTTPQueuedMessages; i++ {
		body.WriteString("data: {\"jsonrpc\":\"2.0\",\"method\":\"notify\"}\n\n")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body.String())
	}))
	defer server.Close()

	transport, err := NewHTTPTransport(context.Background(), server.URL, nil, time.Second)
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}
	defer transport.Close()

	if err := transport.Send(&JSONRPCMessage{ID: 1, Method: "tools/list"}); err != nil {
		t.Fatalf("Send exact-capacity batch: %v", err)
	}
	if got := len(transport.recvChan); got != maxHTTPQueuedMessages {
		t.Fatalf("queued messages = %d, want %d", got, maxHTTPQueuedMessages)
	}
}

func TestHTTPTransportDoesNotCommitSessionFromFailedResponse(t *testing.T) {
	requests := make(chan string, 2)
	call := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		requests <- r.Header.Get("Mcp-Session-Id")
		if call == 1 {
			w.Header().Set("Mcp-Session-Id", "poisoned-session")
			http.Error(w, "initialize failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":2,"result":{}}`)
	}))
	defer server.Close()

	transport, err := NewHTTPTransport(context.Background(), server.URL, nil, time.Second)
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}
	defer transport.Close()

	if err := transport.Send(&JSONRPCMessage{ID: 1, Method: "initialize"}); err == nil {
		t.Fatal("failed initialize response was accepted")
	}
	if err := transport.Send(&JSONRPCMessage{ID: 2, Method: "initialize"}); err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if first, second := <-requests, <-requests; first != "" || second != "" {
		t.Fatalf("session headers = %q, %q; failed response poisoned transport", first, second)
	}
}
