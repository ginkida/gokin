package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	appcontext "gokin/internal/context"
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

	finishOutput := a.installHeadlessHandler()
	a.prepareHeadlessRuntime()

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

// installHeadlessHandler wires a stdout-streaming execution handler and
// returns a finalizer that terminates the streamed answer with a newline so
// shell capture and JSONL post-processing never glue the model output onto
// the next line. The trailing-newline state is guarded by a.mu, same as the
// streamed-token counters.
func (a *App) installHeadlessHandler() func() {
	sawText := false
	endedWithNewline := false

	a.executor.SetHandler(&tools.ExecutionHandler{
		OnText: func(text string) {
			a.touchStepHeartbeat()
			_, _ = fmt.Fprint(os.Stdout, text)

			a.mu.Lock()
			a.streamedChars += len(text)
			a.streamedEstimatedTokens += appcontext.EstimateTokens(text)
			if text != "" {
				sawText = true
				endedWithNewline = strings.HasSuffix(text, "\n")
			}
			a.mu.Unlock()
		},
		OnThinking: func(text string) {
			a.touchStepHeartbeat()
		},
		OnToolStart: func(name string, args map[string]any) {
			a.touchStepHeartbeat()
			a.mu.Lock()
			a.responseToolsUsed = append(a.responseToolsUsed, name)
			a.currentToolContext = toolContextSummary(name, args)
			a.mu.Unlock()
			a.recordToolUsage(name)
			if a.sessionMemory != nil {
				a.sessionMemory.RecordToolCall()
			}
			a.journalEvent("tool_start", map[string]any{
				"tool": name,
				"args": args,
			})
			a.saveRecoverySnapshot()
		},
		OnToolEnd: func(name string, args map[string]any, result tools.ToolResult) {
			a.touchStepHeartbeat()
			a.mu.Lock()
			a.currentToolContext = ""
			a.mu.Unlock()
			a.recordResponseTouchedPaths(name, args, result)
			a.recordResponseCommand(name, args, result)
			a.recordResponseEvidence(name, args, result)
			if name == "todo" && result.Success {
				a.emitTodoUpdate()
			}
			a.journalEvent("tool_end", map[string]any{
				"tool":    name,
				"success": result.Success,
			})
		},
		OnToolProgress: func(name string, elapsed time.Duration) {
			a.touchStepHeartbeat()
		},
		OnToolDetailedProgress: func(name string, progress float64, currentStep string) {
			a.touchStepHeartbeat()
			if currentStep != "" {
				a.mu.Lock()
				a.currentToolContext = currentStep
				a.mu.Unlock()
			}
		},
		OnError: func(err error) {
			if err != nil {
				a.journalEvent("tool_error", map[string]any{"error": err.Error()})
			}
		},
		OnWarning: func(warning string) {
			a.touchStepHeartbeat()
		},
		OnInlineDiff: func(filePath, oldText, newText string) {},
		OnLoopIteration: func(iteration int, toolsUsed int) {
			a.touchStepHeartbeat()
		},
		OnTokenUpdate: func(inputTokens, outputTokens int) {},
	})

	return func() {
		a.mu.Lock()
		needNewline := sawText && !endedWithNewline
		a.mu.Unlock()
		if needNewline {
			_, _ = fmt.Fprintln(os.Stdout)
		}
	}
}
