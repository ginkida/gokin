package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestResumeAsyncPublishesCompletionAfterUsageAccounting(t *testing.T) {
	mock := testkit.NewMockClient()
	mock.EnqueueText("completed")
	runner, _ := newRunLeaseFixture(t, &runLeaseTestClient{MockClient: mock}, "resume-accounting-agent")

	accountingStarted := make(chan struct{})
	accountingRelease := make(chan struct{})
	var once sync.Once
	runner.SetOnAgentUsage(func(string, *AgentResult) {
		once.Do(func() { close(accountingStarted) })
		<-accountingRelease
	})

	agentID, err := runner.ResumeAsync(context.Background(), "resume-accounting-agent", "continue")
	if err != nil {
		t.Fatalf("ResumeAsync: %v", err)
	}
	select {
	case <-accountingStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for usage accounting")
	}

	waitDone := make(chan error, 1)
	go func() {
		_, waitErr := runner.WaitWithTimeout(agentID, 2*time.Second)
		waitDone <- waitErr
	}()
	select {
	case waitErr := <-waitDone:
		t.Fatalf("completion became visible before usage accounting finished: %v", waitErr)
	case <-time.After(50 * time.Millisecond):
	}

	close(accountingRelease)
	select {
	case waitErr := <-waitDone:
		if waitErr != nil {
			t.Fatalf("WaitWithTimeout after accounting: %v", waitErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completion was not published after usage accounting")
	}
	waitForRunLeaseRelease(t, runner, agentID)
}

func TestResumeAsyncPanicCompletesDurableLifecycle(t *testing.T) {
	const agentID = "resume-panic-agent"
	mock := testkit.NewMockClient()
	runner, store := newRunLeaseFixture(t, &panickingClient{MockClient: mock}, agentID)

	completed := make(chan *AgentResult, 1)
	runner.SetOnAgentComplete(func(_ string, result *AgentResult) {
		completed <- result
	})

	gotID, err := runner.ResumeAsync(context.Background(), agentID, "continue")
	if err != nil {
		t.Fatalf("ResumeAsync: %v", err)
	}
	result, err := runner.WaitWithTimeout(gotID, 2*time.Second)
	if err != nil {
		t.Fatalf("WaitWithTimeout: %v", err)
	}
	if result.Status != AgentStatusFailed || !strings.Contains(result.Error, "panic") {
		t.Fatalf("panic result = status %s error %q", result.Status, result.Error)
	}

	select {
	case callbackResult := <-completed:
		if callbackResult.Status != AgentStatusFailed {
			t.Fatalf("completion callback status = %s, want failed", callbackResult.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("panic recovery did not invoke completion callback")
	}

	agent, ok := runner.GetAgent(agentID)
	if !ok {
		t.Fatal("resumed agent is not tracked")
	}
	if status := agent.GetStatus(); status != AgentStatusFailed {
		t.Fatalf("agent status after panic = %s, want failed", status)
	}
	persisted, err := store.Load(agentID)
	if err != nil {
		t.Fatalf("Load persisted agent state: %v", err)
	}
	if persisted.Status != AgentStatusFailed || persisted.EndTime.IsZero() {
		t.Fatalf("persisted panic lifecycle = status %s end %v", persisted.Status, persisted.EndTime)
	}
	waitForRunLeaseRelease(t, runner, agentID)
}

func TestResumeAsyncCancellationPublishesCancelledStatus(t *testing.T) {
	const agentID = "resume-cancel-agent"
	client, started, release := newBlockingRunLeaseClient(1)
	defer close(release)
	runner, _ := newRunLeaseFixture(t, client, agentID)

	gotID, err := runner.ResumeAsync(context.Background(), agentID, "continue")
	if err != nil {
		t.Fatalf("ResumeAsync: %v", err)
	}
	waitForRunLeaseStart(t, started)
	if err := runner.Cancel(gotID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	result, err := runner.WaitWithTimeout(gotID, 2*time.Second)
	if err != nil {
		t.Fatalf("WaitWithTimeout: %v", err)
	}
	if result.Status != AgentStatusCancelled {
		t.Fatalf("status after explicit resume cancellation = %s, want cancelled (error %q)", result.Status, result.Error)
	}
}

func TestDetachedResumeRunContextHonorsAgentTimeout(t *testing.T) {
	agent := &Agent{timeout: 20 * time.Millisecond}
	ctx, cancel := detachedResumeRunContext(context.Background(), agent)
	defer cancel()

	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("detached resume context error = %v, want deadline exceeded", ctx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("detached resume context ignored agent timeout")
	}
}

func TestResumeLastCheckpointUsesTimestampAcrossAgentIDs(t *testing.T) {
	client, started, release := newBlockingRunLeaseClient(1)
	var releaseOnce sync.Once
	releaseRun := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseRun)
	runner, store := newRunLeaseFixture(t, client, "unused-state-agent")
	now := time.Now()

	old := &AgentCheckpoint{
		AgentState:   &AgentState{ID: "z-agent-old", Type: AgentTypeGeneral, MaxTurns: 3},
		Timestamp:    now.Add(-time.Hour),
		CheckpointID: "z-agent-old-999999999999",
	}
	newest := &AgentCheckpoint{
		AgentState:   &AgentState{ID: "a-agent-new", Type: AgentTypeGeneral, MaxTurns: 3},
		Timestamp:    now,
		CheckpointID: "a-agent-new-1",
	}
	if err := store.SaveCheckpoint(old); err != nil {
		t.Fatalf("SaveCheckpoint(old): %v", err)
	}
	if err := store.SaveCheckpoint(newest); err != nil {
		t.Fatalf("SaveCheckpoint(newest): %v", err)
	}

	gotID, err := runner.ResumeLastCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("ResumeLastCheckpoint: %v", err)
	}
	if gotID != newest.AgentState.ID {
		t.Fatalf("resumed agent = %q, want newest-by-timestamp %q", gotID, newest.AgentState.ID)
	}
	waitForRunLeaseStart(t, started)
	releaseRun()
	if _, err := runner.WaitWithTimeout(gotID, 2*time.Second); err != nil {
		t.Fatalf("WaitWithTimeout: %v", err)
	}
	waitForRunLeaseRelease(t, runner, gotID)
}

func TestResumeLastCheckpointRetiresSameAgentReplayAcrossStores(t *testing.T) {
	const agentID = "cross-store-replay-agent"
	baseDir := t.TempDir()
	firstStore, err := NewAgentStore(baseDir)
	if err != nil {
		t.Fatalf("NewAgentStore(first): %v", err)
	}
	secondStore, err := NewAgentStore(baseDir)
	if err != nil {
		t.Fatalf("NewAgentStore(second): %v", err)
	}

	client, started, release := newBlockingRunLeaseClient(2)
	var releaseOnce sync.Once
	releaseRun := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseRun)
	first := NewRunner(context.Background(), client, tools.NewRegistry(), t.TempDir())
	first.SetStore(firstStore)
	second := NewRunner(context.Background(), client, tools.NewRegistry(), t.TempDir())
	second.SetStore(secondStore)

	now := time.Now()
	for _, cp := range []*AgentCheckpoint{
		{
			AgentState:   &AgentState{ID: agentID, Type: AgentTypeGeneral, MaxTurns: 3},
			Timestamp:    now.Add(-time.Minute),
			CheckpointID: agentID + "-older",
		},
		{
			AgentState:   &AgentState{ID: agentID, Type: AgentTypeGeneral, MaxTurns: 3},
			Timestamp:    now,
			CheckpointID: agentID + "-newer",
		},
	} {
		if err := firstStore.SaveCheckpoint(cp); err != nil {
			t.Fatalf("SaveCheckpoint(%s): %v", cp.CheckpointID, err)
		}
	}

	firstID, err := first.ResumeLastCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("first ResumeLastCheckpoint: %v", err)
	}
	waitForRunLeaseStart(t, started)
	secondID, secondErr := second.ResumeLastCheckpoint(context.Background())
	if secondErr == nil {
		releaseRun()
		t.Fatalf("second independent store replayed superseded checkpoint as %q", secondID)
	}
	if calls := len(client.Calls()); calls != 1 {
		releaseRun()
		t.Fatalf("model calls after cross-store replay attempt = %d, want 1", calls)
	}
	releaseRun()
	if _, err := first.WaitWithTimeout(firstID, 2*time.Second); err != nil {
		t.Fatalf("WaitWithTimeout(first): %v", err)
	}
	waitForRunLeaseRelease(t, first, firstID)
}

func TestDurableRunLeaseRejectsConcurrentResumeAcrossStores(t *testing.T) {
	const agentID = "cross-store-state-agent"
	baseDir := t.TempDir()
	firstStore, err := NewAgentStore(baseDir)
	if err != nil {
		t.Fatalf("NewAgentStore(first): %v", err)
	}
	secondStore, err := NewAgentStore(baseDir)
	if err != nil {
		t.Fatalf("NewAgentStore(second): %v", err)
	}
	if err := firstStore.SaveState(&AgentState{
		ID:       agentID,
		Type:     AgentTypeGeneral,
		Status:   AgentStatusCompleted,
		MaxTurns: 3,
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	client, started, release := newBlockingRunLeaseClient(2)
	var releaseOnce sync.Once
	releaseRun := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseRun)
	first := NewRunner(context.Background(), client, tools.NewRegistry(), t.TempDir())
	first.SetStore(firstStore)
	second := NewRunner(context.Background(), client, tools.NewRegistry(), t.TempDir())
	second.SetStore(secondStore)

	firstID, err := first.ResumeAsync(context.Background(), agentID, "first continuation")
	if err != nil {
		t.Fatalf("first ResumeAsync: %v", err)
	}
	waitForRunLeaseStart(t, started)
	if _, err := second.ResumeAsync(context.Background(), agentID, "duplicate continuation"); !errors.Is(err, ErrAgentRunInProgress) {
		releaseRun()
		t.Fatalf("concurrent cross-store ResumeAsync error = %v, want ErrAgentRunInProgress", err)
	}
	if calls := len(client.Calls()); calls != 1 {
		releaseRun()
		t.Fatalf("model calls during durable lease = %d, want 1", calls)
	}

	releaseRun()
	if _, err := first.WaitWithTimeout(firstID, 2*time.Second); err != nil {
		t.Fatalf("WaitWithTimeout(first): %v", err)
	}
	waitForRunLeaseRelease(t, first, firstID)

	secondID, err := second.ResumeAsync(context.Background(), agentID, "later continuation")
	if err != nil {
		t.Fatalf("sequential cross-store ResumeAsync after release: %v", err)
	}
	waitForRunLeaseStart(t, started)
	if _, err := second.WaitWithTimeout(secondID, 2*time.Second); err != nil {
		t.Fatalf("WaitWithTimeout(second): %v", err)
	}
	waitForRunLeaseRelease(t, second, secondID)
	if calls := len(client.Calls()); calls != 2 {
		t.Fatalf("model calls after rejected duplicate and sequential resume = %d, want 2", calls)
	}
}

func TestCheckpointRecoveryFailsClosedWhenAnyCheckpointIsCorrupt(t *testing.T) {
	client, _, release := newBlockingRunLeaseClient(1)
	close(release)
	runner, store := newRunLeaseFixture(t, client, "corrupt-checkpoint-agent")
	valid := &AgentCheckpoint{
		AgentState: &AgentState{
			ID:       "corrupt-checkpoint-agent",
			Type:     AgentTypeGeneral,
			Status:   AgentStatusFailed,
			MaxTurns: 3,
		},
		Timestamp:     time.Now().Add(-time.Minute),
		CheckpointID:  "corrupt-checkpoint-agent-valid",
		TriggerReason: "error",
	}
	if err := store.SaveCheckpoint(valid); err != nil {
		t.Fatalf("SaveCheckpoint(valid): %v", err)
	}
	checkpointDir := filepath.Join(store.dir, "checkpoints")
	if err := os.WriteFile(filepath.Join(checkpointDir, "corrupt-checkpoint-agent-newer.json"), []byte("{"), 0600); err != nil {
		t.Fatalf("write corrupt checkpoint: %v", err)
	}

	if resumed := runner.ResumeErrorCheckpoints(context.Background()); resumed != 0 {
		t.Fatalf("automatic recovery with corrupt checkpoint resumed %d agents, want 0", resumed)
	}
	if _, err := runner.ResumeLastCheckpoint(context.Background()); err == nil {
		t.Fatal("manual recovery fell back to an older checkpoint despite unreadable recovery state")
	}
	if calls := len(client.Calls()); calls != 0 {
		t.Fatalf("model calls after fail-closed corrupt recovery = %d, want 0", calls)
	}
	if _, err := store.LoadCheckpoint(valid.CheckpointID); err != nil {
		t.Fatalf("valid older checkpoint was consumed despite corrupt peer: %v", err)
	}
}
