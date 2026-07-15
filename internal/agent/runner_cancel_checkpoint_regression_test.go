package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

type cancellationTestClient struct {
	*testkit.MockClient
}

func (c *cancellationTestClient) WithModel(string) client.Client { return c }

func TestSpawnWithContextDoesNotCheckpointTerminalContextErrors(t *testing.T) {
	tests := []struct {
		name       string
		context    func() (context.Context, context.CancelFunc)
		wantErr    error
		wantStatus AgentStatus
	}{
		{
			name: "cancelled",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			wantErr:    context.Canceled,
			wantStatus: AgentStatusCancelled,
		},
		{
			name: "deadline exceeded",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			wantErr:    context.DeadlineExceeded,
			wantStatus: AgentStatusFailed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewAgentStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			runner := NewRunner(context.Background(), testkit.NewMockClient(), tools.NewRegistry(), t.TempDir())
			runner.SetStore(store)

			ctx, cancel := tc.context()
			defer cancel()
			_, result, runErr := runner.SpawnWithContext(ctx, "general", "inspect the project", 3, "", "", nil, false, nil)
			if !errors.Is(runErr, tc.wantErr) {
				t.Fatalf("SpawnWithContext error=%v, want %v", runErr, tc.wantErr)
			}
			if result == nil || result.Status != tc.wantStatus {
				t.Fatalf("result=%+v, want status %s", result, tc.wantStatus)
			}
			assertNoAutoResumableErrorCheckpoints(t, store)
		})
	}
}

func TestAgentErrorCheckpointEligibility(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "cancelled", err: context.Canceled, want: false},
		{name: "wrapped cancellation", err: fmt.Errorf("model round: %w", context.Canceled), want: false},
		{name: "deadline", err: context.DeadlineExceeded, want: false},
		{name: "wrapped deadline", err: fmt.Errorf("agent timeout: %w", context.DeadlineExceeded), want: false},
		{name: "recoverable failure", err: errors.New("provider connection reset"), want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldPersistAgentErrorCheckpoint(tc.err); got != tc.want {
				t.Fatalf("shouldPersistAgentErrorCheckpoint(%v)=%v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestSpawnAsyncCancellationDoesNotCreateAutoResumeCheckpoint(t *testing.T) {
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mock := &cancellationTestClient{MockClient: testkit.NewMockClient()}
	mock.EnqueueScript(testkit.ResponseScript{
		DelayBeforeFirstChunk: time.Minute,
		Chunks: []client.ResponseChunk{
			{Text: "should never arrive"},
		},
	})
	started := make(chan struct{})
	var startedOnce sync.Once
	mock.OnSend = func(context.Context) {
		startedOnce.Do(func() { close(started) })
	}

	runner := NewRunner(context.Background(), mock, tools.NewRegistry(), t.TempDir())
	runner.SetStore(store)
	completed := make(chan *AgentResult, 1)
	runner.SetOnAgentComplete(func(_ string, result *AgentResult) {
		completed <- cloneAgentResult(result)
	})

	agentID := runner.SpawnAsync(context.Background(), "general", "inspect the project", 3, "")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("agent did not enter the model round")
	}
	if err := runner.Cancel(agentID); err != nil {
		t.Fatal(err)
	}

	var result *AgentResult
	select {
	case result = <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled agent did not finish cleanup")
	}
	if result.Status != AgentStatusCancelled || !result.Completed || result.Error != context.Canceled.Error() {
		t.Fatalf("cancelled result=%+v", result)
	}
	assertNoAutoResumableErrorCheckpoints(t, store)
}

func assertNoAutoResumableErrorCheckpoints(t *testing.T, store *AgentStore) {
	t.Helper()
	checkpoints, err := store.ListErrorCheckpoints()
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 0 {
		t.Fatalf("terminal cancellation left %d auto-resumable error checkpoint(s): %+v", len(checkpoints), checkpoints)
	}
}
