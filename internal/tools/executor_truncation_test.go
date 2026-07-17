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

type roundIdentityClient struct {
	*scriptedExecutorClient
	providers []string
	models    []string
}

func (c *roundIdentityClient) identityIndex() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := c.next - 1
	if idx < 0 {
		return 0
	}
	return idx
}

func (c *roundIdentityClient) GetProvider() string {
	idx := c.identityIndex()
	if idx >= len(c.providers) {
		return ""
	}
	return c.providers[idx]
}

func (c *roundIdentityClient) GetModel() string {
	idx := c.identityIndex()
	if idx >= len(c.models) {
		return ""
	}
	return c.models[idx]
}

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
	if input != 330 {
		t.Fatalf("input tokens = %d, want aggregate 330", input)
	}
	if output != 33 {
		t.Fatalf("output tokens = %d, want accumulated 33", output)
	}
}

func TestExecutorExecuteLoop_AggregatesCacheUsageAcrossRounds(t *testing.T) {
	registry := NewRegistry()
	cl := &scriptedExecutorClient{
		model: "glm-5.2",
		responses: []*client.StreamingResponse{
			buildExecutorTestUsageStream("part one", genai.FinishReasonMaxTokens, 1000, 20, 100, 700),
			buildExecutorTestUsageStream("part two", genai.FinishReasonStop, 1200, 30, 0, 900),
		},
	}

	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false
	if _, _, err := exec.Execute(context.Background(), nil, "continue with cached context"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	input, output := exec.GetLastTokenUsage()
	if input != 3900 || output != 50 {
		t.Fatalf("token usage = (%d, %d), want full prompt aggregate (3900, 50)", input, output)
	}
	creation, read := exec.GetLastCacheMetrics()
	if creation != 100 || read != 1600 {
		t.Fatalf("cache usage = (%d, %d), want aggregate (100, 1600)", creation, read)
	}
}

func TestExecutorExecuteLoop_AggregatesUsageAcrossToolRounds(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	cl := &scriptedExecutorClient{
		model: "glm-5.2",
		responses: []*client.StreamingResponse{
			buildExecutorTestReadUsageStream("read-1", 1_000, 10, 50, 700),
			buildExecutorTestReadUsageStream("read-2", 1_200, 20, 0, 900),
			buildExecutorTestUsageStream("done", genai.FinishReasonStop, 1_400, 30, 25, 1_100),
		},
	}

	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false
	if _, finalText, err := exec.Execute(context.Background(), nil, "inspect two files"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	} else if finalText != "done" {
		t.Fatalf("finalText = %q, want done", finalText)
	}
	if readTool.calls != 2 {
		t.Fatalf("read calls = %d, want 2", readTool.calls)
	}

	input, output := exec.GetLastTokenUsage()
	if input != 6_375 || output != 60 {
		t.Fatalf("tool-round token usage = (%d, %d), want (6375, 60)", input, output)
	}
	creation, read := exec.GetLastCacheMetrics()
	if creation != 75 || read != 2_700 {
		t.Fatalf("tool-round cache usage = (%d, %d), want (75, 2700)", creation, read)
	}
}

func TestExecutorExecuteLoop_PricesEachProviderRound(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	base := &scriptedExecutorClient{responses: []*client.StreamingResponse{
		buildExecutorTestReadUsageStream("read-1", 100, 10, 0, 0),
		buildExecutorTestReadUsageStream("read-2", 200, 20, 0, 0),
		buildExecutorTestUsageStream("done", genai.FinishReasonStop, 300, 30, 0, 0),
	}}
	cl := &roundIdentityClient{
		scriptedExecutorClient: base,
		providers:              []string{"glm", "kimi", "deepseek"},
		models:                 []string{"glm-5", "kimi-for-coding", "deepseek-v4"},
	}
	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false
	var seen []string
	exec.SetCostCalculator(func(provider, model string, _, _, _ int) (float64, bool) {
		seen = append(seen, provider+"/"+model)
		return float64(len(seen)), true
	})

	if _, _, err := exec.Execute(context.Background(), nil, "inspect two files"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	cost, tracked := exec.GetLastEstimatedCost()
	if !tracked || cost != 6 {
		t.Fatalf("mixed-provider cost = tracked %v cost %v, want 6", tracked, cost)
	}
	want := []string{"glm/glm-5", "kimi/kimi-for-coding", "deepseek/deepseek-v4"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("priced identities = %v, want %v", seen, want)
	}
	provider, model := exec.GetLastProviderIdentity()
	if provider != "deepseek" || model != "deepseek-v4" {
		t.Fatalf("last identity = %s/%s, want deepseek/deepseek-v4", provider, model)
	}
}

func TestExecutorExecuteLoop_AccountsUsageBeforeStreamError(t *testing.T) {
	cl := &scriptedExecutorClient{
		model: "glm-5",
		responses: []*client.StreamingResponse{buildExecutorTestStream(
			client.ResponseChunk{
				InputTokens:              1_000,
				OutputTokens:             25,
				CacheCreationInputTokens: 100,
				CacheReadInputTokens:     700,
			},
			client.ResponseChunk{Error: errors.New("terminal stream failure"), Done: true},
		)},
	}
	exec := NewExecutor(NewRegistry(), cl, time.Second)
	exec.preFlightChecks = false
	exec.SetCostCalculator(func(_, _ string, _, _, _ int) (float64, bool) {
		return 0.42, true
	})

	if _, _, err := exec.Execute(context.Background(), nil, "fail after usage"); err == nil {
		t.Fatal("Execute() error = nil, want stream failure")
	}
	input, output := exec.GetLastTokenUsage()
	if input != 1_800 || output != 25 {
		t.Fatalf("partial-error usage = (%d,%d), want full prompt (1800,25)", input, output)
	}
	creation, read := exec.GetLastCacheMetrics()
	if creation != 100 || read != 700 {
		t.Fatalf("partial-error cache = (%d,%d), want (100,700)", creation, read)
	}
	if cost, tracked := exec.GetLastEstimatedCost(); !tracked || cost != 0.42 {
		t.Fatalf("partial-error cost = tracked %v cost %v, want 0.42", tracked, cost)
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

func buildExecutorTestUsageStream(text string, finish genai.FinishReason, input, output, creation, read int) *client.StreamingResponse {
	return buildExecutorTestStream(client.ResponseChunk{
		Text:                     text,
		Done:                     true,
		FinishReason:             finish,
		InputTokens:              input,
		OutputTokens:             output,
		CacheCreationInputTokens: creation,
		CacheReadInputTokens:     read,
	})
}

func buildExecutorTestReadUsageStream(id string, input, output, creation, read int) *client.StreamingResponse {
	return buildExecutorTestStream(client.ResponseChunk{
		FunctionCalls: []*genai.FunctionCall{{
			ID:   id,
			Name: "read",
			Args: map[string]any{"file_path": "test.txt"},
		}},
		Done:                     true,
		FinishReason:             genai.FinishReasonStop,
		InputTokens:              input,
		OutputTokens:             output,
		CacheCreationInputTokens: creation,
		CacheReadInputTokens:     read,
	})
}

func buildExecutorTestHungStream() *client.StreamingResponse {
	return &client.StreamingResponse{
		Chunks: make(chan client.ResponseChunk),
		Done:   make(chan struct{}),
	}
}
