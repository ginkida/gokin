package app

import (
	"context"
	"fmt"
	"strings"
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

// processMessageWithContext handles user messages with full context management.
func (a *App) processMessageWithContext(ctx context.Context, message string) {
	// Outer timeout to prevent indefinite hangs if API becomes unresponsive
	timeout := 10 * time.Minute
	if a.config.Plan.PlanningTimeout > 0 {
		timeout = a.config.Plan.PlanningTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	defer func() {
		a.mu.Lock()
		a.processing = false
		a.mu.Unlock()

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
	}()

	// Track response start time and reset tools used
	a.mu.Lock()
	a.responseStartTime = time.Now()
	a.responseToolsUsed = nil
	a.streamedChars = 0 // Reset streaming accumulator
	a.messageCount++
	currentMsgCount := a.messageCount
	a.mu.Unlock()

	// === Task 5.8: Inject tool hints every 10 messages ===
	if currentMsgCount > 0 && currentMsgCount%10 == 0 && a.promptBuilder != nil {
		hints := a.getToolHints()
		a.promptBuilder.SetToolHints(hints)
		if hints != "" {
			logging.Debug("tool hints injected", "message_count", currentMsgCount, "hints_length", len(hints))
		}
	}

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

	// === IMPROVEMENT 1: Use Task Router for intelligent routing ===
	var newHistory []*genai.Content
	var response string
	var err error

	if a.taskRouter != nil {
		// Route the task intelligently
		newHistory, response, err = a.taskRouter.Execute(ctx, history, message)

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
		newHistory, response, err = a.executor.Execute(ctx, history, message)
	}

	if err != nil {
		a.safeSendToProgram(ui.ErrorMsg(err))
		return
	}

	// Update session history
	a.session.SetHistory(newHistory)

	// Check for context-clear request after plan approval
	if a.planManager != nil && a.planManager.IsContextClearRequested() {
		approvedPlan := a.planManager.ConsumeContextClearRequest()
		if approvedPlan != nil && a.config.Plan.ClearContext {
			a.executePlanWithClearContext(ctx, approvedPlan)
			return
		}
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
		program.Send(ui.ResponseMetadataMsg{
			Model:        a.config.Model.Name,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			Duration:     duration,
			ToolsUsed:    toolsUsed,
		})
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
	// Enter execution mode - this blocks creation of new plans during execution
	if a.planManager != nil {
		a.planManager.SetExecutionMode(true)
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

	if a.config.Plan.DelegateSteps && a.agentRunner != nil {
		a.executePlanDelegated(ctx, approvedPlan)
	} else {
		a.executePlanDirectly(ctx, approvedPlan)
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

	// 6. Execute each step individually
	const maxRetries = 3
	backoffDurations := []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second}

	for _, step := range approvedPlan.Steps {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Skip non-pending steps (supports plan resume)
		if step.Status != plan.StatusPending {
			continue
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

		// Update plan progress in status bar
		a.safeSendToProgram(ui.PlanProgressMsg{
			PlanID:        approvedPlan.ID,
			CurrentStepID: step.ID,
			CurrentTitle:  step.Title,
			TotalSteps:    totalSteps,
			Completed:     approvedPlan.CompletedCount(),
			Progress:      approvedPlan.Progress(),
			Status:        "in_progress",
		})

		header := fmt.Sprintf("──── Step %d/%d: %s ────\n", step.ID, totalSteps, step.Title)
		a.safeSendToProgram(ui.StreamTextMsg(header))

		// Build step-specific prompt with context from previous steps
		prevSummary := a.planManager.GetPreviousStepsSummary(step.ID, 2000)
		stepMsg := buildDirectStepMessage(step, prevSummary, approvedPlan, totalSteps)

		// Execute step with retry logic
		var response string
		var err error

		for attempt := 0; attempt < maxRetries; attempt++ {
			history := a.session.GetHistory()
			var newHistory []*genai.Content
			newHistory, response, err = a.executor.Execute(ctx, history, stepMsg)

			if err == nil {
				// Success — update session history
				a.session.SetHistory(newHistory)
				break
			}

			// Retry on retryable errors
			if isRetryableError(err) && attempt < maxRetries-1 {
				backoff := backoffDurations[attempt]
				logging.Warn("step execution error, retrying",
					"step_id", step.ID, "attempt", attempt+1, "error", err.Error(), "backoff", backoff)
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
					break
				}
			}
			break
		}

		// Handle step failure
		if err != nil {
			errMsg := err.Error()

			// Retryable error after all attempts → pause for later resume
			if isRetryableError(err) {
				a.planManager.PauseStep(step.ID, errMsg)

				a.safeSendToProgram(ui.StreamTextMsg(
					fmt.Sprintf("\n⏸ Step %d paused after %d attempts: %s\n"+
						"Use /resume-plan to continue when ready.\n",
						step.ID, maxRetries, errMsg)))
				a.safeSendToProgram(ui.PlanProgressMsg{
					PlanID:        approvedPlan.ID,
					CurrentStepID: step.ID,
					CurrentTitle:  step.Title,
					TotalSteps:    totalSteps,
					Completed:     approvedPlan.CompletedCount(),
					Progress:      approvedPlan.Progress(),
					Status:        "paused",
				})
				a.safeSendToProgram(ui.ResponseDoneMsg{})

				logging.Info("plan paused due to retryable error",
					"step_id", step.ID, "error", errMsg)
				return
			}

			// Non-retryable error
			a.planManager.FailStep(step.ID, errMsg)

			a.safeSendToProgram(ui.StreamTextMsg(
				fmt.Sprintf("\n  Step %d failed: %s\n", step.ID, errMsg)))

			if a.config.Plan.AbortOnStepFailure {
				a.safeSendToProgram(ui.StreamTextMsg("Aborting plan due to step failure.\n"))
				break
			}
			continue
		}

		// Step succeeded — store output and mark complete
		output := response
		if len(output) > 2000 {
			output = output[:2000] + "..."
		}
		a.planManager.CompleteStep(step.ID, output)

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
			Status:        "in_progress",
		})

		// Update token count after each step
		if a.contextManager != nil {
			if err := a.contextManager.UpdateTokenCount(ctx); err != nil {
				logging.Debug("failed to update token count", "error", err)
			}
			a.sendTokenUsageUpdate()
		}

		// Accumulate token usage for /cost command
		apiInput, apiOutput := a.executor.GetLastTokenUsage()
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
	}

	// 7. Auto-complete plan
	completedCount := approvedPlan.CompletedCount()
	planFinished := completedCount == totalSteps
	statusText := "complete"
	if !planFinished {
		statusText = "stopped"
	}

	a.safeSendToProgram(ui.StreamTextMsg(
		fmt.Sprintf("\n━━━ Plan %s: %d/%d steps done ━━━\n", statusText, completedCount, totalSteps)))
	a.safeSendToProgram(ui.PlanProgressMsg{
		PlanID:     approvedPlan.ID,
		TotalSteps: totalSteps,
		Completed:  completedCount,
		Progress:   approvedPlan.Progress(),
		Status:     statusText,
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
		Duration:     time.Since(planStart),
	})

	a.safeSendToProgram(ui.ResponseDoneMsg{})

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

// buildDirectStepMessage creates a focused prompt for executing a single step
// in the direct (same-session) execution mode.
func buildDirectStepMessage(step *plan.Step, prevSummary string, _ *plan.Plan, totalSteps int) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Execute step %d of %d: **%s**\n\n", step.ID, totalSteps, step.Title))

	if step.Description != "" {
		sb.WriteString(step.Description)
		sb.WriteString("\n\n")
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

	// Build compact project context for sub-agents
	projectCtx := a.promptBuilder.BuildSubAgentPrompt()

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

	for _, step := range approvedPlan.Steps {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Skip non-pending steps
		if step.Status != plan.StatusPending {
			continue
		}

		// Mark step as started and track current step ID
		a.planManager.StartStep(step.ID)
		a.planManager.SetCurrentStepID(step.ID)

		// Update plan progress in status bar
		a.safeSendToProgram(ui.PlanProgressMsg{
			PlanID:        approvedPlan.ID,
			CurrentStepID: step.ID,
			CurrentTitle:  step.Title,
			TotalSteps:    totalSteps,
			Completed:     approvedPlan.CompletedCount(),
			Progress:      approvedPlan.Progress(),
			Status:        "in_progress",
		})

		// Notify UI of step start with structured header
		header := fmt.Sprintf("──── Step %d/%d: %s ────\n", step.ID, totalSteps, step.Title)
		a.safeSendToProgram(ui.StreamTextMsg(header))

		// Build step prompt with full plan context
		prevSummary := a.planManager.GetPreviousStepsSummary(step.ID, 2000)

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

		// Spawn sub-agent for this step with retry on retryable errors
		var result *agent.AgentResult
		var err error
		const maxRetries = 3
		backoffDurations := []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second}

		for attempt := 0; attempt < maxRetries; attempt++ {
			_, result, err = a.agentRunner.SpawnWithContext(
				ctx, "general", stepPrompt, 30, "", projectCtx, onText, true)

			// Retry on retryable errors (timeout, network, rate limit, etc.)
			if err != nil && isRetryableError(err) && attempt < maxRetries-1 {
				backoff := backoffDurations[attempt]
				logging.Warn("sub-agent error, retrying step",
					"step_id", step.ID, "attempt", attempt+1, "error", err.Error(), "backoff", backoff)
				a.safeSendToProgram(ui.StreamTextMsg(
					fmt.Sprintf("\n⚠️ Step %d failed (attempt %d/%d): %s\nRetrying in %v...\n",
						step.ID, attempt+1, maxRetries, err.Error(), backoff)))

				// Wait with backoff, but respect context cancellation
				backoffTimer := time.NewTimer(backoff)
				select {
				case <-backoffTimer.C:
					continue
				case <-ctx.Done():
					backoffTimer.Stop()
					err = ctx.Err()
					break
				}
			}
			break
		}

		if err != nil || result == nil || result.Status == agent.AgentStatusFailed {
			errMsg := "unknown error"
			if err != nil {
				errMsg = err.Error()
			} else if result != nil {
				errMsg = result.Error
			}

			// Check if this is a retryable error after all retries exhausted
			if err != nil && isRetryableError(err) {
				// Pause the step instead of failing — user can resume later
				a.planManager.PauseStep(step.ID, errMsg)

				a.safeSendToProgram(ui.StreamTextMsg(
					fmt.Sprintf("\n⏸ Step %d paused after %d attempts: %s\n"+
						"Use /resume-plan to continue when ready.\n",
						step.ID, maxRetries, errMsg)))
				a.safeSendToProgram(ui.PlanProgressMsg{
					PlanID:        approvedPlan.ID,
					CurrentStepID: step.ID,
					CurrentTitle:  step.Title,
					TotalSteps:    totalSteps,
					Completed:     approvedPlan.CompletedCount(),
					Progress:      approvedPlan.Progress(),
					Status:        "paused",
				})
				a.safeSendToProgram(ui.ResponseDoneMsg{})

				logging.Info("plan paused due to retryable error",
					"step_id", step.ID, "error", errMsg)
				return // Exit but don't mark as failed — can be resumed
			}

			// Non-retryable error: preserve partial output if available
			if result != nil && result.Output != "" {
				a.planManager.CompleteStep(step.ID, "(partial) "+result.Output)
				logging.Debug("step failed but partial output preserved",
					"step_id", step.ID, "output_len", len(result.Output))
			} else {
				a.planManager.FailStep(step.ID, errMsg)
			}

			a.safeSendToProgram(ui.StreamTextMsg(
				fmt.Sprintf("\n  Step %d failed: %s\n", step.ID, errMsg)))

			if a.config.Plan.AbortOnStepFailure {
				a.safeSendToProgram(ui.StreamTextMsg("Aborting plan due to step failure.\n"))
				break
			}
			continue
		}

		// Store compact output in step and mark complete
		output := result.Output
		if len(output) > 2000 {
			output = output[:2000] + "..."
		}
		a.planManager.CompleteStep(step.ID, output)

		// Store step result in SharedMemory for inter-step communication
		if sharedMem != nil {
			// Store the step output as a fact for other steps to reference
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
			Status:        "in_progress",
		})

		// Save session after each completed step (crash recovery)
		if a.sessionManager != nil {
			if err := a.sessionManager.SaveAfterMessage(); err != nil {
			logging.Warn("failed to save session after message", "error", err)
		}
		}
	}

	// Signal plan completion
	completedCount := approvedPlan.CompletedCount()
	planFinished := completedCount == totalSteps
	statusText := "complete"
	if !planFinished {
		statusText = "stopped"
	}

	a.safeSendToProgram(ui.StreamTextMsg(
		fmt.Sprintf("\n━━━ Plan %s: %d/%d steps done ━━━\n", statusText, completedCount, totalSteps)))
	a.safeSendToProgram(ui.PlanProgressMsg{
		PlanID:     approvedPlan.ID,
		TotalSteps: totalSteps,
		Completed:  completedCount,
		Progress:   approvedPlan.Progress(),
		Status:     statusText,
	})

	// Send response metadata so UI shows duration
	a.safeSendToProgram(ui.ResponseMetadataMsg{
		Model:    a.config.Model.Name,
		Duration: time.Since(planStart),
	})

	a.safeSendToProgram(ui.ResponseDoneMsg{})

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

// isRetryableError checks if an error is retryable (network, timeout, rate limit).
func isRetryableError(err error) bool {
	return client.IsRetryableError(err)
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
