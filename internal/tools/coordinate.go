package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"gokin/internal/logging"
	"google.golang.org/genai"
)

// CoordinatedTaskDef defines a task for coordination.
type CoordinatedTaskDef struct {
	Prompt       string   `json:"prompt"`
	AgentType    string   `json:"agent_type"`
	Priority     int      `json:"priority"`
	Dependencies []string `json:"dependencies,omitempty"`
}

// CoordinateCallback is called when coordination events occur.
type CoordinateCallback interface {
	OnTaskStart(taskID string, agentType string, prompt string)
	OnTaskComplete(taskID string, success bool, output string)
	OnTaskProgress(taskID string, progress float64, currentStep string) // Phase 2: Progress updates
	OnTaskText(taskID string, text string)                              // Phase 2: Streaming text
	OnAllComplete(results map[string]string)
}

// CoordinateTool manages parallel agent execution with dependencies.
type CoordinateTool struct {
	coordinatorFactory func() any // Returns *agent.Coordinator
	callback           CoordinateCallback
}

// NewCoordinateTool creates a new coordinate tool.
func NewCoordinateTool() *CoordinateTool {
	return &CoordinateTool{}
}

// SetCoordinatorFactory sets the factory function for creating coordinators.
func (t *CoordinateTool) SetCoordinatorFactory(factory func() any) {
	t.coordinatorFactory = factory
}

// SetCallback sets the callback for coordination events.
func (t *CoordinateTool) SetCallback(cb CoordinateCallback) {
	t.callback = cb
}

func (t *CoordinateTool) Name() string {
	return "coordinate"
}

func (t *CoordinateTool) Description() string {
	return `Coordinates multiple agents to work in parallel on related tasks. Use this when you need to:
1. Run multiple independent tasks in parallel (e.g., explore code AND run tests)
2. Run tasks with dependencies (e.g., explore first, THEN refactor)
3. Split a complex task into subtasks with proper orchestration

Each task can specify an agent type (explore, bash, general, plan) and dependencies on other tasks.
Tasks without dependencies run in parallel. Tasks with dependencies wait for their prerequisites.`
}

func (t *CoordinateTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"tasks": {
					Type:        genai.TypeArray,
					Description: "List of tasks to coordinate",
					Items: &genai.Schema{
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"id": {
								Type:        genai.TypeString,
								Description: "Unique identifier for this task (used for dependencies)",
							},
							"prompt": {
								Type:        genai.TypeString,
								Description: "The task description/prompt for the agent",
							},
							"agent_type": {
								Type:        genai.TypeString,
								Description: "Type of agent: 'explore', 'bash', 'general', or 'plan'",
								Enum:        []string{"explore", "bash", "general", "plan"},
							},
							"priority": {
								Type:        genai.TypeInteger,
								Description: "Priority (1-10, higher runs first). Default: 5",
							},
							"depends_on": {
								Type:        genai.TypeArray,
								Description: "Task IDs that must complete before this task starts",
								Items: &genai.Schema{
									Type: genai.TypeString,
								},
							},
						},
						Required: []string{"id", "prompt", "agent_type"},
					},
				},
				"max_parallel": {
					Type:        genai.TypeInteger,
					Description: "Maximum number of agents to run in parallel. Clamped to the task count. Default: 3",
				},
				"timeout_minutes": {
					Type:        genai.TypeInteger,
					Description: "Maximum time to wait for all tasks (in minutes). Default: 10",
				},
			},
			Required: []string{"tasks"},
		},
	}
}

func (t *CoordinateTool) Validate(args map[string]any) error {
	tasks, ok := args["tasks"].([]any)
	if !ok || len(tasks) == 0 {
		return NewValidationError("tasks", "must be a non-empty array")
	}
	_, err := prepareCoordinateTasks(tasks)
	return err
}

type coordinateTaskInput struct {
	id           string
	prompt       string
	agentType    string
	priority     int
	dependencies []string
}

// prepareCoordinateTasks parses the whole dependency graph and returns a
// stable topological order. Execute cannot add tasks in the caller's raw order:
// Coordinator.AddTask generates the internal ID needed by dependents, so a
// dependency declared later in the array used to be silently omitted.
func prepareCoordinateTasks(tasks []any) ([]coordinateTaskInput, error) {
	inputs := make([]coordinateTaskInput, 0, len(tasks))
	indexByID := make(map[string]int, len(tasks))

	for i, taskAny := range tasks {
		task, ok := taskAny.(map[string]any)
		if !ok {
			return nil, NewValidationError("tasks", fmt.Sprintf("task %d must be an object", i))
		}

		id, _ := task["id"].(string)
		if id == "" {
			return nil, NewValidationError("tasks", fmt.Sprintf("task %d must have an id", i))
		}
		if _, exists := indexByID[id]; exists {
			return nil, NewValidationError("tasks", fmt.Sprintf("duplicate task id: %s", id))
		}

		prompt, _ := task["prompt"].(string)
		if prompt == "" {
			return nil, NewValidationError("tasks", fmt.Sprintf("task %s must have a prompt", id))
		}

		agentType, _ := task["agent_type"].(string)
		if agentType == "" {
			return nil, NewValidationError("tasks", fmt.Sprintf("task %s must have an agent_type", id))
		}

		priority := 5
		switch value := task["priority"].(type) {
		case float64:
			priority = int(value)
		case int:
			priority = value
		}

		dependencies, err := coordinateDependencies(task["depends_on"])
		if err != nil {
			return nil, NewValidationError("tasks", fmt.Sprintf("task %s: %v", id, err))
		}
		seenDependencies := make(map[string]struct{}, len(dependencies))
		for _, dependency := range dependencies {
			if _, duplicate := seenDependencies[dependency]; duplicate {
				return nil, NewValidationError("tasks", fmt.Sprintf("task %s has duplicate dependency: %s", id, dependency))
			}
			seenDependencies[dependency] = struct{}{}
		}

		indexByID[id] = len(inputs)
		inputs = append(inputs, coordinateTaskInput{
			id:           id,
			prompt:       prompt,
			agentType:    agentType,
			priority:     priority,
			dependencies: dependencies,
		})
	}

	indegree := make([]int, len(inputs))
	dependents := make([][]int, len(inputs))
	for taskIndex, input := range inputs {
		for _, dependency := range input.dependencies {
			dependencyIndex, exists := indexByID[dependency]
			if !exists {
				return nil, NewValidationError("tasks", fmt.Sprintf("task %s depends on unknown task: %s", input.id, dependency))
			}
			if dependency == input.id {
				return nil, NewValidationError("tasks", fmt.Sprintf("task %s cannot depend on itself", input.id))
			}
			indegree[taskIndex]++
			dependents[dependencyIndex] = append(dependents[dependencyIndex], taskIndex)
		}
	}

	ready := make([]int, 0, len(inputs))
	for i, count := range indegree {
		if count == 0 {
			ready = append(ready, i)
		}
	}
	ordered := make([]coordinateTaskInput, 0, len(inputs))
	for len(ready) > 0 {
		index := ready[0]
		ready = ready[1:]
		ordered = append(ordered, inputs[index])
		for _, dependent := range dependents[index] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
	}
	if len(ordered) != len(inputs) {
		var cyclic []string
		for i, count := range indegree {
			if count > 0 {
				cyclic = append(cyclic, inputs[i].id)
			}
		}
		sort.Strings(cyclic)
		return nil, NewValidationError("tasks", fmt.Sprintf("dependency cycle detected involving: %s", strings.Join(cyclic, ", ")))
	}
	return ordered, nil
}

func coordinateDependencies(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	var raw []any
	switch dependencies := value.(type) {
	case []any:
		raw = dependencies
	case []string:
		raw = make([]any, len(dependencies))
		for i, dependency := range dependencies {
			raw[i] = dependency
		}
	default:
		return nil, fmt.Errorf("depends_on must be an array of task IDs")
	}

	result := make([]string, 0, len(raw))
	for _, dependencyAny := range raw {
		dependency, ok := dependencyAny.(string)
		if !ok || dependency == "" {
			return nil, fmt.Errorf("depends_on entries must be non-empty task IDs")
		}
		result = append(result, dependency)
	}
	return result, nil
}

func (t *CoordinateTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if t.coordinatorFactory == nil {
		return NewErrorResult("coordinator not configured"), nil
	}

	// Parse arguments
	tasksAny, ok := args["tasks"].([]any)
	if !ok || len(tasksAny) == 0 {
		return NewErrorResult("tasks must be a non-empty array"), nil
	}
	orderedTasks, err := prepareCoordinateTasks(tasksAny)
	if err != nil {
		return NewErrorResult(err.Error()), nil
	}
	maxParallel := 3
	maxParallelProvided := false
	switch value := args["max_parallel"].(type) {
	case float64:
		maxParallel = int(value)
		maxParallelProvided = true
	case int:
		maxParallel = value
		maxParallelProvided = true
	}
	if maxParallel < 1 {
		maxParallel = 1
	}
	if maxParallel > len(orderedTasks) {
		maxParallel = len(orderedTasks)
	}
	timeoutMinutes := 10
	if tm, ok := args["timeout_minutes"].(float64); ok {
		timeoutMinutes = int(tm)
	}
	// Clamp to a sane range: a negative/zero value yields an instant (or past)
	// deadline, and a huge one overflows time.Duration(n)*time.Minute to a
	// negative wait. Bound to [1, 120] minutes.
	if timeoutMinutes < 1 {
		timeoutMinutes = 1
	} else if timeoutMinutes > 120 {
		timeoutMinutes = 120
	}

	// Create coordinator via factory
	coordAny := t.coordinatorFactory()
	if coordAny == nil {
		return NewErrorResult("failed to create coordinator"), nil
	}

	// We use interface methods to avoid import cycle
	type coordinatorInterface interface {
		AddTask(prompt string, agentType any, priority any, deps []string) string
		Start()
		WaitWithTimeout(ctx context.Context, timeout time.Duration) (map[string]any, error)
		CancelRunning() int
		GetStatus() any
		Stop()
	}

	coord, ok := coordAny.(coordinatorInterface)
	if !ok {
		// Fall back to simplified execution
		return t.executeSimple(ctx, tasksAny)
	}
	if maxParallelProvided {
		if configurer, ok := coordAny.(interface{ SetMaxParallel(int) }); ok {
			configurer.SetMaxParallel(maxParallel)
		}
	}
	// The coordinator holds a context.WithCancel(app-lifetime ctx) child plus a
	// processLoop goroutine. Without Stop(), every coordinate call would leak a
	// context node on the app context (and overhang processLoop). WaitWithTimeout
	// snapshots partial results before returning, so tearing the coordinator down
	// here only reaps the ephemeral machinery — a straggler's result is already
	// discarded by then.
	// Teardown ALSO cancels any still-running spawned agents (no-op when all
	// finished): an Esc'd or timed-out coordination must stop burning provider
	// quota — previously the agents ran on WithoutCancel and were unreachable
	// by ANY user action (the /loop CancelInFlight bug class, one layer up).
	defer func() {
		cancelled := 0
		if waiter, ok := coordAny.(interface {
			CancelRunningAndWait(context.Context) int
		}); ok {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			cancelled = waiter.CancelRunningAndWait(cleanupCtx)
			cleanupCancel()
		} else {
			cancelled = coord.CancelRunning()
		}
		if cancelled > 0 {
			logging.Info("coordinate: cancelled straggler agents on teardown", "count", cancelled)
		}
		coord.Stop()
	}()

	// Build task ID mapping (user IDs -> internal IDs)
	taskIDMap := make(map[string]string)

	// Add tasks to coordinator
	for _, task := range orderedTasks {
		// Map dependencies to internal IDs
		deps := make([]string, 0, len(task.dependencies))
		for _, dependencyID := range task.dependencies {
			deps = append(deps, taskIDMap[dependencyID])
		}

		internalID := coord.AddTask(task.prompt, task.agentType, task.priority, deps)
		taskIDMap[task.id] = internalID
	}

	// Start coordination
	coord.Start()

	// Wait for completion
	timeout := time.Duration(timeoutMinutes) * time.Minute
	results, waitErr := coord.WaitWithTimeout(ctx, timeout)
	if waitErr != nil && len(results) == 0 {
		// Nothing finished before the deadline/cancellation — there's
		// nothing to show, the bare error is the only useful information.
		return NewErrorResult(fmt.Sprintf("coordination failed: %v", waitErr)), nil
	}

	// Build result summary. A non-nil waitErr with a non-empty results map
	// means SOME tasks finished before the timeout/cancellation hit — render
	// them instead of discarding completed work, and note which tasks are
	// still incomplete below.
	var sb strings.Builder
	if waitErr != nil {
		fmt.Fprintf(&sb, "## Coordination Incomplete — %v\n\n", waitErr)
	} else {
		sb.WriteString("## Coordination Complete\n\n")
	}

	// Reverse map internal IDs to user IDs
	reverseMap := make(map[string]string)
	for userID, internalID := range taskIDMap {
		reverseMap[internalID] = userID
	}

	succeeded := 0
	failed := 0
	var policyBlock *PolicyBlock

	for internalID, resultAny := range results {
		userID := reverseMap[internalID]
		if userID == "" {
			userID = internalID
		}

		fmt.Fprintf(&sb, "### Task: %s\n", userID)

		if resultAny == nil {
			sb.WriteString("Status: No result\n\n")
			failed++
			continue
		}

		// Extract result fields via type assertion or JSON
		resultJSON, err := json.Marshal(resultAny)
		if err != nil {
			logging.Debug("failed to marshal task result", "error", err)
			resultJSON = []byte("{}")
		}
		var result struct {
			Status      string       `json:"status"`
			Output      string       `json:"output"`
			Error       string       `json:"error"`
			PolicyBlock *PolicyBlock `json:"policy_block"`
		}
		if err := json.Unmarshal(resultJSON, &result); err != nil {
			logging.Debug("failed to unmarshal task result", "error", err, "taskID", userID)
		}
		if policyBlock == nil && result.PolicyBlock != nil {
			copy := *result.PolicyBlock
			policyBlock = &copy
		}

		switch result.Status {
		case "completed":
			sb.WriteString("Status: **Completed**\n")
			succeeded++
		case "failed", "cancelled":
			reason := result.Error
			if reason == "" {
				reason = result.Status
			}
			fmt.Fprintf(&sb, "Status: **Failed** - %s\n", reason)
			failed++
		default:
			reason := result.Error
			if reason == "" {
				if result.Status == "" {
					reason = "missing result status"
				} else {
					reason = "unknown result status: " + result.Status
				}
			}
			fmt.Fprintf(&sb, "Status: **Failed** - %s\n", reason)
			failed++
		}

		if result.Output != "" {
			// Truncate long outputs
			output := result.Output
			if runes := []rune(output); len(runes) > 500 {
				output = string(runes[:500]) + "...[truncated]"
			}
			fmt.Fprintf(&sb, "Output:\n```\n%s\n```\n", output)
		}
		sb.WriteString("\n")
	}

	if waitErr != nil {
		// Tasks with no entry in `results` never got a Result before the
		// deadline/cancellation hit — still running (or never started), not
		// failed. List them separately so the model doesn't retry work that
		// may still be in flight, or conflate "didn't finish" with "failed".
		var incomplete []string
		for userID, internalID := range taskIDMap {
			if _, ok := results[internalID]; !ok {
				incomplete = append(incomplete, userID)
			}
		}
		sort.Strings(incomplete)
		if len(incomplete) > 0 {
			fmt.Fprintf(&sb, "**Did not finish (still running when the timeout/cancellation hit):** %s\n\n",
				strings.Join(incomplete, ", "))
		}
		fmt.Fprintf(&sb, "---\n**Summary:** %d succeeded, %d failed, %d did not finish out of %d tasks\n",
			succeeded, failed, len(incomplete), len(tasksAny))
		toolResult := NewSuccessResultWithData(sb.String(), map[string]any{
			"succeeded":       succeeded,
			"failed":          failed,
			"did_not_finish":  len(incomplete),
			"total":           len(tasksAny),
			"coordination_ok": false,
		})
		return withAgentPolicyBlock(toolResult, policyBlock), nil
	}

	fmt.Fprintf(&sb, "---\n**Summary:** %d succeeded, %d failed out of %d tasks\n",
		succeeded, failed, len(tasksAny))

	return withAgentPolicyBlock(NewSuccessResult(sb.String()), policyBlock), nil
}

// executeSimple is a fallback when coordinator interface isn't available.
func (t *CoordinateTool) executeSimple(ctx context.Context, tasksAny []any) (ToolResult, error) {
	var sb strings.Builder
	sb.WriteString("## Task Plan\n\n")
	sb.WriteString("Coordinator not available. Tasks to execute:\n\n")

	for i, taskAny := range tasksAny {
		task, _ := taskAny.(map[string]any)
		fmt.Fprintf(&sb, "%d. **%s** (%s)\n", i+1, task["id"], task["agent_type"])
		fmt.Fprintf(&sb, "   Prompt: %s\n", task["prompt"])
		if deps, ok := task["depends_on"].([]any); ok && len(deps) > 0 {
			fmt.Fprintf(&sb, "   Depends on: %v\n", deps)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Use the `task` tool to run these tasks individually.\n")

	return NewSuccessResult(sb.String()), nil
}
