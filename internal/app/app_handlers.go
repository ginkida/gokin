package app

import (
	"context"
	"fmt"
	"strings"
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
		toolName := "operation"
		if req != nil && strings.TrimSpace(req.ToolName) != "" {
			toolName = req.ToolName
		}
		return permission.DecisionDeny, fmt.Errorf("permission required for %s, but interactive approval is unavailable in headless mode; configure permission.rules.%s: allow or explicitly disable permissions for intentional unattended execution", toolName, toolName)
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
			a.notifyPromptExpired(ui.PromptKindPermission, reqID, "Permission request cancelled — no action was run")
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
			a.notifyPromptExpired(ui.PromptKindPermission, reqID, "Permission request expired — no action was run")
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

func (a *App) registerQuestionRequest() (id string, ch chan string, cleanup func()) {
	id = fmt.Sprintf("question-%d", a.questionReqSeq.Add(1))
	ch = make(chan string, 1)
	a.questionPendingMu.Lock()
	if a.questionPending == nil {
		a.questionPending = make(map[string]chan string)
	}
	a.questionPending[id] = ch
	a.questionPendingMu.Unlock()
	cleanup = func() {
		a.questionPendingMu.Lock()
		delete(a.questionPending, id)
		a.questionPendingMu.Unlock()
	}
	return id, ch, cleanup
}

// handleQuestionAnswer routes a UI response only to the waiter that owns its
// request ID. Unknown IDs are expired/duplicate responses and are harmless.
func (a *App) handleQuestionAnswer(reqID, answer string) {
	a.questionPendingMu.Lock()
	ch, ok := a.questionPending[reqID]
	a.questionPendingMu.Unlock()
	if !ok {
		logging.Warn("question answer for unknown or expired request", "request_id", reqID)
		return
	}
	select {
	case ch <- answer:
	default:
		logging.Warn("duplicate question answer ignored", "request_id", reqID)
	}
}

func (a *App) notifyPromptExpired(kind ui.PromptKind, reqID, message string) {
	a.safeSendToProgram(ui.PromptExpiredMsg{Kind: kind, ID: reqID, Message: message})
}

// promptQuestion is called by the AskUserTool to ask the user a question.
func (a *App) promptQuestion(ctx context.Context, question string, options []string, defaultOpt string) (string, error) {
	if a.program == nil {
		return "", fmt.Errorf("program not running")
	}
	reqID, responseCh, cleanup := a.registerQuestionRequest()
	defer cleanup()

	// Send question request to TUI
	a.safeSendToProgram(ui.QuestionRequestMsg{
		ID:       reqID,
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
		case answer := <-responseCh:
			return answer, nil
		case <-ctx.Done():
			a.notifyPromptExpired(ui.PromptKindQuestion, reqID, "Question cancelled — no answer was sent")
			return "", ctx.Err()
		case <-warningTimer.C:
			a.safeSendToProgram(ui.StatusUpdateMsg{
				Type:    ui.StatusStreamIdle,
				Message: "Waiting for your answer to the question above...",
			})
			warningTimer.Reset(repeatDelay)
		case <-questionTimerC:
			logging.Warn("question prompt timed out", "after", timeout)
			a.notifyPromptExpired(ui.PromptKindQuestion, reqID, "Question expired — no answer was sent")
			return "", fmt.Errorf("no answer to the question within %v — you may have stepped away. I did not continue; re-send the task or answer when you're back (or set permission.question_timeout_seconds: -1 to wait indefinitely)", timeout)
		}
	}
}

// PlanApprovalTimeout is the maximum time to wait for a plan approval response.
const PlanApprovalTimeout = 10 * time.Minute

type planApprovalResponse struct {
	Decision ui.PlanApprovalDecision
	Feedback string
}

func (a *App) registerPlanApprovalRequest() (id string, ch chan planApprovalResponse, cleanup func()) {
	id = fmt.Sprintf("plan-%d", a.planReqSeq.Add(1))
	ch = make(chan planApprovalResponse, 1)
	a.planPendingMu.Lock()
	if a.planPending == nil {
		a.planPending = make(map[string]chan planApprovalResponse)
	}
	a.planPending[id] = ch
	a.planPendingMu.Unlock()
	cleanup = func() {
		a.planPendingMu.Lock()
		delete(a.planPending, id)
		a.planPendingMu.Unlock()
	}
	return id, ch, cleanup
}

func (a *App) handlePlanApproval(reqID string, decision ui.PlanApprovalDecision) {
	a.handlePlanApprovalWithFeedback(reqID, decision, "")
}

// handlePlanApprovalWithFeedback only routes intent. State-changing effects
// happen in the correlated waiter against that waiter's original *plan.Plan.
func (a *App) handlePlanApprovalWithFeedback(reqID string, decision ui.PlanApprovalDecision, feedback string) {
	a.planPendingMu.Lock()
	ch, ok := a.planPending[reqID]
	a.planPendingMu.Unlock()
	if !ok {
		logging.Warn("plan response for unknown or expired request", "request_id", reqID)
		return
	}
	select {
	case ch <- planApprovalResponse{Decision: decision, Feedback: feedback}:
	default:
		logging.Warn("duplicate plan response ignored", "request_id", reqID)
	}
}

func (a *App) applyPlanApprovalResponse(p *plan.Plan, response planApprovalResponse) plan.ApprovalDecision {
	switch response.Decision {
	case ui.PlanApproved:
		// The manager commits this decision to the exact current plan before its
		// approval-commit callback lifts plan mode. Keeping this handler pure
		// prevents a stale response for plan A from changing plan B's app state.
		return plan.ApprovalApproved
	case ui.PlanRejected:
		// RequestApproval promotes rejection state only after revalidating that
		// p is still current. The UI handler must stay side-effect free.
		return plan.ApprovalRejected
	case ui.PlanModifyRequested:
		if feedback := strings.TrimSpace(response.Feedback); feedback != "" {
			if a.planManager != nil {
				a.planManager.StageApprovalFeedback(p, feedback)
			}
		}
		return plan.ApprovalModified
	default:
		return plan.ApprovalRejected
	}
}

// promptPlanApproval is called by the plan manager to request user approval.
func (a *App) promptPlanApproval(ctx context.Context, p *plan.Plan) (plan.ApprovalDecision, error) {
	if a.program == nil {
		return plan.ApprovalRejected, fmt.Errorf("plan approval is required, but interactive approval is unavailable in headless mode; disable required plan approval explicitly for intentional unattended execution")
	}
	reqID, responseCh, cleanup := a.registerPlanApprovalRequest()
	defer cleanup()

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
		ID:          reqID,
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
	case response := <-responseCh:
		return a.applyPlanApprovalResponse(p, response), nil
	case <-ctx.Done():
		a.notifyPromptExpired(ui.PromptKindPlan, reqID, "Plan approval cancelled — no decision was applied")
		return plan.ApprovalRejected, ctx.Err()
	case <-planTimer.C:
		logging.Warn("plan approval prompt timed out")
		a.notifyPromptExpired(ui.PromptKindPlan, reqID, "Plan approval expired — no decision was applied")
		return plan.ApprovalRejected, fmt.Errorf("plan approval prompt timed out after %v", PlanApprovalTimeout)
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

// handleModelSelect is called by the TUI when the user selects a model. Client
// reconstruction may perform network/provider setup, so it must not block the
// Bubble Tea event loop that owns rendering and keyboard input.
func (a *App) handleModelSelect(requestID, modelID string) {
	a.safeGo("model-selector-apply", func() {
		result := a.applyModelSelection(modelID)
		result.RequestID = requestID
		a.safeSendToProgram(result)
	})
}

// applyModelSelection transactionally switches models and returns the
// authoritative active model. The selector stays on the old model until this
// result arrives, so a failed client rebuild can never be presented as success.
func (a *App) applyModelSelection(modelID string) ui.ModelSelectResultMsg {
	modelID = strings.TrimSpace(modelID)
	result := ui.ModelSelectResultMsg{RequestedID: modelID}
	if modelID == "" {
		result.Message = "Couldn't switch model: model ID is empty"
		return result
	}

	// Build a candidate without mutating the live config before ApplyConfig has
	// acquired its lock. Preserve the slice backing store too so rollback cannot
	// alias a later model mutation.
	previous := a.GetConfig()
	if previous == nil {
		result.Message = "Couldn't switch model: configuration is unavailable"
		return result
	}

	candidate := previous.Clone()
	candidate.Model.Name = modelID
	candidate.Model.Provider = config.DetectProvider(modelID)
	// Clear any stale model.preset so the explicit selection isn't reverted by
	// MigrateConfig/NormalizeConfig on the ApplyConfig below.
	candidate.Model.Preset = ""

	// Apply MaxOutputTokens from preset for new provider
	if preset, ok := config.ModelPresets[candidate.Model.Provider]; ok {
		candidate.Model.MaxOutputTokens = preset.MaxOutputTokens
	}

	revision, err := a.applyConfig(candidate)
	if err != nil {
		logging.Warn("failed to apply model selection", "error", err)
		result.ModelID = previous.Model.Name
		if current := a.GetConfig(); current != nil {
			result.ModelID = current.Model.Name
		}
		result.Message = fmt.Sprintf("Couldn't switch to %s: %v · current model kept", modelID, err)
		return result
	}

	result.Revision = revision
	result.Success = true
	result.ModelID = modelID
	result.Message = "Switched to " + modelID
	if candidate.Model.Provider == "kimi" && previous.Model.Name != modelID {
		result.Message += " · Kimi model switch invalidates the prompt cache; use /clear for a fresh, lower-cost session"
	}
	return result
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

func (a *App) registerDiffRequest() (id string, ch chan ui.DiffDecision, cleanup func()) {
	id = fmt.Sprintf("diff-%d", a.diffReqSeq.Add(1))
	ch = make(chan ui.DiffDecision, 1)
	a.diffPendingMu.Lock()
	if a.diffPending == nil {
		a.diffPending = make(map[string]chan ui.DiffDecision)
	}
	a.diffPending[id] = ch
	a.diffPendingMu.Unlock()
	cleanup = func() {
		a.diffPendingMu.Lock()
		delete(a.diffPending, id)
		a.diffPendingMu.Unlock()
	}
	return id, ch, cleanup
}

func (a *App) registerMultiDiffRequest() (id string, ch chan map[string]ui.DiffDecision, cleanup func()) {
	id = fmt.Sprintf("multi-diff-%d", a.multiDiffReqSeq.Add(1))
	ch = make(chan map[string]ui.DiffDecision, 1)
	a.multiDiffPendingMu.Lock()
	if a.multiDiffPending == nil {
		a.multiDiffPending = make(map[string]chan map[string]ui.DiffDecision)
	}
	a.multiDiffPending[id] = ch
	a.multiDiffPendingMu.Unlock()
	cleanup = func() {
		a.multiDiffPendingMu.Lock()
		delete(a.multiDiffPending, id)
		a.multiDiffPendingMu.Unlock()
	}
	return id, ch, cleanup
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

	reqID, responseCh, cleanup := a.registerDiffRequest()
	defer cleanup()
	drainDiffDecisionChan(a.diffResponseChan)

	// Send diff preview request to TUI
	a.safeSendToProgram(ui.DiffPreviewRequestMsg{
		RequestID:  reqID,
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
	case decision := <-responseCh:
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
		a.notifyPromptExpired(ui.PromptKindDiff, reqID, "Diff review cancelled — no changes were applied")
		return ui.DiffReject, ctx.Err()
	case <-diffTimer.C:
		logging.Warn("diff decision prompt timed out", "file", filePath)
		a.notifyPromptExpired(ui.PromptKindDiff, reqID, "Diff review expired — no changes were applied")
		return ui.DiffReject, fmt.Errorf("diff decision prompt timed out after %v", DiffDecisionTimeout)
	}
}

// handleDiffDecision is called by the TUI when the user makes a diff preview decision.
func (a *App) handleDiffDecision(reqID string, decision ui.DiffDecision) {
	a.diffPendingMu.Lock()
	ch, ok := a.diffPending[reqID]
	a.diffPendingMu.Unlock()
	if !ok {
		logging.Warn("diff response for unknown or expired request", "request_id", reqID)
		return
	}
	select {
	case ch <- decision:
	default:
		logging.Warn("duplicate diff response ignored", "request_id", reqID)
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

	reqID, responseCh, cleanup := a.registerMultiDiffRequest()
	defer cleanup()
	a.safeSendToProgram(ui.MultiDiffPreviewRequestMsg{RequestID: reqID, Files: files})

	diffTimer := time.NewTimer(DiffDecisionTimeout)
	defer diffTimer.Stop()

	select {
	case decisions := <-responseCh:
		return decisions, nil
	case <-ctx.Done():
		a.notifyPromptExpired(ui.PromptKindMultiDiff, reqID, "Multi-file review cancelled — no changes were applied")
		return nil, ctx.Err()
	case <-diffTimer.C:
		logging.Warn("multi diff decision prompt timed out", "files", len(files))
		a.notifyPromptExpired(ui.PromptKindMultiDiff, reqID, "Multi-file review expired — no changes were applied")
		return nil, fmt.Errorf("multi diff decision prompt timed out after %v", DiffDecisionTimeout)
	}
}

func (a *App) handleMultiDiffDecision(reqID string, decisions map[string]ui.DiffDecision) {
	a.multiDiffPendingMu.Lock()
	ch, ok := a.multiDiffPending[reqID]
	a.multiDiffPendingMu.Unlock()
	if !ok {
		logging.Warn("multi-diff response for unknown or expired request", "request_id", reqID)
		return
	}
	response := make(map[string]ui.DiffDecision, len(decisions))
	for file, decision := range decisions {
		response[file] = decision
	}
	select {
	case ch <- response:
	default:
		logging.Warn("duplicate multi-diff response ignored", "request_id", reqID)
	}
}

func (a *App) reviewWorkspaceChanges(ctx context.Context, changes []agent.WorkspaceChangePreview) ([]string, error) {
	if len(changes) == 0 {
		return nil, nil
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
			return nil, err
		}
		return approvedWorkspaceFiles(changes, decisions), nil
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
			return nil, err
		}
		if decision == ui.DiffReject || decision == ui.DiffRejectAll {
			return nil, nil
		}
	}

	return []string{changes[0].FilePath}, nil
}

func approvedWorkspaceFiles(changes []agent.WorkspaceChangePreview, decisions map[string]ui.DiffDecision) []string {
	approved := make([]string, 0, len(changes))
	for _, change := range changes {
		if decision := decisions[change.FilePath]; decision == ui.DiffApply || decision == ui.DiffApplyAll {
			approved = append(approved, change.FilePath)
		}
	}
	return approved
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
