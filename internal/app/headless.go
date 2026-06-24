package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"gokin/internal/logging"
	"gokin/internal/tools"
)

// RunHeadless executes one user prompt through the normal request pipeline
// without starting the Bubble Tea TUI. It is intended for eval harnesses and
// scripts that need a plain shell command.
func (a *App) RunHeadless(ctx context.Context, prompt string) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if a == nil {
		return fmt.Errorf("app is nil")
	}
	if a.session == nil {
		return fmt.Errorf("session not initialized")
	}
	if a.executor == nil {
		return fmt.Errorf("executor not initialized")
	}

	// Swap the presenter: the execution handler (built once by the builder
	// with ALL the shared bookkeeping — journal, heartbeat, response
	// metadata) stays in place; only WHERE output goes changes. This
	// replaced installHeadlessHandler, a hand-maintained ~80% copy of the
	// builder's handler that silently drifted (no plan-step effects, no
	// token-estimate fires).
	sp := newStdoutPresenter()
	a.setPresenter(sp)
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
	a.mu.Lock()
	a.headlessDirect = true
	a.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	a.mu.Lock()
	a.running = true
	a.processing = true
	a.lastError = ""
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.running = false
		a.processing = false
		a.mu.Unlock()
		if a.cancel != nil {
			a.cancel()
		}
	}()

	a.processMessageWithContext(runCtx, prompt)
	finishOutput()

	// Persist the turn SYNCHRONOUSLY so a subsequent `gokin --headless --resume`
	// continues this conversation. The normal SaveAfterMessage path is async +
	// debounced and may not flush before the headless process exits. Save() is a
	// no-op when session persistence is disabled. A save error must NOT change
	// the headless exit status (the model answer already streamed) — but it must
	// also not be silent: logging is disabled in headless (stdout stays the model
	// answer), so a Debug line here would be dropped (LevelError), hiding the
	// fact that the next `--headless --resume` won't be able to continue. Surface
	// it on STDERR, like the resume warning in main.go.
	if a.sessionManager != nil {
		if err := a.sessionManager.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to persist session: %v (next --headless --resume may not continue)\n", err)
		}
	}

	a.mu.Lock()
	lastErr := a.lastError
	a.mu.Unlock()
	if strings.TrimSpace(lastErr) != "" {
		return fmt.Errorf("%s", lastErr)
	}
	return nil
}

func (a *App) prepareHeadlessRuntime() {
	// Keep logs out of stdout/stderr so eval output remains the model answer.
	logging.DisableLogging()

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
		a.session.SystemInstruction = systemPrompt
	}
	a.pushTurnContext()
	a.session.SetProvider(runtimeProviderForConfig(a.config))
}

// stdoutPresenter streams the model's answer to stdout for headless runs.
// Tool/UI events are no-ops: the journal (shared bookkeeping) records tool
// activity, and there is no screen to paint. Thinking is deliberately NOT
// printed — stdout must stay the model answer.
type stdoutPresenter struct {
	mu               sync.Mutex
	sawText          bool
	endedWithNewline bool
}

func newStdoutPresenter() *stdoutPresenter {
	return &stdoutPresenter{}
}

func (p *stdoutPresenter) StreamText(text string) {
	_, _ = fmt.Fprint(os.Stdout, text)
	if text != "" {
		p.mu.Lock()
		p.sawText = true
		p.endedWithNewline = strings.HasSuffix(text, "\n")
		p.mu.Unlock()
	}
}

// Finish terminates the streamed answer with a newline when it lacks one —
// shell capture must never glue output onto the next line.
func (p *stdoutPresenter) Finish() {
	p.mu.Lock()
	needNewline := p.sawText && !p.endedWithNewline
	p.mu.Unlock()
	if needNewline {
		_, _ = fmt.Fprintln(os.Stdout)
	}
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

func (p *stdoutPresenter) SubAgentActivity(string, string, string, string, map[string]any, string) {
}
