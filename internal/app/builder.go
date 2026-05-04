package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	"gokin/internal/memory"
	"gokin/internal/permission"
	"gokin/internal/plan"
	"gokin/internal/ratelimit"
	"gokin/internal/router"
	"gokin/internal/tasks"
	"gokin/internal/tools"
	"gokin/internal/ui"
	"gokin/internal/undo"
	"gokin/internal/watcher"

	"google.golang.org/genai"
)

// Builder provides a fluent interface for constructing App instances.
// This breaks up the massive New() function and makes dependency injection clearer.
type Builder struct {
	cfg     *config.Config
	workDir string
	ctx     context.Context
	cancel  context.CancelFunc

	// Optional components (nil means not configured)
	configDir        string
	configDirErr     error
	mainClient       client.Client
	registry         *tools.Registry
	executor         *tools.Executor
	readTracker      *tools.FileReadTracker
	session          *chat.Session
	tuiModel         *ui.Model
	projectInfo      *appcontext.ProjectInfo
	projectMemory    *appcontext.ProjectMemory
	promptBuilder    *appcontext.PromptBuilder
	contextManager   *appcontext.ContextManager
	permManager      *permission.Manager
	planManager      *plan.Manager
	hooksManager     *hooks.Manager
	taskManager      *tasks.Manager
	undoManager      *undo.Manager
	agentRunner      *agent.Runner
	commandHandler   *commands.Handler
	searchCache      *cache.SearchCache
	rateLimiter      *ratelimit.Limiter
	auditLogger      *audit.Logger
	fileWatcher      *watcher.Watcher
	taskRouter       *router.Router
	taskOrchestrator *TaskOrchestrator // Unified Task Orchestrator

	// Phase 5: Agent System Improvements (6→10)
	agentTypeRegistry *agent.AgentTypeRegistry
	strategyOptimizer *agent.StrategyOptimizer
	metaAgent         *agent.MetaAgent
	coordinator       *agent.Coordinator

	// Phase 6: Tree Planner
	treePlanner *agent.TreePlanner

	// Phase 7: Delegation Metrics (adaptive delegation rules)
	delegationMetrics *agent.DelegationMetrics

	// Phase 2: Learning infrastructure
	sharedMemory    *agent.SharedMemory
	memStore        *memory.Store
	projectLearning *memory.ProjectLearning
	exampleStore    *memory.ExampleStore
	errorStore      *memory.ErrorStore
	promptOptimizer *agent.PromptOptimizer
	smartRouter     *router.SmartRouter

	// Session persistence
	sessionManager *chat.SessionManager

	// MCP (Model Context Protocol)
	mcpManager        *mcp.Manager
	mcpConnectSummary string // Deferred UI summary of initial MCP connect results
	contextAgent      *appcontext.ContextAgent

	// Context Predictor (predictive file loading)
	contextPredictor *appcontext.ContextPredictor

	// Model capability (inferred from provider/model name)
	modelCapability *router.ModelCapability

	// Session Memory (automatic conversation summary)
	sessionMemory *appcontext.SessionMemoryManager
	workingMemory *appcontext.WorkingMemoryManager

	// For error collection during build
	buildErrors []error
	mu          sync.Mutex

	// Cached app instance (created once, reused)
	cachedApp *App

	// MCP startup race coordination. The MCP manager is created early in
	// initIntegrations and starts receiving tools/list_changed notifications
	// as soon as clients connect — but the App that SyncMCPToolsForServer
	// needs doesn't exist until assembleApp runs later in wireDependencies.
	// Events arriving in that window are queued here and drained once the
	// App is ready.
	mcpDispatchMu          sync.Mutex
	mcpDispatchApp         *App
	mcpPendingToolsChanged []string
}

// NewBuilder creates a new Builder with the given config and work directory.
func NewBuilder(cfg *config.Config, workDir string) *Builder {
	ctx, cancel := context.WithCancel(context.Background())

	return &Builder{
		cfg:         cfg,
		workDir:     workDir,
		ctx:         ctx,
		cancel:      cancel,
		buildErrors: make([]error, 0),
	}
}

// Build constructs the App instance, returning any errors encountered.
func (b *Builder) Build() (*App, error) {
	// Initialize core components
	if err := b.initConfigDir(); err != nil {
		b.addError(err)
	}
	// Check allowed directories BEFORE creating tools and validators
	// This ensures permissions are loaded before PathValidator is created
	if err := b.checkAllowedDirs(); err != nil {
		b.addError(err)
	}
	if err := b.initClient(); err != nil {
		b.addError(err)
		return nil, b.finalizeError()
	}
	// Validate Ollama model availability (auto-pull if missing)
	if err := b.validateOllamaModel(); err != nil {
		b.addError(err)
		return nil, b.finalizeError()
	}
	if err := b.initTools(); err != nil {
		b.addError(err)
	}
	if err := b.initSession(); err != nil {
		b.addError(err)
	}
	if err := b.initManagers(); err != nil {
		b.addError(err)
	}
	if err := b.initIntegrations(); err != nil {
		b.addError(err)
	}
	if err := b.initUI(); err != nil {
		b.addError(err)
	}
	if err := b.wireDependencies(); err != nil {
		b.addError(err)
		return nil, b.finalizeError()
	}

	return b.assembleApp(), nil
}

// initConfigDir initializes the configuration directory.
func (b *Builder) initConfigDir() error {
	configDir, err := appcontext.GetConfigDir()
	b.configDir = configDir
	b.configDirErr = err
	if err != nil {
		logging.Debug("failed to get config dir", "error", err)
		// Don't fail - continue with optional features disabled
	}
	return nil
}

// checkAllowedDirs checks if additional directories should be allowed
// and prompts the user on first run. This happens BEFORE tool creation
// so that PathValidator gets the correct allowed directories.
func (b *Builder) checkAllowedDirs() error {
	// Skip if allowed_dirs is already configured
	if len(b.cfg.Tools.AllowedDirs) > 0 {
		return nil
	}

	// Get home directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil // Skip if we can't get home dir
	}

	// First run - prompt about allowing home directory
	fmt.Printf("\nAllowed directories setup (first run)\n\n")
	fmt.Printf("   Current working directory: %s\n", b.workDir)
	fmt.Printf("   AI can only access the working directory.\n\n")
	fmt.Printf("   Do you want to allow access to home directory (%s)?\n", homeDir)
	fmt.Printf("   This will allow AI to work with files in any of your projects.\n\n")
	fmt.Printf("   [1] Yes, allow access to home directory\n")
	fmt.Printf("   [2] No, allow only current directory\n")
	fmt.Printf("   [3] Specify another directory\n\n")
	fmt.Printf("   Choice [1/2/3]: ")

	var response string
	fmt.Scanln(&response)
	response = strings.TrimSpace(strings.ToLower(response))

	var dirToAdd string
	switch response {
	case "1", "y", "yes":
		dirToAdd = homeDir
	case "3":
		fmt.Printf("   Enter path: ")
		fmt.Scanln(&dirToAdd)
		dirToAdd = strings.TrimSpace(dirToAdd)
		if dirToAdd == "" {
			// User cancelled - save workDir to prevent re-prompting
			dirToAdd = b.workDir
			fmt.Printf("   Only working directory allowed.\n")
		} else if strings.HasPrefix(dirToAdd, "~/") {
			// Expand ~ if present
			dirToAdd = filepath.Join(homeDir, dirToAdd[2:])
		}
	default:
		// "2" or any other - save workDir to prevent re-prompting
		dirToAdd = b.workDir
		fmt.Printf("   Only current working directory allowed.\n")
	}

	if b.cfg.AddAllowedDir(dirToAdd) {
		logging.Debug("saving config", "path", config.GetConfigPath(), "allowed_dir", dirToAdd)
		if err := b.cfg.Save(); err != nil {
			fmt.Printf("   Failed to save: %s\n", err)
			fmt.Printf("   Directory added only for current session.\n\n")
			logging.Error("failed to save config", "error", err)
		} else {
			fmt.Printf("   Added: %s\n", dirToAdd)
			fmt.Printf("   Saved to: %s\n\n", config.GetConfigPath())
			logging.Debug("config saved successfully", "allowed_dirs", b.cfg.Tools.AllowedDirs)
		}
	} else {
		fmt.Printf("   Directory already in allowed list.\n\n")
	}

	return nil
}

// validateOllamaModel checks if the configured model is available.
// Called only when backend is "ollama".
func (b *Builder) validateOllamaModel() error {
	// Skip if not Ollama backend
	if b.cfg.API.Backend != "ollama" {
		return nil
	}

	ollamaClient, ok := b.mainClient.(*client.OllamaClient)
	if !ok {
		return nil
	}

	modelName := b.cfg.Model.Name
	ctx, cancel := context.WithTimeout(b.ctx, 10*time.Second)
	defer cancel()

	available, err := ollamaClient.IsModelAvailable(ctx, modelName)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "connection refused") || strings.Contains(errStr, "no such host") {
			fmt.Fprintf(os.Stderr, "\n⚠ Ollama server is not running.\n\n")
			fmt.Fprintf(os.Stderr, "  Start it with: ollama serve\n")
			fmt.Fprintf(os.Stderr, "  Then restart gokin.\n\n")
			baseURL := b.cfg.API.OllamaBaseURL
			if baseURL == "" {
				baseURL = config.DefaultOllamaBaseURL
			}
			return fmt.Errorf("Ollama server not running at %s", baseURL)
		}
		logging.Debug("ollama healthcheck failed, skipping model validation", "error", err)
		return nil
	}

	if available {
		return nil
	}

	// Model not found — ask user
	return b.promptModelPull(ollamaClient, modelName)
}

// promptModelPull asks user to download a missing model.
func (b *Builder) promptModelPull(c *client.OllamaClient, modelName string) error {
	fmt.Printf("\nModel '%s' is not installed.\n\n", modelName)
	fmt.Printf("Would you like to download it now? [Y/n] ")

	var response string
	fmt.Scanln(&response)
	response = strings.TrimSpace(strings.ToLower(response))

	if response != "" && response != "y" && response != "yes" {
		return fmt.Errorf("model '%s' is not available. Run: ollama pull %s", modelName, modelName)
	}

	fmt.Printf("\nDownloading %s...\n", modelName)

	err := c.PullModel(b.ctx, modelName, func(p client.PullProgress) {
		if p.Total > 0 {
			completedMB := float64(p.Completed) / (1024 * 1024)
			totalMB := float64(p.Total) / (1024 * 1024)
			if totalMB >= 1024 {
				fmt.Printf("\r  %s: %.1f%% (%.1fGB/%.1fGB)    ",
					p.Status, p.Percent, completedMB/1024, totalMB/1024)
			} else {
				fmt.Printf("\r  %s: %.1f%% (%.0fMB/%.0fMB)    ",
					p.Status, p.Percent, completedMB, totalMB)
			}
		} else {
			fmt.Printf("\r  %s...    ", p.Status)
		}
	})

	if err != nil {
		return fmt.Errorf("failed to download model: %w", err)
	}

	fmt.Printf("\n\nModel '%s' downloaded successfully!\n\n", modelName)
	return nil
}

// initClient creates the appropriate API client based on model configuration.
func (b *Builder) initClient() error {
	var err error
	// Use the factory to create the appropriate client (Gemini or Anthropic/GLM-4.7)
	b.mainClient, err = client.NewClient(b.ctx, b.cfg, b.cfg.Model.Name)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	// Debug: log which client was created
	if _, ok := b.mainClient.(*client.AnthropicClient); ok {
		logging.Debug("client created", "type", "Anthropic (GLM/Kimi/MiniMax)", "model", b.cfg.Model.Name)
	} else if _, ok := b.mainClient.(*client.OllamaClient); ok {
		logging.Debug("client created", "type", "Ollama", "model", b.cfg.Model.Name)
	} else {
		logging.Debug("client created", "type", "Unknown", "model", b.cfg.Model.Name)
	}

	return nil
}

// initTools creates the tool registry and executor.
func (b *Builder) initTools() error {
	b.registry = tools.DefaultRegistry(b.workDir)

	// Dynamic tool filtering: select tool sets based on context
	mainTools := b.selectToolSets()
	b.mainClient.SetTools(mainTools)

	b.executor = tools.NewExecutor(b.registry, b.mainClient, b.cfg.Tools.Timeout)
	b.executor.SetWorkDir(b.workDir)
	b.executor.SetModelRoundTimeout(b.cfg.Tools.ModelRoundTimeout)
	b.executor.SetDeltaCheckConfig(
		b.cfg.Tools.DeltaCheck.Enabled,
		b.cfg.Tools.DeltaCheck.Timeout,
		b.cfg.Tools.DeltaCheck.WarnOnly,
		b.cfg.Tools.DeltaCheck.MaxModules,
	)
	// Smart validation: semantic validators + context enrichment + self-review
	if b.cfg.Tools.SmartValidation.Enabled {
		registry := tools.NewSemanticValidatorRegistry()
		registry.Register(&tools.TestQualityValidator{})
		registry.Register(&tools.CIWorkflowValidator{})
		registry.Register(&tools.VersionConsistencyValidator{})
		registry.Register(&tools.GoQualityValidator{})
		registry.Register(&tools.DockerfileValidator{})
		registry.Register(&tools.DockerComposeValidator{})
		registry.Register(&tools.SecurityValidator{})
		registry.Register(&tools.ShellValidator{})
		b.executor.SetSemanticValidators(registry)

		enricher := tools.NewContextEnricher(b.workDir)
		// Predictor wiring deferred to wireDependencies when contextPredictor is available
		b.executor.SetContextEnricher(enricher)
	}
	if threshold := b.cfg.Tools.SmartValidation.SelfReviewThreshold; threshold > 0 {
		b.executor.SetSelfReviewThreshold(threshold)
	}
	// Per-turn Kimi tool budget — 0 disables, clamp-up happens in the setter.
	b.executor.SetKimiToolBudget(b.cfg.Tools.KimiToolBudget)

	compactor := appcontext.NewResultCompactor(b.cfg.Context.ToolResultMaxChars)
	b.executor.SetCompactor(compactor)

	// Deep compressor as a second-pass safety net: ResultCompactor trims
	// the string Content; ResponseCompressor trims nested map/array Data
	// that bypasses the Content path. Shares the same char budget so the
	// two work in tandem rather than at cross-purposes.
	b.executor.SetResponseCompressor(appcontext.NewResponseCompressor(b.cfg.Context.ToolResultMaxChars))

	toolCache := tools.NewToolResultCache(tools.DefaultCacheConfig())
	b.executor.SetToolCache(toolCache)

	// Wire search cache to executor for invalidation on write operations
	if b.searchCache != nil {
		b.executor.SetSearchCache(b.searchCache)
	}

	readTracker := tools.NewFileReadTracker()
	b.executor.SetReadTracker(readTracker)
	b.readTracker = readTracker

	// Enable native macOS notifications if configured
	if b.cfg.UI.NativeNotifications {
		b.executor.GetNotificationManager().EnableNativeNotifications(true)
	}

	return nil
}

// selectToolSets determines which tool sets to include based on the current context.
func (b *Builder) selectToolSets() []*genai.Tool {
	// For Ollama models, use a reduced tool set
	if b.cfg.API.Backend == "ollama" {
		sets := []tools.ToolSet{tools.ToolSetOllamaCore}

		// Add git tools if we're in a git repository
		if b.isInGitRepo() {
			sets = append(sets, tools.ToolSetGit)
		}

		logging.Debug("ollama tool filtering",
			"sets", fmt.Sprintf("%v", sets),
			"total_tools", len(b.registry.FilteredDeclarations(sets...)))
		return b.registry.FilteredGeminiTools(sets...)
	}

	// For cloud models: base tool sets
	// Router will override per-request via SetTools in Execute()
	sets := []tools.ToolSet{
		tools.ToolSetCore,
		tools.ToolSetFileOps,
		tools.ToolSetWeb,
	}

	// Add git tools if we're in a git repository
	if b.isInGitRepo() {
		sets = append(sets, tools.ToolSetGit)
	}

	// Include remaining sets as fallback for non-routed code paths
	sets = append(sets, tools.ToolSetPlanning, tools.ToolSetAgent,
		tools.ToolSetAdvanced, tools.ToolSetMemory)

	logging.Debug("tool filtering",
		"sets", fmt.Sprintf("%v", sets),
		"total_tools", len(b.registry.FilteredDeclarations(sets...)))
	return b.registry.FilteredGeminiTools(sets...)
}

// isInGitRepo checks if the working directory is inside a git repository.
func (b *Builder) isInGitRepo() bool {
	return git.IsGitRepo(b.workDir)
}

// initSession creates the chat session and context management.
func (b *Builder) initSession() error {
	b.session = chat.NewSession()
	b.session.SetWorkDir(b.workDir)
	// Tag the session with the active provider so the auto-resume path
	// can refuse cross-provider loads. See Session.Provider docstring
	// for the history-incompatibility problem this guards against.
	b.session.SetProvider(b.cfg.API.GetActiveProvider())

	b.projectInfo = appcontext.DetectProject(b.workDir)

	b.projectMemory = appcontext.NewProjectMemory(b.workDir)
	if err := b.projectMemory.Load(); err != nil {
		logging.Debug("project memory not loaded", "error", err)
	}
	// Start watching for instruction file changes (GOKIN.md, CLAUDE.md, etc.)
	if err := b.projectMemory.StartWatching(b.ctx, 500); err != nil {
		logging.Debug("project memory file watching not started", "error", err)
	}

	b.promptBuilder = appcontext.NewPromptBuilder(b.workDir, b.projectInfo)
	b.promptBuilder.SetProjectMemory(b.projectMemory)
	if b.memStore != nil && b.cfg.Memory.AutoInject {
		b.promptBuilder.SetMemoryStore(b.memStore)
	}
	if b.projectLearning != nil {
		b.promptBuilder.SetProjectLearning(b.projectLearning)
	}
	b.projectMemory.OnReload(func() {
		b.promptBuilder.Invalidate()
	})
	b.promptBuilder.SetPlanAutoDetect(b.cfg.Plan.AutoDetect)

	// Initialize session memory for automatic context extraction
	smConfig := appcontext.SessionMemoryConfig{
		Enabled:                 b.cfg.SessionMemory.Enabled,
		MinTokensToInit:         b.cfg.SessionMemory.MinTokensToInit,
		MinTokensBetweenUpdates: b.cfg.SessionMemory.MinTokensBetweenUpdates,
		ToolCallsBetweenUpdates: b.cfg.SessionMemory.ToolCallsBetweenUpdates,
	}
	if smConfig.MinTokensToInit == 0 {
		smConfig = appcontext.DefaultSessionMemoryConfig()
	}
	b.sessionMemory = appcontext.NewSessionMemoryManager(b.workDir, smConfig)
	b.sessionMemory.LoadFromDisk()
	b.sessionMemory.SetSummarizer(appcontext.NewClientSessionSummarizer(b.mainClient))
	if b.projectLearning != nil {
		b.sessionMemory.SetProjectLearning(b.projectLearning)
	}
	b.promptBuilder.SetSessionMemory(b.sessionMemory)

	b.workingMemory = appcontext.NewWorkingMemoryManager(b.workDir)
	b.workingMemory.LoadFromDisk()
	b.promptBuilder.SetWorkingMemory(b.workingMemory)

	// Provider-specific prompt addendum (Kimi operating rules, etc.).
	// GetActiveProvider returns "" when nothing is configured yet —
	// SetProvider handles empty string as a no-op.
	b.promptBuilder.SetProvider(b.cfg.API.GetActiveProvider())

	// Ensure .gokin working files are in .gitignore
	appcontext.EnsureGokinGitignore(b.workDir)

	// Detect git worktree for session isolation awareness
	if wt := git.DetectWorktree(b.workDir); wt != nil && !wt.IsMainWorktree {
		logging.Info("running inside git worktree",
			"branch", wt.Branch,
			"head", wt.Head,
			"main_root", git.GetMainWorktreeRoot(b.workDir))
	}

	b.contextManager = appcontext.NewContextManager(b.ctx, b.session, b.mainClient, &b.cfg.Context)
	b.contextAgent = appcontext.NewContextAgent(b.contextManager, b.session, b.configDir)

	// Initialize context predictor for predictive file loading
	b.contextPredictor = appcontext.NewContextPredictor(b.workDir)
	logging.Debug("context predictor initialized")

	// Start session watcher for auto-updating token counts
	b.contextManager.StartSessionWatcher()

	// Create session manager for auto-save/load
	if b.cfg.Session.Enabled {
		smConfig := chat.SessionManagerConfig{
			Enabled:      b.cfg.Session.Enabled,
			SaveInterval: b.cfg.Session.SaveInterval,
			AutoLoad:     b.cfg.Session.AutoLoad,
		}
		var err error
		b.sessionManager, err = chat.NewSessionManager(b.session, smConfig)
		if err != nil {
			logging.Warn("session persistence disabled", "error", err)
			b.sessionManager = nil
		}
	}

	return nil
}

// initManagers creates various manager components.
func (b *Builder) initManagers() error {
	// Permission manager
	if b.cfg.Permission.Enabled {
		rules := permission.NewRulesFromConfig(b.cfg.Permission.DefaultPolicy, b.cfg.Permission.Rules)
		b.permManager = permission.NewManager(rules, true)
	} else {
		b.permManager = permission.NewManager(nil, false)
	}
	b.executor.SetPermissions(b.permManager)

	// Set unrestricted mode when both sandbox and permissions are off
	// In this mode, preflight check errors become warnings (no blocking)
	sandboxOff := !b.cfg.Tools.Bash.Sandbox
	permissionOff := !b.cfg.Permission.Enabled
	b.executor.SetUnrestrictedMode(sandboxOff && permissionOff)
	if sandboxOff && permissionOff {
		logging.Debug("unrestricted mode enabled: sandbox=off, permission=off")
	}

	// Plan manager
	b.planManager = plan.NewManager(b.cfg.Plan.Enabled, b.cfg.Plan.RequireApproval)
	b.planManager.SetWorkDir(b.workDir)
	if b.promptBuilder != nil {
		b.promptBuilder.SetPlanManager(b.planManager)
	}
	if b.contextManager != nil {
		b.contextManager.SetPlanManager(b.planManager)
	}

	// Adaptive replan handler: uses LLM to generate replacement steps on fatal errors
	b.planManager.SetReplanHandler(func(ctx context.Context, p *plan.Plan, failedStep *plan.Step) ([]*plan.Step, error) {
		prompt := fmt.Sprintf(
			"A plan step failed with a fatal error.\n"+
				"Plan: %s\nFailed step %d: %s\nError: %s\n\n"+
				"Generate 1-3 replacement steps to work around this failure and achieve the original goal.\n"+
				"Return each step as a line: STEP: <title> | <description>\n"+
				"Be concise.",
			p.Title, failedStep.ID, failedStep.Title, failedStep.Error)

		onText := func(_ string) {} // discard streaming text
		_, result, err := b.agentRunner.SpawnWithContext(ctx, "plan", prompt, 10, "", "", onText, false, nil)
		if err != nil {
			return nil, err
		}
		if result == nil || result.Output == "" {
			return nil, fmt.Errorf("empty replan response")
		}

		// Parse "STEP: title | description" lines
		var steps []*plan.Step
		for _, line := range strings.Split(result.Output, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "STEP:") {
				continue
			}
			line = strings.TrimPrefix(line, "STEP:")
			line = strings.TrimSpace(line)
			parts := strings.SplitN(line, "|", 2)
			title := strings.TrimSpace(parts[0])
			desc := ""
			if len(parts) > 1 {
				desc = strings.TrimSpace(parts[1])
			}
			if title != "" {
				steps = append(steps, &plan.Step{Title: title, Description: desc})
			}
		}
		if len(steps) == 0 {
			return nil, fmt.Errorf("no steps parsed from replan response")
		}
		return steps, nil
	})

	// Plan persistence store
	if b.configDirErr == nil {
		planStore, err := plan.NewPlanStore(b.configDir)
		if err != nil {
			logging.Warn("plan persistence disabled", "error", err)
		} else {
			b.planManager.SetPlanStore(planStore)
			logging.Debug("plan store initialized", "dir", b.configDir)

			// Auto-load most recent resumable plan (but don't execute)
			if loadedPlan, err := b.planManager.LoadPausedPlan(); err == nil && loadedPlan != nil {
				logging.Debug("loaded resumable plan from previous session",
					"id", loadedPlan.ID,
					"title", loadedPlan.Title,
					"status", loadedPlan.Status,
					"pending_steps", loadedPlan.PendingCount(),
				)
			}
		}
	}

	// Hooks manager
	b.hooksManager = hooks.NewManager(b.cfg.Hooks.Enabled, b.workDir)
	for _, hookCfg := range b.cfg.Hooks.Hooks {
		b.hooksManager.AddHook(&hooks.Hook{
			Name:        hookCfg.Name,
			Type:        hooks.Type(hookCfg.Type),
			ToolName:    hookCfg.ToolName,
			Command:     hookCfg.Command,
			Enabled:     hookCfg.Enabled,
			Condition:   hooks.Condition(hookCfg.Condition),
			FailOnError: hookCfg.FailOnError,
			DependsOn:   hookCfg.DependsOn,
		})
	}
	b.executor.SetHooks(b.hooksManager)

	// Auto-formatter
	formatters := tools.DefaultFormatters()
	for ext, cmd := range b.cfg.Tools.Formatters {
		formatters[ext] = cmd // User overrides
	}
	b.executor.SetFormatter(tools.NewFormatter(formatters, 5*time.Second))

	// Task manager
	b.taskManager = tasks.NewManager(b.workDir)

	// Undo manager
	b.undoManager = undo.NewManager()

	// Agent runner
	b.agentRunner = agent.NewRunner(b.ctx, b.mainClient, b.registry, b.workDir)
	b.agentRunner.SetPermissions(b.permManager)
	b.agentRunner.SetContextConfig(&b.cfg.Context)
	b.agentRunner.SetWorkspaceIsolationEnabled(b.cfg.Plan.WorkspaceIsolation)
	if b.readTracker != nil {
		tracker := b.readTracker
		b.agentRunner.SetRecentFilesProvider(func(limit int) []string {
			return tracker.RecentlyReadFiles(limit)
		})
	}

	// Wire agent store for checkpoint persistence
	if b.configDirErr == nil {
		agentStore, err := agent.NewAgentStore(b.configDir)
		if err != nil {
			logging.Warn("failed to create agent store", "error", err)
		} else {
			b.agentRunner.SetStore(agentStore)
		}
	}

	// Command handler — overlays user aliases from ~/.config/gokin/aliases.yaml
	// on top of the built-in shortcuts. Missing file is silently ignored so
	// first-time users don't see a scary startup error.
	b.commandHandler = commands.NewHandler()
	if b.configDir != "" {
		aliasPath := filepath.Join(b.configDir, "aliases.yaml")
		if err := b.commandHandler.LoadAliasesFromFile(aliasPath); err != nil {
			logging.Warn("failed to load command aliases", "path", aliasPath, "error", err)
		}
	}

	// Initialize task router
	routerCfg := &router.RouterConfig{
		Enabled:            true,
		DecomposeThreshold: 4,
		ParallelThreshold:  7,
	}
	b.taskRouter = router.NewRouter(routerCfg, b.executor, b.agentRunner, b.mainClient, b.registry, b.isInGitRepo(), b.workDir)

	// Wire plan manager to router for plan-aware routing
	// When a plan is active, router avoids nested decomposition
	if b.planManager != nil {
		b.taskRouter.SetPlanChecker(b.planManager)
	}

	logging.Debug("task router initialized",
		"enabled", routerCfg.Enabled,
		"decompose_threshold", routerCfg.DecomposeThreshold,
		"parallel_threshold", routerCfg.ParallelThreshold)

	// Initialize Task Orchestrator (Unified)
	b.taskOrchestrator = NewTaskOrchestrator(5, 10*time.Minute)
	b.taskOrchestrator.SetOnStatusChange(func(id string, status OrchestratorTaskStatus) {
		// UI updates will be handled via App's program
	})
	logging.Debug("task orchestrator initialized", "max_concurrent", 5)

	// === PHASE 5: Agent System Improvements (6→10) ===

	// 1. Agent Type Registry (dynamic agent types)
	b.agentTypeRegistry = agent.NewAgentTypeRegistry()
	b.agentRunner.SetTypeRegistry(b.agentTypeRegistry)
	logging.Debug("agent type registry initialized")

	// 2. Strategy Optimizer (learns from outcomes)
	if b.configDirErr == nil {
		b.strategyOptimizer = agent.NewStrategyOptimizer(b.configDir)
		b.agentRunner.SetStrategyOptimizer(b.strategyOptimizer)
		logging.Debug("strategy optimizer initialized")

		// 2b. Delegation Metrics (adaptive delegation rules)
		b.delegationMetrics = agent.NewDelegationMetrics(b.configDir)
		b.agentRunner.SetDelegationMetrics(b.delegationMetrics)
		logging.Debug("delegation metrics initialized")
	}

	// 3. Coordinator for task orchestration
	coordConfig := &agent.CoordinatorConfig{MaxParallel: 3}
	b.coordinator = agent.NewCoordinator(b.ctx, b.agentRunner, coordConfig)
	b.coordinator.Start()
	logging.Debug("coordinator initialized", "max_parallel", 3)

	// 4. Meta-Agent (monitors and optimizes agents)
	metaConfig := agent.DefaultMetaAgentConfig()
	b.metaAgent = agent.NewMetaAgent(
		b.ctx,
		b.agentRunner,
		b.coordinator,
		b.strategyOptimizer,
		b.agentTypeRegistry,
		metaConfig,
	)
	b.metaAgent.Start()
	b.agentRunner.SetMetaAgent(b.metaAgent)
	logging.Debug("meta-agent initialized",
		"check_interval", metaConfig.CheckInterval,
		"stuck_threshold", metaConfig.StuckThreshold)

	// === PHASE 6: Tree Planner ===
	// 5. Tree Planner for planned execution mode
	treePlannerConfig := agent.DefaultTreePlannerConfig()
	if b.cfg.Plan.PlanningTimeout > 0 {
		treePlannerConfig.PlanningTimeout = b.cfg.Plan.PlanningTimeout
	}
	treePlannerConfig.UseLLMExpansion = b.cfg.Plan.UseLLMExpansion

	// Apply algorithm from config
	if b.cfg.Plan.Algorithm != "" {
		algo := agent.SearchAlgorithm(b.cfg.Plan.Algorithm)
		switch algo {
		case agent.SearchAlgorithmBeam, agent.SearchAlgorithmMCTS, agent.SearchAlgorithmAStar:
			treePlannerConfig.Algorithm = algo
		default:
			logging.Warn("unknown tree search algorithm, using beam",
				"algorithm", b.cfg.Plan.Algorithm)
		}
	}

	b.treePlanner = agent.NewTreePlanner(
		treePlannerConfig,
		b.strategyOptimizer,
		nil, // Reflector will be set per-agent
		b.mainClient,
	)
	b.agentRunner.SetTreePlanner(b.treePlanner)
	b.agentRunner.SetPlanningModeEnabled(b.cfg.Plan.Enabled)
	b.agentRunner.SetRequireApprovalEnabled(b.cfg.Plan.RequireApproval)
	logging.Debug("tree planner initialized",
		"algorithm", treePlannerConfig.Algorithm,
		"beam_width", treePlannerConfig.BeamWidth,
		"max_depth", treePlannerConfig.MaxTreeDepth,
		"timeout", treePlannerConfig.PlanningTimeout)

	// === PHASE 2: Learning Infrastructure ===

	// 1. Shared Memory for inter-agent communication
	b.sharedMemory = agent.NewSharedMemory()
	b.agentRunner.SetSharedMemory(b.sharedMemory)
	logging.Debug("shared memory initialized")

	// 2. Example Store for few-shot learning
	if b.configDirErr == nil {
		var err error
		b.exampleStore, err = memory.NewExampleStore(b.configDir)
		if err != nil {
			logging.Warn("failed to create example store", "error", err)
		} else {
			memory.SeedDefaults(b.exampleStore)
			b.agentRunner.SetExampleStore(&exampleStoreAdapter{store: b.exampleStore})
			logging.Debug("example store initialized")
		}
	}

	// 3. Prompt Optimizer
	if b.configDirErr == nil {
		b.promptOptimizer = agent.NewPromptOptimizer(b.configDir)
		b.agentRunner.SetPromptOptimizer(b.promptOptimizer)
		logging.Debug("prompt optimizer initialized")
	}

	// 4. Smart Router with adaptive selection + model capability
	smartRouterCfg := router.DefaultSmartRouterConfig()
	b.modelCapability = router.InferModelCapability(b.cfg.API.GetActiveProvider(), b.cfg.Model.Name)
	smartRouterCfg.ModelCapability = b.modelCapability
	logging.Debug("model capability inferred", "provider", b.modelCapability.Provider, "tier", b.modelCapability.Tier.String())

	// Adapt for weaker models: self-review boost, weak model guidance, compact history.
	// Also activates when force_weak_optimizations is set in config — lets users opt
	// Strong-tier models (e.g. GLM 5.x) into weak-tier safeguards.
	weakMode := b.modelCapability.Tier < router.CapabilityStrong || b.cfg.Model.ForceWeakOptimizations
	if weakMode {
		if b.cfg.Tools.SmartValidation.SelfReviewThreshold > 2 &&
			(b.modelCapability.SelfReviewBoost || b.cfg.Model.ForceWeakOptimizations) {
			b.executor.SetSelfReviewThreshold(2)
		}
		b.agentRunner.SetWeakModelMode(true)
		if b.cfg.Model.ForceWeakOptimizations {
			logging.Info("force_weak_optimizations enabled — applying weak-tier safeguards to strong model",
				"model", b.cfg.Model.Name, "tier", b.modelCapability.Tier.String())
		}
	}

	b.smartRouter = router.NewSmartRouter(b.ctx, smartRouterCfg, b.executor, b.agentRunner, b.mainClient, b.workDir)
	if b.strategyOptimizer != nil {
		b.smartRouter.SetStrategyOptimizer(b.strategyOptimizer)
	}
	if b.exampleStore != nil {
		b.smartRouter.SetExampleStore(&routerExampleStoreAdapter{store: b.exampleStore})
	}
	logging.Debug("smart router initialized",
		"adaptive_enabled", smartRouterCfg.AdaptiveEnabled,
		"min_data_points", smartRouterCfg.MinDataPoints)

	return nil
}

// initIntegrations initializes optional feature integrations.
func (b *Builder) initIntegrations() error {
	// Wire up task manager to bash tool and apply config
	if bashTool, ok := b.registry.Get("bash"); ok {
		if bt, ok := bashTool.(*tools.BashTool); ok {
			bt.SetTaskManager(b.taskManager)
			bt.SetSandboxEnabled(b.cfg.Tools.Bash.Sandbox)
			// Set unrestricted mode for bash tool (skip command validation)
			sandboxOff := !b.cfg.Tools.Bash.Sandbox
			permissionOff := !b.cfg.Permission.Enabled
			bt.SetUnrestrictedMode(sandboxOff && permissionOff)
			logging.Debug("bash tool configured",
				"sandbox", b.cfg.Tools.Bash.Sandbox,
				"unrestricted", sandboxOff && permissionOff,
				"blocked_commands", len(b.cfg.Tools.Bash.BlockedCommands))
		}
	}

	// Wire up path validation for read and edit tools
	if readTool, ok := b.registry.Get("read"); ok {
		if rt, ok := readTool.(*tools.ReadTool); ok {
			rt.SetWorkDir(b.workDir)
			// Wire context predictor for predictive file loading
			if b.contextPredictor != nil {
				rt.SetPredictor(b.contextPredictor)
			}
			// Wire proactive context provider (Claude-Code-style package
			// sibling + paired test auto-read). Disabled if user opted
			// out in config.
			if pc := b.cfg.Tools.ProactiveContext; pc.Enabled {
				rt.SetProactiveContext(appcontext.NewProactiveReader(
					b.workDir, pc.MaxFiles, pc.MaxLinesPerFile,
				))
			}
		}
	}
	if editTool, ok := b.registry.Get("edit"); ok {
		if et, ok := editTool.(*tools.EditTool); ok {
			et.SetWorkDir(b.workDir)
			// Wire Read-before-Edit invariant. b.readTracker was created a
			// few lines above (shared with ReadTool) so both tools see the
			// same session record set.
			if b.readTracker != nil {
				et.SetReadTracker(b.readTracker)
			}
			et.SetRequireReadBeforeEdit(b.cfg.Tools.RequireReadBeforeEdit)
		}
	}

	// Set additional allowed directories from config
	if len(b.cfg.Tools.AllowedDirs) > 0 {
		if readTool, ok := b.registry.Get("read"); ok {
			if rt, ok := readTool.(*tools.ReadTool); ok {
				rt.SetAllowedDirs(b.cfg.Tools.AllowedDirs)
			}
		}
		if editTool, ok := b.registry.Get("edit"); ok {
			if et, ok := editTool.(*tools.EditTool); ok {
				et.SetAllowedDirs(b.cfg.Tools.AllowedDirs)
			}
		}
		if writeTool, ok := b.registry.Get("write"); ok {
			if wt, ok := writeTool.(*tools.WriteTool); ok {
				wt.SetAllowedDirs(b.cfg.Tools.AllowedDirs)
			}
		}
		if listDirTool, ok := b.registry.Get("list_dir"); ok {
			if lt, ok := listDirTool.(*tools.ListDirTool); ok {
				lt.SetAllowedDirs(b.cfg.Tools.AllowedDirs)
			}
		}
		if globTool, ok := b.registry.Get("glob"); ok {
			if gt, ok := globTool.(*tools.GlobTool); ok {
				gt.SetAllowedDirs(b.cfg.Tools.AllowedDirs)
			}
		}
		if grepTool, ok := b.registry.Get("grep"); ok {
			if gt, ok := grepTool.(*tools.GrepTool); ok {
				gt.SetAllowedDirs(b.cfg.Tools.AllowedDirs)
			}
		}
		if treeTool, ok := b.registry.Get("tree"); ok {
			if tt, ok := treeTool.(*tools.TreeTool); ok {
				tt.SetAllowedDirs(b.cfg.Tools.AllowedDirs)
			}
		}
		// Set allowed directories for file operation tools
		if copyTool, ok := b.registry.Get("copy"); ok {
			if ct, ok := copyTool.(*tools.CopyTool); ok {
				ct.SetAllowedDirs(b.cfg.Tools.AllowedDirs)
			}
		}
		if moveTool, ok := b.registry.Get("move"); ok {
			if mt, ok := moveTool.(*tools.MoveTool); ok {
				mt.SetAllowedDirs(b.cfg.Tools.AllowedDirs)
			}
		}
		if deleteTool, ok := b.registry.Get("delete"); ok {
			if dt, ok := deleteTool.(*tools.DeleteTool); ok {
				dt.SetAllowedDirs(b.cfg.Tools.AllowedDirs)
			}
		}
		if mkdirTool, ok := b.registry.Get("mkdir"); ok {
			if mt, ok := mkdirTool.(*tools.MkdirTool); ok {
				mt.SetAllowedDirs(b.cfg.Tools.AllowedDirs)
			}
		}
	}

	// Wire up undo manager
	if writeTool, ok := b.registry.Get("write"); ok {
		if wt, ok := writeTool.(*tools.WriteTool); ok {
			wt.SetUndoManager(b.undoManager)
		}
	}
	if editTool, ok := b.registry.Get("edit"); ok {
		if et, ok := editTool.(*tools.EditTool); ok {
			et.SetUndoManager(b.undoManager)
		}
	}
	if batchTool, ok := b.registry.Get("batch"); ok {
		if bt, ok := batchTool.(*tools.BatchTool); ok {
			bt.SetUndoManager(b.undoManager)
		}
	}
	// Wire up undo manager for file operation tools
	if copyTool, ok := b.registry.Get("copy"); ok {
		if ct, ok := copyTool.(*tools.CopyTool); ok {
			ct.SetUndoManager(b.undoManager)
		}
	}
	if moveTool, ok := b.registry.Get("move"); ok {
		if mt, ok := moveTool.(*tools.MoveTool); ok {
			mt.SetUndoManager(b.undoManager)
		}
	}
	if deleteTool, ok := b.registry.Get("delete"); ok {
		if dt, ok := deleteTool.(*tools.DeleteTool); ok {
			dt.SetUndoManager(b.undoManager)
		}
	}
	if mkdirTool, ok := b.registry.Get("mkdir"); ok {
		if mt, ok := mkdirTool.(*tools.MkdirTool); ok {
			mt.SetUndoManager(b.undoManager)
		}
	}

	// Wire up agent runner to task tool
	runnerAdapter := &agentRunnerAdapter{runner: b.agentRunner}
	if taskTool, ok := b.registry.Get("task"); ok {
		if tt, ok := taskTool.(*tools.TaskTool); ok {
			tt.SetRunner(runnerAdapter)
		}
	}

	// Wire up task_output tool
	if taskOutputTool, ok := b.registry.Get("task_output"); ok {
		if tot, ok := taskOutputTool.(*tools.TaskOutputTool); ok {
			tot.SetManager(b.taskManager)
			tot.SetRunner(runnerAdapter)
		}
	}

	// Wire up task_stop tool
	if taskStopTool, ok := b.registry.Get("task_stop"); ok {
		if tst, ok := taskStopTool.(*tools.TaskStopTool); ok {
			tst.SetManager(b.taskManager)
			tst.SetRunner(runnerAdapter)
		}
	}

	// Configure web search
	if webSearchTool, ok := b.registry.Get("web_search"); ok {
		if wst, ok := webSearchTool.(*tools.WebSearchTool); ok {
			if b.cfg.Web.SearchAPIKey != "" {
				wst.SetAPIKey(b.cfg.Web.SearchAPIKey)
			}
			switch b.cfg.Web.SearchProvider {
			case "google":
				wst.SetProvider(tools.SearchProviderGoogle)
				wst.SetGoogleCX(b.cfg.Web.GoogleCX)
			case "duckduckgo":
				wst.SetProvider(tools.SearchProviderDuckDuckGo)
			}
		}
	}

	// Wire up kill_shell tool
	if killShellTool, ok := b.registry.Get("kill_shell"); ok {
		if kst, ok := killShellTool.(*tools.KillShellTool); ok {
			kst.SetManager(b.taskManager)
		}
	}

	// Initialize memory store
	if b.cfg.Memory.Enabled && b.configDirErr == nil {
		memoryStore, err := memory.NewStore(b.configDir, b.workDir, b.cfg.Memory.MaxEntries)
		if err != nil {
			logging.Warn("failed to create memory store", "error", err)
		} else {
			if memoryTool, ok := b.registry.Get("memory"); ok {
				if mt, ok := memoryTool.(*tools.MemoryTool); ok {
					mt.SetStore(memoryStore)
				}
			}
			// Store on app for /memory command access
			b.memStore = memoryStore
		}

		// Initialize error store for learning from errors (Phase 3)
		errorStore, err := memory.NewErrorStore(b.configDir)
		if err != nil {
			logging.Warn("failed to create error store", "error", err)
		} else {
			b.errorStore = errorStore
			b.agentRunner.SetErrorStore(errorStore)
			b.executor.SetErrorLearner(&errorLearnerAdapter{store: errorStore})
			logging.Debug("error store initialized for learning from errors")
		}
	}

	// Initialize project learning for memorize tool
	if b.workDir != "" {
		projectLearning, err := memory.GetSharedProjectLearning(b.workDir)
		if err != nil {
			logging.Debug("failed to create project learning", "error", err)
		} else {
			b.projectLearning = projectLearning
			if memorizeTool, ok := b.registry.Get("memorize"); ok {
				if mt, ok := memorizeTool.(*tools.MemorizeTool); ok {
					mt.SetLearning(projectLearning)
					logging.Debug("memorize tool wired with project learning")
				}
			}
		}
	}

	// Wire context predictor to agent runner for enhanced error recovery
	if b.contextPredictor != nil {
		b.agentRunner.SetPredictor(&contextPredictorAdapter{predictor: b.contextPredictor})
		logging.Debug("context predictor wired to agent runner")

		// Also wire to context enricher for prediction-based enrichment
		if enricher := b.executor.GetContextEnricher(); enricher != nil {
			enricher.SetPredictor(&contextPredictorToolsAdapter{predictor: b.contextPredictor})
			logging.Debug("context predictor wired to context enricher")
		}
	}

	// Use compact history strategy for weaker models (smaller effective context)
	if b.modelCapability != nil && b.modelCapability.Tier < router.CapabilityStrong && b.contextManager != nil {
		b.contextManager.SetSummaryStrategy(appcontext.CompactStrategy())
		logging.Debug("using compact summary strategy for model tier", "tier", b.modelCapability.Tier.String())
	}

	// Initialize search cache
	if b.cfg.Cache.Enabled {
		b.searchCache = cache.NewSearchCache(b.cfg.Cache.Capacity, b.cfg.Cache.TTL)
		if grepTool, ok := b.registry.Get("grep"); ok {
			if gt, ok := grepTool.(*tools.GrepTool); ok {
				gt.SetCache(b.searchCache)
			}
		}
		if globTool, ok := b.registry.Get("glob"); ok {
			if gt, ok := globTool.(*tools.GlobTool); ok {
				gt.SetCache(b.searchCache)
			}
		}
	}

	// Wire context predictor to search tools for predictive file loading
	if b.contextPredictor != nil {
		if grepTool, ok := b.registry.Get("grep"); ok {
			if gt, ok := grepTool.(*tools.GrepTool); ok {
				gt.SetPredictor(b.contextPredictor)
			}
		}
		if globTool, ok := b.registry.Get("glob"); ok {
			if gt, ok := globTool.(*tools.GlobTool); ok {
				gt.SetPredictor(b.contextPredictor)
			}
		}
	}

	// Initialize rate limiter
	if b.cfg.RateLimit.Enabled {
		b.rateLimiter = ratelimit.NewLimiter(ratelimit.Config{
			Enabled:           true,
			RequestsPerMinute: b.cfg.RateLimit.RequestsPerMinute,
			TokensPerMinute:   b.cfg.RateLimit.TokensPerMinute,
			BurstSize:         b.cfg.RateLimit.BurstSize,
		})
		b.mainClient.SetRateLimiter(b.rateLimiter)
	}

	// Initialize audit logger
	if b.cfg.Audit.Enabled && b.configDirErr == nil {
		auditLogger, err := audit.NewLogger(b.configDir, b.session.ID, audit.Config{
			Enabled:       true,
			MaxEntries:    b.cfg.Audit.MaxEntries,
			MaxResultLen:  b.cfg.Audit.MaxResultLen,
			RetentionDays: b.cfg.Audit.RetentionDays,
		})
		if err != nil {
			logging.Warn("failed to create audit logger", "error", err)
		} else {
			b.auditLogger = auditLogger
			b.executor.SetAuditLogger(auditLogger)
			// Cleanup old audit files at startup
			if removed, err := auditLogger.CleanupOldFiles(); err != nil {
				logging.Debug("failed to cleanup old audit files", "error", err)
			} else if removed > 0 {
				logging.Debug("cleaned up old audit files", "removed", removed)
			}
		}
	}
	b.executor.SetSessionID(b.session.ID)

	// Initialize file watcher
	if b.cfg.Watcher.Enabled {
		gitIgnore := git.NewGitIgnore(b.workDir)
		_ = gitIgnore.Load()
		fileWatcher, err := watcher.NewWatcher(b.workDir, gitIgnore, watcher.Config{
			Enabled:    true,
			DebounceMs: b.cfg.Watcher.DebounceMs,
			MaxWatches: b.cfg.Watcher.MaxWatches,
		})
		if err != nil {
			logging.Warn("failed to create file watcher", "error", err)
		} else {
			b.fileWatcher = fileWatcher
		}
	}

	// Wire up plan mode tools
	if enterPlanTool, ok := b.registry.Get("enter_plan_mode"); ok {
		if ept, ok := enterPlanTool.(*tools.EnterPlanModeTool); ok {
			ept.SetManager(b.planManager)
		}
	}
	if updateProgressTool, ok := b.registry.Get("update_plan_progress"); ok {
		if upt, ok := updateProgressTool.(*tools.UpdatePlanProgressTool); ok {
			upt.SetManager(b.planManager)
		}
	}
	if getPlanStatusTool, ok := b.registry.Get("get_plan_status"); ok {
		if gpst, ok := getPlanStatusTool.(*tools.GetPlanStatusTool); ok {
			gpst.SetManager(b.planManager)
		}
	}
	if exitPlanTool, ok := b.registry.Get("exit_plan_mode"); ok {
		if ept, ok := exitPlanTool.(*tools.ExitPlanModeTool); ok {
			ept.SetManager(b.planManager)
		}
	}

	// Wire up shared_memory tool (Phase 2)
	if smt, ok := b.registry.Get("shared_memory"); ok {
		if t, ok := smt.(*tools.SharedMemoryTool); ok {
			if b.sharedMemory != nil {
				// Create adapter for shared memory
				adapter := &sharedMemoryToolAdapter{memory: b.sharedMemory}
				t.SetMemory(adapter)
				logging.Debug("shared_memory tool wired")
			}
		}
	}

	// Wire up coordinate tool with coordinator factory
	if coordTool, ok := b.registry.Get("coordinate"); ok {
		if ct, ok := coordTool.(*tools.CoordinateTool); ok {
			runner := b.agentRunner
			ct.SetCoordinatorFactory(func() any {
				coordConfig := &agent.CoordinatorConfig{MaxParallel: 3}
				coord := agent.NewCoordinator(b.ctx, runner, coordConfig)
				return &coordinatorToolAdapter{coord: coord}
			})
			logging.Debug("coordinate tool wired")
		}
	}

	// Initialize MCP (Model Context Protocol)
	if b.cfg.MCP.Enabled && len(b.cfg.MCP.Servers) > 0 {
		// Convert config to MCP server configs
		mcpConfigs := make([]*mcp.ServerConfig, 0, len(b.cfg.MCP.Servers))
		permLevels := make(map[string]string, len(b.cfg.MCP.Servers))
		for _, s := range b.cfg.MCP.Servers {
			mcpConfigs = append(mcpConfigs, &mcp.ServerConfig{
				Name:            s.Name,
				Transport:       s.Transport,
				Command:         s.Command,
				Args:            s.Args,
				Env:             s.Env,
				URL:             s.URL,
				Headers:         s.Headers,
				AutoConnect:     s.AutoConnect,
				Timeout:         s.Timeout,
				MaxRetries:      s.MaxRetries,
				RetryDelay:      s.RetryDelay,
				ToolPrefix:      s.ToolPrefix,
				PermissionLevel: s.PermissionLevel,
			})
			permLevels[s.Name] = s.PermissionLevel
		}

		b.mcpManager = mcp.NewManager(mcpConfigs)
		// Install the tools-changed dispatcher BEFORE ConnectAll so that any
		// notification arriving during initial connect handshakes is
		// captured (queued if App not yet ready, delivered otherwise).
		b.mcpManager.SetToolsChangedCallback(b.dispatchMCPToolsChanged)

		// Count auto-connect servers so we can summarise connect results for the UI.
		autoCount := 0
		for _, cfg := range mcpConfigs {
			if cfg.AutoConnect {
				autoCount++
			}
		}

		// Connect to auto-connect servers
		var connectErr error
		if err := b.mcpManager.ConnectAll(b.ctx); err != nil {
			connectErr = err
			logging.Warn("some MCP servers failed to connect", "error", err)
			// Continue - graceful degradation
		}

		// Build summary for deferred UI toast (UI is not running yet — we'd lose the message).
		connected := len(b.mcpManager.GetConnectedServers())
		toolCount := len(b.mcpManager.GetTools())
		if autoCount > 0 {
			if connectErr != nil {
				b.mcpConnectSummary = fmt.Sprintf("MCP: %d/%d servers connected, %d tools — some servers failed",
					connected, autoCount, toolCount)
			} else if connected > 0 {
				b.mcpConnectSummary = fmt.Sprintf("MCP: %d/%d servers connected, %d tools", connected, autoCount, toolCount)
			}
		}

		// Register MCP tools into the registry and apply per-server risk
		// overrides so the permission layer routes prompts according to each
		// server's configured trust tier instead of the default RiskMedium.
		for _, tool := range b.mcpManager.GetTools() {
			if err := b.registry.Register(tool); err != nil {
				logging.Warn("failed to register MCP tool",
					"tool", tool.Name(), "error", err)
				continue
			}
			if mt, ok := tool.(*mcp.MCPTool); ok {
				if lvl, known := permLevels[mt.GetServerName()]; known {
					permission.SetToolRiskOverride(tool.Name(), permission.ParseRiskLevel(lvl))
				}
			}
		}

		// Refresh tools on client
		b.mainClient.SetTools(b.registry.GeminiTools())

		// Start health check for connected servers
		if len(b.mcpManager.GetConnectedServers()) > 0 && b.cfg.MCP.HealthCheckInterval > 0 {
			b.mcpManager.StartHealthCheck(b.ctx, b.cfg.MCP.HealthCheckInterval)
			logging.Debug("MCP health check started", "interval", b.cfg.MCP.HealthCheckInterval)
		}

		logging.Debug("MCP initialized",
			"servers", len(b.cfg.MCP.Servers),
			"tools", len(b.mcpManager.GetTools()))
	}

	return nil
}

// initUI creates and configures the TUI model.
func (b *Builder) initUI() error {
	b.tuiModel = ui.NewModel()
	b.tuiModel.SetShowTokens(b.cfg.UI.ShowTokenUsage)

	b.tuiModel.SetBellEnabled(b.cfg.UI.Bell)

	// Filter models by current provider/backend
	provider := b.cfg.Model.Provider
	if provider == "" {
		provider = b.cfg.API.Backend
	}
	if provider == "" {
		provider = "glm" // Default
	}

	var uiModels []ui.ModelInfo
	for _, m := range client.GetModelsForProvider(provider) {
		uiModels = append(uiModels, ui.ModelInfo{
			ID:          m.ID,
			Name:        m.Name,
			Description: m.Description,
		})
	}
	b.tuiModel.SetAvailableModels(uiModels)
	b.tuiModel.SetCurrentModel(b.cfg.Model.Name)

	// Set version for display in status bar
	if b.cfg.Version != "" {
		b.tuiModel.SetVersion(b.cfg.Version)
	}

	if b.projectInfo.Type != appcontext.ProjectTypeUnknown {
		b.tuiModel.SetProjectInfo(b.projectInfo.Type.String(), b.projectInfo.Name)
	}

	// Set git branch for status bar display
	if branch := git.GetCurrentBranch(b.workDir); branch != "" {
		b.tuiModel.SetGitBranch(branch)
	}

	return nil
}

// wireDependencies sets up callbacks and inter-component connections.
func (b *Builder) wireDependencies() error {
	app := b.assembleApp()

	if app.promptBuilder != nil {
		if b.memStore != nil && b.cfg.Memory.AutoInject {
			app.promptBuilder.SetMemoryStore(b.memStore)
		}
		if b.projectLearning != nil {
			app.promptBuilder.SetProjectLearning(b.projectLearning)
		}
		if b.workingMemory != nil {
			app.promptBuilder.SetWorkingMemory(b.workingMemory)
		}
		// Re-assert provider selection in case assembleApp picked up a
		// late-bound active provider (e.g., set via CLI flag, not yaml).
		app.promptBuilder.SetProvider(b.cfg.API.GetActiveProvider())
	}
	if app.sessionMemory != nil && b.projectLearning != nil {
		app.sessionMemory.SetProjectLearning(b.projectLearning)
	}

	// Plan-mode gate: executor consults this predicate before running any
	// tool. Prevents a hallucinated write/bash call from bypassing the
	// schema filter. Must be installed AFTER app is assembled so the
	// callback closes over the final *App pointer.
	if app.executor != nil {
		app.executor.SetPlanModeCheck(app.IsPlanningModeEnabled)
	}

	// Set up status callback for clients
	statusCb := &appStatusCallback{app: app}
	attachStatusCallback(b.mainClient, statusCb)

	// Notify UI on MCP health changes. Rate-limited per server so a flapping
	// server can't flood the status area with cascading toasts.
	if b.mcpManager != nil {
		healthToasts := newHealthToastLimiter()
		b.mcpManager.SetHealthChangeCallback(func(name string, healthy bool) {
			if !healthToasts.ShouldEmit(name, time.Now()) {
				return
			}
			if healthy {
				app.safeSendToProgram(ui.StatusUpdateMsg{
					Type:    ui.StatusStreamResume,
					Message: fmt.Sprintf("MCP server %q reconnected", name),
				})
			} else {
				app.safeSendToProgram(ui.StatusUpdateMsg{
					Type:    ui.StatusRecoverableError,
					Message: fmt.Sprintf("MCP server %q disconnected", name),
				})
			}
		})

		// Publish the App to the already-installed tools-changed dispatcher
		// and drain any events that arrived during startup (between
		// ConnectAll and App creation).
		b.mcpDispatchMu.Lock()
		b.mcpDispatchApp = app
		pending := b.mcpPendingToolsChanged
		b.mcpPendingToolsChanged = nil
		b.mcpDispatchMu.Unlock()
		for _, name := range pending {
			// Registry-level reconciliation only; UI isn't up yet for toasts.
			app.SyncMCPToolsForServer(name)
		}
	}

	// Set up executor handler
	b.executor.SetHandler(&tools.ExecutionHandler{
		OnText: func(text string) {
			app.touchStepHeartbeat()
			if app.program != nil {
				app.safeSendToProgram(ui.StreamTextMsg(text))
			}

			// Track streamed text for token estimation
			app.mu.Lock()
			app.streamedChars += len(text)
			chars := app.streamedChars
			// Use content-aware estimation (code/JSON/prose heuristics)
			app.streamedEstimatedTokens += appcontext.EstimateTokens(text)
			estimatedTokens := app.streamedEstimatedTokens
			app.mu.Unlock()

			// Send estimated token update every ~500 chars (~125 tokens) for smoother UI
			if chars/500 > (chars-len(text))/500 {
				if app.program != nil {
					app.safeSendToProgram(ui.StreamTokenUpdateMsg{
						EstimatedOutputTokens: estimatedTokens,
					})
				}
			}
		},
		OnThinking: func(text string) {
			app.touchStepHeartbeat()
			logging.Debug("OnThinking callback fired", "text_length", len(text), "text_preview", text[:min(len(text), 80)])
			if app.program != nil {
				app.safeSendToProgram(ui.StreamThinkingMsg(text))
			}
		},
		OnToolStart: func(name string, args map[string]any) {
			app.touchStepHeartbeat()
			// Track tools used for response metadata
			app.mu.Lock()
			app.responseToolsUsed = append(app.responseToolsUsed, name)
			app.currentToolContext = toolContextSummary(name, args)
			app.mu.Unlock()

			// Task 5.8: Record tool usage for pattern learning
			app.recordToolUsage(name)

			// Session memory: track tool call for extraction threshold
			if app.sessionMemory != nil {
				app.sessionMemory.RecordToolCall()
			}

			if app.program != nil {
				app.safeSendToProgram(ui.ToolCallMsg{Name: name, Args: args})
			}
			app.journalEvent("tool_start", map[string]any{
				"tool": name,
				"args": args,
			})
			app.saveRecoverySnapshot("")

			// Record side effects for active plan step (idempotency guard).
			if app.planManager != nil && app.planManager.IsExecuting() {
				if p := app.planManager.GetCurrentPlan(); p != nil {
					stepID := app.planManager.GetCurrentStepID()
					if stepID > 0 {
						app.captureStepRollbackFromToolArgs(p, stepID, name, args)
						p.RecordStepEffect(stepID, name, args)
						_ = app.planManager.SaveCurrentPlan()
					}
				}
			}
		},
		OnToolEnd: func(name string, args map[string]any, result tools.ToolResult) {
			app.touchStepHeartbeat()
			app.mu.Lock()
			app.currentToolContext = ""
			app.mu.Unlock()

			app.recordResponseTouchedPaths(name, args, result)
			app.recordResponseCommand(name, args, result)
			app.recordResponseEvidence(name, args, result)

			if app.program != nil {
				app.safeSendToProgram(ui.ToolResultMsg{Name: name, Content: result.Content})
			}
			app.journalEvent("tool_end", map[string]any{
				"tool":    name,
				"success": result.Success,
			})

			// Refresh token count after each tool completes (context grew)
			go app.refreshTokenCount()
		},
		OnToolProgress: func(name string, elapsed time.Duration) {
			app.touchStepHeartbeat()
			// Heartbeat for long-running tools - keeps UI timeout from triggering
			if app.program != nil {
				app.mu.Lock()
				ctx := app.currentToolContext
				app.mu.Unlock()
				app.safeSendToProgram(ui.ToolProgressMsg{Name: name, Elapsed: elapsed, CurrentStep: ctx})
			}
		},
		OnToolDetailedProgress: func(name string, progress float64, currentStep string) {
			app.touchStepHeartbeat()
			// Rich progress from within tools — update both context and UI
			if currentStep != "" {
				app.mu.Lock()
				app.currentToolContext = currentStep
				app.mu.Unlock()
			}
			if app.program != nil {
				app.safeSendToProgram(ui.ToolProgressMsg{
					Name:        name,
					Progress:    progress,
					CurrentStep: currentStep,
				})
			}
		},
		OnError: func(err error) {
			app.journalEvent("tool_error", map[string]any{
				"error": err.Error(),
			})
			if app.program != nil {
				app.safeSendToProgram(ui.ErrorMsg(err))
			}
		},
		OnWarning: func(warning string) {
			app.touchStepHeartbeat()
			if warning == "" || app.program == nil {
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

			app.safeSendToProgram(ui.StatusUpdateMsg{
				Type:    ui.StatusWarning,
				Message: warning,
				Details: details,
			})
		},
		OnInlineDiff: func(filePath, oldText, newText string) {
			if app.program != nil {
				app.safeSendToProgram(ui.InlineDiffMsg{
					FilePath: filePath,
					OldText:  oldText,
					NewText:  newText,
				})
			}
		},
		OnLoopIteration: func(iteration int, toolsUsed int) {
			if app.program != nil {
				app.safeSendToProgram(ui.LoopIterationMsg{
					Iteration: iteration,
					ToolsUsed: toolsUsed,
				})
			}
			// Refresh token display between executor rounds so the bar stays current
			app.sendTokenUsageUpdate()
		},
		OnTokenUpdate: func(inputTokens, outputTokens int) {
			// Use input tokens only for context bar — output tokens from this turn
			// become input tokens on the next turn, but the context manager tracks
			// the actual accumulated context more accurately via UpdateTokenCount.
			if app.program != nil && inputTokens > 0 {
				maxTokens := 0
				if app.contextManager != nil {
					if usage := app.contextManager.GetTokenUsage(); usage != nil {
						maxTokens = usage.MaxTokens
					}
				}
				var pct float64
				if maxTokens > 0 {
					pct = float64(inputTokens) / float64(maxTokens)
				}
				app.safeSendToProgram(ui.TokenUsageMsg{
					Tokens:      inputTokens,
					MaxTokens:   maxTokens,
					PercentUsed: pct,
					NearLimit:   pct > 0.8,
				})
			}
		},
		OnFilePeek: func(filePath, title, content, action string) {
			if app.program != nil {
				app.safeSendToProgram(ui.FilePeekMsg{
					FilePath: filePath,
					Title:    title,
					Content:  content,
					Action:   action,
				})
			}
		},
		OnMemoryNotify: func(action, summary string) {
			if b.projectMemory != nil {
				if err := b.projectMemory.Reload(); err != nil {
					logging.Debug("failed to reload project memory after memory update", "error", err)
				}
			}
			if app.promptBuilder != nil {
				app.promptBuilder.Invalidate()
			}
			if app.program != nil {
				msg := "Memory " + action
				if summary != "" {
					if len(summary) > 50 {
						summary = summary[:47] + "..."
					}
					msg += ": " + summary
				}
				app.safeSendToProgram(ui.LearningInsightMsg{Message: msg})
			}
		},
	})

	// Wire pin_context: updater must be set BEFORE LoadPersistedPin —
	// LoadPersistedPin short-circuits when updater is nil, which meant
	// a previously-pinned .gokin/pinned_context.md was silently dropped
	// on startup. And without the updater, Execute() returned "pinned
	// context not supported by this agent" to the model — a silent
	// capability failure Kimi would report as "I don't have pin".
	if pinTool, ok := b.registry.Get("pin_context"); ok {
		if pt, ok := pinTool.(*tools.PinContextTool); ok {
			pt.SetWorkDir(b.workDir)
			if app.promptBuilder != nil {
				pt.SetUpdater(app.promptBuilder.SetPinnedContent)
			}
			pt.LoadPersistedPin()
		}
	}

	// Wire history_search: same class of bug as pin_context. Tool ships
	// in the ToolSetMemory declaration (now always-on for Kimi), but
	// without this getter it returns "history search not supported by
	// this agent" — a silent false negative the model reports as
	// "I can't search history".
	if histTool, ok := b.registry.Get("history_search"); ok {
		if hs, ok := histTool.(*tools.HistorySearchTool); ok {
			if app.session != nil {
				hs.SetHistoryGetter(app.session.GetHistory)
			}
		}
	}

	// Wire session memory update notifications
	if app.sessionMemory != nil {
		app.sessionMemory.SetOnUpdate(func() {
			if app.promptBuilder != nil {
				app.promptBuilder.Invalidate()
			}
			app.safeSendToProgram(ui.LearningInsightMsg{Message: "Session memory updated"})
		})
	}

	// Set up TUI callbacks
	b.tuiModel.SetCallbacks(app.handleSubmit, app.handleQuit)
	b.tuiModel.SetWorkDir(b.workDir)
	if b.commandHandler != nil {
		b.tuiModel.SetCommandAliases(b.commandHandler.Aliases())
	}
	b.tuiModel.SetPermissionCallback(app.handlePermissionDecision)
	b.tuiModel.SetQuestionCallback(app.handleQuestionAnswer)
	b.tuiModel.SetPlanApprovalCallback(app.handlePlanApproval)
	b.tuiModel.SetModelSelectCallback(app.handleModelSelect)
	b.tuiModel.SetDiffDecisionCallback(app.handleDiffDecision)
	b.tuiModel.SetMultiDiffDecisionCallback(app.handleMultiDiffDecision)

	// Set up cancel callback for ESC interrupt
	b.tuiModel.SetCancelCallback(app.CancelProcessing)

	// Set up plan approval with feedback callback
	b.tuiModel.SetPlanApprovalWithFeedbackCallback(app.handlePlanApprovalWithFeedback)

	// Set up permissions toggle callback and initial state
	b.tuiModel.SetPermissionsEnabled(b.cfg.Permission.Enabled)
	b.tuiModel.SetPermissionsToggleCallback(app.TogglePermissions)

	// Set up sandbox toggle callback and initial state
	b.tuiModel.SetSandboxEnabled(b.cfg.Tools.Bash.Sandbox)
	b.tuiModel.SetSandboxToggleCallback(app.ToggleSandbox, app.GetSandboxState)

	// Set up planning mode toggle callback (async to avoid blocking UI)
	b.tuiModel.SetPlanningModeToggleCallback(app.TogglePlanningModeAsync)
	// Shift+Tab uses the session-mode cycle (Normal → Plan → YOLO → Normal)
	// by default when this callback is set. The plan-only toggle above is
	// kept as a fallback for contexts that only want binary plan toggle.
	b.tuiModel.SetSessionModeCycleCallback(app.CycleSessionModeAsync)

	// Set up command palette integration
	hasAuth := config.AnyProviderHasKey(&b.cfg.API)
	paletteCtx := commands.NewPaletteContext(b.workDir, hasAuth)
	paletteProvider := commands.NewPaletteProvider(b.commandHandler, paletteCtx)
	b.tuiModel.SetPaletteProvider(paletteProvider)
	b.tuiModel.RegisterPaletteActions()

	// Set up plan approval callback for context compaction
	b.agentRunner.SetOnPlanApproved(app.CompactContextWithPlan)

	// Set up context compaction notifications
	if b.contextManager != nil {
		b.contextManager.OnCompact = func(oldTokens, newTokens, removedMessages int, reason string) {
			if app.tui != nil {
				saved := oldTokens - newTokens
				msg := fmt.Sprintf("✂️ Context Compacted (%s) - Saved %d tokens.", reason, saved)
				app.tui.AddSystemMessage(msg)
			}
		}
		b.contextManager.OnOptimizeStart = func(reason string) {
			if app.program != nil {
				app.safeSendToProgram(ui.StatusUpdateMsg{
					Type:    ui.StatusInfo,
					Message: fmt.Sprintf("Optimizing context (%s)...", reason),
				})
			}
		}
	}

	// Set up background task tracking callbacks for UI
	b.agentRunner.SetOnAgentStart(func(id, agentType, description string) {
		if app.program != nil {
			// Truncate description if too long
			desc := description
			if len(desc) > 50 {
				desc = desc[:47] + "..."
			}
			app.safeSendToProgram(ui.BackgroundTaskMsg{
				ID:          id,
				Type:        "agent",
				Description: desc,
				Status:      "running",
			})
		}
	})
	b.agentRunner.SetOnAgentComplete(func(id string, result *agent.AgentResult) {
		if app.program != nil {
			status := "completed"
			if result != nil {
				switch result.Status {
				case agent.AgentStatusFailed:
					status = "failed"
				case agent.AgentStatusCancelled:
					status = "cancelled"
				}
			}
			app.safeSendToProgram(ui.BackgroundTaskMsg{
				ID:     id,
				Type:   "agent",
				Status: status,
			})
		}

		// Send native notification on agent completion
		nm := app.executor.GetNotificationManager()
		if nm != nil {
			if result != nil && result.Status == agent.AgentStatusFailed {
				nm.NotifyError("agent", "Task failed", result.Error)
			} else {
				nm.NotifySuccess("agent", "Task completed", nil, 0)
			}
		}

		// Inject completion notification for model awareness
		if result != nil && result.Completed {
			var msg string
			switch result.Status {
			case agent.AgentStatusCompleted:
				output := result.Output
				if runes := []rune(output); len(runes) > 500 {
					output = string(runes[:500]) + "..."
				}
				msg = fmt.Sprintf("Background agent %s (%s) completed in %s. Output: %s",
					id, result.Type, result.Duration.Round(time.Millisecond), output)
			case agent.AgentStatusFailed:
				msg = fmt.Sprintf("Background agent %s (%s) failed: %s",
					id, result.Type, result.Error)
			}
			if msg != "" {
				app.executor.AddPendingNotification(msg)
			}
		}
	})

	b.agentRunner.SetOnAgentProgress(func(id string, progress *agent.AgentProgress) {
		if app.program != nil {
			total := progress.TotalSteps
			if total < 1 {
				total = 1
			}
			app.safeSendToProgram(ui.BackgroundTaskProgressMsg{
				ID:            id,
				Progress:      float64(progress.CurrentStep) / float64(total),
				CurrentStep:   progress.CurrentStep,
				TotalSteps:    progress.TotalSteps,
				CurrentAction: progress.CurrentAction,
				ToolsUsed:     progress.ToolsUsed,
				Elapsed:       progress.Elapsed,
			})
		}
	})

	b.agentRunner.SetOnScratchpadUpdate(func(content string) {
		app.mu.Lock()
		app.scratchpad = content
		app.mu.Unlock()

		if app.session != nil {
			app.session.SetScratchpad(content)
		}

		if app.program != nil {
			app.safeSendToProgram(ui.ScratchpadMsg(content))
		}
	})

	// Wire thinking callback for sub-agents
	b.agentRunner.SetOnThinking(func(text string) {
		if app.program != nil {
			app.safeSendToProgram(ui.StreamThinkingMsg(text))
		}
	})

	// Wire sub-agent activity to UI. `prompt` is only set on the "start"
	// status — it becomes the feed's description for the agent entry.
	b.agentRunner.SetOnSubAgentActivity(func(agentID, agentType, prompt, toolName string, args map[string]any, status string) {
		if app.program != nil {
			app.safeSendToProgram(ui.SubAgentActivityMsg{
				AgentID:   agentID,
				AgentType: agentType,
				Task:      prompt,
				ToolName:  toolName,
				ToolArgs:  args,
				Status:    status,
			})
		}
	})

	// Wire shell task completion notifications for model awareness
	b.taskManager.SetCompletionHandler(func(task *tasks.Task) {
		info := task.GetInfo()
		var msg string
		if info.Status == "completed" {
			output := info.Output
			if runes := []rune(output); len(runes) > 500 {
				output = string(runes[:500]) + "..."
			}
			msg = fmt.Sprintf("Background shell task %s completed (exit %d). Output: %s",
				info.ID, info.ExitCode, output)
		} else if info.Status == "failed" {
			output := info.Output
			if runes := []rune(output); len(runes) > 500 {
				output = string(runes[:500]) + "..."
			}
			msg = fmt.Sprintf("Background shell task %s failed (exit %d). Output: %s",
				info.ID, info.ExitCode, output)
		}
		if msg != "" {
			app.executor.AddPendingNotification(msg)
		}
	})

	// Set up apply code block callback
	b.tuiModel.SetApplyCodeBlockCallback(app.handleApplyCodeBlock)

	// Set up permission callback
	b.permManager.SetPromptHandler(app.promptPermission)

	// Set up ask_user tool
	if askUserTool, ok := b.registry.Get("ask_user"); ok {
		if aut, ok := askUserTool.(*tools.AskUserTool); ok {
			aut.SetHandler(app.promptQuestion)
		}
	}

	// Set up plan approval
	b.planManager.SetApprovalHandler(app.promptPlanApproval)
	b.planManager.SetLintHandler(app.lintPlanBeforeApproval)

	// Set up plan progress updates
	b.planManager.SetProgressUpdateHandler(app.handlePlanProgressUpdate)

	// Set up diff preview (skip if permissions are disabled — no approval needed)
	if b.cfg.DiffPreview.Enabled && b.cfg.Permission.Enabled {
		diffAdapter := &diffHandlerAdapter{app: app}
		if writeTool, ok := b.registry.Get("write"); ok {
			if wt, ok := writeTool.(*tools.WriteTool); ok {
				wt.SetDiffHandler(diffAdapter)
				wt.SetDiffEnabled(true)
			}
		}
		if editTool, ok := b.registry.Get("edit"); ok {
			if et, ok := editTool.(*tools.EditTool); ok {
				et.SetDiffHandler(diffAdapter)
				et.SetDiffEnabled(true)
			}
		}
		if b.agentRunner != nil {
			b.agentRunner.SetWorkspaceReviewHandler(app.reviewWorkspaceChanges)
		}
	}

	// === PHASE 5: Wire UIBroadcaster to Coordinator ===
	if b.coordinator != nil {
		broadcaster := &uiBroadcasterAdapter{app: app}
		b.coordinator.SetUIBroadcaster(broadcaster)

		// Wire coordinator task callbacks to push agent tree snapshots to TUI
		b.coordinator.SetCallbacks(
			func(task *agent.CoordinatedTask) {
				// On task start — rebuild tree snapshot
				app.sendAgentTreeUpdate()
			},
			func(task *agent.CoordinatedTask, result *agent.AgentResult) {
				// On task complete — rebuild tree snapshot
				app.sendAgentTreeUpdate()
			},
			nil, // onAllComplete not needed for UI
		)

		logging.Debug("coordinator UI broadcaster wired")
	}

	// Wire MetaAgent intervention callback to TUI toast
	if b.metaAgent != nil {
		b.metaAgent.SetInterventionCallback(func(agentID string, reason string, action string) {
			if app.program != nil {
				msg := fmt.Sprintf("⚡ Meta-Agent: %s → %s", action, reason)
				app.safeSendToProgram(ui.LearningInsightMsg{Message: msg})
			}
		})
	}

	// Wire Agent Runner onThinking callback to TUI
	if b.agentRunner != nil {
		b.agentRunner.SetOnThinking(func(text string) {
			if app.program != nil {
				app.safeSendToProgram(ui.StreamThinkingMsg(text))
			}
		})
	}

	return nil
}

// dispatchMCPToolsChanged is invoked by the MCP manager whenever a server's
// tool list is refreshed (either via explicit /mcp refresh or a server-sent
// tools/list_changed notification). It runs concurrently with the main
// build path: during startup it may fire before the App is ready.
//
// If the App is ready, we reconcile the registry and emit a UI toast. If
// not, we queue the server name; wireDependencies drains the queue once
// assembleApp has run.
func (b *Builder) dispatchMCPToolsChanged(name string) {
	b.mcpDispatchMu.Lock()
	app := b.mcpDispatchApp
	if app == nil {
		b.mcpPendingToolsChanged = append(b.mcpPendingToolsChanged, name)
		b.mcpDispatchMu.Unlock()
		return
	}
	b.mcpDispatchMu.Unlock()

	// SyncMCPToolsForServer + UI notification outside the lock so a slow
	// LLM client SetTools call can't stall concurrent dispatches from
	// other servers.
	app.SyncMCPToolsForServer(name)
	app.safeSendToProgram(ui.StatusUpdateMsg{
		Type:    ui.StatusStreamResume,
		Message: fmt.Sprintf("MCP %q: tool list updated", name),
	})
}

// assembleApp creates the final App instance from built components.
func (b *Builder) assembleApp() *App {
	// Return cached instance if already created
	if b.cachedApp != nil {
		return b.cachedApp
	}

	b.cachedApp = &App{
		config:                b.cfg,
		workDir:               b.workDir,
		client:                b.mainClient,
		registry:              b.registry,
		executor:              b.executor,
		session:               b.session,
		tui:                   b.tuiModel,
		ctx:                   b.ctx,
		cancel:                b.cancel, // Use the saved cancel function
		projectInfo:           b.projectInfo,
		contextManager:        b.contextManager,
		promptBuilder:         b.promptBuilder,
		contextAgent:          b.contextAgent,
		permManager:           b.permManager,
		permResponseChan:      make(chan permission.Decision, 2),
		questionResponseChan:  make(chan string, 1),
		diffResponseChan:      make(chan ui.DiffDecision, 1),
		multiDiffResponseChan: make(chan map[string]ui.DiffDecision, 1),
		planManager:           b.planManager,
		planApprovalChan:      make(chan plan.ApprovalDecision, 1),
		hooksManager:          b.hooksManager,
		taskManager:           b.taskManager,
		undoManager:           b.undoManager,
		agentRunner:           b.agentRunner,
		commandHandler:        b.commandHandler,
		sessionManager:        b.sessionManager,
		searchCache:           b.searchCache,
		rateLimiter:           b.rateLimiter,
		auditLogger:           b.auditLogger,
		fileWatcher:           b.fileWatcher,
		taskRouter:            b.taskRouter,
		orchestrator:          b.taskOrchestrator,
		reliability:           NewReliabilityManager(),
		policy:                NewPolicyEngine(),
		phaseMetrics:          NewPhaseMetrics(),
		toolMetrics:           NewToolMetrics(),
		// Phase 5: Agent System Improvements
		coordinator:       b.coordinator,
		agentTypeRegistry: b.agentTypeRegistry,
		strategyOptimizer: b.strategyOptimizer,
		metaAgent:         b.metaAgent,
		// Phase 6: Tree Planner
		treePlanner:         b.treePlanner,
		planningModeEnabled: b.cfg.Plan.Enabled,
		// MCP (Model Context Protocol)
		mcpManager:        b.mcpManager,
		mcpInitialSummary: b.mcpConnectSummary,
		sessionMemory:     b.sessionMemory,
		workingMemory:     b.workingMemory,
		// Persistent stores for flush on shutdown
		memoryStore:  b.memStore,
		errorStore:   b.errorStore,
		exampleStore: b.exampleStore,
		// Auto retry tracking
		rateLimitRetryCount: make(map[string]int),
		// Step rollback snapshots
		stepRollbackSnapshots: make(map[string]*stepRollbackSnapshot),
	}

	// Wire rate limit callback from runner to app's limiter
	if b.agentRunner != nil && b.rateLimiter != nil {
		b.agentRunner.SetRateLimitCallback(func(rl *client.RateLimitMetadata) {
			b.cachedApp.handleRateLimitMetadata(rl)
		})
	}

	if j, err := NewExecutionJournal(b.workDir); err == nil {
		b.cachedApp.journal = j
	} else {
		logging.Warn("failed to initialize execution journal", "error", err)
	}

	// Wire up user input callback for agents
	if b.agentRunner != nil {
		b.agentRunner.SetOnInput(func(prompt string) (string, error) {
			return b.cachedApp.promptQuestion(b.ctx, prompt, nil, "")
		})
	}

	// Wire per-tool phase observer → app-level metrics collectors. Tool
	// timing feeds both the raw PhaseMetrics buffer (p50/p95 aggregate for
	// PhaseTool) and the ToolMetrics collector (per-tool counts + error
	// classifier). Kept inside builder so internal/tools doesn't import
	// internal/app.
	if b.executor != nil && b.cachedApp != nil {
		app := b.cachedApp
		b.executor.SetPhaseObserver(func(tool string, d time.Duration, success bool) {
			if app.phaseMetrics != nil {
				app.phaseMetrics.Record(PhaseTool, d)
			}
			if app.toolMetrics != nil {
				kind := ""
				if !success {
					kind = "other"
				}
				app.toolMetrics.Record(tool, d, success, kind)
			}
		})
		// Adaptive parallelism: executor consults ToolMetrics to decide
		// whether a group of reads should run serialized (when any tool
		// in the group has low success rate).
		if app.toolMetrics != nil {
			b.executor.SetToolStatsLookup(app.toolMetrics.Lookup)
		}
	}

	return b.cachedApp
}

// addError records a non-fatal error during build.
func (b *Builder) addError(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buildErrors = append(b.buildErrors, err)
}

// finalizeError combines all build errors into a single error.
func (b *Builder) finalizeError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.buildErrors) == 0 {
		return nil
	}
	msg := fmt.Sprintf("app build failed with %d error(s)", len(b.buildErrors))
	for i, err := range b.buildErrors {
		msg += fmt.Sprintf("\n  %d. %s", i+1, err.Error())
	}
	return fmt.Errorf("%s", msg)
}

// ========== UIBroadcaster Adapter (Phase 5) ==========

// uiBroadcasterAdapter implements agent.UIBroadcaster for tea.Program.
type uiBroadcasterAdapter struct {
	app *App
}

// BroadcastTaskStarted sends a task started event to the UI.
func (a *uiBroadcasterAdapter) BroadcastTaskStarted(taskID, message, planType string) {
	if a.app != nil && a.app.program != nil {
		a.app.safeSendToProgram(ui.TaskStartedEvent{
			TaskID:   taskID,
			Message:  message,
			PlanType: planType,
		})
	}
}

// BroadcastTaskCompleted sends a task completed event to the UI.
func (a *uiBroadcasterAdapter) BroadcastTaskCompleted(taskID string, success bool, duration time.Duration, err error, planType string) {
	if a.app != nil && a.app.program != nil {
		a.app.safeSendToProgram(ui.TaskCompletedEvent{
			TaskID:   taskID,
			Success:  success,
			Duration: duration,
			Error:    err,
			PlanType: planType,
		})
	}
}

// BroadcastTaskProgress sends a task progress event to the UI.
func (a *uiBroadcasterAdapter) BroadcastTaskProgress(taskID string, progress float64, message string) {
	if a.app != nil && a.app.program != nil {
		a.app.safeSendToProgram(ui.TaskProgressEvent{
			TaskID:   taskID,
			Progress: progress,
			Message:  message,
		})
	}
}

// ========== Phase 2: Example Store Adapter ==========

// exampleStoreAdapter adapts memory.ExampleStore to agent.ExampleStoreInterface.
type exampleStoreAdapter struct {
	store *memory.ExampleStore
}

func (a *exampleStoreAdapter) LearnFromSuccess(taskType, prompt, agentType, output string, duration time.Duration, tokens int) error {
	return a.store.LearnFromSuccess(taskType, prompt, agentType, output, duration, tokens)
}

func (a *exampleStoreAdapter) GetSimilarExamples(prompt string, limit int) []agent.TaskExampleSummary {
	examples := a.store.GetSimilarExamples(prompt, limit)
	result := make([]agent.TaskExampleSummary, len(examples))
	for i, ex := range examples {
		result[i] = agent.TaskExampleSummary{
			ID:          ex.ID,
			TaskType:    ex.TaskType,
			InputPrompt: ex.InputPrompt,
			AgentType:   ex.AgentType,
			Duration:    ex.Duration,
			Score:       ex.Score,
		}
	}
	return result
}

func (a *exampleStoreAdapter) GetExamplesForContext(taskType, prompt string, limit int) string {
	return a.store.GetExamplesForContext(taskType, prompt, limit)
}

// routerExampleStoreAdapter adapts memory.ExampleStore to router.ExampleStoreInterface.
type routerExampleStoreAdapter struct {
	store *memory.ExampleStore
}

func (a *routerExampleStoreAdapter) GetSimilarExamples(prompt string, limit int) []router.ExampleSummary {
	examples := a.store.GetSimilarExamples(prompt, limit)
	result := make([]router.ExampleSummary, len(examples))
	for i, ex := range examples {
		result[i] = router.ExampleSummary{
			ID:          ex.ID,
			TaskType:    ex.TaskType,
			InputPrompt: ex.InputPrompt,
			AgentType:   ex.AgentType,
			Duration:    ex.Duration,
			Score:       ex.Score,
		}
	}
	return result
}

func (a *routerExampleStoreAdapter) GetExamplesForContext(taskType, prompt string, limit int) string {
	return a.store.GetExamplesForContext(taskType, prompt, limit)
}

func (a *routerExampleStoreAdapter) LearnFromSuccess(taskType, prompt, agentType, output string, duration time.Duration, tokens int) error {
	return a.store.LearnFromSuccess(taskType, prompt, agentType, output, duration, tokens)
}

// ========== Phase 2: Shared Memory Tool Adapter ==========

// sharedMemoryToolAdapter adapts agent.SharedMemory to tools.SharedMemoryInterface.
type sharedMemoryToolAdapter struct {
	memory *agent.SharedMemory
}

func (a *sharedMemoryToolAdapter) Write(key string, value any, entryType string, sourceAgent string) {
	a.memory.Write(key, value, agent.SharedEntryType(entryType), sourceAgent)
}

func (a *sharedMemoryToolAdapter) WriteWithTTL(key string, value any, entryType string, sourceAgent string, ttl time.Duration) {
	a.memory.WriteWithTTL(key, value, agent.SharedEntryType(entryType), sourceAgent, ttl)
}

func (a *sharedMemoryToolAdapter) Read(key string) (tools.SharedMemoryEntry, bool) {
	entry, ok := a.memory.Read(key)
	if !ok {
		return tools.SharedMemoryEntry{}, false
	}
	return tools.SharedMemoryEntry{
		Key:       entry.Key,
		Value:     entry.Value,
		Type:      string(entry.Type),
		Source:    entry.Source,
		Timestamp: entry.Timestamp,
		Version:   entry.Version,
	}, true
}

func (a *sharedMemoryToolAdapter) ReadByType(entryType string) []tools.SharedMemoryEntry {
	entries := a.memory.ReadByType(agent.SharedEntryType(entryType))
	result := make([]tools.SharedMemoryEntry, len(entries))
	for i, entry := range entries {
		result[i] = tools.SharedMemoryEntry{
			Key:       entry.Key,
			Value:     entry.Value,
			Type:      string(entry.Type),
			Source:    entry.Source,
			Timestamp: entry.Timestamp,
			Version:   entry.Version,
		}
	}
	return result
}

func (a *sharedMemoryToolAdapter) ReadAll() []tools.SharedMemoryEntry {
	entries := a.memory.ReadAll()
	result := make([]tools.SharedMemoryEntry, len(entries))
	for i, entry := range entries {
		result[i] = tools.SharedMemoryEntry{
			Key:       entry.Key,
			Value:     entry.Value,
			Type:      string(entry.Type),
			Source:    entry.Source,
			Timestamp: entry.Timestamp,
			Version:   entry.Version,
		}
	}
	return result
}

func (a *sharedMemoryToolAdapter) Delete(key string) bool {
	return a.memory.Delete(key)
}

func (a *sharedMemoryToolAdapter) GetForContext(agentID string, maxEntries int) string {
	return a.memory.GetForContext(agentID, maxEntries)
}

// ========== Context Predictor Adapter ==========

// contextPredictorAdapter adapts appcontext.ContextPredictor to agent.PredictorInterface.
type contextPredictorAdapter struct {
	predictor *appcontext.ContextPredictor
}

func (a *contextPredictorAdapter) PredictFiles(currentFile string, limit int) []agent.PredictedFile {
	predictions := a.predictor.PredictFiles(currentFile, limit)
	result := make([]agent.PredictedFile, len(predictions))
	for i, p := range predictions {
		result[i] = agent.PredictedFile{
			Path:       p.Path,
			Confidence: p.Confidence,
			Reason:     p.Reason,
		}
	}
	return result
}

// contextPredictorToolsAdapter adapts appcontext.ContextPredictor to tools.FilePredictor.
type contextPredictorToolsAdapter struct {
	predictor *appcontext.ContextPredictor
}

func (a *contextPredictorToolsAdapter) PredictFiles(currentFile string, limit int) []tools.PredictedFile {
	predictions := a.predictor.PredictFiles(currentFile, limit)
	result := make([]tools.PredictedFile, len(predictions))
	for i, p := range predictions {
		result[i] = tools.PredictedFile{
			Path:       p.Path,
			Confidence: p.Confidence,
			Reason:     p.Reason,
		}
	}
	return result
}

// errorLearnerAdapter adapts *memory.ErrorStore to tools.ErrorLearner.
type errorLearnerAdapter struct {
	store *memory.ErrorStore
}

func (a *errorLearnerAdapter) LearnError(errorType, pattern, solution string, tags []string) error {
	return a.store.LearnError(errorType, pattern, solution, tags)
}

// ========== Coordinator Tool Adapter ==========

// coordinatorToolAdapter wraps *agent.Coordinator to match the coordinate tool's interface.
type coordinatorToolAdapter struct {
	coord *agent.Coordinator
}

func (a *coordinatorToolAdapter) AddTask(prompt string, agentType any, priority any, deps []string) string {
	at := agent.ParseAgentType(fmt.Sprintf("%v", agentType))
	p := agent.TaskPriority(5)
	switch v := priority.(type) {
	case int:
		p = agent.TaskPriority(v)
	case float64:
		p = agent.TaskPriority(int(v))
	}
	return a.coord.AddTask(prompt, at, p, deps)
}

func (a *coordinatorToolAdapter) Start() {
	a.coord.Start()
}

func (a *coordinatorToolAdapter) WaitWithTimeout(timeout time.Duration) (map[string]any, error) {
	results, err := a.coord.WaitWithTimeout(timeout)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(results))
	for k, v := range results {
		out[k] = v
	}
	return out, nil
}

func (a *coordinatorToolAdapter) GetStatus() any {
	return a.coord.GetStatus()
}
