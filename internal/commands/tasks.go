package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gokin/internal/agent"
	"gokin/internal/tasks"
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

// BackgroundShellRunner is the narrow tasks.Manager surface /tasks needs for
// background SHELL commands (bash/ssh run_in_background). *tasks.Manager
// already implements this signature-for-signature, so App.GetBackgroundShellRunner
// can return it directly. A DIFFERENT namespace from agent tasks — shell task
// IDs are "task_<unixts>_<n>" (tasks.Manager.Start), agent IDs are 16 hex
// chars (generateAgentID) — so prefix resolution across both never collides.
type BackgroundShellRunner interface {
	List() []tasks.Info
	Cancel(id string) error
}

// TasksCommand lists background agent tasks and shows their results —
// the user-facing window into "spawned an agent, kept working" flows
// (task tool with run_in_background, coordinate, sub-agents).
type TasksCommand struct{}

func (c *TasksCommand) Name() string { return "tasks" }
func (c *TasksCommand) Description() string {
	return "List background agent + shell tasks, view results, or stop a running one"
}
func (c *TasksCommand) Usage() string { return "/tasks [id] | /tasks stop <id>" }
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
	shells := app.GetBackgroundShellRunner()
	if runner == nil && shells == nil {
		return "Background tasks are unavailable in this build.", nil
	}

	if len(args) > 0 && strings.TrimSpace(args[0]) == "stop" {
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			return "Usage: /tasks stop <id>  (ids from /tasks)", nil
		}
		return c.stopTask(runner, shells, strings.TrimSpace(args[1]))
	}
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		return c.renderDetail(runner, shells, strings.TrimSpace(args[0]))
	}
	return c.renderList(runner, shells), nil
}

// stopTask cancels a background agent OR shell task by id (exact or unique
// prefix). Agent resolution is tried first (the more common case); a shell
// task is tried only when the id doesn't resolve as an agent, since the two
// ID namespaces never collide (see BackgroundShellRunner's doc comment).
func (c *TasksCommand) stopTask(runner AgentTaskRunner, shells BackgroundShellRunner, rawID string) (string, error) {
	if runner != nil {
		if match, err := resolveTaskID(runner.ListTaskSummaries(), rawID); err == nil {
			if err := runner.Cancel(match.ID); err != nil {
				return fmt.Sprintf("Failed to stop %s: %v", match.ID, err), nil
			}
			return fmt.Sprintf("Stopped background task %s. Partial output stays available via /tasks %s.", match.ID, match.ID), nil
		} else if strings.Contains(err.Error(), "ambiguous") {
			// Agent and shell IDs have disjoint prefixes, so trying the shell
			// namespace cannot disambiguate an agent-shaped query.
			return err.Error(), nil
		}
	}
	if shells != nil {
		if match, err := resolveShellTaskID(shells.List(), rawID); err == nil {
			if err := shells.Cancel(match.ID); err != nil {
				return fmt.Sprintf("Failed to stop shell task %s: %v", match.ID, err), nil
			}
			return fmt.Sprintf("Stopped background shell task %s (command: %s).", match.ID, match.Command), nil
		} else if strings.Contains(err.Error(), "ambiguous") {
			return err.Error(), nil
		}
	}
	return fmt.Sprintf("no background task matches %q — run /tasks to list IDs", rawID), nil
}

func (c *TasksCommand) renderList(runner AgentTaskRunner, shells BackgroundShellRunner) string {
	var summaries []agent.TaskSummary
	if runner != nil {
		summaries = runner.ListTaskSummaries()
	}
	var shellTasks []tasks.Info
	if shells != nil {
		shellTasks = shells.List()
	}
	if len(summaries) == 0 && len(shellTasks) == 0 {
		return "No background tasks in this session.\n" +
			"Start one with the task tool (run_in_background), /loop, bash/ssh run_in_background, or ask the model to delegate work to a sub-agent."
	}

	var sb strings.Builder
	if len(summaries) > 0 {
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
	}

	if len(shellTasks) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("Background shell tasks (bash/ssh run_in_background)\n\n")
		shown := shellTasks
		if len(shown) > tasksListLimit {
			shown = shown[:tasksListLimit]
		}
		for _, s := range shown {
			fmt.Fprintf(&sb, "%s %s · %s · %s\n", shellStatusIcon(s.Status), s.ID, s.Status, formatTaskDuration(s.Duration))
			fmt.Fprintf(&sb, "   %s\n", firstLineTrimmed(s.Command, 120))
		}
		if len(shellTasks) > tasksListLimit {
			fmt.Fprintf(&sb, "\n… and %d older shell task(s)\n", len(shellTasks)-tasksListLimit)
		}
	}

	sb.WriteString("\nUse /tasks <id> for full output, /tasks stop <id> to cancel a running one.")
	return sb.String()
}

func (c *TasksCommand) renderDetail(runner AgentTaskRunner, shells BackgroundShellRunner, query string) (string, error) {
	var agentErr error
	if runner != nil {
		summaries := runner.ListTaskSummaries()
		match, err := resolveTaskID(summaries, query)
		if err == nil {
			return c.renderAgentDetail(runner, match), nil
		}
		agentErr = err
	}
	if shells != nil {
		match, err := resolveShellTaskID(shells.List(), query)
		if err == nil {
			return renderShellDetail(match), nil
		}
	}
	if agentErr != nil {
		return "", agentErr
	}
	return "", fmt.Errorf("no background task matches %q — run /tasks to list IDs", query)
}

func (c *TasksCommand) renderAgentDetail(runner AgentTaskRunner, match agent.TaskSummary) string {
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
		return sb.String()
	}

	sb.WriteString("\nOutput:\n")
	sb.WriteString(tailRunes(output, tasksOutputTailRunes))
	if outputFile != "" {
		fmt.Fprintf(&sb, "\n\nFull output: %s", outputFile)
	}
	return sb.String()
}

func renderShellDetail(match tasks.Info) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s · %s · %s\n", shellStatusIcon(match.Status), match.ID, match.Status, formatTaskDuration(match.Duration))
	fmt.Fprintf(&sb, "Command: %s\n", match.Command)
	if match.ExitCode != 0 {
		fmt.Fprintf(&sb, "Exit code: %d\n", match.ExitCode)
	}
	if match.Error != "" {
		fmt.Fprintf(&sb, "Error: %s\n", match.Error)
	}
	if strings.TrimSpace(match.Output) == "" {
		if match.Status == "running" {
			sb.WriteString("\nStill running — no output captured yet.")
		} else {
			sb.WriteString("\nNo output captured.")
		}
		return sb.String()
	}
	sb.WriteString("\nOutput:\n")
	sb.WriteString(tailRunes(match.Output, tasksOutputTailRunes))
	return sb.String()
}

// resolveShellTaskID mirrors resolveTaskID for shell tasks.
func resolveShellTaskID(infos []tasks.Info, query string) (tasks.Info, error) {
	var prefixMatches []tasks.Info
	for _, s := range infos {
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
		return tasks.Info{}, fmt.Errorf("no background shell task matches %q", query)
	default:
		ids := make([]string, 0, len(prefixMatches))
		for _, s := range prefixMatches {
			ids = append(ids, s.ID)
		}
		return tasks.Info{}, fmt.Errorf("prefix %q is ambiguous: %s", query, strings.Join(ids, ", "))
	}
}

func shellStatusIcon(status string) string {
	switch status {
	case "running":
		return "●"
	case "completed":
		return "✓"
	case "failed":
		return "✗"
	case "cancelled", "killed":
		return "◌"
	default:
		return "·"
	}
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
