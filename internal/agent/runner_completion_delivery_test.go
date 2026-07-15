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

type completionDeliveryClient struct {
	*testkit.MockClient
}

func (c *completionDeliveryClient) WithModel(string) client.Client { return c }

func TestWaitWithContextDoesNotCrossWakeAgentIDs(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	const agentA = "agent-a"
	const agentB = "agent-b"

	runner.mu.Lock()
	runner.results[agentA] = &AgentResult{AgentID: agentA, Status: AgentStatusRunning}
	runner.results[agentB] = &AgentResult{AgentID: agentB, Status: AgentStatusRunning}
	runner.mu.Unlock()

	type waitOutcome struct {
		result *AgentResult
		err    error
	}
	waitA := make(chan waitOutcome, 1)
	waitB := make(chan waitOutcome, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		result, err := runner.WaitWithContext(ctx, agentA)
		waitA <- waitOutcome{result: result, err: err}
	}()
	go func() {
		result, err := runner.WaitWithContext(ctx, agentB)
		waitB <- waitOutcome{result: result, err: err}
	}()

	waitForResultWaiters(t, runner, map[string]int{agentA: 1, agentB: 1})
	runner.mu.RLock()
	agentBWaiter := runner.resultWaiters[agentB]
	runner.mu.RUnlock()
	publishTestAgentResult(runner, &AgentResult{
		AgentID: agentA, Status: AgentStatusCompleted, Completed: true, Output: "A",
	})

	select {
	case outcome := <-waitA:
		if outcome.err != nil || outcome.result == nil || outcome.result.AgentID != agentA {
			t.Fatalf("agent A wait = result %+v, error %v", outcome.result, outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("agent A did not receive its completion")
	}
	select {
	case <-agentBWaiter.done:
		t.Fatal("agent B's completion channel was closed by agent A")
	default:
	}
	select {
	case outcome := <-waitB:
		t.Fatalf("agent B was cross-woken by agent A: result %+v, error %v", outcome.result, outcome.err)
	default:
	}

	publishTestAgentResult(runner, &AgentResult{
		AgentID: agentB, Status: AgentStatusCompleted, Completed: true, Output: "B",
	})
	select {
	case outcome := <-waitB:
		if outcome.err != nil || outcome.result == nil || outcome.result.AgentID != agentB {
			t.Fatalf("agent B wait = result %+v, error %v", outcome.result, outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("agent B did not receive its completion")
	}
}

func TestWaitWithContextBroadcastsToMultipleWaitersForSameAgent(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	const agentID = "shared-agent"
	const waiterCount = 4

	runner.mu.Lock()
	runner.results[agentID] = &AgentResult{AgentID: agentID, Status: AgentStatusRunning}
	runner.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	results := make(chan *AgentResult, waiterCount)
	errs := make(chan error, waiterCount)
	for range waiterCount {
		go func() {
			result, err := runner.WaitWithContext(ctx, agentID)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}

	waitForResultWaiters(t, runner, map[string]int{agentID: waiterCount})
	publishTestAgentResult(runner, &AgentResult{
		AgentID: agentID, Status: AgentStatusCompleted, Completed: true, Output: "done",
	})

	for i := 0; i < waiterCount; i++ {
		select {
		case err := <-errs:
			t.Fatalf("waiter %d failed: %v", i, err)
		case result := <-results:
			if result == nil || result.AgentID != agentID || result.Output != "done" {
				t.Fatalf("waiter %d result = %+v", i, result)
			}
		case <-time.After(time.Second):
			t.Fatalf("waiter %d did not receive broadcast completion", i)
		}
	}
}

func TestWaitWithContextObservesCompletionBeforeWait(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	const agentID = "already-complete"
	publishTestAgentResult(runner, &AgentResult{
		AgentID: agentID, Status: AgentStatusCompleted, Completed: true, Output: "ready",
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := runner.WaitWithContext(ctx, agentID)
	if err != nil {
		t.Fatalf("WaitWithContext: %v", err)
	}
	if result == nil || result.AgentID != agentID || result.Output != "ready" {
		t.Fatalf("result = %+v", result)
	}

	runner.mu.RLock()
	_, leaked := runner.resultWaiters[agentID]
	runner.mu.RUnlock()
	if leaked {
		t.Fatal("completion-before-wait registered an unnecessary waiter")
	}
}

func TestCleanupOldResultsRetainsPublishedResultUntilRegisteredConsumerTakesIt(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	const agentID = "owned-completion"

	runner.mu.Lock()
	runner.results[agentID] = &AgentResult{AgentID: agentID, Status: AgentStatusRunning}
	runner.mu.Unlock()

	// Register the consumer first, then deliberately split result publication
	// from notification. This is the exact window in which cleanup used to
	// delete the result before notifyResultReady could transfer it to a waiter.
	_, waiter, err := runner.completedResultOrRegisterWaiter(agentID)
	if err != nil {
		t.Fatalf("register waiter: %v", err)
	}

	runner.mu.Lock()
	for i := 0; i < MaxAgentResults+20; i++ {
		id := fmt.Sprintf("decoy-%03d", i)
		runner.results[id] = &AgentResult{
			AgentID: id, Status: AgentStatusCompleted, Completed: true,
		}
	}
	runner.results[agentID] = &AgentResult{
		AgentID: agentID, Status: AgentStatusCompleted, Completed: true, Output: "retained",
	}
	runner.mu.Unlock()

	runner.cleanupOldResults()
	if result, ok := runner.GetResult(agentID); !ok || !result.Completed {
		t.Fatalf("cleanup evicted a completion with a registered consumer: result=%+v ok=%v", result, ok)
	}

	runner.notifyResultReady(agentID)
	runner.cleanupOldResults() // also exercise notification -> consumer scheduling
	result, ok := runner.consumeCompletedResultWaiter(agentID, waiter)
	if !ok || result == nil || result.Output != "retained" {
		t.Fatalf("consume retained completion = %+v ok=%v", result, ok)
	}
}

func TestWaitWithContextConcurrentCompletionAndCleanupOverCapacity(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	const extra = 64
	const timeout = 5 * time.Second
	n := MaxAgentResults + extra

	runner.mu.Lock()
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("agent-%03d", i)
		runner.results[id] = &AgentResult{AgentID: id, Status: AgentStatusRunning}
	}
	runner.mu.Unlock()

	type outcome struct {
		id     string
		result *AgentResult
		err    error
	}
	outcomes := make(chan outcome, n)
	wantedWaiters := make(map[string]int, n)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("agent-%03d", i)
		wantedWaiters[id] = 1
		go func() {
			result, err := runner.WaitWithContext(ctx, id)
			outcomes <- outcome{id: id, result: result, err: err}
		}()
	}
	waitForResultWaiters(t, runner, wantedWaiters)

	// Race more completions than the result cap against repeated eviction.
	// Every waiter must receive its own completion exactly once; cleanup may
	// compact the shared ledger only after that consumer releases ownership.
	stopCleanup := make(chan struct{})
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		for {
			select {
			case <-stopCleanup:
				return
			default:
				runner.cleanupOldResults()
			}
		}
	}()

	var publishers sync.WaitGroup
	publishers.Add(n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("agent-%03d", i)
		go func() {
			defer publishers.Done()
			publishTestAgentResult(runner, &AgentResult{
				AgentID: id, Status: AgentStatusCompleted, Completed: true, Output: id,
			})
		}()
	}
	publishers.Wait()
	close(stopCleanup)
	<-cleanupDone

	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		select {
		case got := <-outcomes:
			if got.err != nil {
				t.Fatalf("WaitWithContext(%s): %v", got.id, got.err)
			}
			if got.result == nil || got.result.AgentID != got.id || got.result.Output != got.id {
				t.Fatalf("WaitWithContext(%s) result = %+v", got.id, got.result)
			}
			if seen[got.id] {
				t.Fatalf("duplicate completion for %s", got.id)
			}
			seen[got.id] = true
		case <-ctx.Done():
			t.Fatalf("received %d/%d completions before timeout: %v", len(seen), n, ctx.Err())
		}
	}

	// Once consumers release their pins, the ordinary cap is enforceable and
	// completion state itself leaves no unbounded waiter ledger behind.
	runner.cleanupOldResults()
	runner.mu.RLock()
	remainingResults := len(runner.results)
	remainingWaiters := len(runner.resultWaiters)
	runner.mu.RUnlock()
	if remainingResults > MaxAgentResults {
		t.Fatalf("results retained after consumption = %d, want <= %d", remainingResults, MaxAgentResults)
	}
	if remainingWaiters != 0 {
		t.Fatalf("result waiter states retained after consumption = %d, want 0", remainingWaiters)
	}
}

func TestWaitWithContextRejectsEvictedOrUnknownAgentWithoutHanging(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	result, err := runner.WaitWithContext(context.Background(), "not-tracked")
	if result != nil {
		t.Fatalf("result = %+v, want nil", result)
	}
	if !errors.Is(err, ErrAgentResultUnavailable) {
		t.Fatalf("error = %v, want ErrAgentResultUnavailable", err)
	}
}

func TestCancelWaitsForRunFinalizationBeforePublishingCompletion(t *testing.T) {
	mock := &completionDeliveryClient{MockClient: testkit.NewMockClient()}
	mock.EnqueueScript(testkit.ResponseScript{
		DelayBeforeFirstChunk: time.Minute,
		Chunks:                []client.ResponseChunk{{Text: "too late"}},
	})
	started := make(chan struct{})
	var startedOnce sync.Once
	mock.OnSend = func(context.Context) { startedOnce.Do(func() { close(started) }) }

	runner := NewRunner(context.Background(), mock, tools.NewRegistry(), t.TempDir())
	finalizing := make(chan struct{})
	releaseFinalization := make(chan struct{})
	runner.SetOnAgentUsage(func(string, *AgentResult) {
		close(finalizing)
		<-releaseFinalization
	})

	agentID := runner.SpawnAsync(context.Background(), "general", "wait for cancellation", 3, "")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("agent did not enter its model round")
	}
	if err := runner.Cancel(agentID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case <-finalizing:
	case <-time.After(time.Second):
		t.Fatal("cancelled run did not reach finalization")
	}

	if result, ok := runner.GetResult(agentID); !ok || result.Completed {
		t.Fatalf("Cancel published completion before finalization: result=%+v ok=%v", result, ok)
	}
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	_, err := runner.WaitWithContext(shortCtx, agentID)
	shortCancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitWithContext before finalization error = %v, want deadline exceeded", err)
	}

	close(releaseFinalization)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := runner.WaitWithContext(ctx, agentID)
	if err != nil {
		t.Fatalf("WaitWithContext after finalization: %v", err)
	}
	if result.Status != AgentStatusCancelled || !result.Completed || result.Error != context.Canceled.Error() {
		t.Fatalf("final cancellation result = %+v", result)
	}
	agent, ok := runner.GetAgent(agentID)
	if !ok || agent.GetStatus() != AgentStatusCancelled {
		t.Fatalf("agent lifecycle status diverged from result: agent=%v ok=%v", agent, ok)
	}
}

func TestAgentCancelBeforeCancelFuncRegistrationIsDelivered(t *testing.T) {
	agent := &Agent{status: AgentStatusPending}
	agent.Cancel()

	ctx, cancel := context.WithCancel(context.Background())
	agent.SetCancelFunc(cancel)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("cancellation requested before run-context registration was lost")
	}

	agent.stateMu.RLock()
	status := agent.status
	endTime := agent.endTime
	agent.stateMu.RUnlock()
	if status != AgentStatusPending || !endTime.IsZero() {
		t.Fatalf("Cancel published terminal lifecycle state early: status=%s end=%v", status, endTime)
	}
}

func publishTestAgentResult(runner *Runner, result *AgentResult) {
	runner.mu.Lock()
	runner.results[result.AgentID] = result
	runner.mu.Unlock()
	runner.notifyResultReady(result.AgentID)
}

func waitForResultWaiters(t *testing.T, runner *Runner, want map[string]int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		runner.mu.RLock()
		matched := true
		for agentID, count := range want {
			waiter := runner.resultWaiters[agentID]
			if waiter == nil || waiter.waiters != count {
				matched = false
				break
			}
		}
		runner.mu.RUnlock()
		if matched {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("waiters were not registered: want %+v", want)
}
