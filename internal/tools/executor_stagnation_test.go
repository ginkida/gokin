package tools

import (
	"context"
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

func TestExecutorExecuteLoop_NonKimiRepeatedReadStillAborts(t *testing.T) {
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
		},
	}

	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false

	_, _, err := exec.Execute(context.Background(), nil, "inspect project.go")
	if err == nil {
		t.Fatal("Execute() error = nil, want stagnation abort")
	}
	if !strings.Contains(err.Error(), "executor stagnation") {
		t.Fatalf("err = %v, want executor stagnation", err)
	}
	if readTool.calls != 4 {
		t.Fatalf("read tool calls = %d, want 4 before abort on the 5th repeat", readTool.calls)
	}
	if len(cl.functionResults) != 4 {
		t.Fatalf("SendFunctionResponse calls = %d, want 4 before stagnation abort", len(cl.functionResults))
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

	historyText := strings.Join(cl.functionHistory[0], "\n")
	if !strings.Contains(historyText, "[Kimi working memory]") {
		t.Fatalf("history = %q, want Kimi working memory scaffold", historyText)
	}
	if !strings.Contains(historyText, `grep "loop guard" in internal/tools ->`) {
		t.Fatalf("history = %q, want summarized grep target", historyText)
	}
	if !strings.Contains(historyText, "reuse these results") {
		t.Fatalf("history = %q, want reuse guidance", historyText)
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
