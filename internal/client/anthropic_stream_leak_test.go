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

	// Warm up (HTTP transport, etc.), then take a baseline as the MINIMUM
	// observed over a settle window — the true quiesced level. (An inflated
	// baseline would only weaken the assertion.)
	runOnce()
	runOnce()
	base := runtime.NumGoroutine()
	for end := time.Now().Add(500 * time.Millisecond); time.Now().Before(end); {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
		if g := runtime.NumGoroutine(); g < base {
			base = g
		}
	}

	const n = 8
	for i := 0; i < n; i++ {
		runOnce()
	}

	// Poll until the count returns to the target or a generous deadline. A
	// GENUINE per-response leak is permanent (pre-fix, each scanner blocks
	// forever on its unbuffered send), so waiting cannot mask it — this only
	// absorbs goroutines still WINDING DOWN, whose exits are asynchronous
	// with response completion. The old single near-instant measurement
	// flaked on slower CI runners ("grew by 3") for exactly that reason.
	// Deadline is deliberately ~10× beyond anything observed locally (the
	// "-race stress tests need wall-clock guards ≥10× local time" lesson):
	// a full-suite -race run on a loaded machine once took >5s to wind
	// down 3 stragglers. Passing runs exit the moment the target is hit,
	// so only a genuinely-failing run pays the full wait.
	target := base + 2
	after := runtime.NumGoroutine()
	for end := time.Now().Add(15 * time.Second); after > target && time.Now().Before(end); {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
		after = runtime.NumGoroutine()
	}

	// Pre-fix: ~n leaked scanner goroutines (after ≈ base+n). Post-fix: stable.
	if after > target {
		t.Fatalf("scanner goroutine leak: baseline=%d after %d responses=%d (grew by %d, want ~0)",
			base, n, after, after-base)
	}
}
