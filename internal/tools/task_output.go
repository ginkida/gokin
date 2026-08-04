package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"gokin/internal/fileutil"
	"gokin/internal/tasks"

	"google.golang.org/genai"
)

// TaskOutputTool retrieves output from background tasks (both shell and agent tasks).
type TaskOutputTool struct {
	manager *tasks.Manager
	runner  AgentRunner // For agent tasks
}

// Keep each incremental page below the normal tool-result compaction limit.
// Advancing next_offset past bytes that the compactor removed would make the
// omitted middle permanently unreadable.
const (
	maxTaskOutputReadBytes       int64 = 24 * 1024
	DefaultTaskOutputWaitTimeout       = 2 * time.Minute
	MaxTaskOutputWaitTimeout           = 10 * time.Minute
	minTaskOutputWaitTimeout           = 100 * time.Millisecond
)

// NewTaskOutputTool creates a new task output tool.
func NewTaskOutputTool() *TaskOutputTool {
	return &TaskOutputTool{}
}

// SetManager sets the task manager for shell tasks.
func (t *TaskOutputTool) SetManager(manager *tasks.Manager) {
	t.manager = manager
}

// SetRunner sets the agent runner for agent tasks.
func (t *TaskOutputTool) SetRunner(runner AgentRunner) {
	t.runner = runner
}

func (t *TaskOutputTool) Name() string {
	return "task_output"
}

func (t *TaskOutputTool) Description() string {
	return "Get output from a background task or list all tasks"
}

func (t *TaskOutputTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"task_id": {
					Type:        genai.TypeString,
					Description: "ID of the task to get output from. If not provided, lists all tasks. Supports both shell task IDs and agent IDs (as returned by the task tool).",
				},
				"action": {
					Type:        genai.TypeString,
					Description: "Action to perform: 'get' (default), 'list', 'cancel'",
					Enum:        []string{"get", "list", "cancel"},
				},
				"block": {
					Type:        genai.TypeBoolean,
					Description: "If true, wait for task completion before returning. Default: false",
				},
				"timeout_ms": {
					Type:        genai.TypeInteger,
					Description: "Timeout in milliseconds when blocking. Default: 120000 (2 minutes). Max: 600000 (10 minutes).",
				},
				"offset": {
					Type:        genai.TypeInteger,
					Description: "Byte offset to read output from. Use this for incremental reads of long-running tasks. Returns only new output since the offset.",
				},
			},
		},
	}
}

func (t *TaskOutputTool) Validate(args map[string]any) error {
	action := GetStringDefault(args, "action", "get")

	if action == "get" || action == "cancel" {
		if _, ok := GetString(args, "task_id"); !ok {
			return NewValidationError("task_id", "task_id is required for this action")
		}
	}
	if _, present := args["offset"]; present {
		offset, ok := GetInt(args, "offset")
		if !ok {
			return NewValidationError("offset", "must be an integer")
		}
		if offset < 0 {
			return NewValidationError("offset", "must be non-negative")
		}
	}

	return nil
}

func (t *TaskOutputTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	action := GetStringDefault(args, "action", "get")
	taskID, _ := GetString(args, "task_id")
	block := GetBoolDefault(args, "block", false)
	offset := int64(GetIntDefault(args, "offset", 0))
	_, incrementalRead := args["offset"]
	timeout := taskOutputWaitTimeout(args)

	switch action {
	case "list":
		return t.listTasks()
	case "cancel":
		return t.cancelTask(ctx, taskID)
	default:
		return t.getTaskOutput(ctx, taskID, block, timeout, offset, incrementalRead)
	}
}

func taskOutputWaitTimeout(args map[string]any) time.Duration {
	timeout := DefaultTaskOutputWaitTimeout
	if timeoutMs, ok := GetInt(args, "timeout_ms"); ok {
		timeout = time.Duration(timeoutMs) * time.Millisecond
	}
	if timeout < minTaskOutputWaitTimeout {
		return minTaskOutputWaitTimeout
	}
	if timeout > MaxTaskOutputWaitTimeout {
		return MaxTaskOutputWaitTimeout
	}
	return timeout
}

func (t *TaskOutputTool) getTaskOutput(ctx context.Context, taskID string, block bool, timeout time.Duration, offset int64, incrementalRead bool) (ToolResult, error) {
	if runnerOwnsAgent(t.runner, taskID) {
		return t.getAgentOutput(ctx, taskID, block, timeout, offset, incrementalRead)
	}

	// Fall back to shell task manager
	if t.manager == nil {
		return NewErrorResult("task manager not configured"), nil
	}

	// If blocking, wait for completion
	if block {
		return t.waitForShellTask(ctx, taskID, timeout)
	}

	info, ok := t.manager.GetInfo(taskID)
	if !ok {
		return NewErrorResult(fmt.Sprintf("task not found: %s", taskID)), nil
	}
	if incrementalRead && info.OutputFile != "" {
		return t.readShellOutputFromFile(info, offset)
	}

	return t.formatShellTaskResult(info), nil
}

// runnerOwnsAgent reports whether the agent runner knows this id.
//
// This used to GUESS the id's shape: the comment claimed UUIDs and the
// predicate required a dash plus len>20, but gokin agent ids are 16 hex
// characters with no dash — so it was unreachable-true and EVERY agent id fell
// through to the shell task manager ("task not found"), making a background
// agent's output unreachable and a runaway agent unstoppable through the very
// commands the task tool tells the model to use. Ask the owner instead.
//
// ListAgents covers running agents (they are registered at spawn); GetResult
// covers a completed agent still in the result ledger. Shell ids are
// task_<unix>_<n> and cannot collide with either, so an unknown id still falls
// through to the shell manager.
func runnerOwnsAgent(runner AgentRunner, taskID string) bool {
	if runner == nil || taskID == "" {
		return false
	}
	if lister, ok := runner.(AgentLister); ok {
		for _, known := range lister.ListAgents() {
			if known == taskID {
				return true
			}
		}
	}
	_, ok := runner.GetResult(taskID)
	return ok
}

// getAgentOutput retrieves output from an agent task
func (t *TaskOutputTool) getAgentOutput(ctx context.Context, agentID string, block bool, timeout time.Duration, offset int64, incrementalRead bool) (ToolResult, error) {
	// If blocking, wait for completion with timeout
	if block {
		return t.waitForAgentTask(ctx, agentID, timeout)
	}

	// Non-blocking: just get current status
	result, ok := t.runner.GetResult(agentID)
	if !ok {
		return NewErrorResult(fmt.Sprintf("agent not found: %s", agentID)), nil
	}

	// A running agent's finalized Output field is intentionally not published
	// yet. Read its live transcript even on the first/default request so callers
	// do not need to guess that offset=0 is required just to see progress.
	// Explicit offsets retain normal paginated semantics for every status.
	if result.OutputFile != "" &&
		(incrementalRead || (!result.Completed && result.Output == "")) {
		return t.readAgentOutputFromFile(result, offset)
	}

	return t.formatAgentResult(result), nil
}

// waitForAgentTask waits for an agent to complete with timeout
func (t *TaskOutputTool) waitForAgentTask(ctx context.Context, agentID string, timeout time.Duration) (ToolResult, error) {
	// Create timeout context
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Poll for completion
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Timeout or cancelled
			result, ok := t.runner.GetResult(agentID)
			if !ok {
				return NewErrorResult(fmt.Sprintf("agent not found: %s", agentID)), nil
			}
			// Return partial result with timeout indicator
			partial := t.formatAgentResult(result)
			if result.Output == "" && result.OutputFile != "" {
				partial, _ = t.readAgentOutputFromFile(result, 0)
			}
			var builder strings.Builder
			builder.WriteString("**Timeout waiting for agent completion**\n\n")
			builder.WriteString(partial.Content)
			data := map[string]any{
				"agent_id":  agentID,
				"status":    string(result.Status),
				"completed": result.Completed,
				"timeout":   true,
			}
			if partialData, ok := partial.Data.(map[string]any); ok {
				for key, value := range partialData {
					data[key] = value
				}
				data["timeout"] = true
			}
			return NewSuccessResultWithData(builder.String(), data), nil

		case <-ticker.C:
			result, ok := t.runner.GetResult(agentID)
			if !ok {
				return NewErrorResult(fmt.Sprintf("agent not found: %s", agentID)), nil
			}
			if result.Completed {
				return t.formatAgentResult(result), nil
			}
		}
	}
}

// waitForShellTask waits for a shell task to complete with timeout
func (t *TaskOutputTool) waitForShellTask(ctx context.Context, taskID string, timeout time.Duration) (ToolResult, error) {
	// Create timeout context
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Poll for completion
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Timeout or cancelled
			info, ok := t.manager.GetInfo(taskID)
			if !ok {
				return NewErrorResult(fmt.Sprintf("task not found: %s", taskID)), nil
			}
			// Return partial result with timeout indicator
			var builder strings.Builder
			builder.WriteString("**Timeout waiting for task completion**\n\n")
			builder.WriteString(t.formatShellTaskResult(info).Content)
			return NewSuccessResultWithData(builder.String(), map[string]any{
				"task_id": taskID,
				"status":  info.Status,
				"running": info.Status == "running",
				"timeout": true,
			}), nil

		case <-ticker.C:
			info, ok := t.manager.GetInfo(taskID)
			if !ok {
				return NewErrorResult(fmt.Sprintf("task not found: %s", taskID)), nil
			}
			if info.Status != "running" {
				return t.formatShellTaskResult(info), nil
			}
		}
	}
}

// formatShellTaskResult formats a shell task result
func (t *TaskOutputTool) formatShellTaskResult(info tasks.Info) ToolResult {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Task: %s\n", info.ID)
	fmt.Fprintf(&builder, "Status: %s\n", info.Status)
	fmt.Fprintf(&builder, "Command: %s\n", info.Command)
	fmt.Fprintf(&builder, "Duration: %s\n", info.Duration)

	if info.Error != "" {
		fmt.Fprintf(&builder, "Error: %s\n", info.Error)
	}
	if info.ExitCode != 0 {
		fmt.Fprintf(&builder, "Exit Code: %d\n", info.ExitCode)
	}
	if info.Summary != "" {
		fmt.Fprintf(&builder, "Verification: %s\n", info.Summary)
	}

	if info.Output != "" {
		builder.WriteString("\nOutput:\n")
		builder.WriteString(info.Output)
	}

	data := map[string]any{
		"task_id":   info.ID,
		"status":    info.Status,
		"command":   info.Command,
		"summary":   info.Summary,
		"output":    info.Output,
		"error":     info.Error,
		"exit_code": info.ExitCode,
		"running":   info.Status == "running",
	}
	if info.OutputFile != "" {
		data["output_file"] = info.OutputFile
		if fi, err := os.Stat(info.OutputFile); err == nil {
			data["total_bytes"] = fi.Size()
		}
	}
	return NewSuccessResultWithData(builder.String(), data)
}

func (t *TaskOutputTool) readShellOutputFromFile(info tasks.Info, offset int64) (ToolResult, error) {
	f, err := os.Open(info.OutputFile)
	if err != nil {
		return t.formatShellTaskResult(info), nil
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return t.formatShellTaskResult(info), nil
	}
	if offset >= stat.Size() {
		return NewSuccessResultWithData("No new output since last read.", map[string]any{
			"task_id":     info.ID,
			"status":      info.Status,
			"running":     info.Status == "running",
			"offset":      offset,
			"next_offset": offset,
			"total_bytes": stat.Size(),
			"summary":     info.Summary,
		}), nil
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return t.formatShellTaskResult(info), nil
	}
	readSize := stat.Size() - offset
	if readSize > maxTaskOutputReadBytes {
		readSize = maxTaskOutputReadBytes
	}
	buf := make([]byte, readSize)
	n, _ := f.Read(buf)
	nextOffset := offset + int64(n)

	var builder strings.Builder
	fmt.Fprintf(&builder, "Task: %s (incremental read)\n", info.ID)
	fmt.Fprintf(&builder, "Status: %s\n", info.Status)
	fmt.Fprintf(&builder, "Bytes: %d-%d of %d\n", offset, nextOffset, stat.Size())
	if info.Summary != "" {
		fmt.Fprintf(&builder, "Verification: %s\n", info.Summary)
	}
	builder.WriteString("\nNew output:\n")
	builder.Write(buf[:n])

	return NewSuccessResultWithData(builder.String(), map[string]any{
		"task_id":     info.ID,
		"status":      info.Status,
		"running":     info.Status == "running",
		"offset":      offset,
		"next_offset": nextOffset,
		"total_bytes": stat.Size(),
		"summary":     info.Summary,
		"output":      string(buf[:n]),
		"output_file": info.OutputFile,
	}), nil
}

// readAgentOutputFromFile reads agent output from file starting at offset.
// This enables incremental reads for long-running agents without loading
// the entire output into memory.
func (t *TaskOutputTool) readAgentOutputFromFile(result AgentResult, offset int64) (ToolResult, error) {
	f, err := fileutil.OpenPrivateRead(result.OutputFile)
	if err != nil {
		// Fall back to in-memory output
		return t.formatAgentResult(result), nil
	}
	defer f.Close()

	// Get file size
	stat, err := f.Stat()
	if err != nil {
		return t.formatAgentResult(result), nil
	}

	if offset >= stat.Size() {
		// No new output
		toolResult := NewSuccessResultWithData("No new output since last read.", map[string]any{
			"agent_id":    result.AgentID,
			"status":      result.Status,
			"completed":   result.Completed,
			"offset":      offset,
			"next_offset": offset,
			"total_bytes": stat.Size(),
		})
		return withAgentPolicyBlock(toolResult, result.PolicyBlock), nil
	}

	if _, err := f.Seek(offset, 0); err != nil {
		return t.formatAgentResult(result), nil
	}

	readSize := stat.Size() - offset
	if readSize > maxTaskOutputReadBytes {
		readSize = maxTaskOutputReadBytes
	}
	buf := make([]byte, readSize)
	n, _ := f.Read(buf)
	newOutput := string(buf[:n])
	nextOffset := offset + int64(n)

	var builder strings.Builder
	fmt.Fprintf(&builder, "Agent: %s (incremental read)\n", result.AgentID)
	fmt.Fprintf(&builder, "Status: %s\n", result.Status)
	fmt.Fprintf(&builder, "Bytes: %d-%d of %d\n", offset, nextOffset, stat.Size())
	builder.WriteString("\nNew output:\n")
	builder.WriteString(newOutput)

	toolResult := NewSuccessResultWithData(builder.String(), map[string]any{
		"agent_id":    result.AgentID,
		"status":      result.Status,
		"completed":   result.Completed,
		"offset":      offset,
		"next_offset": nextOffset,
		"total_bytes": stat.Size(),
		"output":      newOutput,
	})
	return withAgentPolicyBlock(toolResult, result.PolicyBlock), nil
}

// formatAgentResult formats an agent result
func (t *TaskOutputTool) formatAgentResult(result AgentResult) ToolResult {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Agent: %s\n", result.AgentID)
	fmt.Fprintf(&builder, "Type: %s\n", result.Type)
	fmt.Fprintf(&builder, "Status: %s\n", result.Status)
	fmt.Fprintf(&builder, "Duration: %s\n", result.Duration)

	if result.Error != "" {
		fmt.Fprintf(&builder, "Error: %s\n", result.Error)
	}

	if result.Output != "" {
		builder.WriteString("\nOutput:\n")
		builder.WriteString(result.Output)
	}

	data := map[string]any{
		"agent_id":  result.AgentID,
		"type":      result.Type,
		"status":    result.Status,
		"output":    result.Output,
		"error":     result.Error,
		"duration":  result.Duration.String(),
		"completed": result.Completed,
		"running":   result.Status == "running",
	}

	// Include output file info for incremental reading
	if result.OutputFile != "" {
		data["output_file"] = result.OutputFile
		if fi, err := os.Stat(result.OutputFile); err == nil {
			data["total_bytes"] = fi.Size()
		}
	}

	return withAgentPolicyBlock(NewSuccessResultWithData(builder.String(), data), result.PolicyBlock)
}

func (t *TaskOutputTool) listTasks() (ToolResult, error) {
	var builder strings.Builder
	totalCount := 0

	// List shell tasks
	var shellTasks []tasks.Info
	if t.manager != nil {
		shellTasks = t.manager.List()
	}

	// List agent tasks
	var agentTasks []AgentResult
	if t.runner != nil {
		// Get all agent IDs and their results
		if lister, ok := t.runner.(AgentLister); ok {
			for _, agentID := range lister.ListAgents() {
				if result, ok := t.runner.GetResult(agentID); ok {
					agentTasks = append(agentTasks, result)
				}
			}
		}
	}

	totalCount = len(shellTasks) + len(agentTasks)

	if totalCount == 0 {
		return NewSuccessResult("No background tasks"), nil
	}

	fmt.Fprintf(&builder, "Background Tasks (%d total):\n\n", totalCount)

	// Shell tasks
	if len(shellTasks) > 0 {
		builder.WriteString("**Shell Tasks:**\n")
		for _, info := range shellTasks {
			status := info.Status
			if status == "completed" {
				status = "done"
			}

			// Truncate command if too long
			cmd := info.Command
			if runes := []rune(cmd); len(runes) > 50 {
				cmd = string(runes[:47]) + "..."
			}

			fmt.Fprintf(&builder, "  [%s] %s - %s (%s)\n", status, info.ID, cmd, info.Duration)
		}
		builder.WriteString("\n")
	}

	// Agent tasks
	if len(agentTasks) > 0 {
		builder.WriteString("**Agent Tasks:**\n")
		for _, result := range agentTasks {
			status := string(result.Status)
			if status == "completed" {
				status = "done"
			}

			fmt.Fprintf(&builder, "  [%s] %s - %s (%s)\n", status, result.AgentID, result.Type, result.Duration.Round(time.Millisecond))
		}
	}

	// JSON data for structured access
	shellData, err := json.Marshal(shellTasks)
	if err != nil {
		shellData = []byte("[]")
	}
	agentData, err := json.Marshal(agentTasks)
	if err != nil {
		agentData = []byte("[]")
	}

	return NewSuccessResultWithData(builder.String(), map[string]any{
		"shell_tasks": string(shellData),
		"agent_tasks": string(agentData),
		"count":       totalCount,
	}), nil
}

func (t *TaskOutputTool) cancelTask(ctx context.Context, taskID string) (ToolResult, error) {
	// Check if this is an agent task
	if runnerOwnsAgent(t.runner, taskID) {
		if canceller, ok := t.runner.(AgentCanceller); ok {
			if err := canceller.Cancel(taskID); err != nil {
				return NewErrorResult(err.Error()), nil
			}
			return NewSuccessResult(fmt.Sprintf("Agent %s cancelled", taskID)), nil
		}
		return NewErrorResult("agent cancellation not supported"), nil
	}

	// Fall back to shell task manager
	if t.manager == nil {
		return NewErrorResult("task manager not configured"), nil
	}

	if err := t.manager.Cancel(taskID); err != nil {
		return NewErrorResult(err.Error()), nil
	}

	return NewSuccessResult(fmt.Sprintf("Task %s cancelled", taskID)), nil
}

// AgentLister is an interface for listing agents.
type AgentLister interface {
	ListAgents() []string
}

// AgentCanceller is an interface for cancelling agents.
type AgentCanceller interface {
	Cancel(agentID string) error
}
