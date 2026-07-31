package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

func TestRunnerCancelAllWaitsThroughTerminalPersistence(t *testing.T) {
	mock := &cancelToolHistoryClient{MockClient: testkit.NewMockClient()}
	mock.EnqueueScript(testkit.ResponseScript{Chunks: []client.ResponseChunk{
		{FunctionCalls: []*genai.FunctionCall{{
			ID: "shutdown-write", Name: "write",
			Args: map[string]any{"file_path": "result.txt"},
		}}},
		{Done: true, FinishReason: genai.FinishReasonStop},
	}})

	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	writeTool := &cancelToolHistoryWrite{started: started, release: release}
	registry := tools.NewRegistry()
	if err := registry.Register(writeTool); err != nil {
		t.Fatal(err)
	}

	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(context.Background(), mock, registry, t.TempDir())
	runner.SetStore(store)
	agentID := runner.SpawnAsync(context.Background(), "general", "write the result", 3, "")

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not reach blocking stateful tool")
	}

	ids := runner.CancelAll()
	if len(ids) != 1 || ids[0] != agentID {
		t.Fatalf("CancelAll IDs = %v, want [%s]", ids, agentID)
	}
	waitDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		waitDone <- runner.WaitAllWithContext(ctx, ids)
	}()

	select {
	case err := <-waitDone:
		t.Fatalf("wait returned before stateful tool settled: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("WaitAllWithContext: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown wait did not finish")
	}

	result, ok := runner.GetResult(agentID)
	if !ok || result == nil || !result.Completed || result.Status != AgentStatusCancelled {
		t.Fatalf("terminal result = %+v, ok=%v", result, ok)
	}
	state, err := store.Load(agentID)
	if err != nil {
		t.Fatalf("terminal state was not persisted: %v", err)
	}
	if state.Status != AgentStatusCancelled {
		t.Fatalf("persisted status = %s, want cancelled", state.Status)
	}
}
