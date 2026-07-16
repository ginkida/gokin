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
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// Load state from store
	state, err := r.store.Load(agentID)
	if err != nil {
		return "", fmt.Errorf("failed to load agent state: %w", err)
	}
	runLease, err := r.acquireAgentRunLease(state.ID)
	if err != nil {
		return "", err
	}
	defer runLease.Release()

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

	// Run agent with the new prompt (continuing from previous context). Register
	// a cancellable run context before Run so Runner.Cancel never becomes a
	// status-only no-op on the synchronous resume path.
	var runCtx context.Context
	var runCancel context.CancelFunc
	if agentTimeout := agent.GetTimeout(); agentTimeout > 0 {
		runCtx, runCancel = context.WithTimeout(ctx, agentTimeout)
	} else {
		runCtx, runCancel = context.WithCancel(ctx)
	}
	defer runCancel()
	agent.SetCancelFunc(runCancel)

	startTime := time.Now()
	result, err := agent.Run(runCtx, prompt)
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

	r.recordAgentExecutionLearning(deps, string(state.Type), prompt, result, duration, "resume")
	r.reportAgentUsage(agent.ID, result)
	r.mu.Lock()
	r.results[agent.ID] = result
	r.mu.Unlock()
	r.notifyResultReady(agent.ID)

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
	// Once accepted, an asynchronous resume deliberately outlives the caller's
	// tool/request context. Reject a context which was already cancelled at the
	// ownership boundary instead of racing goroutine scheduling below.
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// Load state from store
	state, err := r.store.Load(agentID)
	if err != nil {
		return "", fmt.Errorf("failed to load agent state: %w", err)
	}
	runLease, err := r.acquireAgentRunLease(state.ID)
	if err != nil {
		return "", err
	}
	leaseTransferred := false
	defer func() {
		if !leaseTransferred {
			runLease.Release()
		}
	}()

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
	invokeAgentStart(onStart, agent.ID, string(state.Type), prompt)

	// Run agent asynchronously with proper cleanup
	leaseTransferred = true
	go func() {
		// Keep the lease through final persistence/result publication and panic
		// recovery, not merely through Agent.Run.
		defer runLease.Release()
		// Ensure cleanup happens even on panic
		defer func() {
			if p := recover(); p != nil {
				logging.Warn("resumed agent panicked",
					"agent_id", agent.ID,
					"panic", p,
					"stack", logging.PanicStack())
				r.publishResumedAgentPanic(
					agent, deps, onComplete, string(state.Type), prompt,
					"resume_async", p,
				)
			}
		}()

		// Detach from caller's context so resumed agent survives tool timeout.
		agentCtx, agentCancel := detachedResumeRunContext(ctx, agent)
		defer agentCancel()

		// Store cancel func for explicit Agent.Cancel()
		agent.SetCancelFunc(agentCancel)

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
			applyAgentRunError(result, err)
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

		r.recordAgentExecutionLearning(deps, string(state.Type), prompt, result, duration, "resume_async")
		r.reportAgentUsage(agent.ID, result)
		r.mu.Lock()
		r.results[agent.ID] = result
		r.mu.Unlock()
		r.notifyResultReady(agent.ID)

		// Notify UI about agent completion
		invokeAgentComplete(onComplete, agent.ID, result)
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
	if ctx.Err() != nil {
		return 0
	}

	checkpoints, err := r.store.ListErrorCheckpoints()
	if err != nil {
		logging.Warn("failed to inspect error checkpoints — automatic recovery skipped", "error", err)
		return 0
	}
	if len(checkpoints) == 0 {
		return 0
	}

	// Several error checkpoints may belong to the same logical agent (for
	// example an automatic snapshot followed by a terminal provider error).
	// Only the newest state is a valid continuation; launching every file would
	// replay the same task multiple times even though the run lease serializes
	// them. Keep older files as superseded members of the same claim group.
	type checkpointGroup struct {
		latest     *AgentCheckpoint
		superseded []*AgentCheckpoint
	}
	groups := make(map[string]*checkpointGroup)
	for _, cp := range checkpoints {
		if cp == nil {
			continue
		}
		if cp.AgentState == nil {
			_ = r.store.DeleteCheckpoint(cp.CheckpointID)
			continue
		}
		agentID := cp.AgentState.ID
		group := groups[agentID]
		if group == nil {
			groups[agentID] = &checkpointGroup{latest: cp}
			continue
		}
		newer := cp.Timestamp.After(group.latest.Timestamp) ||
			(cp.Timestamp.Equal(group.latest.Timestamp) && cp.CheckpointID > group.latest.CheckpointID)
		if newer {
			group.superseded = append(group.superseded, group.latest)
			group.latest = cp
		} else {
			group.superseded = append(group.superseded, cp)
		}
	}

	resumed := 0
	for _, group := range groups {
		// Do not consume durable recovery state once shutdown/cancellation has
		// started. A run accepted before cancellation is intentionally detached
		// and is thereafter stopped only by Runner.Cancel or its own timeout.
		if ctx.Err() != nil {
			break
		}
		cp := group.latest

		state := cp.AgentState
		runLease, leaseErr := r.acquireAgentRunLease(state.ID)
		if leaseErr != nil {
			// Another Spawn/Resume path owns this logical agent. Preserve the
			// checkpoint: the active run may still need it until its terminal
			// state is durably published.
			logging.Debug("skipping checkpoint for active agent",
				"checkpoint_id", cp.CheckpointID,
				"agent_id", state.ID,
				"error", leaseErr)
			continue
		}

		// Create agent, restore from checkpoint while holding the per-agent
		// lease. This prevents concurrent monitors from constructing and
		// launching duplicate copies with the same persisted history.
		deps := r.snapshotAgentDeps()
		agent := r.newRestoredAgent(ctx, deps, state, deps.permissions)

		// Store without auto-checkpoint (if it fails again, no new error cp)
		if r.store != nil {
			agent.SetStore(r.store)
		}

		if err := agent.RestoreFromCheckpoint(cp); err != nil {
			runLease.Release()
			// Bumped Debug→Warn: a checkpoint that fails to restore is
			// silently skipped (continue), so users with N broken
			// checkpoints saw nothing resume and had no signal why.
			// Visible at default log level so post-mortem can correlate
			// with whatever made the checkpoint unloadable (schema drift,
			// truncated file, etc.).
			logging.Warn("failed to restore from checkpoint — skipping",
				"checkpoint_id", cp.CheckpointID, "error", err)
			continue
		}
		// Retire older error snapshots before claiming the selected checkpoint.
		// If cleanup fails, keep the selected file unconsumed and do not execute;
		// a later recovery can retry without risking a stale replay.
		supersededRemoved := true
		for _, old := range group.superseded {
			if err := r.store.DeleteCheckpoint(old.CheckpointID); err != nil {
				supersededRemoved = false
				logging.Warn("failed to retire superseded error checkpoint — not resuming",
					"checkpoint_id", old.CheckpointID,
					"selected_checkpoint_id", cp.CheckpointID,
					"agent_id", state.ID,
					"error", err)
				break
			}
		}
		if !supersededRemoved {
			runLease.Release()
			continue
		}

		// Consume only after a successful restore, but always before Run. If
		// deletion fails, do not execute: otherwise a later monitor/startup can
		// replay the same side effects from a checkpoint we failed to mark used.
		consumed, err := r.store.ConsumeCheckpoint(cp.CheckpointID)
		if err != nil {
			runLease.Release()
			logging.Warn("failed to consume error checkpoint — not resuming",
				"checkpoint_id", cp.CheckpointID,
				"agent_id", state.ID,
				"error", err)
			continue
		}
		if !consumed {
			runLease.Release()
			logging.Debug("error checkpoint already consumed by another recovery path",
				"checkpoint_id", cp.CheckpointID,
				"agent_id", state.ID)
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

		go func(a *Agent, runDeps runnerAgentDeps, restoredType string, continuationPrompt string, lease *agentRunLease) {
			defer lease.Release()
			defer func() {
				if p := recover(); p != nil {
					// Bumped Debug→Warn + stack capture: an agent panic
					// during auto-resume is captured into result.Error,
					// but Debug-level meant nobody saw it in field logs.
					// Stack trace makes the rare non-trivial panic
					// (vs. nil-deref on stale checkpoint) debuggable.
					logging.Warn("auto-resumed agent panicked",
						"agent_id", a.ID,
						"panic", p,
						"stack", logging.PanicStack())
					r.publishResumedAgentPanic(
						a, runDeps, onComplete, restoredType, continuationPrompt,
						"resume_error_checkpoint", p,
					)
				}
			}()

			// Detach from caller's context so resumed agent survives tool timeout.
			agentCtx, agentCancel := detachedResumeRunContext(ctx, a)
			defer agentCancel()
			a.SetCancelFunc(agentCancel)

			result, err := a.Run(agentCtx, continuationPrompt)
			var duration time.Duration
			if result != nil {
				duration = result.Duration
			}
			if result == nil {
				result = &AgentResult{AgentID: a.ID, Type: a.Type, Status: AgentStatusFailed, Error: "nil result", Completed: true}
			}
			if err != nil {
				applyAgentRunError(result, err)
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
			r.reportAgentUsage(a.ID, result)

			r.mu.Lock()
			r.results[a.ID] = result
			r.mu.Unlock()
			r.notifyResultReady(a.ID)

			invokeAgentComplete(onComplete, a.ID, result)
		}(agent, deps, stateType, resumePrompt, runLease)

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
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// List all checkpoints (empty agentID = no filter)
	ids, err := r.store.ListCheckpoints("")
	if err != nil {
		return "", fmt.Errorf("failed to list checkpoints: %w", err)
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("no checkpoints found")
	}

	// IDs are `<agent-id>-<unix-nano>`, so lexicographic ordering is valid only
	// within one agent. Across agents it primarily sorts by agent ID and can pick
	// an arbitrarily old checkpoint. Load metadata and compare actual timestamps.
	var cp *AgentCheckpoint
	loaded := make([]*AgentCheckpoint, 0, len(ids))
	for _, id := range ids {
		candidate, loadErr := r.store.LoadCheckpoint(id)
		if loadErr != nil {
			// An unreadable file may be newer than every valid candidate. Falling
			// back to older state can replay side effects, so manual recovery must
			// fail closed just like automatic error recovery.
			return "", fmt.Errorf("failed to load checkpoint %s while finding latest: %w", id, loadErr)
		}
		if candidate.AgentState == nil {
			_ = r.store.DeleteCheckpoint(candidate.CheckpointID)
			continue
		}
		loaded = append(loaded, candidate)
		if cp == nil || candidate.Timestamp.After(cp.Timestamp) ||
			(candidate.Timestamp.Equal(cp.Timestamp) && candidate.CheckpointID > cp.CheckpointID) {
			cp = candidate
		}
	}
	if cp == nil {
		return "", fmt.Errorf("no valid checkpoints found")
	}
	latestID := cp.CheckpointID

	state := cp.AgentState
	runLease, err := r.acquireAgentRunLease(state.ID)
	if err != nil {
		return "", err
	}
	leaseTransferred := false
	defer func() {
		if !leaseTransferred {
			runLease.Release()
		}
	}()

	deps := r.snapshotAgentDeps()
	agent := r.newRestoredAgent(ctx, deps, state, deps.permissions)
	if r.store != nil {
		agent.SetStore(r.store)
	}

	if err := agent.RestoreFromCheckpoint(cp); err != nil {
		return "", fmt.Errorf("failed to restore from checkpoint: %w", err)
	}
	// Claim the logical agent's newest continuation as a group. Otherwise, once
	// this file is consumed, another Runner/process can immediately discover an
	// older checkpoint for the same agent and replay it concurrently.
	for _, old := range loaded {
		if old == cp || old.AgentState == nil || old.AgentState.ID != state.ID {
			continue
		}
		if err := r.store.DeleteCheckpoint(old.CheckpointID); err != nil {
			return "", fmt.Errorf("failed to retire superseded checkpoint %s: %w", old.CheckpointID, err)
		}
	}
	// A checkpoint must be durably consumed before any model/tool execution.
	// Ignoring deletion failure would allow the same side effects to replay on
	// the next ResumeLastCheckpoint/startup monitor call.
	consumed, err := r.store.ConsumeCheckpoint(cp.CheckpointID)
	if err != nil {
		return "", fmt.Errorf("failed to consume checkpoint %s: %w", cp.CheckpointID, err)
	}
	if !consumed {
		return "", fmt.Errorf("checkpoint %s was already consumed by another recovery path", cp.CheckpointID)
	}

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.results[agent.ID] = &AgentResult{AgentID: agent.ID, Type: agent.Type, Status: AgentStatusPending}
	onStart := r.onAgentStart
	onComplete := r.onAgentComplete
	r.mu.Unlock()

	attachMetaAgentMonitoring(agent, deps.metaAgent)

	invokeAgentStart(onStart, agent.ID, string(state.Type), "Resumed from checkpoint")

	leaseTransferred = true
	go func(a *Agent, runDeps runnerAgentDeps, lease *agentRunLease) {
		defer lease.Release()
		defer func() {
			if p := recover(); p != nil {
				// Same fix as auto-resume above: Debug→Warn + stack so
				// resumed-agent panics surface in field logs.
				logging.Warn("resumed agent panicked",
					"agent_id", a.ID,
					"panic", p,
					"stack", logging.PanicStack())
				r.publishResumedAgentPanic(
					a, runDeps, onComplete, string(state.Type),
					"You were resumed from a checkpoint. Continue your previous task.",
					"resume_last_checkpoint", p,
				)
			}
		}()

		// Detach from caller's context so resumed agent survives tool timeout.
		agentCtx, agentCancel := detachedResumeRunContext(ctx, a)
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
			applyAgentRunError(result, err)
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
		r.reportAgentUsage(a.ID, result)

		r.mu.Lock()
		r.results[a.ID] = result
		r.mu.Unlock()
		r.notifyResultReady(a.ID)

		invokeAgentComplete(onComplete, a.ID, result)
	}(agent, deps, runLease)

	logging.Debug("resumed agent from last checkpoint", "agent_id", agent.ID, "checkpoint_id", latestID)
	return agent.ID, nil
}

// detachedResumeRunContext gives accepted asynchronous resumes the same
// bounded lifetime as fresh asynchronous spawns while deliberately removing
// only the caller/tool cancellation edge.
func detachedResumeRunContext(ctx context.Context, agent *Agent) (context.Context, context.CancelFunc) {
	base := context.WithoutCancel(ctx)
	if agent != nil {
		if timeout := agent.GetTimeout(); timeout > 0 {
			return context.WithTimeout(base, timeout)
		}
	}
	return context.WithCancel(base)
}

// publishResumedAgentPanic completes every durable and observable lifecycle
// edge before releasing the run lease. Previously panic recovery only changed
// the in-memory result map: Agent status and persisted state remained Running,
// workspace cleanup/accounting were skipped, and UI completion never fired.
func (r *Runner) publishResumedAgentPanic(
	agent *Agent,
	deps runnerAgentDeps,
	onComplete func(string, *AgentResult),
	agentType, prompt, strategy string,
	panicValue any,
) {
	end := time.Now()
	start := agent.GetStartTime()
	duration := time.Duration(0)
	if !start.IsZero() {
		duration = end.Sub(start)
	}

	agent.stateMu.Lock()
	agent.status = AgentStatusFailed
	agent.endTime = end
	inputTokens := agent.usageInputTokens
	outputTokens := agent.usageOutputTokens
	cacheReadTokens := agent.usageCacheReadTokens
	estimatedCost := agent.usageEstimatedCost
	costTracked := agent.usageCostTracked
	agent.stateMu.Unlock()

	result := &AgentResult{
		AgentID:              agent.ID,
		Type:                 agent.Type,
		Status:               AgentStatusFailed,
		Error:                fmt.Sprintf("agent panic on resume: %v", panicValue),
		Completed:            false,
		Duration:             duration,
		InputTokens:          inputTokens,
		OutputTokens:         outputTokens,
		CacheReadInputTokens: cacheReadTokens,
		EstimatedCost:        estimatedCost,
		CostTracked:          costTracked,
		MutatingToolCalls:    agent.MutatingToolCount(),
		StatefulToolAttempts: agent.StatefulToolAttemptCount(),
		TouchedPaths:         agent.GetTouchedPaths(),
	}
	agent.populateResultIdentity(result)
	r.finalizeAgentWorkspace(agent, result)
	result.Completed = true

	if deps.metaAgent != nil {
		deps.metaAgent.UnregisterAgent(agent.ID)
	}
	r.saveAgentState(agent)
	r.recordAgentExecutionLearning(deps, agentType, prompt, result, duration, strategy)
	r.reportAgentUsage(agent.ID, result)

	r.mu.Lock()
	r.results[agent.ID] = result
	r.mu.Unlock()
	r.notifyResultReady(agent.ID)
	invokeAgentComplete(onComplete, agent.ID, result)
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
