package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gokin/internal/logging"
	"gokin/internal/permission"
	"gokin/internal/tools"
)

// Spawn creates and starts a new agent with the given task.
// agentType should be "explore", "bash", "general", "plan", "claude-code-guide", or "coordinator".
// Also supports dynamic types registered via AgentTypeRegistry.
func (r *Runner) Spawn(ctx context.Context, agentType string, prompt string, maxTurns int, model string) (string, error) {
	// Cleanup old completed agents and results to prevent unbounded growth
	r.cleanupOldResults()

	deps := r.snapshotAgentDeps()
	agent := r.newConfiguredAgent(ctx, deps, agentType, maxTurns, model, deps.permissions)

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.mu.Unlock()

	// Wire tool activity to both meta-agent and UI
	r.mu.RLock()
	onSubAgentActivity := r.onSubAgentActivity
	r.mu.RUnlock()

	if deps.metaAgent != nil {
		deps.metaAgent.RegisterAgent(agent.ID, agent.Type)
	}
	agent.SetOnToolActivity(func(id, toolName string, args map[string]any, status string) {
		if onSubAgentActivity != nil {
			onSubAgentActivity(id, string(agent.Type), "", toolName, args, "tool_"+status)
		}
		if deps.metaAgent != nil && status == "start" {
			deps.metaAgent.UpdateActivity(agent.ID, toolName, agent.GetTurnCount())
		}
	})

	// Report activity to coordinator
	r.reportActivity()

	// Notify UI about agent start — pass the prompt so the feed can surface
	// what this agent is actually working on (instead of "Sub-agent: general").
	if onSubAgentActivity != nil {
		onSubAgentActivity(agent.ID, string(agent.Type), prompt, "", nil, "start")
	}

	// Run agent synchronously with per-agent timeout
	runCtx := ctx
	if agentTimeout := agent.GetTimeout(); agentTimeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, agentTimeout)
		defer cancel()
	}
	startTime := time.Now()
	result, err := agent.Run(runCtx, prompt)
	duration := time.Since(startTime)

	// Notify UI about agent completion
	if onSubAgentActivity != nil {
		status := "complete"
		if err != nil {
			status = "failed"
		}
		onSubAgentActivity(agent.ID, string(agent.Type), "", "", nil, status)
	}

	// Report activity after completion
	r.reportActivity()

	// Unregister from meta-agent
	if deps.metaAgent != nil {
		deps.metaAgent.UnregisterAgent(agent.ID)
	}

	// Save agent state for potential resume
	r.saveAgentState(agent)

	if result == nil {
		result = &AgentResult{
			AgentID:   agent.ID,
			Type:      agent.Type,
			Status:    AgentStatusFailed,
			Error:     "agent returned nil result",
			Completed: true,
			Duration:  duration,
		}
	}
	workspaceErr := r.finalizeAgentWorkspace(agent, result)
	r.recordAgentExecutionLearning(deps, agentType, prompt, result, duration, "spawn")

	r.mu.Lock()
	r.results[agent.ID] = result
	r.mu.Unlock()
	r.notifyResultReady()

	if err == nil && workspaceErr != nil {
		err = workspaceErr
	}
	if err != nil {
		return agent.ID, err
	}

	return agent.ID, nil
}

// SpawnWithContext creates and runs a sub-agent with project context and streaming.
// Unlike Spawn, it returns the AgentResult directly for immediate use by the caller.
// When skipPermissions is true, the sub-agent will not ask for permission before
// executing tools (used for approved plan execution).
func (r *Runner) SpawnWithContext(
	ctx context.Context,
	agentType string,
	prompt string,
	maxTurns int,
	model string,
	projectContext string,
	onText func(string),
	skipPermissions bool,
	progressCallback ProgressCallback,
) (string, *AgentResult, error) {
	deps := r.snapshotAgentDeps()

	// Pass nil permissions for approved plan execution to avoid per-tool prompts
	var perms *permission.Manager
	if !skipPermissions {
		perms = deps.permissions
	}
	agent := r.newConfiguredAgent(ctx, deps, agentType, maxTurns, model, perms)

	// Inject project context and streaming callbacks
	agent.SetProjectContext(projectContext)
	agent.SetOnText(onText)

	// Wire thinking callback from runner
	r.mu.RLock()
	onThinking := r.onThinking
	r.mu.RUnlock()
	if onThinking != nil {
		agent.SetOnThinking(onThinking)
	}

	// Wire checkpoint store and enable auto-checkpoint for long-running agents
	if r.store != nil {
		agent.SetStore(r.store)
		if maxTurns > 10 {
			agent.EnableAutoCheckpoint(0)
		}
	}

	// Wire progress callback for real-time sub-agent progress
	if progressCallback != nil {
		agent.SetProgressCallback(progressCallback)
	}

	// Wire sub-agent activity callback (chain with meta-agent UpdateActivity)
	r.mu.RLock()
	onSubAgentActivity := r.onSubAgentActivity
	r.mu.RUnlock()
	agentID := agent.ID
	agent.SetOnToolActivity(func(id, toolName string, args map[string]any, status string) {
		if onSubAgentActivity != nil {
			onSubAgentActivity(id, string(agent.Type), "", toolName, args, "tool_"+status)
		}
		if deps.metaAgent != nil && status == "start" {
			deps.metaAgent.UpdateActivity(agentID, toolName, agent.GetTurnCount())
		}
	})

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.mu.Unlock()

	// Register with meta-agent for monitoring
	if deps.metaAgent != nil {
		deps.metaAgent.RegisterAgent(agentID, agent.Type)
	}

	r.reportActivity()

	// Notify UI with task description so the activity feed shows what this
	// agent is actually working on.
	if onSubAgentActivity != nil {
		onSubAgentActivity(agent.ID, string(agent.Type), prompt, "", nil, "start")
	}

	// Apply per-agent timeout and store cancel func for explicit Cancel()
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

	// Ensure result is never nil (matches SpawnAsync pattern)
	if result == nil {
		result = &AgentResult{
			AgentID:   agent.ID,
			Type:      agent.Type,
			Status:    AgentStatusFailed,
			Error:     "nil result from agent",
			Completed: true,
		}
	}

	if err != nil {
		result.Error = err.Error()
		result.Status = AgentStatusFailed
	}

	// Ensure Completed is always true so WaitWithContext doesn't spin
	result.Completed = true

	// Save error checkpoint on failure for potential recovery
	if err != nil && r.store != nil && agent.GetTurnCount() > 0 {
		if _, cpErr := agent.SaveCheckpoint("error"); cpErr != nil {
			logging.Debug("failed to save error checkpoint", "agent_id", agent.ID, "error", cpErr)
		}
	}

	r.reportActivity()

	// Notify UI of completion so the feed stops showing this agent as
	// "running" after it finishes. Must match the "start" notification
	// added earlier — without this, agents pile up as permanent Running
	// entries in the activity feed.
	if onSubAgentActivity != nil {
		completionStatus := "complete"
		if err != nil || result.Status == AgentStatusFailed {
			completionStatus = "failed"
		}
		onSubAgentActivity(agent.ID, string(agent.Type), "", "", nil, completionStatus)
	}

	// Unregister from meta-agent
	if deps.metaAgent != nil {
		deps.metaAgent.UnregisterAgent(agentID)
	}

	workspaceErr := r.finalizeAgentWorkspace(agent, result)
	r.saveAgentState(agent)
	r.recordAgentExecutionLearning(deps, agentType, prompt, result, duration, "spawn_with_context")

	r.mu.Lock()
	r.results[agent.ID] = result
	r.mu.Unlock()
	r.notifyResultReady()

	if err == nil && workspaceErr != nil {
		err = workspaceErr
	}

	return agent.ID, result, err
}

// SpawnAsync creates and starts a new agent asynchronously.
// agentType should be "explore", "bash", "general", "plan", "claude-code-guide", or "coordinator".
func (r *Runner) SpawnAsync(ctx context.Context, agentType string, prompt string, maxTurns int, model string) string {
	deps := r.snapshotAgentDeps()
	agent := r.newConfiguredAgent(ctx, deps, agentType, maxTurns, model, deps.permissions)

	// Wire checkpoint store and enable auto-checkpoint for long-running agents
	if r.store != nil {
		agent.SetStore(r.store)
		if maxTurns > 10 {
			agent.EnableAutoCheckpoint(0)
		}
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
	onAgentProgress := r.onAgentProgress
	r.mu.Unlock()

	// Wire tool activity to both meta-agent and UI
	r.mu.RLock()
	onSubAgentActivity := r.onSubAgentActivity
	r.mu.RUnlock()

	if deps.metaAgent != nil {
		deps.metaAgent.RegisterAgent(agent.ID, agent.Type)
	}
	agent.SetOnToolActivity(func(id, toolName string, args map[string]any, status string) {
		if onSubAgentActivity != nil {
			onSubAgentActivity(id, string(agent.Type), "", toolName, args, "tool_"+status)
		}
		if deps.metaAgent != nil && status == "start" {
			deps.metaAgent.UpdateActivity(agent.ID, toolName, agent.GetTurnCount())
		}
	})

	// Report activity to coordinator
	r.reportActivity()

	// Notify UI about agent start
	if onStart != nil {
		onStart(agent.ID, agentType, prompt)
	}
	if onSubAgentActivity != nil {
		onSubAgentActivity(agent.ID, agentType, prompt, "", nil, "start")
	}

	// Run agent asynchronously with proper cleanup
	go func() {
		agentID := agent.ID

		// Ensure cleanup happens even on panic
		defer func() {
			if p := recover(); p != nil {
				// Was log-silent: panic was captured into result.Error
				// only. Without a log entry, post-mortem couldn't see
				// which agent type / spawn site faulted. Warn + stack.
				logging.Warn("spawned agent panicked",
					"agent_id", agentID,
					"panic", p,
					"stack", logging.PanicStack())
				r.mu.Lock()
				if result, ok := r.results[agentID]; ok {
					result.Error = fmt.Sprintf("agent panic: %v", p)
					result.Status = AgentStatusFailed
					result.Completed = true
				}
				r.mu.Unlock()
				r.notifyResultReady()
			}
		}()

		// Detach from caller's context so agent survives tool timeout.
		bgCtx := context.WithoutCancel(ctx)
		var agentCtx context.Context
		var agentCancel context.CancelFunc
		if agentTimeout := agent.GetTimeout(); agentTimeout > 0 {
			agentCtx, agentCancel = context.WithTimeout(bgCtx, agentTimeout)
		} else {
			agentCtx, agentCancel = context.WithCancel(bgCtx)
		}
		defer agentCancel()

		// Store cancel func for explicit Agent.Cancel()
		agent.SetCancelFunc(agentCancel)

		// Check if original context is already cancelled
		select {
		case <-ctx.Done():
			if deps.metaAgent != nil {
				deps.metaAgent.UnregisterAgent(agentID)
			}
			cancelledResult := &AgentResult{
				AgentID:   agentID,
				Type:      agent.Type,
				Status:    AgentStatusCancelled,
				Error:     ctx.Err().Error(),
				Completed: true,
			}
			r.finalizeAgentWorkspace(agent, cancelledResult)
			r.mu.Lock()
			r.results[agentID] = cancelledResult
			r.mu.Unlock()
			r.notifyResultReady()
			return
		default:
		}

		// Start progress ticker for periodic updates
		progressTicker := time.NewTicker(2 * time.Second)
		defer progressTicker.Stop()

		progressCtx, progressCancel := context.WithCancel(agentCtx)
		defer progressCancel()

		go func() {
			for {
				select {
				case <-progressTicker.C:
					progress := agent.GetProgress()
					if onAgentProgress != nil {
						onAgentProgress(agentID, &progress)
					}
				case <-progressCtx.Done():
					return
				}
			}
		}()

		startTime := time.Now()
		result, err := agent.Run(agentCtx, prompt)
		duration := time.Since(startTime)

		// Ensure result is never nil
		if result == nil {
			result = &AgentResult{
				AgentID:   agentID,
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

		// Notify UI about agent completion (SpawnAsync path)
		if onSubAgentActivity != nil {
			completionStatus := "complete"
			if err != nil || result.Status == AgentStatusFailed {
				completionStatus = "failed"
			}
			onSubAgentActivity(agentID, string(agent.Type), "", "", nil, completionStatus)
		}

		// Unregister from meta-agent
		if deps.metaAgent != nil {
			deps.metaAgent.UnregisterAgent(agentID)
		}

		// Save error checkpoint on failure for potential recovery
		if err != nil && r.store != nil && agent.GetTurnCount() > 0 {
			if _, cpErr := agent.SaveCheckpoint("error"); cpErr != nil {
				logging.Debug("failed to save error checkpoint", "agent_id", agent.ID, "error", cpErr)
			}
		}

		// Save agent state for potential resume
		r.finalizeAgentWorkspace(agent, result)
		r.saveAgentState(agent)
		r.recordAgentExecutionLearning(deps, agentType, prompt, result, duration, "spawn_async")

		r.mu.Lock()
		r.results[agentID] = result
		r.mu.Unlock()
		r.notifyResultReady()

		// Notify UI about agent completion
		if onComplete != nil {
			onComplete(agentID, result)
		}
	}()

	return agent.ID
}

// SpawnAsyncWithStreaming creates and starts a new agent asynchronously with streaming support.
// The onText callback receives real-time text output from the agent.
// The onProgress callback receives progress updates.
func (r *Runner) SpawnAsyncWithStreaming(
	ctx context.Context,
	agentType string,
	prompt string,
	maxTurns int,
	model string,
	onText func(string),
	onProgress func(id string, progress *AgentProgress),
) string {
	deps := r.snapshotAgentDeps()
	agent := r.newConfiguredAgent(ctx, deps, agentType, maxTurns, model, deps.permissions)

	// Set up streaming callbacks
	if onText != nil {
		agent.SetOnText(onText)
	}

	// Wire thinking callback from runner
	r.mu.RLock()
	onThinking := r.onThinking
	r.mu.RUnlock()
	if onThinking != nil {
		agent.SetOnThinking(onThinking)
	}

	// Wire sub-agent activity callback (chain with meta-agent UpdateActivity)
	r.mu.RLock()
	onSubAgentActivity := r.onSubAgentActivity
	r.mu.RUnlock()
	agent.SetOnToolActivity(func(id, toolName string, args map[string]any, status string) {
		if onSubAgentActivity != nil {
			onSubAgentActivity(id, string(agent.Type), "", toolName, args, "tool_"+status)
		}
		if deps.metaAgent != nil && status == "start" {
			deps.metaAgent.UpdateActivity(agent.ID, toolName, agent.GetTurnCount())
		}
	})

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.results[agent.ID] = &AgentResult{
		AgentID: agent.ID,
		Type:    agent.Type,
		Status:  AgentStatusPending,
	}
	onStart := r.onAgentStart
	onComplete := r.onAgentComplete
	onAgentProgress := r.onAgentProgress
	r.mu.Unlock()

	// Register with meta-agent for monitoring
	if deps.metaAgent != nil {
		deps.metaAgent.RegisterAgent(agent.ID, agent.Type)
	}

	// Report activity to coordinator
	r.reportActivity()

	// Notify UI about agent start
	if onStart != nil {
		onStart(agent.ID, agentType, prompt)
	}
	if onSubAgentActivity != nil {
		onSubAgentActivity(agent.ID, agentType, prompt, "", nil, "start")
	}

	// Run agent asynchronously with streaming and progress updates
	go func() {
		agentID := agent.ID

		// Ensure cleanup happens even on panic
		defer func() {
			if p := recover(); p != nil {
				// Was log-silent: panic was captured into result.Error
				// only. Without a log entry, post-mortem couldn't see
				// which agent type / spawn site faulted. Warn + stack.
				logging.Warn("spawned agent panicked",
					"agent_id", agentID,
					"panic", p,
					"stack", logging.PanicStack())
				r.mu.Lock()
				if result, ok := r.results[agentID]; ok {
					result.Error = fmt.Sprintf("agent panic: %v", p)
					result.Status = AgentStatusFailed
					result.Completed = true
				}
				r.mu.Unlock()
				r.notifyResultReady()
			}
		}()

		// Detach from caller's context so agent survives tool timeout.
		bgCtx := context.WithoutCancel(ctx)
		var agentCtx context.Context
		var agentCancel context.CancelFunc
		if agentTimeout := agent.GetTimeout(); agentTimeout > 0 {
			agentCtx, agentCancel = context.WithTimeout(bgCtx, agentTimeout)
		} else {
			agentCtx, agentCancel = context.WithCancel(bgCtx)
		}
		defer agentCancel()

		// Store cancel func for explicit Agent.Cancel()
		agent.SetCancelFunc(agentCancel)

		// Start progress ticker for periodic updates
		progressTicker := time.NewTicker(2 * time.Second)
		defer progressTicker.Stop()

		// Create a context with cancellation for the progress goroutine
		progressCtx, progressCancel := context.WithCancel(agentCtx)
		defer progressCancel()

		// Progress update goroutine
		go func() {
			for {
				select {
				case <-progressTicker.C:
					progress := agent.GetProgress()
					if onProgress != nil {
						onProgress(agentID, &progress)
					}
					if onAgentProgress != nil {
						onAgentProgress(agentID, &progress)
					}
				case <-progressCtx.Done():
					return
				}
			}
		}()

		// Check if original context is already cancelled
		select {
		case <-ctx.Done():
			if deps.metaAgent != nil {
				deps.metaAgent.UnregisterAgent(agentID)
			}
			cancelledResult := &AgentResult{
				AgentID:   agentID,
				Type:      agent.Type,
				Status:    AgentStatusCancelled,
				Error:     ctx.Err().Error(),
				Completed: true,
			}
			r.finalizeAgentWorkspace(agent, cancelledResult)
			r.mu.Lock()
			r.results[agentID] = cancelledResult
			r.mu.Unlock()
			r.notifyResultReady()
			return
		default:
		}

		startTime := time.Now()
		result, err := agent.Run(agentCtx, prompt)
		duration := time.Since(startTime)

		// Ensure result is never nil
		if result == nil {
			result = &AgentResult{
				AgentID:   agentID,
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

		// Notify UI of completion — matches the "start" notification we
		// send from this path. Without this the activity feed would keep
		// showing a long-since-finished agent as Running forever.
		if onSubAgentActivity != nil {
			completionStatus := "complete"
			if err != nil || result.Status == AgentStatusFailed {
				completionStatus = "failed"
			}
			onSubAgentActivity(agentID, string(agent.Type), "", "", nil, completionStatus)
		}

		// Unregister from meta-agent
		if deps.metaAgent != nil {
			deps.metaAgent.UnregisterAgent(agentID)
		}

		// Save agent state for potential resume
		r.finalizeAgentWorkspace(agent, result)
		r.saveAgentState(agent)
		r.recordAgentExecutionLearning(deps, agentType, prompt, result, duration, "spawn_async_streaming")

		r.mu.Lock()
		r.results[agentID] = result
		r.mu.Unlock()
		r.notifyResultReady()

		// Notify UI about agent completion
		if onComplete != nil {
			onComplete(agentID, result)
		}
	}()

	return agent.ID
}

// SpawnMultiple creates and starts multiple agents in parallel.
func (r *Runner) SpawnMultiple(ctx context.Context, tasks []AgentTask) ([]string, error) {
	ids := make([]string, len(tasks))
	results := make([]*AgentResult, len(tasks))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t AgentTask) {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					logging.Error("panic in SpawnMultiple worker",
						"idx", idx,
						"type", t.Type,
						"panic", p,
						"stack", logging.PanicStack())
					mu.Lock()
					if results[idx] == nil {
						results[idx] = &AgentResult{
							Type:      AgentType(t.Type),
							Status:    AgentStatusFailed,
							Error:     fmt.Sprintf("internal panic: %v", p),
							Completed: true,
						}
					}
					mu.Unlock()
				}
			}()

			deps := r.snapshotAgentDeps()
			agent := r.newConfiguredAgent(ctx, deps, string(t.Type), t.MaxTurns, t.Model, deps.permissions)

			// Apply thoroughness from task or context
			th := tools.ThoroughnessFromContext(ctx)
			if t.Thoroughness != "" {
				th = tools.ParseThoroughness(t.Thoroughness)
			}
			agent.ApplyThoroughness(th, t.MaxTurns)
			os := tools.OutputStyleFromContext(ctx)
			if t.OutputStyle != "" {
				os = tools.ParseOutputStyle(t.OutputStyle)
			}
			agent.SetOutputStyle(os)

			r.mu.Lock()
			r.agents[agent.ID] = agent
			r.mu.Unlock()

			attachMetaAgentMonitoring(agent, deps.metaAgent)

			// Apply per-agent timeout
			runCtx := ctx
			if agentTimeout := agent.GetTimeout(); agentTimeout > 0 {
				var cancel context.CancelFunc
				runCtx, cancel = context.WithTimeout(ctx, agentTimeout)
				defer cancel()
			}
			startTime := time.Now()
			result, err := agent.Run(runCtx, t.Prompt)
			duration := time.Since(startTime)

			// Ensure result is never nil (matches SpawnAsync pattern)
			if result == nil {
				result = &AgentResult{
					AgentID:   agent.ID,
					Type:      agent.Type,
					Status:    AgentStatusFailed,
					Error:     "nil result from agent",
					Completed: true,
				}
			}
			if err != nil {
				result.Error = err.Error()
				result.Status = AgentStatusFailed
			}
			result.Completed = true

			// Unregister from meta-agent
			if deps.metaAgent != nil {
				deps.metaAgent.UnregisterAgent(agent.ID)
			}

			workspaceErr := r.finalizeAgentWorkspace(agent, result)
			r.saveAgentState(agent)
			r.recordAgentExecutionLearning(deps, string(t.Type), t.Prompt, result, duration, "spawn_multiple")
			if err == nil && workspaceErr != nil {
				err = workspaceErr
			}

			mu.Lock()
			ids[idx] = agent.ID
			results[idx] = result
			if err != nil && firstErr == nil {
				firstErr = err
			}
			mu.Unlock()

			r.mu.Lock()
			r.results[agent.ID] = result
			r.mu.Unlock()
			r.notifyResultReady()
		}(i, task)
	}

	wg.Wait()

	return ids, firstErr
}
