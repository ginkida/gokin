package agent

import (
	"context"
	"fmt"
	"sync"
	"time"
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
	// Fast path: check immediately
	r.mu.RLock()
	result, ok := r.results[agentID]
	r.mu.RUnlock()
	if ok && result.Completed {
		return result, nil
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.resultReady:
			r.mu.RLock()
			result, ok := r.results[agentID]
			r.mu.RUnlock()
			if ok && result.Completed {
				return result, nil
			}
		}
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

// GetResult returns the result for an agent.
func (r *Runner) GetResult(agentID string) (*AgentResult, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result, ok := r.results[agentID]
	return result, ok
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
