package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"gokin/internal/tools"
)

// TestGetResultRace_VsCancel (round 5) pins the fix: GetResult used to return
// the shared *AgentResult pointer straight out of r.results after releasing
// r.mu — every external consumer (task.go/task_output.go's
// agentRunnerAdapter, router.go, messenger.go, commands/tasks.go) then reads
// Status/Completed/Error/Output off that pointer with NO lock of its own,
// while Runner.Cancel (task_stop / meta-agent stuck-check / shutdown)
// mutates the SAME struct's fields under r.mu.Lock(). This is the identical
// "unsynchronized flag read" class already fixed for WaitWithContext and
// Coordinator (round 3/4) — GetResult itself, the method actually wired to
// model-triggerable tools (task + task_output + task_stop), was left open.
// Fixed by returning a value-copy snapshot instead of the shared pointer.
func TestGetResultRace_VsCancel(t *testing.T) {
	registry := tools.NewRegistry()
	runner := NewRunner(context.Background(), nil, registry, t.TempDir())

	const agentID = "get-result-race-agent"
	agent := &Agent{ID: agentID, status: AgentStatusRunning}

	runner.mu.Lock()
	runner.agents[agentID] = agent
	runner.results[agentID] = &AgentResult{AgentID: agentID, Status: AgentStatusRunning}
	runner.mu.Unlock()

	stop := make(chan struct{})
	readerDone := make(chan struct{})
	var cancelWG sync.WaitGroup

	cancelWG.Add(1)
	go func() {
		defer cancelWG.Done()
		_ = runner.Cancel(agentID)
	}()

	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
				if result, ok := runner.GetResult(agentID); ok {
					// Read every scalar field, mirroring real consumers
					// (task_output.go's status rendering).
					_ = result.Status
					_ = result.Completed
					_ = result.Error
					_ = result.Output
				}
				time.Sleep(time.Millisecond)
			}
		}
	}()

	cancelWG.Wait()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if result, ok := runner.GetResult(agentID); ok && result.Completed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(stop)
	<-readerDone

	result, ok := runner.GetResult(agentID)
	if !ok {
		t.Fatal("expected a result to still be present after Cancel")
	}
	if !result.Completed {
		t.Fatal("expected Cancel to have marked the result Completed")
	}
	if result.Status != AgentStatusCancelled {
		t.Fatalf("status = %v, want %v", result.Status, AgentStatusCancelled)
	}
}

// TestGetResult_ReturnsIndependentSnapshot is the correctness regression
// check for the value-copy change: the returned struct must NOT alias the
// stored one — mutating the map's entry after a GetResult call must not
// retroactively change the already-returned snapshot's scalar fields.
func TestGetResult_ReturnsIndependentSnapshot(t *testing.T) {
	registry := tools.NewRegistry()
	runner := NewRunner(context.Background(), nil, registry, t.TempDir())

	const agentID = "snapshot-agent"
	runner.mu.Lock()
	runner.results[agentID] = &AgentResult{AgentID: agentID, Status: AgentStatusRunning}
	runner.mu.Unlock()

	snap, ok := runner.GetResult(agentID)
	if !ok {
		t.Fatal("expected a result")
	}
	if snap.Status != AgentStatusRunning {
		t.Fatalf("snapshot status = %v, want Running", snap.Status)
	}

	// Mutate the STORED result in place (simulating a later Cancel/completion).
	runner.mu.Lock()
	runner.results[agentID].Status = AgentStatusCompleted
	runner.results[agentID].Completed = true
	runner.mu.Unlock()

	// The earlier snapshot must be unaffected — it was a value copy at the
	// time of the call, not a live view.
	if snap.Status != AgentStatusRunning {
		t.Fatalf("snapshot mutated after the fact: status = %v, want it to stay Running", snap.Status)
	}
	if snap.Completed {
		t.Fatal("snapshot's Completed flipped after the fact — GetResult is not returning an independent copy")
	}

	// A FRESH call must see the update.
	fresh, ok := runner.GetResult(agentID)
	if !ok || !fresh.Completed || fresh.Status != AgentStatusCompleted {
		t.Fatalf("fresh GetResult = %+v ok=%v, want Completed=true Status=Completed", fresh, ok)
	}
}
