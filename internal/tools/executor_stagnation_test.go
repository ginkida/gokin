package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/client"

	"google.golang.org/genai"
)

type scriptedExecutorClient struct {
	model string

	mu              sync.Mutex
	next            int
	responses       []*client.StreamingResponse
	functionResults [][]*genai.FunctionResponse
	functionHistory [][]string
}

func (c *scriptedExecutorClient) SendMessage(ctx context.Context, message string) (*client.StreamingResponse, error) {
	return c.nextResponse()
}

func (c *scriptedExecutorClient) SendMessageWithHistory(ctx context.Context, history []*genai.Content, message string) (*client.StreamingResponse, error) {
	return c.nextResponse()
}

func (c *scriptedExecutorClient) SendFunctionResponse(ctx context.Context, history []*genai.Content, results []*genai.FunctionResponse) (*client.StreamingResponse, error) {
	c.mu.Lock()
	c.functionResults = append(c.functionResults, cloneFunctionResponses(results))
	c.functionHistory = append(c.functionHistory, flattenHistoryTexts(history))
	c.mu.Unlock()
	return c.nextResponse()
}

func (c *scriptedExecutorClient) SetTools(tools []*genai.Tool)       {}
func (c *scriptedExecutorClient) SetRateLimiter(limiter interface{}) {}
func (c *scriptedExecutorClient) CountTokens(ctx context.Context, contents []*genai.Content) (*genai.CountTokensResponse, error) {
	return &genai.CountTokensResponse{}, nil
}
func (c *scriptedExecutorClient) GetModel() string          { return c.model }
func (c *scriptedExecutorClient) SetModel(modelName string) { c.model = modelName }
func (c *scriptedExecutorClient) WithModel(modelName string) client.Client {
	// Fresh struct — copying *c would copy the mutex and trip copylocks.
	// The scripted response queue is intentionally per-instance, so
	// callers that want the same script must re-enqueue.
	return &scriptedExecutorClient{model: modelName}
}
func (c *scriptedExecutorClient) GetRawClient() any                       { return nil }
func (c *scriptedExecutorClient) SetSystemInstruction(instruction string) {}
func (c *scriptedExecutorClient) SetTurnContext(turnContext string)       {}
func (c *scriptedExecutorClient) SetThinkingBudget(budget int32)          {}
func (c *scriptedExecutorClient) Close() error                            { return nil }

func (c *scriptedExecutorClient) nextResponse() (*client.StreamingResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.next >= len(c.responses) {
		return nil, fmt.Errorf("unexpected model call #%d", c.next+1)
	}
	resp := c.responses[c.next]
	c.next++
	return resp, nil
}

type scriptedReadTool struct {
	calls int
}

func (t *scriptedReadTool) Name() string { return "read" }

func (t *scriptedReadTool) Description() string {
	return "test read tool"
}

func (t *scriptedReadTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "read", Description: "test read"}
}

func (t *scriptedReadTool) Validate(args map[string]any) error { return nil }

func (t *scriptedReadTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	t.calls++
	return NewSuccessResult("package main\n\nfunc main() {}\n"), nil
}

type scriptedStaticTool struct {
	name    string
	content string
	calls   int
}

func (t *scriptedStaticTool) Name() string { return t.name }

func (t *scriptedStaticTool) Description() string {
	return "test tool"
}

func (t *scriptedStaticTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.name, Description: "test tool"}
}

func (t *scriptedStaticTool) Validate(args map[string]any) error { return nil }

func (t *scriptedStaticTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	t.calls++
	return NewSuccessResult(t.content), nil
}

func TestExecutorExecuteLoop_EmptyResponseAfterToolResultsIsRetryableError(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	cl := &scriptedExecutorClient{
		model: "kimi-for-coding",
		responses: []*client.StreamingResponse{
			buildExecutorTestReadStream("r0"),
			buildExecutorTestEmptyStream(),
		},
	}
	exec := NewExecutor(registry, cl, time.Second)

	_, finalText, err := exec.Execute(context.Background(), nil, "inspect project.go")
	if err == nil {
		t.Fatal("Execute() error = nil, want retryable empty-response error")
	}
	if !errors.Is(err, client.ErrEmptyModelResponse) {
		t.Fatalf("Execute() error = %v, want ErrEmptyModelResponse", err)
	}
	if strings.Contains(finalText, "[Auto]") || strings.Contains(finalText, "I've read the file") {
		t.Fatalf("finalText used stale auto fallback: %q", finalText)
	}
	if !client.IsRetryableError(err) {
		t.Fatalf("empty response after tools should be retryable: %v", err)
	}
}

func TestExecutorExecuteLoop_KimiRepeatedReadUsesRecoveryHint(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	cl := &scriptedExecutorClient{
		model: "kimi-for-coding",
		responses: []*client.StreamingResponse{
			buildExecutorTestReadStream("r0"),
			buildExecutorTestReadStream("r1"),
			buildExecutorTestReadStream("r2"),
			buildExecutorTestReadStream("r3"),
			buildExecutorTestReadStream("r4"),
			buildExecutorTestTextStream("Recovered after loop guard."),
		},
	}

	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false

	_, finalText, err := exec.Execute(context.Background(), nil, "inspect project.go")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if finalText != "Recovered after loop guard." {
		t.Fatalf("finalText = %q, want %q", finalText, "Recovered after loop guard.")
	}
	if readTool.calls != 4 {
		t.Fatalf("read tool calls = %d, want 4 before loop guard short-circuits the 5th repeat", readTool.calls)
	}
	if len(cl.functionResults) != 5 {
		t.Fatalf("SendFunctionResponse calls = %d, want 5", len(cl.functionResults))
	}

	recoveryResults := cl.functionResults[len(cl.functionResults)-1]
	if len(recoveryResults) != 1 {
		t.Fatalf("recovery result count = %d, want 1", len(recoveryResults))
	}
	success, _ := recoveryResults[0].Response["success"].(bool)
	if success {
		t.Fatalf("recovery result should be marked unsuccessful to break the loop: %+v", recoveryResults[0].Response)
	}
	errMsg, _ := recoveryResults[0].Response["error"].(string)
	if !strings.Contains(errMsg, "Do not call it again") {
		t.Fatalf("recovery error message = %q, want explicit loop-break guidance", errMsg)
	}
}

// Read-loop recovery is model-agnostic (v0.86.7): a non-Kimi model that re-reads
// the same range gets the same loop-break hint Kimi already got, instead of the
// turn-killing hard abort that used to fire for every other provider.
func TestExecutorExecuteLoop_NonKimiRepeatedReadRecovers(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	cl := &scriptedExecutorClient{
		model: "glm-4.7",
		responses: []*client.StreamingResponse{
			buildExecutorTestReadStream("r0"),
			buildExecutorTestReadStream("r1"),
			buildExecutorTestReadStream("r2"),
			buildExecutorTestReadStream("r3"),
			buildExecutorTestReadStream("r4"),
			buildExecutorTestTextStream("Recovered after loop guard."),
		},
	}

	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false

	_, finalText, err := exec.Execute(context.Background(), nil, "inspect project.go")
	if err != nil {
		t.Fatalf("Execute() error = %v, want graceful recovery (no abort) for non-Kimi read loop", err)
	}
	if finalText != "Recovered after loop guard." {
		t.Fatalf("finalText = %q, want %q", finalText, "Recovered after loop guard.")
	}
	if readTool.calls != 4 {
		t.Fatalf("read tool calls = %d, want 4 before loop guard short-circuits the 5th repeat", readTool.calls)
	}
	if len(cl.functionResults) != 5 {
		t.Fatalf("SendFunctionResponse calls = %d, want 5 (recovery hint on the 5th, not an abort)", len(cl.functionResults))
	}

	recoveryResults := cl.functionResults[len(cl.functionResults)-1]
	if len(recoveryResults) != 1 {
		t.Fatalf("recovery result count = %d, want 1", len(recoveryResults))
	}
	if success, _ := recoveryResults[0].Response["success"].(bool); success {
		t.Fatalf("recovery result should be marked unsuccessful to break the loop: %+v", recoveryResults[0].Response)
	}
	errMsg, _ := recoveryResults[0].Response["error"].(string)
	if !strings.Contains(errMsg, "Do not call it again") {
		t.Fatalf("recovery error message = %q, want explicit loop-break guidance", errMsg)
	}
	if !strings.Contains(errMsg, "already have this file's content") {
		t.Fatalf("recovery error message = %q, want read-specific reuse guidance", errMsg)
	}
}

// The hard abort remains as a bounded backstop: a read loop that ignores every
// recovery hint (budget 3) AND both force-finalize demands still aborts.
func TestExecutorExecuteLoop_RepeatedReadAbortsAfterRecoveryBudget(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// 10 identical reads: 4 execute, the 5th–7th each draw a recovery hint
	// (budget = 3), the 8th–9th draw force-finalize demands, and the 10th
	// exhausts everything and aborts.
	responses := make([]*client.StreamingResponse, 0, 10)
	for i := 0; i < 10; i++ {
		responses = append(responses, buildExecutorTestReadStream(fmt.Sprintf("r%d", i)))
	}
	cl := &scriptedExecutorClient{model: "glm-4.7", responses: responses}

	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false

	_, _, err := exec.Execute(context.Background(), nil, "inspect project.go")
	if err == nil {
		t.Fatal("Execute() error = nil, want stagnation abort after recovery + finalize budgets exhausted")
	}
	if !strings.Contains(err.Error(), "executor stagnation") {
		t.Fatalf("err = %v, want executor stagnation", err)
	}
	if !strings.Contains(err.Error(), "after recovery hint") {
		t.Fatalf("err = %v, want it to note the recovery hints were already spent", err)
	}
	if readTool.calls != 4 {
		t.Fatalf("read tool calls = %d, want 4 (recovery hints never re-execute the tool)", readTool.calls)
	}
	if len(cl.functionResults) != 9 {
		t.Fatalf("SendFunctionResponse calls = %d, want 9 (4 reads + 3 hints + 2 finalize) before abort", len(cl.functionResults))
	}
}

func TestExecutorExecuteLoop_KimiRepeatedSearchUsesRecoveryHint(t *testing.T) {
	tests := []struct {
		name         string
		toolName     string
		toolContent  string
		stream       func(id string) *client.StreamingResponse
		targetPhrase string
	}{
		{
			name:         "grep",
			toolName:     "grep",
			toolContent:  "internal/tools/executor.go:123: loop guard\n",
			stream:       buildExecutorTestGrepStream,
			targetPhrase: `grep "loop guard" in internal/tools`,
		},
		{
			name:         "glob",
			toolName:     "glob",
			toolContent:  "internal/tools/executor.go\ninternal/tools/tool.go\n",
			stream:       buildExecutorTestGlobStream,
			targetPhrase: `glob "**/*.go" in internal/tools`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewRegistry()
			tool := &scriptedStaticTool{name: tt.toolName, content: tt.toolContent}
			if err := registry.Register(tool); err != nil {
				t.Fatalf("Register() error = %v", err)
			}

			cl := &scriptedExecutorClient{
				model: "kimi-for-coding",
				responses: []*client.StreamingResponse{
					tt.stream("r0"),
					tt.stream("r1"),
					tt.stream("r2"),
					tt.stream("r3"),
					tt.stream("r4"),
					buildExecutorTestTextStream("Recovered after search loop guard."),
				},
			}

			exec := NewExecutor(registry, cl, time.Second)
			exec.preFlightChecks = false

			_, finalText, err := exec.Execute(context.Background(), nil, "search the project")
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if finalText != "Recovered after search loop guard." {
				t.Fatalf("finalText = %q, want %q", finalText, "Recovered after search loop guard.")
			}
			if tool.calls != 4 {
				t.Fatalf("%s calls = %d, want 4 before loop guard short-circuits the 5th repeat", tt.toolName, tool.calls)
			}

			recoveryResults := cl.functionResults[len(cl.functionResults)-1]
			if len(recoveryResults) != 1 {
				t.Fatalf("recovery result count = %d, want 1", len(recoveryResults))
			}
			errMsg, _ := recoveryResults[0].Response["error"].(string)
			if !strings.Contains(errMsg, "Do not call it again") {
				t.Fatalf("recovery error message = %q, want explicit loop-break guidance", errMsg)
			}
			if !strings.Contains(errMsg, tt.targetPhrase) {
				t.Fatalf("recovery error message = %q, want target phrase %q", errMsg, tt.targetPhrase)
			}
		})
	}
}

func TestExecutorExecuteLoop_KimiInjectsWorkingMemoryAfterExploration(t *testing.T) {
	registry := NewRegistry()
	tool := &scriptedStaticTool{
		name:    "grep",
		content: "internal/tools/executor.go:123: loop guard\ninternal/tools/tool.go:44: loop guard\n",
	}
	if err := registry.Register(tool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	cl := &scriptedExecutorClient{
		model: "kimi-for-coding",
		responses: []*client.StreamingResponse{
			buildExecutorTestGrepStream("g0"),
			buildExecutorTestTextStream("Summarized the findings."),
		},
	}

	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false

	_, finalText, err := exec.Execute(context.Background(), nil, "search the project")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if finalText != "Summarized the findings." {
		t.Fatalf("finalText = %q, want %q", finalText, "Summarized the findings.")
	}
	if len(cl.functionHistory) != 1 {
		t.Fatalf("function history count = %d, want 1", len(cl.functionHistory))
	}

	if len(cl.functionResults) != 1 || len(cl.functionResults[0]) != 1 {
		t.Fatalf("function results = %+v, want one result batch", cl.functionResults)
	}
	resultContent, _ := cl.functionResults[0][0].Response["content"].(string)
	if !strings.Contains(resultContent, "[Kimi working memory]") {
		t.Fatalf("result content = %q, want Kimi working memory scaffold", resultContent)
	}
	if !strings.Contains(resultContent, `grep "loop guard" in internal/tools ->`) {
		t.Fatalf("result content = %q, want summarized grep target", resultContent)
	}
	if !strings.Contains(resultContent, "reuse these results") {
		t.Fatalf("result content = %q, want reuse guidance", resultContent)
	}

	historyText := strings.Join(cl.functionHistory[0], "\n")
	if strings.Contains(historyText, "[Kimi working memory]") {
		t.Fatalf("history = %q, notification must not be inserted before tool results", historyText)
	}
}

func TestExecutorExecuteLoop_NonKimiSkipsWorkingMemoryScaffold(t *testing.T) {
	registry := NewRegistry()
	tool := &scriptedStaticTool{
		name:    "grep",
		content: "internal/tools/executor.go:123: loop guard\n",
	}
	if err := registry.Register(tool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	cl := &scriptedExecutorClient{
		model: "glm-4.7",
		responses: []*client.StreamingResponse{
			buildExecutorTestGrepStream("g0"),
			buildExecutorTestTextStream("Summarized the findings."),
		},
	}

	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false

	_, _, err := exec.Execute(context.Background(), nil, "search the project")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(cl.functionHistory) != 1 {
		t.Fatalf("function history count = %d, want 1", len(cl.functionHistory))
	}

	historyText := strings.Join(cl.functionHistory[0], "\n")
	if strings.Contains(historyText, "[Kimi working memory]") {
		t.Fatalf("history = %q, want no Kimi scaffold for non-Kimi model", historyText)
	}
}

func cloneFunctionResponses(results []*genai.FunctionResponse) []*genai.FunctionResponse {
	cloned := make([]*genai.FunctionResponse, len(results))
	for i, result := range results {
		if result == nil {
			continue
		}
		respCopy := make(map[string]any, len(result.Response))
		for key, value := range result.Response {
			respCopy[key] = value
		}
		cloned[i] = &genai.FunctionResponse{
			ID:       result.ID,
			Name:     result.Name,
			Response: respCopy,
		}
	}
	return cloned
}

func flattenHistoryTexts(history []*genai.Content) []string {
	var parts []string
	for _, msg := range history {
		if msg == nil {
			continue
		}
		for _, part := range msg.Parts {
			if part != nil && strings.TrimSpace(part.Text) != "" {
				parts = append(parts, part.Text)
			}
		}
	}
	return parts
}

func buildExecutorTestReadStream(id string) *client.StreamingResponse {
	return buildExecutorTestStream(client.ResponseChunk{
		FunctionCalls: []*genai.FunctionCall{{
			ID:   id,
			Name: "read",
			Args: map[string]any{
				"file_path": "project.go",
				"offset":    1.0,
				"limit":     100.0,
			},
		}},
		Done:         true,
		FinishReason: genai.FinishReasonStop,
	})
}

func buildExecutorTestGrepStream(id string) *client.StreamingResponse {
	return buildExecutorTestStream(client.ResponseChunk{
		FunctionCalls: []*genai.FunctionCall{{
			ID:   id,
			Name: "grep",
			Args: map[string]any{
				"pattern": "loop guard",
				"path":    "internal/tools",
			},
		}},
		Done:         true,
		FinishReason: genai.FinishReasonStop,
	})
}

func buildExecutorTestGlobStream(id string) *client.StreamingResponse {
	return buildExecutorTestStream(client.ResponseChunk{
		FunctionCalls: []*genai.FunctionCall{{
			ID:   id,
			Name: "glob",
			Args: map[string]any{
				"pattern": "**/*.go",
				"path":    "internal/tools",
			},
		}},
		Done:         true,
		FinishReason: genai.FinishReasonStop,
	})
}

func buildExecutorTestTextStream(text string) *client.StreamingResponse {
	return buildExecutorTestStream(client.ResponseChunk{
		Text:         text,
		Done:         true,
		FinishReason: genai.FinishReasonStop,
	})
}

func buildExecutorTestEmptyStream() *client.StreamingResponse {
	return buildExecutorTestStream(client.ResponseChunk{
		Done:         true,
		FinishReason: genai.FinishReasonStop,
	})
}

func buildExecutorTestStream(chunks ...client.ResponseChunk) *client.StreamingResponse {
	ch := make(chan client.ResponseChunk, len(chunks))
	done := make(chan struct{})
	for _, chunk := range chunks {
		ch <- chunk
	}
	close(ch)
	close(done)
	return &client.StreamingResponse{Chunks: ch, Done: done}
}
