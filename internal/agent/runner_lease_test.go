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

// runLeaseTestClient deliberately reuses itself across WithModel so every
// restored agent hits one shared call ledger/barrier.
type runLeaseTestClient struct {
	*testkit.MockClient
}

func (c *runLeaseTestClient) WithModel(string) client.Client { return c }

func newBlockingRunLeaseClient(responses int) (*runLeaseTestClient, <-chan struct{}, chan<- struct{}) {
	mock := testkit.NewMockClient()
	for i := 0; i < responses; i++ {
		mock.EnqueueText("completed")
	}
	started := make(chan struct{}, responses+4)
	release := make(chan struct{})
	mock.OnSend = func(ctx context.Context) {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	return &runLeaseTestClient{MockClient: mock}, started, release
}

func newRunLeaseFixture(t *testing.T, c client.Client, agentID string) (*Runner, *AgentStore) {
	t.Helper()
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewAgentStore: %v", err)
	}
	state := &AgentState{
		ID:       agentID,
		Type:     AgentTypeGeneral,
		Status:   AgentStatusCompleted,
		MaxTurns: 3,
	}
	if err := store.SaveState(state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	runner := NewRunner(context.Background(), c, tools.NewRegistry(), t.TempDir())
	runner.SetStore(store)
	return runner, store
}

func waitForRunLeaseRelease(t *testing.T, runner *Runner, agentID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runner.mu.RLock()
		_, active := runner.activeRuns[agentID]
		runner.mu.RUnlock()
		if !active {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("run lease for %q was not released", agentID)
}

func waitForRunLeaseStart(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent model call")
	}
}

func TestRunLeaseRejectsConcurrentResumeAndReleasesAfterFinalization(t *testing.T) {
	const agentID = "persisted-resume-agent"
	c, started, release := newBlockingRunLeaseClient(2)
	runner, _ := newRunLeaseFixture(t, c, agentID)

	gotID, err := runner.ResumeAsync(context.Background(), agentID, "continue once")
	if err != nil {
		t.Fatalf("first ResumeAsync: %v", err)
	}
	if gotID != agentID {
		t.Fatalf("first resumed ID = %q, want %q", gotID, agentID)
	}
	waitForRunLeaseStart(t, started)

	// Mix the synchronous and asynchronous entry points. Without a shared
	// lease this second call restores the same state and repeats model/tool
	// side effects while the first invocation is still blocked.
	secondDone := make(chan error, 1)
	go func() {
		_, err := runner.Resume(context.Background(), agentID, "duplicate")
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if !errors.Is(err, ErrAgentRunInProgress) {
			t.Fatalf("concurrent Resume error = %v, want ErrAgentRunInProgress", err)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent Resume did not fail closed")
	}
	if calls := len(c.Calls()); calls != 1 {
		t.Fatalf("model calls while one resume owns the lease = %d, want 1", calls)
	}

	close(release)
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := runner.WaitWithContext(waitCtx, agentID); err != nil {
		t.Fatalf("wait for first resume: %v", err)
	}
	waitForRunLeaseRelease(t, runner, agentID)

	// A terminal run releases its lease; sequential continuation remains valid.
	if _, err := runner.ResumeAsync(context.Background(), agentID, "continue later"); err != nil {
		t.Fatalf("sequential ResumeAsync after completion: %v", err)
	}
	waitForRunLeaseStart(t, started)
	waitForRunLeaseRelease(t, runner, agentID)
	if calls := len(c.Calls()); calls != 2 {
		t.Fatalf("model calls after one concurrent rejection and one sequential resume = %d, want 2", calls)
	}
}

func TestSpawnLeaseBlocksCheckpointReplayWhileOriginalRunIsActive(t *testing.T) {
	c, started, release := newBlockingRunLeaseClient(1)
	runner, store := newRunLeaseFixture(t, c, "unused-persisted-agent")

	agentID := runner.SpawnAsync(context.Background(), "general", "perform side effect once", 3, "")
	if agentID == "" {
		t.Fatal("SpawnAsync returned empty agent ID")
	}
	waitForRunLeaseStart(t, started)

	runner.mu.RLock()
	agent := runner.agents[agentID]
	runner.mu.RUnlock()
	if agent == nil {
		t.Fatalf("spawned agent %q was not registered", agentID)
	}
	cp, err := agent.SaveCheckpoint("error")
	if err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	if _, err := runner.ResumeLastCheckpoint(context.Background()); !errors.Is(err, ErrAgentRunInProgress) {
		t.Fatalf("ResumeLastCheckpoint during original run = %v, want ErrAgentRunInProgress", err)
	}
	if resumed := runner.ResumeErrorCheckpoints(context.Background()); resumed != 0 {
		t.Fatalf("ResumeErrorCheckpoints while original run active = %d, want 0", resumed)
	}
	if _, err := store.LoadCheckpoint(cp.CheckpointID); err != nil {
		t.Fatalf("active-run checkpoint was consumed by rejected replay: %v", err)
	}
	if calls := len(c.Calls()); calls != 1 {
		t.Fatalf("model calls after checkpoint replay attempts = %d, want 1", calls)
	}

	close(release)
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := runner.WaitWithContext(waitCtx, agentID); err != nil {
		t.Fatalf("wait for original spawn: %v", err)
	}
	waitForRunLeaseRelease(t, runner, agentID)
}

func TestConcurrentCheckpointMonitorsLaunchAgentExactlyOnce(t *testing.T) {
	const agentID = "checkpoint-monitor-agent"
	c, started, release := newBlockingRunLeaseClient(1)
	runner, store := newRunLeaseFixture(t, c, agentID)
	cp := &AgentCheckpoint{
		AgentState: &AgentState{
			ID:       agentID,
			Type:     AgentTypeGeneral,
			Status:   AgentStatusFailed,
			MaxTurns: 3,
		},
		Timestamp:         time.Now(),
		CheckpointID:      agentID + "-100",
		TriggerReason:     "error",
		ScratchpadContent: "newest recovery state",
	}
	older := &AgentCheckpoint{
		AgentState: &AgentState{
			ID:       agentID,
			Type:     AgentTypeGeneral,
			Status:   AgentStatusFailed,
			MaxTurns: 3,
		},
		Timestamp:         cp.Timestamp.Add(-time.Minute),
		CheckpointID:      agentID + "-050",
		TriggerReason:     "error",
		ScratchpadContent: "stale recovery state",
	}
	if err := store.SaveCheckpoint(older); err != nil {
		t.Fatalf("SaveCheckpoint(older): %v", err)
	}
	if err := store.SaveCheckpoint(cp); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	const monitors = 12
	counts := make(chan int, monitors)
	var wg sync.WaitGroup
	for i := 0; i < monitors; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			counts <- runner.ResumeErrorCheckpoints(context.Background())
		}()
	}
	wg.Wait()
	close(counts)
	total := 0
	for count := range counts {
		total += count
	}
	if total != 1 {
		t.Fatalf("total resumed across %d concurrent monitors = %d, want 1", monitors, total)
	}
	waitForRunLeaseStart(t, started)
	if calls := len(c.Calls()); calls != 1 {
		t.Fatalf("model calls from concurrent checkpoint monitors = %d, want 1", calls)
	}
	runner.mu.RLock()
	restoredAgent := runner.agents[agentID]
	runner.mu.RUnlock()
	if restoredAgent == nil {
		t.Fatal("checkpoint monitor did not register the restored agent")
	}
	restoredAgent.stateMu.RLock()
	scratchpad := restoredAgent.Scratchpad
	restoredAgent.stateMu.RUnlock()
	if scratchpad != cp.ScratchpadContent {
		t.Fatalf("restored scratchpad = %q, want newest %q", scratchpad, cp.ScratchpadContent)
	}
	if _, err := store.LoadCheckpoint(cp.CheckpointID); err == nil {
		t.Fatal("claimed checkpoint still exists and can be replayed")
	}
	if _, err := store.LoadCheckpoint(older.CheckpointID); err == nil {
		t.Fatal("superseded checkpoint still exists and can replay stale state")
	}

	close(release)
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := runner.WaitWithContext(waitCtx, agentID); err != nil {
		t.Fatalf("wait for checkpoint resume: %v", err)
	}
	waitForRunLeaseRelease(t, runner, agentID)
}

func TestCheckpointClaimIsAtomicAcrossRunnersSharingStore(t *testing.T) {
	const agentID = "cross-runner-checkpoint-agent"
	c, started, release := newBlockingRunLeaseClient(1)
	first, store := newRunLeaseFixture(t, c, agentID)
	second := NewRunner(context.Background(), c, tools.NewRegistry(), t.TempDir())
	second.SetStore(store)
	cp := &AgentCheckpoint{
		AgentState: &AgentState{
			ID:       agentID,
			Type:     AgentTypeGeneral,
			Status:   AgentStatusFailed,
			MaxTurns: 3,
		},
		Timestamp:     time.Now(),
		CheckpointID:  agentID + "-atomic",
		TriggerReason: "error",
	}
	if err := store.SaveCheckpoint(cp); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	start := make(chan struct{})
	counts := make(chan int, 2)
	for _, runner := range []*Runner{first, second} {
		go func(r *Runner) {
			<-start
			counts <- r.ResumeErrorCheckpoints(context.Background())
		}(runner)
	}
	close(start)
	total := (<-counts) + (<-counts)
	if total != 1 {
		t.Fatalf("total resumed by two runners sharing one store = %d, want 1", total)
	}
	waitForRunLeaseStart(t, started)
	if calls := len(c.Calls()); calls != 1 {
		t.Fatalf("model calls after cross-runner checkpoint claim = %d, want 1", calls)
	}

	close(release)
	waitForRunLeaseRelease(t, first, agentID)
	waitForRunLeaseRelease(t, second, agentID)
}
