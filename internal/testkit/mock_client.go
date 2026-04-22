package testkit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"gokin/internal/client"

	"google.golang.org/genai"
)

// MockClient is a thread-safe test double for client.Client.
//
// Configure behavior by queuing scripts (EnqueueText, EnqueueToolCall,
// EnqueueError, EnqueueStartupError, EnqueueOverload, EnqueueScript) before
// invoking Send* methods. After the test, inspect recorded invocations
// through Calls().
//
// Scripts are consumed FIFO. If all scripts are exhausted, Send* returns
// ErrNoScript — tests should enqueue enough to cover every expected call.
type MockClient struct {
	mu sync.Mutex

	model       string
	tools       []*genai.Tool
	systemInstr string
	thinkBudget int32
	rawClient   any
	rateLimiter any

	scripts []ResponseScript
	cursor  int
	calls   []RecordedCall
	clones  []*MockClient

	// OnSend, if non-nil, is invoked on every Send* call after the call is
	// recorded but before the script is returned. Useful for asserting
	// context state or injecting timing into tests.
	OnSend func(ctx context.Context)
}

// ResponseScript describes a scripted response returned from one Send* call.
//
// Exactly one of StartupError or Chunks should be populated. If StartupError
// is set, Send* returns it synchronously (simulating an error before any
// streaming begins, e.g., a 401 on request build). Otherwise, Chunks are
// emitted one at a time on the StreamingResponse.Chunks channel.
type ResponseScript struct {
	Chunks                []client.ResponseChunk
	StartupError          error
	DelayBeforeFirstChunk time.Duration
}

// RecordedCall captures a single invocation of a Send* method for later
// assertion. Fields not applicable to the method (e.g., Responses on
// SendMessage) are left zero.
type RecordedCall struct {
	Method     string
	Message    string
	History    []*genai.Content
	Responses  []*genai.FunctionResponse
	Model      string
	ToolsCount int
	At         time.Time
}

// ErrNoScript is returned when a Send* method is called but no more scripts
// are queued. Tests should enqueue one script per expected Send* call.
var ErrNoScript = errors.New("testkit: MockClient has no more scripts queued")

// NewMockClient creates a fresh MockClient with a default model name.
func NewMockClient() *MockClient {
	return &MockClient{model: "mock-model"}
}

// EnqueueText queues a successful response carrying a single text chunk
// followed by a Done terminator.
func (m *MockClient) EnqueueText(text string) *MockClient {
	return m.EnqueueScript(ResponseScript{
		Chunks: []client.ResponseChunk{
			{Text: text},
			{Done: true, FinishReason: genai.FinishReasonStop, InputTokens: 10, OutputTokens: len(text) / 4},
		},
	})
}

// EnqueueToolCall queues a response where the model requests a single
// function call. Tests can chain EnqueueText after this to script the
// follow-up turn.
func (m *MockClient) EnqueueToolCall(name string, args map[string]any) *MockClient {
	call := &genai.FunctionCall{Name: name, Args: args}
	return m.EnqueueScript(ResponseScript{
		Chunks: []client.ResponseChunk{
			{FunctionCalls: []*genai.FunctionCall{call}},
			{Done: true, FinishReason: genai.FinishReasonStop, InputTokens: 10, OutputTokens: 5},
		},
	})
}

// EnqueueError queues a response that starts streaming and then emits an
// error chunk (server-side failure mid-stream). Matches the production
// convention where Send* returns (response, nil) and the error arrives on
// the Chunks channel.
func (m *MockClient) EnqueueError(err error) *MockClient {
	return m.EnqueueScript(ResponseScript{
		Chunks: []client.ResponseChunk{
			{Error: err, Done: true},
		},
	})
}

// EnqueueStartupError queues a synchronous error returned directly from
// Send* (simulates request-build or pre-flight auth failures).
func (m *MockClient) EnqueueStartupError(err error) *MockClient {
	return m.EnqueueScript(ResponseScript{StartupError: err})
}

// EnqueueOverload queues a GLM-1305-style overload error emitted mid-stream.
// Useful for exercising retry, windowed breaker, and failover paths.
func (m *MockClient) EnqueueOverload() *MockClient {
	return m.EnqueueError(fmt.Errorf("z.ai API error (1305): service temporarily overloaded"))
}

// EnqueueScript queues a fully custom ResponseScript.
func (m *MockClient) EnqueueScript(script ResponseScript) *MockClient {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scripts = append(m.scripts, script)
	return m
}

// Calls returns a snapshot of every recorded Send* invocation, in order.
// The returned slice is a copy — safe to inspect without further locking.
func (m *MockClient) Calls() []RecordedCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RecordedCall, len(m.calls))
	copy(out, m.calls)
	return out
}

// Clones returns every MockClient produced via WithModel. Useful for
// verifying failover swaps.
func (m *MockClient) Clones() []*MockClient {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*MockClient, len(m.clones))
	copy(out, m.clones)
	return out
}

// Reset clears scripts, cursor, and recorded calls without touching
// model/tools/etc. Useful between subtests that share a client.
func (m *MockClient) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scripts = nil
	m.cursor = 0
	m.calls = nil
}

// SendMessage implements client.Client.
func (m *MockClient) SendMessage(ctx context.Context, message string) (*client.StreamingResponse, error) {
	return m.dispatch(ctx, RecordedCall{Method: "SendMessage", Message: message})
}

// SendMessageWithHistory implements client.Client.
func (m *MockClient) SendMessageWithHistory(ctx context.Context, history []*genai.Content, message string) (*client.StreamingResponse, error) {
	return m.dispatch(ctx, RecordedCall{
		Method:  "SendMessageWithHistory",
		Message: message,
		History: cloneContents(history),
	})
}

// SendFunctionResponse implements client.Client.
func (m *MockClient) SendFunctionResponse(ctx context.Context, history []*genai.Content, results []*genai.FunctionResponse) (*client.StreamingResponse, error) {
	return m.dispatch(ctx, RecordedCall{
		Method:    "SendFunctionResponse",
		History:   cloneContents(history),
		Responses: cloneResponses(results),
	})
}

func (m *MockClient) dispatch(ctx context.Context, call RecordedCall) (*client.StreamingResponse, error) {
	m.mu.Lock()
	call.Model = m.model
	call.ToolsCount = len(m.tools)
	call.At = time.Now()
	m.calls = append(m.calls, call)

	script, ok := m.popScriptLocked()
	onSend := m.OnSend
	m.mu.Unlock()

	if onSend != nil {
		onSend(ctx)
	}

	if !ok {
		return nil, ErrNoScript
	}
	if script.StartupError != nil {
		return nil, script.StartupError
	}
	return runScript(ctx, script), nil
}

func (m *MockClient) popScriptLocked() (ResponseScript, bool) {
	if m.cursor >= len(m.scripts) {
		return ResponseScript{}, false
	}
	script := m.scripts[m.cursor]
	m.cursor++
	return script, true
}

func runScript(ctx context.Context, script ResponseScript) *client.StreamingResponse {
	buf := len(script.Chunks) + 1
	chunks := make(chan client.ResponseChunk, buf)
	done := make(chan struct{})

	go func() {
		defer close(chunks)
		defer close(done)

		if script.DelayBeforeFirstChunk > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(script.DelayBeforeFirstChunk):
			}
		}

		for _, c := range script.Chunks {
			select {
			case <-ctx.Done():
				return
			case chunks <- c:
			}
		}
	}()

	return &client.StreamingResponse{Chunks: chunks, Done: done}
}

// SetTools implements client.Client.
func (m *MockClient) SetTools(tools []*genai.Tool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tools = tools
}

// SetRateLimiter implements client.Client.
func (m *MockClient) SetRateLimiter(limiter any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rateLimiter = limiter
}

// CountTokens implements client.Client. Returns a deterministic estimate
// (≈4 chars per token) so tests don't need a real tokenizer.
func (m *MockClient) CountTokens(ctx context.Context, contents []*genai.Content) (*genai.CountTokensResponse, error) {
	var total int32
	for _, c := range contents {
		for _, p := range c.Parts {
			total += int32(len(p.Text) / 4)
		}
	}
	return &genai.CountTokensResponse{TotalTokens: total}, nil
}

// GetModel implements client.Client.
func (m *MockClient) GetModel() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.model
}

// SetModel implements client.Client.
func (m *MockClient) SetModel(modelName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.model = modelName
}

// WithModel implements client.Client. Returns a clone that shares no script
// queue with the parent — tests that exercise failover should enqueue
// scripts on the clone explicitly.
func (m *MockClient) WithModel(modelName string) client.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	clone := &MockClient{model: modelName}
	m.clones = append(m.clones, clone)
	return clone
}

// GetRawClient implements client.Client.
func (m *MockClient) GetRawClient() any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rawClient
}

// SetSystemInstruction implements client.Client.
func (m *MockClient) SetSystemInstruction(instruction string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.systemInstr = instruction
}

// SetThinkingBudget implements client.Client.
func (m *MockClient) SetThinkingBudget(budget int32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.thinkBudget = budget
}

// SystemInstruction returns the last value set via SetSystemInstruction.
// Not part of Client interface — test helper.
func (m *MockClient) SystemInstruction() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.systemInstr
}

// ThinkingBudget returns the last value set via SetThinkingBudget. Not part
// of Client interface — test helper.
func (m *MockClient) ThinkingBudget() int32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.thinkBudget
}

// Close implements client.Client. Always succeeds.
func (m *MockClient) Close() error {
	return nil
}

func cloneContents(in []*genai.Content) []*genai.Content {
	if in == nil {
		return nil
	}
	out := make([]*genai.Content, len(in))
	copy(out, in)
	return out
}

func cloneResponses(in []*genai.FunctionResponse) []*genai.FunctionResponse {
	if in == nil {
		return nil
	}
	out := make([]*genai.FunctionResponse, len(in))
	copy(out, in)
	return out
}

// Compile-time check that MockClient satisfies client.Client.
var _ client.Client = (*MockClient)(nil)
