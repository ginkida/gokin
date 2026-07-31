package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

type cancelToolHistoryClient struct {
	*testkit.MockClient
}

func (c *cancelToolHistoryClient) WithModel(string) client.Client { return c }

type cancelToolHistoryWrite struct {
	started chan struct{}
	release chan struct{}
	runs    atomic.Int32
}

func (t *cancelToolHistoryWrite) Name() string        { return "write" }
func (t *cancelToolHistoryWrite) Description() string { return "blocking history commit probe" }
func (t *cancelToolHistoryWrite) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.Name()}
}
func (t *cancelToolHistoryWrite) Validate(map[string]any) error { return nil }
func (t *cancelToolHistoryWrite) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	t.runs.Add(1)
	close(t.started)
	<-t.release
	return tools.NewSuccessResult("write committed"), nil
}

func TestAgentCancellationCommitsToolResponseBeforeResume(t *testing.T) {
	const callID = "call-write-1"
	mock := &cancelToolHistoryClient{MockClient: testkit.NewMockClient()}
	mock.EnqueueScript(testkit.ResponseScript{Chunks: []client.ResponseChunk{
		{FunctionCalls: []*genai.FunctionCall{{
			ID: callID, Name: "write", Args: map[string]any{"file_path": "result.txt"},
		}}},
		{Done: true, FinishReason: genai.FinishReasonStop},
	}})
	// The restored agent must continue from the committed FunctionResponse
	// without executing the write again.
	mock.EnqueueText("resumed safely")

	started := make(chan struct{})
	release := make(chan struct{})
	writeTool := &cancelToolHistoryWrite{started: started, release: release}
	registry := tools.NewRegistry()
	if err := registry.Register(writeTool); err != nil {
		t.Fatal(err)
	}

	agent := NewAgent(AgentTypeGeneral, mock, registry, t.TempDir(), 3, "", nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	type runOutcome struct {
		result *AgentResult
		err    error
	}
	runDone := make(chan runOutcome, 1)
	go func() {
		result, err := agent.Run(ctx, "write the result")
		runDone <- runOutcome{result: result, err: err}
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("agent did not start the write tool")
	}
	cancel()
	close(release)

	var outcome runOutcome
	select {
	case outcome = <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled agent did not finish")
	}
	if !errors.Is(outcome.err, context.Canceled) || outcome.result == nil ||
		outcome.result.Status != AgentStatusCancelled {
		t.Fatalf("cancel outcome = result %+v error %v", outcome.result, outcome.err)
	}

	state := agent.GetState()
	var calls, responses int
	for _, content := range state.History {
		for _, part := range content.Parts {
			if part.FunctionCall != nil && part.FunctionCall.ID == callID {
				calls++
			}
			if part.FunctionResp != nil && part.FunctionResp.ID == callID {
				responses++
				if success, _ := part.FunctionResp.Response["success"].(bool); !success {
					t.Fatalf("persisted tool response = %#v, want success", part.FunctionResp.Response)
				}
			}
		}
	}
	if calls != 1 || responses != 1 {
		t.Fatalf("persisted tool pair calls/responses = %d/%d, want 1/1", calls, responses)
	}

	restored := NewAgent(AgentTypeGeneral, mock, registry, t.TempDir(), 3, "", nil, nil)
	if err := restored.RestoreHistory(state); err != nil {
		t.Fatal(err)
	}
	response, err := restored.getModelResponse(context.Background())
	if err != nil || response == nil || response.Text != "resumed safely" {
		t.Fatalf("resumed response/error = %#v/%v", response, err)
	}
	if writeTool.runs.Load() != 1 {
		t.Fatalf("write executed %d times, want exactly once", writeTool.runs.Load())
	}

	recorded := mock.Calls()
	if len(recorded) != 2 || recorded[1].Method != "SendFunctionResponse" ||
		len(recorded[1].Responses) != 1 || recorded[1].Responses[0].ID != callID {
		t.Fatalf("resume provider calls = %#v", recorded)
	}
}

func TestRestoreHistoryRepairsLegacyOrphanedToolCall(t *testing.T) {
	state := &AgentState{
		ID:     "legacy-agent",
		Type:   AgentTypeGeneral,
		Status: AgentStatusCancelled,
		History: []SerializedContent{
			{Role: string(genai.RoleUser), Parts: []SerializedPart{{
				Type: "text", Text: "original task",
			}}},
			{Role: string(genai.RoleModel), Parts: []SerializedPart{
				{Type: "text", Text: "I will inspect it."},
				{Type: "function_call", FunctionCall: &SerializedFunc{
					ID: "orphan-call", Name: "read", Args: map[string]any{"file_path": "main.go"},
				}},
			}},
		},
	}
	agent := &Agent{invokedSkills: nil}
	if err := agent.RestoreHistory(state); err != nil {
		t.Fatal(err)
	}

	restored := agent.GetState()
	var textFound bool
	for _, content := range restored.History {
		for _, part := range content.Parts {
			if part.FunctionCall != nil || part.FunctionResp != nil {
				t.Fatalf("legacy orphan survived restore: %#v", restored.History)
			}
			if part.Text == "I will inspect it." {
				textFound = true
			}
		}
	}
	if !textFound {
		t.Fatal("repair removed sibling model text along with the orphaned call")
	}
}
