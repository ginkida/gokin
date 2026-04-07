package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"gokin/internal/client"
	"gokin/internal/config"
	"gokin/internal/logging"
	"gokin/internal/memory"
	"gokin/internal/permission"
	"gokin/internal/tools"
)

// Constants for resource management
const (
	// MaxAgentResults is the maximum number of agent results to keep in memory
	MaxAgentResults = 100
	// MaxCompletedAgents is the maximum number of completed agents to keep
	MaxCompletedAgents = 50
)

// ActivityReporter is an interface for reporting agent activity.
type ActivityReporter interface {
	ReportActivity()
}

// Runner manages the execution of multiple agents.
type Runner struct {
	ctx          context.Context
	client       client.Client
	baseRegistry tools.ToolRegistry
	workDir      string
	agents       map[string]*Agent
	results      map[string]*AgentResult
	store        *AgentStore
	permissions  *permission.Manager

	// Activity reporting
	activityReporter ActivityReporter

	// Inter-agent communication
	messengerFactory func(agentID string) *AgentMessenger

	ctxCfg *config.ContextConfig

	// Error learning (Phase 3)
	errorStore *memory.ErrorStore

	// Phase 5: Agent system improvements
	typeRegistry      *AgentTypeRegistry
	strategyOptimizer *StrategyOptimizer
	metaAgent         *MetaAgent

	// Phase 6: Tree Planner
	treePlanner            *TreePlanner
	planningModeEnabled    bool // Global planning mode flag
	requireApprovalEnabled bool // Global require approval flag

	// Phase 7: Delegation Metrics
	delegationMetrics *DelegationMetrics

	// Callback for context compaction when plan is approved
	onPlanApproved func(planSummary string)

	// Callbacks for background task tracking (UI updates)
	onAgentStart    func(id, agentType, description string)
	onAgentComplete func(id string, result *AgentResult)

	// Phase 2: Progress tracking callback
	onAgentProgress func(id string, progress *AgentProgress)

	// Scratchpad state
	onScratchpadUpdate func(content string)
	sharedScratchpad   string

	// Phase 2: Shared memory for inter-agent communication
	sharedMemory *SharedMemory

	// User input callback
	onInput func(prompt string) (string, error)

	// Phase 2: Example store for few-shot learning
	exampleStore ExampleStoreInterface

	// Phase 2: Prompt optimizer
	promptOptimizer *PromptOptimizer

	// File predictor for enhanced error recovery
	predictor PredictorInterface

	// Thinking callback for UI display
	onThinking func(text string)

	// Sub-agent activity callback for UI updates
	onSubAgentActivity func(agentID, agentType, toolName string, args map[string]any, status string)

	// Event-driven notification for result completion (replaces polling in WaitWithContext)
	resultReady chan struct{}

	// Weak model mode: extra guidance for weaker models
	weakModelMode bool

	// Workspace isolation for supported sub-agents
	workspaceIsolationEnabled bool
	workspaceReviewHandler    func(context.Context, []WorkspaceChangePreview) (bool, error)

	// Rate limiting callback (adaptive)
	onRateLimit func(rl *client.RateLimitMetadata)

	mu sync.RWMutex
}

// GetActiveAgent returns an agent by ID, or the first active agent if ID is empty.
func (r *Runner) GetActiveAgent(id string) *Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if id != "" {
		return r.agents[id]
	}

	// Fallback: return the first agent (in plans, there is usually one active at a time)
	for _, a := range r.agents {
		return a
	}

	return nil
}

type runnerAgentDeps struct {
	ctxCfg              *config.ContextConfig
	errorStore          *memory.ErrorStore
	predictor           PredictorInterface
	sharedMemory        *SharedMemory
	sharedScratchpad    string
	scratchpadCallback  func(string)
	typeRegistry        *AgentTypeRegistry
	onInput             func(string) (string, error)
	client              client.Client
	baseRegistry        tools.ToolRegistry
	workDir             string
	permissions         *permission.Manager
	messengerFactory    func(agentID string) *AgentMessenger
	strategyOptimizer   *StrategyOptimizer
	delegationMetrics   *DelegationMetrics
	metaAgent           *MetaAgent
	treePlanner         *TreePlanner
	planningModeEnabled bool
	requireApproval     bool
	onPlanApproved      func(planSummary string)
	onThinking          func(text string)
	exampleStore        ExampleStoreInterface
	promptOptimizer     *PromptOptimizer
	workspaceIsolation  bool
}

// SetPermissions sets the permission manager for agents.
func (r *Runner) SetPermissions(mgr *permission.Manager) {
	r.mu.Lock()
	r.permissions = mgr
	r.mu.Unlock()
}

// SetActivityReporter sets the activity reporter.
func (r *Runner) SetActivityReporter(reporter ActivityReporter) {
	r.mu.Lock()
	r.activityReporter = reporter
	r.mu.Unlock()
}

// SetContextConfig sets the context configuration for agents.
func (r *Runner) SetContextConfig(cfg *config.ContextConfig) {
	r.mu.Lock()
	r.ctxCfg = cfg
	r.mu.Unlock()
}

// SetErrorStore sets the error store for learning from errors.
func (r *Runner) SetErrorStore(store *memory.ErrorStore) {
	r.mu.Lock()
	r.errorStore = store
	r.mu.Unlock()
}

// GetErrorStore returns the error store.
func (r *Runner) GetErrorStore() *memory.ErrorStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.errorStore
}

// SetPredictor sets the file predictor for enhanced error recovery.
func (r *Runner) SetPredictor(p PredictorInterface) {
	r.mu.Lock()
	r.predictor = p
	r.mu.Unlock()
}

// SetTypeRegistry sets the agent type registry for dynamic types.
func (r *Runner) SetTypeRegistry(registry *AgentTypeRegistry) {
	r.mu.Lock()
	r.typeRegistry = registry
	r.mu.Unlock()
}

// GetTypeRegistry returns the agent type registry.
func (r *Runner) GetTypeRegistry() *AgentTypeRegistry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.typeRegistry
}

// SetStrategyOptimizer sets the strategy optimizer for learning from outcomes.
func (r *Runner) SetStrategyOptimizer(optimizer *StrategyOptimizer) {
	r.mu.Lock()
	r.strategyOptimizer = optimizer
	r.mu.Unlock()
}

// GetStrategyOptimizer returns the strategy optimizer.
func (r *Runner) GetStrategyOptimizer() *StrategyOptimizer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.strategyOptimizer
}

// SetMetaAgent sets the meta-agent for monitoring and optimization.
func (r *Runner) SetMetaAgent(meta *MetaAgent) {
	r.mu.Lock()
	r.metaAgent = meta
	r.mu.Unlock()
}

// GetMetaAgent returns the meta-agent.
func (r *Runner) GetMetaAgent() *MetaAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.metaAgent
}

// SetTreePlanner sets the tree planner for planned execution.
func (r *Runner) SetTreePlanner(tp *TreePlanner) {
	r.mu.Lock()
	r.treePlanner = tp
	r.mu.Unlock()
}

// GetTreePlanner returns the tree planner.
func (r *Runner) GetTreePlanner() *TreePlanner {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.treePlanner
}

// IsPlanningModeEnabled returns the global planning mode enabled flag.
func (r *Runner) IsPlanningModeEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.planningModeEnabled
}

// SetPlanningModeEnabled sets the global planning mode enabled flag.
func (r *Runner) SetPlanningModeEnabled(enabled bool) {
	r.mu.Lock()
	r.planningModeEnabled = enabled
	r.mu.Unlock()
}

// IsRequireApprovalEnabled returns the global require approval flag.
func (r *Runner) IsRequireApprovalEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.requireApprovalEnabled
}

// SetRequireApprovalEnabled sets the global require approval flag.
func (r *Runner) SetRequireApprovalEnabled(enabled bool) {
	r.mu.Lock()
	r.requireApprovalEnabled = enabled
	r.mu.Unlock()
}

// SetDelegationMetrics sets the delegation metrics for adaptive delegation rules.
func (r *Runner) SetDelegationMetrics(dm *DelegationMetrics) {
	r.mu.Lock()
	r.delegationMetrics = dm
	r.mu.Unlock()
}

// GetDelegationMetrics returns the delegation metrics.
func (r *Runner) GetDelegationMetrics() *DelegationMetrics {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.delegationMetrics
}

// SetOnPlanApproved sets the callback for when a plan is approved.
// This callback is used to compact context and inject plan summary.
func (r *Runner) SetOnPlanApproved(callback func(planSummary string)) {
	r.mu.Lock()
	r.onPlanApproved = callback
	r.mu.Unlock()
}

// SetOnAgentStart sets the callback for when a background agent starts.
func (r *Runner) SetOnAgentStart(callback func(id, agentType, description string)) {
	r.mu.Lock()
	r.onAgentStart = callback
	r.mu.Unlock()
}

// SetOnAgentComplete sets the callback for when a background agent completes.
func (r *Runner) SetOnAgentComplete(callback func(id string, result *AgentResult)) {
	r.mu.Lock()
	r.onAgentComplete = callback
	r.mu.Unlock()
}

// SetOnAgentProgress sets the callback for agent progress updates.
func (r *Runner) SetOnAgentProgress(callback func(id string, progress *AgentProgress)) {
	r.mu.Lock()
	r.onAgentProgress = callback
	r.mu.Unlock()
}

// SetWeakModelMode enables extra guidance for all agents created by this runner.
func (r *Runner) SetWeakModelMode(enabled bool) {
	r.mu.Lock()
	r.weakModelMode = enabled
	r.mu.Unlock()
}

// SetOnThinking sets the callback for thinking/reasoning content from agents.
func (r *Runner) SetOnThinking(fn func(string)) {
	r.mu.Lock()
	r.onThinking = fn
	r.mu.Unlock()
}

// SetOnScratchpadUpdate sets the callback for agent scratchpad updates.
// The callback is called asynchronously to avoid deadlock.
func (r *Runner) SetOnScratchpadUpdate(fn func(string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onScratchpadUpdate = func(content string) {
		// Update shared scratchpad atomically using a separate method
		// to avoid holding lock while calling user callback
		r.updateSharedScratchpad(content)
		// Call user callback outside any lock to prevent deadlock
		if fn != nil {
			fn(content)
		}
	}
}

// updateSharedScratchpad atomically updates the shared scratchpad.
func (r *Runner) updateSharedScratchpad(content string) {
	r.mu.Lock()
	r.sharedScratchpad = content
	r.mu.Unlock()
}

// SetSharedScratchpad sets the shared scratchpad content.
func (r *Runner) SetSharedScratchpad(content string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sharedScratchpad = content
}

// SetSharedMemory sets the shared memory for inter-agent communication.
func (r *Runner) SetSharedMemory(sm *SharedMemory) {
	r.mu.Lock()
	r.sharedMemory = sm
	r.mu.Unlock()
}

// GetSharedMemory returns the shared memory instance.
func (r *Runner) GetSharedMemory() *SharedMemory {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sharedMemory
}

// ExampleStoreInterface defines the interface for example stores.
type ExampleStoreInterface interface {
	LearnFromSuccess(taskType, prompt, agentType, output string, duration time.Duration, tokens int) error
	GetSimilarExamples(prompt string, limit int) []TaskExampleSummary
	GetExamplesForContext(taskType, prompt string, limit int) string
}

// TaskExampleSummary contains a summary of a task example.
type TaskExampleSummary struct {
	ID          string
	TaskType    string
	InputPrompt string
	AgentType   string
	Duration    time.Duration
	Score       float64
}

// SetExampleStore sets the example store for few-shot learning.
func (r *Runner) SetExampleStore(store ExampleStoreInterface) {
	r.mu.Lock()
	r.exampleStore = store
	r.mu.Unlock()
}

// GetExampleStore returns the example store.
func (r *Runner) GetExampleStore() ExampleStoreInterface {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.exampleStore
}

// SetOnInput sets the callback for requesting user input.
func (r *Runner) SetOnInput(callback func(string) (string, error)) {
	r.mu.Lock()
	r.onInput = callback
	r.mu.Unlock()
}

// SetPromptOptimizer sets the prompt optimizer.
func (r *Runner) SetPromptOptimizer(optimizer *PromptOptimizer) {
	r.mu.Lock()
	r.promptOptimizer = optimizer
	r.mu.Unlock()
}

// SetWorkspaceIsolationEnabled toggles per-agent isolated workspaces for safe read-only agents.
func (r *Runner) SetWorkspaceIsolationEnabled(enabled bool) {
	r.mu.Lock()
	r.workspaceIsolationEnabled = enabled
	r.mu.Unlock()
}

// SetWorkspaceReviewHandler sets the callback used to review isolated workspace
// changes before apply-back into the primary workspace.
func (r *Runner) SetWorkspaceReviewHandler(handler func(context.Context, []WorkspaceChangePreview) (bool, error)) {
	r.mu.Lock()
	r.workspaceReviewHandler = handler
	r.mu.Unlock()
}

// SetOnSubAgentActivity sets the callback for sub-agent activity reporting.
func (r *Runner) SetOnSubAgentActivity(fn func(agentID, agentType, toolName string, args map[string]any, status string)) {
	r.mu.Lock()
	r.onSubAgentActivity = fn
	r.mu.Unlock()
}

// GetPromptOptimizer returns the prompt optimizer.
func (r *Runner) GetPromptOptimizer() *PromptOptimizer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.promptOptimizer
}

// reportActivity reports activity if configured.
func (r *Runner) reportActivity() {
	r.mu.RLock()
	reporter := r.activityReporter
	r.mu.RUnlock()

	if reporter != nil {
		reporter.ReportActivity()
	}
}

// NewRunner creates a new agent runner.
func NewRunner(ctx context.Context, c client.Client, registry tools.ToolRegistry, workDir string) *Runner {
	r := &Runner{
		ctx:          ctx,
		client:       c,
		baseRegistry: registry,
		workDir:      workDir,
		agents:       make(map[string]*Agent),
		results:      make(map[string]*AgentResult),
		resultReady:  make(chan struct{}, 1),
	}
	// Set up messenger factory
	r.messengerFactory = func(agentID string) *AgentMessenger {
		return NewAgentMessenger(ctx, r, agentID)
	}
	return r
}

func (r *Runner) snapshotAgentDeps() runnerAgentDeps {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return runnerAgentDeps{
		ctxCfg:              r.ctxCfg,
		errorStore:          r.errorStore,
		predictor:           r.predictor,
		sharedMemory:        r.sharedMemory,
		sharedScratchpad:    r.sharedScratchpad,
		scratchpadCallback:  r.onScratchpadUpdate,
		typeRegistry:        r.typeRegistry,
		onInput:             r.onInput,
		client:              r.client,
		baseRegistry:        r.baseRegistry,
		workDir:             r.workDir,
		permissions:         r.permissions,
		messengerFactory:    r.messengerFactory,
		strategyOptimizer:   r.strategyOptimizer,
		delegationMetrics:   r.delegationMetrics,
		metaAgent:           r.metaAgent,
		treePlanner:         r.treePlanner,
		planningModeEnabled: r.planningModeEnabled,
		requireApproval:     r.requireApprovalEnabled,
		onPlanApproved:      r.onPlanApproved,
		onThinking:          r.onThinking,
		exampleStore:        r.exampleStore,
		promptOptimizer:     r.promptOptimizer,
		workspaceIsolation:  r.workspaceIsolationEnabled,
	}
}

func cloneTreePlanner(tp *TreePlanner) *TreePlanner {
	if tp == nil {
		return nil
	}

	tp.mu.RLock()
	defer tp.mu.RUnlock()

	var cfgCopy *TreePlannerConfig
	if tp.config != nil {
		cfg := *tp.config
		cfgCopy = &cfg
	}

	return &TreePlanner{
		config:      cfgCopy,
		strategyOpt: tp.strategyOpt,
		reflector:   tp.reflector,
		client:      tp.client,
		trees:       make(map[string]*PlanTree),
	}
}

func (r *Runner) newConfiguredAgent(
	ctx context.Context,
	deps runnerAgentDeps,
	agentType string,
	maxTurns int,
	model string,
	perms *permission.Manager,
) *Agent {
	agentBaseRegistry := deps.baseRegistry
	agentWorkDir := strings.TrimSpace(deps.workDir)
	var isolated *isolatedWorkspace

	isolationMode := resolveWorkspaceIsolationMode(agentType, deps)
	if isolationMode != workspaceIsolationDisabled {
		ws, err := prepareIsolatedWorkspace(agentWorkDir, isolationMode)
		if err != nil {
			logging.Debug("failed to prepare isolated workspace", "agent_type", agentType, "workdir", agentWorkDir, "mode", isolationMode, "error", err)
		} else {
			isolated = ws
			agentBaseRegistry = tools.CloneRegistryForWorkDir(deps.baseRegistry, ws.Root)
		}
	}

	var agent *Agent
	if deps.typeRegistry != nil {
		if dynType, ok := deps.typeRegistry.GetDynamic(agentType); ok {
			agent = NewAgentWithDynamicType(
				dynType,
				deps.client,
				agentBaseRegistry,
				agentWorkDir,
				maxTurns,
				model,
				perms,
				deps.ctxCfg,
			)
		}
	}
	if agent == nil {
		agent = NewAgent(
			ParseAgentType(agentType),
			deps.client,
			agentBaseRegistry,
			agentWorkDir,
			maxTurns,
			model,
			perms,
			deps.ctxCfg,
		)
	}

	agent.ApplyThoroughness(tools.ThoroughnessFromContext(ctx), maxTurns)
	agent.SetOutputStyle(tools.OutputStyleFromContext(ctx))
	agent.LoadPinnedContext()

	// Apply settings from runner
	r.mu.RLock()
	weakMode := r.weakModelMode
	onRL := r.onRateLimit
	r.mu.RUnlock()

	if weakMode {
		agent.SetWeakModelMode(true)
	}
	if onRL != nil {
		agent.SetOnRateLimit(onRL)
	}

	if deps.onInput != nil {
		agent.SetOnInput(deps.onInput)
	}
	if isolated != nil {
		agent.workDir = isolated.Root
		agent.isolatedWorkspace = isolated
		agent.SetAllowedRequestedTools(allowedRequestedToolsForIsolationMode(isolationMode))
		if bt, ok := agent.registry.Get("bash"); ok {
			if bashTool, ok := bt.(*tools.BashTool); ok && isolated.ApplyBackOnSuccess {
				bashTool.EnableManagedWorkspaceApplyBackMode(agent.workDir)
			}
		}
	}

	if deps.messengerFactory != nil {
		messenger := deps.messengerFactory(agent.ID)
		agent.SetMessenger(messenger)
	}

	if deps.errorStore != nil && agent.reflector != nil {
		agent.reflector.SetErrorStore(deps.errorStore)
	}
	if deps.predictor != nil && agent.reflector != nil {
		agent.reflector.SetPredictor(deps.predictor)
	}
	if deps.sharedMemory != nil {
		agent.SetSharedMemory(deps.sharedMemory)
	}
	if deps.sharedScratchpad != "" {
		agent.stateMu.Lock()
		agent.Scratchpad = deps.sharedScratchpad
		agent.stateMu.Unlock()
	}
	if deps.scratchpadCallback != nil {
		agent.SetOnScratchpadUpdate(deps.scratchpadCallback)
	}

	if depth := DelegationDepthFromContext(ctx); depth > 0 && agent.delegation != nil {
		agent.delegation.SetDepth(depth)
	}
	if deps.delegationMetrics != nil && agent.delegation != nil {
		agent.delegation.SetDelegationMetrics(deps.delegationMetrics)
	}
	if deps.strategyOptimizer != nil && agent.delegation != nil {
		agent.delegation.SetStrategyOptimizer(deps.strategyOptimizer)
	}

	if deps.treePlanner != nil {
		planner := cloneTreePlanner(deps.treePlanner)
		agent.SetTreePlanner(planner)
		if deps.planningModeEnabled {
			agent.EnablePlanningMode(nil)
		}
		agent.SetRequireApproval(deps.requireApproval)
		if deps.onPlanApproved != nil {
			agent.SetOnPlanApproved(deps.onPlanApproved)
		}
	}

	if deps.onThinking != nil {
		agent.SetOnThinking(deps.onThinking)
	}

	return agent
}

func (r *Runner) recordAgentExecutionLearning(
	deps runnerAgentDeps,
	agentType string,
	prompt string,
	result *AgentResult,
	duration time.Duration,
	strategyName string,
) {
	if result == nil {
		return
	}

	success := result.Status == AgentStatusCompleted && result.Error == ""

	if deps.strategyOptimizer != nil {
		deps.strategyOptimizer.RecordExecution(agentType, strategyName, success, duration)
	}

	if success && deps.exampleStore != nil {
		output := result.Output
		go func() {
			if err := deps.exampleStore.LearnFromSuccess(
				agentType,
				prompt,
				agentType,
				output,
				duration,
				0,
			); err != nil {
				logging.Debug("failed to learn from success", "error", err)
			}
		}()
	}

	if deps.promptOptimizer != nil {
		deps.promptOptimizer.RecordExecution(agentType, prompt, success, 0, duration)
	}
}

func (r *Runner) newRestoredAgent(
	ctx context.Context,
	deps runnerAgentDeps,
	state *AgentState,
	perms *permission.Manager,
) *Agent {
	agent := r.newConfiguredAgent(ctx, deps, string(state.Type), state.MaxTurns, state.Model, perms)
	agent.ID = state.ID
	return agent
}

func attachMetaAgentMonitoring(agent *Agent, meta *MetaAgent) {
	if meta == nil {
		return
	}

	meta.RegisterAgent(agent.ID, agent.Type)
	agentID := agent.ID
	agent.SetOnToolActivity(func(_ string, toolName string, _ map[string]any, status string) {
		if status == "start" {
			meta.UpdateActivity(agentID, toolName, agent.GetTurnCount())
		}
	})
}

var workspaceIsolationReadOnlyTools = toolNameSet(
	"read", "glob", "grep", "tree", "list_dir", "diff", "todo", "env",
	"web_fetch", "web_search", "ask_user",
	"tools_list", "request_tool", "ask_agent",
	"enter_plan_mode", "update_plan_progress", "get_plan_status", "exit_plan_mode",
	"undo_plan", "redo_plan",
	"git_status", "git_diff", "git_log", "git_blame",
	"run_tests", "verify_code", "check_impact",
	"semantic_search", "code_graph",
	"shared_memory", "update_scratchpad", "pin_context", "history_search",
	"memory", "memorize",
)

var workspaceIsolationApplyBackTools = toolNameSet(
	"read", "glob", "grep", "tree", "list_dir", "diff", "todo", "env",
	"web_fetch", "web_search", "ask_user",
	"tools_list", "request_tool", "ask_agent",
	"enter_plan_mode", "update_plan_progress", "get_plan_status", "exit_plan_mode",
	"undo_plan", "redo_plan",
	"git_status", "git_diff", "git_log", "git_blame",
	"run_tests", "verify_code", "check_impact",
	"semantic_search", "code_graph",
	"shared_memory", "update_scratchpad", "pin_context", "history_search",
	"memory", "memorize",
	"write", "edit", "batch", "refactor", "copy", "move", "delete", "mkdir",
	"bash",
)

func toolNameSet(names ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}

func allowedRequestedToolsForIsolationMode(mode workspaceIsolationMode) []string {
	switch mode {
	case workspaceIsolationReadOnly:
		return toolNameList(workspaceIsolationReadOnlyTools)
	case workspaceIsolationApplyBack:
		return toolNameList(workspaceIsolationApplyBackTools)
	default:
		return nil
	}
}

func toolNameList(set map[string]struct{}) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func resolveAgentAllowedTools(agentType string, deps runnerAgentDeps) []string {
	if deps.typeRegistry != nil {
		if dynType, ok := deps.typeRegistry.GetDynamic(agentType); ok {
			return dynType.AllowedTools
		}
	}
	return ParseAgentType(agentType).AllowedTools()
}

func supportedWorkspaceIsolationMode(agentType string, deps runnerAgentDeps, supported map[string]struct{}, mode workspaceIsolationMode) workspaceIsolationMode {
	if !deps.workspaceIsolation || strings.TrimSpace(deps.workDir) == "" {
		return workspaceIsolationDisabled
	}

	allowedTools := resolveAgentAllowedTools(agentType, deps)
	if len(allowedTools) == 0 {
		return workspaceIsolationDisabled
	}

	for _, name := range allowedTools {
		if _, ok := supported[name]; !ok {
			return workspaceIsolationDisabled
		}
	}

	return mode
}

func resolveWorkspaceIsolationMode(agentType string, deps runnerAgentDeps) workspaceIsolationMode {
	if mode := supportedWorkspaceIsolationMode(agentType, deps, workspaceIsolationReadOnlyTools, workspaceIsolationReadOnly); mode != workspaceIsolationDisabled {
		return mode
	}
	return supportedWorkspaceIsolationMode(agentType, deps, workspaceIsolationApplyBackTools, workspaceIsolationApplyBack)
}

func (r *Runner) finalizeAgentWorkspace(agent *Agent, result *AgentResult) error {
	if agent == nil || agent.isolatedWorkspace == nil || result == nil {
		return nil
	}

	if result.Metadata == nil {
		result.Metadata = make(map[string]interface{})
	}
	result.Metadata["isolated_workspace"] = true
	result.Metadata["isolated_workspace_strategy"] = agent.isolatedWorkspace.Strategy
	if agent.isolatedWorkspace.ApplyBackOnSuccess {
		result.Metadata["isolated_workspace_apply_back_enabled"] = true
	}

	if result.IsSuccess() {
		if agent.isolatedWorkspace.ApplyBackOnSuccess {
			r.mu.RLock()
			reviewHandler := r.workspaceReviewHandler
			r.mu.RUnlock()

			if reviewHandler != nil {
				previews, err := agent.isolatedWorkspace.ChangePreviews()
				if err != nil {
					reviewErr := fmt.Errorf("failed to review isolated workspace changes: %w", err)
					result.Status = AgentStatusFailed
					result.Error = reviewErr.Error()
					result.Metadata["isolated_workspace_review_error"] = reviewErr.Error()
					result.Metadata["isolated_workspace_dir"] = agent.workDir
					return reviewErr
				}
				if len(previews) > 0 {
					result.Metadata["isolated_workspace_review_required"] = true
					approved, err := reviewHandler(context.Background(), previews)
					if err != nil {
						reviewErr := fmt.Errorf("failed to review isolated workspace changes: %w", err)
						result.Status = AgentStatusFailed
						result.Error = reviewErr.Error()
						result.Metadata["isolated_workspace_review_error"] = reviewErr.Error()
						result.Metadata["isolated_workspace_dir"] = agent.workDir
						return reviewErr
					}
					if !approved {
						reviewErr := fmt.Errorf("isolated workspace changes rejected by user")
						result.Status = AgentStatusFailed
						result.Error = reviewErr.Error()
						result.Metadata["isolated_workspace_review_rejected"] = true
						result.Metadata["isolated_workspace_dir"] = agent.workDir
						return reviewErr
					}
					result.Metadata["isolated_workspace_reviewed"] = true
					result.Metadata["isolated_workspace_review_files"] = len(previews)
				}
			}

			applyResult, err := agent.isolatedWorkspace.ApplyBack()
			if err != nil {
				applyErr := fmt.Errorf("failed to apply isolated workspace changes: %w", err)
				result.Status = AgentStatusFailed
				result.Error = applyErr.Error()
				result.Metadata["isolated_workspace_apply_back_error"] = applyErr.Error()
				result.Metadata["isolated_workspace_dir"] = agent.workDir
				return applyErr
			}
			if applyResult != nil {
				result.Metadata["isolated_workspace_apply_back"] = len(applyResult.ChangedFiles) > 0
				result.Metadata["isolated_workspace_applied_files"] = applyResult.ChangedFiles
				result.Metadata["isolated_workspace_patch_bytes"] = applyResult.PatchBytes
				if applyResult.Mode != "" {
					result.Metadata["isolated_workspace_apply_back_mode"] = applyResult.Mode
				}
			}
		}

		if err := agent.isolatedWorkspace.Cleanup(); err != nil {
			result.Metadata["isolated_workspace_cleanup_error"] = err.Error()
			result.Metadata["isolated_workspace_dir"] = agent.workDir
			return nil
		}
		result.Metadata["isolated_workspace_cleaned"] = true
		return nil
	}

	result.Metadata["isolated_workspace_dir"] = agent.workDir
	return nil
}

// GetClient returns the underlying client.
func (r *Runner) GetClient() client.Client {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.client
}

// SetClient updates the underlying client.
func (r *Runner) SetClient(c client.Client) {
	r.mu.Lock()
	r.client = c
	r.mu.Unlock()
}

// cleanupOldResults removes old completed agents and results to prevent unbounded growth.
// Should be called periodically or when capacity is reached.
func (r *Runner) cleanupOldResults() {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Clean up completed agents if we exceed the limit
	if len(r.agents) > MaxCompletedAgents {
		type agentEntry struct {
			id      string
			endTime time.Time
		}
		var completed []agentEntry
		for id, agent := range r.agents {
			if agent.GetStatus() == AgentStatusCompleted ||
				agent.GetStatus() == AgentStatusFailed ||
				agent.GetStatus() == AgentStatusCancelled {
				completed = append(completed, agentEntry{id, agent.GetEndTime()})
			}
		}
		// Sort oldest first
		sort.Slice(completed, func(i, j int) bool {
			return completed[i].endTime.Before(completed[j].endTime)
		})
		// Remove oldest completed agents (keep MaxCompletedAgents/2)
		removeCount := len(completed) - MaxCompletedAgents/2
		if removeCount > 0 {
			for i := 0; i < removeCount; i++ {
				delete(r.agents, completed[i].id)
			}
			logging.Debug("cleaned up old agents", "removed", removeCount)
		}
	}

	// Clean up old results if we exceed the limit
	if len(r.results) > MaxAgentResults {
		type resultEntry struct {
			id      string
			endTime time.Time
		}
		var completed []resultEntry
		for id, result := range r.results {
			if result.Completed {
				endTime := time.Time{}
				if agent, ok := r.agents[id]; ok {
					endTime = agent.GetEndTime()
				}
				completed = append(completed, resultEntry{id, endTime})
			}
		}
		// Sort oldest first
		sort.Slice(completed, func(i, j int) bool {
			return completed[i].endTime.Before(completed[j].endTime)
		})
		removeCount := len(completed) - MaxAgentResults/2
		if removeCount > 0 {
			for i := 0; i < removeCount; i++ {
				delete(r.results, completed[i].id)
			}
			logging.Debug("cleaned up old results", "removed", removeCount)
		}
	}
}
// SetRateLimitCallback sets the rate limit callback for agents.
func (r *Runner) SetRateLimitCallback(cb func(rl *client.RateLimitMetadata)) {
	r.mu.Lock()
	r.onRateLimit = cb
	r.mu.Unlock()
}
