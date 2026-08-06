package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/config"
	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

type headlessPartialTimeoutClient struct {
	*testkit.MockClient
	text string
}

func (c *headlessPartialTimeoutClient) SendMessageWithHistory(
	context.Context,
	[]*genai.Content,
	string,
) (*client.StreamingResponse, error) {
	chunks := make(chan client.ResponseChunk, 1)
	chunks <- client.ResponseChunk{Text: c.text, InputTokens: 40, OutputTokens: 11}
	return &client.StreamingResponse{Chunks: chunks, Done: make(chan struct{})}, nil
}

func newHeadlessPartialTimeoutTestApp(t *testing.T, partial string) (*App, *tools.Executor) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Model.Provider = "mock"
	cfg.Model.Name = "mock-model"
	cfg.DoneGate.Enabled = false

	modelClient := &headlessPartialTimeoutClient{
		MockClient: testkit.NewMockClient(),
		text:       partial,
	}
	registry := tools.NewRegistry()
	tool := &appHeadlessScriptedTool{name: "unused"}
	if err := registry.Register(tool); err != nil {
		t.Fatal(err)
	}
	executor := tools.NewExecutor(registry, modelClient, time.Second)
	executor.SetModelRoundTimeout(20 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	application := &App{
		config:              cfg,
		workDir:             t.TempDir(),
		client:              modelClient,
		registry:            registry,
		executor:            executor,
		session:             chat.NewSession(),
		ctx:                 ctx,
		cancel:              cancel,
		rateLimitRetryCount: make(map[string]int),
		// Intentionally leave autoResumeCount nil: this is a supported zero-value
		// boundary and catches a previously terminal-status-masked panic.
	}
	executor.SetHandler(application.buildExecutionHandler(nil))
	return application, executor
}

func TestRunHeadlessWithOptions_JSONTimeoutRetainsPartialResult(t *testing.T) {
	const partial = "Useful partial result before the deadline."
	application, _ := newHeadlessPartialTimeoutTestApp(t, partial)
	var stdout bytes.Buffer

	returned, err := application.RunHeadlessWithOptions(context.Background(), "finish the analysis", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       &stdout,
		Stderr:       io.Discard,
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("headless timeout error = %v, want typed deadline error", err)
	}
	decoded := decodeSingleHeadlessResult(t, stdout.Bytes())
	for name, result := range map[string]HeadlessResult{"returned": returned, "encoded": decoded} {
		if result.Status != "timeout" || result.Error == nil || result.Error.Kind != "timeout" {
			t.Fatalf("%s timeout result = %+v", name, result)
		}
		if result.Result != partial {
			t.Fatalf("%s partial result = %q, want %q", name, result.Result, partial)
		}
		if !strings.Contains(result.Error.Message, string(client.FailureReasonModelRoundTimeout)) {
			t.Fatalf("%s timeout diagnostic = %q", name, result.Error.Message)
		}
		if result.Usage.InputTokens != 40 || result.Usage.OutputTokens != 11 {
			t.Fatalf("%s partial timeout usage = %+v, want input/output 40/11", name, result.Usage)
		}
	}
	if strings.Contains(stdout.String(), "panic in message processing") {
		t.Fatalf("masked scheduler panic leaked into JSON: %s", stdout.String())
	}
}

func TestRunHeadlessWithOptions_TextTimeoutPrintsPartialAndFails(t *testing.T) {
	const partial = "Useful text-mode partial result."
	application, _ := newHeadlessPartialTimeoutTestApp(t, partial)
	var stdout bytes.Buffer

	returned, err := application.RunHeadlessWithOptions(context.Background(), "finish the analysis", HeadlessOptions{
		OutputFormat: HeadlessOutputText,
		Stdout:       &stdout,
		Stderr:       io.Discard,
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("text timeout error = %v, want typed deadline error", err)
	}
	if returned.Status != "timeout" || returned.Result != partial ||
		returned.Error == nil || returned.Error.Kind != "timeout" {
		t.Fatalf("text timeout result = %+v", returned)
	}
	if got := stdout.String(); got != partial+"\n" {
		t.Fatalf("text timeout stdout = %q, want partial result plus newline", got)
	}
}

func TestRunHeadlessWithOptions_StreamJSONTimeoutRetainsDeltaAndPartialResult(t *testing.T) {
	const partial = "Partial stream-json result."
	application, _ := newHeadlessPartialTimeoutTestApp(t, partial)
	var stdout bytes.Buffer

	returned, err := application.RunHeadlessWithOptions(context.Background(), "finish the analysis", HeadlessOptions{
		OutputFormat: HeadlessOutputStreamJSON,
		Stdout:       &stdout,
		Stderr:       io.Discard,
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stream-json timeout error = %v, want typed deadline error", err)
	}
	records := decodeHeadlessJSONL(t, stdout.Bytes())
	if len(records) < 2 {
		t.Fatalf("stream-json records = %d, want progress + terminal:\n%s", len(records), stdout.String())
	}
	var sawPartialDelta bool
	for _, raw := range records[:len(records)-1] {
		var event HeadlessStreamEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "assistant_delta" && event.Data["text"] == partial {
			sawPartialDelta = true
		}
	}
	if !sawPartialDelta {
		t.Fatalf("stream-json lost the partial assistant delta:\n%s", stdout.String())
	}
	var terminal HeadlessResult
	if err := json.Unmarshal(records[len(records)-1], &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.Type != "result" || terminal.Status != "timeout" || terminal.Result != partial ||
		terminal.Error == nil || terminal.Error.Kind != "timeout" {
		t.Fatalf("stream-json terminal result = %+v", terminal)
	}
	if returned.Status != terminal.Status || returned.Result != terminal.Result {
		t.Fatalf("returned result diverges from terminal JSON: %+v vs %+v", returned, terminal)
	}
}
