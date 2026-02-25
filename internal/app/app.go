package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"google.golang.org/genai"

	"gokin/internal/agent"
	"gokin/internal/audit"
	"gokin/internal/cache"
	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/commands"
	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/git"
	"gokin/internal/hooks"
	"gokin/internal/logging"
	"gokin/internal/mcp"
	"gokin/internal/permission"
	"gokin/internal/plan"
	"gokin/internal/ratelimit"
	"gokin/internal/router"
	"gokin/internal/semantic"
	"gokin/internal/tasks"
	"gokin/internal/tools"
	"gokin/internal/ui"
	"gokin/internal/undo"
	"gokin/internal/watcher"
)

// SystemPrompt is the default system prompt for the assistant.
const SystemPrompt = `You are Gokin, an AI assistant for software development. You help users work with code by:
- Reading and understanding code files
- Writing and editing code
- Running shell commands
- Searching for files and content
- Managing tasks

You have access to the following tools:
- read: Read file contents with line numbers
- write: Create or overwrite files
- edit: Search and replace text in files
- bash: Execute shell commands
- glob: Find files matching patterns
- grep: Search file contents with regex
- todo: Track tasks and progress
- diff: Compare files and show differences
- tree: Display directory structure
- env: Check environment variables

CRITICAL RESPONSE GUIDELINES:
1. **ALWAYS provide a direct answer** to the user's question after using tools
2. **NEVER just read files silently** - always explain what you found
3. **Be specific and actionable** - give concrete recommendations
4. **Structure your response**:
   - First: Direct answer to the question
   - Then: Evidence from code (what you read)
   - Finally: Specific suggestions or next steps
5. **If analyzing code**: summarize key points, highlight issues, suggest improvements
6. **If asked to explain**: break down complex concepts clearly
7. **Use markdown formatting** for better readability (code blocks, lists, headers)
8. **After using ANY tool** (read, list_dir, grep, etc.) you MUST provide a response summarizing what you found
9. **Even if the tool returns empty results**, explain what that means
10. **Never leave a conversation hanging** - always conclude with a clear answer or question

Examples of GOOD responses:
- "This code does X. I noticed 3 potential issues: 1)... 2)... 3)..."
- "Based on the files I read, here's what I found: [summary]. Suggestions: [list]"
- "The architecture uses [pattern]. Main components are: [list]. To improve: [suggestions]"
- "I listed the directory and found: [files]. Here's the project structure: [analysis]"
- "The search returned [results]. This means: [conclusion]"

Examples of BAD responses (avoid these):
- Reading files and saying nothing
- "OK" or "Done" without explanation
- Just listing files without analysis
- Using tools without providing ANY response afterward
- Calling list_dir/read/grep and then stopping

Additional Guidelines:
- Always read files before editing them
- Use the todo tool to track multi-step tasks
- Prefer editing existing files over creating new ones
- When executing commands, explain what they do
- Handle errors gracefully and suggest fixes

The user's working directory is: %s`

// App is the main application orchestrator.
type App struct {
	config   *config.Config
	workDir  string
	client   client.Client
	registry *tools.Registry
	executor *tools.Executor
	session  *chat.Session
	tui      *ui.Model
	program  *tea.Program

	// Application context for cancellation
	ctx    context.Context
	cancel context.CancelFunc

	// Context management
	projectInfo    *appcontext.ProjectInfo
	contextManager *appcontext.ContextManager
	promptBuilder  *appcontext.PromptBuilder
	contextAgent   *appcontext.ContextAgent

	// Permission management
	permManager      *permission.Manager
	permResponseChan chan permission.Decision

	// Question handling
	questionResponseChan chan string

	// Diff preview handling
	diffResponseChan  chan ui.DiffDecision
	diffBatchDecision ui.DiffDecision

	// Plan management
	planManager      *plan.Manager
	planApprovalChan chan plan.ApprovalDecision

	// Hooks management
	hooksManager *hooks.Manager

	// Task management
	taskManager *tasks.Manager

	// Undo management
	undoManager *undo.Manager

	// Agent management
	agentRunner *agent.Runner

	// Command handler
	commandHandler *commands.Handler

	// Token tracking
	totalInputTokens  int
	totalOutputTokens int

	// Response metadata tracking
	responseStartTime time.Time
	responseToolsUsed []string

	// Session persistence
	sessionManager *chat.SessionManager

	// New feature integrations
	searchCache       *cache.SearchCache
	rateLimiter       *ratelimit.Limiter
	auditLogger       *audit.Logger
	fileWatcher       *watcher.Watcher
	semanticIndexer   *semantic.EnhancedIndexer
	backgroundIndexer *semantic.BackgroundIndexer

	// Task router for intelligent task routing
	taskRouter *router.Router

	// Agent Scratchpad (shared)
	scratchpad string

	// Unified Task Orchestrator
	orchestrator *TaskOrchestrator
	reliability  *ReliabilityManager
	policy       *PolicyEngine

	uiUpdateManager *UIUpdateManager // Coordinates periodic UI updates

	// === PHASE 5: Agent System Improvements (6→10) ===
	coordinator       *agent.Coordinator       // Task orchestration
	agentTypeRegistry *agent.AgentTypeRegistry // Dynamic agent types
	strategyOptimizer *agent.StrategyOptimizer // Strategy learning
	metaAgent         *agent.MetaAgent         // Agent monitoring

	// === PHASE 6: Tree Planner ===
	treePlanner         *agent.TreePlanner // Tree-based planning
	planningModeEnabled bool               // toggle for planning mode

	// MCP (Model Context Protocol)
	mcpManager *mcp.Manager

	// Streaming token estimation
	streamedChars int // Accumulated chars during current streaming session

	// === Task 5.7: Project Context Auto-Injection ===
	detectedProjectContext string // Computed once at startup

	// === Task 5.8: Tool Usage Pattern Learning ===
	toolPatterns []toolPattern // Detected repeating tool sequences
	recentTools  []string      // Last 20 tool names used
	messageCount int           // Total messages processed (for periodic hint injection)

	// Current tool context for progress bar display
	currentToolContext string

	// Error context for retry awareness
	lastError     string    // Last error message for context on retry
	lastErrorTime time.Time // When the last error occurred

	mu         sync.Mutex
	running    bool
	processing bool // Guards against concurrent message processing

	// Processing cancellation for ESC interrupt
	processingCancel context.CancelFunc
	processingMu     sync.Mutex

	// Plan execution watchdog
	stepHeartbeatMu   sync.RWMutex
	lastStepHeartbeat time.Time

	// Step rollback snapshots (rollback-first guardrail)
	stepRollbackMu        sync.Mutex
	stepRollbackSnapshots map[string]*stepRollbackSnapshot

	// Execution journal and recovery
	journal *ExecutionJournal

	// Session memory governance
	sessionArchiveMu         sync.Mutex
	sessionArchivedMessages  int
	sessionArchiveOperations int
	lastSessionArchive       time.Time

	// Signal handler cleanup
	signalCleanup func()

	// Session pre-load flag (set by ResumeLastSession before Run)
	sessionPreloaded bool

	// Pending message queue
	pendingMessage string
	pendingMu      sync.Mutex

	// Automatic retry tracking for rate-limit failures.
	rateLimitRetryMu    sync.Mutex
	rateLimitRetryCount map[string]int
}

// toolPattern, detectPatterns, getToolHints, recordToolUsage are in pattern_detector.go
// detectProjectContext, extractGoModInfo, extractPackageJSONInfo, readFirstLines, fileExists, readFileHead are in project_detector.go

// New creates a new application instance.
func New(cfg *config.Config, workDir string) (*App, error) {
	return NewBuilder(cfg, workDir).Build()
}

// Run starts the application.
func (a *App) Run() error {
	// NOTE: Allowed dirs prompt is now done in Builder.checkAllowedDirs()
	// before tool creation, so PathValidator gets correct directories.

	// Configure logging to file to avoid TUI interference
	configDir, err := appcontext.GetConfigDir()
	if err == nil && a.config.Logging.Level != "" {
		level := logging.ParseLevel(a.config.Logging.Level)
		if err := logging.EnableFileLogging(configDir, level); err != nil {
			// Silently continue with logging disabled
			logging.DisableLogging()
		}
	} else {
		// Disable logging if no config dir or level not set
		logging.DisableLogging()
	}

	// === Task 5.7: Detect project context once at startup ===
	a.detectedProjectContext = a.detectProjectContext()
	if a.detectedProjectContext != "" && a.promptBuilder != nil {
		a.promptBuilder.SetDetectedContext(a.detectedProjectContext)
		logging.Debug("project context auto-detected", "length", len(a.detectedProjectContext))
	}

	// Run on_start hooks with proper context
	if a.hooksManager != nil {
		a.hooksManager.RunOnStart(a.ctx)
	}

	// Load input history
	if err := a.tui.LoadInputHistory(); err != nil {
		logging.Debug("failed to load input history", "error", err)
	}

	// Auto-load previous session if enabled (skip if already pre-loaded via ResumeLastSession)
	var sessionRestored bool
	if a.sessionPreloaded {
		sessionRestored = true
	} else if a.sessionManager != nil {
		state, info, err := a.sessionManager.LoadLast()
		if err == nil && state != nil {
			// Check if session has any content
			if len(state.History) > 0 {
				if restoreErr := a.sessionManager.RestoreFromState(state); restoreErr != nil {
					logging.Warn("failed to restore session", "error", restoreErr)
				} else {
					sessionRestored = true
					// Sync scratchpad from restored session
					a.scratchpad = a.session.GetScratchpad()
					if a.agentRunner != nil {
						a.agentRunner.SetSharedScratchpad(a.scratchpad)
					}
					// Notify TUI about restored scratchpad
					a.safeSendToProgram(ui.ScratchpadMsg(a.scratchpad))

					// Notify user about restored session
					a.tui.AddSystemMessage(fmt.Sprintf("Restored session from %s (%d messages)",
						info.LastActive.Format("2006-01-02 15:04"), len(state.History)))
				}
			}
		}
	}

	// Build model-specific enhancement
	modelEnhancement := a.buildModelEnhancement()

	// Set system instruction via native API parameter (not as user message)
	if !sessionRestored {
		systemPrompt := a.promptBuilder.Build()
		systemPrompt += modelEnhancement
		a.client.SetSystemInstruction(systemPrompt)
		a.session.SystemInstruction = systemPrompt
	} else {
		// Restored session: clean up legacy system prompt messages from history
		a.stripLegacySystemMessages()

		// Use saved system instruction or rebuild
		if a.session.SystemInstruction != "" {
			a.client.SetSystemInstruction(a.session.SystemInstruction)
		} else {
			// Legacy session without SystemInstruction — rebuild
			systemPrompt := a.promptBuilder.Build()
			systemPrompt += modelEnhancement
			a.client.SetSystemInstruction(systemPrompt)
			a.session.SystemInstruction = systemPrompt
		}
	}

	// Start session manager for periodic saves
	if a.sessionManager != nil {
		a.sessionManager.Start(a.ctx)
	}

	// Save session on panic before re-panicking
	defer func() {
		if r := recover(); r != nil {
			if a.sessionManager != nil {
				_ = a.sessionManager.Save()
			}
			panic(r)
		}
	}()

	// Show one-time onboarding welcome on first launch.
	if a.config.UI.ShowWelcome {
		a.tui.Welcome()
		a.tui.AddSystemMessage("Getting started:\n• /quickstart for examples\n• /doctor to verify setup\n• /plan to enable step-by-step execution\n• /update to check for new versions")
		a.config.UI.ShowWelcome = false
		if err := a.config.Save(); err != nil {
			logging.Warn("failed to persist onboarding welcome state", "error", err)
		}
	}
	a.journalEvent("app_started", map[string]any{
		"workdir": a.workDir,
	})

	// Check for paused plans and notify user
	if a.planManager != nil && a.planManager.HasPausedPlan() {
		plans, err := a.planManager.ListResumablePlans()
		if err == nil && len(plans) > 0 {
			// Show notification about resumable plan
			latestPlan := plans[0] // Most recent
			msg := fmt.Sprintf("Paused plan found: %s (%d/%d steps complete)\nUse /resume-plan to continue.",
				latestPlan.Title, latestPlan.Completed, latestPlan.StepCount)
			a.tui.AddSystemMessage(msg)
			logging.Info("paused plan available for resume",
				"plan_id", latestPlan.ID,
				"title", latestPlan.Title,
				"progress", fmt.Sprintf("%d/%d", latestPlan.Completed, latestPlan.StepCount))
		}
	}

	// Show recovery hint if previous run was interrupted mid-processing.
	if a.journal != nil {
		if snap, err := a.journal.LoadRecovery(); err == nil && snap != nil && snap.Processing {
			msg := fmt.Sprintf("Recovery snapshot found from %s.\nProcessing was interrupted with %d messages in session.\nUse /recovery and /journal for details.",
				snap.Timestamp.Format("2006-01-02 15:04"), snap.HistoryLen)
			a.tui.AddSystemMessage(msg)
		}
	}

	// Auto-resume agents from error checkpoints (silent, debug log only)
	if a.agentRunner != nil {
		a.agentRunner.ResumeErrorCheckpoints(a.ctx)
	}

	// Create and run the program
	a.program = a.tui.GetProgram()

	a.mu.Lock()
	a.running = true
	a.mu.Unlock()

	// === PHASE 4: Initialize UI Auto-Update System ===
	a.initializeUIUpdateSystem()

	// Start background processes
	if a.orchestrator != nil {
		go a.orchestrator.Start(a.ctx)
	}
	if a.contextAgent != nil {
		go a.contextAgent.Start(a.ctx)
	}

	// Set app reference in TUI for data providers
	a.tui.SetApp(a)

	// Set up signal handling for graceful shutdown
	a.signalCleanup = a.setupSignalHandler()

	// Start periodic task cleanup goroutine
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if a.taskManager != nil {
					cleaned := a.taskManager.Cleanup(30 * time.Minute)
					if cleaned > 0 {
						logging.Debug("cleaned up completed tasks", "count", cleaned)
					}
				}
				// Clean up old agent checkpoint files
				if a.agentRunner != nil {
					a.agentRunner.CleanupOldCheckpoints(24 * time.Hour)
				}
			case <-a.ctx.Done():
				return
			}
		}
	}()

	// Start file watcher if enabled
	if a.fileWatcher != nil {
		a.fileWatcher.SetOnFileChange(func(path string, op watcher.Operation) {
			// Invalidate cache on file changes
			if a.searchCache != nil {
				a.searchCache.InvalidateByPath(path)
			}
		})
		if err := a.fileWatcher.Start(); err != nil {
			logging.Warn("failed to start file watcher", "error", err)
		}
	}

	// Start background semantic indexer (watcher-driven incremental indexing)
	if a.backgroundIndexer != nil {
		if err := a.backgroundIndexer.Start(); err != nil {
			logging.Warn("failed to start background semantic indexer", "error", err)
		}
	}

	// Start initial indexing for semantic search if enabled
	if a.semanticIndexer != nil && a.config.Semantic.IndexOnStart {
		go func() {
			logging.Debug("starting background semantic indexing")

			// Use LoadOrIndex for intelligent loading from cache
			maxAge := 24 * time.Hour // Cache is fresh for 24 hours
			if err := a.semanticIndexer.LoadOrIndex(a.ctx, true, maxAge); err != nil {
				logging.Error("semantic indexing failed", "error", err)
			} else {
				stats := a.semanticIndexer.GetStats()
				logging.Debug("semantic indexing complete",
					"files", stats.FileCount,
					"chunks", stats.ChunkCount)
			}
		}()
	}

	_, runErr := a.program.Run()
	a.tui.Cleanup()

	a.mu.Lock()
	a.running = false
	a.mu.Unlock()

	// === PHASE 4: Stop UI Auto-Update System ===
	if a.uiUpdateManager != nil {
		a.uiUpdateManager.Stop()
		logging.Debug("UI update manager stopped")
	}

	// Stop session manager
	if a.sessionManager != nil {
		a.sessionManager.Stop()
	}

	return runErr
}

// handleSubmit handles user message submission.
func (a *App) handleSubmit(message string) {
	a.mu.Lock()
	if a.processing {
		// Copy program reference while holding lock
		program := a.program
		a.mu.Unlock()

		// Save as pending message (replaces any previous pending)
		a.pendingMu.Lock()
		if a.pendingMessage != "" {
			logging.Debug("pending message replaced — previous message dropped",
				"dropped_len", len(a.pendingMessage))
			if program != nil {
				program.Send(ui.StreamTextMsg("⚠ Previous queued message was replaced by new input\n"))
			}
		}
		a.pendingMessage = message
		a.pendingMu.Unlock()
		a.journalEvent("request_queued", map[string]any{
			"message_preview": previewForJournal(message),
		})
		a.saveRecoverySnapshot(message)

		// Notify user that message is queued (using copied reference)
		if program != nil {
			program.Send(ui.StreamTextMsg("📥 Message queued - will process after current request completes\n"))
		}
		return
	}
	a.processing = true

	// Parse command BEFORE unlocking to avoid race condition
	// (parsing is fast and doesn't need to be concurrent)
	name, args, isCmd := a.commandHandler.Parse(message)
	a.mu.Unlock()

	// Journal and recovery AFTER unlock (saveRecoverySnapshot takes a.mu internally)
	a.journalEvent("request_accept", map[string]any{
		"message_preview": previewForJournal(message),
	})
	a.saveRecoverySnapshot("")

	// Now safely start the goroutine
	if isCmd {
		go a.executeCommand(name, args)
		return
	}

	// Create cancelable context for this request
	a.processingMu.Lock()
	ctx, cancel := context.WithCancel(a.ctx)
	a.processingCancel = cancel
	a.processingMu.Unlock()

	// Process message normally (coordinator is now integrated in agent system)
	go func() {
		defer func() {
			a.processingMu.Lock()
			a.processingCancel = nil
			a.processingMu.Unlock()
		}()
		a.processMessageWithContext(ctx, message)
	}()
}

// executeCommand executes a slash command.
func (a *App) executeCommand(name string, args []string) {
	defer func() {
		a.mu.Lock()
		a.processing = false
		a.mu.Unlock()
		a.saveRecoverySnapshot("")
	}()

	a.mu.Lock()
	a.diffBatchDecision = ui.DiffPending
	a.mu.Unlock()
	a.journalEvent("command_started", map[string]any{
		"command": name,
		"args":    args,
	})

	ctx := a.ctx
	result, err := a.commandHandler.Execute(ctx, name, args, a)

	// Copy program reference under lock for safe access
	a.mu.Lock()
	program := a.program
	a.mu.Unlock()

	if program != nil {
		if err != nil {
			a.journalEvent("command_failed", map[string]any{
				"command": name,
				"error":   err.Error(),
			})
			program.Send(ui.ErrorMsg(err))
		} else {
			a.journalEvent("command_completed", map[string]any{
				"command": name,
			})
			// Handle special command markers
			if strings.HasPrefix(result, "__browse:") {
				browsePath := strings.TrimPrefix(result, "__browse:")
				program.Send(ui.FileBrowserRequestMsg{StartPath: browsePath})
			} else {
				// Display command result as assistant message
				program.Send(ui.StreamTextMsg(result))
			}
		}
		program.Send(ui.ResponseDoneMsg{})
	}
}

// handleQuit handles quit request.
func (a *App) handleQuit() {
	a.mu.Lock()
	a.processing = false
	a.mu.Unlock()
	a.saveRecoverySnapshot("")
	a.journalEvent("app_quit", nil)

	// Use graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), GracefulShutdownTimeout)
	defer cancel()
	a.gracefulShutdown(ctx)
}

// processMessage processes a user message asynchronously (uses app context).
func (a *App) processMessage(message string) {
	a.processMessageWithContext(a.ctx, message)
}

// processMessageWithContext and related methods are in message_processor.go

// GetPlanManager returns the plan manager.
func (a *App) GetPlanManager() *plan.Manager {
	return a.planManager
}

// GetTreePlanner returns the tree planner.
func (a *App) GetTreePlanner() *agent.TreePlanner {
	return a.treePlanner
}

// GetRuntimeHealthReport returns runtime reliability and provider health diagnostics.
func (a *App) GetRuntimeHealthReport() string {
	var sb strings.Builder
	sb.WriteString("Runtime health:\n")

	if a.reliability != nil {
		s := a.reliability.Snapshot()
		mode := "normal"
		if s.Degraded {
			mode = fmt.Sprintf("degraded (%v remaining)", s.DegradedRemaining)
		}
		sb.WriteString(fmt.Sprintf("- mode: %s\n", mode))
		sb.WriteString(fmt.Sprintf("- consecutive_failures: %d\n", s.ConsecutiveFailures))
		sb.WriteString(fmt.Sprintf("- window_failures: %d (start: %s)\n",
			s.WindowFailures, s.WindowStartedAt.Format("2006-01-02 15:04:05")))
	}

	age := a.stepHeartbeatAge()
	if age > 0 {
		sb.WriteString(fmt.Sprintf("- step_heartbeat_age: %v\n", age.Round(time.Second)))
	} else {
		sb.WriteString("- step_heartbeat_age: n/a\n")
	}

	sb.WriteString("\n")
	sb.WriteString(client.GetProviderHealthReport())
	return sb.String()
}

// GetUIRuntimeStatus returns a compact runtime snapshot for TUI status bar rendering.
func (a *App) GetUIRuntimeStatus() ui.RuntimeStatusSnapshot {
	out := ui.RuntimeStatusSnapshot{
		Mode:           "normal",
		RequestBreaker: "n/a",
		StepBreaker:    "n/a",
	}

	if a.config != nil {
		out.Provider = a.config.API.GetActiveProvider()
	}
	if a.reliability != nil {
		s := a.reliability.Snapshot()
		if s.Degraded {
			out.Mode = "degraded"
			out.DegradedRemaining = s.DegradedRemaining
		}
		out.ConsecutiveFailure = s.ConsecutiveFailures
	}
	if a.policy != nil {
		s := a.policy.Snapshot()
		if s.RequestBreakerState != "" {
			out.RequestBreaker = s.RequestBreakerState
		}
		if s.StepBreakerState != "" {
			out.StepBreaker = s.StepBreakerState
		}
	}

	age := a.stepHeartbeatAge()
	if age > 0 {
		out.HasHeartbeat = true
		out.HeartbeatAge = age
	}

	return out
}

// GetPolicyReport returns current policy engine state.
func (a *App) GetPolicyReport() string {
	if a.policy == nil {
		return "Policy engine not initialized."
	}
	s := a.policy.Snapshot()
	return fmt.Sprintf("Policy engine:\n- request_breaker: %s\n- step_breaker: %s",
		s.RequestBreakerState, s.StepBreakerState)
}

// GetLedgerReport returns run ledger details for the active plan.
func (a *App) GetLedgerReport() string {
	if a.planManager == nil {
		return "Ledger unavailable: plan manager not initialized."
	}
	p := a.planManager.GetCurrentPlan()
	if p == nil {
		return "Ledger: no active plan."
	}

	steps := p.GetStepsSnapshot()
	ledger := p.GetRunLedgerSnapshot()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Run ledger for %s (%s)\n", p.Title, p.ID))

	for _, step := range steps {
		entry := ledger[step.ID]
		if entry == nil {
			sb.WriteString(fmt.Sprintf("- step %d (%s): no side effects recorded\n", step.ID, step.Title))
			continue
		}
		status := "incomplete"
		if entry.Completed {
			status = "completed"
		} else if entry.PartialEffects {
			status = "partial_effects"
		}
		sb.WriteString(fmt.Sprintf(
			"- step %d (%s): %s, tool_calls=%d, files=%d, commands=%d, duplicates=%d\n",
			step.ID, step.Title, status, entry.ToolCalls, len(entry.FilesTouched), len(entry.Commands), entry.DuplicateEffects,
		))
	}

	return sb.String()
}

// GetPlanProofReport returns contract/evidence proof diagnostics for one plan step.
// stepID: 0 means auto-select current/most-recent actionable step.
func (a *App) GetPlanProofReport(stepID int) string {
	if a.planManager == nil {
		return "Plan proof unavailable: plan manager not initialized."
	}
	p := a.planManager.GetCurrentPlan()
	if p == nil {
		return "Plan proof: no active plan."
	}

	steps := p.GetStepsSnapshot()
	if len(steps) == 0 {
		return fmt.Sprintf("Plan proof: active plan %s has no steps.", p.ID)
	}
	target := selectPlanProofStep(steps, a.planManager.GetCurrentStepID(), stepID)
	if target == nil {
		return fmt.Sprintf("Plan proof: step %d not found.", stepID)
	}

	ledger := p.GetRunLedgerSnapshot()
	entry := ledger[target.ID]

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Plan proof for %s (%s)\n", p.Title, p.ID))
	sb.WriteString(fmt.Sprintf("Step %d: %s\n", target.ID, target.Title))
	sb.WriteString(fmt.Sprintf("- status: %s\n", target.Status.String()))
	if !target.StartTime.IsZero() {
		sb.WriteString(fmt.Sprintf("- started: %s\n", target.StartTime.Format("2006-01-02 15:04:05")))
	}
	if !target.EndTime.IsZero() {
		sb.WriteString(fmt.Sprintf("- ended: %s\n", target.EndTime.Format("2006-01-02 15:04:05")))
	}
	if strings.TrimSpace(target.Error) != "" {
		sb.WriteString(fmt.Sprintf("- error: %s\n", target.Error))
	}

	sb.WriteString("\nContract:\n")
	if len(target.Inputs) > 0 {
		sb.WriteString(fmt.Sprintf("- inputs: %s\n", strings.Join(target.Inputs, "; ")))
	}
	if ea := strings.TrimSpace(target.ExpectedArtifact); ea != "" {
		sb.WriteString(fmt.Sprintf("- expected_artifact: %s\n", ea))
	}
	if len(target.ExpectedArtifactPaths) > 0 {
		sb.WriteString(fmt.Sprintf("- expected_artifact_paths: %s\n", strings.Join(target.ExpectedArtifactPaths, ", ")))
	} else {
		sb.WriteString("- expected_artifact_paths: (none)\n")
	}
	if len(target.SuccessCriteria) > 0 {
		sb.WriteString(fmt.Sprintf("- success_criteria: %s\n", strings.Join(target.SuccessCriteria, "; ")))
	}
	if len(target.VerifyCommands) > 0 {
		sb.WriteString(fmt.Sprintf("- verify_commands: %s\n", strings.Join(target.VerifyCommands, "; ")))
	}
	if rb := strings.TrimSpace(target.Rollback); rb != "" {
		sb.WriteString(fmt.Sprintf("- rollback: %s\n", rb))
	}

	sb.WriteString("\nProof:\n")
	if note := strings.TrimSpace(target.VerificationNote); note != "" {
		sb.WriteString(fmt.Sprintf("- verification_note: %s\n", note))
	} else {
		sb.WriteString("- verification_note: (empty)\n")
	}
	sb.WriteString(fmt.Sprintf("- evidence_items: %d\n", len(target.Evidence)))
	for _, evidence := range target.Evidence {
		evidence = strings.TrimSpace(evidence)
		if evidence == "" {
			continue
		}
		if strings.HasPrefix(evidence, "proof_json=") {
			raw := strings.TrimSpace(strings.TrimPrefix(evidence, "proof_json="))
			if pretty := prettyProofJSON(raw); pretty != "" {
				sb.WriteString("- proof_json:\n")
				sb.WriteString(pretty)
				sb.WriteString("\n")
				continue
			}
		}
		sb.WriteString("- ")
		sb.WriteString(evidence)
		sb.WriteString("\n")
	}

	sb.WriteString("\nRun ledger:\n")
	if entry == nil {
		sb.WriteString("- no ledger entry for step\n")
	} else {
		sb.WriteString(fmt.Sprintf("- tool_calls: %d\n", entry.ToolCalls))
		sb.WriteString(fmt.Sprintf("- tools: %d\n", len(entry.Tools)))
		if len(entry.Tools) > 0 {
			sb.WriteString("  ")
			sb.WriteString(strings.Join(entry.Tools, ", "))
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("- files_touched: %d\n", len(entry.FilesTouched)))
		if len(entry.FilesTouched) > 0 {
			sb.WriteString("  ")
			sb.WriteString(strings.Join(entry.FilesTouched, ", "))
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("- commands: %d\n", len(entry.Commands)))
		if len(entry.Commands) > 0 {
			for _, cmd := range entry.Commands {
				sb.WriteString("  - ")
				sb.WriteString(cmd)
				sb.WriteString("\n")
			}
		}
		sb.WriteString(fmt.Sprintf("- duplicate_effects: %d\n", entry.DuplicateEffects))
		sb.WriteString(fmt.Sprintf("- partial_effects: %t\n", entry.PartialEffects))
		sb.WriteString(fmt.Sprintf("- completed: %t\n", entry.Completed))
	}

	if a.config != nil && a.config.Plan.RequireExpectedArtifactPaths {
		sb.WriteString("\nStrict mode:\n- require_expected_artifact_paths: enabled\n")
	} else {
		sb.WriteString("\nStrict mode:\n- require_expected_artifact_paths: disabled\n")
	}

	return sb.String()
}

func selectPlanProofStep(steps []*plan.Step, currentStepID int, requestedStepID int) *plan.Step {
	if requestedStepID > 0 {
		for _, step := range steps {
			if step != nil && step.ID == requestedStepID {
				return step
			}
		}
		return nil
	}
	if currentStepID > 0 {
		for _, step := range steps {
			if step != nil && step.ID == currentStepID {
				return step
			}
		}
	}
	for _, step := range steps {
		if step != nil && (step.Status == plan.StatusInProgress || step.Status == plan.StatusPaused || step.Status == plan.StatusFailed) {
			return step
		}
	}
	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		if step == nil {
			continue
		}
		if len(step.Evidence) > 0 || strings.TrimSpace(step.VerificationNote) != "" {
			return step
		}
	}
	for _, step := range steps {
		if step != nil {
			return step
		}
	}
	return nil
}

func prettyProofJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	b, err := json.MarshalIndent(payload, "  ", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

// GetJournalReport returns recent execution journal entries.
func (a *App) GetJournalReport() string {
	if a.journal == nil {
		return "Execution journal is not initialized."
	}
	entries, err := a.journal.Tail(30)
	if err != nil {
		return fmt.Sprintf("Failed to read execution journal: %v", err)
	}
	if len(entries) == 0 {
		return "Execution journal is empty."
	}
	var sb strings.Builder
	sb.WriteString("Execution journal (latest 30):\n")
	for _, e := range entries {
		sb.WriteString(fmt.Sprintf("- %s  %s", e.Timestamp.Format("15:04:05"), e.Event))
		if len(e.Details) > 0 {
			if p, ok := e.Details["message_preview"].(string); ok && p != "" {
				sb.WriteString(fmt.Sprintf(" | %s", p))
			} else if r, ok := e.Details["reason"].(string); ok && r != "" {
				sb.WriteString(fmt.Sprintf(" | %s", r))
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// GetRecoveryReport returns the latest persisted recovery snapshot.
func (a *App) GetRecoveryReport() string {
	if a.journal == nil {
		return "Recovery snapshot unavailable: journal not initialized."
	}
	snap, err := a.journal.LoadRecovery()
	if err != nil {
		return fmt.Sprintf("Failed to load recovery snapshot: %v", err)
	}
	if snap == nil {
		return "No recovery snapshot found."
	}
	return fmt.Sprintf(
		"Recovery snapshot:\n- updated: %s\n- session: %s\n- processing: %v\n- pending_message: %q\n- history_len: %d\n- plan_id: %s\n- current_step: %d",
		snap.Timestamp.Format("2006-01-02 15:04:05"), snap.SessionID, snap.Processing, snap.PendingMessage, snap.HistoryLen, snap.PlanID, snap.CurrentStepID,
	)
}

// GetObservabilityReport returns a unified operational report.
func (a *App) GetObservabilityReport() string {
	var sb strings.Builder
	sb.WriteString(a.GetRuntimeHealthReport())
	sb.WriteString("\n\n")
	sb.WriteString(a.GetPolicyReport())
	sb.WriteString("\n\n")
	sb.WriteString(a.GetLedgerReport())
	sb.WriteString("\n\n")
	sb.WriteString(a.GetRecoveryReport())
	sb.WriteString("\n\n")
	sb.WriteString(a.GetSessionGovernanceReport())
	sb.WriteString("\n\n")
	sb.WriteString(a.GetJournalReport())
	return sb.String()
}

// GetSessionGovernanceReport returns memory-governance statistics.
func (a *App) GetSessionGovernanceReport() string {
	a.sessionArchiveMu.Lock()
	defer a.sessionArchiveMu.Unlock()
	last := "never"
	if !a.lastSessionArchive.IsZero() {
		last = a.lastSessionArchive.Format("2006-01-02 15:04:05")
	}
	return fmt.Sprintf(
		"Session memory governance:\n- archive_ops: %d\n- archived_messages: %d\n- last_archive: %s\n- soft_limit: %d\n- keep_tail: %d",
		a.sessionArchiveOperations,
		a.sessionArchivedMessages,
		last,
		sessionGovernanceSoftLimit,
		sessionGovernanceKeepTail,
	)
}

// GetAgentTypeRegistry returns the agent type registry.
func (a *App) GetAgentTypeRegistry() *agent.AgentTypeRegistry {
	return a.agentTypeRegistry
}

// AppInterface implementation for commands package

// GetSession returns the current session.
func (a *App) GetSession() *chat.Session {
	return a.session
}

// ResumeLastSession loads and restores the most recent session before Run() is called.
func (a *App) ResumeLastSession() error {
	if a.sessionManager == nil {
		return fmt.Errorf("session manager not configured")
	}

	state, info, err := a.sessionManager.LoadLast()
	if err != nil {
		return fmt.Errorf("failed to load last session: %w", err)
	}
	if state == nil || len(state.History) == 0 {
		return fmt.Errorf("no previous session found")
	}

	if err := a.sessionManager.RestoreFromState(state); err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}

	a.mu.Lock()
	a.scratchpad = a.session.GetScratchpad()
	a.sessionPreloaded = true
	a.mu.Unlock()

	if a.agentRunner != nil {
		a.agentRunner.SetSharedScratchpad(a.scratchpad)
	}

	logging.Info("pre-loaded session", "session_id", info.ID, "messages", info.MessageCount)
	return nil
}

// GetHistoryManager returns a new history manager.
func (a *App) GetHistoryManager() (*chat.HistoryManager, error) {
	return chat.NewHistoryManager()
}

// GetContextManager returns the context manager.
func (a *App) GetContextManager() *appcontext.ContextManager {
	return a.contextManager
}

// safeSendToProgram safely sends a message to the Bubbletea program.
// It copies the program reference under lock to prevent race conditions.
func (a *App) safeSendToProgram(msg tea.Msg) {
	a.mu.Lock()
	program := a.program
	a.mu.Unlock()

	if program != nil {
		program.Send(msg)
	}
}

// sendTokenUsageUpdate sends a token usage update to the UI.
// This can be called from any goroutine safely.
func (a *App) sendTokenUsageUpdate() {
	if a.contextManager == nil || !a.config.UI.ShowTokenUsage {
		return
	}

	a.mu.Lock()
	program := a.program
	streamedChars := a.streamedChars
	a.mu.Unlock()

	if program == nil {
		return
	}

	usage := a.contextManager.GetTokenUsage()
	if usage == nil {
		return
	}

	tokens := usage.InputTokens
	isEstimate := usage.IsEstimate
	percentUsed := usage.PercentUsed
	nearLimit := usage.NearLimit

	// Add streaming estimate: ~4 chars per token
	if streamedChars > 0 {
		tokens += streamedChars / 4
		isEstimate = true
		if usage.MaxTokens > 0 {
			percentUsed = float64(tokens) / float64(usage.MaxTokens)
			nearLimit = percentUsed > 0.8
		}
	}

	program.Send(ui.TokenUsageMsg{
		Tokens:      tokens,
		MaxTokens:   usage.MaxTokens,
		PercentUsed: percentUsed,
		NearLimit:   nearLimit,
		IsEstimate:  isEstimate,
	})
}

// refreshTokenCount recalculates token count from session history and sends update to UI.
// More expensive than sendTokenUsageUpdate - call after history changes.
func (a *App) refreshTokenCount() {
	if a.contextManager == nil {
		return
	}
	if err := a.contextManager.UpdateTokenCount(a.ctx); err != nil {
		logging.Debug("failed to refresh token count", "error", err)
		return
	}
	a.sendTokenUsageUpdate()
}

// GetUndoManager returns the undo manager.
func (a *App) GetUndoManager() *undo.Manager {
	return a.undoManager
}

// GetWorkDir returns the working directory.
func (a *App) GetWorkDir() string {
	return a.workDir
}

// ClearConversation clears the session history.
func (a *App) ClearConversation() {
	a.session.Clear()

	// Re-set system instruction via API parameter
	systemPrompt := a.promptBuilder.Build()
	a.client.SetSystemInstruction(systemPrompt)
	a.session.SystemInstruction = systemPrompt
}

// CompactContextWithPlan clears the conversation and injects the plan summary.
// This is called when a plan is approved to free up context space.
func (a *App) CompactContextWithPlan(planSummary string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Clear the session
	a.session.Clear()

	// Re-set system instruction via API parameter
	systemPrompt := a.promptBuilder.Build()
	a.client.SetSystemInstruction(systemPrompt)
	a.session.SystemInstruction = systemPrompt

	// Inject the plan summary as a user message for execution context
	if planSummary != "" {
		a.session.AddUserMessage("Execute the approved plan. Summary:\n\n" + planSummary)
	}

	// Log the context compaction
	logging.Info("context compacted for plan execution",
		"session_id", a.session.ID,
		"plan_summary_length", len(planSummary))
}

// GetTodoTool returns the todo tool from the registry.
func (a *App) GetTodoTool() *tools.TodoTool {
	if todoTool, ok := a.registry.Get("todo"); ok {
		if tt, ok := todoTool.(*tools.TodoTool); ok && tt != nil {
			return tt
		}
	}
	return nil
}

// GetConfig returns the current configuration.
func (a *App) GetConfig() *config.Config {
	return a.config
}

// GetTokenStats returns token usage statistics for the session.
func (a *App) GetTokenStats() commands.TokenStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return commands.TokenStats{
		InputTokens:  a.totalInputTokens,
		OutputTokens: a.totalOutputTokens,
		TotalTokens:  a.totalInputTokens + a.totalOutputTokens,
	}
}

// GetModelSetter returns the client for model switching.
func (a *App) GetModelSetter() commands.ModelSetter {
	return a.client
}

// TogglePermissions toggles the permission system on/off.
func (a *App) TogglePermissions() bool {
	a.mu.Lock()

	if a.permManager == nil {
		a.mu.Unlock()
		return false
	}

	currentEnabled := a.permManager.IsEnabled()
	newEnabled := !currentEnabled
	a.permManager.SetEnabled(newEnabled)

	// Update TUI display
	if a.tui != nil {
		a.tui.SetPermissionsEnabled(newEnabled)
	}

	if newEnabled {
		logging.Debug("permissions enabled")
	} else {
		logging.Debug("permissions disabled")
	}

	// Update unrestricted mode based on new state
	a.updateUnrestrictedModeLocked()

	// Copy state for UI message before unlocking
	program := a.program
	sandboxEnabled := a.config.Tools.Bash.Sandbox
	planningModeEnabled := a.planningModeEnabled
	modelName := a.config.Model.Name
	a.mu.Unlock()

	// Send UI update message (program.Send is thread-safe)
	if program != nil {
		program.Send(ui.ConfigUpdateMsg{
			PermissionsEnabled:  newEnabled,
			SandboxEnabled:      sandboxEnabled,
			PlanningModeEnabled: planningModeEnabled,
			ModelName:           modelName,
		})
	}

	return newEnabled
}

// TogglePlanningMode toggles the tree planning mode on/off.
func (a *App) TogglePlanningMode() bool {
	a.mu.Lock()

	a.planningModeEnabled = !a.planningModeEnabled
	newEnabled := a.planningModeEnabled

	if a.planManager != nil {
		a.planManager.SetEnabled(newEnabled)
	}

	// Update agent runner
	if a.agentRunner != nil {
		a.agentRunner.SetPlanningModeEnabled(newEnabled)
	}

	// Update TUI display (direct setter for immediate effect)
	if a.tui != nil {
		a.tui.SetPlanningModeEnabled(newEnabled)
	}

	if newEnabled {
		logging.Debug("planning mode enabled")
	} else {
		logging.Debug("planning mode disabled")
	}

	// Copy state for UI message before unlocking
	program := a.program
	permissionsEnabled := a.permManager != nil && a.permManager.IsEnabled()
	sandboxEnabled := a.config.Tools.Bash.Sandbox
	modelName := a.config.Model.Name
	a.mu.Unlock()

	// Send UI update message to trigger proper refresh
	if program != nil {
		program.Send(ui.ConfigUpdateMsg{
			PermissionsEnabled:  permissionsEnabled,
			SandboxEnabled:      sandboxEnabled,
			PlanningModeEnabled: newEnabled,
			ModelName:           modelName,
		})
	}

	return newEnabled
}

// IsPlanningModeEnabled returns whether planning mode is active.
func (a *App) IsPlanningModeEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.planningModeEnabled
}

// TogglePlanningModeAsync toggles planning mode asynchronously.
// This is safe to call from UI callbacks as it doesn't block the Bubble Tea event loop.
func (a *App) TogglePlanningModeAsync() {
	go func() {
		newEnabled := a.TogglePlanningMode()
		a.safeSendToProgram(ui.PlanningModeToggledMsg{Enabled: newEnabled})
	}()
}

// ToggleSandbox toggles the bash sandbox mode on/off.
func (a *App) ToggleSandbox() bool {
	a.mu.Lock()

	a.config.Tools.Bash.Sandbox = !a.config.Tools.Bash.Sandbox
	newEnabled := a.config.Tools.Bash.Sandbox

	// Update bash tool sandbox setting
	if a.registry != nil {
		if bashTool, ok := a.registry.Get("bash"); ok {
			if bt, ok := bashTool.(*tools.BashTool); ok {
				bt.SetSandboxEnabled(newEnabled)
			}
		}
	}

	// Update TUI display
	if a.tui != nil {
		a.tui.SetSandboxEnabled(newEnabled)
	}

	if newEnabled {
		logging.Debug("sandbox enabled")
	} else {
		logging.Debug("sandbox disabled")
	}

	// Update unrestricted mode based on new state
	a.updateUnrestrictedModeLocked()

	// Copy state for UI message before unlocking
	cfg := a.config
	program := a.program
	permissionsEnabled := a.permManager != nil && a.permManager.IsEnabled()
	planningModeEnabled := a.planningModeEnabled
	modelName := a.config.Model.Name
	a.mu.Unlock()

	// Save config outside of mutex to avoid blocking on file I/O
	if err := cfg.Save(); err != nil {
		logging.Warn("failed to save sandbox setting", "error", err)
	}

	// Send UI update message to trigger proper refresh
	if program != nil {
		program.Send(ui.ConfigUpdateMsg{
			PermissionsEnabled:  permissionsEnabled,
			SandboxEnabled:      newEnabled,
			PlanningModeEnabled: planningModeEnabled,
			ModelName:           modelName,
		})
	}

	return newEnabled
}

// GetSandboxState returns whether sandbox mode is enabled.
func (a *App) GetSandboxState() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.config.Tools.Bash.Sandbox
}

// updateUnrestrictedModeLocked updates the executor's unrestricted mode based on
// current sandbox and permission states. Must be called with a.mu held.
func (a *App) updateUnrestrictedModeLocked() {
	if a.executor == nil {
		return
	}

	sandboxOff := !a.config.Tools.Bash.Sandbox
	permissionOff := a.permManager == nil || !a.permManager.IsEnabled()
	unrestrictedMode := sandboxOff && permissionOff

	// Update executor's unrestricted mode
	a.executor.SetUnrestrictedMode(unrestrictedMode)

	// Update bash tool's unrestricted mode
	if a.registry != nil {
		if bashTool, ok := a.registry.Get("bash"); ok {
			if bt, ok := bashTool.(*tools.BashTool); ok {
				bt.SetUnrestrictedMode(unrestrictedMode)
			}
		}
	}

	if unrestrictedMode {
		logging.Debug("unrestricted mode enabled: sandbox=off, permission=off")
	} else {
		logging.Debug("unrestricted mode disabled")
	}
}

// GetPermissionsState returns whether permissions are enabled.
func (a *App) GetPermissionsState() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.permManager == nil {
		return false
	}
	return a.permManager.IsEnabled()
}

// GetProjectInfo returns the detected project information.
func (a *App) GetProjectInfo() *appcontext.ProjectInfo {
	return a.projectInfo
}

// GetSemanticIndexer returns the semantic search indexer.
func (a *App) GetSemanticIndexer() (*semantic.EnhancedIndexer, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.semanticIndexer == nil {
		return nil, fmt.Errorf("semantic search not enabled")
	}

	return a.semanticIndexer, nil
}

// ApplyConfig saves the given configuration and re-initializes affected components.
func (a *App) ApplyConfig(cfg *config.Config) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	// 1. Save to file if path is available
	if err := cfg.Save(); err != nil {
		logging.Warn("failed to save config to file", "error", err)
		// Continue anyway as we want to apply it in-memory
	}

	// 2. Update internal config
	a.config = cfg

	// 3. Re-initialize client
	newClient, err := client.NewClient(a.ctx, a.config, a.config.Model.Name)
	if err != nil {
		return fmt.Errorf("failed to re-initialize client: %w", err)
	}
	attachStatusCallback(newClient, &appStatusCallback{app: a})
	oldClient := a.client
	a.client = newClient
	if oldClient != nil {
		go func() {
			if err := oldClient.Close(); err != nil {
				logging.Warn("failed to close old client", "error", err)
			}
		}()
	}

	// 4. Update executor's client and sync tools
	if a.executor != nil {
		a.executor.SetClient(newClient)
		if a.registry != nil {
			newClient.SetTools(a.registry.GeminiTools())
		}
	}

	// 5. Update agent runner
	if a.agentRunner != nil {
		a.agentRunner.SetClient(newClient)
		a.agentRunner.SetContextConfig(&a.config.Context)
	}

	// 6. Update context manager
	if a.contextManager != nil {
		a.contextManager.SetConfig(&a.config.Context)
		a.contextManager.SetClient(newClient)
	}

	// 7. Update rate limiter
	if a.config.RateLimit.Enabled {
		if a.rateLimiter == nil {
			a.rateLimiter = ratelimit.NewLimiter(ratelimit.Config{
				Enabled:           true,
				RequestsPerMinute: a.config.RateLimit.RequestsPerMinute,
				TokensPerMinute:   a.config.RateLimit.TokensPerMinute,
				BurstSize:         a.config.RateLimit.BurstSize,
			})
		} else {
			// Update existing limiter (assuming it has a way to update config)
			// For now, recreate it or ignore if no update method
		}
		a.client.SetRateLimiter(a.rateLimiter)
	}

	// 8. Update permission manager (YOLO mode)
	if a.permManager != nil {
		a.permManager.SetEnabled(a.config.Permission.Enabled)
	}

	// 8a. Update bash tool sandbox mode
	if a.registry != nil {
		if bashTool, ok := a.registry.Get("bash"); ok {
			if bt, ok := bashTool.(*tools.BashTool); ok {
				bt.SetSandboxEnabled(a.config.Tools.Bash.Sandbox)
			}
		}
	}

	// 8c. Update UI state (model name, etc.)
	if a.tui != nil {
		a.tui.SetCurrentModel(a.config.Model.Name)
		a.tui.SetShowTokens(a.config.UI.ShowTokenUsage)
		a.tui.SetPermissionsEnabled(a.config.Permission.Enabled)
		a.tui.SetSandboxEnabled(a.config.Tools.Bash.Sandbox)
		a.tui.SetPlanningModeEnabled(a.planningModeEnabled)
	}

	// 8d. Send ConfigUpdateMsg to Bubbletea program to refresh UI
	if a.program != nil {
		a.program.Send(ui.ConfigUpdateMsg{
			PermissionsEnabled:  a.config.Permission.Enabled,
			SandboxEnabled:      a.config.Tools.Bash.Sandbox,
			PlanningModeEnabled: a.planningModeEnabled,
			ModelName:           a.config.Model.Name,
		})
	}

	// 9. Update search cache
	if a.config.Cache.Enabled && a.searchCache == nil {
		a.searchCache = cache.NewSearchCache(a.config.Cache.Capacity, a.config.Cache.TTL)
		// Re-wire to tools (complex, but most tools check on use)
	}

	logging.Info("configuration applied successfully", "model", a.config.Model.Name)
	return nil
}

// stripLegacySystemMessages removes old-style system prompt messages from session history.
// Before Phase 1, system prompt was injected as history[0] (user message) + history[1] (model ack).
// Now system prompt is passed via API parameter, so these legacy messages waste tokens.
func (a *App) stripLegacySystemMessages() {
	history := a.session.GetHistory()
	if len(history) < 2 {
		return
	}

	stripCount := 0

	// Check if first message is a legacy system prompt (user role, long text with system prompt markers)
	if history[0].Role == string(genai.RoleUser) && len(history[0].Parts) > 0 {
		text := history[0].Parts[0].Text
		if len(text) > 500 && (strings.Contains(text, "You are") || strings.Contains(text, "MANDATORY") || strings.Contains(text, "available tools")) {
			stripCount = 1
			// Also check for model acknowledgment
			if len(history) >= 2 && history[1].Role == string(genai.RoleModel) && len(history[1].Parts) > 0 {
				ackText := history[1].Parts[0].Text
				if len(ackText) < 200 && (strings.Contains(ackText, "understand") || strings.Contains(ackText, "I'll") || strings.Contains(ackText, "help")) {
					stripCount = 2
				}
			}
		}
	}

	if stripCount > 0 {
		a.session.SetHistory(history[stripCount:])
		logging.Info("stripped legacy system messages from restored session", "count", stripCount)
	}
}

// toolContextSummary extracts a brief context string from tool args for progress display.
// Examples: "read internal/app/app.go", "bash go build ./...", "grep pattern in *.go"
func toolContextSummary(name string, args map[string]any) string {
	switch name {
	case "read":
		if fp, ok := args["file_path"].(string); ok {
			return filepath.Base(fp)
		}
	case "write", "edit":
		if fp, ok := args["file_path"].(string); ok {
			return filepath.Base(fp)
		}
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			if len(cmd) > 30 {
				cmd = cmd[:30] + "…"
			}
			return cmd
		}
	case "grep":
		if p, ok := args["pattern"].(string); ok {
			if len(p) > 20 {
				p = p[:20] + "…"
			}
			return p
		}
	case "glob":
		if p, ok := args["pattern"].(string); ok {
			return p
		}
	}
	return ""
}

// buildModelEnhancement returns model-specific prompt enhancements.
func (a *App) buildModelEnhancement() string {
	modelName := a.config.Model.Name

	if strings.HasPrefix(modelName, "glm") {
		return "\n\n**GLM Model Note:** After every tool call, you MUST respond with analysis of results. Never call tools and stop silently. Structure: What I Found → Key Points → Next Steps."
	}

	if strings.Contains(modelName, "flash") {
		return "\n\n**Flash Model Note:** Keep responses detailed with specific file:line references despite speed optimizations."
	}

	// Ollama models: per-model prompting + tool calling fallback
	if a.config.API.Backend == "ollama" {
		var enhancement string

		// Add per-model prompt enhancement based on model profile
		enhancement += client.ModelPromptEnhancement(modelName)

		// Add tool calling fallback prompt for models without native tool support
		profile := client.GetModelProfile(modelName)
		if !profile.SupportsTools {
			// Use only the filtered tool set (same as selectToolSets in builder)
			decls := a.getActiveToolDeclarations()
			enhancement += client.ToolCallFallbackPrompt(decls)
		}

		return enhancement
	}

	return ""
}

// refreshSystemInstruction rebuilds and reapplies the dynamic system instruction.
// This keeps active contract/memory/tool-hints in sync with current runtime state.
func (a *App) refreshSystemInstruction() {
	if a.promptBuilder == nil || a.client == nil || a.session == nil {
		return
	}
	systemPrompt := a.promptBuilder.Build() + a.buildModelEnhancement()
	if strings.TrimSpace(systemPrompt) == "" {
		return
	}
	a.client.SetSystemInstruction(systemPrompt)
	a.session.SystemInstruction = systemPrompt
}

// getActiveToolDeclarations returns declarations for the tools actually available
// to the current model (matching selectToolSets logic in builder).
func (a *App) getActiveToolDeclarations() []*genai.FunctionDeclaration {
	if a.config.API.Backend == "ollama" {
		sets := []tools.ToolSet{tools.ToolSetOllamaCore}
		if git.IsGitRepo(a.workDir) {
			sets = append(sets, tools.ToolSetGit)
		}
		return a.registry.FilteredDeclarations(sets...)
	}
	return a.registry.Declarations()
}

// handleApplyCodeBlock is in app_handlers.go

// CancelProcessing cancels the current processing request.
// Called when user presses ESC during processing.
func (a *App) CancelProcessing() {
	a.processingMu.Lock()
	defer a.processingMu.Unlock()
	if a.processingCancel != nil {
		a.processingCancel()
		a.processingCancel = nil
	}
}

// agentRunnerAdapter wraps agent.Runner to implement tools.AgentRunner interface.
type agentRunnerAdapter struct {
	runner *agent.Runner
}

func (a *agentRunnerAdapter) Spawn(ctx context.Context, agentType string, prompt string, maxTurns int, model string) (string, error) {
	return a.runner.Spawn(ctx, agentType, prompt, maxTurns, model)
}

func (a *agentRunnerAdapter) SpawnAsync(ctx context.Context, agentType string, prompt string, maxTurns int, model string) string {
	return a.runner.SpawnAsync(ctx, agentType, prompt, maxTurns, model)
}

func (a *agentRunnerAdapter) SpawnAsyncWithStreaming(ctx context.Context, agentType string, prompt string, maxTurns int, model string, onText func(string), onProgress func(id string, progress *tools.AgentProgress)) string {
	// Convert tools.AgentProgress callback to agent.AgentProgress callback
	var agentProgressCb func(id string, progress *agent.AgentProgress)
	if onProgress != nil {
		agentProgressCb = func(id string, progress *agent.AgentProgress) {
			if progress != nil {
				onProgress(id, &tools.AgentProgress{
					AgentID:       progress.AgentID,
					CurrentStep:   progress.CurrentStep,
					TotalSteps:    progress.TotalSteps,
					CurrentAction: progress.CurrentAction,
					Elapsed:       progress.Elapsed,
					ToolsUsed:     progress.ToolsUsed,
				})
			}
		}
	}
	return a.runner.SpawnAsyncWithStreaming(ctx, agentType, prompt, maxTurns, model, onText, agentProgressCb)
}

func (a *agentRunnerAdapter) Resume(ctx context.Context, agentID string, prompt string) (string, error) {
	return a.runner.Resume(ctx, agentID, prompt)
}

func (a *agentRunnerAdapter) ResumeAsync(ctx context.Context, agentID string, prompt string) (string, error) {
	return a.runner.ResumeAsync(ctx, agentID, prompt)
}

func (a *agentRunnerAdapter) GetResult(agentID string) (tools.AgentResult, bool) {
	result, ok := a.runner.GetResult(agentID)
	if !ok || result == nil {
		return tools.AgentResult{}, false
	}
	return tools.AgentResult{
		AgentID:   result.AgentID,
		Type:      string(result.Type),
		Status:    string(result.Status),
		Output:    result.Output,
		Error:     result.Error,
		Duration:  result.Duration,
		Completed: result.Completed,
	}, true
}

// diffHandlerAdapter is in app_handlers.go

// GetUIDebugState returns a serializable snapshot of the TUI state.
func (a *App) GetUIDebugState() (any, error) {
	if a.tui == nil {
		return nil, fmt.Errorf("TUI not initialized")
	}
	return a.tui.DebugState(), nil
}

// GetVersion returns the current application version.
func (a *App) GetVersion() string {
	return a.config.Version
}

// AddSystemMessage adds a system message to the TUI chat.
func (a *App) AddSystemMessage(msg string) {
	if a.tui != nil {
		a.tui.AddSystemMessage(msg)
	}
}
