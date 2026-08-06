package agent

import (
	"context"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestSpawnAsyncRejectsPreCancelledButSurvivesPostAcceptanceCancel(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "plain"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			mock := testkit.NewMockClient()
			mock.EnqueueScript(testkit.ResponseScript{
				Chunks:                []client.ResponseChunk{{Text: "completed", Done: true}},
				DelayBeforeFirstChunk: 75 * time.Millisecond,
			})
			runner := NewRunner(context.Background(), mock, tools.NewRegistry(), t.TempDir())

			preCancelled, preCancel := context.WithCancel(context.Background())
			preCancel()
			var rejected string
			if streaming {
				rejected = runner.SpawnAsyncWithStreaming(preCancelled, "general", "reject", 1, "", func(string) {}, nil)
			} else {
				rejected = runner.SpawnAsync(preCancelled, "general", "reject", 1, "")
			}
			if rejected != "" {
				t.Fatalf("pre-cancelled spawn returned id %q", rejected)
			}

			parent, cancelParent := context.WithCancel(context.Background())
			var id string
			if streaming {
				id = runner.SpawnAsyncWithStreaming(parent, "general", "finish", 1, "", func(string) {}, nil)
			} else {
				id = runner.SpawnAsync(parent, "general", "finish", 1, "")
			}
			if id == "" {
				t.Fatal("accepted spawn returned empty id")
			}
			cancelParent() // Mirrors repl_exec's tool context closing after Future return.

			waitCtx, waitCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer waitCancel()
			result, err := runner.WaitWithContext(waitCtx, id)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status == AgentStatusCancelled || result.Error == context.Canceled.Error() {
				t.Fatalf("accepted background agent inherited caller cleanup: %+v", result)
			}
			if !result.Completed {
				t.Fatalf("background result not finalized: %+v", result)
			}
		})
	}
}
