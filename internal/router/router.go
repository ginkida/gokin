package router

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"gokin/internal/agent"
	"gokin/internal/client"
	"gokin/internal/logging"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

// PlanChecker interface for checking planning state
type PlanChecker interface {
	IsActive() bool
	IsEnabled() bool
}

// routingRecord stores a routing decision and its outcome for learning.
type routingRecord struct {
	message   string
	taskType  TaskType
	strategy  ExecutionStrategy
	success   bool
	timestamp time.Time
}

// Router determines the optimal execution strategy for incoming tasks
// and routes them to the appropriate handler (direct, executor, or sub-agent).
type Router struct {
	analyzer    *TaskAnalyzer
	executor    *tools.Executor
	agentRunner AgentRunner
	client      client.Client
	workDir     string

	// Tool filtering
	registry  *tools.Registry // Tool registry for per-request filtering
	isGitRepo bool            // Whether working dir is a git repo

	// Plan awareness
	planChecker PlanChecker

	// Configuration
	enabled            bool
	decomposeThreshold int
	parallelThreshold  int
	costAware          bool
	fastModel          string
	modelCapability    *ModelCapability

	// Learned routing
	routingHistory []routingRecord
	historyMu      sync.RWMutex

	// Context awareness
	recentErrors     int
	recentOps        int
	conversationMode string // "exploring", "implementing", "debugging", "refactoring"

	// Session-depth counters feed the adaptive thinking-budget logic. Executor
	// calls RecordTurn after each model turn; ResetDepth clears on /clear.
	depthMu       sync.RWMutex
	depthTurns    int
	depthTools    int
	depthPressure bool
}

// AgentRunner interface for spawning agents (implemented by agent.Runner)
type AgentRunner interface {
	Spawn(ctx context.Context, agentType string, prompt string, maxTurns int, model string) (string, error)
	SpawnAsync(ctx context.Context, agentType string, prompt string, maxTurns int, model string) string
	GetResult(agentID string) (*agent.AgentResult, bool)
}

// RouterConfig holds configuration for the router
type RouterConfig struct {
	Enabled            bool
	DecomposeThreshold int              // Default: 4
	ParallelThreshold  int              // Default: 7
	CostAware          bool             // Enable cost-aware model selection
	FastModel          string           // Model for simple tasks (e.g., "gemini-2.0-flash")
	ModelCapability    *ModelCapability // Model capability for adaptive routing
}

// NewRouter creates a new task router
func NewRouter(cfg *RouterConfig, executor *tools.Executor, agentRunner AgentRunner, client client.Client, registry *tools.Registry, isGitRepo bool, workDir string) *Router {
	if cfg == nil {
		cfg = &RouterConfig{
			Enabled:            true,
			DecomposeThreshold: 4,
			ParallelThreshold:  7,
		}
	}

	return &Router{
		analyzer:           NewTaskAnalyzer(cfg.DecomposeThreshold, cfg.ParallelThreshold),
		executor:           executor,
		agentRunner:        agentRunner,
		client:             client,
		workDir:            workDir,
		registry:           registry,
		isGitRepo:          isGitRepo,
		enabled:            cfg.Enabled,
		decomposeThreshold: cfg.DecomposeThreshold,
		parallelThreshold:  cfg.ParallelThreshold,
		costAware:          cfg.CostAware,
		fastModel:          cfg.FastModel,
		modelCapability:    cfg.ModelCapability,
		routingHistory:     make([]routingRecord, 0, 100),
	}
}

// SetPlanChecker sets the plan checker for plan-aware routing.
func (r *Router) SetPlanChecker(checker PlanChecker) {
	r.historyMu.Lock()
	defer r.historyMu.Unlock()
	r.planChecker = checker
}

// Route determines the best execution strategy and returns a routing decision
func (r *Router) Route(message string) *RoutingDecision {
	return r.RouteWithContext(context.Background(), message)
}

// RouteWithContext is like Route but propagates the caller's context to
// LLM-backed decomposition. Prefer this when a context is available so the
// operation can be cancelled by parent timeout.
func (r *Router) RouteWithContext(ctx context.Context, message string) *RoutingDecision {
	analysis := r.analyzer.Analyze(message)

	// Model capability adjustments: weaker models decompose earlier
	r.applyCapabilityAdjustments(analysis)

	// If a plan is actively being executed, prefer simpler strategies
	// to avoid nested planning/coordination
	r.historyMu.RLock()
	planChecker := r.planChecker
	r.historyMu.RUnlock()
	planActive := planChecker != nil && planChecker.IsActive()
	if planActive {
		logging.Debug("plan active, using simplified routing",
			"original_score", analysis.Score,
			"original_strategy", analysis.Strategy)
		// Reduce complexity score when plan is active to prevent nested decomposition
		if analysis.Score > r.decomposeThreshold {
			analysis.Score = r.decomposeThreshold - 1
		}
		// Don't use sub-agents during plan execution (plan steps are already sub-agents)
		if analysis.Strategy == StrategySubAgent {
			analysis.Strategy = StrategyExecutor
		}
	}

	// Context-aware adjustment: high error rate suggests debugging mode
	if r.GetErrorRate() > 0.3 && analysis.Strategy != StrategySubAgent {
		logging.Debug("high error rate detected, preferring executor for debugging",
			"error_rate", r.GetErrorRate(),
			"mode", r.GetConversationMode())
		// In debugging mode, prefer executor over direct/sub-agent
		// because it allows iterative tool use
		if analysis.Strategy == StrategyDirect {
			analysis.Strategy = StrategyExecutor
		}
	}

	logging.Debug("task routed",
		"message", message,
		"complexity", analysis.Score,
		"type", analysis.Type,
		"strategy", analysis.Strategy,
		"reasoning", analysis.Reasoning)

	// Adjust strategy based on learned history
	r.adjustStrategyFromHistory(analysis)

	decision := &RoutingDecision{
		Analysis:    analysis,
		Message:     message,
		ShouldRoute: true,
	}

	// Check for automatic decomposition for high-complexity tasks
	if analysis.Score >= r.decomposeThreshold {
		decomposition := r.analyzer.DecomposeWithContext(ctx, message)
		if len(decomposition.Subtasks) > 1 {
			decision.Handler = HandlerCoordinated
			decision.Decomposition = decomposition
			decision.Reasoning = fmt.Sprintf("Auto-decomposition: %d subtasks (%s)",
				len(decomposition.Subtasks), decomposition.Reasoning)
			decision.SuggestedToolSets = r.selectToolSets(analysis)

			logging.Info("task decomposed",
				"message", message,
				"subtasks", len(decomposition.Subtasks),
				"can_parallel", decomposition.CanParallel)

			return decision
		}
	}

	// Determine which handler to use
	switch analysis.Strategy {
	case StrategyDirect:
		decision.Handler = HandlerDirect
		decision.Reasoning = "Direct AI response without tools"

	case StrategySingleTool:
		decision.Handler = HandlerExecutor
		decision.Reasoning = "Expecting a single tool call"

	case StrategyExecutor:
		decision.Handler = HandlerExecutor
		decision.Reasoning = "Standard execution via function calling loop"

	case StrategySubAgent:
		decision.Handler = HandlerSubAgent
		decision.SubAgentType = r.selectSubAgentType(analysis.Type)
		decision.Background = analysis.Type == TaskTypeBackground
		decision.Reasoning = fmt.Sprintf("Using sub-agent type '%s'", decision.SubAgentType)

	default:
		decision.Handler = HandlerExecutor
		decision.Reasoning = "Standard strategy"
	}

	// Cost-aware model selection
	if r.costAware && r.fastModel != "" {
		decision.SuggestedModel = r.selectCostAwareModel(analysis)
	}

	// Dynamic thinking budget based on complexity
	decision.ThinkingBudget = r.selectThinkingBudget(analysis)

	// Per-request tool filtering
	decision.SuggestedToolSets = r.selectToolSets(analysis)

	return decision
}

// Execute routes the task to the appropriate handler and returns the result
func (r *Router) Execute(ctx context.Context, history []*genai.Content, message string) ([]*genai.Content, string, error) {
	decision := r.RouteWithContext(ctx, message)

	// Apply thinking budget for this request
	r.client.SetThinkingBudget(decision.ThinkingBudget)

	// Apply per-request tool filtering
	if r.registry != nil && len(decision.SuggestedToolSets) > 0 {
		r.client.SetTools(r.registry.FilteredGeminiTools(decision.SuggestedToolSets...))
	}

	// Add tool usage hint based on task type
	if hint := r.toolHint(decision.Analysis); hint != "" {
		message = hint + "\n\n" + message
	}

	// Add thinking hint for complex tasks
	if decision.Analysis.Score >= 4 || decision.Analysis.Strategy == StrategySubAgent {
		message = "Before acting, analyze the problem step by step and consider edge cases.\n\n" + message
	}

	switch decision.Handler {
	case HandlerDirect:
		// Direct AI response without tools
		return r.executeDirect(ctx, history, message)

	case HandlerExecutor:
		// Standard function calling loop
		return r.executor.Execute(ctx, history, message)

	case HandlerSubAgent:
		// Spawn a sub-agent
		return r.executeViaSubAgent(ctx, message, decision.SubAgentType, decision.Background)

	case HandlerCoordinated:
		// Execute via coordinator with decomposed subtasks
		return r.executeCoordinated(ctx, decision.Decomposition)

	default:
		return r.executor.Execute(ctx, history, message)
	}
}

// executeDirect gets a direct AI response without tool usage.
// executor.Execute already prepends the user message, so we must NOT add it here.
func (r *Router) executeDirect(ctx context.Context, history []*genai.Content, message string) ([]*genai.Content, string, error) {
	return r.executor.Execute(ctx, history, message)
}

// executeViaSubAgent spawns a sub-agent to handle the task
func (r *Router) executeViaSubAgent(ctx context.Context, message string, agentType string, background bool) ([]*genai.Content, string, error) {
	if r.agentRunner == nil {
		return nil, "", fmt.Errorf("sub-agent requested but agent runner is not configured")
	}

	// Auto-infer thoroughness for explore/bash agents based on task complexity
	if agentType == "explore" || agentType == "bash" {
		analysis := r.analyzer.Analyze(message)
		switch {
		case analysis.Score <= 3:
			ctx = tools.WithThoroughness(ctx, tools.ThoroughnessQuick)
		case analysis.Score >= 7:
			ctx = tools.WithThoroughness(ctx, tools.ThoroughnessThorough)
		}
	}

	logging.Info("spawning sub-agent",
		"type", agentType,
		"background", background,
		"message", message)

	var agentID string
	var err error

	if background {
		// Spawn in background, return immediately
		agentID = r.agentRunner.SpawnAsync(ctx, agentType, message, 30, "")
		backgroundMsg := fmt.Sprintf("Background agent %s started for the task. ID: %s\nUse /task_output %s to check status.", agentType, agentID, agentID)
		return nil, backgroundMsg, nil
	}

	// Spawn and wait for completion
	agentID, err = r.agentRunner.Spawn(ctx, agentType, message, 30, "")
	if err != nil {
		return nil, "", fmt.Errorf("failed to spawn sub-agent: %w", err)
	}

	// Get result
	result, ok := r.agentRunner.GetResult(agentID)
	if !ok {
		return nil, "", fmt.Errorf("sub-agent %s did not return a result", agentID)
	}

	if result.Error != "" {
		return nil, "", fmt.Errorf("sub-agent failed: %s", result.Error)
	}

	// Format output
	var response string
	if result.Output != "" {
		response = result.Output
	} else {
		response = fmt.Sprintf("Agent %s completed the task", agentType)
	}

	// Return minimal history (just the result)
	history := []*genai.Content{
		genai.NewContentFromText(response, genai.RoleModel),
	}

	return history, response, nil
}

// selectSubAgentType chooses the appropriate sub-agent type based on task type
func (r *Router) selectSubAgentType(taskType TaskType) string {
	switch taskType {
	case TaskTypeExploration:
		return "explore"
	case TaskTypeBackground:
		return "bash"
	case TaskTypeRefactoring:
		return "general" // Refactoring needs write access
	case TaskTypeComplex:
		return "general"
	case TaskTypeMultiTool:
		return "general"
	default:
		return "general"
	}
}

// SubtaskResult holds the result of a subtask execution.
type SubtaskResult struct {
	ID      string
	AgentID string
	Output  string
	Error   string
	Success bool
}

// maxParallelSubtasks is the maximum number of subtasks to execute in parallel.
const maxParallelSubtasks = 5

// executeCoordinated executes decomposed subtasks via coordinator
func (r *Router) executeCoordinated(ctx context.Context, decomposition *DecompositionResult) ([]*genai.Content, string, error) {
	if r.agentRunner == nil {
		return nil, "", fmt.Errorf("agent runner not configured for coordination")
	}

	logging.Info("executing coordinated task",
		"subtasks", len(decomposition.Subtasks),
		"can_parallel", decomposition.CanParallel)

	var allOutputs strings.Builder
	allOutputs.WriteString("## Coordinated Execution\n\n")
	fmt.Fprintf(&allOutputs, "Task decomposed into %d subtasks.\n\n", len(decomposition.Subtasks))

	if decomposition.CanParallel {
		allOutputs.WriteString("**Mode:** Parallel execution\n\n")
	} else {
		allOutputs.WriteString("**Mode:** Sequential execution\n\n")
	}

	// Track completed tasks and their results
	completed := make(map[string]bool)
	subtaskResults := make(map[string]*SubtaskResult)
	var resultsMu sync.Mutex

	for {
		// Find ready tasks (dependencies met)
		var ready []Subtask
		for _, st := range decomposition.Subtasks {
			if completed[st.ID] {
				continue
			}

			// Check dependencies
			depsOK := true
			for _, dep := range st.Dependencies {
				if !completed[dep] {
					depsOK = false
					break
				}
			}

			if depsOK {
				ready = append(ready, st)
			}
		}

		if len(ready) == 0 {
			break // All done or blocked
		}

		// Execute ready tasks - parallel if allowed, sequential otherwise
		if decomposition.CanParallel && len(ready) > 1 {
			// Parallel execution with semaphore
			var wg sync.WaitGroup
			semaphore := make(chan struct{}, maxParallelSubtasks)

			for _, st := range ready {
				wg.Add(1)
				semaphore <- struct{}{} // Acquire semaphore

				go func(subtask Subtask) {
					defer wg.Done()
					defer func() { <-semaphore }()
					defer func() {
						if rec := recover(); rec != nil {
							logging.Error("subtask goroutine panic", "id", subtask.ID, "panic", rec)
							resultsMu.Lock()
							subtaskResults[subtask.ID] = &SubtaskResult{
								ID:    subtask.ID,
								Error: fmt.Sprintf("internal panic: %v", rec),
							}
							completed[subtask.ID] = true
							resultsMu.Unlock()
						}
					}()

					result := r.executeSubtask(ctx, subtask)

					resultsMu.Lock()
					subtaskResults[subtask.ID] = result
					completed[subtask.ID] = true
					resultsMu.Unlock()
				}(st)
			}

			wg.Wait()
		} else {
			// Sequential execution
			for _, st := range ready {
				result := r.executeSubtask(ctx, st)
				subtaskResults[st.ID] = result
				completed[st.ID] = true
			}
		}
	}

	// Write results in order
	successCount := 0
	failedCount := 0

	for _, st := range decomposition.Subtasks {
		result, ok := subtaskResults[st.ID]
		if !ok {
			continue
		}

		fmt.Fprintf(&allOutputs, "### Subtask: %s (%s)\n", st.ID, st.AgentType)
		fmt.Fprintf(&allOutputs, "Prompt: %s\n\n", st.Prompt)

		if result.Success {
			successCount++
			output := result.Output
			if runes := []rune(output); len(runes) > 1000 {
				output = string(runes[:1000]) + "...[truncated]"
			}
			allOutputs.WriteString("**Status:** Completed\n\n")
			fmt.Fprintf(&allOutputs, "**Result:**\n%s\n\n", output)
		} else {
			failedCount++
			fmt.Fprintf(&allOutputs, "**Status:** Error - %s\n\n", result.Error)
		}
	}

	// Summary
	allOutputs.WriteString("---\n")
	fmt.Fprintf(&allOutputs, "**Total:** %d succeeded, %d failed out of %d subtasks\n",
		successCount, failedCount, len(decomposition.Subtasks))

	response := allOutputs.String()
	history := []*genai.Content{
		genai.NewContentFromText(response, genai.RoleModel),
	}

	return history, response, nil
}

// executeSubtask executes a single subtask and returns the result.
func (r *Router) executeSubtask(ctx context.Context, st Subtask) *SubtaskResult {
	result := &SubtaskResult{
		ID: st.ID,
	}

	agentID, err := r.agentRunner.Spawn(ctx, st.AgentType, st.Prompt, 20, "")
	if err != nil {
		result.Error = err.Error()
		result.Success = false
		return result
	}

	result.AgentID = agentID

	agentResult, ok := r.agentRunner.GetResult(agentID)
	if !ok {
		result.Error = "no result returned"
		result.Success = false
		return result
	}

	if agentResult.Error != "" {
		result.Error = agentResult.Error
		result.Success = false
		return result
	}

	result.Output = agentResult.Output
	result.Success = true
	return result
}

// GetAnalysis returns the task analysis without executing
func (r *Router) GetAnalysis(message string) *TaskComplexity {
	return r.analyzer.Analyze(message)
}

// RecordRoutingOutcome records whether a routing decision was successful.
func (r *Router) RecordRoutingOutcome(message string, analysis *TaskComplexity, success bool) {
	r.historyMu.Lock()
	defer r.historyMu.Unlock()

	r.routingHistory = append(r.routingHistory, routingRecord{
		message:   message,
		taskType:  analysis.Type,
		strategy:  analysis.Strategy,
		success:   success,
		timestamp: time.Now(),
	})

	// Keep last 100 records
	if len(r.routingHistory) > 100 {
		r.routingHistory = r.routingHistory[len(r.routingHistory)-100:]
	}
}

// getStrategySuccessRate returns the success rate for a given strategy from history.
func (r *Router) getStrategySuccessRate(strategy ExecutionStrategy) float64 {
	r.historyMu.RLock()
	defer r.historyMu.RUnlock()

	total := 0
	successes := 0
	for _, rec := range r.routingHistory {
		if rec.strategy == strategy {
			total++
			if rec.success {
				successes++
			}
		}
	}

	if total < 3 {
		return 0.5 // Not enough data
	}
	return float64(successes) / float64(total)
}

// adjustStrategyFromHistory adjusts the routing strategy based on historical success rates.
func (r *Router) adjustStrategyFromHistory(analysis *TaskComplexity) {
	currentRate := r.getStrategySuccessRate(analysis.Strategy)

	// If current strategy has poor success rate (<30%), try alternatives
	if currentRate < 0.3 && currentRate > 0 {
		// Try upgrading to a more capable strategy
		switch analysis.Strategy {
		case StrategyDirect:
			altRate := r.getStrategySuccessRate(StrategyExecutor)
			if altRate > currentRate {
				logging.Debug("learned routing override",
					"from", analysis.Strategy,
					"to", StrategyExecutor,
					"current_rate", currentRate,
					"alt_rate", altRate)
				analysis.Strategy = StrategyExecutor
			}
		case StrategyExecutor:
			altRate := r.getStrategySuccessRate(StrategySubAgent)
			if altRate > currentRate {
				logging.Debug("learned routing override",
					"from", analysis.Strategy,
					"to", StrategySubAgent,
					"current_rate", currentRate,
					"alt_rate", altRate)
				analysis.Strategy = StrategySubAgent
			}
		}
	}
}

// RoutingDecision represents the routing decision for a task
type RoutingDecision struct {
	Analysis          *TaskComplexity
	Message           string
	Handler           HandlerType
	SubAgentType      string
	Background        bool
	ShouldRoute       bool
	Reasoning         string
	Decomposition     *DecompositionResult // For HandlerCoordinated
	LearnedExamples   []LearnedExample     // Similar past tasks (Phase 2)
	SuggestedModel    string               // Cost-aware model suggestion (empty = use default)
	ThinkingBudget    int32                // 0 = disabled, >0 = max thinking tokens
	SuggestedToolSets []tools.ToolSet      // Tool sets for this request
}

// LearnedExample contains information about a learned example for few-shot learning.
type LearnedExample struct {
	ID        string
	TaskType  string
	Prompt    string
	AgentType string
	Score     float64
}

// HandlerType represents the execution handler
type HandlerType string

const (
	HandlerDirect      HandlerType = "direct"
	HandlerExecutor    HandlerType = "executor"
	HandlerSubAgent    HandlerType = "sub_agent"
	HandlerCoordinated HandlerType = "coordinated"
)

// String returns the string representation
func (h HandlerType) String() string {
	return string(h)
}

// selectThinkingBudget returns the thinking token budget based on task complexity
// and model capability. Weaker models get proportionally more thinking budget.
//
// The base budget scales by strategy (Direct=0 → SubAgent=4096). Two adaptive
// layers stack on top:
//
//  1. Session-depth scaling: once the session has accumulated state (many
//     turns, many tool calls, or a compaction has fired), the task in front
//     of the model is usually more involved than its prompt suggests. Scale
//     by min(1 + turns/10 + tools/20, 2.0).
//  2. Last-request pressure: if the previous request hit a stream-idle
//     timeout or returned an empty response, the budget was likely too
//     small — bump by 1.5x for the next one (decays on success).
//
// Hard-capped at 16384 to stop runaway thinking on pathological inputs.
func (r *Router) selectThinkingBudget(analysis *TaskComplexity) int32 {
	var budget int32
	switch analysis.Strategy {
	case StrategyDirect:
		budget = 0
	case StrategySingleTool:
		if analysis.Score <= 2 {
			budget = 0
		} else {
			budget = 1024
		}
	case StrategyExecutor:
		if analysis.Score >= 5 {
			budget = 2048
		} else {
			budget = 1024
		}
	case StrategySubAgent:
		budget = 4096
	}

	if budget > 0 && r.modelCapability != nil {
		budget = int32(float64(budget) * r.modelCapability.ThinkingMultiplier)
	}
	if budget > 0 {
		budget = int32(float64(budget) * r.sessionDepthMultiplier())
	}
	if budget > 0 && r.pressureBoost() {
		budget = int32(float64(budget) * 1.5)
	}
	const maxBudget int32 = 16384
	if budget > maxBudget {
		budget = maxBudget
	}
	return budget
}

// sessionDepthMultiplier returns a factor in [1.0, 2.0] that grows with
// accumulated session activity. Linear in (turns/10 + tools/20), clamped.
// Zero when no session stats are wired — caller gets neutral 1.0.
func (r *Router) sessionDepthMultiplier() float64 {
	turns, tools := r.sessionDepthStats()
	if turns == 0 && tools == 0 {
		return 1.0
	}
	mult := 1.0 + float64(turns)/10.0 + float64(tools)/20.0
	if mult > 2.0 {
		mult = 2.0
	}
	return mult
}

// sessionDepthStats returns current (turn count, tool call count) from the
// router-scoped counters. Hooks are called by Execute() on each turn; in
// tests they stay zero and the multiplier collapses to 1.0.
func (r *Router) sessionDepthStats() (turns, tools int) {
	r.depthMu.RLock()
	defer r.depthMu.RUnlock()
	return r.depthTurns, r.depthTools
}

// pressureBoost reports whether the previous request showed signs of
// budget starvation (stream-idle or empty-response). One-shot flag —
// cleared on next successful response.
func (r *Router) pressureBoost() bool {
	r.depthMu.RLock()
	defer r.depthMu.RUnlock()
	return r.depthPressure
}

// RecordTurn is invoked by the executor at the end of each model turn to
// feed the adaptive budget logic with session activity.
func (r *Router) RecordTurn(toolCalls int, pressure bool) {
	r.depthMu.Lock()
	defer r.depthMu.Unlock()
	r.depthTurns++
	r.depthTools += toolCalls
	r.depthPressure = pressure
}

// ResetDepth clears session activity counters — called on /clear or new
// session load so old state doesn't leak across conversations.
func (r *Router) ResetDepth() {
	r.depthMu.Lock()
	defer r.depthMu.Unlock()
	r.depthTurns = 0
	r.depthTools = 0
	r.depthPressure = false
}

// selectCostAwareModel returns the fast model for simple tasks, empty for complex ones.
func (r *Router) selectCostAwareModel(analysis *TaskComplexity) string {
	// Use fast model for direct responses and simple single-tool calls
	switch analysis.Strategy {
	case StrategyDirect:
		return r.fastModel
	case StrategySingleTool:
		// Single tool calls with low complexity can use fast model
		if analysis.Score <= 2 {
			return r.fastModel
		}
	}
	// Complex tasks use the default (primary) model
	return ""
}

// applyCapabilityAdjustments modifies task analysis based on model capability.
// Weaker models get lower decompose thresholds (complex tasks split earlier).
func (r *Router) applyCapabilityAdjustments(analysis *TaskComplexity) {
	if r.modelCapability == nil || r.modelCapability.DecomposeAdjust == 0 {
		return
	}

	effectiveThreshold := max(r.decomposeThreshold+r.modelCapability.DecomposeAdjust, 2)

	// If task exceeds adjusted threshold but not original, force sub-agent strategy
	if analysis.Score >= effectiveThreshold && analysis.Score < r.decomposeThreshold {
		analysis.Strategy = StrategySubAgent
		analysis.Reasoning += fmt.Sprintf(" (model-adjusted: %s tier lowers decompose threshold to %d)",
			r.modelCapability.Tier, effectiveThreshold)
	}
}

// selectToolSets determines which tool sets to include based on task analysis.
// Uses both Strategy and TaskType to minimize tool declarations per request,
// reducing token overhead (~60 tokens per tool declaration).
func (r *Router) selectToolSets(analysis *TaskComplexity) []tools.ToolSet {
	// Base: core + memory are always included. Memory (remember/recall/
	// forget/list) is a fundamental capability — gating it per-strategy
	// silently broke "запомни это" / "recall X" requests because Direct/
	// SingleTool routes stripped the tool list, then the model honestly
	// reported "I can't access persistent memory". 4 tool declarations
	// ≈ 240 tokens per request (cached on repeat); worth it for
	// deterministic agent continuity.
	sets := []tools.ToolSet{tools.ToolSetCore, tools.ToolSetMemory}

	switch analysis.Strategy {
	case StrategyDirect:
		// Pure Q&A — only core, no git/fileops/web needed.
		return r.filterToolSetsByCapability(sets)

	case StrategySingleTool:
		// Single tool call: core covers read/write/edit/bash/glob/grep.
		if r.isGitRepo {
			sets = append(sets, tools.ToolSetGit)
		}
		return r.filterToolSetsByCapability(sets)

	case StrategyExecutor:
		// Task-type-aware filtering within executor strategy.
		switch analysis.Type {
		case TaskTypeQuestion:
			return r.filterToolSetsByCapability(sets)

		case TaskTypeExploration:
			if r.isGitRepo {
				sets = append(sets, tools.ToolSetGit)
			}
			return r.filterToolSetsByCapability(sets)

		case TaskTypeRefactoring:
			if r.isGitRepo {
				sets = append(sets, tools.ToolSetGit)
			}
			sets = append(sets, tools.ToolSetFileOps, tools.ToolSetAdvanced)
			return r.filterToolSetsByCapability(sets)

		case TaskTypeComplex:
			if r.isGitRepo {
				sets = append(sets, tools.ToolSetGit)
			}
			sets = append(sets, tools.ToolSetFileOps, tools.ToolSetWeb, tools.ToolSetAdvanced)
			return r.filterToolSetsByCapability(sets)

		default:
			if r.isGitRepo {
				sets = append(sets, tools.ToolSetGit)
			}
			sets = append(sets, tools.ToolSetFileOps)
			return r.filterToolSetsByCapability(sets)
		}

	case StrategySubAgent:
		if r.isGitRepo {
			sets = append(sets, tools.ToolSetGit)
		}
		sets = append(sets, tools.ToolSetFileOps, tools.ToolSetWeb,
			tools.ToolSetAdvanced, tools.ToolSetPlanning,
			tools.ToolSetAgent, tools.ToolSetMemory)
		return r.filterToolSetsByCapability(sets)
	}

	// Fallback: core + git + fileops
	if r.isGitRepo {
		sets = append(sets, tools.ToolSetGit)
	}
	sets = append(sets, tools.ToolSetFileOps)
	return r.filterToolSetsByCapability(sets)
}

// filterToolSetsByCapability drops tool sets that historically confused
// weaker models. The policy has been narrowed over time as specific
// "stripped but actually useful" bugs surfaced — each exclusion needs
// to justify itself against the alternative of a silent capability
// failure ("I can't access X") the user sees.
//
// Current allowlist:
//   - Weak: Core, Git, FileOps, Memory, Web, Planning
//   - Medium: also Advanced (Kimi/GLM-4/MiniMax are capable enough)
//   - Strong: no filtering
//
// Dropped for non-Strong tiers:
//   - ToolSetAgent (ask_agent/coordinate/shared_memory/update_scratchpad/
//     request_tool) — genuinely advanced orchestration primitives that
//     do confuse lower tiers. Sub-agents get them via a separate path.
//   - ToolSetSemantic — removed in v0.65.0 entirely; still safe to omit.
func (r *Router) filterToolSetsByCapability(sets []tools.ToolSet) []tools.ToolSet {
	if r.modelCapability == nil || r.modelCapability.Tier >= CapabilityStrong {
		return sets
	}

	weakAllow := map[tools.ToolSet]bool{
		tools.ToolSetCore:     true,
		tools.ToolSetGit:      true,
		tools.ToolSetFileOps:  true,
		tools.ToolSetMemory:   true,
		tools.ToolSetWeb:      true,
		tools.ToolSetPlanning: true,
	}
	mediumAllow := map[tools.ToolSet]bool{
		tools.ToolSetAdvanced: true,
	}

	var filtered []tools.ToolSet
	for _, s := range sets {
		if weakAllow[s] {
			filtered = append(filtered, s)
			continue
		}
		if r.modelCapability.Tier == CapabilityMedium && mediumAllow[s] {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// toolHint returns an optional prompt hint based on task type.
func (r *Router) toolHint(analysis *TaskComplexity) string {
	switch analysis.Type {
	case TaskTypeExploration:
		return "For this task, prefer read, glob, grep, and tree for exploring code. Avoid write/edit unless explicitly asked."
	case TaskTypeRefactoring:
		return "For this task, prefer edit over write to make targeted changes. Use diff to verify."
	case TaskTypeQuestion:
		return "" // No hint for simple questions
	default:
		return ""
	}
}

// TrackOperation records an operation outcome for context awareness.
func (r *Router) TrackOperation(toolName string, success bool) {
	r.historyMu.Lock()
	defer r.historyMu.Unlock()

	r.recentOps++
	if !success {
		r.recentErrors++
	}

	// Reset counters every 20 operations
	if r.recentOps >= 20 {
		r.recentOps = 0
		r.recentErrors = 0
	}

	// Update conversation mode based on tool usage patterns
	r.updateConversationMode(toolName)
}

// updateConversationMode infers the conversation mode from recent tool usage.
func (r *Router) updateConversationMode(toolName string) {
	switch {
	case toolName == "grep" || toolName == "glob" || toolName == "read" || toolName == "tree":
		r.conversationMode = "exploring"
	case toolName == "write" || toolName == "edit":
		r.conversationMode = "implementing"
	case toolName == "bash" && r.recentErrors > 2:
		r.conversationMode = "debugging"
	}
}

// GetConversationMode returns the current inferred conversation mode.
func (r *Router) GetConversationMode() string {
	r.historyMu.RLock()
	defer r.historyMu.RUnlock()

	if r.conversationMode == "" {
		return "exploring"
	}
	return r.conversationMode
}

// GetErrorRate returns the recent error rate.
func (r *Router) GetErrorRate() float64 {
	r.historyMu.RLock()
	defer r.historyMu.RUnlock()

	if r.recentOps == 0 {
		return 0
	}
	return float64(r.recentErrors) / float64(r.recentOps)
}
