package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gokin/internal/agent"
	"gokin/internal/client"
	appcontext "gokin/internal/context"
	"gokin/internal/logging"
	"gokin/internal/plan"
	"gokin/internal/tools"
	"gokin/internal/ui"

	"google.golang.org/genai"
)

const (
	// planStepOutputMaxChars is the max characters stored per step output.
	planStepOutputMaxChars = 8000
	// planStepVerifyTimeout bounds orchestrator-run verify_commands per step.
	planStepVerifyTimeout = 2 * time.Minute
	// planSummaryMaxChars is the max characters for previous steps summary context.
	planSummaryMaxChars = 2000
	// messageIdleTimeout is the maximum time without any model activity
	// (text, tool calls, thinking) before we cancel message processing.
	// Unlike a wall-clock timeout, this survives system sleep/wake cycles
	// because the heartbeat freezes during sleep and resumes on wake.
	messageIdleTimeout = 10 * time.Minute
	// idleCheckInterval is how often we check for idle timeout.
	idleCheckInterval = 30 * time.Second
)

// processMessageWithContext handles user messages with full context management.
func (a *App) processMessageWithContext(ctx context.Context, message string) {
	a.journalEvent("request_started", map[string]any{
		"message_preview": previewForJournal(message),
	})
	a.saveRecoverySnapshot("")

	// Group all file changes from this message for atomic undo.
	if a.undoManager != nil {
		groupID := fmt.Sprintf("msg-%d", time.Now().UnixNano())
		a.undoManager.SetActiveGroup(groupID)
		defer a.undoManager.ClearActiveGroup()
	}

	// Activity-based idle timeout: cancel if no model activity (text, tool calls,
	// thinking) for messageIdleTimeout. Unlike wall-clock context.WithTimeout,
	// this survives system sleep/wake — heartbeat freezes during sleep, and
	// resumes when callbacks fire on wake.
	// PlanningTimeout is for individual plan-step LLM calls (default 60s)
	// and must NOT be used here — it would kill normal conversations.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.touchStepHeartbeat()
	go func() {
		ticker := time.NewTicker(idleCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if age := a.stepHeartbeatAge(); age > messageIdleTimeout {
					logging.Warn("message processing idle timeout",
						"idle", age.Round(time.Second).String())
					cancel()
					return
				}
			}
		}
	}()

	defer func() {
		a.mu.Lock()
		a.processing = false
		a.mu.Unlock()
		if a.executor != nil {
			a.executor.SetSideEffectDedup(false)
		}

		// Check for pending message and process it
		a.pendingMu.Lock()
		pending := a.pendingMessage
		a.pendingMessage = ""
		a.pendingMu.Unlock()

		if pending != "" {
			// Notify user that we're processing pending message
			a.safeSendToProgram(ui.StreamTextMsg("\n📤 Processing queued message...\n"))
			// Recursively handle the pending message
			go a.handleSubmit(pending)
		}
		a.saveRecoverySnapshot("")
	}()

	// Track response start time and reset tools used
	a.mu.Lock()
	a.responseStartTime = time.Now()
	a.responseToolsUsed = nil
	a.streamedChars = 0 // Reset streaming accumulator
	a.messageCount++
	a.diffBatchDecision = ui.DiffPending
	currentMsgCount := a.messageCount
	a.mu.Unlock()

	// New top-level request: clear side-effect ledger.
	if a.executor != nil {
		a.executor.ResetSideEffectLedger()
		a.executor.SetSideEffectDedup(false)
	}

	// Reset stale in_progress todos from previous turn.
	// The model will re-set them to in_progress as it works.
	if tt := a.GetTodoTool(); tt != nil {
		tt.ResetInProgress()
	}

	// === Task 5.8: Inject tool hints every 10 messages ===
	if currentMsgCount > 0 && currentMsgCount%10 == 0 && a.promptBuilder != nil {
		hints := a.getToolHints()
		a.promptBuilder.SetToolHints(hints)
		if hints != "" {
			logging.Debug("tool hints injected", "message_count", currentMsgCount, "hints_length", len(hints))
		}
	}

	// Set last user message for conditional planning protocol injection.
	if a.promptBuilder != nil {
		a.promptBuilder.SetLastMessage(message)
	}

	// Keep dynamic system instruction in sync (contract/memory/hints can change between turns).
	a.refreshSystemInstruction()

	// Prepare context (check tokens, optimize if needed)
	if a.contextManager != nil {
		if err := a.contextManager.PrepareForRequest(ctx); err != nil {
			logging.Debug("failed to prepare context", "error", err)
		}

		// Send token usage to UI BEFORE request (after optimization)
		// This shows the actual context size that will be sent
		a.sendTokenUsageUpdate()
	}

	// Get current history
	history := a.session.GetHistory()

	// Inject error context if retrying after a recent failure
	a.mu.Lock()
	if a.lastError != "" && time.Since(a.lastErrorTime) < 2*time.Minute {
		message = fmt.Sprintf("[Note: previous attempt failed with: %s. The context from that attempt is preserved in history.]\n\n%s", a.lastError, message)
		a.lastError = "" // Clear after use
	}
	a.mu.Unlock()

	// === IMPROVEMENT 1: Use Task Router for intelligent routing ===
	// Auto-retry transient errors with unified stream retry policy.
	retryPolicy := client.DefaultStreamRetryPolicy()
	failoverTriggered := false
	originalMessage := message
	retryMessage := originalMessage

	var newHistory []*genai.Content
	var response string
	var err error
	contextTruncated := false
	requestRetryCount := 0
	partialIdleRetryCount := 0

	for {
		history = a.session.GetHistory() // Re-read history on each attempt (partial saves possible)
		currentMessage := retryMessage
		execFn := func() error {
			if a.taskRouter != nil {
				// Route the task intelligently
				newHistory, response, err = a.taskRouter.Execute(ctx, history, currentMessage)

				// Log routing decision for debugging
				if analysis := a.taskRouter.GetAnalysis(message); analysis != nil {
					logging.Debug("task routed",
						"complexity", analysis.Score,
						"type", analysis.Type,
						"strategy", analysis.Strategy,
						"reasoning", analysis.Reasoning)
				}
			} else {
				// Fallback to standard executor
				newHistory, response, err = a.executor.Execute(ctx, history, currentMessage)
			}
			return err
		}
		if a.policy != nil {
			err = a.policy.ExecuteRequest(ctx, execFn)
		} else {
			err = execFn()
		}

		if err == nil {
			break
		}

		ft := client.DetectFailureTelemetry(err)
		logging.Warn("request attempt failed",
			"reason", ft.Reason,
			"partial", ft.Partial,
			"timeout", ft.Timeout,
			"provider", ft.Provider,
			"request_retries", requestRetryCount,
			"partial_retries", partialIdleRetryCount,
			"error", err)

		// Don't retry if context cancelled (user abort)
		if ctx.Err() != nil {
			err = client.ContextErr(ctx)
			break
		}

		// Context too long — emergency truncate and retry (once only)
		if client.IsContextTooLongError(err) && a.contextManager != nil && !contextTruncated {
			contextTruncated = true
			if a.executor != nil {
				a.executor.SetSideEffectDedup(true)
			}
			removed := a.contextManager.EmergencyTruncate()
			a.safeSendToProgram(ui.StreamTextMsg(
				fmt.Sprintf("\n⚠️ Context window exceeded — emergency truncated %d messages. Retrying...\n", removed)))
			continue
		}

		decision := client.DecideStreamRetry(
			retryPolicy,
			err,
			requestRetryCount,
			partialIdleRetryCount,
			ctx,
			client.StreamRetryOptions{AllowPartial: true},
		)
		if !decision.ShouldRetry {
			break
		}
		if a.executor != nil {
			a.executor.SetSideEffectDedup(true)
		}

		if decision.Partial {
			partialIdleRetryCount++
			// Partial stream retries should continue from the last complete sentence.
			retryMessage = buildContinuationRetryMessage(originalMessage, newHistory)
		} else {
			requestRetryCount++
		}

		// After repeated transient failures, auto-enable provider failover chain.
		// This avoids user-visible dead-ends when a provider/model is unstable.
		totalRetries := requestRetryCount + partialIdleRetryCount
		if !failoverTriggered && totalRetries >= 2 && isRetryableError(err) {
			if chain, failoverErr := a.activateEmergencyFailoverClient(); failoverErr == nil {
				failoverTriggered = true
				a.safeSendToProgram(ui.StreamTextMsg(
					fmt.Sprintf("\n🔁 Automatic provider failover activated: %s\n", chain)))
			} else {
				logging.Debug("automatic failover not activated", "error", failoverErr)
			}
		}

		// Save partial history before retry (preserves tool side effects)
		if len(newHistory) > len(history) {
			a.session.SetHistory(newHistory)
			if a.sessionManager != nil {
				_ = a.sessionManager.SaveAfterMessage()
			}
		}

		// Warn user about retry
		backoff := decision.Delay
		if decision.Partial {
			a.safeSendToProgram(ui.StreamTextMsg(
				fmt.Sprintf("\n⚠️ Response stream stalled after partial output. Auto-retry #%d/%d in %v...\n",
					partialIdleRetryCount, retryPolicy.MaxPartialRetries, backoff.Round(time.Second))))
		} else if ft.Reason == string(client.FailureReasonStreamIdleTimeout) {
			a.safeSendToProgram(ui.StreamTextMsg(
				fmt.Sprintf("\n⚠️ Response stream stalled. Auto-retry #%d/%d in %v...\n",
					requestRetryCount, retryPolicy.MaxRetries, backoff.Round(time.Second))))
		} else {
			a.safeSendToProgram(ui.StreamTextMsg(
				fmt.Sprintf("\n⚠️ Request failed (%s): %s\nRetrying in %v (%d/%d)...\n",
					ft.Reason, err.Error(), backoff.Round(time.Second), requestRetryCount, retryPolicy.MaxRetries)))
		}

		backoffTimer := time.NewTimer(backoff)
		select {
		case <-backoffTimer.C:
			continue
		case <-ctx.Done():
			backoffTimer.Stop()
			err = client.ContextErr(ctx)
		}
		break
	}

	if err != nil {
		ft := client.DetectFailureTelemetry(err)
		a.journalEvent("request_failed", map[string]any{
			"error":           err.Error(),
			"message_preview": previewForJournal(message),
			"failure_reason":  ft.Reason,
			"partial":         ft.Partial,
			"timeout":         ft.Timeout.String(),
			"provider":        ft.Provider,
		})
		if a.reliability != nil {
			a.reliability.RecordFailure()
		}
		if errors.Is(err, ErrRequestCircuitOpen) {
			a.safeSendToProgram(ui.StreamTextMsg(
				"\n⚠️ Request circuit breaker is open. Waiting for recovery window.\n"))
		}

		// Save history on error only if it ends with a model message (complete turn).
		// If it ends with a user/tool-result message, the model never responded —
		// saving it would create an orphaned tool_result that breaks subsequent API calls.
		if len(newHistory) > len(history) {
			if last := newHistory[len(newHistory)-1]; last != nil && last.Role == genai.RoleModel {
				a.session.SetHistory(newHistory)
				if a.sessionManager != nil {
					_ = a.sessionManager.SaveAfterMessage()
				}
			} else {
				// Trim back to the last model message to avoid orphaned tool results
				trimmed := trimToLastModelMessage(newHistory, len(history))
				if len(trimmed) > len(history) {
					a.session.SetHistory(trimmed)
					if a.sessionManager != nil {
						_ = a.sessionManager.SaveAfterMessage()
					}
				}
			}
		}
		// Store error for context injection on retry
		a.mu.Lock()
		a.lastError = err.Error()
		a.lastErrorTime = time.Now()
		a.mu.Unlock()

		if a.reliability != nil && a.reliability.IsDegraded() {
			a.safeSendToProgram(ui.StreamTextMsg(
				fmt.Sprintf("\n⚠️ Switching to safe mode for stability (%v).\n", a.reliability.DegradedRemaining())))
		}

		if client.IsRateLimitError(err) {
			attempt, delay, ok := a.scheduleRateLimitAutoRetry(message)
			if ok {
				a.journalEvent("rate_limit_auto_retry_scheduled", map[string]any{
					"attempt": attempt,
					"delay":   delay.String(),
				})
				a.safeSendToProgram(ui.StreamTextMsg(
					fmt.Sprintf("\n⏳ API rate limit reached. Auto-retrying in %v (attempt %d/%d). You don't need to resend.\n",
						delay.Round(time.Second), attempt, maxAutoRateLimitRetries)))
				a.safeSendToProgram(ui.ResponseDoneMsg{})

				go func(msg string, wait time.Duration) {
					timer := time.NewTimer(wait)
					defer timer.Stop()
					select {
					case <-timer.C:
						a.handleSubmit(msg)
					case <-a.ctx.Done():
						return
					}
				}(message, delay)
				return
			}
		}

		a.clearRateLimitRetry(message)

		a.safeSendToProgram(ui.ErrorMsg(err))
		return
	}

	a.clearRateLimitRetry(message)

	if a.reliability != nil {
		a.reliability.RecordSuccess()
	}

	// Update session history
	a.session.SetHistory(newHistory)
	a.applyToolOutputHygiene()

	// Check for context-clear request after plan approval
	if a.planManager != nil && a.planManager.IsContextClearRequested() {
		approvedPlan := a.planManager.ConsumeContextClearRequest()
		if approvedPlan != nil && a.config.Plan.ClearContext {
			a.executePlanWithClearContext(ctx, approvedPlan)
			return
		}
	}

	// Hard done-gate before response completion.
	if !a.enforceDoneGate(ctx, message) {
		return
	}

	// Save session after each message
	if a.sessionManager != nil {
		if err := a.sessionManager.SaveAfterMessage(); err != nil {
			logging.Debug("failed to save session after message", "error", err)
		}
	}

	// Update token count after processing and send to UI
	if a.contextManager != nil {
		if err := a.contextManager.UpdateTokenCount(ctx); err != nil {
			logging.Debug("failed to update token count", "error", err)
		}

		// Send final token usage to UI
		a.sendTokenUsageUpdate()

		// Track cumulative token usage for /cost command
		usage := a.contextManager.GetTokenUsage()
		a.mu.Lock()
		a.totalInputTokens = usage.InputTokens
		// Use API usage metadata if available, otherwise estimate
		apiInput, apiOutput := a.executor.GetLastTokenUsage()
		if apiOutput > 0 {
			a.totalOutputTokens += apiOutput
		} else if response != "" {
			// Fallback: estimate output tokens from response length (approx 4 chars per token)
			a.totalOutputTokens += len(response) / 4
		}
		if apiInput > 0 {
			a.totalInputTokens = apiInput
		}
		a.mu.Unlock()
	}

	// Note: response text is already streamed via OnText callback in executor handler
	// Don't send it again here to avoid duplicate output
	_ = response // Used for token counting above

	// Signal completion - copy program reference under lock
	a.mu.Lock()
	program := a.program
	duration := time.Since(a.responseStartTime)
	toolsUsed := make([]string, len(a.responseToolsUsed))
	copy(toolsUsed, a.responseToolsUsed)
	inputTokens := a.totalInputTokens
	outputTokens := a.totalOutputTokens
	a.mu.Unlock()

	if program != nil {
		program.Send(ui.ResponseDoneMsg{})

		// Send response metadata
		_, cacheRead := a.executor.GetLastCacheMetrics()
		program.Send(ui.ResponseMetadataMsg{
			Model:                a.config.Model.Name,
			InputTokens:          inputTokens,
			OutputTokens:         outputTokens,
			CacheReadInputTokens: cacheRead,
			Duration:             duration,
			ToolsUsed:            toolsUsed,
		})
	}

	// Notify on long message processing completion (for background terminals)
	if duration > 30*time.Second {
		if nm := a.executor.GetNotificationManager(); nm != nil {
			nm.NotifySuccess("assistant", fmt.Sprintf("Response ready (%s)", formatDuration(duration)), nil, duration)
		}
	}

	// Update todos display
	todoTool, ok := a.registry.Get("todo")
	if ok {
		if tt, ok := todoTool.(*tools.TodoTool); ok {
			items := tt.GetItems()
			var display []string
			for _, item := range items {
				var icon string
				switch item.Status {
				case "pending":
					icon = "○"
				case "in_progress":
					icon = "◐"
				case "completed":
					icon = "●"
				}
				display = append(display, fmt.Sprintf("%s %s", icon, item.Content))
			}
			if program != nil {
				program.Send(ui.TodoUpdateMsg(display))
			}
		}
	}
}

// executePlanWithClearContext dispatches plan execution to either delegated
// sub-agent mode or direct monolithic execution.
func (a *App) executePlanWithClearContext(ctx context.Context, approvedPlan *plan.Plan) {
	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()
	a.startPlanWatchdog(execCtx, execCancel, approvedPlan.ID)

	// Enter execution mode - this blocks creation of new plans during execution
	if a.planManager != nil {
		a.planManager.SetExecutionMode(true)
		if err := a.planManager.TransitionCurrentPlanLifecycle(plan.LifecycleExecuting); err != nil {
			logging.Warn("failed to transition plan lifecycle to executing", "error", err)
		}
	}

	if a.agentRunner != nil {
		sharedMem := a.agentRunner.GetSharedMemory()
		if sharedMem != nil {
			completedCount := approvedPlan.CompletedCount()
			if completedCount == 0 {
				// Clear SharedMemory for fresh plan execution
				sharedMem.Clear()
				logging.Debug("shared memory cleared for new plan execution", "plan_id", approvedPlan.ID)
			} else {
				// Resuming plan: restore completed steps to SharedMemory
				a.restoreSharedMemoryFromPlan(sharedMem, approvedPlan)
			}
		}
	}

	delegated := a.config.Plan.DelegateSteps && a.agentRunner != nil
	if delegated && a.shouldUseSafeMode() {
		a.safeSendToProgram(ui.StreamTextMsg(
			fmt.Sprintf("⚠️ Safe mode active (%v): running plan without delegation.\n", a.reliability.DegradedRemaining())))
		delegated = false
	}

	if delegated {
		a.executePlanDelegated(execCtx, approvedPlan)
	} else {
		a.executePlanDirectly(execCtx, approvedPlan)
	}
}

// restoreSharedMemoryFromPlan repopulates SharedMemory with results from completed steps.
// This is used when resuming a plan to give sub-agents access to previous step results.
func (a *App) restoreSharedMemoryFromPlan(sharedMem *agent.SharedMemory, p *plan.Plan) {
	steps := p.GetStepsSnapshot()
	restored := 0
	for _, step := range steps {
		if step.Status == plan.StatusCompleted && step.Output != "" {
			sharedMem.Write(
				fmt.Sprintf("step_%d_result", step.ID),
				map[string]string{
					"title":  step.Title,
					"output": step.Output,
				},
				agent.SharedEntryTypeFact,
				fmt.Sprintf("plan_step_%d", step.ID),
			)
			restored++
		}
	}
	if restored > 0 {
		logging.Debug("shared memory restored from completed steps",
			"plan_id", p.ID, "steps_restored", restored)
	}
}

// executePlanDirectly executes an approved plan step-by-step using the main
// session executor. Each step gets a targeted prompt, and the orchestrator
// manages progress, SharedMemory, and auto-completion automatically.
// Unlike delegated mode, all steps share the same session history for continuity.
func (a *App) executePlanDirectly(ctx context.Context, approvedPlan *plan.Plan) {
	planStart := time.Now()

	logging.Debug("executing plan directly (step-by-step)",
		"plan_id", approvedPlan.ID,
		"title", approvedPlan.Title,
		"steps", approvedPlan.StepCount())

	// Ensure execution mode is reset on any exit path (including panics and early returns)
	defer func() {
		if a.planManager != nil {
			a.planManager.SetExecutionMode(false)
			a.planManager.SetCurrentStepID(-1)
		}
	}()

	// Skip diff approval prompts — the plan itself was already approved
	ctx = tools.ContextWithSkipDiff(ctx)

	// 1. Save context snapshot before clearing (preserves planning decisions)
	contextSnapshot := a.extractContextSnapshot()
	if contextSnapshot != "" {
		approvedPlan.SetContextSnapshot(contextSnapshot)
		logging.Debug("context snapshot saved", "plan_id", approvedPlan.ID, "snapshot_len", len(contextSnapshot))
	}

	// 2. Convert plan steps to PlanStepInfo for the base prompt
	stepInfos := make([]appcontext.PlanStepInfo, 0, len(approvedPlan.Steps))
	for _, s := range approvedPlan.Steps {
		stepInfos = append(stepInfos, appcontext.PlanStepInfo{
			ID:          s.ID,
			Title:       s.Title,
			Description: s.Description,
		})
	}

	// 3. Build plan execution prompt (includes context snapshot if available)
	planPrompt := a.promptBuilder.BuildPlanExecutionPromptWithContext(
		approvedPlan.Title, approvedPlan.Description, stepInfos, contextSnapshot)

	// 3b. Save plan to persistent storage before clearing session
	if a.planManager != nil {
		if err := a.planManager.SaveCurrentPlan(); err != nil {
			logging.Warn("failed to save plan before execution", "error", err)
		}
	}

	// 4. Clear session and inject plan context
	a.session.Clear()
	a.session.AddUserMessage(planPrompt)
	a.session.AddModelMessage("I understand the approved plan. I will execute each step as instructed.")

	totalSteps := len(approvedPlan.Steps)

	// Get SharedMemory for inter-step communication
	var sharedMem *agent.SharedMemory
	if a.agentRunner != nil {
		sharedMem = a.agentRunner.GetSharedMemory()
	}

	// 5. Notify UI with plan banner
	a.safeSendToProgram(ui.StreamTextMsg(
		fmt.Sprintf("\n━━━ Executing plan: %s (%d steps) ━━━\n\n", approvedPlan.Title, totalSteps)))

	// 6. Execute steps using NextReadySteps for dependency-aware + parallel execution
	const maxRetries = 3
	backoffDurations := []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second}

	// Auto-resume: when all ready steps are exhausted but paused steps remain,
	// wait a cooldown period and retry them automatically.
	const maxAutoResumeRounds = 2
	const autoResumeCooldown = 60 * time.Second
	autoResumeCount := 0
	maxExecutionRounds := maxPlanExecutionRounds(totalSteps)
	executionRounds := 0

	for {
		executionRounds++
		if executionRounds > maxExecutionRounds {
			a.planManager.PausePlan()
			a.safeSendToProgram(ui.StreamTextMsg(
				"\n⏸ Plan paused — execution safety limit reached. Use /resume-plan to continue.\n"))
			a.safeSendToProgram(ui.PlanProgressMsg{
				PlanID:     approvedPlan.ID,
				TotalSteps: totalSteps,
				Completed:  approvedPlan.CompletedCount(),
				Progress:   approvedPlan.Progress(),
				Status:     "paused",
				Reason:     "execution safety limit reached",
			})
			a.safeSendToProgram(ui.ResponseDoneMsg{})
			return
		}

		select {
		case <-ctx.Done():
			return
		default:
		}

		readySteps := approvedPlan.NextReadySteps()
		if len(readySteps) == 0 {
			// No ready steps. Check if there are paused steps to auto-resume.
			if approvedPlan.HasPausedSteps() && autoResumeCount < maxAutoResumeRounds {
				autoResumeCount++
				a.safeSendToProgram(ui.StreamTextMsg(
					fmt.Sprintf("\n⏳ Waiting %v before auto-resuming paused steps (round %d/%d)...\n",
						autoResumeCooldown, autoResumeCount, maxAutoResumeRounds)))

				cooldownTimer := time.NewTimer(autoResumeCooldown)
				select {
				case <-cooldownTimer.C:
				case <-ctx.Done():
					cooldownTimer.Stop()
					return
				}

				resumed := a.planManager.ResumePausedSteps()
				a.safeSendToProgram(ui.StreamTextMsg(
					fmt.Sprintf("🔄 Resumed %d paused step(s)\n\n", resumed)))
				continue
			}

			// Auto-resume exhausted or no paused steps — exit loop
			if approvedPlan.HasPausedSteps() {
				a.planManager.PausePlan()
				a.safeSendToProgram(ui.StreamTextMsg(
					"\n⏸ Plan paused — auto-resume exhausted. Use /resume-plan to continue.\n"))
				a.safeSendToProgram(ui.PlanProgressMsg{
					PlanID:     approvedPlan.ID,
					TotalSteps: totalSteps,
					Completed:  approvedPlan.CompletedCount(),
					Progress:   approvedPlan.Progress(),
					Status:     "paused",
					Reason:     "auto-resume exhausted",
				})
				a.safeSendToProgram(ui.ResponseDoneMsg{})
				return
			}
			break // All steps done — proceed to summary
		}

		// Direct mode shares a single session, so run sequentially for reliability.
		for _, step := range readySteps {
			a.executeDirectStep(ctx, step, approvedPlan, totalSteps, sharedMem, maxRetries, backoffDurations)
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}

	// Enforce final verification gate before producing terminal plan summary.
	if !a.enforceDoneGate(ctx, approvedPlan.Request) {
		return
	}

	// 7. Plan completion summary
	planDuration := time.Since(planStart)
	summary := a.formatPlanSummary(approvedPlan, planDuration)
	a.safeSendToProgram(ui.StreamTextMsg(summary))

	completedCount := approvedPlan.CompletedCount()
	statusText := "completed"
	if completedCount < approvedPlan.StepCount() {
		statusText = "paused"
	}

	a.safeSendToProgram(ui.PlanProgressMsg{
		PlanID:     approvedPlan.ID,
		TotalSteps: approvedPlan.StepCount(),
		Completed:  completedCount,
		Progress:   approvedPlan.Progress(),
		Status:     statusText,
		Reason:     "plan execution round finished",
	})

	// Send response metadata so UI shows duration and token usage
	a.mu.Lock()
	inputTokens := a.totalInputTokens
	outputTokens := a.totalOutputTokens
	a.mu.Unlock()
	a.safeSendToProgram(ui.ResponseMetadataMsg{
		Model:        a.config.Model.Name,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Duration:     planDuration,
	})

	a.safeSendToProgram(ui.ResponseDoneMsg{})
	a.journalEvent("request_completed", map[string]any{
		"message_preview": previewForJournal(approvedPlan.Request),
	})
	a.enforceSessionMemoryGovernance("request_completed")

	a.finalizePlanLifecycleState(approvedPlan)

	// Clear plan if fully completed
	if approvedPlan.IsComplete() && a.planManager != nil {
		a.planManager.ClearPlan()
		logging.Debug("plan auto-cleared after completion", "plan_id", approvedPlan.ID)
	}

	// Save session
	if a.sessionManager != nil {
		if err := a.sessionManager.SaveAfterMessage(); err != nil {
			logging.Warn("failed to save session after message", "error", err)
		}
	}
}

// getStepTimeout returns the timeout to use for a step.
func (a *App) getStepTimeout(step *plan.Step) time.Duration {
	if step.Timeout > 0 {
		return step.Timeout
	}
	if a.config.Plan.DefaultStepTimeout > 0 {
		return a.config.Plan.DefaultStepTimeout
	}
	return 5 * time.Minute
}

// executeDirectStep executes a single step in the direct (same-session) mode.
func (a *App) executeDirectStep(ctx context.Context, step *plan.Step, approvedPlan *plan.Plan, totalSteps int, sharedMem *agent.SharedMemory, maxRetries int, backoffDurations []time.Duration) {
	select {
	case <-ctx.Done():
		return
	default:
	}

	// Evaluate conditional steps
	if step.Condition != "" && step.ShouldSkip(approvedPlan) {
		a.planManager.SkipStep(step.ID)
		a.safeSendToProgram(ui.StreamTextMsg(
			fmt.Sprintf("  Step %d skipped (condition: %s)\n", step.ID, step.Condition)))
		return
	}
	step.EnsureContractDefaults()

	// Idempotency guard: if previous attempt already produced side effects for
	// this step but never reached completion, avoid automatic re-execution.
	if approvedPlan.HasPartialEffects(step.ID) || approvedPlan.HasDuplicateRisk(step.ID) {
		reason := "partial effects from previous attempt detected"
		if approvedPlan.HasDuplicateRisk(step.ID) {
			reason = "duplicate side effects detected across retries"
		}
		a.planManager.PauseStep(step.ID, reason)
		a.journalEvent("plan_step_paused", map[string]any{
			"plan_id": approvedPlan.ID,
			"step_id": step.ID,
			"reason":  reason,
		})
		a.safeSendToProgram(ui.StreamTextMsg(
			fmt.Sprintf("⏸ Step %d paused for safety: %s. Review and /resume-plan when ready.\n", step.ID, reason)))
		a.safeSendToProgram(ui.PlanProgressMsg{
			PlanID:        approvedPlan.ID,
			CurrentStepID: step.ID,
			CurrentTitle:  step.Title,
			TotalSteps:    totalSteps,
			Completed:     approvedPlan.CompletedCount(),
			Progress:      approvedPlan.Progress(),
			Status:        "paused",
			Reason:        reason,
		})
		return
	}

	if requiresHumanCheckpoint(step) && !step.CheckpointPassed {
		reason := "checkpoint required: high-risk step needs operator confirmation"
		a.planManager.PauseStep(step.ID, reason)
		a.journalEvent("plan_checkpoint_pause", map[string]any{
			"plan_id": approvedPlan.ID,
			"step_id": step.ID,
			"title":   step.Title,
		})
		a.safeSendToProgram(ui.StreamTextMsg(
			fmt.Sprintf("⏸ Step %d requires checkpoint approval.\nWhy: %s\nRun /resume-plan to approve and continue.\n",
				step.ID, step.Title)))
		a.safeSendToProgram(ui.PlanProgressMsg{
			PlanID:        approvedPlan.ID,
			CurrentStepID: step.ID,
			CurrentTitle:  step.Title,
			TotalSteps:    totalSteps,
			Completed:     approvedPlan.CompletedCount(),
			Progress:      approvedPlan.Progress(),
			Status:        "paused",
			Reason:        reason,
		})
		return
	}

	// Compact history if needed before executing next step
	if a.contextManager != nil {
		if err := a.contextManager.PrepareForRequest(ctx); err != nil {
			logging.Debug("failed to prepare context before step", "step_id", step.ID, "error", err)
		}
	}

	// Mark step as started and track current step ID
	a.planManager.StartStep(step.ID)
	a.planManager.SetCurrentStepID(step.ID)
	a.refreshSystemInstruction()
	a.touchStepHeartbeat()
	a.journalEvent("plan_step_started", map[string]any{
		"plan_id":   approvedPlan.ID,
		"step_id":   step.ID,
		"step":      step.Title,
		"execution": "direct",
	})
	a.saveRecoverySnapshot("")
	a.startStepRollbackSnapshot(approvedPlan, step)

	// Update plan progress in status bar
	a.safeSendToProgram(ui.PlanProgressMsg{
		PlanID:        approvedPlan.ID,
		CurrentStepID: step.ID,
		CurrentTitle:  step.Title,
		TotalSteps:    totalSteps,
		Completed:     approvedPlan.CompletedCount(),
		Progress:      approvedPlan.Progress(),
		Status:        "in_progress",
		Reason:        "step started",
	})

	header := fmt.Sprintf("──── Step %d/%d: %s ────\n", step.ID, totalSteps, step.Title)
	a.safeSendToProgram(ui.StreamTextMsg(header))

	// Build step-specific prompt with context from previous steps
	prevSummary := a.planManager.GetPreviousStepsSummary(step.ID, planSummaryMaxChars)
	stepMsg := buildDirectStepMessage(step, prevSummary, totalSteps)

	// Per-step timeout
	stepTimeout := a.getStepTimeout(step)
	stepCtx, stepCancel := context.WithTimeout(ctx, stepTimeout)
	defer stepCancel()

	// Execute step with retry logic
	var response string
	var err error
	errCat := plan.ErrorUnknown

	for attempt := 0; attempt < maxRetries; attempt++ {
		history, histVersion := a.session.GetHistoryWithVersion()
		var newHistory []*genai.Content
		execFn := func() error {
			newHistory, response, err = a.executor.Execute(stepCtx, history, stepMsg)
			return err
		}
		if a.policy != nil {
			err = a.policy.ExecutePlanStep(stepCtx, execFn)
		} else {
			err = execFn()
		}

		if err == nil {
			// Success — update session history with version check.
			// For parallel plan steps, another step may have updated history
			// while we were executing. In that case, append only the new entries
			// that our execution produced to the current history.
			if !a.session.SetHistoryIfVersion(newHistory, histVersion) {
				// Version changed — merge by appending our new entries
				delta := newHistory[len(history):]
				currentHistory := a.session.GetHistory()
				merged := make([]*genai.Content, len(currentHistory)+len(delta))
				copy(merged, currentHistory)
				copy(merged[len(currentHistory):], delta)
				a.session.SetHistory(merged)
			}
			break
		}

		// Classify the error
		errCat = plan.ClassifyError(err, err.Error())
		if errors.Is(err, ErrStepCircuitOpen) {
			errCat = plan.ErrorTransient
		}

		// Retry only on transient errors
		if errCat == plan.ErrorTransient && attempt < maxRetries-1 {
			backoff := backoffDurations[attempt]
			logging.Warn("step execution error, retrying",
				"step_id", step.ID, "attempt", attempt+1, "error", err.Error(),
				"category", errCat.String(), "backoff", backoff)
			a.safeSendToProgram(ui.StreamTextMsg(
				fmt.Sprintf("\n⚠️ Step %d failed (attempt %d/%d): %s\nRetrying in %v...\n",
					step.ID, attempt+1, maxRetries, err.Error(), backoff)))

			backoffTimer := time.NewTimer(backoff)
			select {
			case <-backoffTimer.C:
				continue
			case <-ctx.Done():
				backoffTimer.Stop()
				err = ctx.Err()
				errCat = plan.ClassifyError(err, err.Error())
				break
			}
		}
		break
	}

	// Handle step failure
	if err != nil {
		errMsg := err.Error()
		if a.reliability != nil {
			a.reliability.RecordFailure()
		}

		// Transient error after all attempts → pause step, plan continues with other steps
		if errCat == plan.ErrorTransient {
			reason := fmt.Sprintf("%s (after %d attempts; will auto-retry later)", errMsg, maxRetries)
			a.pauseStepWithRollback(ctx, approvedPlan, step, totalSteps, reason)

			logging.Info("step paused due to transient error, plan continues",
				"step_id", step.ID, "error", errMsg, "category", errCat.String())
			return
		}

		// Fatal/logic/unknown error
		a.planManager.FailStep(step.ID, errMsg)
		a.commitStepRollbackSnapshot(approvedPlan.ID, step.ID)
		a.journalEvent("plan_step_failed", map[string]any{
			"plan_id": approvedPlan.ID,
			"step_id": step.ID,
			"reason":  errMsg,
		})

		a.safeSendToProgram(ui.StreamTextMsg(
			fmt.Sprintf("\n  Step %d failed (%s): %s\n", step.ID, errCat.String(), errMsg)))

		// Attempt adaptive replan on fatal errors
		if errCat == plan.ErrorFatal && a.planManager.HasReplanHandler() {
			if replanErr := a.planManager.RequestReplan(ctx, step); replanErr == nil {
				a.safeSendToProgram(ui.StreamTextMsg("[Plan adjusted after step failure. Continuing.]\n"))
				logging.Info("plan replanned after fatal step error",
					"step_id", step.ID, "plan_version", approvedPlan.Version)
				return // Don't abort — the loop will pick up new steps
			} else {
				logging.Warn("replan attempt failed", "error", replanErr)
			}
		}

		if a.config.Plan.AbortOnStepFailure {
			a.safeSendToProgram(ui.StreamTextMsg("Aborting plan due to step failure.\n"))
		}
		return
	}

	// Step succeeded — store output and mark complete
	if a.reliability != nil {
		a.reliability.RecordSuccess()
	}

	output := response
	if len(output) > planStepOutputMaxChars {
		output = output[:planStepOutputMaxChars] + "..."
	}

	// Record token usage for this step
	apiInput, apiOutput := a.executor.GetLastTokenUsage()
	step.TokensUsed = apiInput + apiOutput

	verificationSummary, verificationOutput, verificationOK, verificationReason := a.runStepVerificationCommands(ctx, approvedPlan, step)
	if !verificationOK {
		a.pauseStepWithRollback(ctx, approvedPlan, step, totalSteps, verificationReason)
		return
	}
	if verificationOutput != "" {
		if output != "" {
			output += "\n\n"
		}
		output += verificationOutput
		if len(output) > planStepOutputMaxChars {
			output = output[:planStepOutputMaxChars] + "..."
		}
	}
	if verificationSummary != "" {
		a.journalEvent("plan_step_verification_passed", map[string]any{
			"plan_id": approvedPlan.ID,
			"step_id": step.ID,
			"summary": verificationSummary,
		})
	}

	evidence, verificationNote, evidenceOK, evidenceReason := a.buildStepCompletionEvidence(approvedPlan, step, output)
	if !evidenceOK {
		a.pauseStepWithRollback(ctx, approvedPlan, step, totalSteps, evidenceReason)
		return
	}
	a.planManager.RecordStepVerification(step.ID, evidence, verificationNote)

	a.planManager.CompleteStep(step.ID, output)
	a.commitStepRollbackSnapshot(approvedPlan.ID, step.ID)
	a.journalEvent("plan_step_completed", map[string]any{
		"plan_id": approvedPlan.ID,
		"step_id": step.ID,
		"output":  previewForJournal(output),
	})

	// Store step result in SharedMemory for inter-step communication
	if sharedMem != nil {
		sharedMem.Write(
			fmt.Sprintf("step_%d_result", step.ID),
			map[string]string{
				"title":  step.Title,
				"output": output,
			},
			agent.SharedEntryTypeFact,
			fmt.Sprintf("plan_step_%d", step.ID),
		)
		logging.Debug("step result stored in shared memory",
			"step_id", step.ID, "output_len", len(output))
	}

	a.safeSendToProgram(ui.StreamTextMsg(
		fmt.Sprintf("  Step %d complete\n\n", step.ID)))
	a.safeSendToProgram(ui.PlanProgressMsg{
		PlanID:        approvedPlan.ID,
		CurrentStepID: step.ID,
		CurrentTitle:  step.Title,
		TotalSteps:    totalSteps,
		Completed:     approvedPlan.CompletedCount(),
		Progress:      approvedPlan.Progress(),
		Status:        "completed",
		Reason:        "step completed",
	})

	// Update token count after each step
	if a.contextManager != nil {
		if err := a.contextManager.UpdateTokenCount(ctx); err != nil {
			logging.Debug("failed to update token count", "error", err)
		}
		a.sendTokenUsageUpdate()
	}

	// Accumulate token usage for /cost command
	a.mu.Lock()
	if apiOutput > 0 {
		a.totalOutputTokens += apiOutput
	} else if response != "" {
		a.totalOutputTokens += len(response) / 4
	}
	if apiInput > 0 {
		a.totalInputTokens = apiInput
	}
	a.mu.Unlock()

	// Save session after each completed step (crash recovery)
	if a.sessionManager != nil {
		if err := a.sessionManager.SaveAfterMessage(); err != nil {
			logging.Warn("failed to save session after message", "error", err)
		}
	}
	a.enforceSessionMemoryGovernance("plan_step_completed")
}

// buildDirectStepMessage creates a focused prompt for executing a single step
// in the direct (same-session) execution mode.
func buildDirectStepMessage(step *plan.Step, prevSummary string, totalSteps int) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Execute step %d of %d: **%s**\n\n", step.ID, totalSteps, step.Title))

	if step.Description != "" {
		sb.WriteString(step.Description)
		sb.WriteString("\n\n")
	}

	sb.WriteString("Step Contract:\n")
	if len(step.Inputs) > 0 {
		sb.WriteString("- Inputs:\n")
		for _, in := range step.Inputs {
			sb.WriteString(fmt.Sprintf("  - %s\n", in))
		}
	}
	if step.ExpectedArtifact != "" {
		sb.WriteString(fmt.Sprintf("- Expected artifact: %s\n", step.ExpectedArtifact))
	}
	if len(step.ExpectedArtifactPaths) > 0 {
		sb.WriteString("- Expected artifact paths:\n")
		for _, path := range step.ExpectedArtifactPaths {
			sb.WriteString(fmt.Sprintf("  - %s\n", path))
		}
	}
	if len(step.SuccessCriteria) > 0 {
		sb.WriteString("- Success criteria:\n")
		for _, c := range step.SuccessCriteria {
			sb.WriteString(fmt.Sprintf("  - %s\n", c))
		}
	}
	if len(step.VerifyCommands) > 0 {
		sb.WriteString("- Verify commands:\n")
		for _, cmd := range step.VerifyCommands {
			sb.WriteString(fmt.Sprintf("  - %s\n", cmd))
		}
	}
	if step.Rollback != "" {
		sb.WriteString(fmt.Sprintf("- Rollback: %s\n", step.Rollback))
	}
	sb.WriteString("\n")

	if contractJSON := buildStepContractJSON(step, totalSteps); contractJSON != "" {
		sb.WriteString("StepContractJSON:\n")
		sb.WriteString("```json\n")
		sb.WriteString(contractJSON)
		sb.WriteString("\n```\n\n")
	}

	if prevSummary != "" {
		sb.WriteString("Previous steps summary:\n")
		sb.WriteString(prevSummary)
		sb.WriteString("\n")
	}

	sb.WriteString("Rules:\n")
	sb.WriteString("- Execute ONLY this step, nothing else\n")
	sb.WriteString("- Always READ files before editing them\n")
	sb.WriteString("- Do NOT call update_plan_progress or exit_plan_mode — the orchestrator handles this\n")
	sb.WriteString("- Provide a brief summary of what was done at the end\n")
	sb.WriteString("- Include evidence: changed files, executed commands, and verification facts\n")
	sb.WriteString("- Treat verify_commands as mandatory completion gate\n")
	sb.WriteString("- Report any issues or deviations from the plan\n")

	return sb.String()
}

// executePlanDelegated executes an approved plan by spawning a sub-agent per step.
// Each step runs in isolation with project context injected, and only compact
// summaries are stored in the main session.
func (a *App) executePlanDelegated(ctx context.Context, approvedPlan *plan.Plan) {
	planStart := time.Now()

	logging.Debug("executing plan via sub-agent delegation",
		"plan_id", approvedPlan.ID,
		"title", approvedPlan.Title,
		"steps", approvedPlan.StepCount())

	// Ensure execution mode is reset on any exit path (including panics and early returns)
	defer func() {
		if a.planManager != nil {
			a.planManager.SetExecutionMode(false)
			a.planManager.SetCurrentStepID(-1)
		}
	}()

	// Skip diff approval prompts for delegated plan execution —
	// the plan itself was already approved by the user.
	ctx = tools.ContextWithSkipDiff(ctx)

	totalSteps := len(approvedPlan.Steps)

	// Get SharedMemory for inter-step communication
	var sharedMem *agent.SharedMemory
	if a.agentRunner != nil {
		sharedMem = a.agentRunner.GetSharedMemory()
	}

	// Save context snapshot if not already present (e.g., first execution, not resume)
	// Priority: 1) SharedMemory structured snapshot, 2) Plan string snapshot, 3) Extract new
	contextSnapshot := ""

	// First, try to get structured snapshot from SharedMemory
	if sharedMem != nil {
		if formattedSnapshot := sharedMem.GetContextSnapshotForPrompt(); formattedSnapshot != "" {
			contextSnapshot = formattedSnapshot
			logging.Debug("using structured context snapshot from shared memory",
				"plan_id", approvedPlan.ID, "snapshot_len", len(contextSnapshot))
		}
	}

	// Fall back to plan's string snapshot
	if contextSnapshot == "" {
		contextSnapshot = approvedPlan.GetContextSnapshot()
	}

	// If still empty and first execution, extract new snapshot
	if contextSnapshot == "" && approvedPlan.CompletedCount() == 0 {
		contextSnapshot = a.extractContextSnapshot()
		if contextSnapshot != "" {
			approvedPlan.SetContextSnapshot(contextSnapshot)
			logging.Debug("context snapshot saved for delegated plan",
				"plan_id", approvedPlan.ID, "snapshot_len", len(contextSnapshot))
		}
	}

	// Notify UI with plan banner
	a.safeSendToProgram(ui.StreamTextMsg(
		fmt.Sprintf("\n━━━ Executing plan: %s (%d steps) ━━━\n\n", approvedPlan.Title, totalSteps)))

	// Auto-resume: when all ready steps are exhausted but paused steps remain,
	// wait a cooldown period and retry them automatically.
	const maxAutoResumeRounds = 2
	const autoResumeCooldown = 60 * time.Second
	autoResumeCount := 0
	maxExecutionRounds := maxPlanExecutionRounds(totalSteps)
	executionRounds := 0

	for {
		executionRounds++
		if executionRounds > maxExecutionRounds {
			a.planManager.PausePlan()
			a.safeSendToProgram(ui.StreamTextMsg(
				"\n⏸ Plan paused — execution safety limit reached. Use /resume-plan to continue.\n"))
			a.safeSendToProgram(ui.PlanProgressMsg{
				PlanID:     approvedPlan.ID,
				TotalSteps: totalSteps,
				Completed:  approvedPlan.CompletedCount(),
				Progress:   approvedPlan.Progress(),
				Status:     "paused",
				Reason:     "execution safety limit reached",
			})
			a.safeSendToProgram(ui.ResponseDoneMsg{})
			return
		}

		select {
		case <-ctx.Done():
			return
		default:
		}

		readySteps := approvedPlan.NextReadySteps()
		if len(readySteps) == 0 {
			// No ready steps. Check if there are paused steps to auto-resume.
			if approvedPlan.HasPausedSteps() && autoResumeCount < maxAutoResumeRounds {
				autoResumeCount++
				a.safeSendToProgram(ui.StreamTextMsg(
					fmt.Sprintf("\n⏳ Waiting %v before auto-resuming paused steps (round %d/%d)...\n",
						autoResumeCooldown, autoResumeCount, maxAutoResumeRounds)))

				cooldownTimer := time.NewTimer(autoResumeCooldown)
				select {
				case <-cooldownTimer.C:
				case <-ctx.Done():
					cooldownTimer.Stop()
					return
				}

				resumed := a.planManager.ResumePausedSteps()
				a.safeSendToProgram(ui.StreamTextMsg(
					fmt.Sprintf("🔄 Resumed %d paused step(s)\n\n", resumed)))
				continue
			}

			// Auto-resume exhausted or no paused steps — exit loop
			if approvedPlan.HasPausedSteps() {
				a.planManager.PausePlan()
				a.safeSendToProgram(ui.StreamTextMsg(
					"\n⏸ Plan paused — auto-resume exhausted. Use /resume-plan to continue.\n"))
				a.safeSendToProgram(ui.PlanProgressMsg{
					PlanID:     approvedPlan.ID,
					TotalSteps: totalSteps,
					Completed:  approvedPlan.CompletedCount(),
					Progress:   approvedPlan.Progress(),
					Status:     "paused",
					Reason:     "auto-resume exhausted",
				})
				a.safeSendToProgram(ui.ResponseDoneMsg{})
				return
			}
			break // All steps done — proceed to summary
		}

		if len(readySteps) == 1 || a.shouldUseSafeMode() {
			if len(readySteps) > 1 && a.shouldUseSafeMode() {
				a.safeSendToProgram(ui.StreamTextMsg("⚠️ Safe mode: running steps sequentially.\n"))
			}
			for _, step := range readySteps {
				a.executeDelegatedStep(ctx, step, approvedPlan, totalSteps, sharedMem, contextSnapshot)
			}
		} else {
			// Parallel execution of ready steps with context cancellation support
			done := make(chan struct{})
			var wg sync.WaitGroup
			for _, step := range readySteps {
				wg.Add(1)
				go func(s *plan.Step) {
					defer wg.Done()
					a.executeDelegatedStep(ctx, s, approvedPlan, totalSteps, sharedMem, contextSnapshot)
				}(step)
			}
			go func() {
				wg.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-ctx.Done():
				<-done
			}
		}
	}

	// Enforce final verification gate before producing terminal plan summary.
	if !a.enforceDoneGate(ctx, approvedPlan.Request) {
		return
	}

	// Plan completion summary
	planDuration := time.Since(planStart)
	summary := a.formatPlanSummary(approvedPlan, planDuration)
	a.safeSendToProgram(ui.StreamTextMsg(summary))

	delegatedCompletedCount := approvedPlan.CompletedCount()
	delegatedStatusText := "completed"
	if delegatedCompletedCount < approvedPlan.StepCount() {
		delegatedStatusText = "paused"
	}

	a.safeSendToProgram(ui.PlanProgressMsg{
		PlanID:     approvedPlan.ID,
		TotalSteps: approvedPlan.StepCount(),
		Completed:  delegatedCompletedCount,
		Progress:   approvedPlan.Progress(),
		Status:     delegatedStatusText,
		Reason:     "plan execution round finished",
	})

	// Send response metadata so UI shows duration
	a.safeSendToProgram(ui.ResponseMetadataMsg{
		Model:    a.config.Model.Name,
		Duration: planDuration,
	})

	a.safeSendToProgram(ui.ResponseDoneMsg{})

	a.finalizePlanLifecycleState(approvedPlan)

	// Clear plan if fully completed
	if approvedPlan.IsComplete() && a.planManager != nil {
		a.planManager.ClearPlan()
		logging.Debug("plan auto-cleared after completion", "plan_id", approvedPlan.ID)
	}

	// Note: SetExecutionMode(false) is handled by defer at function start

	// Save session
	if a.sessionManager != nil {
		if err := a.sessionManager.SaveAfterMessage(); err != nil {
			logging.Warn("failed to save session after message", "error", err)
		}
	}
}

// executeDelegatedStep executes a single step via sub-agent delegation.
func (a *App) executeDelegatedStep(ctx context.Context, step *plan.Step, approvedPlan *plan.Plan, totalSteps int, sharedMem *agent.SharedMemory, contextSnapshot string) {
	select {
	case <-ctx.Done():
		return
	default:
	}

	// Evaluate conditional steps
	if step.Condition != "" && step.ShouldSkip(approvedPlan) {
		a.planManager.SkipStep(step.ID)
		a.safeSendToProgram(ui.StreamTextMsg(
			fmt.Sprintf("  Step %d skipped (condition: %s)\n", step.ID, step.Condition)))
		return
	}
	step.EnsureContractDefaults()

	// Idempotency guard for delegated execution.
	if approvedPlan.HasPartialEffects(step.ID) || approvedPlan.HasDuplicateRisk(step.ID) {
		reason := "partial effects from previous attempt detected"
		if approvedPlan.HasDuplicateRisk(step.ID) {
			reason = "duplicate side effects detected across retries"
		}
		a.planManager.PauseStep(step.ID, reason)
		a.journalEvent("plan_step_paused", map[string]any{
			"plan_id": approvedPlan.ID,
			"step_id": step.ID,
			"reason":  reason,
		})
		a.safeSendToProgram(ui.StreamTextMsg(
			fmt.Sprintf("⏸ Step %d paused for safety: %s. Review and /resume-plan when ready.\n", step.ID, reason)))
		a.safeSendToProgram(ui.PlanProgressMsg{
			PlanID:        approvedPlan.ID,
			CurrentStepID: step.ID,
			CurrentTitle:  step.Title,
			TotalSteps:    totalSteps,
			Completed:     approvedPlan.CompletedCount(),
			Progress:      approvedPlan.Progress(),
			Status:        "paused",
			Reason:        reason,
		})
		return
	}

	if requiresHumanCheckpoint(step) && !step.CheckpointPassed {
		reason := "checkpoint required: high-risk step needs operator confirmation"
		a.planManager.PauseStep(step.ID, reason)
		a.journalEvent("plan_checkpoint_pause", map[string]any{
			"plan_id": approvedPlan.ID,
			"step_id": step.ID,
			"title":   step.Title,
		})
		a.safeSendToProgram(ui.StreamTextMsg(
			fmt.Sprintf("⏸ Step %d requires checkpoint approval.\nWhy: %s\nRun /resume-plan to approve and continue.\n",
				step.ID, step.Title)))
		a.safeSendToProgram(ui.PlanProgressMsg{
			PlanID:        approvedPlan.ID,
			CurrentStepID: step.ID,
			CurrentTitle:  step.Title,
			TotalSteps:    totalSteps,
			Completed:     approvedPlan.CompletedCount(),
			Progress:      approvedPlan.Progress(),
			Status:        "paused",
			Reason:        reason,
		})
		return
	}

	// Mark step as started and track current step ID
	a.planManager.StartStep(step.ID)
	a.planManager.SetCurrentStepID(step.ID)
	a.refreshSystemInstruction()
	a.touchStepHeartbeat()
	a.journalEvent("plan_step_started", map[string]any{
		"plan_id":   approvedPlan.ID,
		"step_id":   step.ID,
		"step":      step.Title,
		"execution": "delegated",
	})
	a.saveRecoverySnapshot("")
	a.startStepRollbackSnapshot(approvedPlan, step)

	// Update plan progress in status bar
	a.safeSendToProgram(ui.PlanProgressMsg{
		PlanID:        approvedPlan.ID,
		CurrentStepID: step.ID,
		CurrentTitle:  step.Title,
		TotalSteps:    totalSteps,
		Completed:     approvedPlan.CompletedCount(),
		Progress:      approvedPlan.Progress(),
		Status:        "in_progress",
		Reason:        "step started",
	})

	// Notify UI of step start with structured header
	header := fmt.Sprintf("──── Step %d/%d: %s ────\n", step.ID, totalSteps, step.Title)
	a.safeSendToProgram(ui.StreamTextMsg(header))

	// Build step prompt with full plan context
	prevSummary := a.planManager.GetPreviousStepsSummary(step.ID, planSummaryMaxChars)
	projectCtx := ""
	if a.promptBuilder != nil {
		projectCtx = a.promptBuilder.BuildSubAgentPrompt()
	}

	// Get SharedMemory context for this sub-agent
	sharedMemCtx := ""
	if sharedMem != nil {
		sharedMemCtx = sharedMem.GetForContext(fmt.Sprintf("plan_step_%d", step.ID), 20)
	}

	stepPrompt := buildStepPrompt(&StepPromptContext{
		Step:            step,
		PrevSummary:     prevSummary,
		PlanTitle:       approvedPlan.Title,
		PlanDescription: approvedPlan.Description,
		PlanRequest:     approvedPlan.Request,
		ContextSnapshot: contextSnapshot,
		SharedMemoryCtx: sharedMemCtx,
		TotalSteps:      totalSteps,
		CompletedCount:  approvedPlan.CompletedCount(),
	})

	// Stream sub-agent text to TUI
	onText := func(text string) {
		a.safeSendToProgram(ui.StreamTextMsg(text))
	}

	// Per-step timeout
	stepTimeout := a.getStepTimeout(step)
	stepCtx, stepCancel := context.WithTimeout(ctx, stepTimeout)
	defer stepCancel()

	// Spawn sub-agent for this step with retry on transient errors
	var result *agent.AgentResult
	var err error
	errCat := plan.ErrorUnknown
	const maxRetries = 3
	backoffDurations := []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second}

	for attempt := 0; attempt < maxRetries; attempt++ {
		execFn := func() error {
			_, result, err = a.agentRunner.SpawnWithContext(
				stepCtx, "general", stepPrompt, 30, "", projectCtx, onText, true,
				func(progress *agent.AgentProgress) {
					a.safeSendToProgram(ui.PlanProgressMsg{
						PlanID:        approvedPlan.ID,
						CurrentStepID: step.ID,
						CurrentTitle:  step.Title,
						TotalSteps:    totalSteps,
						Completed:     approvedPlan.CompletedCount(),
						Progress:      approvedPlan.Progress(),
						Status:        "in_progress",
						SubStepInfo:   progress.FormatProgress(),
						Reason:        "agent progress update",
					})
				})
			return err
		}
		if a.policy != nil {
			err = a.policy.ExecutePlanStep(stepCtx, execFn)
		} else {
			err = execFn()
		}

		if err != nil {
			errCat = plan.ClassifyError(err, err.Error())
			if errors.Is(err, ErrStepCircuitOpen) {
				errCat = plan.ErrorTransient
			}
			if errCat == plan.ErrorTransient && attempt < maxRetries-1 {
				backoff := backoffDurations[attempt]
				logging.Warn("sub-agent error, retrying step",
					"step_id", step.ID, "attempt", attempt+1, "error", err.Error(),
					"category", errCat.String(), "backoff", backoff)
				a.safeSendToProgram(ui.StreamTextMsg(
					fmt.Sprintf("\n⚠️ Step %d failed (attempt %d/%d): %s\nRetrying in %v...\n",
						step.ID, attempt+1, maxRetries, err.Error(), backoff)))

				backoffTimer := time.NewTimer(backoff)
				select {
				case <-backoffTimer.C:
					continue
				case <-ctx.Done():
					backoffTimer.Stop()
					err = ctx.Err()
					errCat = plan.ClassifyError(err, err.Error())
					break
				}
			}
		}
		break
	}

	if err != nil || result == nil || result.Status == agent.AgentStatusFailed {
		if a.reliability != nil {
			a.reliability.RecordFailure()
		}

		errMsg := "unknown error"
		if err != nil {
			errMsg = err.Error()
		} else if result != nil {
			errMsg = result.Error
			errCat = plan.ClassifyError(err, errMsg)
		}

		// Transient error after all retries → pause step, plan continues with other steps
		if errCat == plan.ErrorTransient {
			reason := fmt.Sprintf("%s (after %d attempts; will auto-retry later)", errMsg, maxRetries)
			a.pauseStepWithRollback(ctx, approvedPlan, step, totalSteps, reason)

			logging.Info("step paused due to transient error, plan continues",
				"step_id", step.ID, "error", errMsg, "category", errCat.String())
			return
		}

		// Non-transient error: preserve partial output if available
		if result != nil && result.Output != "" {
			a.planManager.CompleteStep(step.ID, "(partial) "+result.Output)
			a.commitStepRollbackSnapshot(approvedPlan.ID, step.ID)
			logging.Debug("step failed but partial output preserved",
				"step_id", step.ID, "output_len", len(result.Output))
		} else {
			a.planManager.FailStep(step.ID, errMsg)
			a.commitStepRollbackSnapshot(approvedPlan.ID, step.ID)
			a.journalEvent("plan_step_failed", map[string]any{
				"plan_id": approvedPlan.ID,
				"step_id": step.ID,
				"reason":  errMsg,
			})
		}

		a.safeSendToProgram(ui.StreamTextMsg(
			fmt.Sprintf("\n  Step %d failed (%s): %s\n", step.ID, errCat.String(), errMsg)))

		// Attempt adaptive replan on fatal errors
		if errCat == plan.ErrorFatal && a.planManager.HasReplanHandler() {
			if replanErr := a.planManager.RequestReplan(ctx, step); replanErr == nil {
				a.safeSendToProgram(ui.StreamTextMsg("[Plan adjusted after step failure. Continuing.]\n"))
				logging.Info("plan replanned after fatal step error",
					"step_id", step.ID, "plan_version", approvedPlan.Version)
				return // Don't abort — the loop will pick up new steps
			} else {
				logging.Warn("replan attempt failed", "error", replanErr)
			}
		}

		if a.config.Plan.AbortOnStepFailure {
			a.safeSendToProgram(ui.StreamTextMsg("Aborting plan due to step failure.\n"))
		}
		return
	}

	// Store compact output in step and mark complete
	if a.reliability != nil {
		a.reliability.RecordSuccess()
	}

	output := result.Output
	if len(output) > planStepOutputMaxChars {
		output = output[:planStepOutputMaxChars] + "..."
	}

	// Record token usage for this step (estimate from output length for delegated steps)
	step.TokensUsed = len(output) / 4

	verificationSummary, verificationOutput, verificationOK, verificationReason := a.runStepVerificationCommands(ctx, approvedPlan, step)
	if !verificationOK {
		a.pauseStepWithRollback(ctx, approvedPlan, step, totalSteps, verificationReason)
		return
	}
	if verificationOutput != "" {
		if output != "" {
			output += "\n\n"
		}
		output += verificationOutput
		if len(output) > planStepOutputMaxChars {
			output = output[:planStepOutputMaxChars] + "..."
		}
	}
	if verificationSummary != "" {
		a.journalEvent("plan_step_verification_passed", map[string]any{
			"plan_id": approvedPlan.ID,
			"step_id": step.ID,
			"summary": verificationSummary,
		})
	}

	evidence, verificationNote, evidenceOK, evidenceReason := a.buildStepCompletionEvidence(approvedPlan, step, output)
	if !evidenceOK {
		a.pauseStepWithRollback(ctx, approvedPlan, step, totalSteps, evidenceReason)
		return
	}
	a.planManager.RecordStepVerification(step.ID, evidence, verificationNote)

	a.planManager.CompleteStep(step.ID, output)
	a.commitStepRollbackSnapshot(approvedPlan.ID, step.ID)
	a.journalEvent("plan_step_completed", map[string]any{
		"plan_id": approvedPlan.ID,
		"step_id": step.ID,
		"output":  previewForJournal(output),
	})

	// Extract agent metrics from result metadata
	if result.Metadata != nil {
		metrics := &plan.StepAgentMetrics{
			Duration: result.Duration,
		}
		if v, ok := result.Metadata["tree_total_nodes"].(int); ok {
			metrics.TotalNodes = v
		}
		if v, ok := result.Metadata["tree_max_depth"].(int); ok {
			metrics.MaxDepth = v
		}
		if v, ok := result.Metadata["tree_expanded_nodes"].(int); ok {
			metrics.ExpandedNodes = v
		}
		if v, ok := result.Metadata["tree_replan_count"].(int); ok {
			metrics.ReplanCount = v
		}
		if v, ok := result.Metadata["tree_succeeded_nodes"].(int); ok {
			metrics.SucceededNodes = v
		}
		if v, ok := result.Metadata["tree_failed_nodes"].(int); ok {
			metrics.FailedNodes = v
		}
		if metrics.TotalNodes > 0 {
			step.AgentMetrics = metrics
		}
	}

	// Store step result in SharedMemory for inter-step communication
	if sharedMem != nil {
		sharedMem.Write(
			fmt.Sprintf("step_%d_result", step.ID),
			map[string]string{
				"title":  step.Title,
				"output": output,
			},
			agent.SharedEntryTypeFact,
			fmt.Sprintf("plan_step_%d", step.ID),
		)
		logging.Debug("step result stored in shared memory",
			"step_id", step.ID, "output_len", len(output))
	}

	a.safeSendToProgram(ui.StreamTextMsg(
		fmt.Sprintf("  Step %d complete\n\n", step.ID)))
	a.safeSendToProgram(ui.PlanProgressMsg{
		PlanID:        approvedPlan.ID,
		CurrentStepID: step.ID,
		CurrentTitle:  step.Title,
		TotalSteps:    totalSteps,
		Completed:     approvedPlan.CompletedCount(),
		Progress:      approvedPlan.Progress(),
		Status:        "completed",
		Reason:        "step completed",
	})

	// Save session after each completed step (crash recovery)
	if a.sessionManager != nil {
		if err := a.sessionManager.SaveAfterMessage(); err != nil {
			logging.Warn("failed to save session after message", "error", err)
		}
	}
	a.enforceSessionMemoryGovernance("plan_step_completed")
}

// isRetryableError checks if an error is retryable (network, timeout, rate limit).
func isRetryableError(err error) bool {
	return client.IsRetryableError(err)
}

func (a *App) shouldUseSafeMode() bool {
	return a.reliability != nil && a.reliability.IsDegraded()
}

func buildContinuationRetryMessage(baseMessage string, history []*genai.Content) string {
	baseMessage = strings.TrimSpace(baseMessage)
	last := lastModelText(history)
	if last == "" {
		return "[System note: previous response was interrupted. Continue from where you stopped without repeating completed parts.]\n\n" + baseMessage
	}

	anchor := lastCompleteSentence(last)
	if anchor == "" {
		anchor = truncateTail(last, 220)
	}
	anchor = strings.TrimSpace(anchor)
	if anchor == "" {
		return "[System note: previous response was interrupted. Continue from where you stopped without repeating completed parts.]\n\n" + baseMessage
	}

	return fmt.Sprintf(
		"[System note: previous response was interrupted by a stream timeout. Continue from the last complete sentence without repeating earlier text. Last complete sentence: %q]\n\n%s",
		anchor,
		baseMessage,
	)
}

func lastModelText(history []*genai.Content) string {
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg == nil || msg.Role != genai.RoleModel {
			continue
		}

		var sb strings.Builder
		for _, part := range msg.Parts {
			if part != nil && strings.TrimSpace(part.Text) != "" {
				sb.WriteString(part.Text)
			}
		}

		text := strings.TrimSpace(sb.String())
		if text != "" {
			return text
		}
	}
	return ""
}

func lastCompleteSentence(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	for i := len(text) - 1; i >= 0; i-- {
		switch text[i] {
		case '.', '!', '?', '\n':
			return strings.TrimSpace(text[:i+1])
		}
	}
	return ""
}

func truncateTail(text string, max int) string {
	text = strings.TrimSpace(text)
	if max <= 0 || len(text) <= max {
		return text
	}
	return "..." + strings.TrimSpace(text[len(text)-max:])
}

// trimToLastModelMessage trims history back to the last model message,
// ensuring we don't persist orphaned tool results. minLen is the minimum
// length to preserve (original history before this request).
func trimToLastModelMessage(history []*genai.Content, minLen int) []*genai.Content {
	for i := len(history) - 1; i >= minLen; i-- {
		if history[i] != nil && history[i].Role == genai.RoleModel {
			return history[:i+1]
		}
	}
	return history[:minLen]
}

func maxPlanExecutionRounds(totalSteps int) int {
	if totalSteps <= 0 {
		return 40
	}
	rounds := totalSteps * 12
	if rounds < 40 {
		return 40
	}
	if rounds > 600 {
		return 600
	}
	return rounds
}

func requiresHumanCheckpoint(step *plan.Step) bool {
	if step == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(step.Title + " " + step.Description))
	if text == "" {
		return false
	}
	keywords := []string{
		"migration", "migrate", "drop table", "drop database",
		"mass update", "bulk delete", "deploy", "production",
		"billing", "payment",
	}
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// finalizePlanLifecycleState applies a validated terminal/non-terminal lifecycle state
// after an execution round finishes.
func (a *App) finalizePlanLifecycleState(p *plan.Plan) {
	if a.planManager == nil || p == nil {
		return
	}

	target := plan.LifecycleFailed
	switch {
	case p.Status == plan.StatusPaused || p.HasPausedSteps():
		target = plan.LifecyclePaused
	case p.Status == plan.StatusCompleted:
		target = plan.LifecycleCompleted
	case p.Status == plan.StatusFailed:
		target = plan.LifecycleFailed
	case p.StepCount() > 0 && p.CompletedCount() == p.StepCount():
		target = plan.LifecycleCompleted
	}

	if err := a.planManager.TransitionCurrentPlanLifecycle(target); err != nil {
		logging.Warn("failed to finalize plan lifecycle state", "target", string(target), "error", err)
	}
}

// formatPlanSummary generates a rich summary after plan execution completes.
func (a *App) formatPlanSummary(p *plan.Plan, duration time.Duration) string {
	var sb strings.Builder
	steps := p.GetStepsSnapshot()

	sb.WriteString("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	sb.WriteString("  Plan Execution Summary\n")
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	sb.WriteString(fmt.Sprintf("  Plan: %s\n", p.Title))
	sb.WriteString(fmt.Sprintf("  Duration: %s", formatDuration(duration)))
	if p.Version > 0 {
		sb.WriteString(fmt.Sprintf("  (v%d)", p.Version))
	}
	sb.WriteString("\n\n")

	completed, failed, skipped, totalTokens := 0, 0, 0, 0
	for _, step := range steps {
		totalTokens += step.TokensUsed
		switch step.Status {
		case plan.StatusCompleted:
			completed++
			sb.WriteString(fmt.Sprintf("  ✓ Step %d: %s (%s)\n", step.ID, step.Title, formatDuration(step.Duration())))
		case plan.StatusFailed:
			failed++
			errMsg := step.Error
			if len(errMsg) > 80 {
				errMsg = errMsg[:80] + "..."
			}
			sb.WriteString(fmt.Sprintf("  ✗ Step %d: %s — %s\n", step.ID, step.Title, errMsg))
		case plan.StatusSkipped:
			skipped++
			sb.WriteString(fmt.Sprintf("  ⊘ Step %d: %s (skipped)\n", step.ID, step.Title))
		case plan.StatusPaused:
			sb.WriteString(fmt.Sprintf("  ⏸ Step %d: %s (paused)\n", step.ID, step.Title))
		default:
			sb.WriteString(fmt.Sprintf("  ○ Step %d: %s (pending)\n", step.ID, step.Title))
		}
		if step.AgentMetrics != nil {
			m := step.AgentMetrics
			fmt.Fprintf(&sb, "    Agent: %d nodes, depth %d", m.TotalNodes, m.MaxDepth)
			if m.ReplanCount > 0 {
				fmt.Fprintf(&sb, ", %d replans", m.ReplanCount)
			}
			sb.WriteString("\n")
		}
	}

	sb.WriteString(fmt.Sprintf("\n  Results: %d completed", completed))
	if failed > 0 {
		sb.WriteString(fmt.Sprintf(", %d failed", failed))
	}
	if skipped > 0 {
		sb.WriteString(fmt.Sprintf(", %d skipped", skipped))
	}
	sb.WriteString(fmt.Sprintf(" / %d total\n", len(steps)))
	if totalTokens > 0 {
		sb.WriteString(fmt.Sprintf("  Tokens used: ~%d\n", totalTokens))
	}
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	return sb.String()
}

// formatDuration formats a duration as a human-readable string.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}

func (a *App) pauseStepWithRollback(ctx context.Context, approvedPlan *plan.Plan, step *plan.Step, totalSteps int, reason string) {
	if approvedPlan == nil || step == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "step paused by contract gate"
	}

	rollbackApplied, rollbackSummary := a.rollbackStepSnapshot(ctx, approvedPlan, step)
	finalReason := reason
	if rollbackSummary != "" {
		finalReason = reason + " | " + rollbackSummary
	}

	a.planManager.PauseStep(step.ID, finalReason)
	a.journalEvent("plan_step_paused", map[string]any{
		"plan_id":          approvedPlan.ID,
		"step_id":          step.ID,
		"reason":           finalReason,
		"rollback_applied": rollbackApplied,
		"rollback_summary": rollbackSummary,
	})

	message := fmt.Sprintf("⏸ Step %d paused: %s\n", step.ID, reason)
	if rollbackSummary != "" {
		if rollbackApplied {
			message += fmt.Sprintf("↩ %s\n", rollbackSummary)
		} else {
			message += fmt.Sprintf("⚠ Rollback issue: %s\n", rollbackSummary)
		}
	}
	a.safeSendToProgram(ui.StreamTextMsg(message))
	a.safeSendToProgram(ui.PlanProgressMsg{
		PlanID:        approvedPlan.ID,
		CurrentStepID: step.ID,
		CurrentTitle:  step.Title,
		TotalSteps:    totalSteps,
		Completed:     approvedPlan.CompletedCount(),
		Progress:      approvedPlan.Progress(),
		Status:        "paused",
		Reason:        finalReason,
	})
}

func (a *App) runStepVerificationCommands(ctx context.Context, approvedPlan *plan.Plan, step *plan.Step) (string, string, bool, string) {
	if step == nil {
		return "", "", false, "missing step context for verification"
	}

	required := normalizeVerifyCommands(step.VerifyCommands)
	if len(required) == 0 {
		return "", "", false, fmt.Sprintf("step %d is missing verify_commands contract", step.ID)
	}

	bashTool, ok := a.registry.Get("bash")
	if !ok {
		return "", "", false, "cannot run verify_commands: bash tool is unavailable"
	}

	verifyCtx, cancel := context.WithTimeout(ctx, planStepVerifyTimeout)
	defer cancel()
	projectProfile := detectDoneGateProfile(a.workDir)

	lines := make([]string, 0, len(required))
	for i, cmd := range required {
		if safe, safetyReason := a.validateVerifyCommandSafety(cmd, projectProfile); !safe {
			return "", "", false, fmt.Sprintf("unsafe verify command blocked: %s (%s)", cmd, safetyReason)
		}

		wrapped := cmd
		if wd := strings.TrimSpace(a.workDir); wd != "" {
			wrapped = "cd " + shellQuote(wd) + " && " + cmd
		}

		args := map[string]any{
			"command":     wrapped,
			"description": fmt.Sprintf("plan step %d verify %d/%d", step.ID, i+1, len(required)),
		}

		if a.permManager != nil && a.permManager.IsEnabled() {
			resp, err := a.permManager.Check(verifyCtx, "bash", args)
			if err != nil {
				return "", "", false, fmt.Sprintf("verify command blocked: %v", err)
			}
			if resp != nil && !resp.Allowed {
				reason := strings.TrimSpace(resp.Reason)
				if reason == "" {
					reason = "permission denied"
				}
				return "", "", false, fmt.Sprintf("verify command blocked: %s", reason)
			}
		}

		a.safeSendToProgram(ui.StreamTextMsg(
			fmt.Sprintf("  ↳ verify %d/%d: %s\n", i+1, len(required), cmd)))

		if approvedPlan != nil {
			approvedPlan.RecordStepEffect(step.ID, "bash", map[string]any{"command": wrapped})
			if a.planManager != nil {
				_ = a.planManager.SaveCurrentPlan()
			}
		}

		result, err := bashTool.Execute(verifyCtx, args)
		if err != nil {
			return "", "", false, fmt.Sprintf("verify command failed: %s (%v)", cmd, err)
		}

		detail := strings.TrimSpace(result.Content)
		if len(detail) > 240 {
			detail = detail[:240] + "..."
		}
		if !result.Success {
			failure := strings.TrimSpace(result.Error)
			if failure == "" {
				failure = detail
			}
			if failure == "" {
				failure = "command returned non-success status"
			}
			return "", "", false, fmt.Sprintf("verify command failed: %s (%s)", cmd, failure)
		}

		if detail == "" {
			lines = append(lines, fmt.Sprintf("PASS `%s`", cmd))
		} else {
			lines = append(lines, fmt.Sprintf("PASS `%s`: %s", cmd, detail))
		}
	}

	summary := fmt.Sprintf("verify_commands_passed=%d/%d", len(required), len(required))
	return summary, "Verification results:\n- " + strings.Join(lines, "\n- "), true, ""
}

func (a *App) buildStepCompletionEvidence(p *plan.Plan, step *plan.Step, output string) ([]string, string, bool, string) {
	if p == nil || step == nil {
		return nil, "", false, "missing step context"
	}

	ledger := p.GetRunLedgerSnapshot()
	entry := ledger[step.ID]

	evidence := make([]string, 0, 14)
	hasToolCalls := false
	hasTools := false
	hasFiles := false
	hasCommands := false
	verificationByCommand := false
	if entry != nil {
		if entry.ToolCalls > 0 {
			evidence = append(evidence, fmt.Sprintf("tool_calls=%d", entry.ToolCalls))
			hasToolCalls = true
		}
		if len(entry.Tools) > 0 {
			evidence = append(evidence, "tools="+joinLimited(entry.Tools, 4))
			hasTools = true
		}
		if len(entry.FilesTouched) > 0 {
			evidence = append(evidence, "files="+joinLimited(entry.FilesTouched, 4))
			hasFiles = true
		}
		if len(entry.Commands) > 0 {
			evidence = append(evidence, "commands="+joinLimited(entry.Commands, 3))
			hasCommands = true
			verificationByCommand = commandsContainVerificationSignals(entry.Commands)
		}
	}

	output = strings.TrimSpace(output)
	hasOutput := false
	if output != "" {
		compact := output
		if len(compact) > 260 {
			compact = compact[:260] + "..."
		}
		evidence = append(evidence, "output="+compact)
		hasOutput = true
	}

	hasOperationalProof := hasToolCalls || hasTools || hasFiles || hasCommands
	artifactHints := collectExpectedArtifactHints(step)
	explicitArtifactPaths := normalizeExpectedArtifactPaths(step.ExpectedArtifactPaths)
	if a != nil && a.config != nil && a.config.Plan.RequireExpectedArtifactPaths {
		if stepLikelyMutatesFiles(step, entry) && len(explicitArtifactPaths) == 0 {
			return nil, "", false, fmt.Sprintf(
				"step %d is missing expected_artifact_paths while mutating files (strict mode)",
				step.ID,
			)
		}
	}
	artifactHintsProof, matchedArtifactHints, missingArtifactHints := artifactHintsCovered(artifactHints, entry, output)
	hasArtifactProof := hasFiles || hasCommands || hasOutput
	if len(artifactHints) > 0 {
		hasArtifactProof = hasArtifactProof && artifactHintsProof
	}
	requiresArtifactProof := strings.TrimSpace(step.ExpectedArtifact) != ""
	requiredVerifyCommands := normalizeVerifyCommands(step.VerifyCommands)
	verifyCommandsSatisfied := verifyCommandsCovered(requiredVerifyCommands, entry)
	requiresVerificationProof := len(requiredVerifyCommands) > 0 || criteriaRequireVerificationSignals(step.SuccessCriteria, step.Description)
	verificationByOutput := outputContainsVerificationSignals(output)
	hasVerificationProof := verificationByCommand || verificationByOutput || verifyCommandsSatisfied

	evidenceClasses := 0
	if hasOperationalProof {
		evidenceClasses++
	}
	if hasOutput {
		evidenceClasses++
	}
	if hasVerificationProof {
		evidenceClasses++
	}

	requiredClasses := 1
	if requiresArtifactProof || len(step.SuccessCriteria) > 0 {
		requiredClasses = 2
	}

	if requiresArtifactProof && len(artifactHints) > 0 && !artifactHintsProof {
		return nil, "", false, fmt.Sprintf(
			"step %d missing artifact proof for expected paths: %s",
			step.ID,
			joinLimited(missingArtifactHints, 4),
		)
	}
	if requiresArtifactProof && !hasArtifactProof {
		return nil, "", false, fmt.Sprintf("step %d lacks proof for expected artifact", step.ID)
	}
	if len(requiredVerifyCommands) > 0 && !verifyCommandsSatisfied {
		return nil, "", false, fmt.Sprintf("step %d missing proof that required verify_commands were executed", step.ID)
	}
	if requiresVerificationProof && !hasVerificationProof {
		return nil, "", false, fmt.Sprintf("step %d lacks verification proof for success criteria", step.ID)
	}
	if evidenceClasses < requiredClasses {
		return nil, "", false, fmt.Sprintf(
			"step %d needs stronger proof (%d/%d evidence classes: operational/output/verification)",
			step.ID, evidenceClasses, requiredClasses,
		)
	}

	if len(evidence) == 0 {
		return nil, "", false, "cannot complete step without proof (no diff/command/output evidence)"
	}

	proofJSON := buildStepCompletionProofJSON(
		step,
		hasArtifactProof,
		hasVerificationProof,
		hasOperationalProof,
		hasOutput,
		evidenceClasses,
		requiredClasses,
		len(requiredVerifyCommands),
		verifyCommandsSatisfied,
		len(artifactHints),
		artifactHintsProof,
		matchedArtifactHints,
		missingArtifactHints,
		entry,
	)
	if proofJSON != "" {
		evidence = append(evidence, "proof_json="+proofJSON)
	}

	evidence = append(evidence, fmt.Sprintf("contract.artifact_proof=%t", hasArtifactProof))
	evidence = append(evidence, fmt.Sprintf("contract.artifact_hints=%d", len(artifactHints)))
	evidence = append(evidence, fmt.Sprintf("contract.artifact_hints_proof=%t", artifactHintsProof))
	if len(matchedArtifactHints) > 0 {
		evidence = append(evidence, "artifact_hints_matched="+joinLimited(matchedArtifactHints, 6))
	}
	evidence = append(evidence, fmt.Sprintf("contract.verification_proof=%t", hasVerificationProof))
	evidence = append(evidence, fmt.Sprintf("contract.verify_commands=%d", len(requiredVerifyCommands)))
	evidence = append(evidence, fmt.Sprintf("contract.verify_commands_proof=%t", verifyCommandsSatisfied))
	evidence = append(evidence, fmt.Sprintf("contract.evidence_classes=%d/%d", evidenceClasses, requiredClasses))

	note := fmt.Sprintf(
		"Contract verification: artifact_proof=%t, artifact_hints=%d(proof=%t), verification_proof=%t, verify_commands=%d(proof=%t), evidence_classes=%d/%d; success_criteria=%d; expected_artifact=%s; evidence_items=%d.",
		hasArtifactProof,
		len(artifactHints),
		artifactHintsProof,
		hasVerificationProof,
		len(requiredVerifyCommands),
		verifyCommandsSatisfied,
		evidenceClasses,
		requiredClasses,
		len(step.SuccessCriteria),
		strings.TrimSpace(step.ExpectedArtifact),
		len(evidence),
	)
	return evidence, note, true, ""
}

func joinLimited(items []string, limit int) string {
	if len(items) == 0 {
		return ""
	}
	if limit <= 0 || len(items) <= limit {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:limit], ", ") + fmt.Sprintf(", ...(+%d)", len(items)-limit)
}

func criteriaRequireVerificationSignals(criteria []string, description string) bool {
	keywords := []string{
		"verify", "verification", "test", "lint", "build", "compile",
		"check", "typecheck", "vet", "pass", "validate", "validated",
		"проверь", "провер", "тест", "линт", "сборк", "валидац",
	}

	texts := make([]string, 0, len(criteria)+1)
	for _, c := range criteria {
		c = strings.TrimSpace(strings.ToLower(c))
		if c != "" {
			texts = append(texts, c)
		}
	}
	desc := strings.TrimSpace(strings.ToLower(description))
	if desc != "" {
		texts = append(texts, desc)
	}

	for _, text := range texts {
		for _, kw := range keywords {
			if strings.Contains(text, kw) {
				return true
			}
		}
	}
	return false
}

func commandsContainVerificationSignals(commands []string) bool {
	keywords := []string{
		" test", "go test", "pytest", "cargo test", "npm test", "pnpm test", "yarn test", "bun test",
		"lint", "typecheck", "check", "verify", "vet", "build", "compile",
	}
	for _, cmd := range commands {
		lower := " " + strings.ToLower(strings.TrimSpace(cmd))
		for _, kw := range keywords {
			if strings.Contains(lower, kw) {
				return true
			}
		}
	}
	return false
}

func outputContainsVerificationSignals(output string) bool {
	lower := strings.ToLower(strings.TrimSpace(output))
	if lower == "" {
		return false
	}

	negative := []string{" failed", " failure", " error", "panic", "traceback", "assertionerror"}
	for _, marker := range negative {
		if strings.Contains(" "+lower, marker) {
			return false
		}
	}

	positive := []string{
		"passed", "successful", "verified", "no issues", "no errors",
		"build succeeded", "all tests passed", "lint passed", "check passed",
		"успеш", "проверен", "проверка пройдена", "без ошибок",
	}
	for _, marker := range positive {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func (a *App) validateVerifyCommandSafety(command string, projectProfile doneGateProfile) (bool, string) {
	command = strings.TrimSpace(command)
	if command == "" {
		return false, "empty command"
	}

	lower := strings.ToLower(command)

	denyContains := normalizePolicyMarkers(defaultVerifyPolicyDenyContains())
	allowContains := make([]string, 0)
	requireIntent := true
	if a != nil && a.config != nil {
		policy := a.config.Plan.VerifyPolicy
		if policy.Enabled {
			requireIntent = policy.RequireVerificationIntent
			allowContains = append(allowContains, normalizePolicyMarkers(policy.AllowContains)...)
			denyContains = append(denyContains, normalizePolicyMarkers(policy.DenyContains)...)
			for _, profileKey := range a.resolveVerifyPolicyProfiles(lower, projectProfile) {
				profileCfg, ok := policy.Profiles[profileKey]
				if !ok {
					continue
				}
				allowContains = append(allowContains, normalizePolicyMarkers(profileCfg.AllowContains)...)
				denyContains = append(denyContains, normalizePolicyMarkers(profileCfg.DenyContains)...)
			}
		} else {
			requireIntent = false
		}
	}
	allowContains = dedupePolicyMarkers(allowContains)
	denyContains = dedupePolicyMarkers(denyContains)
	if commandContainsAny(lower, denyContains) {
		return false, "contains disallowed mutating operation"
	}
	if len(allowContains) > 0 && !commandContainsAny(lower, allowContains) {
		return false, "command does not match allowlist markers for active verify policy"
	}

	// Allow /dev/null redirection, deny file-writing redirects in verify stage.
	if strings.Contains(lower, ">>") {
		return false, "append redirection is not allowed in verify commands"
	}
	redirectionCheck := lower
	for _, allowed := range []string{"2>&1", "1>/dev/null", "2>/dev/null", ">/dev/null"} {
		redirectionCheck = strings.ReplaceAll(redirectionCheck, allowed, "")
	}
	if strings.Contains(redirectionCheck, ">") {
		return false, "output redirection to files is not allowed in verify commands"
	}

	validator := tools.NewDefaultSafetyValidator()
	check, err := validator.ValidateSafety(context.Background(), "bash", map[string]any{"command": command})
	if err != nil {
		return false, err.Error()
	}
	if check != nil {
		if !check.IsValid {
			return false, strings.Join(check.Errors, "; ")
		}
		for _, warning := range check.Warnings {
			if strings.Contains(strings.ToLower(warning), "destructive operation detected") {
				return false, warning
			}
		}
	}

	if requireIntent && !looksLikeVerificationCommand(lower) {
		return false, "command does not match verification intent"
	}

	return true, ""
}

func defaultVerifyPolicyDenyContains() []string {
	return []string{
		"rm -", "rm -rf", " mv ", " cp ", " chmod ", " chown ", "sudo ",
		"mkfs", " dd ", "git reset", "git clean", "git checkout --",
		"git commit", "git push", "git stash", "npm install", "npm i ",
		"pnpm add", "yarn add", "pip install", "go get ", "cargo add ",
		"brew install", "apt install", "apk add", "dnf install", "pacman -s",
		"curl |", "wget |",
	}
}

func normalizePolicyMarkers(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func dedupePolicyMarkers(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func commandContainsAny(lowerCommand string, markers []string) bool {
	lowerCommand = strings.ToLower(lowerCommand)
	guarded := " " + lowerCommand
	for _, marker := range markers {
		marker = strings.TrimSpace(strings.ToLower(marker))
		if marker == "" {
			continue
		}
		if strings.Contains(guarded, marker) {
			return true
		}
	}
	return false
}

func stepLikelyMutatesFiles(step *plan.Step, entry *plan.RunLedgerEntry) bool {
	if entry == nil {
		return false
	}

	for _, toolName := range entry.Tools {
		if isMutatingToolName(toolName) {
			return true
		}
	}

	requiredVerify := normalizeVerifyCommands(nil)
	if step != nil {
		requiredVerify = normalizeVerifyCommands(step.VerifyCommands)
	}
	for _, cmd := range entry.Commands {
		if isCommandCoveredByVerify(cmd, requiredVerify) {
			continue
		}
		if commandLooksMutating(cmd) {
			return true
		}
	}
	return false
}

func isMutatingToolName(name string) bool {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "write", "edit", "batch", "move", "delete", "mkdir", "atomicwrite":
		return true
	default:
		return false
	}
}

func isCommandCoveredByVerify(command string, required []string) bool {
	if len(required) == 0 {
		return false
	}
	fingerprint := normalizeCommandFingerprint(command)
	if fingerprint == "" {
		return false
	}
	for _, verifyCmd := range required {
		v := normalizeCommandFingerprint(verifyCmd)
		if v == "" {
			continue
		}
		if fingerprint == v || strings.Contains(fingerprint, v) || strings.Contains(v, fingerprint) {
			return true
		}
	}
	return false
}

func commandLooksMutating(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	if lower == "" {
		return false
	}
	if looksLikeVerificationCommand(lower) {
		return false
	}
	markers := []string{
		"rm -", "mv ", "cp ", "chmod ", "chown ", "truncate ", "touch ",
		"tee ", "sed -i", "perl -pi", "cat >", "echo >", ">>", "apply_patch",
		"git add", "git rm", "git mv", "git commit", "git checkout --",
		"npm install", "npm i ", "pnpm add", "yarn add", "pip install",
		"go get ", "cargo add ", "mkdir ", "rmdir ",
	}
	return commandContainsAny(lower, markers)
}

func (a *App) resolveVerifyPolicyProfiles(lowerCommand string, profile doneGateProfile) []string {
	keys := []string{"default"}
	seen := map[string]bool{"default": true}
	appendKey := func(key string) {
		key = strings.TrimSpace(strings.ToLower(key))
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		keys = append(keys, key)
	}

	present := a.projectVerifyStacks(profile)
	if len(present) == 1 {
		appendKey(present[0])
		return keys
	}

	stackKeywords := map[string][]string{
		"go":     {"go test", "go vet", "go build"},
		"node":   {"npm ", "pnpm ", "yarn ", "bun ", "eslint", "vitest", "jest", "tsc"},
		"python": {"pytest", "ruff", "mypy", "python -m"},
		"rust":   {"cargo "},
		"java":   {"mvn ", "gradle", "./gradlew"},
		"cmake":  {"cmake", "ctest"},
		"bazel":  {"bazel "},
		"make":   {"make "},
		"php":    {"composer", "phpunit", "phpstan"},
	}

	for _, stack := range present {
		keywords := stackKeywords[stack]
		for _, kw := range keywords {
			if strings.Contains(lowerCommand, kw) {
				appendKey(stack)
				break
			}
		}
	}

	return keys
}

func (a *App) projectVerifyStacks(profile doneGateProfile) []string {
	stacks := make([]string, 0, 9)
	if len(profile.GoModules) > 0 {
		stacks = append(stacks, "go")
	}
	if len(profile.NodeProjects) > 0 {
		stacks = append(stacks, "node")
	}
	if len(profile.PythonRoots) > 0 {
		stacks = append(stacks, "python")
	}
	if len(profile.RustModules) > 0 {
		stacks = append(stacks, "rust")
	}
	if len(profile.JavaProjects) > 0 {
		stacks = append(stacks, "java")
	}
	if len(profile.CMakeProjects) > 0 {
		stacks = append(stacks, "cmake")
	}
	if len(profile.BazelRoots) > 0 {
		stacks = append(stacks, "bazel")
	}
	if len(profile.MakeProjects) > 0 {
		stacks = append(stacks, "make")
	}
	if len(profile.PHPProjects) > 0 {
		stacks = append(stacks, "php")
	}
	return stacks
}

func looksLikeVerificationCommand(lowerCommand string) bool {
	if strings.TrimSpace(lowerCommand) == "" {
		return false
	}
	signals := []string{
		" test", "go test", "pytest", "cargo test", "npm test", "pnpm test", "yarn test", "bun test",
		"lint", "typecheck", "check", "verify", "vet", "build", "compile", "validate",
		"git diff --check", "git status", "git rev-parse", "mvn test", "gradle test", "composer validate",
		"bazel test", "cmake --build", "make test", "phpstan", "ruff", "mypy",
	}
	padded := " " + lowerCommand
	for _, signal := range signals {
		if strings.Contains(padded, signal) {
			return true
		}
	}
	return false
}

func normalizeVerifyCommands(commands []string) []string {
	if len(commands) == 0 {
		return nil
	}
	out := make([]string, 0, len(commands))
	seen := make(map[string]bool, len(commands))
	for _, cmd := range commands {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}
		fingerprint := normalizeCommandFingerprint(cmd)
		if fingerprint == "" || seen[fingerprint] {
			continue
		}
		seen[fingerprint] = true
		out = append(out, cmd)
	}
	return out
}

func normalizeExpectedArtifactPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		fingerprint := normalizePathHintForMatch(path)
		if fingerprint == "" || seen[fingerprint] {
			continue
		}
		seen[fingerprint] = true
		out = append(out, path)
	}
	return out
}

func normalizeCommandFingerprint(cmd string) string {
	cmd = strings.TrimSpace(strings.ToLower(cmd))
	if cmd == "" {
		return ""
	}
	return strings.Join(strings.Fields(cmd), " ")
}

func verifyCommandsCovered(required []string, entry *plan.RunLedgerEntry) bool {
	if len(required) == 0 {
		return true
	}
	if entry == nil || len(entry.Commands) == 0 {
		return false
	}

	executed := make([]string, 0, len(entry.Commands))
	for _, cmd := range entry.Commands {
		fingerprint := normalizeCommandFingerprint(cmd)
		if fingerprint == "" {
			continue
		}
		executed = append(executed, fingerprint)
	}
	if len(executed) == 0 {
		return false
	}

	for _, requiredCmd := range required {
		requiredFingerprint := normalizeCommandFingerprint(requiredCmd)
		if requiredFingerprint == "" {
			continue
		}
		found := false
		for _, executedCmd := range executed {
			if executedCmd == requiredFingerprint ||
				strings.Contains(executedCmd, requiredFingerprint) ||
				strings.Contains(requiredFingerprint, executedCmd) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func extractArtifactPathHints(expectedArtifact string) []string {
	expectedArtifact = strings.TrimSpace(expectedArtifact)
	if expectedArtifact == "" {
		return nil
	}

	tokens := strings.FieldsFunc(expectedArtifact, func(r rune) bool {
		switch r {
		case ' ', '\n', '\t', ',', ';', '|':
			return true
		default:
			return false
		}
	})

	hints := make([]string, 0, len(tokens))
	seen := make(map[string]bool)
	for _, token := range tokens {
		candidate := normalizePathToken(token)
		if !looksLikeArtifactPathHint(candidate) {
			continue
		}
		normalized := normalizePathHintForMatch(candidate)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		hints = append(hints, candidate)
	}
	return hints
}

func collectExpectedArtifactHints(step *plan.Step) []string {
	if step == nil {
		return nil
	}
	hints := make([]string, 0, len(step.ExpectedArtifactPaths)+4)
	seen := make(map[string]bool)
	appendHint := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		fingerprint := normalizePathHintForMatch(raw)
		if fingerprint == "" || seen[fingerprint] {
			return
		}
		seen[fingerprint] = true
		hints = append(hints, raw)
	}

	for _, p := range normalizeExpectedArtifactPaths(step.ExpectedArtifactPaths) {
		appendHint(p)
	}
	for _, inferred := range extractArtifactPathHints(step.ExpectedArtifact) {
		appendHint(inferred)
	}
	return hints
}

func looksLikeArtifactPathHint(token string) bool {
	if token == "" {
		return false
	}
	if strings.HasPrefix(token, "/") || strings.HasPrefix(token, "./") || strings.HasPrefix(token, "../") {
		return true
	}
	if strings.Contains(token, "/") || strings.Contains(token, "\\") {
		return true
	}
	ext := filepath.Ext(token)
	if ext != "" && len(ext) <= 8 && len(token) > len(ext) {
		return true
	}
	return false
}

func normalizePathHintForMatch(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, "\\", "/")
	value = strings.Trim(value, "\"'`")
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.TrimPrefix(value, "./")
	value = strings.TrimPrefix(value, "/")
	value = strings.Trim(value, "/")
	return value
}

func artifactHintsCovered(hints []string, entry *plan.RunLedgerEntry, output string) (bool, []string, []string) {
	if len(hints) == 0 {
		return true, nil, nil
	}

	candidates := make([]string, 0, 16)
	addCandidate := func(raw string) {
		normalized := normalizePathHintForMatch(raw)
		if normalized == "" {
			return
		}
		candidates = append(candidates, normalized)
	}

	if entry != nil {
		for _, file := range entry.FilesTouched {
			addCandidate(file)
		}
		for _, cmd := range entry.Commands {
			for _, pathHint := range extractCommandPathHints(cmd) {
				addCandidate(pathHint)
			}
		}
	}
	for _, outputHint := range extractArtifactPathHints(output) {
		addCandidate(outputHint)
	}

	matched := make([]string, 0, len(hints))
	missing := make([]string, 0, len(hints))
	for _, hint := range hints {
		hintNorm := normalizePathHintForMatch(hint)
		if hintNorm == "" {
			continue
		}
		found := false
		for _, candidate := range candidates {
			if candidate == hintNorm ||
				strings.HasSuffix(candidate, "/"+hintNorm) ||
				strings.Contains(candidate, hintNorm) ||
				strings.Contains(hintNorm, candidate) {
				found = true
				break
			}
		}
		if found {
			matched = append(matched, hint)
		} else {
			missing = append(missing, hint)
		}
	}
	return len(missing) == 0, matched, missing
}

func buildStepContractJSON(step *plan.Step, totalSteps int) string {
	if step == nil {
		return ""
	}

	payload := map[string]any{
		"step_id":                 step.ID,
		"total_steps":             totalSteps,
		"title":                   strings.TrimSpace(step.Title),
		"description":             strings.TrimSpace(step.Description),
		"inputs":                  step.Inputs,
		"expected_artifact":       strings.TrimSpace(step.ExpectedArtifact),
		"expected_artifact_paths": normalizeExpectedArtifactPaths(step.ExpectedArtifactPaths),
		"success_criteria":        step.SuccessCriteria,
		"verify_commands":         normalizeVerifyCommands(step.VerifyCommands),
		"rollback":                strings.TrimSpace(step.Rollback),
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ""
	}
	return string(data)
}

func buildStepCompletionProofJSON(
	step *plan.Step,
	hasArtifactProof, hasVerificationProof, hasOperationalProof, hasOutput bool,
	evidenceClasses, requiredClasses int,
	verifyCommandsCount int,
	verifyCommandsProof bool,
	artifactHintsCount int,
	artifactHintsProof bool,
	artifactHintsMatched []string,
	artifactHintsMissing []string,
	entry *plan.RunLedgerEntry,
) string {
	if step == nil {
		return ""
	}

	proof := map[string]any{
		"step_id":                 step.ID,
		"expected_artifact":       strings.TrimSpace(step.ExpectedArtifact),
		"expected_artifact_paths": normalizeExpectedArtifactPaths(step.ExpectedArtifactPaths),
		"success_criteria_count":  len(step.SuccessCriteria),
		"artifact_proof":          hasArtifactProof,
		"verification_proof":      hasVerificationProof,
		"operational_proof":       hasOperationalProof,
		"output_proof":            hasOutput,
		"evidence_classes":        evidenceClasses,
		"required_classes":        requiredClasses,
		"verify_commands_count":   verifyCommandsCount,
		"verify_commands_proof":   verifyCommandsProof,
		"artifact_hints_count":    artifactHintsCount,
		"artifact_hints_proof":    artifactHintsProof,
	}
	if len(artifactHintsMatched) > 0 {
		proof["artifact_hints_matched"] = artifactHintsMatched
	}
	if len(artifactHintsMissing) > 0 {
		proof["artifact_hints_missing"] = artifactHintsMissing
	}

	if entry != nil {
		proof["tool_calls"] = entry.ToolCalls
		proof["tools"] = entry.Tools
		proof["files_touched"] = entry.FilesTouched
		proof["commands"] = entry.Commands
	}

	data, err := json.Marshal(proof)
	if err != nil {
		return ""
	}
	return string(data)
}

// StepPromptContext holds context for building step prompts.
type StepPromptContext struct {
	Step            *plan.Step
	PrevSummary     string
	PlanTitle       string
	PlanDescription string
	PlanRequest     string
	ContextSnapshot string
	SharedMemoryCtx string
	TotalSteps      int
	CompletedCount  int
}

func buildStepPrompt(ctx *StepPromptContext) string {
	var sb strings.Builder

	// Plan overview (helps sub-agent understand the overall goal)
	sb.WriteString("# Plan Execution Context\n\n")
	sb.WriteString(fmt.Sprintf("**Plan:** %s\n", ctx.PlanTitle))
	if ctx.PlanDescription != "" {
		sb.WriteString(fmt.Sprintf("**Goal:** %s\n", ctx.PlanDescription))
	}
	if ctx.PlanRequest != "" && len(ctx.PlanRequest) < 500 {
		sb.WriteString(fmt.Sprintf("**Original Request:** %s\n", ctx.PlanRequest))
	}
	sb.WriteString(fmt.Sprintf("**Progress:** Step %d of %d (%d completed)\n\n",
		ctx.Step.ID, ctx.TotalSteps, ctx.CompletedCount))

	// Context from planning discussion (key decisions)
	if ctx.ContextSnapshot != "" {
		sb.WriteString("## Key Decisions from Planning\n")
		sb.WriteString(ctx.ContextSnapshot)
		sb.WriteString("\n")
	}

	// Shared memory from previous steps (inter-agent knowledge)
	if ctx.SharedMemoryCtx != "" {
		sb.WriteString(ctx.SharedMemoryCtx)
	}

	// Current step details
	sb.WriteString(fmt.Sprintf("## Current Step %d: %s\n", ctx.Step.ID, ctx.Step.Title))
	if ctx.Step.Description != "" {
		sb.WriteString(ctx.Step.Description)
		sb.WriteString("\n")
	}
	sb.WriteString("\n### Step Contract\n")
	if len(ctx.Step.Inputs) > 0 {
		sb.WriteString("- Inputs:\n")
		for _, in := range ctx.Step.Inputs {
			sb.WriteString(fmt.Sprintf("  - %s\n", in))
		}
	}
	if ctx.Step.ExpectedArtifact != "" {
		sb.WriteString(fmt.Sprintf("- Expected artifact: %s\n", ctx.Step.ExpectedArtifact))
	}
	if len(ctx.Step.ExpectedArtifactPaths) > 0 {
		sb.WriteString("- Expected artifact paths:\n")
		for _, path := range ctx.Step.ExpectedArtifactPaths {
			sb.WriteString(fmt.Sprintf("  - %s\n", path))
		}
	}
	if len(ctx.Step.SuccessCriteria) > 0 {
		sb.WriteString("- Success criteria:\n")
		for _, c := range ctx.Step.SuccessCriteria {
			sb.WriteString(fmt.Sprintf("  - %s\n", c))
		}
	}
	if len(ctx.Step.VerifyCommands) > 0 {
		sb.WriteString("- Verify commands:\n")
		for _, cmd := range ctx.Step.VerifyCommands {
			sb.WriteString(fmt.Sprintf("  - %s\n", cmd))
		}
	}
	if ctx.Step.Rollback != "" {
		sb.WriteString(fmt.Sprintf("- Rollback: %s\n", ctx.Step.Rollback))
	}
	if contractJSON := buildStepContractJSON(ctx.Step, ctx.TotalSteps); contractJSON != "" {
		sb.WriteString("\n### StepContractJSON\n")
		sb.WriteString("```json\n")
		sb.WriteString(contractJSON)
		sb.WriteString("\n```\n")
	}

	// Previous steps summary (compact)
	if ctx.PrevSummary != "" {
		sb.WriteString("\n## Previous Steps Summary\n")
		sb.WriteString(ctx.PrevSummary)
	}

	sb.WriteString("\n## Execution Rules\n")
	sb.WriteString("- Read files before editing\n")
	sb.WriteString("- Execute exactly what this step describes\n")
	sb.WriteString("- Build upon work from previous steps\n")
	sb.WriteString("- Provide a brief summary of what was done\n")
	sb.WriteString("- Provide explicit evidence (files/commands/output) for completion\n")
	sb.WriteString("- Verify commands are mandatory and must pass before completion\n")
	sb.WriteString("- Report any issues or deviations from the plan\n")

	return sb.String()
}

// extractContextSnapshot creates a summary of the current session context.
// This preserves key decisions and findings from the planning conversation.
// It also creates a structured ContextSnapshot and saves it to SharedMemory.
func (a *App) extractContextSnapshot() string {
	history := a.session.GetHistory()
	if len(history) < 4 {
		return "" // Not enough context to summarize
	}

	// Create structured snapshot for SharedMemory
	snapshot := agent.NewContextSnapshot()

	var sb strings.Builder
	sb.WriteString("## Context from Planning Discussion\n\n")

	// Extract key points from recent messages (skip system prompt)
	messageCount := 0
	maxMessages := 6 // Last 3 turns (6 messages)

	for i := len(history) - 1; i >= 0 && messageCount < maxMessages; i-- {
		content := history[i]
		if content == nil || len(content.Parts) == 0 {
			continue
		}

		role := "User"
		if content.Role == "model" {
			role = "Assistant"
		}

		// Extract text content from parts
		for _, part := range content.Parts {
			if part != nil && part.Text != "" {
				text := part.Text

				// Extract structured information from assistant messages
				if content.Role == "model" {
					a.extractSnapshotFromText(snapshot, text)
				} else {
					// User messages often contain requirements
					a.extractRequirementsFromText(snapshot, text)
				}

				// Truncate long messages for string output
				if len(text) > 500 {
					text = text[:500] + "..."
				}
				sb.WriteString(fmt.Sprintf("**%s**: %s\n\n", role, text))
				messageCount++
				break
			}
		}

		// Also check for function calls (tool results) to extract key files
		for _, part := range content.Parts {
			if part != nil && part.FunctionResponse != nil {
				a.extractKeyFilesFromToolResult(snapshot, part.FunctionResponse)
			}
		}
	}

	// Save structured snapshot to SharedMemory
	if a.agentRunner != nil {
		if sharedMem := a.agentRunner.GetSharedMemory(); sharedMem != nil {
			sharedMem.SaveContextSnapshot(snapshot, "planning_phase")
			logging.Debug("structured context snapshot saved to shared memory",
				"key_files", len(snapshot.KeyFiles),
				"discoveries", len(snapshot.Discoveries),
				"requirements", len(snapshot.Requirements),
				"decisions", len(snapshot.Decisions))
		}
	}

	return sb.String()
}

// extractSnapshotFromText extracts structured information from assistant text.
func (a *App) extractSnapshotFromText(snapshot *agent.ContextSnapshot, text string) {
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)

		// Look for decisions (architectural patterns)
		if strings.Contains(lower, "decided") || strings.Contains(lower, "will use") ||
			strings.Contains(lower, "approach:") || strings.Contains(lower, "решено") ||
			strings.Contains(lower, "использ") {
			if len(line) > 20 && len(line) < 300 {
				snapshot.AddDecision(line)
			}
		}

		// Look for discoveries
		if strings.Contains(lower, "found") || strings.Contains(lower, "discovered") ||
			strings.Contains(lower, "noticed") || strings.Contains(lower, "обнаруж") ||
			strings.Contains(lower, "нашёл") || strings.Contains(lower, "нашел") {
			if len(line) > 20 && len(line) < 300 {
				snapshot.AddDiscovery(line)
			}
		}

		// Look for error patterns
		if strings.Contains(lower, "error:") || strings.Contains(lower, "failed:") ||
			strings.Contains(lower, "ошибка:") {
			if len(line) > 10 && len(line) < 200 {
				// Try to extract error pattern and add with empty solution for now
				snapshot.ErrorPatterns[line] = ""
			}
		}
	}
}

// extractRequirementsFromText extracts requirements from user text.
func (a *App) extractRequirementsFromText(snapshot *agent.ContextSnapshot, text string) {
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)

		// Look for requirements/constraints
		if strings.Contains(lower, "must") || strings.Contains(lower, "should") ||
			strings.Contains(lower, "need") || strings.Contains(lower, "require") ||
			strings.Contains(lower, "должен") || strings.Contains(lower, "нужно") ||
			strings.Contains(lower, "требован") {
			if len(line) > 15 && len(line) < 300 {
				snapshot.AddRequirement(line)
			}
		}
	}
}

// extractKeyFilesFromToolResult extracts key files from tool results.
func (a *App) extractKeyFilesFromToolResult(snapshot *agent.ContextSnapshot, fr *genai.FunctionResponse) {
	if fr == nil || fr.Name != "read" {
		return
	}

	// fr.Response is map[string]any - try to extract file path
	if fr.Response != nil {
		if path, ok := fr.Response["file_path"].(string); ok {
			// Add file with a placeholder summary (will be enriched later)
			if _, exists := snapshot.KeyFiles[path]; !exists {
				snapshot.KeyFiles[path] = "read during planning"
			}
		}
	}
}
