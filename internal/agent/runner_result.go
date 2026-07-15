package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"gokin/internal/logging"
)

// ErrAgentResultUnavailable means the requested result is no longer tracked by
// the runner (or the ID never belonged to it). Waiting cannot make progress in
// that state: there is no publisher left that could signal a newly registered
// waiter. Callers can use errors.Is to distinguish this from cancellation.
var ErrAgentResultUnavailable = errors.New("agent result unavailable")

// Wait waits for an agent to complete and returns its result.
// Uses a default 10-minute timeout. For context-aware waiting, use WaitWithContext.
func (r *Runner) Wait(agentID string) (*AgentResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return r.WaitWithContext(ctx, agentID)
}

// resultWaitState is shared by every current waiter for one agent. Completion
// transfers an immutable result snapshot into this state before closing done.
// That snapshot is the waiters' ownership boundary: cleanup may run as soon as
// completion is published without making an awakened waiter re-read an evicted
// map entry. The state remains pinned until every registered waiter consumes or
// abandons it. All fields are guarded by Runner.mu.
type resultWaitState struct {
	done      chan struct{}
	waiters   int
	completed bool
	result    *AgentResult
}

// notifyResultReady signals that agentID's final result has been published.
// Closing the per-agent channel is a lossless broadcast: it preserves multiple
// waiters, while the snapshot carried by the state closes the completion ->
// cleanup -> waiter-recheck lost-notification window.
func (r *Runner) notifyResultReady(agentID string) {
	r.mu.Lock()
	waiter := r.resultWaiters[agentID]
	result := r.results[agentID]
	if waiter != nil && !waiter.completed && result != nil && result.Completed {
		waiter.result = cloneAgentResult(result)
		waiter.completed = true
		close(waiter.done)
	}
	needsCleanup := len(r.results) > MaxAgentResults || len(r.agents) > MaxCompletedAgents
	r.mu.Unlock()

	// A burst can start while every result is still pending, so cleanup only at
	// spawn time does not enforce the cap once those agents finish. Completed
	// consumers are pinned above; unowned history can be compacted immediately.
	if needsCleanup {
		r.cleanupOldResults()
	}
}

// WaitWithContext waits for an agent to complete, respecting context cancellation.
func (r *Runner) WaitWithContext(ctx context.Context, agentID string) (*AgentResult, error) {
	result, waiter, err := r.completedResultOrRegisterWaiter(agentID)
	if err != nil || result != nil {
		return result, err
	}

	select {
	case <-ctx.Done():
		// Prefer a completion that won the Runner.mu race over a simultaneous
		// context cancellation. It owns a final result and cannot be replayed.
		if result, ok := r.consumeCompletedResultWaiter(agentID, waiter); ok {
			return result, nil
		}
		r.releaseResultWaiter(agentID, waiter)
		return nil, ctx.Err()
	case <-waiter.done:
		result, ok := r.consumeCompletedResultWaiter(agentID, waiter)
		if !ok {
			// done is closed only after completed/result are committed under
			// the same lock, so reaching this branch signals internal corruption
			// rather than a condition another notification could repair.
			return nil, fmt.Errorf("%w for agent %q after completion signal", ErrAgentResultUnavailable, agentID)
		}
		return result, nil
	}
}

// completedResultOrRegisterWaiter atomically checks completion and registers
// this call as a waiter. Holding the same lock across both operations closes
// the classic missed-wakeup window between a fast-path check and subscription.
func (r *Runner) completedResultOrRegisterWaiter(agentID string) (*AgentResult, *resultWaitState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if result, ok := r.results[agentID]; ok && result.Completed {
		return cloneAgentResult(result), nil, nil
	}
	if r.resultWaiters == nil {
		r.resultWaiters = make(map[string]*resultWaitState)
	}
	waiter := r.resultWaiters[agentID]
	// A completed waiter may outlive the shared result entry while its
	// registered consumers are being scheduled. Joining it is still safe: the
	// owned snapshot and closed channel provide immediate, lossless delivery.
	if waiter == nil {
		_, resultTracked := r.results[agentID]
		_, agentTracked := r.agents[agentID]
		if !resultTracked && !agentTracked {
			return nil, nil, fmt.Errorf("%w: %s", ErrAgentResultUnavailable, agentID)
		}
	}
	if waiter == nil {
		waiter = &resultWaitState{done: make(chan struct{})}
		r.resultWaiters[agentID] = waiter
	}
	waiter.waiters++
	return nil, waiter, nil
}

// consumeCompletedResultWaiter atomically takes one waiter's share of the
// completion snapshot. The snapshot is cloned once more so multiple callers
// cannot mutate one another's result/metadata.
func (r *Runner) consumeCompletedResultWaiter(agentID string, waiter *resultWaitState) (*AgentResult, bool) {
	if waiter == nil {
		return nil, false
	}

	r.mu.Lock()
	if !waiter.completed || waiter.result == nil {
		r.mu.Unlock()
		return nil, false
	}

	result := cloneAgentResult(waiter.result)
	needsCleanup := len(r.results) > MaxAgentResults || len(r.agents) > MaxCompletedAgents
	r.mu.Unlock()

	// Keep this consumer's pin until cleanup finishes. With a >cap completion
	// burst, each scheduled waiter can evict already-consumed history while its
	// own result remains protected, bringing memory back under the cap even when
	// no subsequent agent is spawned.
	if needsCleanup {
		r.cleanupOldResults()
	}

	r.mu.Lock()
	r.releaseResultWaiterLocked(agentID, waiter)
	r.mu.Unlock()
	return result, true
}

func (r *Runner) releaseResultWaiter(agentID string, waiter *resultWaitState) {
	if waiter == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseResultWaiterLocked(agentID, waiter)
}

func (r *Runner) releaseResultWaiterLocked(agentID string, waiter *resultWaitState) {
	if current := r.resultWaiters[agentID]; current == waiter && waiter.waiters > 0 {
		waiter.waiters--
		if waiter.waiters == 0 {
			delete(r.resultWaiters, agentID)
		}
	}
}

// resultHasConsumersLocked reports whether cleanup must retain an agent/result
// until WaitWithContext has consumed its completion snapshot. Runner.mu must be
// held by the caller.
func (r *Runner) resultHasConsumersLocked(agentID string) bool {
	waiter := r.resultWaiters[agentID]
	return waiter != nil && waiter.waiters > 0
}

// completedResultLocked returns an owned snapshot iff agentID has completed.
// Both the flag check and the copy happen under r.mu: returning the shared
// pointer would merely move the race to the caller's later reads of
// Status/Error/Metadata while a finalizer or panic recovery mutates it in place.
func (r *Runner) completedResultLocked(agentID string) (*AgentResult, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result, ok := r.results[agentID]
	if ok && result.Completed {
		return cloneAgentResult(result), true
	}
	return nil, false
}

// cloneAgentResult returns a caller-owned snapshot. AgentResult contains a
// slice and an extensible metadata graph, so a struct copy alone would still
// expose runner-owned mutable collections to readers and callbacks.
func cloneAgentResult(result *AgentResult) *AgentResult {
	if result == nil {
		return nil
	}
	clone := *result
	if result.PolicyBlock != nil {
		policyBlock := *result.PolicyBlock
		clone.PolicyBlock = &policyBlock
	}
	clone.TouchedPaths = append([]string(nil), result.TouchedPaths...)
	clone.Metadata = cloneAgentMetadata(result.Metadata)
	return &clone
}

func cloneAgentMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	clone := make(map[string]any, len(metadata))
	for key, value := range metadata {
		clone[key] = cloneAgentMetadataValue(value)
	}
	return clone
}

func cloneAgentMetadataValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAgentMetadata(typed)
	case map[string]string:
		clone := make(map[string]string, len(typed))
		for key, item := range typed {
			clone[key] = item
		}
		return clone
	case []any:
		clone := make([]any, len(typed))
		for i, item := range typed {
			clone[i] = cloneAgentMetadataValue(item)
		}
		return clone
	case []string:
		return append([]string(nil), typed...)
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
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

// GetResult returns a deep-enough caller-owned snapshot of an agent result.
// External consumers poll it without holding r.mu and may retain or mutate it;
// none of that should race with or modify the runner's internal ledger.
func (r *Runner) GetResult(agentID string) (*AgentResult, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result, ok := r.results[agentID]
	if !ok {
		return nil, false
	}
	return cloneAgentResult(result), true
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
	r.mu.RLock()
	agent, ok := r.agents[agentID]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("agent not found: %s", agentID)
	}

	// Cancellation is a request, not completion. The run goroutine owns final
	// status/result publication and signals waiters only after workspace cleanup,
	// persistence, learning and usage accounting have finished.
	agent.Cancel()
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
			if r.resultHasConsumersLocked(id) {
				continue
			}
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
