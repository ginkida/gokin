package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"gokin/internal/logging"
	"gokin/internal/permission"
	"gokin/internal/tools"
)

// fewShotExampleLimit caps how many past successful runs are injected as
// few-shot context into a spawn prompt.
const fewShotExampleLimit = 2

var approvedPlanStepPermissionOverrides = map[string]permission.Level{
	"write":       permission.LevelAllow,
	"atomicwrite": permission.LevelAllow,
	"edit":        permission.LevelAllow,
	"copy":        permission.LevelAllow,
	"move":        permission.LevelAllow,
	"mkdir":       permission.LevelAllow,
	"run_tests":   permission.LevelAllow,
}

func approvedPlanStepPermissions(base *permission.Manager) *permission.Manager {
	if base == nil {
		return nil
	}
	return base.WithPolicyOverrides(approvedPlanStepPermissionOverrides)
}

// shouldPersistAgentErrorCheckpoint keeps auto-recovery for genuine execution
// failures only. Context cancellation and deadline expiry are terminal control
// flow: silently resuming either on the next app start can repeat tool calls the
// user stopped or restart work that already exhausted its configured limit.
func shouldPersistAgentErrorCheckpoint(err error) bool {
	return err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded)
}

func applyAgentRunError(result *AgentResult, err error) {
	result.Error = err.Error()
	result.Status = AgentStatusFailed
	if errors.Is(err, context.Canceled) {
		result.Status = AgentStatusCancelled
	}
}

// runAgentWithPanicRecovery confines a panic to the Agent.Run boundary while
// preserving enough terminal provenance for synchronous callers to make a
// fail-closed retry decision. In particular, Runner.Spawn must still publish a
// result before returning its non-empty agent ID: Router inspects that result to
// distinguish a safe pre-execution failure from a panic after a stateful tool.
//
// Keep this recovery scoped to Agent.Run. The normal Spawn lifecycle below
// remains responsible for workspace finalization, persistence, result delivery,
// and observer notifications, exactly as it is for an ordinary returned error.
func runAgentWithPanicRecovery(agent *Agent, ctx context.Context, prompt, spawnSite string) (result *AgentResult, err error) {
	started := time.Now()
	defer func() {
		panicValue := recover()
		if panicValue == nil {
			return
		}

		logging.Warn("synchronous agent run panicked",
			"agent_id", agent.ID,
			"spawn_site", spawnSite,
			"panic", panicValue,
			"stack", logging.PanicStack())

		ended := time.Now()
		agent.stateMu.Lock()
		agent.status = AgentStatusFailed
		agent.endTime = ended
		inputTokens := agent.usageInputTokens
		outputTokens := agent.usageOutputTokens
		cacheReadTokens := agent.usageCacheReadTokens
		estimatedCost := agent.usageEstimatedCost
		costTracked := agent.usageCostTracked
		invocationScope := agent.invocationScope
		agent.stateMu.Unlock()

		err = fmt.Errorf("agent panic during %s: %v", spawnSite, panicValue)
		result = &AgentResult{
			AgentID:              agent.ID,
			Type:                 agent.Type,
			Status:               AgentStatusFailed,
			Error:                err.Error(),
			Duration:             ended.Sub(started),
			Completed:            true,
			PolicyBlock:          agent.policyBlockSnapshot(),
			InputTokens:          inputTokens,
			OutputTokens:         outputTokens,
			CacheReadInputTokens: cacheReadTokens,
			EstimatedCost:        estimatedCost,
			CostTracked:          costTracked,
			InvocationScope:      invocationScope,
			MutatingToolCalls:    agent.MutatingToolCount(),
			StatefulToolAttempts: agent.StatefulToolAttemptCount(),
			TouchedPaths:         agent.GetTouchedPaths(),
		}
		agent.populateResultIdentity(result)
	}()

	return agent.Run(ctx, prompt)
}

// withFewShot prepends the most relevant past SUCCESSFUL runs (matched by prompt
// tags, sorted by relevance×success) to a spawn prompt so the agent learns from
// what worked before. This closes the ExampleStore loop — it was write-only
// (LearnFromSuccess persisted runs but nothing ever read them back). Returns the
// prompt unchanged when there's no example store or no tag overlap (common case),
// so it's a no-op except where it genuinely helps.
func (r *Runner) withFewShot(deps runnerAgentDeps, agentType, prompt string) string {
	if deps.exampleStore == nil {
		return prompt
	}
	examples := deps.exampleStore.GetExamplesForContext(agentType, prompt, fewShotExampleLimit)
	if examples == "" {
		return prompt
	}
	return examples + "\n---\n\nNow handle this request:\n\n" + prompt
}

// Spawn creates and starts a new agent with the given task.
// agentType should be "explore", "bash", "general", "plan", "claude-code-guide", or "coordinator".
// Also supports dynamic types registered via AgentTypeRegistry.
func (r *Runner) Spawn(ctx context.Context, agentType string, prompt string, maxTurns int, model string) (string, error) {
	// Cleanup old completed agents and results to prevent unbounded growth
	r.cleanupOldResults()

	deps := r.snapshotAgentDeps()
	agent := r.newConfiguredAgent(ctx, deps, agentType, maxTurns, model, deps.permissions)
	runLease, err := r.acquireAgentRunLease(agent.ID)
	if err != nil {
		return "", err
	}
	defer runLease.Release()

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
	agent.SetOnToolActivity(func(id, toolName string, args map[string]any, status string, success bool, summary string) {
		invokeSubAgentActivity(onSubAgentActivity, id, string(agent.Type), "", toolName, args, "tool_"+status, success, summary)
		if deps.metaAgent != nil && status == "start" {
			deps.metaAgent.UpdateActivity(agent.ID, toolName, agent.GetTurnCount())
		}
	})

	// Report activity to coordinator
	r.reportActivity()

	// Run context + cancel registration BEFORE the start event: without the
	// SetCancelFunc, Runner.Cancel/task_stop flipped the agent's STATUS to
	// cancelled and reported "stopped" while the run itself kept executing to
	// completion — the only Spawn variant missing the registration
	// (SpawnWithContext and both async paths already do it). Registering
	// before the start notification means a Cancel racing the very first
	// activity event already has a live cancel func to kill.
	var runCtx context.Context
	var runCancel context.CancelFunc
	runCtx, runCancel = agentRunContext(ctx, agent)
	defer runCancel()
	agent.SetCancelFunc(runCancel)

	// Notify UI about agent start — pass the prompt so the feed can surface
	// what this agent is actually working on (instead of "Sub-agent: general").
	invokeSubAgentActivity(onSubAgentActivity, agent.ID, string(agent.Type), prompt, "", nil, "start", false, "")

	startTime := time.Now()
	result, err := runAgentWithPanicRecovery(agent, runCtx, r.withFewShot(deps, agentType, prompt), "spawn")
	duration := time.Since(startTime)

	// Notify UI about agent completion
	status := "complete"
	if err != nil {
		status = "failed"
	}
	invokeSubAgentActivity(onSubAgentActivity, agent.ID, string(agent.Type), "", "", nil, status, false, "")

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
	r.reportAgentUsage(agent.ID, result)

	r.mu.Lock()
	r.results[agent.ID] = cloneAgentResult(result)
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

// SpawnWithContext creates and runs a sub-agent with project context and streaming.
// Unlike Spawn, it returns the AgentResult directly for immediate use by the caller.
// When skipPermissions is true, the sub-agent receives a bounded local-work
// capability for an approved plan step. It does not bypass the permission
// system: external, destructive, and otherwise-unlisted operations retain the
// base policy and prompt behavior.

// agentRunContext derives the run context for a spawned agent. The agent's
// own timeout (DefaultAgentTimeout / thoroughness-tuned) is a DEFAULT SAFETY
// CAP for spawns whose parent context carries NO deadline (a foreground
// task-tool spawn on the undeadlined processing ctx). When the caller DID
// budget the run with an explicit deadline — a /loop iterationCtx (30m), an
// eval run — that outer budget wins: stacking the agent's smaller default
// under it silently clipped loop iterations at 10m while the loop budget said
// 30m (v0.100.103 field report: "general ✗ 10.0m" — a productive loop agent
// was killed by DefaultAgentTimeout mid-write, and the v0.100.102 iteration
// budget raise was unreachable behind it).
func agentRunContext(ctx context.Context, agent *Agent) (context.Context, context.CancelFunc) {
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return context.WithCancel(ctx)
	}
	if t := agent.GetTimeout(); t > 0 {
		return context.WithTimeout(ctx, t)
	}
	return context.WithCancel(ctx)
}

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
	r.cleanupOldResults()
	deps := r.snapshotAgentDeps()

	perms := deps.permissions
	if skipPermissions {
		perms = approvedPlanStepPermissions(deps.permissions)
	}
	agent := r.newConfiguredAgent(ctx, deps, agentType, maxTurns, model, perms)
	runLease, err := r.acquireAgentRunLease(agent.ID)
	if err != nil {
		return "", nil, err
	}
	defer runLease.Release()

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
	agent.SetOnToolActivity(func(id, toolName string, args map[string]any, status string, success bool, summary string) {
		invokeSubAgentActivity(onSubAgentActivity, id, string(agent.Type), "", toolName, args, "tool_"+status, success, summary)
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
	invokeSubAgentActivity(onSubAgentActivity, agent.ID, string(agent.Type), prompt, "", nil, "start", false, "")

	// Apply per-agent timeout and store cancel func for explicit Cancel()
	var runCtx context.Context
	var runCancel context.CancelFunc
	runCtx, runCancel = agentRunContext(ctx, agent)
	defer runCancel()
	agent.SetCancelFunc(runCancel)

	startTime := time.Now()
	result, err := runAgentWithPanicRecovery(agent, runCtx, r.withFewShot(deps, agentType, prompt), "spawn_with_context")
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
		applyAgentRunError(result, err)
	}

	// Ensure Completed is always true so WaitWithContext doesn't spin
	result.Completed = true

	// Save genuine failures for recovery. Cancellation/deadline are terminal
	// control flow and must never be silently replayed on the next app start.
	if shouldPersistAgentErrorCheckpoint(err) && r.store != nil && agent.GetTurnCount() > 0 {
		if _, cpErr := agent.SaveCheckpoint("error"); cpErr != nil {
			logging.Debug("failed to save error checkpoint", "agent_id", agent.ID, "error", cpErr)
		}
	}

	r.reportActivity()

	// Notify UI of completion so the feed stops showing this agent as
	// "running" after it finishes. Must match the "start" notification
	// added earlier — without this, agents pile up as permanent Running
	// entries in the activity feed.
	completionStatus := "complete"
	if err != nil || result.Status == AgentStatusFailed {
		completionStatus = "failed"
	}
	invokeSubAgentActivity(onSubAgentActivity, agent.ID, string(agent.Type), "", "", nil, completionStatus, false, "")

	// Unregister from meta-agent
	if deps.metaAgent != nil {
		deps.metaAgent.UnregisterAgent(agentID)
	}

	workspaceErr := r.finalizeAgentWorkspace(agent, result)
	r.saveAgentState(agent)
	r.recordAgentExecutionLearning(deps, agentType, prompt, result, duration, "spawn_with_context")
	r.reportAgentUsage(agent.ID, result)

	r.mu.Lock()
	r.results[agent.ID] = cloneAgentResult(result)
	r.mu.Unlock()
	r.notifyResultReady(agent.ID)

	if err == nil && workspaceErr != nil {
		err = workspaceErr
	}

	return agent.ID, result, err
}

// SpawnAsync creates and starts a new agent asynchronously.
// agentType should be "explore", "bash", "general", "plan", "claude-code-guide", or "coordinator".
func (r *Runner) SpawnAsync(ctx context.Context, agentType string, prompt string, maxTurns int, model string) string {
	r.cleanupOldResults()
	deps := r.snapshotAgentDeps()
	agent := r.newConfiguredAgent(ctx, deps, agentType, maxTurns, model, deps.permissions)
	runLease, leaseErr := r.acquireAgentRunLease(agent.ID)
	if leaseErr != nil {
		logging.Error("refusing duplicate async agent run", "agent_id", agent.ID, "error", leaseErr)
		return ""
	}
	leaseTransferred := false
	defer func() {
		if !leaseTransferred {
			runLease.Release()
		}
	}()

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
	agent.SetOnToolActivity(func(id, toolName string, args map[string]any, status string, success bool, summary string) {
		invokeSubAgentActivity(onSubAgentActivity, id, string(agent.Type), "", toolName, args, "tool_"+status, success, summary)
		if deps.metaAgent != nil && status == "start" {
			deps.metaAgent.UpdateActivity(agent.ID, toolName, agent.GetTurnCount())
		}
	})

	// Report activity to coordinator
	r.reportActivity()

	// Notify UI about agent start
	invokeAgentStart(onStart, agent.ID, agentType, prompt)
	invokeSubAgentActivity(onSubAgentActivity, agent.ID, agentType, prompt, "", nil, "start", false, "")

	// Run agent asynchronously with proper cleanup
	leaseTransferred = true
	go func() {
		defer runLease.Release()
		agentID := agent.ID
		var result *AgentResult

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
				if result == nil {
					result = &AgentResult{AgentID: agentID, Type: agent.Type}
				}
				result.Error = fmt.Sprintf("agent panic: %v", p)
				result.Status = AgentStatusFailed
				result.Completed = false
				// finalizeAgentWorkspace does git/file I/O — call it lock-free,
				// then publish atomically. Completed used to become visible before
				// this call mutated Metadata, so waiters could race the cleanup.
				// Reusing the local result also makes its finalized guard effective
				// if the panic happened after normal workspace finalization.
				r.finalizeAgentWorkspace(agent, result)
				result.Completed = true
				if deps.metaAgent != nil {
					deps.metaAgent.UnregisterAgent(agentID)
				}
				r.mu.Lock()
				r.results[agentID] = result
				r.mu.Unlock()
				r.notifyResultReady(agentID)
			}
		}()

		// Detach from caller's context so agent survives tool timeout.
		bgCtx := context.WithoutCancel(ctx)
		var agentCtx context.Context
		var agentCancel context.CancelFunc
		agentCtx, agentCancel = agentRunContext(bgCtx, agent)
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
			r.notifyResultReady(agentID)
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
					invokeAgentProgress(onAgentProgress, agentID, &progress)
				case <-progressCtx.Done():
					return
				}
			}
		}()

		startTime := time.Now()
		var err error
		result, err = agent.Run(agentCtx, r.withFewShot(deps, agentType, prompt))
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
			applyAgentRunError(result, err)
		}

		// Ensure Completed is always true so WaitWithContext doesn't spin
		result.Completed = true

		// Notify UI about agent completion (SpawnAsync path)
		completionStatus := "complete"
		if err != nil || result.Status == AgentStatusFailed {
			completionStatus = "failed"
		}
		invokeSubAgentActivity(onSubAgentActivity, agentID, string(agent.Type), "", "", nil, completionStatus, false, "")

		// Unregister from meta-agent
		if deps.metaAgent != nil {
			deps.metaAgent.UnregisterAgent(agentID)
		}

		// Save genuine failures for recovery. Cancellation/deadline are terminal
		// control flow and must never be silently replayed on the next app start.
		if shouldPersistAgentErrorCheckpoint(err) && r.store != nil && agent.GetTurnCount() > 0 {
			if _, cpErr := agent.SaveCheckpoint("error"); cpErr != nil {
				logging.Debug("failed to save error checkpoint", "agent_id", agent.ID, "error", cpErr)
			}
		}

		// Save agent state for potential resume
		r.finalizeAgentWorkspace(agent, result)
		r.saveAgentState(agent)
		r.recordAgentExecutionLearning(deps, agentType, prompt, result, duration, "spawn_async")
		r.reportAgentUsage(agentID, result)

		r.mu.Lock()
		r.results[agentID] = result
		r.mu.Unlock()
		r.notifyResultReady(agentID)

		// Notify UI about agent completion
		invokeAgentComplete(onComplete, agentID, result)
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
	runLease, leaseErr := r.acquireAgentRunLease(agent.ID)
	if leaseErr != nil {
		logging.Error("refusing duplicate streaming agent run", "agent_id", agent.ID, "error", leaseErr)
		return ""
	}
	leaseTransferred := false
	defer func() {
		if !leaseTransferred {
			runLease.Release()
		}
	}()

	// Streaming is only a presentation variant of SpawnAsync. Keep the same
	// persistence contract: long-running agents checkpoint as they progress and
	// genuine terminal failures leave an error checkpoint for startup recovery.
	// Without this wiring, the common background-task path (selected whenever a
	// text/progress callback is present) was the one path that could not recover
	// after a provider failure or process restart.
	if r.store != nil {
		agent.SetStore(r.store)
		if maxTurns > 10 {
			agent.EnableAutoCheckpoint(0)
		}
	}

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
	agent.SetOnToolActivity(func(id, toolName string, args map[string]any, status string, success bool, summary string) {
		invokeSubAgentActivity(onSubAgentActivity, id, string(agent.Type), "", toolName, args, "tool_"+status, success, summary)
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
	invokeAgentStart(onStart, agent.ID, agentType, prompt)
	invokeSubAgentActivity(onSubAgentActivity, agent.ID, agentType, prompt, "", nil, "start", false, "")

	// Run agent asynchronously with streaming and progress updates
	leaseTransferred = true
	go func() {
		defer runLease.Release()
		agentID := agent.ID
		var result *AgentResult

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
				if result == nil {
					result = &AgentResult{AgentID: agentID, Type: agent.Type}
				}
				result.Error = fmt.Sprintf("agent panic: %v", p)
				result.Status = AgentStatusFailed
				result.Completed = false
				r.finalizeAgentWorkspace(agent, result)
				result.Completed = true
				if deps.metaAgent != nil {
					deps.metaAgent.UnregisterAgent(agentID)
				}
				r.mu.Lock()
				r.results[agentID] = result
				r.mu.Unlock()
				r.notifyResultReady(agentID)
			}
		}()

		// Detach from caller's context so agent survives tool timeout.
		bgCtx := context.WithoutCancel(ctx)
		var agentCtx context.Context
		var agentCancel context.CancelFunc
		agentCtx, agentCancel = agentRunContext(bgCtx, agent)
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
					invokeAgentProgress(onProgress, agentID, &progress)
					invokeAgentProgress(onAgentProgress, agentID, &progress)
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
			r.notifyResultReady(agentID)
			return
		default:
		}

		startTime := time.Now()
		var err error
		result, err = agent.Run(agentCtx, r.withFewShot(deps, agentType, prompt))
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
			applyAgentRunError(result, err)
		}

		// Ensure Completed is always true so WaitWithContext doesn't spin
		result.Completed = true

		// Notify UI of completion — matches the "start" notification we
		// send from this path. Without this the activity feed would keep
		// showing a long-since-finished agent as Running forever.
		completionStatus := "complete"
		if err != nil || result.Status == AgentStatusFailed {
			completionStatus = "failed"
		}
		invokeSubAgentActivity(onSubAgentActivity, agentID, string(agent.Type), "", "", nil, completionStatus, false, "")

		// Unregister from meta-agent
		if deps.metaAgent != nil {
			deps.metaAgent.UnregisterAgent(agentID)
		}

		// Match SpawnAsync: cancellation/deadline are intentional terminal
		// control flow and must not be replayed, while genuine failures remain
		// resumable after restart.
		if shouldPersistAgentErrorCheckpoint(err) && r.store != nil && agent.GetTurnCount() > 0 {
			if _, cpErr := agent.SaveCheckpoint("error"); cpErr != nil {
				logging.Debug("failed to save error checkpoint", "agent_id", agent.ID, "error", cpErr)
			}
		}

		// Save agent state for potential resume
		r.finalizeAgentWorkspace(agent, result)
		r.saveAgentState(agent)
		r.recordAgentExecutionLearning(deps, agentType, prompt, result, duration, "spawn_async_streaming")
		r.reportAgentUsage(agentID, result)

		r.mu.Lock()
		r.results[agentID] = result
		r.mu.Unlock()
		r.notifyResultReady(agentID)

		// Notify UI about agent completion
		invokeAgentComplete(onComplete, agentID, result)
	}()

	return agent.ID
}

// SpawnMultiple creates and starts multiple agents in parallel.
func (r *Runner) SpawnMultiple(ctx context.Context, tasks []AgentTask) ([]string, error) {
	r.cleanupOldResults()
	ids := make([]string, len(tasks))
	results := make([]*AgentResult, len(tasks))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t AgentTask) {
			defer wg.Done()
			var runLease *agentRunLease
			// Registered before panic recovery so recovery/final accounting runs
			// while this worker still owns its logical agent ID.
			defer func() { runLease.Release() }()
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
			var leaseErr error
			runLease, leaseErr = r.acquireAgentRunLease(agent.ID)
			if leaseErr != nil {
				mu.Lock()
				results[idx] = &AgentResult{
					AgentID:   agent.ID,
					Type:      agent.Type,
					Status:    AgentStatusFailed,
					Error:     leaseErr.Error(),
					Completed: true,
				}
				if firstErr == nil {
					firstErr = leaseErr
				}
				mu.Unlock()
				return
			}

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

			// Apply per-agent timeout. ALWAYS wrap in a cancellable ctx and
			// register it via SetCancelFunc — the same "task_stop lies" bug
			// class fixed for the sync Spawn path (v0.100.78): without this,
			// Runner.Cancel/task_stop on a SpawnMultiple agent (this is the
			// fan-out primitive /audit's find/verify phases use) flipped the
			// STATUS to cancelled and reported "stopped" while the run itself
			// kept executing to its own timeout.
			var runCtx context.Context
			var runCancel context.CancelFunc
			runCtx, runCancel = agentRunContext(ctx, agent)
			defer runCancel()
			agent.SetCancelFunc(runCancel)
			startTime := time.Now()
			result, err := agent.Run(runCtx, r.withFewShot(deps, string(t.Type), t.Prompt))
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
			r.reportAgentUsage(agent.ID, result)
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
			r.notifyResultReady(agent.ID)
		}(i, task)
	}

	wg.Wait()

	return ids, firstErr
}
