package app

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
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

func TestIsOverloadError_Words(t *testing.T) {
	cases := map[string]bool{
		"server overloaded":            true,
		"API rate limit exceeded":      true,
		"too many requests (HTTP 429)": true,
		"random network timeout":       false,
		"connection reset by peer":     false,
		"":                             false,
	}
	for msg, want := range cases {
		got := isOverloadError(errors.New(msg))
		if got != want {
			t.Errorf("isOverloadError(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestIsOverloadError_CodesWordBoundary(t *testing.T) {
	// Real z.ai errors — should match.
	for _, msg := range []string{
		"z.ai API error (1305): service unavailable",
		"[GLM 1305] overloaded",
		"HTTP 529: Site is overloaded",
		"status 529 returned",
		"code=1305",
	} {
		if !isOverloadError(errors.New(msg)) {
			t.Errorf("expected overload match for %q", msg)
		}
	}

	// Numeric substrings embedded inside timings — must NOT match.
	for _, msg := range []string{
		"timeout after 1305ms",
		"elapsed 529ms",
		"retry delay 11305 attempts",
		"dial error 5291",
	} {
		if isOverloadError(errors.New(msg)) {
			t.Errorf("unexpected overload match for %q", msg)
		}
	}
}

func TestIsOverloadError_Nil(t *testing.T) {
	if isOverloadError(nil) {
		t.Error("nil error should not match overload")
	}
}

func TestProcessMessageWithContext_RetriesEmptyResponseAfterToolResults(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model.Provider = "mock"
	cfg.Model.Name = "mock-model"
	cfg.DoneGate.Enabled = false

	mock := testkit.NewMockClient()
	mock.EnqueueToolCall("read", map[string]any{
		"file_path": "project.go",
		"offset":    1,
		"limit":     100,
	})
	mock.EnqueueScript(testkit.ResponseScript{
		Chunks: []client.ResponseChunk{
			{Done: true, FinishReason: genai.FinishReasonStop},
		},
	})
	mock.EnqueueText("Retried and completed after the empty tool follow-up.")

	registry := tools.NewRegistry()
	if err := registry.Register(&appRetryReadTool{}); err != nil {
		t.Fatalf("Register(read): %v", err)
	}
	exec := tools.NewExecutor(registry, mock, time.Second)

	a := &App{
		config:              cfg,
		workDir:             t.TempDir(),
		client:              mock,
		registry:            registry,
		executor:            exec,
		session:             chat.NewSession(),
		ctx:                 context.Background(),
		rateLimitRetryCount: make(map[string]int),
	}

	a.processMessageWithContext(context.Background(), "inspect project.go")

	methods := make([]string, 0, len(mock.Calls()))
	for _, call := range mock.Calls() {
		methods = append(methods, call.Method)
	}
	wantMethods := []string{
		"SendMessageWithHistory",
		"SendFunctionResponse",
		"SendMessageWithHistory",
	}
	if !slices.Equal(methods, wantMethods) {
		t.Fatalf("mock call methods = %v, want %v", methods, wantMethods)
	}

	historyText := strings.Join(flattenAppTestHistory(a.session.GetHistory()), "\n")
	if strings.Contains(historyText, "[Auto]") || strings.Contains(historyText, "I've read the file") {
		t.Fatalf("history contains stale auto fallback:\n%s", historyText)
	}
	if !strings.Contains(historyText, "Retried and completed after the empty tool follow-up.") {
		t.Fatalf("history missing retried final response:\n%s", historyText)
	}

	if a.lastError != "" {
		t.Fatalf("lastError = %q, want cleared after retry success", a.lastError)
	}
}

func TestRunHeadless_ProcessesOnePromptWithoutTUI(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model.Provider = "mock"
	cfg.Model.Name = "mock-model"
	cfg.DoneGate.Enabled = false

	mock := testkit.NewMockClient()
	mock.EnqueueText("Headless response complete.")

	registry := tools.NewRegistry()
	exec := tools.NewExecutor(registry, mock, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &App{
		config:              cfg,
		workDir:             t.TempDir(),
		client:              mock,
		registry:            registry,
		executor:            exec,
		session:             chat.NewSession(),
		ctx:                 ctx,
		cancel:              cancel,
		rateLimitRetryCount: make(map[string]int),
	}

	// Hand-built App: install the unified execution handler the builder
	// would normally wire (RunHeadless only swaps the presenter).
	exec.SetHandler(a.buildExecutionHandler(nil))

	stdout := captureStdout(t, func() error {
		return a.RunHeadless(context.Background(), "answer once")
	})

	if !strings.Contains(stdout, "Headless response complete.") {
		t.Fatalf("stdout missing headless response: %q", stdout)
	}
	if !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("headless stdout must end with a newline for shell capture: %q", stdout)
	}
	if a.headlessDirect {
		t.Fatal("RunHeadless leaked its direct-routing override after the invocation")
	}

	if err := a.lastError; err != "" {
		t.Fatalf("RunHeadless() error = %v", err)
	}

	calls := mock.Calls()
	if len(calls) != 1 || calls[0].Method != "SendMessageWithHistory" {
		t.Fatalf("mock calls = %+v, want one SendMessageWithHistory", calls)
	}
	historyText := strings.Join(flattenAppTestHistory(a.session.GetHistory()), "\n")
	if !strings.Contains(historyText, "Headless response complete.") {
		t.Fatalf("history missing headless response:\n%s", historyText)
	}
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	os.Stdout = w

	runErr := fn()
	_ = w.Close()
	os.Stdout = orig
	if runErr != nil {
		t.Fatalf("captured function error = %v", runErr)
	}

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout pipe: %v", err)
	}
	return string(out)
}

type appRetryReadTool struct{}

func (t *appRetryReadTool) Name() string { return "read" }

func (t *appRetryReadTool) Description() string { return "test read tool" }

func (t *appRetryReadTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "read", Description: "test read"}
}

func (t *appRetryReadTool) Validate(args map[string]any) error { return nil }

func (t *appRetryReadTool) Execute(ctx context.Context, args map[string]any) (tools.ToolResult, error) {
	return tools.NewSuccessResult("package main\n\nfunc main() {}\n"), nil
}

func flattenAppTestHistory(history []*genai.Content) []string {
	var out []string
	for _, content := range history {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part != nil && strings.TrimSpace(part.Text) != "" {
				out = append(out, part.Text)
			}
		}
	}
	return out
}
