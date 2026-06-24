package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"google.golang.org/genai"
)

// The streaming consumer goroutine must release its scanner goroutine on EVERY
// exit — including the normal completion path (a `[DONE]` / message_stop while
// the scanner is still mid-read). Before the `defer close(stopScan)` fix, the
// scanner blocked forever on its unbuffered send (consumer gone, ctx live),
// leaking one goroutine + its up-to-8MB buffer PER RESPONSE. This test drives
// real streamed requests through an httptest SSE server and asserts the
// goroutine count does not grow per response — WITHOUT cancelling ctx (the fix
// must release the scanner on consumer return, not rely on ctx).
func TestStreaming_NoScannerGoroutineLeakPerResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		// One content delta, then [DONE] — the consumer returns on [DONE] while
		// the scanner is still reading, reproducing the leak shape. Then the
		// handler returns, closing the body.
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	c := &AnthropicClient{
		config:     AnthropicConfig{Model: "glm-5", BaseURL: srv.URL, APIKey: "test", Provider: "glm"},
		httpClient: &http.Client{},
	}

	runOnce := func() {
		resp, err := c.SendMessageWithHistory(context.Background(),
			[]*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}, "")
		if err != nil {
			t.Fatalf("SendMessageWithHistory: %v", err)
		}
		for range resp.Chunks { // drain to completion (consumer goroutine returns)
		}
	}

	// settle waits for the goroutine count to quiesce after draining.
	settle := func() int {
		var n int
		for i := 0; i < 50; i++ {
			runtime.GC()
			time.Sleep(10 * time.Millisecond)
			n = runtime.NumGoroutine()
			if i > 3 && n == runtime.NumGoroutine() {
				break
			}
		}
		return n
	}

	// Warm up (HTTP transport, etc.), then take a baseline.
	runOnce()
	runOnce()
	base := settle()

	const n = 8
	for i := 0; i < n; i++ {
		runOnce()
	}
	after := settle()

	// Pre-fix: ~n leaked scanner goroutines (after ≈ base+n). Post-fix: stable.
	if after > base+2 {
		t.Fatalf("scanner goroutine leak: baseline=%d after %d responses=%d (grew by %d, want ~0)",
			base, n, after, after-base)
	}
}
