package tools

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestExecuteStreamingToolClosedChannelRacePreservesError(t *testing.T) {
	exec := NewExecutor(NewRegistry(), nil, 0)
	for i := 0; i < 100; i++ {
		result, err := exec.executeStreamingTool(context.Background(), closedErrorStreamingTool{}, nil)
		if err != nil {
			t.Fatalf("iteration %d returned transport error: %v", i, err)
		}
		if result.Success || !strings.Contains(result.Error, "terminal stream failure") {
			t.Fatalf("iteration %d result = %#v, want terminal tool failure", i, result)
		}
	}
}

func TestWebFetchStreamingPreservesToolFailure(t *testing.T) {
	tool := &WebFetchTool{
		client: &http.Client{Transport: staticHTTPRoundTripper{
			status: http.StatusNotFound,
			body:   "missing",
		}},
		maxSize: 1024,
	}
	stream, err := tool.ExecuteStreaming(context.Background(), map[string]any{
		"url": "https://example.test/missing",
	})
	if err != nil {
		t.Fatalf("ExecuteStreaming: %v", err)
	}
	content, streamErr := CollectStreamingResult(stream)
	if streamErr == nil || !strings.Contains(streamErr.Error(), "HTTP 404") {
		t.Fatalf("stream error = %v, want HTTP 404 failure", streamErr)
	}
	if content != "" {
		t.Fatalf("failure was encoded as successful content: %q", content)
	}
}

func TestExecuteStreamingToolCannotReportSuccessAfterCancellation(t *testing.T) {
	executor := NewExecutor(NewRegistry(), nil, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := executor.executeStreamingTool(ctx, closedSuccessfulStreamingTool{}, nil)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("transport error = %v, want context cancellation", err)
	}
	if result.Success {
		t.Fatalf("cancelled closed stream reported success: %+v", result)
	}
}

func TestWebFetchStreamingPublishesCancellation(t *testing.T) {
	tool := &WebFetchTool{
		client: &http.Client{Transport: staticHTTPRoundTripper{
			status: http.StatusOK,
			body:   strings.Repeat("payload", 2000),
		}},
		maxSize: 1 << 20,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream, err := tool.ExecuteStreaming(ctx, map[string]any{"url": "https://example.test/cancel"})
	if err != nil {
		t.Fatalf("ExecuteStreaming: %v", err)
	}
	_, streamErr := CollectStreamingResult(stream)
	if streamErr == nil || !errors.Is(streamErr, context.Canceled) {
		t.Fatalf("stream error = %v, want context cancellation", streamErr)
	}
}

type closedErrorStreamingTool struct{}

type closedSuccessfulStreamingTool struct{ closedErrorStreamingTool }

func (closedSuccessfulStreamingTool) ExecuteStreaming(context.Context, map[string]any) (*StreamingToolResult, error) {
	chunks := make(chan string, 1)
	chunks <- "partial"
	close(chunks)
	done := make(chan struct{})
	close(done)
	return &StreamingToolResult{Chunks: chunks, Done: done, Error: make(chan error)}, nil
}

func (closedErrorStreamingTool) Name() string        { return "closed_error_stream" }
func (closedErrorStreamingTool) Description() string { return "test-only stream" }
func (closedErrorStreamingTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "closed_error_stream"}
}
func (closedErrorStreamingTool) Validate(map[string]any) error { return nil }
func (closedErrorStreamingTool) Execute(context.Context, map[string]any) (ToolResult, error) {
	return NewErrorResult("not used"), nil
}
func (closedErrorStreamingTool) SupportsStreaming() bool { return true }
func (closedErrorStreamingTool) ExecuteStreaming(context.Context, map[string]any) (*StreamingToolResult, error) {
	chunks := make(chan string)
	done := make(chan struct{})
	errCh := make(chan error, 1)
	errCh <- errors.New("terminal stream failure")
	close(chunks)
	close(done)
	return &StreamingToolResult{Chunks: chunks, Done: done, Error: errCh}, nil
}

type staticHTTPRoundTripper struct {
	status int
	body   string
}

func (r staticHTTPRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: r.status,
		Status:     http.StatusText(r.status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(r.body)),
	}, nil
}
