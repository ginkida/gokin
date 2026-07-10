package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gokin/internal/agent"
)

// AgentTaskRunner is the narrow Runner surface /tasks needs. May be nil when
// the agent subsystem isn't wired (unit tests, early lifecycle) — the command
// nil-checks and reports unavailability instead of crashing. Interface (not
// *agent.Runner) so command tests can fake it, same pattern as LoopManager.
type AgentTaskRunner interface {
	ListTaskSummaries() []agent.TaskSummary
	GetResult(agentID string) (*agent.AgentResult, bool)
	// Cancel stops a running background agent (its run context is killed;
	// partial work is preserved by the agent layer). /tasks stop <id> is the
	// USER-facing stop — previously only the MODEL could stop an agent
	// (task_stop tool), so a user watching a runaway background task had no
	// way to kill it short of quitting gokin.
	Cancel(agentID string) error
}

// TasksCommand lists background agent tasks and shows their results —
// the user-facing window into "spawned an agent, kept working" flows
// (task tool with run_in_background, coordinate, sub-agents).
type TasksCommand struct{}

func (c *TasksCommand) Name() string        { return "tasks" }
func (c *TasksCommand) Description() string { return "List background agent tasks and their results" }
func (c *TasksCommand) Usage() string       { return "/tasks [id] | /tasks stop <id>" }
func (c *TasksCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategorySession,
		Icon:     "tasks",
		Priority: 65,
		HasArgs:  true,
		ArgHint:  "[id]",
	}
}

const (
	// tasksListLimit caps the list view — the runner's Cleanup already evicts
	// old agents, this just keeps one screen readable.
	tasksListLimit = 20
	// tasksOutputTailRunes caps the detail view's output. Tail, not head:
	// an agent's conclusion is at the END of its output.
	tasksOutputTailRunes = 3000
)

func (c *TasksCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	runner := app.GetAgentTaskRunner()
	if runner == nil {
		return "Background agents are unavailable in this build.", nil
	}

	if len(args) > 0 && strings.TrimSpace(args[0]) == "stop" {
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			return "Usage: /tasks stop <id>  (ids from /tasks)", nil
		}
		return c.stopTask(runner, strings.TrimSpace(args[1]))
	}
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		return c.renderDetail(runner, strings.TrimSpace(args[0]))
	}
	return c.renderList(runner), nil
}

// stopTask cancels a background agent by id (exact or unique prefix — same
// resolution as the detail view).
func (c *TasksCommand) stopTask(runner AgentTaskRunner, rawID string) (string, error) {
	match, err := resolveTaskID(runner.ListTaskSummaries(), rawID)
	if err != nil {
		return err.Error(), nil
	}
	if err := runner.Cancel(match.ID); err != nil {
		return fmt.Sprintf("Failed to stop %s: %v", match.ID, err), nil
	}
	return fmt.Sprintf("Stopped background task %s. Partial output stays available via /tasks %s.", match.ID, match.ID), nil
}

func (c *TasksCommand) renderList(runner AgentTaskRunner) string {
	summaries := runner.ListTaskSummaries()
	if len(summaries) == 0 {
		return "No background tasks in this session.\n" +
			"Start one with the task tool (run_in_background), /loop, or ask the model to delegate work to a sub-agent."
	}

	var sb strings.Builder
	sb.WriteString("Background tasks\n\n")

	shown := summaries
	if len(shown) > tasksListLimit {
		shown = shown[:tasksListLimit]
	}
	for _, s := range shown {
		task := s.Task
		if task == "" {
			task = "(no task recorded)"
		}
		fmt.Fprintf(&sb, "%s %s · %s · %s\n", taskStatusIcon(s.Status), s.ID, s.Type, formatTaskDuration(s.Duration))
		fmt.Fprintf(&sb, "   %s\n", task)
		if s.Error != "" {
			fmt.Fprintf(&sb, "   error: %s\n", firstLineTrimmed(s.Error, 120))
		}
	}
	if len(summaries) > tasksListLimit {
		fmt.Fprintf(&sb, "\n… and %d older task(s)\n", len(summaries)-tasksListLimit)
	}
	sb.WriteString("\nUse /tasks <id> to view a task's full output.")
	return sb.String()
}

func (c *TasksCommand) renderDetail(runner AgentTaskRunner, query string) (string, error) {
	summaries := runner.ListTaskSummaries()
	match, err := resolveTaskID(summaries, query)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s · %s · %s · %s\n", taskStatusIcon(match.Status), match.ID, match.Type, match.Status, formatTaskDuration(match.Duration))
	if match.Task != "" {
		fmt.Fprintf(&sb, "Task: %s\n", match.Task)
	}
	if match.Error != "" {
		fmt.Fprintf(&sb, "Error: %s\n", match.Error)
	}

	output := match.Output
	outputFile := ""
	if res, ok := runner.GetResult(match.ID); ok && res != nil {
		if res.Output != "" {
			output = res.Output
		}
		outputFile = res.OutputFile
	}
	if strings.TrimSpace(output) == "" {
		if match.Status == agent.AgentStatusRunning {
			sb.WriteString("\nStill running — no output captured yet.")
		} else {
			sb.WriteString("\nNo output captured.")
		}
		return sb.String(), nil
	}

	sb.WriteString("\nOutput:\n")
	sb.WriteString(tailRunes(output, tasksOutputTailRunes))
	if outputFile != "" {
		fmt.Fprintf(&sb, "\n\nFull output: %s", outputFile)
	}
	return sb.String(), nil
}

// resolveTaskID matches an exact ID first, then a unique prefix — agent IDs
// are long generated strings nobody should have to type in full.
func resolveTaskID(summaries []agent.TaskSummary, query string) (agent.TaskSummary, error) {
	var prefixMatches []agent.TaskSummary
	for _, s := range summaries {
		if s.ID == query {
			return s, nil
		}
		if strings.HasPrefix(s.ID, query) {
			prefixMatches = append(prefixMatches, s)
		}
	}
	switch len(prefixMatches) {
	case 1:
		return prefixMatches[0], nil
	case 0:
		return agent.TaskSummary{}, fmt.Errorf("no background task matches %q — run /tasks to list IDs", query)
	default:
		ids := make([]string, 0, len(prefixMatches))
		for _, s := range prefixMatches {
			ids = append(ids, s.ID)
		}
		return agent.TaskSummary{}, fmt.Errorf("prefix %q is ambiguous: %s", query, strings.Join(ids, ", "))
	}
}

func taskStatusIcon(status agent.AgentStatus) string {
	switch status {
	case agent.AgentStatusRunning:
		return "●"
	case agent.AgentStatusCompleted:
		return "✓"
	case agent.AgentStatusFailed:
		return "✗"
	case agent.AgentStatusCancelled:
		return "◌"
	default:
		return "·"
	}
}

func formatTaskDuration(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	return d.Round(time.Second).String()
}

func firstLineTrimmed(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	runes := []rune(s)
	if maxRunes > 0 && len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return s
}

// tailRunes keeps the LAST n runes — an agent's conclusion lives at the end.
func tailRunes(s string, n int) string {
	runes := []rune(s)
	if n <= 0 || len(runes) <= n {
		return s
	}
	return "…" + string(runes[len(runes)-n:])
}
