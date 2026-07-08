package app

import (
	"context"
	"fmt"
	"time"

	"gokin/internal/agent"
	"gokin/internal/config"
	"gokin/internal/logging"
	"gokin/internal/permission"
	"gokin/internal/plan"
	"gokin/internal/ui"
)

// DefaultPermissionTimeout is the fallback wait for a permission response when
// config.Permission.PromptTimeoutSeconds is unset (0). The effective timeout is
// resolved per-prompt by permPromptTimeout (configurable; <0 = no timeout).
const DefaultPermissionTimeout = 5 * time.Minute

// permPromptTimeout resolves the interactive permission-prompt wait from config:
// >0 → that many seconds; 0 → DefaultPermissionTimeout (also covers configs
// predating the field); <0 → 0 here, meaning "no timeout" (wait indefinitely
// with periodic reminders). Snapshotted under a.mu — a.config is swapped by
// ApplyConfig under the same lock.
func (a *App) permPromptTimeout() time.Duration {
	secs := 0
	a.mu.Lock()
	if a.config != nil {
		secs = a.config.Permission.PromptTimeoutSeconds
	}
	a.mu.Unlock()
	switch {
	case secs < 0:
		return 0 // indefinite
	case secs == 0:
		return DefaultPermissionTimeout
	default:
		return time.Duration(secs) * time.Second
	}
}

// registerPermRequest allocates a unique ID and a dedicated response channel
// for one in-flight promptPermission call, and returns a cleanup func that
// unregisters it. Keying by ID (instead of the old single shared channel) is
// what lets handlePermissionDecision correlate a UI decision — including the
// auto-deny fired for a SECOND concurrent request while a modal is already
// showing the first — to the exact request it answers, never a different
// one racing on the same channel.
func (a *App) registerPermRequest() (id string, ch chan permission.Decision, cleanup func()) {
	id = fmt.Sprintf("perm-%d", a.permReqSeq.Add(1))
	ch = make(chan permission.Decision, 1)

	a.permPendingMu.Lock()
	a.permPending[id] = ch
	a.permPendingMu.Unlock()

	cleanup = func() {
		a.permPendingMu.Lock()
		delete(a.permPending, id)
		a.permPendingMu.Unlock()
	}
	return id, ch, cleanup
}

// promptPermission is called by the permission manager to ask the user for permission.
// It sends a request to the TUI and waits for a response with timeout.
func (a *App) promptPermission(ctx context.Context, req *permission.Request) (permission.Decision, error) {
	if a.program == nil {
		return permission.DecisionAllow, nil
	}

	reqID, respCh, cleanup := a.registerPermRequest()
	defer cleanup()

	// Send permission request to TUI
	a.safeSendToProgram(ui.PermissionRequestMsg{
		ID:        reqID,
		ToolName:  req.ToolName,
		Args:      req.Args,
		RiskLevel: req.RiskLevel.String(),
		Reason:    req.Reason,
	})

	// Warning timer - remind user after 30 seconds, then every 60 seconds
	const warningDelay = 30 * time.Second
	const repeatDelay = 60 * time.Second
	warningTimer := time.NewTimer(warningDelay)
	defer warningTimer.Stop()

	// Resolve the configurable timeout. A zero duration means "no timeout" —
	// leave permTimerC nil so the select branch never fires and we wait
	// indefinitely (the reminders keep nudging; Esc/ctx-cancel still aborts).
	timeout := a.permPromptTimeout()
	var permTimerC <-chan time.Time
	if timeout > 0 {
		permTimer := time.NewTimer(timeout)
		defer permTimer.Stop()
		permTimerC = permTimer.C
	}

	// Wait for response from TUI with timeout and periodic warnings
	for {
		select {
		case decision := <-respCh:
			return decision, nil
		case <-ctx.Done():
			return permission.DecisionDeny, ctx.Err()
		case <-warningTimer.C:
			// Send warning to UI
			a.safeSendToProgram(ui.StatusUpdateMsg{
				Type:    ui.StatusStreamIdle,
				Message: fmt.Sprintf("Waiting for permission: %s...", req.ToolName),
			})
			// Reset timer for next reminder
			warningTimer.Reset(repeatDelay)
		case <-permTimerC:
			// Graceful, AFK-aware message: distinguish "you stepped away" from a
			// deliberate refusal so the agent can report/resume rather than read
			// it as a hard failure. (Set permission.prompt_timeout_seconds: -1 to
			// disable the timeout entirely.)
			logging.Warn("permission prompt timed out", "tool", req.ToolName, "after", timeout)
			return permission.DecisionDeny, fmt.Errorf("no response to the permission request for %s within %v — you may have stepped away. I did not run it; re-send the task or approve when you're back (or set permission.prompt_timeout_seconds: -1 to wait indefinitely)", req.ToolName, timeout)
		}
	}
}

// handlePermissionDecision is called by the TUI when the user makes a
// permission decision — reqID identifies WHICH promptPermission call this
// answers (the currently-displayed request for a normal user decision, or a
// different, just-arrived request for the "auto-deny while a modal is
// already showing" case). Routing by ID is what prevents a decision meant
// for one concurrent permission prompt from resolving a different one.
func (a *App) handlePermissionDecision(reqID string, decision ui.PermissionDecision) {
	// Convert UI decision to permission.Decision
	var permDecision permission.Decision
	switch decision {
	case ui.PermissionAllow:
		permDecision = permission.DecisionAllow
	case ui.PermissionAllowSession:
		permDecision = permission.DecisionAllowSession
	case ui.PermissionDeny:
		permDecision = permission.DecisionDeny
	case ui.PermissionDenySession:
		permDecision = permission.DecisionDenySession
	default:
		permDecision = permission.DecisionDeny
	}

	a.permPendingMu.Lock()
	ch, ok := a.permPending[reqID]
	a.permPendingMu.Unlock()
	if !ok {
		// The request already timed out / its promptPermission call already
		// returned (cleanup ran) — nothing waiting to deliver to.
		logging.Warn("permission decision for unknown or expired request", "request_id", reqID)
		return
	}

	// Send decision to the waiting promptPermission call with timeout
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	select {
	case ch <- permDecision:
	case <-timer.C:
		logging.Warn("permission response channel timeout - no listener", "request_id", reqID)
	}
}

// QuestionTimeout is the fallback wait for an ask_user question response when
// config.Permission.QuestionTimeoutSeconds is unset (0). The effective timeout
// is resolved per-question by questionPromptTimeout (configurable;
// <0 = no timeout).
const QuestionTimeout = 10 * time.Minute

// questionPromptTimeout resolves the ask_user question wait from config, with
// the SAME semantics as permPromptTimeout: >0 → that many seconds; 0 →
// QuestionTimeout (covers old configs); <0 → 0 meaning "no timeout" (wait
// indefinitely with periodic reminders). Snapshotted under a.mu.
func (a *App) questionPromptTimeout() time.Duration {
	secs := 0
	a.mu.Lock()
	if a.config != nil {
		secs = a.config.Permission.QuestionTimeoutSeconds
	}
	a.mu.Unlock()
	switch {
	case secs < 0:
		return 0 // indefinite
	case secs == 0:
		return QuestionTimeout
	default:
		return time.Duration(secs) * time.Second
	}
}

// promptQuestion is called by the AskUserTool to ask the user a question.
func (a *App) promptQuestion(ctx context.Context, question string, options []string, defaultOpt string) (string, error) {
	if a.program == nil {
		return "", fmt.Errorf("program not running")
	}

	// Send question request to TUI
	a.safeSendToProgram(ui.QuestionRequestMsg{
		Question: question,
		Options:  options,
		Default:  defaultOpt,
	})

	// Reminder timer — nudge the user after 60s, then every 60s, so a question
	// doesn't sit silently (the agent appears hung). Matches the permission
	// prompt's reminder cadence.
	const warningDelay = 60 * time.Second
	const repeatDelay = 60 * time.Second
	warningTimer := time.NewTimer(warningDelay)
	defer warningTimer.Stop()

	// Resolve the configurable timeout. A zero duration means "no timeout" —
	// leave questionTimerC nil so the select branch never fires and we wait
	// indefinitely (the reminders keep nudging; Esc/ctx-cancel still aborts).
	timeout := a.questionPromptTimeout()
	var questionTimerC <-chan time.Time
	if timeout > 0 {
		questionTimer := time.NewTimer(timeout)
		defer questionTimer.Stop()
		questionTimerC = questionTimer.C
	}

	for {
		select {
		case answer := <-a.questionResponseChan:
			return answer, nil
		case <-ctx.Done():
			return "", ctx.Err()
		case <-warningTimer.C:
			a.safeSendToProgram(ui.StatusUpdateMsg{
				Type:    ui.StatusStreamIdle,
				Message: "Waiting for your answer to the question above...",
			})
			warningTimer.Reset(repeatDelay)
		case <-questionTimerC:
			logging.Warn("question prompt timed out", "after", timeout)
			return "", fmt.Errorf("no answer to the question within %v — you may have stepped away. I did not continue; re-send the task or answer when you're back (or set permission.question_timeout_seconds: -1 to wait indefinitely)", timeout)
		}
	}
}

// handleQuestionAnswer is called by the TUI when the user answers a question.
func (a *App) handleQuestionAnswer(answer string) {
	// Send answer to the waiting promptQuestion call with timeout
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	select {
	case a.questionResponseChan <- answer:
	case <-timer.C:
		logging.Warn("question response channel timeout - no listener")
	}
}

// PlanApprovalTimeout is the maximum time to wait for a plan approval response.
const PlanApprovalTimeout = 10 * time.Minute

// promptPlanApproval is called by the plan manager to request user approval.
func (a *App) promptPlanApproval(ctx context.Context, p *plan.Plan) (plan.ApprovalDecision, error) {
	if a.program == nil {
		return plan.ApprovalApproved, nil
	}

	// Convert plan steps to UI format
	steps := make([]ui.PlanStepInfo, len(p.Steps))
	for i, step := range p.Steps {
		steps[i] = ui.PlanStepInfo{
			ID:          step.ID,
			Title:       step.Title,
			Description: step.Description,
		}
	}

	// Build plan approval request
	msg := ui.PlanApprovalRequestMsg{
		Title:       p.Title,
		Description: p.Description,
		Steps:       steps,
	}

	// Send plan approval request to TUI
	a.safeSendToProgram(msg)

	// Wait for response from TUI with timeout to prevent deadlock
	planTimer := time.NewTimer(PlanApprovalTimeout)
	defer planTimer.Stop()

	select {
	case decision := <-a.planApprovalChan:
		return decision, nil
	case <-ctx.Done():
		return plan.ApprovalRejected, ctx.Err()
	case <-planTimer.C:
		logging.Warn("plan approval prompt timed out")
		return plan.ApprovalRejected, fmt.Errorf("plan approval prompt timed out after %v", PlanApprovalTimeout)
	}
}

// handlePlanApproval is called by the TUI when the user makes a plan approval decision.
func (a *App) handlePlanApproval(decision ui.PlanApprovalDecision) {
	a.handlePlanApprovalWithFeedback(decision, "")
}

// handlePlanApprovalWithFeedback is called by the TUI when the user makes a plan approval decision with feedback.
func (a *App) handlePlanApprovalWithFeedback(decision ui.PlanApprovalDecision, feedback string) {
	// Convert UI decision to plan.ApprovalDecision
	var planDecision plan.ApprovalDecision
	switch decision {
	case ui.PlanApproved:
		planDecision = plan.ApprovalApproved
		// Claude Code semantics: approving a plan hands control back to the
		// agent for execution — which means lifting the read-only
		// restriction. If we stayed in plan mode the model would still only
		// see read tools and couldn't carry out the plan it just proposed.
		// User can re-enter plan mode via Shift+Tab or /plan for the next
		// task.
		a.disablePlanModeAfterApproval()
	case ui.PlanRejected:
		planDecision = plan.ApprovalRejected
		// Save the rejected plan for context
		if currentPlan := a.planManager.GetCurrentPlan(); currentPlan != nil {
			a.planManager.SaveRejectedPlan(currentPlan)
		}
	case ui.PlanModifyRequested:
		planDecision = plan.ApprovalModified
		// Save the rejected plan for context
		if currentPlan := a.planManager.GetCurrentPlan(); currentPlan != nil {
			a.planManager.SaveRejectedPlan(currentPlan)
		}
		// Handle feedback - send as a new message to the model
		if feedback != "" {
			a.planManager.SetFeedback(feedback)
			feedbackMsg := fmt.Sprintf("Please modify the plan according to this feedback:\n\n%s", feedback)
			// Process feedback as a new message. safeGo, not raw go: handleSubmit
			// runs the full request pipeline — a panic there on a raw goroutine
			// crashes the whole CLI (same class as the pending-queue dispatch fix).
			a.safeGo("plan-feedback-dispatch", func() { a.handleSubmit(feedbackMsg) })
		}
	default:
		planDecision = plan.ApprovalRejected
	}

	// Send decision to the waiting promptPlanApproval call with timeout
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	select {
	case a.planApprovalChan <- planDecision:
	case <-timer.C:
		logging.Warn("plan approval response channel timeout - no listener")
	}
}

// handlePlanProgressUpdate is called when plan execution progress is made.
func (a *App) handlePlanProgressUpdate(progress *plan.ProgressUpdate) {
	if a.program == nil {
		return
	}

	// Send progress update to TUI
	a.safeSendToProgram(ui.PlanProgressMsg{
		PlanID:        progress.PlanID,
		CurrentStepID: progress.CurrentStepID,
		CurrentTitle:  progress.CurrentTitle,
		TotalSteps:    progress.TotalSteps,
		Completed:     progress.Completed,
		Progress:      progress.Progress,
		Status:        progress.Status,
		Reason:        progress.Reason,
	})
}

// handleModelSelect is called by the TUI when the user selects a model.
func (a *App) handleModelSelect(modelID string) {
	if a.config == nil {
		return
	}
	a.config.Model.Name = modelID
	a.config.Model.Provider = config.DetectProvider(modelID)
	// Clear any stale model.preset so the explicit selection isn't reverted by
	// MigrateConfig/NormalizeConfig on the ApplyConfig below.
	a.config.Model.Preset = ""

	// Apply MaxOutputTokens from preset for new provider
	if preset, ok := config.ModelPresets[a.config.Model.Provider]; ok {
		a.config.Model.MaxOutputTokens = preset.MaxOutputTokens
	}

	if err := a.ApplyConfig(a.config); err != nil {
		logging.Warn("failed to apply model selection", "error", err)
	}
}

// DiffDecisionTimeout is the maximum time to wait for a diff decision response.
const DiffDecisionTimeout = 5 * time.Minute

func drainDiffDecisionChan(ch <-chan ui.DiffDecision) {
	if ch == nil {
		return
	}
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func drainMultiDiffDecisionChan(ch <-chan map[string]ui.DiffDecision) {
	if ch == nil {
		return
	}
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// promptDiffDecision is called by tools to request user approval for file changes.
// It sends a request to the TUI and waits for a response with timeout.
func (a *App) promptDiffDecision(ctx context.Context, filePath, oldContent, newContent, toolName string, isNewFile bool) (ui.DiffDecision, error) {
	a.diffPromptMu.Lock()
	defer a.diffPromptMu.Unlock()

	a.mu.Lock()
	batch := a.diffBatchDecision
	a.mu.Unlock()
	if batch == ui.DiffApplyAll {
		return ui.DiffApply, nil
	}
	if batch == ui.DiffRejectAll {
		return ui.DiffReject, nil
	}

	if a.program == nil {
		return ui.DiffApply, nil
	}

	drainDiffDecisionChan(a.diffResponseChan)

	// Send diff preview request to TUI
	a.safeSendToProgram(ui.DiffPreviewRequestMsg{
		FilePath:   filePath,
		OldContent: oldContent,
		NewContent: newContent,
		ToolName:   toolName,
		IsNewFile:  isNewFile,
	})

	// Wait for response from TUI with timeout to prevent deadlock
	diffTimer := time.NewTimer(DiffDecisionTimeout)
	defer diffTimer.Stop()

	select {
	case decision := <-a.diffResponseChan:
		if decision == ui.DiffApplyAll {
			a.mu.Lock()
			a.diffBatchDecision = ui.DiffApplyAll
			a.mu.Unlock()
			return ui.DiffApply, nil
		}
		if decision == ui.DiffRejectAll {
			a.mu.Lock()
			a.diffBatchDecision = ui.DiffRejectAll
			a.mu.Unlock()
			return ui.DiffReject, nil
		}
		return decision, nil
	case <-ctx.Done():
		return ui.DiffReject, ctx.Err()
	case <-diffTimer.C:
		logging.Warn("diff decision prompt timed out", "file", filePath)
		return ui.DiffReject, fmt.Errorf("diff decision prompt timed out after %v", DiffDecisionTimeout)
	}
}

// handleDiffDecision is called by the TUI when the user makes a diff preview decision.
func (a *App) handleDiffDecision(decision ui.DiffDecision) {
	// Send decision to the waiting promptDiffDecision call with timeout
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	select {
	case a.diffResponseChan <- decision:
	case <-timer.C:
		logging.Warn("diff response channel timeout - no listener")
	}
}

func (a *App) promptMultiDiffDecision(ctx context.Context, files []ui.DiffFile) (map[string]ui.DiffDecision, error) {
	a.diffPromptMu.Lock()
	defer a.diffPromptMu.Unlock()

	if len(files) == 0 {
		return map[string]ui.DiffDecision{}, nil
	}

	drainMultiDiffDecisionChan(a.multiDiffResponseChan)

	if a.program == nil {
		decisions := make(map[string]ui.DiffDecision, len(files))
		for _, file := range files {
			decisions[file.FilePath] = ui.DiffApply
		}
		return decisions, nil
	}

	a.safeSendToProgram(ui.MultiDiffPreviewRequestMsg{Files: files})

	diffTimer := time.NewTimer(DiffDecisionTimeout)
	defer diffTimer.Stop()

	select {
	case decisions := <-a.multiDiffResponseChan:
		return decisions, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-diffTimer.C:
		logging.Warn("multi diff decision prompt timed out", "files", len(files))
		return nil, fmt.Errorf("multi diff decision prompt timed out after %v", DiffDecisionTimeout)
	}
}

func (a *App) handleMultiDiffDecision(decisions map[string]ui.DiffDecision) {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	select {
	case a.multiDiffResponseChan <- decisions:
	case <-timer.C:
		logging.Warn("multi diff response channel timeout - no listener")
	}
}

func (a *App) reviewWorkspaceChanges(ctx context.Context, changes []agent.WorkspaceChangePreview) (bool, error) {
	if len(changes) == 0 {
		return true, nil
	}

	if len(changes) > 1 {
		files := make([]ui.DiffFile, 0, len(changes))
		for _, change := range changes {
			files = append(files, ui.DiffFile{
				FilePath:   change.FilePath,
				OldContent: change.OldContent,
				NewContent: change.NewContent,
				IsNewFile:  change.IsNewFile,
			})
		}

		decisions, err := a.promptMultiDiffDecision(ctx, files)
		if err != nil {
			return false, err
		}
		for _, decision := range decisions {
			if decision != ui.DiffApply {
				return false, nil
			}
		}
		return true, nil
	}

	a.mu.Lock()
	prevBatch := a.diffBatchDecision
	a.diffBatchDecision = ui.DiffPending
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.diffBatchDecision = prevBatch
		a.mu.Unlock()
	}()

	for _, change := range changes {
		decision, err := a.promptDiffDecision(
			ctx,
			change.FilePath,
			change.OldContent,
			change.NewContent,
			"apply_back",
			change.IsNewFile,
		)
		if err != nil {
			return false, err
		}
		if decision == ui.DiffReject || decision == ui.DiffRejectAll {
			return false, nil
		}
	}

	return true, nil
}

// handleApplyCodeBlock handles applying a code block to a file.
func (a *App) handleApplyCodeBlock(filename, content string) {
	if filename == "" || content == "" {
		return
	}

	// Use the write tool to apply the code block
	writeTool, ok := a.registry.Get("write")
	if !ok {
		logging.Debug("write tool not found")
		return
	}

	// Execute the write tool
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Warn("write tool goroutine recovered from panic", "error", r)
			}
		}()
		ctx := a.ctx
		args := map[string]any{
			"file_path": filename,
			"content":   content,
		}

		result, err := writeTool.Execute(ctx, args)
		if err != nil {
			a.safeSendToProgram(ui.ErrorMsg(err))
			return
		}

		if !result.Success {
			a.safeSendToProgram(ui.ErrorMsg(fmt.Errorf("%s", result.Error)))
		}
	}()
}

// diffHandlerAdapter wraps App to implement tools.DiffHandler interface.
type diffHandlerAdapter struct {
	app *App
}

func (d *diffHandlerAdapter) PromptDiff(ctx context.Context, filePath, oldContent, newContent, toolName string, isNewFile bool) (bool, error) {
	decision, err := d.app.promptDiffDecision(ctx, filePath, oldContent, newContent, toolName, isNewFile)
	if err != nil {
		return false, err
	}
	return decision == ui.DiffApply, nil
}
