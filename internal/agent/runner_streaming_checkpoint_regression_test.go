package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

type streamingCheckpointTestClient struct {
	*testkit.MockClient
}

func TestSpawnAsyncWithStreamingCancellationIsTerminalAndNotCheckpointed(t *testing.T) {
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mock := &streamingCheckpointTestClient{MockClient: testkit.NewMockClient()}
	mock.EnqueueScript(testkit.ResponseScript{
		DelayBeforeFirstChunk: time.Minute,
		Chunks:                []client.ResponseChunk{{Text: "too late"}},
	})
	started := make(chan struct{})
	var once sync.Once
	mock.OnSend = func(context.Context) { once.Do(func() { close(started) }) }

	runner := NewRunner(context.Background(), mock, tools.NewRegistry(), t.TempDir())
	runner.SetStore(store)
	completed := make(chan *AgentResult, 1)
	runner.SetOnAgentComplete(func(_ string, result *AgentResult) {
		completed <- cloneAgentResult(result)
	})
	agentID := runner.SpawnAsyncWithStreaming(
		context.Background(), "general", "inspect the project", 3, "", func(string) {}, nil,
	)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("streaming agent did not start")
	}
	if err := runner.Cancel(agentID); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-completed:
		if result.Status != AgentStatusCancelled || !result.Completed || result.Error != context.Canceled.Error() {
			t.Fatalf("cancelled streaming result=%+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled streaming agent did not complete")
	}
	checkpoints, err := store.ListErrorCheckpoints()
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 0 {
		t.Fatalf("cancelled streaming agent left %d resumable checkpoint(s)", len(checkpoints))
	}
}

func (c *streamingCheckpointTestClient) WithModel(string) client.Client { return c }

// A background task takes the streaming spawn path whenever the parent has a
// text/progress callback. That path must have the same crash-recovery contract
// as SpawnAsync: a genuine provider/execution failure leaves one error
// checkpoint that startup recovery can resume.
func TestSpawnAsyncWithStreamingPersistsRecoverableFailureCheckpoint(t *testing.T) {
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	mock := &streamingCheckpointTestClient{MockClient: testkit.NewMockClient()}
	// The agent may retry a transient round failure; keep every attempt
	// deterministically failing so the run reaches its terminal error path.
	for range 8 {
		mock.EnqueueStartupError(errors.New("provider connection reset"))
	}

	runner := NewRunner(context.Background(), mock, tools.NewRegistry(), t.TempDir())
	runner.SetStore(store)
	completed := make(chan *AgentResult, 1)
	runner.SetOnAgentComplete(func(_ string, result *AgentResult) {
		completed <- cloneAgentResult(result)
	})

	agentID := runner.SpawnAsyncWithStreaming(
		context.Background(), "general", "inspect the project", 3, "", func(string) {}, nil,
	)

	select {
	case result := <-completed:
		if result.Status != AgentStatusFailed || !result.Completed {
			t.Fatalf("failed streaming result=%+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("streaming agent did not complete")
	}

	checkpoints, err := store.ListErrorCheckpoints()
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 1 {
		t.Fatalf("streaming failure left %d error checkpoint(s), want 1", len(checkpoints))
	}
	if checkpoints[0].AgentState == nil || checkpoints[0].AgentState.ID != agentID {
		t.Fatalf("checkpoint belongs to wrong agent: %+v", checkpoints[0])
	}
}
