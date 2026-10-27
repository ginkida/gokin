package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gokin/internal/client"
	"gokin/internal/testkit"
)

func TestHeadlessDegenerateOutputFailsAfterOneRetry(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		data, _ := json.Marshal(map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": strings.Repeat("Let me reconsider this again. ", 2000)}})
		fmt.Fprintf(w, "data: %s\n\ndata: {\"type\":\"message_stop\"}\n\n", data)
	}))
	defer server.Close()
	c, err := client.NewAnthropicClient(client.AnthropicConfig{Model: "glm-5", Provider: "glm", APIKey: "test", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	application, executor := newHeadlessPolicyTestApp(t, testkit.NewMockClient(), &appHeadlessScriptedTool{name: "read"})
	application.config.Model.Provider, application.config.Model.Name = "glm", "glm-5"
	application.client = c
	executor.SetClient(c)
	var stdout bytes.Buffer
	result, err := application.RunHeadlessWithOptions(t.Context(), "inspect", HeadlessOptions{Stdout: &stdout, Stderr: io.Discard, OutputFormat: HeadlessOutputJSON, NoSelfReview: true})
	if err == nil || result.Status != "error" || result.Error == nil || result.Error.Kind != "degenerate_output" {
		t.Fatalf("result=%+v err=%v output=%s", result, err, stdout.String())
	}
	if requests.Load() != 2 {
		t.Fatalf("requests=%d; want exactly one retry", requests.Load())
	}
	if len(application.session.GetPendingRecoveries()) != 0 {
		t.Fatal("second trip scheduled another automatic recovery")
	}
}
