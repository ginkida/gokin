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

	// Sub-agent activity callback for UI updates
	onSubAgentActivity func(agentID, agentType, toolName string, args map[string]any, status string)

	// Event-driven notification for result completion (replaces polling in WaitWithContext)
	resultReady chan struct{}

	// Workspace isolation for supported sub-agents
	workspaceIsolationEnabled bool
	workspaceReviewHandler    func(context.Context, []WorkspaceChangePreview) (bool, error)

	mu sync.RWMutex
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

// Spawn creates and starts a new agent with the given task.
// agentType should be "explore", "bash", "general", "plan", "claude-code-guide", or "coordinator".
// Also supports dynamic types registered via AgentTypeRegistry.
func (r *Runner) Spawn(ctx context.Context, agentType string, prompt string, maxTurns int, model string) (string, error) {
	// Cleanup old completed agents and results to prevent unbounded growth
	r.cleanupOldResults()

	deps := r.snapshotAgentDeps()
	agent := r.newConfiguredAgent(ctx, deps, agentType, maxTurns, model, deps.permissions)

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.mu.Unlock()

	// Register with meta-agent for monitoring and wire activity updates
	if deps.metaAgent != nil {
		deps.metaAgent.RegisterAgent(agent.ID, agent.Type)
		agentID := agent.ID
		agent.SetOnToolActivity(func(_ string, toolName string, _ map[string]any, status string) {
			if status == "start" {
				deps.metaAgent.UpdateActivity(agentID, toolName, agent.GetTurnCount())
			}
		})
	}

	// Report activity to coordinator
	r.reportActivity()

	// Run agent synchronously with per-agent timeout
	runCtx := ctx
	if agentTimeout := agent.GetTimeout(); agentTimeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, agentTimeout)
		defer cancel()
	}
	startTime := time.Now()
	result, err := agent.Run(runCtx, prompt)
	duration := time.Since(startTime)

	// Report activity after completion
	r.reportActivity()

	// Unregister from meta-agent
	if deps.metaAgent != nil {
		deps.metaAgent.UnregisterAgent(agent.ID)
	}

	// Save agent state for potential resume
	r.saveAgentState(agent)

	if result == nil {
		result = &AgentResult{
			AgentID:   agent.ID,
			Type:      agent.Type,
			Status:    AgentStatusFailed,
			Error:     "agent returned nil result",
			Completed: true,
			Duration:  duration,
		}
	}
	workspaceErr := r.finalizeAgentWorkspace(agent, result)
	r.recordAgentExecutionLearning(deps, agentType, prompt, result, duration, "spawn")

	r.mu.Lock()
	r.results[agent.ID] = result
	r.mu.Unlock()
	r.notifyResultReady()

	if err == nil && workspaceErr != nil {
		err = workspaceErr
	}
	if err != nil {
		return agent.ID, err
	}

	return agent.ID, nil
}

// SpawnWithContext creates and runs a sub-agent with project context and streaming.
// Unlike Spawn, it returns the AgentResult directly for immediate use by the caller.
// When skipPermissions is true, the sub-agent will not ask for permission before
// executing tools (used for approved plan execution).
func (r *Runner) SpawnWithContext(
	ctx context.Context,
	agentType string,
	prompt string,
	maxTurns int,
	model string,
	projectContext string,
	onText func(string),
	skipPermissions bool,
	progressCallback ProgressCallback,
) (string, *AgentResult, error) {
	deps := r.snapshotAgentDeps()

	// Pass nil permissions for approved plan execution to avoid per-tool prompts
	var perms *permission.Manager
	if !skipPermissions {
		perms = deps.permissions
	}
	agent := r.newConfiguredAgent(ctx, deps, agentType, maxTurns, model, perms)

	// Inject project context and streaming callback
	agent.SetProjectContext(projectContext)
	agent.SetOnText(onText)

	// Wire checkpoint store and enable auto-checkpoint for long-running agents
	if r.store != nil {
		agent.SetStore(r.store)
		if maxTurns > 10 {
			agent.EnableAutoCheckpoint(0)
		}
	}

	// Wire progress callback for real-time sub-agent progress
	if progressCallback != nil {
		agent.SetProgressCallback(progressCallback)
	}

	// Wire sub-agent activity callback (chain with meta-agent UpdateActivity)
	r.mu.RLock()
	onSubAgentActivity := r.onSubAgentActivity
	r.mu.RUnlock()
	agentID := agent.ID
	agent.SetOnToolActivity(func(id, toolName string, args map[string]any, status string) {
		if onSubAgentActivity != nil {
			onSubAgentActivity(id, string(agent.Type), toolName, args, "tool_"+status)
		}
		if deps.metaAgent != nil && status == "start" {
			deps.metaAgent.UpdateActivity(agentID, toolName, agent.GetTurnCount())
		}
	})

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.mu.Unlock()

	// Register with meta-agent for monitoring
	if deps.metaAgent != nil {
		deps.metaAgent.RegisterAgent(agentID, agent.Type)
	}

	r.reportActivity()

	// Apply per-agent timeout and store cancel func for explicit Cancel()
	var runCtx context.Context
	var runCancel context.CancelFunc
	if agentTimeout := agent.GetTimeout(); agentTimeout > 0 {
		runCtx, runCancel = context.WithTimeout(ctx, agentTimeout)
	} else {
		runCtx, runCancel = context.WithCancel(ctx)
	}
	defer runCancel()
	agent.SetCancelFunc(runCancel)

	startTime := time.Now()
	result, err := agent.Run(runCtx, prompt)
	duration := time.Since(startTime)

	// Ensure result is never nil (matches SpawnAsync pattern)
	if result == nil {
		result = &AgentResult{
			AgentID:   agent.ID,
			Type:      agent.Type,
			Status:    AgentStatusFailed,
			Error:     "nil result from agent",
			Completed: true,
		}
	}

	if err != nil {
		result.Error = err.Error()
		result.Status = AgentStatusFailed
	}

	// Ensure Completed is always true so WaitWithContext doesn't spin
	result.Completed = true

	// Save error checkpoint on failure for potential recovery
	if err != nil && r.store != nil && agent.GetTurnCount() > 0 {
		if _, cpErr := agent.SaveCheckpoint("error"); cpErr != nil {
			logging.Debug("failed to save error checkpoint", "agent_id", agent.ID, "error", cpErr)
		}
	}

	r.reportActivity()

	// Unregister from meta-agent
	if deps.metaAgent != nil {
		deps.metaAgent.UnregisterAgent(agentID)
	}

	workspaceErr := r.finalizeAgentWorkspace(agent, result)
	r.saveAgentState(agent)
	r.recordAgentExecutionLearning(deps, agentType, prompt, result, duration, "spawn_with_context")

	r.mu.Lock()
	r.results[agent.ID] = result
	r.mu.Unlock()
	r.notifyResultReady()

	if err == nil && workspaceErr != nil {
		err = workspaceErr
	}

	return agent.ID, result, err
}

// SpawnAsync creates and starts a new agent asynchronously.
// agentType should be "explore", "bash", "general", "plan", "claude-code-guide", or "coordinator".
func (r *Runner) SpawnAsync(ctx context.Context, agentType string, prompt string, maxTurns int, model string) string {
	deps := r.snapshotAgentDeps()
	agent := r.newConfiguredAgent(ctx, deps, agentType, maxTurns, model, deps.permissions)

	// Wire checkpoint store and enable auto-checkpoint for long-running agents
	if r.store != nil {
		agent.SetStore(r.store)
		if maxTurns > 10 {
			agent.EnableAutoCheckpoint(0)
		}
	}

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.results[agent.ID] = &AgentResult{
		AgentID: agent.ID,
		Type:    agent.Type,
		Status:  AgentStatusPending,
	}
	onStart := r.onAgentStart
	onComplete := r.onAgentComplete
	onAgentProgress := r.onAgentProgress
	r.mu.Unlock()

	// Register with meta-agent for monitoring and wire activity updates
	if deps.metaAgent != nil {
		deps.metaAgent.RegisterAgent(agent.ID, agent.Type)
		agentIDForMeta := agent.ID
		agent.SetOnToolActivity(func(_ string, toolName string, _ map[string]any, status string) {
			if status == "start" {
				deps.metaAgent.UpdateActivity(agentIDForMeta, toolName, agent.GetTurnCount())
			}
		})
	}

	// Report activity to coordinator
	r.reportActivity()

	// Notify UI about agent start
	if onStart != nil {
		onStart(agent.ID, agentType, prompt)
	}

	// Run agent asynchronously with proper cleanup
	go func() {
		agentID := agent.ID

		// Ensure cleanup happens even on panic
		defer func() {
			if p := recover(); p != nil {
				r.mu.Lock()
				if result, ok := r.results[agentID]; ok {
					result.Error = fmt.Sprintf("agent panic: %v", p)
					result.Status = AgentStatusFailed
					result.Completed = true
				}
				r.mu.Unlock()
				r.notifyResultReady()
			}
		}()

		// Detach from caller's context so agent survives tool timeout.
		bgCtx := context.WithoutCancel(ctx)
		var agentCtx context.Context
		var agentCancel context.CancelFunc
		if agentTimeout := agent.GetTimeout(); agentTimeout > 0 {
			agentCtx, agentCancel = context.WithTimeout(bgCtx, agentTimeout)
		} else {
			agentCtx, agentCancel = context.WithCancel(bgCtx)
		}
		defer agentCancel()

		// Store cancel func for explicit Agent.Cancel()
		agent.SetCancelFunc(agentCancel)

		// Check if original context is already cancelled
		select {
		case <-ctx.Done():
			if deps.metaAgent != nil {
				deps.metaAgent.UnregisterAgent(agentID)
			}
			cancelledResult := &AgentResult{
				AgentID:   agentID,
				Type:      agent.Type,
				Status:    AgentStatusCancelled,
				Error:     ctx.Err().Error(),
				Completed: true,
			}
			r.finalizeAgentWorkspace(agent, cancelledResult)
			r.mu.Lock()
			r.results[agentID] = cancelledResult
			r.mu.Unlock()
			r.notifyResultReady()
			return
		default:
		}

		// Start progress ticker for periodic updates
		progressTicker := time.NewTicker(2 * time.Second)
		defer progressTicker.Stop()

		progressCtx, progressCancel := context.WithCancel(agentCtx)
		defer progressCancel()

		go func() {
			for {
				select {
				case <-progressTicker.C:
					progress := agent.GetProgress()
					if onAgentProgress != nil {
						onAgentProgress(agentID, &progress)
					}
				case <-progressCtx.Done():
					return
				}
			}
		}()

		startTime := time.Now()
		result, err := agent.Run(agentCtx, prompt)
		duration := time.Since(startTime)

		// Ensure result is never nil
		if result == nil {
			result = &AgentResult{
				AgentID:   agentID,
				Type:      agent.Type,
				Status:    AgentStatusFailed,
				Error:     "nil result from agent",
				Completed: true,
			}
		}

		// Handle error by updating result status
		if err != nil {
			result.Error = err.Error()
			result.Status = AgentStatusFailed
		}

		// Ensure Completed is always true so WaitWithContext doesn't spin
		result.Completed = true

		// Unregister from meta-agent
		if deps.metaAgent != nil {
			deps.metaAgent.UnregisterAgent(agentID)
		}

		// Save error checkpoint on failure for potential recovery
		if err != nil && r.store != nil && agent.GetTurnCount() > 0 {
			if _, cpErr := agent.SaveCheckpoint("error"); cpErr != nil {
				logging.Debug("failed to save error checkpoint", "agent_id", agent.ID, "error", cpErr)
			}
		}

		// Save agent state for potential resume
		r.finalizeAgentWorkspace(agent, result)
		r.saveAgentState(agent)
		r.recordAgentExecutionLearning(deps, agentType, prompt, result, duration, "spawn_async")

		r.mu.Lock()
		r.results[agentID] = result
		r.mu.Unlock()
		r.notifyResultReady()

		// Notify UI about agent completion
		if onComplete != nil {
			onComplete(agentID, result)
		}
	}()

	return agent.ID
}

// SpawnAsyncWithStreaming creates and starts a new agent asynchronously with streaming support.
// The onText callback receives real-time text output from the agent.
// The onProgress callback receives progress updates.
func (r *Runner) SpawnAsyncWithStreaming(
	ctx context.Context,
	agentType string,
	prompt string,
	maxTurns int,
	model string,
	onText func(string),
	onProgress func(id string, progress *AgentProgress),
) string {
	deps := r.snapshotAgentDeps()
	agent := r.newConfiguredAgent(ctx, deps, agentType, maxTurns, model, deps.permissions)

	// Set up streaming callback
	if onText != nil {
		agent.SetOnText(onText)
	}

	// Wire sub-agent activity callback (chain with meta-agent UpdateActivity)
	r.mu.RLock()
	onSubAgentActivity := r.onSubAgentActivity
	r.mu.RUnlock()
	agent.SetOnToolActivity(func(id, toolName string, args map[string]any, status string) {
		if onSubAgentActivity != nil {
			onSubAgentActivity(id, string(agent.Type), toolName, args, "tool_"+status)
		}
		if deps.metaAgent != nil && status == "start" {
			deps.metaAgent.UpdateActivity(agent.ID, toolName, agent.GetTurnCount())
		}
	})

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.results[agent.ID] = &AgentResult{
		AgentID: agent.ID,
		Type:    agent.Type,
		Status:  AgentStatusPending,
	}
	onStart := r.onAgentStart
	onComplete := r.onAgentComplete
	onAgentProgress := r.onAgentProgress
	r.mu.Unlock()

	// Register with meta-agent for monitoring
	if deps.metaAgent != nil {
		deps.metaAgent.RegisterAgent(agent.ID, agent.Type)
	}

	// Report activity to coordinator
	r.reportActivity()

	// Notify UI about agent start
	if onStart != nil {
		onStart(agent.ID, agentType, prompt)
	}

	// Run agent asynchronously with streaming and progress updates
	go func() {
		agentID := agent.ID

		// Ensure cleanup happens even on panic
		defer func() {
			if p := recover(); p != nil {
				r.mu.Lock()
				if result, ok := r.results[agentID]; ok {
					result.Error = fmt.Sprintf("agent panic: %v", p)
					result.Status = AgentStatusFailed
					result.Completed = true
				}
				r.mu.Unlock()
				r.notifyResultReady()
			}
		}()

		// Detach from caller's context so agent survives tool timeout.
		bgCtx := context.WithoutCancel(ctx)
		var agentCtx context.Context
		var agentCancel context.CancelFunc
		if agentTimeout := agent.GetTimeout(); agentTimeout > 0 {
			agentCtx, agentCancel = context.WithTimeout(bgCtx, agentTimeout)
		} else {
			agentCtx, agentCancel = context.WithCancel(bgCtx)
		}
		defer agentCancel()

		// Store cancel func for explicit Agent.Cancel()
		agent.SetCancelFunc(agentCancel)

		// Start progress ticker for periodic updates
		progressTicker := time.NewTicker(2 * time.Second)
		defer progressTicker.Stop()

		// Create a context with cancellation for the progress goroutine
		progressCtx, progressCancel := context.WithCancel(agentCtx)
		defer progressCancel()

		// Progress update goroutine
		go func() {
			for {
				select {
				case <-progressTicker.C:
					progress := agent.GetProgress()
					if onProgress != nil {
						onProgress(agentID, &progress)
					}
					if onAgentProgress != nil {
						onAgentProgress(agentID, &progress)
					}
				case <-progressCtx.Done():
					return
				}
			}
		}()

		// Check if original context is already cancelled
		select {
		case <-ctx.Done():
			if deps.metaAgent != nil {
				deps.metaAgent.UnregisterAgent(agentID)
			}
			cancelledResult := &AgentResult{
				AgentID:   agentID,
				Type:      agent.Type,
				Status:    AgentStatusCancelled,
				Error:     ctx.Err().Error(),
				Completed: true,
			}
			r.finalizeAgentWorkspace(agent, cancelledResult)
			r.mu.Lock()
			r.results[agentID] = cancelledResult
			r.mu.Unlock()
			r.notifyResultReady()
			return
		default:
		}

		startTime := time.Now()
		result, err := agent.Run(agentCtx, prompt)
		duration := time.Since(startTime)

		// Ensure result is never nil
		if result == nil {
			result = &AgentResult{
				AgentID:   agentID,
				Type:      agent.Type,
				Status:    AgentStatusFailed,
				Error:     "nil result from agent",
				Completed: true,
			}
		}

		// Handle error by updating result status
		if err != nil {
			result.Error = err.Error()
			result.Status = AgentStatusFailed
		}

		// Ensure Completed is always true so WaitWithContext doesn't spin
		result.Completed = true

		// Unregister from meta-agent
		if deps.metaAgent != nil {
			deps.metaAgent.UnregisterAgent(agentID)
		}

		// Save agent state for potential resume
		r.finalizeAgentWorkspace(agent, result)
		r.saveAgentState(agent)
		r.recordAgentExecutionLearning(deps, agentType, prompt, result, duration, "spawn_async_streaming")

		r.mu.Lock()
		r.results[agentID] = result
		r.mu.Unlock()
		r.notifyResultReady()

		// Notify UI about agent completion
		if onComplete != nil {
			onComplete(agentID, result)
		}
	}()

	return agent.ID
}

// SpawnMultiple creates and starts multiple agents in parallel.
func (r *Runner) SpawnMultiple(ctx context.Context, tasks []AgentTask) ([]string, error) {
	ids := make([]string, len(tasks))
	results := make([]*AgentResult, len(tasks))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t AgentTask) {
			defer wg.Done()

			deps := r.snapshotAgentDeps()
			agent := r.newConfiguredAgent(ctx, deps, string(t.Type), t.MaxTurns, t.Model, deps.permissions)

			// Apply thoroughness from task or context
			th := tools.ThoroughnessFromContext(ctx)
			if t.Thoroughness != "" {
				th = tools.ParseThoroughness(t.Thoroughness)
			}
			agent.ApplyThoroughness(th, t.MaxTurns)
			os := tools.OutputStyleFromContext(ctx)
			if t.OutputStyle != "" {
				os = tools.ParseOutputStyle(t.OutputStyle)
			}
			agent.SetOutputStyle(os)

			r.mu.Lock()
			r.agents[agent.ID] = agent
			r.mu.Unlock()

			attachMetaAgentMonitoring(agent, deps.metaAgent)

			// Apply per-agent timeout
			runCtx := ctx
			if agentTimeout := agent.GetTimeout(); agentTimeout > 0 {
				var cancel context.CancelFunc
				runCtx, cancel = context.WithTimeout(ctx, agentTimeout)
				defer cancel()
			}
			startTime := time.Now()
			result, err := agent.Run(runCtx, t.Prompt)
			duration := time.Since(startTime)

			// Ensure result is never nil (matches SpawnAsync pattern)
			if result == nil {
				result = &AgentResult{
					AgentID:   agent.ID,
					Type:      agent.Type,
					Status:    AgentStatusFailed,
					Error:     "nil result from agent",
					Completed: true,
				}
			}
			if err != nil {
				result.Error = err.Error()
				result.Status = AgentStatusFailed
			}
			result.Completed = true

			// Unregister from meta-agent
			if deps.metaAgent != nil {
				deps.metaAgent.UnregisterAgent(agent.ID)
			}

			workspaceErr := r.finalizeAgentWorkspace(agent, result)
			r.saveAgentState(agent)
			r.recordAgentExecutionLearning(deps, string(t.Type), t.Prompt, result, duration, "spawn_multiple")
			if err == nil && workspaceErr != nil {
				err = workspaceErr
			}

			mu.Lock()
			ids[idx] = agent.ID
			results[idx] = result
			if err != nil && firstErr == nil {
				firstErr = err
			}
			mu.Unlock()

			r.mu.Lock()
			r.results[agent.ID] = result
			r.mu.Unlock()
			r.notifyResultReady()
		}(i, task)
	}

	wg.Wait()

	return ids, firstErr
}

// Wait waits for an agent to complete and returns its result.
// Uses a default 10-minute timeout. For context-aware waiting, use WaitWithContext.
func (r *Runner) Wait(agentID string) (*AgentResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return r.WaitWithContext(ctx, agentID)
}

// notifyResultReady signals that an agent result became complete.
// Non-blocking: if the channel already has a pending signal, the send is skipped.
func (r *Runner) notifyResultReady() {
	select {
	case r.resultReady <- struct{}{}:
	default:
	}
}

// WaitWithContext waits for an agent to complete, respecting context cancellation.
func (r *Runner) WaitWithContext(ctx context.Context, agentID string) (*AgentResult, error) {
	// Fast path: check immediately
	r.mu.RLock()
	result, ok := r.results[agentID]
	r.mu.RUnlock()
	if ok && result.Completed {
		return result, nil
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.resultReady:
			r.mu.RLock()
			result, ok := r.results[agentID]
			r.mu.RUnlock()
			if ok && result.Completed {
				return result, nil
			}
		}
	}
}

// WaitWithTimeout waits for an agent to complete with a specific timeout.
func (r *Runner) WaitWithTimeout(agentID string, timeout time.Duration) (*AgentResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return r.WaitWithContext(ctx, agentID)
}

// WaitAll waits for multiple agents to complete.
func (r *Runner) WaitAll(agentIDs []string) ([]*AgentResult, error) {
	results := make([]*AgentResult, len(agentIDs))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for i, id := range agentIDs {
		wg.Add(1)
		go func(idx int, agentID string) {
			defer wg.Done()

			result, err := r.Wait(agentID)

			mu.Lock()
			// Ensure result is never nil
			if result == nil {
				result = &AgentResult{
					AgentID:   agentID,
					Status:    AgentStatusFailed,
					Error:     fmt.Sprintf("wait failed: %v", err),
					Completed: true,
				}
			}
			results[idx] = result
			if err != nil && firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
		}(i, id)
	}

	wg.Wait()
	return results, firstErr
}

// GetResult returns the result for an agent.
func (r *Runner) GetResult(agentID string) (*AgentResult, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result, ok := r.results[agentID]
	return result, ok
}

// GetAgent returns an agent by ID.
func (r *Runner) GetAgent(agentID string) (*Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	agent, ok := r.agents[agentID]
	return agent, ok
}

// Cancel cancels an agent's execution.
func (r *Runner) Cancel(agentID string) error {
	r.mu.Lock()

	agent, ok := r.agents[agentID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("agent not found: %s", agentID)
	}

	agent.Cancel()

	// Update result — must set Completed so WaitWithContext doesn't spin
	completed := false
	if result, ok := r.results[agentID]; ok {
		result.Status = AgentStatusCancelled
		result.Completed = true
		completed = true
	}
	r.mu.Unlock()

	if completed {
		r.notifyResultReady()
	}

	return nil
}

// ListAgents returns all agent IDs.
func (r *Runner) ListAgents() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]string, 0, len(r.agents))
	for id := range r.agents {
		ids = append(ids, id)
	}
	return ids
}

// ListRunning returns IDs of currently running agents.
func (r *Runner) ListRunning() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]string, 0)
	for id, agent := range r.agents {
		if agent.GetStatus() == AgentStatusRunning {
			ids = append(ids, id)
		}
	}
	return ids
}

// Cleanup removes completed agents older than the specified duration.
func (r *Runner) Cleanup(maxAge time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	cleaned := 0

	for id, agent := range r.agents {
		status := agent.GetStatus()
		if status == AgentStatusCompleted || status == AgentStatusFailed || status == AgentStatusCancelled {
			endTime := agent.GetEndTime()
			if !endTime.IsZero() && endTime.Before(cutoff) {
				delete(r.agents, id)
				delete(r.results, id)
				cleaned++
			}
		}
	}

	return cleaned
}

// SetStore sets the agent store for persistence.
func (r *Runner) SetStore(store *AgentStore) {
	r.mu.Lock()
	r.store = store
	r.mu.Unlock()
}

// Resume resumes an agent from a saved state.
func (r *Runner) Resume(ctx context.Context, agentID string, prompt string) (string, error) {
	if r.store == nil {
		return "", fmt.Errorf("agent store not configured")
	}

	// Load state from store
	state, err := r.store.Load(agentID)
	if err != nil {
		return "", fmt.Errorf("failed to load agent state: %w", err)
	}

	// Create a new agent with the same configuration
	deps := r.snapshotAgentDeps()
	agent := r.newRestoredAgent(ctx, deps, state, deps.permissions)
	if r.store != nil {
		agent.SetStore(r.store)
	}

	// Restore history
	if err := agent.RestoreHistory(state); err != nil {
		return "", fmt.Errorf("failed to restore agent history: %w", err)
	}

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.mu.Unlock()

	attachMetaAgentMonitoring(agent, deps.metaAgent)

	// Run agent with the new prompt (continuing from previous context)
	startTime := time.Now()
	result, err := agent.Run(ctx, prompt)
	duration := time.Since(startTime)

	if result == nil {
		result = &AgentResult{
			AgentID:   agent.ID,
			Type:      agent.Type,
			Status:    AgentStatusFailed,
			Error:     "nil result from agent",
			Completed: true,
			Duration:  duration,
		}
	}

	if deps.metaAgent != nil {
		deps.metaAgent.UnregisterAgent(agent.ID)
	}

	workspaceErr := r.finalizeAgentWorkspace(agent, result)
	// Save updated state
	if r.store != nil {
		if err := r.store.Save(agent); err != nil {
			logging.Warn("failed to save agent state", "agent_id", agent.ID, "error", err)
		}
	}

	r.mu.Lock()
	r.results[agent.ID] = result
	r.mu.Unlock()
	r.notifyResultReady()
	r.recordAgentExecutionLearning(deps, string(state.Type), prompt, result, duration, "resume")

	if err == nil && workspaceErr != nil {
		err = workspaceErr
	}
	if err != nil {
		return agent.ID, err
	}

	return agent.ID, nil
}

// ResumeAsync resumes an agent asynchronously.
func (r *Runner) ResumeAsync(ctx context.Context, agentID string, prompt string) (string, error) {
	if r.store == nil {
		return "", fmt.Errorf("agent store not configured")
	}

	// Load state from store
	state, err := r.store.Load(agentID)
	if err != nil {
		return "", fmt.Errorf("failed to load agent state: %w", err)
	}

	// Create a new agent with the same configuration
	deps := r.snapshotAgentDeps()
	agent := r.newRestoredAgent(ctx, deps, state, deps.permissions)
	if r.store != nil {
		agent.SetStore(r.store)
	}

	// Restore history
	if err := agent.RestoreHistory(state); err != nil {
		return "", fmt.Errorf("failed to restore agent history: %w", err)
	}

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.results[agent.ID] = &AgentResult{
		AgentID: agent.ID,
		Type:    agent.Type,
		Status:  AgentStatusPending,
	}
	onStart := r.onAgentStart
	onComplete := r.onAgentComplete
	r.mu.Unlock()

	attachMetaAgentMonitoring(agent, deps.metaAgent)

	// Notify UI about agent start (resumed)
	if onStart != nil {
		onStart(agent.ID, string(state.Type), prompt)
	}

	// Run agent asynchronously with proper cleanup
	go func() {
		// Ensure cleanup happens even on panic
		defer func() {
			if p := recover(); p != nil {
				r.mu.Lock()
				if result, ok := r.results[agent.ID]; ok {
					result.Error = fmt.Sprintf("agent panic: %v", p)
					result.Status = AgentStatusFailed
					result.Completed = true
				}
				r.mu.Unlock()
				r.notifyResultReady()
			}
		}()

		// Detach from caller's context so resumed agent survives tool timeout.
		bgCtx := context.WithoutCancel(ctx)
		agentCtx, agentCancel := context.WithCancel(bgCtx)
		defer agentCancel()

		// Store cancel func for explicit Agent.Cancel()
		agent.SetCancelFunc(agentCancel)

		// Check if original context is already cancelled
		select {
		case <-ctx.Done():
			if deps.metaAgent != nil {
				deps.metaAgent.UnregisterAgent(agent.ID)
			}
			cancelledResult := &AgentResult{
				AgentID:   agent.ID,
				Type:      state.Type,
				Status:    AgentStatusCancelled,
				Error:     ctx.Err().Error(),
				Completed: true,
			}
			r.finalizeAgentWorkspace(agent, cancelledResult)
			r.mu.Lock()
			r.results[agent.ID] = cancelledResult
			r.mu.Unlock()
			r.notifyResultReady()
			return
		default:
		}

		result, err := agent.Run(agentCtx, prompt)
		var duration time.Duration
		if result != nil {
			duration = result.Duration
		}

		// Ensure result is never nil
		if result == nil {
			result = &AgentResult{
				AgentID:   agent.ID,
				Type:      agent.Type,
				Status:    AgentStatusFailed,
				Error:     "nil result from agent",
				Completed: true,
			}
		}

		// Handle error by updating result status
		if err != nil {
			result.Error = err.Error()
			result.Status = AgentStatusFailed
		}

		// Ensure Completed is always true so WaitWithContext doesn't spin
		result.Completed = true

		if deps.metaAgent != nil {
			deps.metaAgent.UnregisterAgent(agent.ID)
		}

		r.finalizeAgentWorkspace(agent, result)
		// Save updated state
		if r.store != nil {
			if saveErr := r.store.Save(agent); saveErr != nil {
				logging.Warn("failed to save agent state",
					"agent_id", agent.ID,
					"error", saveErr)
			}
		}

		r.mu.Lock()
		r.results[agent.ID] = result
		r.mu.Unlock()
		r.notifyResultReady()
		r.recordAgentExecutionLearning(deps, string(state.Type), prompt, result, duration, "resume_async")

		// Notify UI about agent completion
		if onComplete != nil {
			onComplete(agent.ID, result)
		}
	}()

	return agent.ID, nil
}

// saveAgentState saves the agent state if store is configured.
func (r *Runner) saveAgentState(agent *Agent) {
	if r.store != nil {
		if err := r.store.Save(agent); err != nil {
			logging.Warn("failed to save agent state", "agent_id", agent.ID, "error", err)
		}
	}
}

// CleanupOldCheckpoints removes checkpoint files older than maxAge.
func (r *Runner) CleanupOldCheckpoints(maxAge time.Duration) {
	if r.store == nil {
		return
	}
	cleaned, err := r.store.CleanupOldCheckpointFiles(maxAge)
	if err != nil {
		logging.Debug("checkpoint cleanup error", "error", err)
	} else if cleaned > 0 {
		logging.Debug("cleaned up old checkpoint files", "count", cleaned)
	}
}

// ResumeErrorCheckpoints finds error checkpoints and silently resumes agents in the background.
// Each checkpoint is deleted before resume to prevent infinite retry loops.
// Resumed agents do NOT get auto-checkpoint enabled — if they fail again, no new error checkpoint is created.
// Returns the number of agents successfully resumed.
func (r *Runner) ResumeErrorCheckpoints(ctx context.Context) int {
	if r.store == nil {
		return 0
	}

	checkpoints, err := r.store.ListErrorCheckpoints()
	if err != nil || len(checkpoints) == 0 {
		return 0
	}

	resumed := 0
	for _, cp := range checkpoints {
		if cp.AgentState == nil {
			_ = r.store.DeleteCheckpoint(cp.CheckpointID)
			continue
		}

		// Delete BEFORE resume — anti-infinite-retry
		_ = r.store.DeleteCheckpoint(cp.CheckpointID)

		// Create agent, restore from checkpoint
		state := cp.AgentState
		deps := r.snapshotAgentDeps()
		agent := r.newRestoredAgent(ctx, deps, state, deps.permissions)

		// Store without auto-checkpoint (if it fails again, no new error cp)
		if r.store != nil {
			agent.SetStore(r.store)
		}

		if err := agent.RestoreFromCheckpoint(cp); err != nil {
			logging.Debug("failed to restore from checkpoint", "checkpoint_id", cp.CheckpointID, "error", err)
			continue
		}

		r.mu.Lock()
		r.agents[agent.ID] = agent
		r.results[agent.ID] = &AgentResult{AgentID: agent.ID, Type: agent.Type, Status: AgentStatusPending}
		onComplete := r.onAgentComplete
		r.mu.Unlock()

		attachMetaAgentMonitoring(agent, deps.metaAgent)

		stateType := string(state.Type)
		resumePrompt := "You were restarted after an error. Continue your previous task or report what went wrong."

		go func(a *Agent, runDeps runnerAgentDeps, restoredType string, continuationPrompt string) {
			defer func() {
				if p := recover(); p != nil {
					logging.Debug("auto-resumed agent panicked", "agent_id", a.ID, "panic", fmt.Sprintf("%v", p))
					r.mu.Lock()
					if result, ok := r.results[a.ID]; ok {
						result.Error = fmt.Sprintf("agent panic on resume: %v", p)
						result.Status = AgentStatusFailed
						result.Completed = true
					}
					r.mu.Unlock()
					r.notifyResultReady()
				}
			}()

			// Detach from caller's context so resumed agent survives tool timeout.
			bgCtx := context.WithoutCancel(ctx)
			agentCtx, agentCancel := context.WithCancel(bgCtx)
			defer agentCancel()
			a.SetCancelFunc(agentCancel)

			select {
			case <-ctx.Done():
				if runDeps.metaAgent != nil {
					runDeps.metaAgent.UnregisterAgent(a.ID)
				}
				cancelledResult := &AgentResult{
					AgentID:   a.ID,
					Type:      a.Type,
					Status:    AgentStatusCancelled,
					Error:     ctx.Err().Error(),
					Completed: true,
				}
				r.finalizeAgentWorkspace(a, cancelledResult)
				r.mu.Lock()
				r.results[a.ID] = cancelledResult
				r.mu.Unlock()
				r.notifyResultReady()
				return
			default:
			}

			result, err := a.Run(agentCtx, continuationPrompt)
			var duration time.Duration
			if result != nil {
				duration = result.Duration
			}
			if result == nil {
				result = &AgentResult{AgentID: a.ID, Type: a.Type, Status: AgentStatusFailed, Error: "nil result", Completed: true}
			}
			if err != nil {
				result.Error = err.Error()
				result.Status = AgentStatusFailed
			}
			result.Completed = true

			if runDeps.metaAgent != nil {
				runDeps.metaAgent.UnregisterAgent(a.ID)
			}

			r.finalizeAgentWorkspace(a, result)
			r.saveAgentState(a)
			r.recordAgentExecutionLearning(
				runDeps,
				restoredType,
				continuationPrompt,
				result,
				duration,
				"resume_error_checkpoint",
			)

			r.mu.Lock()
			r.results[a.ID] = result
			r.mu.Unlock()
			r.notifyResultReady()

			if onComplete != nil {
				onComplete(a.ID, result)
			}
		}(agent, deps, stateType, resumePrompt)

		resumed++
	}

	if resumed > 0 {
		logging.Debug("auto-resumed agents from error checkpoints", "count", resumed)
	}
	return resumed
}

// ResumeLastCheckpoint finds the most recent checkpoint across all agents and resumes it.
// The checkpoint is deleted before resume to prevent duplicate runs.
// Returns the agent ID and any error.
func (r *Runner) ResumeLastCheckpoint(ctx context.Context) (string, error) {
	if r.store == nil {
		return "", fmt.Errorf("agent store not configured")
	}

	// List all checkpoints (empty agentID = no filter)
	ids, err := r.store.ListCheckpoints("")
	if err != nil {
		return "", fmt.Errorf("failed to list checkpoints: %w", err)
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("no checkpoints found")
	}

	// Checkpoint names include timestamps — pick the lexicographically latest
	latestID := ids[0]
	for _, id := range ids[1:] {
		if id > latestID {
			latestID = id
		}
	}

	cp, err := r.store.LoadCheckpoint(latestID)
	if err != nil {
		return "", fmt.Errorf("failed to load checkpoint %s: %w", latestID, err)
	}
	if cp.AgentState == nil {
		_ = r.store.DeleteCheckpoint(cp.CheckpointID)
		return "", fmt.Errorf("checkpoint %s has no agent state", latestID)
	}

	// Delete before resume to prevent duplicate runs
	_ = r.store.DeleteCheckpoint(cp.CheckpointID)

	state := cp.AgentState
	deps := r.snapshotAgentDeps()
	agent := r.newRestoredAgent(ctx, deps, state, deps.permissions)
	if r.store != nil {
		agent.SetStore(r.store)
	}

	if err := agent.RestoreFromCheckpoint(cp); err != nil {
		return "", fmt.Errorf("failed to restore from checkpoint: %w", err)
	}

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.results[agent.ID] = &AgentResult{AgentID: agent.ID, Type: agent.Type, Status: AgentStatusPending}
	onStart := r.onAgentStart
	onComplete := r.onAgentComplete
	r.mu.Unlock()

	attachMetaAgentMonitoring(agent, deps.metaAgent)

	if onStart != nil {
		onStart(agent.ID, string(state.Type), "Resumed from checkpoint")
	}

	go func(a *Agent, runDeps runnerAgentDeps) {
		defer func() {
			if p := recover(); p != nil {
				logging.Debug("resumed agent panicked", "agent_id", a.ID, "panic", fmt.Sprintf("%v", p))
				r.mu.Lock()
				if result, ok := r.results[a.ID]; ok {
					result.Error = fmt.Sprintf("agent panic on resume: %v", p)
					result.Status = AgentStatusFailed
					result.Completed = true
				}
				r.mu.Unlock()
				r.notifyResultReady()
			}
		}()

		// Detach from caller's context so resumed agent survives tool timeout.
		bgCtx := context.WithoutCancel(ctx)
		agentCtx, agentCancel := context.WithCancel(bgCtx)
		defer agentCancel()
		a.SetCancelFunc(agentCancel)

		result, err := a.Run(agentCtx, "You were resumed from a checkpoint. Continue your previous task.")
		var duration time.Duration
		if result != nil {
			duration = result.Duration
		}
		if result == nil {
			result = &AgentResult{AgentID: a.ID, Type: a.Type, Status: AgentStatusFailed, Error: "nil result", Completed: true}
		}
		if err != nil {
			result.Error = err.Error()
			result.Status = AgentStatusFailed
		}
		result.Completed = true

		if runDeps.metaAgent != nil {
			runDeps.metaAgent.UnregisterAgent(a.ID)
		}

		r.finalizeAgentWorkspace(a, result)
		r.saveAgentState(a)
		r.recordAgentExecutionLearning(
			runDeps,
			string(state.Type),
			"You were resumed from a checkpoint. Continue your previous task.",
			result,
			duration,
			"resume_last_checkpoint",
		)

		r.mu.Lock()
		r.results[a.ID] = result
		r.mu.Unlock()
		r.notifyResultReady()

		if onComplete != nil {
			onComplete(a.ID, result)
		}
	}(agent, deps)

	logging.Debug("resumed agent from last checkpoint", "agent_id", agent.ID, "checkpoint_id", latestID)
	return agent.ID, nil
}

// Close flushes all agent data (project learning) to prevent data loss on shutdown.
func (r *Runner) Close() {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, agent := range r.agents {
		if err := agent.Close(); err != nil {
			logging.Warn("failed to close agent", "agent_id", agent.ID, "error", err)
		}
	}

	// Cleanup old checkpoints on shutdown, keeping only 2 most recent per agent
	if r.store != nil {
		for _, agent := range r.agents {
			if cleaned, err := r.store.CleanupCheckpoints(agent.ID, 2); err == nil && cleaned > 0 {
				logging.Debug("cleaned up agent checkpoints", "agent_id", agent.ID, "cleaned", cleaned)
			}
		}
	}
}
