package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gokin/internal/client"

	"google.golang.org/genai"
)

func TestExecutorExecuteLoop_MaxTokensTextAutoContinuesIntoToolCall(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	cl := &scriptedExecutorClient{
		model: "glm-5.1",
		responses: []*client.StreamingResponse{
			buildExecutorTestMaxTokensTextStream("Let me read the rest first."),
			buildExecutorTestReadStream("read-after-truncation"),
			buildExecutorTestTextStream("Done after reading."),
		},
	}

	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false

	history, finalText, err := exec.Execute(context.Background(), nil, "finish the fix")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if readTool.calls != 1 {
		t.Fatalf("read tool calls = %d, want 1", readTool.calls)
	}
	if finalText != "Let me read the rest first.Done after reading." {
		t.Fatalf("finalText = %q", finalText)
	}
	if strings.Contains(finalText, "Response truncated") {
		t.Fatalf("finalText still contains truncation warning: %q", finalText)
	}

	historyText := strings.Join(flattenHistoryTexts(history), "\n")
	if !strings.Contains(historyText, "Continue exactly where the previous assistant message stopped") {
		t.Fatalf("history missing continuation prompt: %q", historyText)
	}
}

func TestExecutorExecuteLoop_MaxTokensTextChunksAreCarried(t *testing.T) {
	registry := NewRegistry()
	cl := &scriptedExecutorClient{
		model: "glm-5.1",
		responses: []*client.StreamingResponse{
			buildExecutorTestMaxTokensTextStreamWithUsage("Part one. ", 100, 10),
			buildExecutorTestMaxTokensTextStreamWithUsage("Part two. ", 110, 11),
			buildExecutorTestTextStreamWithUsage("Part three.", 120, 12),
		},
	}

	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false

	_, finalText, err := exec.Execute(context.Background(), nil, "write a long answer")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if finalText != "Part one. Part two. Part three." {
		t.Fatalf("finalText = %q", finalText)
	}
	if strings.Contains(finalText, "Response truncated") {
		t.Fatalf("finalText should not contain warning after successful continuation: %q", finalText)
	}
	if cl.next != 3 {
		t.Fatalf("model calls = %d, want 3", cl.next)
	}

	input, output := exec.GetLastTokenUsage()
	if input != 120 {
		t.Fatalf("input tokens = %d, want latest 120", input)
	}
	if output != 33 {
		t.Fatalf("output tokens = %d, want accumulated 33", output)
	}
}

func TestExecutorExecuteLoop_MaxTokensContinuationIsBounded(t *testing.T) {
	registry := NewRegistry()
	cl := &scriptedExecutorClient{
		model: "glm-5.1",
		responses: []*client.StreamingResponse{
			buildExecutorTestMaxTokensTextStream("A "),
			buildExecutorTestMaxTokensTextStream("B "),
			buildExecutorTestMaxTokensTextStream("C "),
			buildExecutorTestMaxTokensTextStream("D "),
			buildExecutorTestTextStream("should not be requested"),
		},
	}

	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false

	history, finalText, err := exec.Execute(context.Background(), nil, "write a very long answer")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if cl.next != 4 {
		t.Fatalf("model calls = %d, want 4 (initial + 3 continuations)", cl.next)
	}
	if !strings.Contains(finalText, "A B C D ") {
		t.Fatalf("finalText missing carried chunks: %q", finalText)
	}
	if !strings.Contains(finalText, "Response truncated") {
		t.Fatalf("finalText missing bounded truncation warning: %q", finalText)
	}

	historyText := strings.Join(flattenHistoryTexts(history), "\n")
	if got := strings.Count(historyText, "Continue exactly where the previous assistant message stopped"); got != 3 {
		t.Fatalf("continuation prompt count = %d, want 3\nhistory: %q", got, historyText)
	}
}

func TestExecutorExecuteLoop_HungModelStreamTimesOut(t *testing.T) {
	registry := NewRegistry()
	cl := &scriptedExecutorClient{
		model:     "glm-5.1",
		responses: []*client.StreamingResponse{buildExecutorTestHungStream()},
	}

	exec := NewExecutor(registry, cl, time.Second)
	exec.SetModelRoundTimeout(20 * time.Millisecond)

	start := time.Now()
	_, _, err := exec.Execute(context.Background(), nil, "do not hang")
	if err == nil {
		t.Fatal("Execute() error = nil, want model round timeout")
	}
	if !errors.Is(err, client.ErrModelRoundTimeout) {
		t.Fatalf("Execute() error = %v, want ErrModelRoundTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Execute() took %v, want bounded timeout", elapsed)
	}
}

func buildExecutorTestMaxTokensTextStream(text string) *client.StreamingResponse {
	return buildExecutorTestMaxTokensTextStreamWithUsage(text, 0, 0)
}

func buildExecutorTestMaxTokensTextStreamWithUsage(text string, inputTokens, outputTokens int) *client.StreamingResponse {
	return buildExecutorTestStream(client.ResponseChunk{
		Text:         text,
		Done:         true,
		FinishReason: genai.FinishReasonMaxTokens,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	})
}

func buildExecutorTestTextStreamWithUsage(text string, inputTokens, outputTokens int) *client.StreamingResponse {
	return buildExecutorTestStream(client.ResponseChunk{
		Text:         text,
		Done:         true,
		FinishReason: genai.FinishReasonStop,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	})
}

func buildExecutorTestHungStream() *client.StreamingResponse {
	return &client.StreamingResponse{
		Chunks: make(chan client.ResponseChunk),
		Done:   make(chan struct{}),
	}
}
