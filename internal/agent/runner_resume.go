package agent

import (
	"context"
	"fmt"
	"time"

	"gokin/internal/logging"
)

// Resume resumes an agent from a saved state.
func (r *Runner) Resume(ctx context.Context, agentID string, prompt string) (string, error) {
	if r.store == nil {
		return "", fmt.Errorf("agent store not configured")
	}

	// Load state from store
	state, err := r.store.Load(agentID)
	if err != nil {
		return "", fmt.Errorf("failed to load agent state: %w", err)
	}

	// Create a new agent with the same configuration
	deps := r.snapshotAgentDeps()
	agent := r.newRestoredAgent(ctx, deps, state, deps.permissions)
	if r.store != nil {
		agent.SetStore(r.store)
	}

	// Restore history
	if err := agent.RestoreHistory(state); err != nil {
		return "", fmt.Errorf("failed to restore agent history: %w", err)
	}

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.mu.Unlock()

	attachMetaAgentMonitoring(agent, deps.metaAgent)

	// Run agent with the new prompt (continuing from previous context)
	startTime := time.Now()
	result, err := agent.Run(ctx, prompt)
	duration := time.Since(startTime)

	if result == nil {
		result = &AgentResult{
			AgentID:   agent.ID,
			Type:      agent.Type,
			Status:    AgentStatusFailed,
			Error:     "nil result from agent",
			Completed: true,
			Duration:  duration,
		}
	}

	if deps.metaAgent != nil {
		deps.metaAgent.UnregisterAgent(agent.ID)
	}

	workspaceErr := r.finalizeAgentWorkspace(agent, result)
	// Save updated state
	if r.store != nil {
		if err := r.store.Save(agent); err != nil {
			logging.Warn("failed to save agent state", "agent_id", agent.ID, "error", err)
		}
	}

	r.mu.Lock()
	r.results[agent.ID] = result
	r.mu.Unlock()
	r.notifyResultReady()
	r.recordAgentExecutionLearning(deps, string(state.Type), prompt, result, duration, "resume")

	if err == nil && workspaceErr != nil {
		err = workspaceErr
	}
	if err != nil {
		return agent.ID, err
	}

	return agent.ID, nil
}

// ResumeAsync resumes an agent asynchronously.
func (r *Runner) ResumeAsync(ctx context.Context, agentID string, prompt string) (string, error) {
	if r.store == nil {
		return "", fmt.Errorf("agent store not configured")
	}

	// Load state from store
	state, err := r.store.Load(agentID)
	if err != nil {
		return "", fmt.Errorf("failed to load agent state: %w", err)
	}

	// Create a new agent with the same configuration
	deps := r.snapshotAgentDeps()
	agent := r.newRestoredAgent(ctx, deps, state, deps.permissions)
	if r.store != nil {
		agent.SetStore(r.store)
	}

	// Restore history
	if err := agent.RestoreHistory(state); err != nil {
		return "", fmt.Errorf("failed to restore agent history: %w", err)
	}

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.results[agent.ID] = &AgentResult{
		AgentID: agent.ID,
		Type:    agent.Type,
		Status:  AgentStatusPending,
	}
	onStart := r.onAgentStart
	onComplete := r.onAgentComplete
	r.mu.Unlock()

	attachMetaAgentMonitoring(agent, deps.metaAgent)

	// Notify UI about agent start (resumed)
	if onStart != nil {
		onStart(agent.ID, string(state.Type), prompt)
	}

	// Run agent asynchronously with proper cleanup
	go func() {
		// Ensure cleanup happens even on panic
		defer func() {
			if p := recover(); p != nil {
				r.mu.Lock()
				if result, ok := r.results[agent.ID]; ok {
					result.Error = fmt.Sprintf("agent panic: %v", p)
					result.Status = AgentStatusFailed
					result.Completed = true
				}
				r.mu.Unlock()
				r.notifyResultReady()
			}
		}()

		// Detach from caller's context so resumed agent survives tool timeout.
		bgCtx := context.WithoutCancel(ctx)
		agentCtx, agentCancel := context.WithCancel(bgCtx)
		defer agentCancel()

		// Store cancel func for explicit Agent.Cancel()
		agent.SetCancelFunc(agentCancel)

		// Check if original context is already cancelled
		select {
		case <-ctx.Done():
			if deps.metaAgent != nil {
				deps.metaAgent.UnregisterAgent(agent.ID)
			}
			cancelledResult := &AgentResult{
				AgentID:   agent.ID,
				Type:      state.Type,
				Status:    AgentStatusCancelled,
				Error:     ctx.Err().Error(),
				Completed: true,
			}
			r.finalizeAgentWorkspace(agent, cancelledResult)
			r.mu.Lock()
			r.results[agent.ID] = cancelledResult
			r.mu.Unlock()
			r.notifyResultReady()
			return
		default:
		}

		result, err := agent.Run(agentCtx, prompt)
		var duration time.Duration
		if result != nil {
			duration = result.Duration
		}

		// Ensure result is never nil
		if result == nil {
			result = &AgentResult{
				AgentID:   agent.ID,
				Type:      agent.Type,
				Status:    AgentStatusFailed,
				Error:     "nil result from agent",
				Completed: true,
			}
		}

		// Handle error by updating result status
		if err != nil {
			result.Error = err.Error()
			result.Status = AgentStatusFailed
		}

		// Ensure Completed is always true so WaitWithContext doesn't spin
		result.Completed = true

		if deps.metaAgent != nil {
			deps.metaAgent.UnregisterAgent(agent.ID)
		}

		r.finalizeAgentWorkspace(agent, result)
		// Save updated state
		if r.store != nil {
			if saveErr := r.store.Save(agent); saveErr != nil {
				logging.Warn("failed to save agent state",
					"agent_id", agent.ID,
					"error", saveErr)
			}
		}

		r.mu.Lock()
		r.results[agent.ID] = result
		r.mu.Unlock()
		r.notifyResultReady()
		r.recordAgentExecutionLearning(deps, string(state.Type), prompt, result, duration, "resume_async")

		// Notify UI about agent completion
		if onComplete != nil {
			onComplete(agent.ID, result)
		}
	}()

	return agent.ID, nil
}

// saveAgentState saves the agent state if store is configured.
func (r *Runner) saveAgentState(agent *Agent) {
	if r.store != nil {
		if err := r.store.Save(agent); err != nil {
			logging.Warn("failed to save agent state", "agent_id", agent.ID, "error", err)
		}
	}
}

// CleanupOldCheckpoints removes checkpoint files older than maxAge.
func (r *Runner) CleanupOldCheckpoints(maxAge time.Duration) {
	if r.store == nil {
		return
	}
	cleaned, err := r.store.CleanupOldCheckpointFiles(maxAge)
	if err != nil {
		logging.Debug("checkpoint cleanup error", "error", err)
	} else if cleaned > 0 {
		logging.Debug("cleaned up old checkpoint files", "count", cleaned)
	}
}

// ResumeErrorCheckpoints finds error checkpoints and silently resumes agents in the background.
// Each checkpoint is deleted before resume to prevent infinite retry loops.
// Resumed agents do NOT get auto-checkpoint enabled — if they fail again, no new error checkpoint is created.
// Returns the number of agents successfully resumed.
func (r *Runner) ResumeErrorCheckpoints(ctx context.Context) int {
	if r.store == nil {
		return 0
	}

	checkpoints, err := r.store.ListErrorCheckpoints()
	if err != nil || len(checkpoints) == 0 {
		return 0
	}

	resumed := 0
	for _, cp := range checkpoints {
		if cp.AgentState == nil {
			_ = r.store.DeleteCheckpoint(cp.CheckpointID)
			continue
		}

		// Delete BEFORE resume — anti-infinite-retry
		_ = r.store.DeleteCheckpoint(cp.CheckpointID)

		// Create agent, restore from checkpoint
		state := cp.AgentState
		deps := r.snapshotAgentDeps()
		agent := r.newRestoredAgent(ctx, deps, state, deps.permissions)

		// Store without auto-checkpoint (if it fails again, no new error cp)
		if r.store != nil {
			agent.SetStore(r.store)
		}

		if err := agent.RestoreFromCheckpoint(cp); err != nil {
			logging.Debug("failed to restore from checkpoint", "checkpoint_id", cp.CheckpointID, "error", err)
			continue
		}

		r.mu.Lock()
		r.agents[agent.ID] = agent
		r.results[agent.ID] = &AgentResult{AgentID: agent.ID, Type: agent.Type, Status: AgentStatusPending}
		onComplete := r.onAgentComplete
		r.mu.Unlock()

		attachMetaAgentMonitoring(agent, deps.metaAgent)

		stateType := string(state.Type)
		resumePrompt := "You were restarted after an error. Continue your previous task or report what went wrong."

		go func(a *Agent, runDeps runnerAgentDeps, restoredType string, continuationPrompt string) {
			defer func() {
				if p := recover(); p != nil {
					logging.Debug("auto-resumed agent panicked", "agent_id", a.ID, "panic", fmt.Sprintf("%v", p))
					r.mu.Lock()
					if result, ok := r.results[a.ID]; ok {
						result.Error = fmt.Sprintf("agent panic on resume: %v", p)
						result.Status = AgentStatusFailed
						result.Completed = true
					}
					r.mu.Unlock()
					r.notifyResultReady()
				}
			}()

			// Detach from caller's context so resumed agent survives tool timeout.
			bgCtx := context.WithoutCancel(ctx)
			agentCtx, agentCancel := context.WithCancel(bgCtx)
			defer agentCancel()
			a.SetCancelFunc(agentCancel)

			select {
			case <-ctx.Done():
				if runDeps.metaAgent != nil {
					runDeps.metaAgent.UnregisterAgent(a.ID)
				}
				cancelledResult := &AgentResult{
					AgentID:   a.ID,
					Type:      a.Type,
					Status:    AgentStatusCancelled,
					Error:     ctx.Err().Error(),
					Completed: true,
				}
				r.finalizeAgentWorkspace(a, cancelledResult)
				r.mu.Lock()
				r.results[a.ID] = cancelledResult
				r.mu.Unlock()
				r.notifyResultReady()
				return
			default:
			}

			result, err := a.Run(agentCtx, continuationPrompt)
			var duration time.Duration
			if result != nil {
				duration = result.Duration
			}
			if result == nil {
				result = &AgentResult{AgentID: a.ID, Type: a.Type, Status: AgentStatusFailed, Error: "nil result", Completed: true}
			}
			if err != nil {
				result.Error = err.Error()
				result.Status = AgentStatusFailed
			}
			result.Completed = true

			if runDeps.metaAgent != nil {
				runDeps.metaAgent.UnregisterAgent(a.ID)
			}

			r.finalizeAgentWorkspace(a, result)
			r.saveAgentState(a)
			r.recordAgentExecutionLearning(
				runDeps,
				restoredType,
				continuationPrompt,
				result,
				duration,
				"resume_error_checkpoint",
			)

			r.mu.Lock()
			r.results[a.ID] = result
			r.mu.Unlock()
			r.notifyResultReady()

			if onComplete != nil {
				onComplete(a.ID, result)
			}
		}(agent, deps, stateType, resumePrompt)

		resumed++
	}

	if resumed > 0 {
		logging.Debug("auto-resumed agents from error checkpoints", "count", resumed)
	}
	return resumed
}

// ResumeLastCheckpoint finds the most recent checkpoint across all agents and resumes it.
// The checkpoint is deleted before resume to prevent duplicate runs.
// Returns the agent ID and any error.
func (r *Runner) ResumeLastCheckpoint(ctx context.Context) (string, error) {
	if r.store == nil {
		return "", fmt.Errorf("agent store not configured")
	}

	// List all checkpoints (empty agentID = no filter)
	ids, err := r.store.ListCheckpoints("")
	if err != nil {
		return "", fmt.Errorf("failed to list checkpoints: %w", err)
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("no checkpoints found")
	}

	// Checkpoint names include timestamps — pick the lexicographically latest
	latestID := ids[0]
	for _, id := range ids[1:] {
		if id > latestID {
			latestID = id
		}
	}

	cp, err := r.store.LoadCheckpoint(latestID)
	if err != nil {
		return "", fmt.Errorf("failed to load checkpoint %s: %w", latestID, err)
	}
	if cp.AgentState == nil {
		_ = r.store.DeleteCheckpoint(cp.CheckpointID)
		return "", fmt.Errorf("checkpoint %s has no agent state", latestID)
	}

	// Delete before resume to prevent duplicate runs
	_ = r.store.DeleteCheckpoint(cp.CheckpointID)

	state := cp.AgentState
	deps := r.snapshotAgentDeps()
	agent := r.newRestoredAgent(ctx, deps, state, deps.permissions)
	if r.store != nil {
		agent.SetStore(r.store)
	}

	if err := agent.RestoreFromCheckpoint(cp); err != nil {
		return "", fmt.Errorf("failed to restore from checkpoint: %w", err)
	}

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.results[agent.ID] = &AgentResult{AgentID: agent.ID, Type: agent.Type, Status: AgentStatusPending}
	onStart := r.onAgentStart
	onComplete := r.onAgentComplete
	r.mu.Unlock()

	attachMetaAgentMonitoring(agent, deps.metaAgent)

	if onStart != nil {
		onStart(agent.ID, string(state.Type), "Resumed from checkpoint")
	}

	go func(a *Agent, runDeps runnerAgentDeps) {
		defer func() {
			if p := recover(); p != nil {
				logging.Debug("resumed agent panicked", "agent_id", a.ID, "panic", fmt.Sprintf("%v", p))
				r.mu.Lock()
				if result, ok := r.results[a.ID]; ok {
					result.Error = fmt.Sprintf("agent panic on resume: %v", p)
					result.Status = AgentStatusFailed
					result.Completed = true
				}
				r.mu.Unlock()
				r.notifyResultReady()
			}
		}()

		// Detach from caller's context so resumed agent survives tool timeout.
		bgCtx := context.WithoutCancel(ctx)
		agentCtx, agentCancel := context.WithCancel(bgCtx)
		defer agentCancel()
		a.SetCancelFunc(agentCancel)

		result, err := a.Run(agentCtx, "You were resumed from a checkpoint. Continue your previous task.")
		var duration time.Duration
		if result != nil {
			duration = result.Duration
		}
		if result == nil {
			result = &AgentResult{AgentID: a.ID, Type: a.Type, Status: AgentStatusFailed, Error: "nil result", Completed: true}
		}
		if err != nil {
			result.Error = err.Error()
			result.Status = AgentStatusFailed
		}
		result.Completed = true

		if runDeps.metaAgent != nil {
			runDeps.metaAgent.UnregisterAgent(a.ID)
		}

		r.finalizeAgentWorkspace(a, result)
		r.saveAgentState(a)
		r.recordAgentExecutionLearning(
			runDeps,
			string(state.Type),
			"You were resumed from a checkpoint. Continue your previous task.",
			result,
			duration,
			"resume_last_checkpoint",
		)

		r.mu.Lock()
		r.results[a.ID] = result
		r.mu.Unlock()
		r.notifyResultReady()

		if onComplete != nil {
			onComplete(a.ID, result)
		}
	}(agent, deps)

	logging.Debug("resumed agent from last checkpoint", "agent_id", agent.ID, "checkpoint_id", latestID)
	return agent.ID, nil
}

// Close flushes all agent data (project learning) to prevent data loss on shutdown.
func (r *Runner) Close() {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, agent := range r.agents {
		if err := agent.Close(); err != nil {
			logging.Warn("failed to close agent", "agent_id", agent.ID, "error", err)
		}
	}

	// Cleanup old checkpoints on shutdown, keeping only 2 most recent per agent
	if r.store != nil {
		for _, agent := range r.agents {
			if cleaned, err := r.store.CleanupCheckpoints(agent.ID, 2); err == nil && cleaned > 0 {
				logging.Debug("cleaned up agent checkpoints", "agent_id", agent.ID, "cleaned", cleaned)
			}
		}
	}
}
