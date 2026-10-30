package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"gokin/internal/agent"
	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/commands"
	"gokin/internal/goals"
	"gokin/internal/hooks"
	"gokin/internal/lifecycle"
	"gokin/internal/logging"
	"gokin/internal/security"
	"gokin/internal/tools"
)

const briefResultInstruction = "Final answer style: give a single-line summary of the result. Omit verification narration and changed-file lists (the caller receives files_changed separately). Report any material failure or limitation honestly in that line. If a structured output schema is specified, follow it and keep string summaries concise."

// headlessPolicyFailure is the first policy refusal observed during one
// RunHeadless invocation. First-wins keeps the exit error deterministic even
// when a model tries several forbidden alternatives before producing prose.
type headlessPolicyFailure struct {
	ToolName string
	Block    tools.PolicyBlock
}

// headlessTerminalOutcome is an otherwise-recoverable foreground condition
// that must fail a non-interactive invocation closed. Interactive turns do not
// record these outcomes: their UI error plus a usable prompt is the recovery
// contract. Kind is a stable HeadlessError.Kind value.
type headlessTerminalOutcome struct {
	Kind    string
	Message string
}

// headlessTimeoutError keeps the returned Go error machine-checkable while
// preserving the provider/watchdog diagnostic as its visible message.
type headlessTimeoutError struct {
	message string
}

func (e *headlessTimeoutError) Error() string { return e.message }

func (e *headlessTimeoutError) Unwrap() error { return context.DeadlineExceeded }

// headlessMaxTurnsError preserves errors.Is semantics after the app's terminal
// outcome latch has converted the executor error into durable kind/message data.
type headlessMaxTurnsError struct {
	message string
}

func (e *headlessMaxTurnsError) Error() string { return e.message }

func (e *headlessMaxTurnsError) Unwrap() error { return tools.ErrMaxTurnsExceeded }

type headlessBudgetError struct {
	message string
}

func (e *headlessBudgetError) Error() string { return e.message }

func (e *headlessBudgetError) Unwrap() error { return tools.ErrBudgetExceeded }

type headlessCostUnavailableError struct {
	message string
}

func (e *headlessCostUnavailableError) Error() string { return e.message }

func (e *headlessCostUnavailableError) Unwrap() error { return tools.ErrCostUnavailable }

// HeadlessOutputFormat selects the stable stdout contract used by
// RunHeadlessWithOptions. Text preserves the traditional streamed answer;
// JSON emits exactly one versioned result object after the turn completes;
// stream-json emits JSONL progress followed by that same terminal object.
type HeadlessOutputFormat string

const (
	HeadlessOutputText       HeadlessOutputFormat = "text"
	HeadlessOutputJSON       HeadlessOutputFormat = "json"
	HeadlessOutputStreamJSON HeadlessOutputFormat = "stream-json"

	HeadlessSchemaVersion = 1
)

// HeadlessOptions controls presentation and invocation-scoped execution policy.
// Nil writers use the process stdout/stderr. Callers that intentionally want
// to discard a stream should pass io.Discard explicitly.
type HeadlessOptions struct {
	Lifecycle *lifecycle.Trace
	// StrictPermissions makes the first ordinary tool denial terminal.
	StrictPermissions bool
	// PermissionMode labels denial diagnostics; CLI config enforces the mode.
	PermissionMode string
	// Brief requests a one-line final answer and buffers text until completion.
	Brief bool
	// NoSelfReview skips optional reviews/nudges while retaining hard checks.
	NoSelfReview bool
	OutputFormat HeadlessOutputFormat
	Stdout       io.Writer
	Stderr       io.Writer
	// JSONSchema requests a validated final JSON value. It is compiled before
	// execution and may be reused safely across stream-json input records.
	JSONSchema *StructuredOutputSchema
	// StreamState keeps stream-json event sequence numbers monotonic across
	// several RunHeadlessWithOptions calls sharing one JSONL connection.
	StreamState *HeadlessStreamState
	// MaxTurns sets the executor's model/tool-round limit for this invocation
	// only. Zero disables the turn cap, matching headless CLI semantics.
	MaxTurns int
	// Timeout is an overall execution deadline for this invocation. Zero means
	// no extra wall-clock deadline; model/tool/idle safety timeouts still apply.
	Timeout time.Duration
	// MaxBudgetUSD is a hard invocation cost ceiling. Zero disables the ceiling.
	// Positive limits require an explicitly known provider/model tariff.
	MaxBudgetUSD float64
	// InlineExternalSteers keeps accepted final-window steering inside this
	// synchronous invocation. Detached workers enable it so durable inbox
	// messages can be acknowledged without an invisible asynchronous turn.
	InlineExternalSteers bool
}

// HeadlessUsage is the usage delta for this invocation, not a cumulative
// session total. That distinction matters when one App executes several turns.
type HeadlessUsage struct {
	InputTokens          int `json:"input_tokens"`
	OutputTokens         int `json:"output_tokens"`
	CacheReadInputTokens int `json:"cache_read_input_tokens"`
	TotalTokens          int `json:"total_tokens"`
	ModelRounds          int `json:"model_rounds"`
}

// HeadlessCost describes the cost attributable to this invocation.
// Tracked distinguishes a known zero (for example local Ollama) from absent
// provider pricing.
type HeadlessCost struct {
	EstimatedUSD float64 `json:"estimated_usd"`
	Tracked      bool    `json:"tracked"`
}

// HeadlessError is a machine-readable terminal outcome. Policy failures retain
// the denied tool and policy kind so automation does not need to parse prose.
type HeadlessError struct {
	Kind       string `json:"kind"`
	Message    string `json:"message"`
	Tool       string `json:"tool,omitempty"`
	PolicyKind string `json:"policy_kind,omitempty"`
}

// HeadlessResult is the versioned, single-object automation envelope.
type HeadlessResult struct {
	Sandbox           security.SandboxStatus   `json:"sandbox"`
	PermissionDenials []tools.PermissionDenial `json:"permission_denials"`
	FilesChanged      []string                 `json:"files_changed"`
	Goal              *goals.Goal              `json:"goal,omitempty"`
	SchemaVersion     int                      `json:"schema_version"`
	Type              string                   `json:"type"`
	Result            string                   `json:"result"`
	StructuredOutput  *any                     `json:"structured_output,omitempty"`
	SessionID         string                   `json:"session_id"`
	Status            string                   `json:"status"`
	Error             *HeadlessError           `json:"error,omitempty"`
	Usage             HeadlessUsage            `json:"usage"`
	Cost              HeadlessCost             `json:"cost"`
	DurationMS        int64                    `json:"duration_ms"`
	Warnings          []string                 `json:"warnings,omitempty"`

	Verification  tools.VerificationSummary `json:"verification"`
	Incomplete    bool                      `json:"incomplete"`
	ClaimMismatch bool                      `json:"claim_mismatch,omitempty"`
	Timing        lifecycle.Timing          `json:"timing"`

	FilesChangedPartial bool `json:"files_changed_partial,omitempty"`
}

// RunHeadless executes one user prompt through the normal request pipeline
// without starting the Bubble Tea TUI. It is intended for eval harnesses and
// scripts that need a plain shell command.
func (a *App) RunHeadless(ctx context.Context, prompt string) error {
	_, err := a.RunHeadlessWithOptions(ctx, prompt, HeadlessOptions{
		OutputFormat: HeadlessOutputText,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
	})
	return err
}

// RunHeadlessWithOptions executes the same one-turn pipeline as RunHeadless
// while exposing a stable machine-readable result. Execution failures are
// both returned (for a non-zero process exit) and represented in JSON stdout;
// diagnostics and persistence warnings remain on stderr.
func (a *App) RunHeadlessWithOptions(ctx context.Context, prompt string, opts HeadlessOptions) (HeadlessResult, error) {
	started := time.Now()
	trace := opts.Lifecycle
	ownedTrace := trace == nil
	if ownedTrace {
		trace = lifecycle.New(nil)
	}
	trace.StartTurn()
	defer func() {
		trace.EndTurn()
		if ownedTrace {
			trace.Finish()
		}
	}()
	result := HeadlessResult{
		Sandbox:           a.GetBashSandboxStatus(),
		Verification:      tools.VerificationSummary{Status: "not_run"},
		Incomplete:        true,
		PermissionDenials: []tools.PermissionDenial{},
		FilesChanged:      []string{},
		SchemaVersion:     HeadlessSchemaVersion,
		Type:              "result",
		Status:            "error",
	}

	format := opts.OutputFormat
	if format == "" {
		format = HeadlessOutputText
	}
	if format != HeadlessOutputText && format != HeadlessOutputJSON && format != HeadlessOutputStreamJSON {
		return result, fmt.Errorf("unsupported headless output format %q (want text, json, or stream-json)", format)
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if a != nil {
		a.bashSandbox.Warn(ctx, opts.Stderr)
	}
	emitEarlyFailure := func(kind string, failure error) (HeadlessResult, error) {
		result.Status = "error"
		if errors.Is(failure, context.Canceled) {
			result.Status, kind = "cancelled", "cancelled"
		} else if errors.Is(failure, context.DeadlineExceeded) {
			result.Status, kind = "timeout", "timeout"
		}
		result.Error = &HeadlessError{Kind: kind, Message: failure.Error()}
		result.DurationMS = time.Since(started).Milliseconds()
		result.Timing = trace.Snapshot()
		if a != nil && a.session != nil {
			result.SessionID = a.session.GetID()
		}
		if format == HeadlessOutputJSON || format == HeadlessOutputStreamJSON {
			if err := json.NewEncoder(opts.Stdout).Encode(result); err != nil {
				return result, errors.Join(failure, fmt.Errorf("write headless JSON result: %w", err))
			}
		}
		return result, failure
	}

	if a != nil && (a.bashSandbox.Required() || result.Sandbox.Network == "blocked") && result.Sandbox.Status != "enforced" {
		if result.Sandbox.Network == "blocked" {
			return emitEarlyFailure("sandbox", fmt.Errorf("unsafe bash sandbox policy: %s", result.Sandbox.Reason))
		}
		return emitEarlyFailure("sandbox", fmt.Errorf("required bash sandbox unavailable: %s", result.Sandbox.Reason))
	}
	if opts.MaxTurns < 0 {
		return emitEarlyFailure("validation", fmt.Errorf("max turns must be zero or greater"))
	}
	if opts.Timeout < 0 {
		return emitEarlyFailure("validation", fmt.Errorf("timeout must be zero or greater"))
	}
	if opts.MaxBudgetUSD < 0 {
		return emitEarlyFailure("validation", fmt.Errorf("maximum budget must be zero or greater"))
	}
	if opts.JSONSchema != nil && format == HeadlessOutputText {
		return emitEarlyFailure(
			"validation",
			fmt.Errorf("structured output requires json or stream-json output format"),
		)
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" && (a == nil || a.hooksManager == nil) {
		return emitEarlyFailure("validation", fmt.Errorf("prompt is required"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Timeout > 0 {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithTimeout(ctx, opts.Timeout)
		defer deadlineCancel()
	}
	if a == nil {
		return emitEarlyFailure("app_init", fmt.Errorf("app is nil"))
	}
	if a.session == nil {
		return emitEarlyFailure("app_init", fmt.Errorf("session not initialized"))
	}
	result.SessionID = a.session.GetID()
	if a.executor == nil {
		return emitEarlyFailure("app_init", fmt.Errorf("executor not initialized"))
	}
	var pendingRecovery *chat.SerializedPendingRecovery

	// Claim the same foreground slot used by the interactive submit path before
	// swapping presenters or execution policy. A busy App must remain entirely
	// untouched: in particular, a headless caller must never replace another
	// turn's cancellation owner and later clear its processing flag.
	runCtx, cancel := context.WithCancel(ctx)
	runCtx = client.NewOutputRecoveryBudget(runCtx)
	permissionMode := opts.PermissionMode
	if permissionMode == "" {
		a.mu.Lock()
		if a.planningModeEnabled {
			permissionMode = "plan"
		} else if a.config != nil && a.config.Permission.DontAsk {
			permissionMode = "dontAsk"
		}
		a.mu.Unlock()
	}
	runCtx, permissionState := tools.ContextWithHeadlessPermissions(runCtx, opts.StrictPermissions, permissionMode)
	if opts.NoSelfReview || os.Getenv("GOKIN_NO_SELF_REVIEW") == "1" {
		runCtx = tools.ContextWithoutSelfReview(runCtx)
	}
	runCtx = tools.ContextWithMaxTurns(runCtx, opts.MaxTurns)
	runCtx = tools.ContextWithMaxBudgetUSD(runCtx, opts.MaxBudgetUSD)
	claim, err := a.claimHeadlessForeground(cancel)
	if err != nil {
		cancel()
		return emitEarlyFailure("headless_busy", err)
	}
	// claimHeadlessForeground creates this invocation's terminal owner; bind
	// delegated-agent accounting only after that token exists.
	runCtx = a.withHeadlessInvocationScope(runCtx)
	defer a.closeAsyncHooks()
	runCtx = a.withAuxiliaryRequests(runCtx)
	if a.contextManager != nil {
		a.contextManager.SetAuxiliaryRequestContext(runCtx)
	}
	restoreInlineSteers := a.executor.SetInlineFinalUserSteers(opts.InlineExternalSteers)
	defer restoreInlineSteers()
	pipelineStarted := false
	defer func() {
		cancel()
		a.releaseHeadlessForeground(claim, pipelineStarted)
	}()
	runCtx = a.withSessionStartHooks(runCtx)
	if err := hooks.AwaitSessionStart(runCtx); err != nil {
		return emitEarlyFailure("session_start", err)
	}
	var startupFollowup string
	if initial := a.takeSessionInitialMessage(); initial != "" {
		startupFollowup, prompt = prompt, initial
	}
	if strings.TrimSpace(prompt) == "" {
		return emitEarlyFailure("validation", fmt.Errorf("prompt is required; pass a prompt or provide SessionStart initialUserMessage"))
	}
	// Re-evaluate durable recovery state only after the shared foreground slot
	// is ours. The prior turn may have persisted a retry while this invocation
	// was waiting to claim the slot; launching an unrelated prompt from the
	// earlier snapshot would bypass that lineage.
	var headlessLineage conversationLineage
	pendingRecovery, headlessLineage, err = a.prepareHeadlessRecovery(prompt)
	if err != nil {
		return emitEarlyFailure("recovery_pending", err)
	}
	result.SessionID = headlessLineage.sessionID
	memoryQuery := prompt
	if pendingRecovery != nil {
		claimEpoch := headlessLineage.epoch
		claimed, checkpoints, claimErr := a.claimPendingRecovery(
			pendingRecovery.ID, pendingRecovery.SessionID, claimEpoch)
		if claimErr != nil {
			return emitEarlyFailure("recovery_claim", claimErr)
		}
		// Headless owns the exclusive foreground before claiming. Handing the
		// generation to its run context is therefore the dispatch boundary.
		a.markRecoveryDispatched(claimed.SessionID, claimed.ID)
		prompt = claimed.Message
		if claimed.UserMessage != "" {
			memoryQuery = claimed.UserMessage
		}
		runCtx = withPersistedSideEffectRecovery(
			runCtx, claimed.ID, claimed.SessionID, checkpoints)
		runCtx = withConversationLineage(runCtx, claimed.SessionID, claimEpoch)
	} else {
		runCtx = withConversationLineage(
			runCtx, headlessLineage.sessionID, headlessLineage.epoch)
	}

	// Swap the presenter: the execution handler (built once by the builder
	// with ALL the shared bookkeeping — journal, heartbeat, response
	// metadata) stays in place; only WHERE output goes changes. This
	// replaced installHeadlessHandler, a hand-maintained ~80% copy of the
	// builder's handler that silently drifted (no plan-step effects, no
	// token-estimate fires).
	previousPresenter := a.currentPresenter()
	goalFields := strings.Fields(prompt)
	goalTextMode := len(goalFields) > 0 && goalFields[0] == "/goal"
	if goal := a.session.GoalState().Current; goal != nil && goal.Status == goals.Active {
		goalTextMode = true
	}
	var sp headlessOutputPresenter
	switch format {
	case HeadlessOutputStreamJSON:
		sp = newStreamJSONPresenter(opts.Stdout, result.SessionID, opts.StreamState)
	default:
		presenterWriter := io.Discard
		if format == HeadlessOutputText && !goalTextMode && !opts.Brief {
			presenterWriter = opts.Stdout
		}
		sp = newStdoutPresenter(presenterWriter)
	}
	a.setPresenter(sp)
	presenterRestored := false
	defer func() {
		if !presenterRestored {
			a.setPresenter(previousPresenter)
		}
	}()
	finishOutput := sp.Finish
	a.runSystemPromptMu.Lock()
	if opts.Brief {
		a.runBriefPrompt = briefResultInstruction
	}
	a.runSystemPromptMu.Unlock()
	defer func() {
		a.runSystemPromptMu.Lock()
		a.runBriefPrompt = ""
		a.runSystemPromptMu.Unlock()
		if opts.Brief {
			if current := a.clientSnapshot(); current != nil {
				a.applySystemInstruction(current, a.session.GetSystemInstruction(), false)
			}
		}
	}()
	a.prepareHeadlessRuntime()
	restoreStructuredInstruction := a.installStructuredOutputInstruction(opts.JSONSchema)
	defer restoreStructuredInstruction()

	// Prefer direct execution over the task router: one agent, deterministic
	// behavior — the right default for evals and scripts. NOTE: since the
	// output-channel unification this is a POLICY choice, not a correctness
	// crutch — routed runs now deliver their final response through the
	// presenter (deliverUnstreamedResponse) and journal sub-agent tool
	// events (handleSubAgentActivity). The original incident (deepseek
	// baseline: routed headless printed nothing, journal blind) cannot
	// recur even with routing enabled.
	var structuredErr error
	structuredAttempts := 0
	for {
		goalFields := strings.Fields(prompt)
		var work, commandResult string
		var goalCommand bool
		var localCommand bool
		var goalErr, submissionErr error
		if len(goalFields) > 0 && goalFields[0] == "/goal" && commands.GoalStartsWork(goalFields[1:]) {
			runCtx, submissionErr = a.withPromptSubmission(runCtx, prompt)
		}
		if submissionErr == nil {
			runCtx, work, commandResult, localCommand, goalErr = a.headlessSlashCommand(runCtx, prompt)
			if !localCommand && goalErr == nil {
				work, commandResult, goalCommand, goalErr = a.goalCommand(runCtx, prompt)
			}
			if localCommand && a.session != nil {
				result.SessionID = a.session.GetID()
			}
		}
		if submissionErr != nil {
			a.reportHeadlessStopFailure(runCtx, a.activeHeadlessTerminalToken(), submissionErr)
		} else if goalErr != nil {
			kind := "goal"
			if localCommand {
				kind = "command"
			}
			a.recordHeadlessTerminalOutcome(kind, goalErr.Error())
		} else if (goalCommand || localCommand) && work == "" {
			a.recordHeadlessFinalResult(commandResult)
			if format == HeadlessOutputText && !goalTextMode {
				sp.StreamText(commandResult)
			}
			// Status/clear commands are read-only with respect to the model. A
			// schema mismatch must not turn their output into a new worker turn.
			if opts.JSONSchema != nil {
				_, structuredErr = opts.JSONSchema.Validate(commandResult)
			}
		} else {
			if goalCommand || localCommand {
				prompt, memoryQuery = work, work
			}
			a.goalMu.Lock()
			a.goalNext = nil
			a.goalMu.Unlock()
			turnCtx := runCtx
			if goalCommand {
				if goal := a.session.GoalState().Current; goal != nil {
					turnCtx = context.WithValue(runCtx, goalContinuationKey{}, &goalContinuation{sessionID: a.session.GetID(), goalID: goal.ID, epoch: a.recoveryEpoch.Load(), turns: goal.Turns})
				}
			}
			for {
				pipelineStarted = true
				result.StructuredOutput = nil
				var deferred *goalTurnDeferral
				processingCtx := turnCtx
				if opts.JSONSchema != nil {
					deferred = &goalTurnDeferral{}
					processingCtx = context.WithValue(processingCtx, goalTurnDeferralKey{}, deferred)
				}
				a.processMessageWithMemoryQuery(processingCtx, prompt, memoryQuery)
				if runCtx.Err() != nil {
					// A deadline can fall after /goal creates its owner but before
					// processMessage installs a turn observation. That early return
					// still has to pause the exact goal created by this invocation.
					if source, ok := turnCtx.Value(goalContinuationKey{}).(*goalContinuation); ok {
						a.sessionLeaseMu.Lock()
						current := a.session.GoalState().Current
						unpaused := a.goalSourceMatchesLocked(source) && current != nil && !current.Suspended
						a.sessionLeaseMu.Unlock()
						if unpaused {
							a.failGoalTurn(turnCtx, source, runCtx.Err())
						}
					}
				}
				if !a.headlessPipelineHealthy(runCtx) {
					if deferred != nil && deferred.ready {
						err := deferred.err
						if err == nil {
							err = runCtx.Err()
						}
						if err == nil {
							err = fmt.Errorf("goal work stopped before its terminal answer completed")
						}
						a.finalizeDeferredGoalTurn(turnCtx, deferred, a.headlessFinalResultSnapshot(), err)
					}
					break
				}
				result.Result = selectHeadlessResult(sp.Result(), a.headlessFinalResultSnapshot())
				correctionCtx := context.WithValue(runCtx, goalTurnDeferralKey{}, deferred)
				if deferred != nil && deferred.lineage != nil {
					correctionCtx = withConversationLineage(correctionCtx, deferred.lineage.sessionID, deferred.lineage.epoch)
				}
				if deferred != nil && deferred.observation != nil && deferred.observation.account != nil {
					correctionCtx = a.withGoalUsageContext(correctionCtx, deferred.observation.account)
				}
				structuredErr = a.correctHeadlessStructuredOutput(correctionCtx, opts.JSONSchema, sp, &result, memoryQuery, &structuredAttempts)
				finalizationErr := structuredErr
				if deferred != nil && deferred.err != nil {
					finalizationErr = deferred.err
				}
				if finalizationErr == nil && !a.headlessPipelineHealthy(runCtx) {
					finalizationErr = fmt.Errorf("structured-output correction did not complete")
				}
				if structuredErr != nil && a.headlessPipelineHealthy(runCtx) {
					a.recordHeadlessTerminalOutcome("structured_output", structuredErr.Error())
				}
				a.finalizeDeferredGoalTurn(turnCtx, deferred, result.Result, finalizationErr)
				if structuredErr != nil || !a.headlessPipelineHealthy(runCtx) {
					break
				}
				next := a.takeGoalContinuation()
				if next == nil {
					break
				}
				prompt, memoryQuery = next.prompt, next.prompt
				turnCtx = withConversationLineage(runCtx, next.sessionID, next.epoch)
				turnCtx = context.WithValue(turnCtx, goalContinuationKey{}, next)
			}
		}
		if startupFollowup == "" || structuredErr != nil || !a.headlessPipelineHealthy(runCtx) {
			break
		}
		prompt, memoryQuery = startupFollowup, startupFollowup
		startupFollowup = ""
	}
	result.Result = selectHeadlessResult(sp.Result(), a.headlessFinalResultSnapshot())

	finishOutput()
	// The shared finalizer retains foreground ownership while the headless token
	// is active. Restore presenter/routing immediately; the slot itself remains
	// occupied through persistence and result encoding, so no interactive turn
	// can inherit headless routing or output.
	a.setPresenter(previousPresenter)
	presenterRestored = true
	a.restoreHeadlessDirect(claim)
	result.Result = selectHeadlessResult(sp.Result(), a.headlessFinalResultSnapshot())

	trace.Phase("async_hook_cleanup")
	a.closeAsyncHooks()
	// Persist the turn SYNCHRONOUSLY so a subsequent headless resume continues
	// this conversation. The normal SaveAfterMessage path is async +
	// debounced and may not flush before the process exits. Save() is a no-op
	// when persistence is disabled. A save error is terminal: exit 0 would tell
	// an orchestrator that the advertised session_id is safe to resume even
	// though the updated state was never committed. The model result remains in
	// stdout/JSON for inspection, while the process fails closed.
	var saveErr error
	if a.sessionManager != nil {
		trace.Phase("session_save")
		// Terminal paths such as a failed done-gate or recovered panic return
		// before message_processor's normal checkpoint sync. Persist the executor
		// journal first so an exact resume cannot repeat a mutation that already
		// completed before finalization failed.
		a.syncToolCheckpoints()
		if err := a.sessionManager.Save(); err != nil {
			saveErr = fmt.Errorf("failed to persist session: %w", err)
			fmt.Fprintf(opts.Stderr, "Error: %v\n", saveErr)
		}
	}

	// Headless is a second exit path that never reaches gracefulShutdown, so it
	if pipelineStarted && runCtx.Err() == nil {
		if goal := a.session.GoalState().Current; goal != nil && goal.Status == goals.Active {
			a.recordHeadlessTerminalOutcome("goal_unfinished", "Goal remains active: "+goal.Reason)
		}
	}

	// Headless is a second exit path that never reaches gracefulShutdown, so it
	// must flush the durable stores itself. Without this the lifetime tool
	// counts are lost in exactly the mode that runs evals and scripts — the
	// traffic the measurement most needs. A telemetry failure must not change
	// the headless exit status, so this is logged, not returned.
	trace.Phase("tool_usage_flush")
	if err := a.toolUsage.Flush(); err != nil {
		logging.Debug("tool usage ledger flush failed in headless", "error", err)
	}

	a.mu.Lock()
	lastErr := a.lastError
	a.mu.Unlock()
	var runErr error
	if blocked := a.headlessPolicyFailureSnapshot(); blocked != nil {
		reason := strings.TrimSpace(blocked.Block.Reason)
		if reason == "" {
			reason = "tool call was not authorized"
		}
		runErr = fmt.Errorf("headless execution blocked by %s policy for tool %q: %s",
			blocked.Block.Kind, blocked.ToolName, reason)
		result.Status = "policy_blocked"
		result.Error = &HeadlessError{
			Kind:       "policy_blocked",
			Message:    runErr.Error(),
			Tool:       blocked.ToolName,
			PolicyKind: string(blocked.Block.Kind),
		}
	} else if terminal := a.headlessTerminalOutcomeSnapshot(); terminal != nil {
		switch terminal.Kind {
		case "timeout":
			runErr = &headlessTimeoutError{message: terminal.Message}
			result.Status = "timeout"
		case "max_turns":
			runErr = &headlessMaxTurnsError{message: terminal.Message}
			result.Status = "error"
		case "budget_exceeded":
			runErr = &headlessBudgetError{message: terminal.Message}
			result.Status = "error"
		case "cost_unavailable":
			runErr = &headlessCostUnavailableError{message: terminal.Message}
			result.Status = "error"
		case "hook_stopped":
			runErr = errors.New(terminal.Message)
			result.Status = "policy_blocked"
		default:
			runErr = errors.New(terminal.Message)
			result.Status = "error"
		}
		result.Error = &HeadlessError{Kind: terminal.Kind, Message: terminal.Message}
	} else if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		runErr = context.DeadlineExceeded
		result.Status = "timeout"
		result.Error = &HeadlessError{Kind: "timeout", Message: runErr.Error()}
	} else if errors.Is(runCtx.Err(), context.Canceled) {
		runErr = context.Canceled
		result.Status = "cancelled"
		result.Error = &HeadlessError{Kind: "cancelled", Message: runErr.Error()}
	} else if strings.TrimSpace(lastErr) != "" {
		runErr = fmt.Errorf("%s", lastErr)
		result.Status = "error"
		result.Error = &HeadlessError{Kind: "execution", Message: runErr.Error()}
	} else if structuredErr != nil {
		runErr = structuredErr
		result.Status = "error"
		result.Error = &HeadlessError{
			Kind:    "structured_output",
			Message: structuredErr.Error(),
		}
	} else if saveErr != nil {
		runErr = saveErr
		result.Status = "error"
		result.Error = &HeadlessError{Kind: "persistence_failed", Message: saveErr.Error()}
	} else {
		result.Status = "success"
	}
	if saveErr != nil && !errors.Is(runErr, saveErr) {
		runErr = errors.Join(runErr, saveErr)
		if result.Error != nil {
			result.Error.Message = runErr.Error()
		}
	}

	result.Usage, result.Cost = a.headlessInvocationMetricsSnapshot()
	if rounds, scoped := tools.InvocationModelTurnsClaimed(runCtx); scoped {
		result.Usage.ModelRounds = rounds
	}
	if budgetLedger, budgeted := tools.InvocationBudgetLedgerFromContext(runCtx); budgeted {
		_, spent := budgetLedger.Snapshot()
		// The shared ledger is authoritative for a budgeted invocation. Agent
		// callbacks can aggregate nested results differently (and auxiliary
		// planner/summarizer rounds are not all represented by AgentResult), so
		// using their sum here can either double-count or omit real spend.
		result.Cost.EstimatedUSD = spent
		result.Cost.Tracked = !errors.Is(runErr, tools.ErrCostUnavailable)
	}
	result.DurationMS = time.Since(started).Milliseconds()
	result.Sandbox = a.GetBashSandboxStatus()
	trace.Phase("result_encoding")
	result.Timing = trace.Snapshot()
	result.SessionID = a.session.GetID()
	a.journalEvent("headless_metrics", map[string]any{
		"input_tokens":            result.Usage.InputTokens,
		"output_tokens":           result.Usage.OutputTokens,
		"cache_read_input_tokens": result.Usage.CacheReadInputTokens,
		"total_tokens":            result.Usage.TotalTokens,
		"model_rounds":            result.Usage.ModelRounds,
		"duration_ms":             result.DurationMS,
		"estimated_usd":           result.Cost.EstimatedUSD,
		"cost_tracked":            result.Cost.Tracked,
	})

	if outputErr := sp.Err(); outputErr != nil {
		wrapped := fmt.Errorf("write headless output: %w", outputErr)
		if runErr == nil {
			runErr = wrapped
			result.Status = "error"
			result.Error = &HeadlessError{Kind: "output", Message: wrapped.Error()}
		} else {
			runErr = errors.Join(runErr, wrapped)
		}
	}

	result.PermissionDenials = permissionState.Denials()
	result.Verification = permissionState.Verification()
	a.mu.Lock()
	checkClaims := a.responseClaimRequested
	a.mu.Unlock()
	// Schema correction may replace an earlier claim or introduce a new one.
	// Judge the final emitted answer even when the correction budget is spent.
	if checkClaims {
		result.ClaimMismatch = len(a.absentMutationClaim(runCtx, result.Result)) > 0
	}
	if result.ClaimMismatch && format == HeadlessOutputText {
		fmt.Fprintln(opts.Stderr, "Warning: claim mismatch — the final answer claims file changes, but no successful mutation was recorded.")
	}
	unfinished, _ := tools.IncompleteTodoSummary(a.registry)
	result.Incomplete = result.ClaimMismatch || result.Status != "success" || unfinished > 0 || permissionState.Incomplete()
	if len(result.PermissionDenials) > 0 {
		result.Incomplete = true
		deniedTools := make([]string, 0, len(result.PermissionDenials))
		for _, denial := range result.PermissionDenials {
			deniedTools = append(deniedTools, denial.Tool)
		}
		sort.Strings(deniedTools)
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"%d tool call(s) were denied by permissions (%s); the answer may not be based on them",
			len(result.PermissionDenials), strings.Join(slices.Compact(deniedTools), ", ")))
	}
	a.mu.Lock()
	result.FilesChanged = append([]string{}, a.headlessFilesChanged...)
	result.FilesChangedPartial = a.headlessFilesChangedPartial || a.executor.WorkspaceWritesPartial()
	a.mu.Unlock()
	sort.Strings(result.FilesChanged)
	if format == HeadlessOutputText && result.FilesChangedPartial {
		if _, err := fmt.Fprintln(opts.Stderr, "Warning: files_changed is partial; workspace write observation exceeded its limits or was unavailable."); err != nil {
			return result, errors.Join(runErr, err)
		}
	}
	if format == HeadlessOutputText && len(result.FilesChanged) > 0 && result.Verification.Status != "passed" {
		if _, err := fmt.Fprintf(opts.Stderr, "Not verified: %s (see JSON verification checks for details).\n", result.Verification.Status); err != nil {
			return result, errors.Join(runErr, err)
		}
	}
	if opts.Brief && opts.JSONSchema == nil {
		result.Result = strings.Join(strings.Fields(result.Result), " ")
	}
	result.Goal = a.session.GoalState().Current
	if (goalTextMode || opts.Brief) && format == HeadlessOutputText && result.Result != "" {
		if _, err := fmt.Fprintln(opts.Stdout, result.Result); err != nil {
			return result, errors.Join(runErr, err)
		}
	}
	if opts.Brief && format == HeadlessOutputText {
		for _, path := range result.FilesChanged {
			if _, err := fmt.Fprintln(opts.Stdout, path); err != nil {
				return result, errors.Join(runErr, err)
			}
		}
	}
	if format == HeadlessOutputJSON || format == HeadlessOutputStreamJSON {
		if err := json.NewEncoder(opts.Stdout).Encode(result); err != nil {
			outputErr := fmt.Errorf("write headless JSON result: %w", err)
			if runErr != nil {
				return result, errors.Join(runErr, outputErr)
			}
			return result, outputErr
		}
	}

	return result, runErr
}

func (a *App) headlessPipelineHealthy(ctx context.Context) bool {
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if a == nil || a.headlessPolicyFailureSnapshot() != nil ||
		a.headlessTerminalOutcomeSnapshot() != nil {
		return false
	}
	a.mu.Lock()
	lastErr := strings.TrimSpace(a.lastError)
	a.mu.Unlock()
	return lastErr == ""
}

func headlessUsageDelta(before, after commands.TokenStats) HeadlessUsage {
	input := max(after.InputTokens-before.InputTokens, 0)
	output := max(after.OutputTokens-before.OutputTokens, 0)
	cacheRead := max(after.CacheReadInputTokens-before.CacheReadInputTokens, 0)
	return HeadlessUsage{
		InputTokens:          input,
		OutputTokens:         output,
		CacheReadInputTokens: min(cacheRead, input),
		TotalTokens:          input + output,
	}
}

func selectHeadlessResult(streamed, final string) string {
	// Internal completion-review and done-gate auto-fix calls share the live
	// presenter, so the stream can contain several model exchanges. The JSON
	// result is the terminal user-facing answer, not their concatenation.
	if strings.TrimSpace(final) != "" {
		return final
	}
	return streamed
}

// headlessForegroundClaim is the ownership token for one RunHeadless call.
// The terminal pointer already identifies invocation-scoped watchdog/accounting
// state, so reusing it here keeps lifecycle cleanup aligned with that contract.
type headlessForegroundClaim struct {
	terminal       *headlessTerminalOutcome
	previousDirect bool
	tracked        bool
}

// claimHeadlessForeground atomically checks and occupies the application's
// shared foreground slot. running denotes a live interactive runtime while
// processing denotes an accepted foreground turn; either makes an embedded
// headless invocation unsafe. The caller-created cancel function is installed
// as the normal foreground cancellation owner, but its parent is the caller's
// context rather than App.ctx.
func (a *App) claimHeadlessForeground(cancel context.CancelFunc) (*headlessForegroundClaim, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.shuttingDown || a.headlessRunActive || a.processing || a.running {
		return nil, fmt.Errorf("headless execution cannot start while the application foreground is busy")
	}

	a.processingMu.Lock()
	defer a.processingMu.Unlock()
	if a.processingCancel != nil {
		return nil, fmt.Errorf("headless execution cannot start while the application foreground is busy")
	}
	if !a.foregroundWorkers.Add() {
		return nil, fmt.Errorf("headless execution cannot start while the application is shutting down")
	}

	previousDirect := a.headlessDirect
	a.beginHeadlessPolicyTrackingLocked()
	a.headlessDirect = true
	a.processing = true
	a.dropSteerLeftovers = false
	a.lastError = ""
	a.processingCancel = cancel
	return &headlessForegroundClaim{
		terminal:       a.headlessTerminal,
		previousDirect: previousDirect,
		tracked:        true,
	}, nil
}

// restoreHeadlessDirect returns shared routing policy to its pre-invocation
// value as soon as model processing ends. Token matching prevents a stale
// cleanup from changing a later invocation.
func (a *App) restoreHeadlessDirect(claim *headlessForegroundClaim) {
	if claim == nil {
		return
	}
	a.mu.Lock()
	if a.headlessRunActive && a.headlessTerminal == claim.terminal {
		a.headlessDirect = claim.previousDirect
	}
	a.mu.Unlock()
}

// releaseHeadlessForeground clears only state still owned by claim. Once the
// shared message pipeline has started, its finalizer clears processingCancel
// but deliberately retains the processing slot for this terminal phase.
func (a *App) releaseHeadlessForeground(claim *headlessForegroundClaim, pipelineStarted bool) {
	if claim == nil {
		return
	}
	if claim.tracked {
		defer a.foregroundWorkers.Done()
	}

	a.mu.Lock()
	if !a.headlessRunActive || a.headlessTerminal != claim.terminal {
		a.mu.Unlock()
		return
	}

	a.headlessDirect = claim.previousDirect
	if !pipelineStarted {
		a.processingMu.Lock()
		a.processingCancel = nil
		a.processingMu.Unlock()
	}
	a.endHeadlessPolicyTrackingLocked()
	a.mu.Unlock()

	// The pipeline finalizer intentionally retained processing ownership while
	// the headless token was active. With presenter/routing/tracking restored,
	// use the ordinary foreground release path so a queued interactive request
	// is dispatched exactly once and under its own context.
	a.finishForegroundProcessing(nil)
}

func (a *App) beginHeadlessPolicyTracking() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.headlessRunActive {
		return fmt.Errorf("headless execution is already running")
	}
	a.beginHeadlessPolicyTrackingLocked()
	return nil
}

func (a *App) beginHeadlessPolicyTrackingLocked() {
	a.headlessRunActive = true
	a.headlessPolicyFailure = nil
	// The empty outcome is also this invocation's identity token. A fresh
	// pointer on every run lets asynchronous watchdogs prove that their owner
	// is still active before recording a terminal condition.
	a.headlessTerminal = &headlessTerminalOutcome{}
	a.headlessFinalResult = ""
	a.headlessFilesChanged = nil
	a.headlessFilesChangedPartial = false
	a.headlessInvocationUsage = HeadlessUsage{}
	a.headlessInvocationCost = HeadlessCost{}
	a.headlessCostIncomplete = false
	a.headlessAgentUsageScope = agent.InvocationScope{}
	a.headlessAgentUsageScopeOwner = nil
}

func (a *App) endHeadlessPolicyTracking() {
	a.mu.Lock()
	a.endHeadlessPolicyTrackingLocked()
	a.mu.Unlock()
}

func (a *App) endHeadlessPolicyTrackingLocked() {
	a.headlessRunActive = false
	a.headlessPolicyFailure = nil
	a.headlessTerminal = nil
	a.headlessFinalResult = ""
	a.headlessFilesChanged = nil
	a.headlessFilesChangedPartial = false
	a.headlessInvocationUsage = HeadlessUsage{}
	a.headlessInvocationCost = HeadlessCost{}
	a.headlessCostIncomplete = false
	a.headlessAgentUsageScope = agent.InvocationScope{}
	a.headlessAgentUsageScopeOwner = nil
}

// headlessInvocationMetricsSnapshot returns the usage attributed directly to
// the active invocation. It deliberately does not derive a delta from session
// totals: metadata-free session input is a lower bound and therefore does not
// monotonically add across turns.
func (a *App) headlessInvocationMetricsSnapshot() (HeadlessUsage, HeadlessCost) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cost := a.headlessInvocationCost
	if a.headlessCostIncomplete {
		cost.Tracked = false
	}
	return a.headlessInvocationUsage, cost
}

// recordHeadlessPolicyFailure is called by the shared execution handler. It
// is a no-op for interactive turns and latches only the first failure in the
// active headless invocation.
func (a *App) recordHeadlessPolicyFailure(toolName string, block tools.PolicyBlock) {
	if block.Recoverable || a.advisoryCorrectionActive.Load() {
		return
	}
	// Failing closed here is deliberate: a model reaching for a tool during a
	// "just reformat your answer" step is exactly the moment to stop. But the
	// refusal came from the empty ceiling THIS file installs for correction
	// turns, so reporting the raw reason reads as the operator's own policy
	// firing. Name the real cause instead.
	if a.structuredCorrectionActive.Load() {
		block.Reason = "tool use is not available during a structured-output format correction; " +
			"the model attempted " + toolName + " instead of returning JSON"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.headlessRunActive || a.headlessPolicyFailure != nil {
		return
	}
	a.headlessPolicyFailure = &headlessPolicyFailure{ToolName: toolName, Block: block}
}

func (a *App) headlessPolicyFailureSnapshot() *headlessPolicyFailure {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.headlessPolicyFailure == nil {
		return nil
	}
	copy := *a.headlessPolicyFailure
	return &copy
}

// recordHeadlessTerminalOutcome latches the first generic terminal condition
// for the active headless turn. It deliberately does nothing for interactive
// execution, where panic and done-gate failures remain recoverable in the TUI.
func (a *App) recordHeadlessTerminalOutcome(kind, message string) {
	a.recordHeadlessTerminalOutcomeForTurn(a.activeHeadlessTerminalToken(), kind, message)
}

// activeHeadlessTerminalToken returns the identity of the current headless
// invocation. Asynchronous producers retain it so a late result cannot leak
// into a later invocation on the same App.
func (a *App) activeHeadlessTerminalToken() *headlessTerminalOutcome {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.headlessRunActive {
		return nil
	}
	return a.headlessTerminal
}

// recordHeadlessTerminalOutcomeForTurn is first-wins and turn-scoped. It is a
// no-op for interactive turns, completed runs, and stale watchdogs.
func (a *App) recordHeadlessTerminalOutcomeForTurn(token *headlessTerminalOutcome, kind, message string) {
	kind = strings.TrimSpace(kind)
	message = strings.TrimSpace(message)
	if token == nil || kind == "" || message == "" {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.headlessRunActive || a.headlessTerminal != token || token.Kind != "" {
		return
	}
	token.Kind = kind
	token.Message = message
}

func (a *App) headlessTerminalOutcomeSnapshot() *headlessTerminalOutcome {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.headlessTerminal == nil || a.headlessTerminal.Kind == "" {
		return nil
	}
	copy := *a.headlessTerminal
	return &copy
}

func (a *App) recordHeadlessFinalResult(result string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.headlessRunActive {
		return
	}
	a.headlessFinalResult = result
}

func (a *App) headlessFinalResultSnapshot() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.headlessFinalResult
}

func (a *App) prepareHeadlessRuntime() {
	// Keep logs out of stdout/stderr so eval output remains the model answer.
	if a.config == nil || !a.config.Debug {
		logging.DisableLogging()
	}

	// A one-shot result cannot truthfully succeed while detached work is still
	// running: the process releases its session lease and cancels App resources
	// immediately after this turn. Remove background arguments from the model
	// schema and retain a runtime rejection for malformed/hallucinated calls.
	if a.registry != nil {
		if tool, ok := a.registry.Get("bash"); ok {
			if bash, ok := tool.(*tools.BashTool); ok {
				bash.SetBackgroundAllowed(false)
			}
		}
		if tool, ok := a.registry.Get("task"); ok {
			if task, ok := tool.(*tools.TaskTool); ok {
				task.SetBackgroundAllowed(false)
			}
		}
	}

	if a.promptBuilder != nil {
		// a.config is explicitly treated as possibly-nil at the top of this
		// function; keep that contract here rather than panicking on the first
		// caller that has not built one yet.
		if a.config == nil || !a.config.Bare {
			a.detectedProjectContext = a.detectProjectContext()
			if a.detectedProjectContext != "" {
				a.promptBuilder.SetDetectedContext(a.detectedProjectContext)
			}
		}
		a.promptBuilder.SetPlanMode(a.planningModeEnabled)
	}
	if a.client != nil {
		a.client.SetTools(a.toolsForCurrentMode())
	}

	systemPrompt := ""
	systemPrompt = a.buildDefaultSystemInstruction()
	if (strings.TrimSpace(systemPrompt) != "" || a.hasRunSystemPromptCustomization()) &&
		a.client != nil {
		a.applySystemInstruction(a.client, systemPrompt, true)
	}
	a.pushTurnContext()
	if a.session != nil {
		a.session.SetProvider(runtimeProviderForConfig(a.config))
	}
}

// stdoutPresenter streams the model's answer to stdout for headless runs.
// Tool/UI events are no-ops: the journal (shared bookkeeping) records tool
// activity, and there is no screen to paint. Thinking is deliberately NOT
// printed — stdout must stay the model answer.
type stdoutPresenter struct {
	mu               sync.Mutex
	writer           io.Writer
	result           strings.Builder
	writeErr         error
	sawText          bool
	endedWithNewline bool
}

func newStdoutPresenter(writer io.Writer) *stdoutPresenter {
	if writer == nil {
		writer = io.Discard
	}
	return &stdoutPresenter{writer: writer}
}

func (p *stdoutPresenter) StreamText(text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if text == "" {
		return
	}
	_, _ = p.result.WriteString(text)
	if p.writeErr == nil {
		_, p.writeErr = io.WriteString(p.writer, text)
	}
	if text != "" {
		p.sawText = true
		p.endedWithNewline = strings.HasSuffix(text, "\n")
	}
}

// Finish terminates the streamed answer with a newline when it lacks one —
// shell capture must never glue output onto the next line.
func (p *stdoutPresenter) Finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sawText && !p.endedWithNewline && p.writeErr == nil {
		_, p.writeErr = fmt.Fprintln(p.writer)
	}
}

func (p *stdoutPresenter) Result() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.result.String()
}

func (p *stdoutPresenter) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writeErr
}

func (p *stdoutPresenter) StreamThinking(string)                            {}
func (p *stdoutPresenter) StreamTokenEstimate(int)                          {}
func (p *stdoutPresenter) ToolStart(string, map[string]any)                 {}
func (p *stdoutPresenter) ToolEnd(string, map[string]any, tools.ToolResult) {}
func (p *stdoutPresenter) ToolProgress(string, time.Duration, string)       {}
func (p *stdoutPresenter) ToolDetailedProgress(string, float64, string)     {}
func (p *stdoutPresenter) ToolError(error)                                  {}
func (p *stdoutPresenter) Warning(string)                                   {}
func (p *stdoutPresenter) InlineDiff(string, string, string)                {}
func (p *stdoutPresenter) LoopIteration(int, int)                           {}
func (p *stdoutPresenter) TokenUsage(int, int, float64)                     {}
func (p *stdoutPresenter) FilePeek(string, string, string, string)          {}
func (p *stdoutPresenter) MemoryNotify(string)                              {}

func (p *stdoutPresenter) SubAgentActivity(string, string, string, string, map[string]any, string, bool, string) {
}
