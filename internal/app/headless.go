package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"gokin/internal/agent"
	"gokin/internal/commands"
	"gokin/internal/logging"
	"gokin/internal/tools"
)

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

// HeadlessOutputFormat selects the stable stdout contract used by
// RunHeadlessWithOptions. Text preserves the traditional streamed answer;
// JSON emits exactly one versioned result object after the turn completes.
type HeadlessOutputFormat string

const (
	HeadlessOutputText HeadlessOutputFormat = "text"
	HeadlessOutputJSON HeadlessOutputFormat = "json"

	HeadlessSchemaVersion = 1
)

// HeadlessOptions controls presentation without changing the execution path.
// Nil writers use the process stdout/stderr. Callers that intentionally want
// to discard a stream should pass io.Discard explicitly.
type HeadlessOptions struct {
	OutputFormat HeadlessOutputFormat
	Stdout       io.Writer
	Stderr       io.Writer
}

// HeadlessUsage is the usage delta for this invocation, not a cumulative
// session total. That distinction matters when one App executes several turns.
type HeadlessUsage struct {
	InputTokens          int `json:"input_tokens"`
	OutputTokens         int `json:"output_tokens"`
	CacheReadInputTokens int `json:"cache_read_input_tokens"`
	TotalTokens          int `json:"total_tokens"`
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
	SchemaVersion int            `json:"schema_version"`
	Type          string         `json:"type"`
	Result        string         `json:"result"`
	SessionID     string         `json:"session_id"`
	Status        string         `json:"status"`
	Error         *HeadlessError `json:"error,omitempty"`
	Usage         HeadlessUsage  `json:"usage"`
	Cost          HeadlessCost   `json:"cost"`
	DurationMS    int64          `json:"duration_ms"`
	Warnings      []string       `json:"warnings,omitempty"`
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
	result := HeadlessResult{
		SchemaVersion: HeadlessSchemaVersion,
		Type:          "result",
		Status:        "error",
	}

	format := opts.OutputFormat
	if format == "" {
		format = HeadlessOutputText
	}
	if format != HeadlessOutputText && format != HeadlessOutputJSON {
		return result, fmt.Errorf("unsupported headless output format %q (want text or json)", format)
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	emitEarlyFailure := func(kind string, failure error) (HeadlessResult, error) {
		result.Status = "error"
		result.Error = &HeadlessError{Kind: kind, Message: failure.Error()}
		result.DurationMS = time.Since(started).Milliseconds()
		if a != nil && a.session != nil {
			result.SessionID = a.session.GetID()
		}
		if format == HeadlessOutputJSON {
			if err := json.NewEncoder(opts.Stdout).Encode(result); err != nil {
				return result, errors.Join(failure, fmt.Errorf("write headless JSON result: %w", err))
			}
		}
		return result, failure
	}

	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return emitEarlyFailure("validation", fmt.Errorf("prompt is required"))
	}
	if ctx == nil {
		ctx = context.Background()
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

	// Claim the same foreground slot used by the interactive submit path before
	// swapping presenters or execution policy. A busy App must remain entirely
	// untouched: in particular, a headless caller must never replace another
	// turn's cancellation owner and later clear its processing flag.
	runCtx, cancel := context.WithCancel(ctx)
	claim, err := a.claimHeadlessForeground(cancel)
	if err != nil {
		cancel()
		return emitEarlyFailure("headless_busy", err)
	}
	// claimHeadlessForeground creates this invocation's terminal owner; bind
	// delegated-agent accounting only after that token exists.
	runCtx = a.withHeadlessInvocationScope(runCtx)
	pipelineStarted := false
	defer func() {
		cancel()
		a.releaseHeadlessForeground(claim, pipelineStarted)
	}()

	// Swap the presenter: the execution handler (built once by the builder
	// with ALL the shared bookkeeping — journal, heartbeat, response
	// metadata) stays in place; only WHERE output goes changes. This
	// replaced installHeadlessHandler, a hand-maintained ~80% copy of the
	// builder's handler that silently drifted (no plan-step effects, no
	// token-estimate fires).
	previousPresenter := a.currentPresenter()
	presenterWriter := io.Discard
	if format == HeadlessOutputText {
		presenterWriter = opts.Stdout
	}
	sp := newStdoutPresenter(presenterWriter)
	a.setPresenter(sp)
	presenterRestored := false
	defer func() {
		if !presenterRestored {
			a.setPresenter(previousPresenter)
		}
	}()
	finishOutput := sp.Finish
	a.prepareHeadlessRuntime()

	// Prefer direct execution over the task router: one agent, deterministic
	// behavior — the right default for evals and scripts. NOTE: since the
	// output-channel unification this is a POLICY choice, not a correctness
	// crutch — routed runs now deliver their final response through the
	// presenter (deliverUnstreamedResponse) and journal sub-agent tool
	// events (handleSubAgentActivity). The original incident (deepseek
	// baseline: routed headless printed nothing, journal blind) cannot
	// recur even with routing enabled.
	pipelineStarted = true
	a.processMessageWithContext(runCtx, prompt)
	finishOutput()
	// The shared finalizer retains foreground ownership while the headless token
	// is active. Restore presenter/routing immediately; the slot itself remains
	// occupied through persistence and result encoding, so no interactive turn
	// can inherit headless routing or output.
	a.setPresenter(previousPresenter)
	presenterRestored = true
	a.restoreHeadlessDirect(claim)
	result.Result = selectHeadlessResult(sp.Result(), a.headlessFinalResultSnapshot())

	// Persist the turn SYNCHRONOUSLY so a subsequent headless resume continues
	// this conversation. The normal SaveAfterMessage path is async +
	// debounced and may not flush before the process exits. Save() is a no-op
	// when persistence is disabled. A save error is terminal: exit 0 would tell
	// an orchestrator that the advertised session_id is safe to resume even
	// though the updated state was never committed. The model result remains in
	// stdout/JSON for inspection, while the process fails closed.
	var saveErr error
	if a.sessionManager != nil {
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
		if terminal.Kind == "timeout" {
			runErr = &headlessTimeoutError{message: terminal.Message}
			result.Status = "timeout"
		} else {
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
	result.DurationMS = time.Since(started).Milliseconds()
	result.SessionID = a.session.GetID()

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

	if format == HeadlessOutputJSON {
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

	if a.headlessRunActive || a.processing || a.running {
		return nil, fmt.Errorf("headless execution cannot start while the application foreground is busy")
	}

	a.processingMu.Lock()
	defer a.processingMu.Unlock()
	if a.processingCancel != nil {
		return nil, fmt.Errorf("headless execution cannot start while the application foreground is busy")
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
	logging.DisableLogging()

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
		a.detectedProjectContext = a.detectProjectContext()
		if a.detectedProjectContext != "" {
			a.promptBuilder.SetDetectedContext(a.detectedProjectContext)
		}
		a.promptBuilder.SetPlanMode(a.planningModeEnabled)
	}
	if a.client != nil {
		a.client.SetTools(a.toolsForCurrentMode())
	}

	systemPrompt := ""
	if a.promptBuilder != nil {
		systemPrompt = a.promptBuilder.Build()
	}
	systemPrompt += a.buildModelEnhancement()
	if strings.TrimSpace(systemPrompt) != "" && a.client != nil {
		a.client.SetSystemInstruction(systemPrompt)
		if a.session != nil {
			a.session.SetSystemInstruction(systemPrompt)
		}
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
