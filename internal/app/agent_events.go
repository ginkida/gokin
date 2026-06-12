package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	appcontext "gokin/internal/context"
	"gokin/internal/hooks"
	"gokin/internal/logging"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

// agentPresenter is WHERE agent output goes — the TUI program in interactive
// mode, stdout in headless mode. It owns ONLY presentation. Everything else
// the execution handler does — heartbeat, token estimation, response
// metadata, journal events, recovery snapshots, plan-step effects — is
// shared bookkeeping that lives in buildExecutionHandler regardless of the
// presenter, so the two modes can never drift apart again (pre-unification,
// the headless handler was a hand-maintained ~80% copy of the builder's).
type agentPresenter interface {
	StreamText(text string)
	StreamThinking(text string)
	// StreamTokenEstimate fires every ~500 streamed chars with the running
	// output-token estimate.
	StreamTokenEstimate(estimatedTokens int)
	ToolStart(name string, args map[string]any)
	ToolEnd(name string, args map[string]any, result tools.ToolResult)
	ToolProgress(name string, elapsed time.Duration, currentStep string)
	ToolDetailedProgress(name string, progress float64, currentStep string)
	ToolError(err error)
	Warning(warning string)
	InlineDiff(filePath, oldText, newText string)
	LoopIteration(iteration, toolsUsed int)
	TokenUsage(inputTokens, maxTokens int, percentUsed float64)
	FilePeek(filePath, title, content, action string)
	MemoryNotify(message string)
	// SubAgentActivity mirrors Runner.onSubAgentActivity: lifecycle
	// (start/complete/failed) and tool events (tool_start/tool_end) of
	// spawned sub-agents.
	SubAgentActivity(agentID, agentType, prompt, toolName string, args map[string]any, status string)
}

// currentPresenter returns the active presenter (never nil — defaults to the
// TUI presenter installed by the builder).
func (a *App) currentPresenter() agentPresenter {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.presenter == nil {
		return &tuiPresenter{app: a}
	}
	return a.presenter
}

// setPresenter swaps WHERE agent output goes. RunHeadless swaps in the
// stdout presenter; the execution handler itself is built once by the
// builder and never replaced — bookkeeping must not depend on the mode.
func (a *App) setPresenter(p agentPresenter) {
	a.mu.Lock()
	a.presenter = p
	a.mu.Unlock()
}

// buildExecutionHandler assembles the executor's ExecutionHandler from the
// SHARED bookkeeping (identical in every mode) plus the SWAPPABLE presenter
// (resolved per event via currentPresenter). projectMemory is owned by the
// builder; passed in so the memory-notify reload keeps working without App
// growing a field for it.
func (a *App) buildExecutionHandler(projectMemory *appcontext.ProjectMemory) *tools.ExecutionHandler {
	// Per-event indirection: each callback resolves the presenter at call
	// time so a headless swap takes effect mid-flight and there is exactly
	// ONE handler for the executor's lifetime.
	present := func() agentPresenter { return a.currentPresenter() }
	return &tools.ExecutionHandler{
		OnText: func(text string) {
			a.touchStepHeartbeat()
			present().StreamText(text)

			// Track streamed text for token estimation
			a.mu.Lock()
			a.streamedChars += len(text)
			chars := a.streamedChars
			// Use content-aware estimation (code/JSON/prose heuristics)
			a.streamedEstimatedTokens += appcontext.EstimateTokens(text)
			estimatedTokens := a.streamedEstimatedTokens
			a.mu.Unlock()

			// Estimated token update every ~500 chars (~125 tokens) for smoother UI
			if chars/500 > (chars-len(text))/500 {
				present().StreamTokenEstimate(estimatedTokens)
			}
		},
		OnThinking: func(text string) {
			a.touchStepHeartbeat()
			logging.Debug("OnThinking callback fired", "text_length", len(text), "text_preview", text[:min(len(text), 80)])
			present().StreamThinking(text)
		},
		OnToolStart: func(name string, args map[string]any) {
			a.touchStepHeartbeat()
			// Track tools used for response metadata
			a.mu.Lock()
			a.responseToolsUsed = append(a.responseToolsUsed, name)
			a.currentToolContext = toolContextSummary(name, args)
			a.mu.Unlock()

			// Record tool usage for pattern learning
			a.recordToolUsage(name)

			// Session memory: track tool call for extraction threshold
			if a.sessionMemory != nil {
				a.sessionMemory.RecordToolCall()
			}

			present().ToolStart(name, args)
			a.journalEvent("tool_start", map[string]any{
				"tool": name,
				"args": args,
			})
			a.saveRecoverySnapshot()

			// Record side effects for active plan step (idempotency guard).
			if a.planManager != nil && a.planManager.IsExecuting() {
				if p := a.planManager.GetCurrentPlan(); p != nil {
					stepID := a.planManager.GetCurrentStepID()
					if stepID > 0 {
						a.captureStepRollbackFromToolArgs(p, stepID, name, args)
						p.RecordStepEffect(stepID, name, args)
						_ = a.planManager.SaveCurrentPlan()
					}
				}
			}
		},
		OnToolEnd: func(name string, args map[string]any, result tools.ToolResult) {
			a.touchStepHeartbeat()
			a.mu.Lock()
			a.currentToolContext = ""
			a.mu.Unlock()

			a.recordResponseTouchedPaths(name, args, result)
			a.recordResponseCommand(name, args, result)
			a.recordResponseEvidence(name, args, result)

			// Live todo checklist: flip items the instant the todo tool runs,
			// instead of only at end-of-turn finalization.
			if name == "todo" && result.Success {
				a.emitTodoUpdate()
			}

			present().ToolEnd(name, args, result)
			a.journalEvent("tool_end", map[string]any{
				"tool":    name,
				"success": result.Success,
			})

			// Refresh token count after each tool completes (context grew)
			go a.refreshTokenCount()
		},
		OnToolProgress: func(name string, elapsed time.Duration) {
			a.touchStepHeartbeat()
			a.mu.Lock()
			ctx := a.currentToolContext
			a.mu.Unlock()
			present().ToolProgress(name, elapsed, ctx)
		},
		OnToolDetailedProgress: func(name string, progress float64, currentStep string) {
			a.touchStepHeartbeat()
			// Rich progress from within tools — update both context and UI
			if currentStep != "" {
				a.mu.Lock()
				a.currentToolContext = currentStep
				a.mu.Unlock()
			}
			present().ToolDetailedProgress(name, progress, currentStep)
		},
		OnError: func(err error) {
			a.journalEvent("tool_error", map[string]any{
				"error": err.Error(),
			})
			present().ToolError(err)
		},
		OnWarning: func(warning string) {
			a.touchStepHeartbeat()
			if warning == "" {
				return
			}
			present().Warning(warning)
		},
		OnInlineDiff: func(filePath, oldText, newText string) {
			present().InlineDiff(filePath, oldText, newText)
		},
		OnLoopIteration: func(iteration int, toolsUsed int) {
			present().LoopIteration(iteration, toolsUsed)
			// Refresh token display between executor rounds so the bar stays current
			a.sendTokenUsageUpdate()
		},
		OnTokenUpdate: func(inputTokens, outputTokens int) {
			// Use input tokens only for context bar — output tokens from this turn
			// become input tokens on the next turn, but the context manager tracks
			// the actual accumulated context more accurately via UpdateTokenCount.
			if inputTokens <= 0 {
				return
			}
			maxTokens := 0
			if a.contextManager != nil {
				if usage := a.contextManager.GetTokenUsage(); usage != nil {
					maxTokens = usage.MaxTokens
				}
			}
			var pct float64
			if maxTokens > 0 {
				pct = float64(inputTokens) / float64(maxTokens)
			}
			present().TokenUsage(inputTokens, maxTokens, pct)
		},
		OnFilePeek: func(filePath, title, content, action string) {
			present().FilePeek(filePath, title, content, action)
		},
		OnMemoryNotify: func(action, summary string) {
			if projectMemory != nil {
				if err := projectMemory.Reload(); err != nil {
					logging.Debug("failed to reload project memory after memory update", "error", err)
				}
			}
			if a.promptBuilder != nil {
				a.promptBuilder.Invalidate()
			}
			msg := "Memory " + action
			if summary != "" {
				if runes := []rune(summary); len(runes) > 50 {
					summary = string(runes[:47]) + "..."
				}
				msg += ": " + summary
			}
			present().MemoryNotify(msg)
		},
	}
}

// tuiPresenter routes agent output to the Bubble Tea program. Every send goes
// through safeSendToProgram (nil-program + shutdown safe); the explicit
// program-nil checks avoid building messages nobody will receive.
type tuiPresenter struct {
	app *App
}

func (p *tuiPresenter) StreamText(text string) {
	if p.app.program != nil {
		p.app.safeSendToProgram(ui.StreamTextMsg(text))
	}
}

func (p *tuiPresenter) StreamThinking(text string) {
	if p.app.program != nil {
		p.app.safeSendToProgram(ui.StreamThinkingMsg(text))
	}
}

func (p *tuiPresenter) StreamTokenEstimate(estimatedTokens int) {
	if p.app.program != nil {
		p.app.safeSendToProgram(ui.StreamTokenUpdateMsg{
			EstimatedOutputTokens: estimatedTokens,
		})
	}
}

func (p *tuiPresenter) ToolStart(name string, args map[string]any) {
	if p.app.program != nil {
		p.app.safeSendToProgram(ui.ToolCallMsg{Name: name, Args: args})
	}
}

func (p *tuiPresenter) ToolEnd(name string, args map[string]any, result tools.ToolResult) {
	if p.app.program != nil {
		p.app.safeSendToProgram(ui.ToolResultMsg{
			Name:    name,
			Args:    args,
			Content: result.Content,
			Failed:  !result.Success,
			Error:   result.Error,
		})
	}
}

func (p *tuiPresenter) ToolProgress(name string, elapsed time.Duration, currentStep string) {
	if p.app.program != nil {
		p.app.safeSendToProgram(ui.ToolProgressMsg{Name: name, Elapsed: elapsed, CurrentStep: currentStep})
	}
}

func (p *tuiPresenter) ToolDetailedProgress(name string, progress float64, currentStep string) {
	if p.app.program != nil {
		p.app.safeSendToProgram(ui.ToolProgressMsg{
			Name:        name,
			Progress:    progress,
			CurrentStep: currentStep,
		})
	}
}

func (p *tuiPresenter) ToolError(err error) {
	if p.app.program != nil {
		p.app.safeSendToProgram(ui.ErrorMsg(err))
	}
}

func (p *tuiPresenter) Warning(warning string) {
	if p.app.program == nil {
		return
	}

	details := map[string]any{}
	lower := strings.ToLower(warning)
	switch {
	case strings.Contains(lower, "loop guard"), strings.Contains(lower, "model may be looping"):
		details["tag"] = "loop-guard"
	case strings.Contains(lower, "tool budget"):
		// Budget fires can repeat across iterations if Kimi keeps
		// producing tool calls after the hint. Tagging collapses
		// the N toasts into one that updates in place.
		details["tag"] = "tool-budget"
	}

	p.app.safeSendToProgram(ui.StatusUpdateMsg{
		Type:    ui.StatusWarning,
		Message: warning,
		Details: details,
	})
}

func (p *tuiPresenter) InlineDiff(filePath, oldText, newText string) {
	if p.app.program != nil {
		p.app.safeSendToProgram(ui.InlineDiffMsg{
			FilePath: filePath,
			OldText:  oldText,
			NewText:  newText,
		})
	}
}

func (p *tuiPresenter) LoopIteration(iteration, toolsUsed int) {
	if p.app.program != nil {
		p.app.safeSendToProgram(ui.LoopIterationMsg{
			Iteration: iteration,
			ToolsUsed: toolsUsed,
		})
	}
}

func (p *tuiPresenter) TokenUsage(inputTokens, maxTokens int, percentUsed float64) {
	if p.app.program != nil {
		p.app.safeSendToProgram(ui.TokenUsageMsg{
			Tokens:      inputTokens,
			MaxTokens:   maxTokens,
			PercentUsed: percentUsed,
			NearLimit:   percentUsed > 0.8,
		})
	}
}

func (p *tuiPresenter) FilePeek(filePath, title, content, action string) {
	if p.app.program != nil {
		p.app.safeSendToProgram(ui.FilePeekMsg{
			FilePath: filePath,
			Title:    title,
			Content:  content,
			Action:   action,
		})
	}
}

func (p *tuiPresenter) MemoryNotify(message string) {
	if p.app.program != nil {
		p.app.safeSendToProgram(ui.LearningInsightMsg{Message: message})
	}
}

// deliverUnstreamedResponse presents response text that no streaming path
// delivered this turn. Direct-executor turns stream live via OnText (which
// bumps streamedChars); minimal-history router strategies (sub-agent,
// coordinated) return the final text as a plain string with streamedChars
// still 0 — pre-unification that text was silently dropped. Idempotent:
// delivering counts the chars, so a second call is a no-op.
func (a *App) deliverUnstreamedResponse(response string) {
	if strings.TrimSpace(response) == "" {
		return
	}
	a.mu.Lock()
	streamed := a.streamedChars
	if streamed == 0 {
		a.streamedChars += len(response)
		a.streamedEstimatedTokens += appcontext.EstimateTokens(response)
	}
	a.mu.Unlock()
	if streamed > 0 {
		return
	}
	a.currentPresenter().StreamText(response)
}

// handleSubAgentActivity is the unified sink for sub-agent events: journal
// bookkeeping (tool events carry agent_id so eval scoring and post-mortems
// see sub-agent work — pre-unification the journal was BLIND to it) plus the
// active presenter. Wired to Runner.SetOnSubAgentActivity by the builder.
func (a *App) handleSubAgentActivity(agentID, agentType, prompt, toolName string, args map[string]any, status string) {
	switch status {
	case "tool_start":
		a.journalEvent("tool_start", map[string]any{
			"tool":     toolName,
			"args":     args,
			"agent_id": agentID,
		})
	case "tool_end":
		a.journalEvent("tool_end", map[string]any{
			"tool":     toolName,
			"agent_id": agentID,
		})
	case "start":
		a.journalEvent("agent_start", map[string]any{
			"agent_id":   agentID,
			"agent_type": agentType,
			"task":       previewForJournal(prompt),
		})
	case "complete", "failed":
		a.journalEvent("agent_end", map[string]any{
			"agent_id": agentID,
			"status":   status,
		})
	}

	a.currentPresenter().SubAgentActivity(agentID, agentType, prompt, toolName, args, status)
}

func (p *tuiPresenter) SubAgentActivity(agentID, agentType, prompt, toolName string, args map[string]any, status string) {
	if p.app.program != nil {
		p.app.safeSendToProgram(ui.SubAgentActivityMsg{
			AgentID:   agentID,
			AgentType: agentType,
			Task:      prompt,
			ToolName:  toolName,
			ToolArgs:  args,
			Status:    status,
		})
	}
}

// runStopHooks fires end-of-turn hooks (hooks.Stop). A FailOnError stop hook
// that exits non-zero asks the agent to CONTINUE: its output is enqueued as
// the next message (type-ahead pending queue). Bounded: at most ONE
// hook-driven continuation per user-initiated turn — stopHookActive marks
// the continuation turn, and stop hooks are skipped at ITS end, so a hook
// that always fails cannot loop the agent forever. Headless runs only
// journal the outcome (one-shot semantics — there is no next turn).
func (a *App) runStopHooks(ctx context.Context, response string) {
	if a.hooksManager == nil {
		return
	}

	a.mu.Lock()
	wasContinuation := a.stopHookActive
	a.stopHookActive = false
	headless := a.headlessDirect
	a.mu.Unlock()

	if wasContinuation {
		// This turn IS the hook-driven continuation — don't re-gate it.
		return
	}

	results := a.hooksManager.RunStop(ctx, response)
	for _, r := range results {
		if r.Error != nil {
			a.journalEvent("stop_hook", map[string]any{
				"hook":  r.Hook.Name,
				"error": r.Error.Error(),
			})
		}
	}

	blocked, ok := hooks.Blocked(results)
	if !ok {
		return
	}
	name := blocked.Hook.Name
	if name == "" {
		name = blocked.Hook.Command
	}
	reason := strings.TrimSpace(blocked.Output)
	if reason == "" && blocked.Error != nil {
		reason = blocked.Error.Error()
	}

	if headless {
		// One-shot mode: surface the verdict, no continuation turn exists.
		a.currentPresenter().Warning(fmt.Sprintf("Stop hook %q failed: %s", name, reason))
		return
	}

	followUp := fmt.Sprintf("[Stop hook %q asks you to continue] %s", name, reason)
	a.mu.Lock()
	a.stopHookActive = true
	a.mu.Unlock()
	if _, ok := a.enqueuePending(followUp); !ok {
		// Queue full — drop the continuation rather than displace user input.
		a.mu.Lock()
		a.stopHookActive = false
		a.mu.Unlock()
		a.currentPresenter().Warning(fmt.Sprintf("Stop hook %q wanted a continuation but the queue is full", name))
		return
	}
	a.currentPresenter().Warning(fmt.Sprintf("Stop hook %q: continuing — %s", name, truncateRunesApp(reason, 120)))
}

// truncateRunesApp rune-safely truncates s to max runes for toast display.
func truncateRunesApp(s string, max int) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	runes := []rune(s)
	if max <= 0 || len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
