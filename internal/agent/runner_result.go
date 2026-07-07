package agent

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"gokin/internal/logging"
)

// Wait waits for an agent to complete and returns its result.
// Uses a default 10-minute timeout. For context-aware waiting, use WaitWithContext.
func (r *Runner) Wait(agentID string) (*AgentResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return r.WaitWithContext(ctx, agentID)
}

// notifyResultReady signals that an agent result became complete.
// Non-blocking: if the channel already has a pending signal, the send is skipped.
func (r *Runner) notifyResultReady() {
	select {
	case r.resultReady <- struct{}{}:
	default:
	}
}

// WaitWithContext waits for an agent to complete, respecting context cancellation.
func (r *Runner) WaitWithContext(ctx context.Context, agentID string) (*AgentResult, error) {
	// Fast path: check immediately. The `.Completed` read MUST happen while
	// still holding r.mu (not after RUnlock): some writers (the panic-
	// recovery defer in SpawnAsync/SpawnAsyncWithStreaming, and Cancel())
	// mutate the SAME *AgentResult pointer's fields IN PLACE under
	// r.mu.Lock(), rather than publishing a fresh pointer via
	// r.results[agentID]=result. Reading .Completed after releasing the lock
	// races against that in-place mutation (caught by -race: a panicking
	// spawned agent's defer writing .Completed concurrently with a poller
	// reading it unguarded). Checking under the SAME RLock establishes the
	// happens-before edge the flag-read idiom requires.
	if result, ok := r.completedResultLocked(agentID); ok {
		return result, nil
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.resultReady:
			if result, ok := r.completedResultLocked(agentID); ok {
				return result, nil
			}
		}
	}
}

// completedResultLocked returns (result, true) iff agentID has a completed
// result, checking .Completed under r.mu so the read can't race an in-place
// mutator holding the write lock (see WaitWithContext). Also used by
// Coordinator (same package) instead of GetResult()+a raw .Completed check —
// GetResult releases r.mu before returning the pointer, so a bare field read
// afterward would race the SAME writers (SpawnAsync's panic-recovery defer,
// Runner.Cancel — reachable independently of Coordinator's own c.mu via
// task_stop/shutdown) this helper exists to guard against.
func (r *Runner) completedResultLocked(agentID string) (*AgentResult, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result, ok := r.results[agentID]
	if ok && result.Completed {
		return result, true
	}
	return nil, false
}

// WaitWithTimeout waits for an agent to complete with a specific timeout.
func (r *Runner) WaitWithTimeout(agentID string, timeout time.Duration) (*AgentResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return r.WaitWithContext(ctx, agentID)
}

// WaitAll waits for multiple agents to complete.
func (r *Runner) WaitAll(agentIDs []string) ([]*AgentResult, error) {
	results := make([]*AgentResult, len(agentIDs))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for i, id := range agentIDs {
		wg.Add(1)
		go func(idx int, agentID string) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					// Was log-silent: panic captured into result.Error
					// only. Without log entry, there was no signal which
					// agent's Wait faulted. Match the rest of the agent/
					// panic-recovery sites — Warn + stack.
					logging.Warn("panic in WaitAll worker",
						"agent_id", agentID,
						"panic", rec,
						"stack", logging.PanicStack())
					mu.Lock()
					if results[idx] == nil {
						results[idx] = &AgentResult{
							AgentID:   agentID,
							Status:    AgentStatusFailed,
							Error:     fmt.Sprintf("internal panic: %v", rec),
							Completed: true,
						}
					}
					mu.Unlock()
				}
			}()

			result, err := r.Wait(agentID)

			mu.Lock()
			// Ensure result is never nil
			if result == nil {
				result = &AgentResult{
					AgentID:   agentID,
					Status:    AgentStatusFailed,
					Error:     fmt.Sprintf("wait failed: %v", err),
					Completed: true,
				}
			}
			results[idx] = result
			if err != nil && firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
		}(i, id)
	}

	wg.Wait()
	return results, firstErr
}

// GetResult returns a SNAPSHOT (copy) of an agent's result, not the shared
// pointer. External consumers (task.go/task_output.go's agentRunnerAdapter,
// router.go, messenger.go) read Status/Completed/Error/Output off whatever
// this returns with no lock of their own — round 4's completedResultLocked
// fix covers in-package callers that gate on .Completed first, but GetResult
// is the general-purpose accessor used to poll a STILL-RUNNING agent too, so
// there's no flag to gate on. Returning the raw pointer let a concurrent
// Runner.Cancel (task_stop / meta-agent stuck-check / shutdown) mutate the
// SAME struct's fields under r.mu.Lock() while the caller read them
// unguarded — a genuine data race on a currently wired, model-triggerable
// path (task(run_in_background) + task_output(get) + task_stop). AgentResult
// has no lock fields, so a value copy is race-free for the scalar fields;
// Metadata (a map) is intentionally NOT deep-copied here — a residual, lower-
// priority gap, not the race this fix closes.
func (r *Runner) GetResult(agentID string) (*AgentResult, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result, ok := r.results[agentID]
	if !ok {
		return nil, false
	}
	cp := *result
	return &cp, true
}

// GetAgent returns an agent by ID.
func (r *Runner) GetAgent(agentID string) (*Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	agent, ok := r.agents[agentID]
	return agent, ok
}

// Cancel cancels an agent's execution.
func (r *Runner) Cancel(agentID string) error {
	r.mu.Lock()

	agent, ok := r.agents[agentID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("agent not found: %s", agentID)
	}

	agent.Cancel()

	// Update result — must set Completed so WaitWithContext doesn't spin
	completed := false
	if result, ok := r.results[agentID]; ok {
		result.Status = AgentStatusCancelled
		result.Completed = true
		completed = true
	}
	r.mu.Unlock()

	if completed {
		r.notifyResultReady()
	}

	return nil
}

// ListAgents returns all agent IDs.
func (r *Runner) ListAgents() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]string, 0, len(r.agents))
	for id := range r.agents {
		ids = append(ids, id)
	}
	return ids
}

// TaskSummary is a user-facing snapshot of one background agent, built for
// the /tasks command. Output/Error come from the result ledger and may be
// empty while the agent is still running.
type TaskSummary struct {
	ID        string
	Type      string
	Status    AgentStatus
	Task      string
	StartTime time.Time
	Duration  time.Duration
	Output    string
	Error     string
	Completed bool
}

// ListTaskSummaries returns a snapshot of every tracked agent (running and
// completed, until Cleanup evicts them), sorted running-first then by start
// time descending. Lock order r.mu → agent.stateMu matches Cleanup.
func (r *Runner) ListTaskSummaries() []TaskSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]TaskSummary, 0, len(r.agents))
	for id, ag := range r.agents {
		s := TaskSummary{
			ID:        id,
			Type:      string(ag.Type),
			Status:    ag.GetStatus(),
			Task:      ag.GetTaskPreview(80),
			StartTime: ag.GetStartTime(),
		}
		switch {
		case s.Status == AgentStatusRunning && !s.StartTime.IsZero():
			s.Duration = time.Since(s.StartTime)
		case !s.StartTime.IsZero():
			if end := ag.GetEndTime(); !end.IsZero() {
				s.Duration = end.Sub(s.StartTime)
			}
		}
		if res, ok := r.results[id]; ok && res != nil {
			s.Output = res.Output
			s.Error = res.Error
			s.Completed = res.Completed
			if res.Duration > 0 {
				s.Duration = res.Duration
			}
		}
		out = append(out, s)
	}

	sort.Slice(out, func(i, j int) bool {
		iRunning := out[i].Status == AgentStatusRunning
		jRunning := out[j].Status == AgentStatusRunning
		if iRunning != jRunning {
			return iRunning
		}
		if !out[i].StartTime.Equal(out[j].StartTime) {
			return out[i].StartTime.After(out[j].StartTime)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ListRunning returns IDs of currently running agents.
func (r *Runner) ListRunning() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]string, 0)
	for id, agent := range r.agents {
		if agent.GetStatus() == AgentStatusRunning {
			ids = append(ids, id)
		}
	}
	return ids
}

// Cleanup removes completed agents older than the specified duration.
// Also removes associated output files from disk.
func (r *Runner) Cleanup(maxAge time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	cleaned := 0

	for id, agent := range r.agents {
		status := agent.GetStatus()
		if status == AgentStatusCompleted || status == AgentStatusFailed || status == AgentStatusCancelled {
			endTime := agent.GetEndTime()
			if !endTime.IsZero() && endTime.Before(cutoff) {
				// Clean up agent output file if it exists
				if result, ok := r.results[id]; ok && result.OutputFile != "" {
					os.Remove(result.OutputFile)
				}
				delete(r.agents, id)
				delete(r.results, id)
				cleaned++
			}
		}
	}

	return cleaned
}

// SetStore sets the agent store for persistence.
func (r *Runner) SetStore(store *AgentStore) {
	r.mu.Lock()
	r.store = store
	r.mu.Unlock()
}
