package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"
)

// AgentProgress represents progress information from an agent.
type AgentProgress struct {
	AgentID       string
	CurrentStep   int
	TotalSteps    int
	CurrentAction string
	Elapsed       time.Duration
	ToolsUsed     []string
}

// AgentRunner interface for spawning and managing agents.
// This is implemented by agent.Runner to avoid import cycles.
type AgentRunner interface {
	Spawn(ctx context.Context, agentType string, prompt string, maxTurns int, model string) (string, error)
	SpawnAsync(ctx context.Context, agentType string, prompt string, maxTurns int, model string) string
	SpawnAsyncWithStreaming(ctx context.Context, agentType string, prompt string, maxTurns int, model string, onText func(string), onProgress func(id string, progress *AgentProgress)) string
	Resume(ctx context.Context, agentID string, prompt string) (string, error)
	ResumeAsync(ctx context.Context, agentID string, prompt string) (string, error)
	GetResult(agentID string) (AgentResult, bool)
}

// AgentResult represents the result from an agent execution.
type AgentResult struct {
	AgentID       string
	Type          string
	Model         string
	Provider      string
	EstimatedCost float64
	CostTracked   bool
	Status        string
	Output        string
	Error         string
	Duration      time.Duration
	Completed     bool
	OutputFile    string // Path to file-backed output stream (for incremental reads)
	PolicyBlock   *PolicyBlock
}

// AgentTypeDefinition is the model-facing portion of an agent type. Tool
// allowlists and prompts intentionally stay inside the runner/agent packages;
// the task schema only needs a stable name and a concise selection hint.
type AgentTypeDefinition struct {
	Name        string
	Description string
}

// AgentTypeProvider supplies one coherent catalog snapshot. Implementations
// may be updated at runtime, so callers must not retain mutable internal data.
type AgentTypeProvider interface {
	SnapshotAgentTypes() []AgentTypeDefinition
}

// TaskTool spawns subagents to handle complex tasks.
type TaskTool struct {
	mu                       sync.RWMutex
	runner                   AgentRunner
	agentTypes               AgentTypeProvider
	toolCapabilityCeiling    []string
	toolCapabilityRestricted bool
	backgroundAllowed        bool
}

// NewTaskTool creates a new TaskTool instance.
func NewTaskTool() *TaskTool {
	return &TaskTool{backgroundAllowed: true}
}

// SetRunner sets the agent runner for spawning subagents.
func (t *TaskTool) SetRunner(runner AgentRunner) {
	t.mu.Lock()
	t.runner = runner
	t.mu.Unlock()
}

// SetAgentTypeProvider exposes built-in and loaded custom agent types to the
// model. The provider is shared by clones so runtime registrations appear on
// the next declaration/validation snapshot.
func (t *TaskTool) SetAgentTypeProvider(provider AgentTypeProvider) {
	t.mu.Lock()
	t.agentTypes = provider
	t.mu.Unlock()
}

// SetToolCapabilityCeiling binds a cloned task tool to its parent agent's
// actual toolkit. A child receives the intersection of this ceiling and its
// own type allowlist; delegation can never recover authority filtered out of
// the parent. Passing nil is reserved for the unrestricted foreground tool.
func (t *TaskTool) SetToolCapabilityCeiling(names []string) {
	t.mu.Lock()
	t.toolCapabilityRestricted = names != nil
	t.toolCapabilityCeiling = normalizeToolNames(names)
	t.mu.Unlock()
}

// SetBackgroundAllowed controls whether the model may detach sub-agents from
// the current request. Headless callers disable this to keep terminal outcome
// and process lifetime deterministic.
func (t *TaskTool) SetBackgroundAllowed(allowed bool) {
	t.mu.Lock()
	t.backgroundAllowed = allowed
	t.mu.Unlock()
}

func (t *TaskTool) Name() string {
	return "task"
}

func (t *TaskTool) Description() string {
	return taskToolDescription(t.agentTypeSnapshot())
}

func (t *TaskTool) Declaration() *genai.FunctionDeclaration {
	state := t.snapshot()
	agentTypes := agentTypeSnapshot(state.agentTypes)
	typeNames := agentTypeNames(agentTypes)
	declaration := &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: taskToolDescription(agentTypes),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"prompt": {
					Type:        genai.TypeString,
					Description: "The task for the subagent to perform",
				},
				"subagent_type": {
					Type:        genai.TypeString,
					Description: "Registered agent type to spawn. See the task tool description for each type's specialization.",
					Enum:        typeNames,
				},
				"description": {
					Type:        genai.TypeString,
					Description: "A short description of the task (3-5 words)",
				},
				"run_in_background": {
					Type:        genai.TypeBoolean,
					Description: "If true, run the agent in the background and return immediately",
				},
				"max_turns": {
					Type:        genai.TypeInteger,
					Description: "Maximum number of agentic turns (API round-trips) before stopping. Default: 30.",
				},
				"model": {
					Type:        genai.TypeString,
					Description: "Model to use: 'flash' (fast), 'pro' (balanced). If not specified, inherits from parent.",
					Enum:        []string{"flash", "pro"},
				},
				"resume": {
					Type:        genai.TypeString,
					Description: "Agent ID to resume from previous execution. If provided, continues from saved state.",
				},
				"thoroughness": {
					Type:        genai.TypeString,
					Description: "Depth of agent investigation: 'quick' (fast, minimal analysis), 'normal' (default), 'thorough' (comprehensive, deep analysis). Applies to all agent types.",
					Enum:        []string{"quick", "normal", "thorough"},
				},
				"output_style": {
					Type:        genai.TypeString,
					Description: "Response format style: 'concise' (bullet points, minimal), 'normal' (default), 'detailed' (verbose with full explanations). Applies to all agent types.",
					Enum:        []string{"concise", "normal", "detailed"},
				},
			},
			Required: []string{"prompt"},
		},
	}
	if !state.backgroundAllowed {
		delete(declaration.Parameters.Properties, "run_in_background")
	}
	return declaration
}

func (t *TaskTool) Validate(args map[string]any) error {
	state := t.snapshot()
	return validateTaskArgs(args, agentTypeSnapshot(state.agentTypes), state.backgroundAllowed)
}

func validateTaskArgs(args map[string]any, agentTypes []AgentTypeDefinition, backgroundAllowed bool) error {
	prompt, ok := GetString(args, "prompt")
	if !ok || prompt == "" {
		return NewValidationError("prompt", "is required")
	}
	if runInBackground, _ := GetBool(args, "run_in_background"); runInBackground && !backgroundAllowed {
		return NewValidationError("run_in_background", "is not available in this execution mode")
	}

	// If resuming, we don't need subagent_type
	resume, _ := GetString(args, "resume")
	if resume != "" {
		return nil
	}

	agentType, ok := GetString(args, "subagent_type")
	if !ok || agentType == "" {
		return NewValidationError("subagent_type", "is required when not resuming")
	}

	for _, definition := range agentTypes {
		if agentType == definition.Name {
			return nil
		}
	}

	return NewValidationError("subagent_type", fmt.Sprintf(
		"unknown agent type %q; available types: %s",
		agentType, strings.Join(agentTypeNames(agentTypes), ", ")))
}

func (t *TaskTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	state := t.snapshot()
	agentTypes := agentTypeSnapshot(state.agentTypes)
	if err := validateTaskArgs(args, agentTypes, state.backgroundAllowed); err != nil {
		return NewErrorResult("validation error: " + err.Error()), nil
	}
	if state.runner == nil {
		return NewErrorResult("task runner not initialized"), nil
	}
	if state.toolCapabilityRestricted {
		ctx = ContextWithAgentToolCapabilityCeiling(ctx, state.toolCapabilityCeiling)
	}

	prompt, _ := GetString(args, "prompt")
	agentType, _ := GetString(args, "subagent_type")
	description := GetStringDefault(args, "description", "")
	runInBackground := GetBoolDefault(args, "run_in_background", false)
	maxTurns := GetIntDefault(args, "max_turns", 30)
	model := GetStringDefault(args, "model", "")
	resume := GetStringDefault(args, "resume", "")
	thoroughnessStr := GetStringDefault(args, "thoroughness", "")
	outputStyleStr := GetStringDefault(args, "output_style", "")

	// Inject thoroughness into context
	if thoroughnessStr != "" {
		ctx = WithThoroughness(ctx, ParseThoroughness(thoroughnessStr))
	}

	// Inject output style into context
	if outputStyleStr != "" {
		ctx = WithOutputStyle(ctx, ParseOutputStyle(outputStyleStr))
	}

	// If resuming an existing agent
	if resume != "" {
		if runInBackground {
			return t.executeResumeBackground(ctx, state.runner, resume, prompt, description)
		}
		return t.executeResumeForeground(ctx, state.runner, resume, prompt, description)
	}

	if runInBackground {
		return t.executeBackground(ctx, state.runner, agentType, prompt, description, maxTurns, model)
	}

	return t.executeForeground(ctx, state.runner, agentType, prompt, description, maxTurns, model)
}

type taskToolState struct {
	runner                   AgentRunner
	agentTypes               AgentTypeProvider
	toolCapabilityCeiling    []string
	toolCapabilityRestricted bool
	backgroundAllowed        bool
}

func (t *TaskTool) snapshot() taskToolState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return taskToolState{
		runner:                   t.runner,
		agentTypes:               t.agentTypes,
		toolCapabilityCeiling:    cloneTaskToolNames(t.toolCapabilityCeiling),
		toolCapabilityRestricted: t.toolCapabilityRestricted,
		backgroundAllowed:        t.backgroundAllowed,
	}
}

func cloneTaskToolNames(names []string) []string {
	if names == nil {
		return nil
	}
	cloned := make([]string, len(names))
	copy(cloned, names)
	return cloned
}

func (t *TaskTool) clone() *TaskTool {
	state := t.snapshot()
	clone := NewTaskTool()
	clone.runner = state.runner
	clone.agentTypes = state.agentTypes
	clone.toolCapabilityCeiling = state.toolCapabilityCeiling
	clone.toolCapabilityRestricted = state.toolCapabilityRestricted
	clone.backgroundAllowed = state.backgroundAllowed
	return clone
}

func (t *TaskTool) agentTypeSnapshot() []AgentTypeDefinition {
	t.mu.RLock()
	provider := t.agentTypes
	t.mu.RUnlock()
	return agentTypeSnapshot(provider)
}

func agentTypeSnapshot(provider AgentTypeProvider) []AgentTypeDefinition {
	byName := make(map[string]AgentTypeDefinition)
	for _, definition := range builtinAgentTypeDefinitions() {
		byName[definition.Name] = definition
	}
	if provider != nil {
		for _, definition := range provider.SnapshotAgentTypes() {
			definition.Name = strings.TrimSpace(definition.Name)
			definition.Description = strings.Join(strings.Fields(definition.Description), " ")
			if definition.Name == "" {
				continue
			}
			// A provider cannot redefine the model-facing meaning of a built-in.
			if _, builtin := byName[definition.Name]; builtin {
				continue
			}
			byName[definition.Name] = definition
		}
	}

	types := make([]AgentTypeDefinition, 0, len(byName))
	for _, definition := range byName {
		types = append(types, definition)
	}
	sort.Slice(types, func(i, j int) bool { return types[i].Name < types[j].Name })
	return types
}

func builtinAgentTypeDefinitions() []AgentTypeDefinition {
	return []AgentTypeDefinition{
		{Name: "explore", Description: "Explore and analyze codebases with read-only search tools"},
		{Name: "bash", Description: "Execute focused shell commands"},
		{Name: "general", Description: "Handle general-purpose multi-step work"},
		{Name: "plan", Description: "Design implementation strategies with read-only planning tools"},
		{Name: "claude-code-guide", Description: "Answer CLI documentation and usage questions"},
	}
}

func agentTypeNames(agentTypes []AgentTypeDefinition) []string {
	names := make([]string, len(agentTypes))
	for i, definition := range agentTypes {
		names[i] = definition.Name
	}
	return names
}

func taskToolDescription(agentTypes []AgentTypeDefinition) string {
	var description strings.Builder
	description.WriteString("Spawns a specialized subagent to handle complex tasks autonomously.\nAgent types:\n")
	for _, definition := range agentTypes {
		fmt.Fprintf(&description, "- %s: %s\n", definition.Name, definition.Description)
	}
	description.WriteString("\nUse for multi-step tasks, parallel exploration, or isolated command execution.")
	return description.String()
}

// maxSubAgentReportChars bounds how much of a sub-agent's transcript is folded
// back into the PARENT agent's context. An agent's conclusion is at the END of
// its transcript (like Claude Code's tail), so we keep the tail and point to the
// file-backed full output. Without this, a deep sub-agent (dozens of turns)
// dumps its entire narration into the parent window — the opposite of the
// "delegation is context-cheap" contract that makes sub-agents worth spawning.
const maxSubAgentReportChars = 6000

// writeSubAgentOutput appends a sub-agent's output to b, capped (rune-safe) to
// the final maxSubAgentReportChars with a truncation marker + OutputFile pointer
// when it overflows. The full transcript stays in OutputFile for task_output.
func writeSubAgentOutput(b *strings.Builder, out, outputFile string) {
	if out == "" {
		return
	}
	b.WriteString("### Output:\n")
	runes := []rune(out)
	if len(runes) <= maxSubAgentReportChars {
		b.WriteString(out)
		return
	}
	fmt.Fprintf(b, "[sub-agent transcript truncated to the final %d chars — the conclusion is below", maxSubAgentReportChars)
	if outputFile != "" {
		fmt.Fprintf(b, "; full output in %s (read it or use task_output for the rest)", outputFile)
	}
	b.WriteString("]\n…\n")
	b.WriteString(string(runes[len(runes)-maxSubAgentReportChars:]))
}

// errOrUnknown renders an error string, or a placeholder when nil.
func errOrUnknown(err error) string {
	if err != nil {
		return err.Error()
	}
	return "unknown error"
}

// failureReason picks the failure reason from the stored result.Error, falling
// back to the Spawn/Resume err — the agent sets result.Error only on its err!=nil
// path, so both carry the same reason on a real failure; either may be the one
// populated depending on where the failure originated.
func failureReason(resultErr string, spawnErr error) string {
	if resultErr != "" {
		return resultErr
	}
	if spawnErr != nil {
		return spawnErr.Error()
	}
	return ""
}

func (t *TaskTool) executeForeground(ctx context.Context, runner AgentRunner, agentType, prompt, description string, maxTurns int, model string) (ToolResult, error) {
	agentID, err := runner.Spawn(ctx, agentType, prompt, maxTurns, model)
	// Spawn is synchronous and stores the agent's PARTIAL result (output + reason)
	// before returning a non-nil err — agent.Run sets result.Error only on the
	// err!=nil path, so the real failure shape is err!=nil. The old early-return
	// here ran BEFORE GetResult and discarded the sub-agent's built code / analysis
	// / half-finished edits, handing the parent a bare "Agent failed" with nothing
	// to continue from. Only a never-started spawn (empty agentID) is a true
	// failure with nothing to preserve.
	if agentID == "" {
		return NewErrorResult(fmt.Sprintf("Agent failed to start: %s", errOrUnknown(err))), nil
	}

	result, ok := runner.GetResult(agentID)
	if !ok {
		return NewErrorResult(fmt.Sprintf("Agent failed: %s", errOrUnknown(err))), nil
	}

	var output strings.Builder

	// Header
	if description != "" {
		fmt.Fprintf(&output, "## Task: %s\n\n", description)
	}
	fmt.Fprintf(&output, "Agent ID: %s\n", result.AgentID)
	fmt.Fprintf(&output, "Type: %s\n", result.Type)
	fmt.Fprintf(&output, "Status: %s\n", result.Status)
	fmt.Fprintf(&output, "Duration: %s\n\n", result.Duration.Round(time.Millisecond))

	// Result — surface the failure reason from result.Error OR the Spawn err.
	if reason := failureReason(result.Error, err); reason != "" {
		fmt.Fprintf(&output, "**Error:** %s\n\n", reason)
	}

	writeSubAgentOutput(&output, result.Output, result.OutputFile)

	toolResult := NewSuccessResultWithData(output.String(), map[string]any{
		"agent_id": result.AgentID,
		"type":     result.Type,
		"status":   result.Status,
		"duration": result.Duration.String(),
	})
	return withAgentPolicyBlock(toolResult, result.PolicyBlock), nil
}

func (t *TaskTool) executeBackground(ctx context.Context, runner AgentRunner, agentType, prompt, description string, maxTurns int, model string) (ToolResult, error) {
	// Check for streaming callback in context
	onText := GetStreamingCallback(ctx)
	onProgress := GetProgressCallback(ctx)

	var agentID string
	if onText != nil || onProgress != nil {
		// Use streaming-enabled spawn
		var progressAdapter func(id string, progress *AgentProgress)
		if onProgress != nil {
			progressAdapter = func(id string, progress *AgentProgress) {
				if progress != nil {
					pct := float64(progress.CurrentStep) / float64(max(progress.TotalSteps, 1))
					onProgress(pct, progress.CurrentAction)
				}
			}
		}
		agentID = runner.SpawnAsyncWithStreaming(ctx, agentType, prompt, maxTurns, model, onText, progressAdapter)
	} else {
		// Use standard async spawn
		agentID = runner.SpawnAsync(ctx, agentType, prompt, maxTurns, model)
	}

	var output strings.Builder
	output.WriteString("Agent spawned in background.\n\n")
	fmt.Fprintf(&output, "Agent ID: %s\n", agentID)
	fmt.Fprintf(&output, "Type: %s\n", agentType)
	if description != "" {
		fmt.Fprintf(&output, "Task: %s\n", description)
	}
	if onText != nil {
		output.WriteString("Streaming: enabled\n")
	}
	output.WriteString("\nUse task_output with this agent_id to check status and results.")

	return NewSuccessResultWithData(output.String(), map[string]any{
		"agent_id":   agentID,
		"type":       agentType,
		"background": true,
		"streaming":  onText != nil,
	}), nil
}

func (t *TaskTool) executeResumeForeground(ctx context.Context, runner AgentRunner, agentID, prompt, description string) (ToolResult, error) {
	resumedID, err := runner.Resume(ctx, agentID, prompt)
	// Resume stores the partial result before returning a non-nil err (same shape
	// as Spawn); only a true load/restore failure returns an empty resumedID. When
	// the agent actually resumed, surface its partial work instead of dropping it.
	if resumedID == "" {
		return NewErrorResult(fmt.Sprintf("Failed to resume agent: %s", errOrUnknown(err))), nil
	}

	result, ok := runner.GetResult(resumedID)
	if !ok {
		return NewErrorResult(fmt.Sprintf("Failed to resume agent: %s", errOrUnknown(err))), nil
	}

	var output strings.Builder

	// Header
	output.WriteString("## Agent Resumed\n\n")
	if description != "" {
		fmt.Fprintf(&output, "Task: %s\n", description)
	}
	fmt.Fprintf(&output, "Agent ID: %s\n", result.AgentID)
	fmt.Fprintf(&output, "Type: %s\n", result.Type)
	fmt.Fprintf(&output, "Status: %s\n", result.Status)
	fmt.Fprintf(&output, "Duration: %s\n\n", result.Duration.Round(time.Millisecond))

	// Result — surface the failure reason from result.Error OR the Resume err.
	if reason := failureReason(result.Error, err); reason != "" {
		fmt.Fprintf(&output, "**Error:** %s\n\n", reason)
	}

	writeSubAgentOutput(&output, result.Output, result.OutputFile)

	toolResult := NewSuccessResultWithData(output.String(), map[string]any{
		"agent_id": result.AgentID,
		"type":     result.Type,
		"status":   result.Status,
		"duration": result.Duration.String(),
		"resumed":  true,
	})
	return withAgentPolicyBlock(toolResult, result.PolicyBlock), nil
}

func withAgentPolicyBlock(result ToolResult, block *PolicyBlock) ToolResult {
	if block == nil {
		return result
	}
	return WithPolicyBlock(result, block.Kind, block.Reason)
}

func (t *TaskTool) executeResumeBackground(ctx context.Context, runner AgentRunner, agentID, prompt, description string) (ToolResult, error) {
	resumedID, err := runner.ResumeAsync(ctx, agentID, prompt)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("Failed to resume agent: %s", err)), nil
	}

	var output strings.Builder
	output.WriteString("Agent resumed in background.\n\n")
	fmt.Fprintf(&output, "Agent ID: %s\n", resumedID)
	if description != "" {
		fmt.Fprintf(&output, "Task: %s\n", description)
	}
	output.WriteString("\nUse task_output with this agent_id to check status and results.")

	return NewSuccessResultWithData(output.String(), map[string]any{
		"agent_id":   resumedID,
		"background": true,
		"resumed":    true,
	}), nil
}
