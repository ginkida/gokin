package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gokin/internal/loops"
)

// LoopCommand exposes the /loop autonomous workflow system.
//
// Subcommands (parsed from args[0]):
//
//	(no args)        list active loops
//	list             same as no args (full list including stopped)
//	<task>           start self-paced loop with the task description
//	<interval> <task> start interval loop (interval like 5m, 1h, 30s)
//	status <id>      show loop details + recent iterations
//	stop <id>        stop a running loop (preserves history)
//	pause <id>       pause (won't fire until resume)
//	resume <id>      re-arm a paused loop
//	now <id>         force iteration to fire on the next scheduler tick
//	remove <id>      delete loop state file (irreversible)
//
// The "interval as first token" parse is what makes /loop feel like a
// natural command: /loop 30m clean up TODOs reads the way users type.
type LoopCommand struct{}

func (c *LoopCommand) Name() string        { return "loop" }
func (c *LoopCommand) Description() string { return "Run a recurring task in the background" }
func (c *LoopCommand) Usage() string {
	return `/loop                       List active loops
/loop <task>                Start self-paced loop
/loop <interval> <task>     Start interval loop (e.g. /loop 5m sync repo)
/loop status <id>           Show loop details
/loop stop <id>             Stop a loop (preserves history)
/loop pause <id>            Pause (until /loop resume)
/loop resume <id>           Re-arm paused loop
/loop now <id>              Fire immediately on next tick
/loop remove <id>           Delete loop state file`
}

func (c *LoopCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategorySession,
		Icon:     "loop",
		Priority: 60,
		HasArgs:  true,
		ArgHint:  "[<interval>] <task> | status|stop|pause|resume|now|remove <id>",
	}
}

func (c *LoopCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	mgr := app.GetLoopManager()
	if mgr == nil {
		return "Loop system unavailable in this build.", nil
	}

	// No args → list. Most common quick-check use case.
	if len(args) == 0 {
		return formatList(mgr.List()), nil
	}

	// First arg is a subcommand verb, an interval, or the start of a
	// free-form task description. Disambiguate carefully — if it's a
	// known verb, route to the verb handler; if it parses as an
	// interval (5m, 1h, 30s), treat the rest as the task; otherwise
	// the entire args slice is the task description.
	first := strings.ToLower(args[0])

	switch first {
	case "list":
		return formatList(mgr.List()), nil
	case "status":
		if len(args) < 2 {
			return "Usage: /loop status <id>", nil
		}
		return formatStatus(mgr, args[1])
	case "stop":
		if len(args) < 2 {
			return "Usage: /loop stop <id>", nil
		}
		if err := mgr.Stop(args[1]); err != nil {
			return fmt.Sprintf("Failed to stop %s: %v", args[1], err), nil
		}
		return fmt.Sprintf("Stopped loop %s.", args[1]), nil
	case "pause":
		if len(args) < 2 {
			return "Usage: /loop pause <id>", nil
		}
		if err := mgr.Pause(args[1]); err != nil {
			return fmt.Sprintf("Failed to pause %s: %v", args[1], err), nil
		}
		return fmt.Sprintf("Paused loop %s. Resume with /loop resume %s.", args[1], args[1]), nil
	case "resume":
		if len(args) < 2 {
			return "Usage: /loop resume <id>", nil
		}
		if err := mgr.Resume(args[1]); err != nil {
			return fmt.Sprintf("Failed to resume %s: %v", args[1], err), nil
		}
		return fmt.Sprintf("Resumed loop %s.", args[1]), nil
	case "now":
		if len(args) < 2 {
			return "Usage: /loop now <id>", nil
		}
		if err := mgr.FireNow(args[1]); err != nil {
			return fmt.Sprintf("Failed to fire %s: %v", args[1], err), nil
		}
		return fmt.Sprintf("Loop %s armed to fire on the next scheduler tick.", args[1]), nil
	case "remove":
		if len(args) < 2 {
			return "Usage: /loop remove <id>", nil
		}
		if err := mgr.Remove(args[1]); err != nil {
			return fmt.Sprintf("Failed to remove %s: %v", args[1], err), nil
		}
		return fmt.Sprintf("Removed loop %s.", args[1]), nil
	}

	// Not a verb. Try to parse first arg as an interval (5m, 1h, 30s).
	// If it parses, the remaining args are the task. Otherwise everything
	// is the task and we use self-paced mode.
	if seconds, ok := parseLoopInterval(args[0]); ok {
		if len(args) < 2 {
			return fmt.Sprintf("Interval %s given but no task. Usage: /loop %s <task>", args[0], args[0]), nil
		}
		task := strings.Join(args[1:], " ")
		l, err := mgr.Add(task, loops.ModeInterval, seconds)
		if err != nil {
			return fmt.Sprintf("Failed to start loop: %v", err), nil
		}
		return fmt.Sprintf("Started loop %s — fires every %s.\n  Task: %s\n\nView: /loop status %s | Stop: /loop stop %s",
			l.ID, args[0], previewTaskShort(task), l.ID, l.ID), nil
	}

	// Fallthrough: entire args are a task description, self-paced mode.
	task := strings.Join(args, " ")
	l, err := mgr.Add(task, loops.ModeSelfPaced, 0)
	if err != nil {
		return fmt.Sprintf("Failed to start loop: %v", err), nil
	}
	return fmt.Sprintf("Started self-paced loop %s.\n  Task: %s\n\nView: /loop status %s | Stop: /loop stop %s",
		l.ID, previewTaskShort(task), l.ID, l.ID), nil
}

// parseLoopInterval recognizes the same shorthand as /schedule and /loop
// in Claude Code: 5m, 1h, 2d, 30s. Returns total seconds and ok=true on
// success. Doesn't accept compound forms (1h30m) — keep the surface
// small; users with unusual intervals can use raw seconds (300s instead
// of 5m, etc.). Negative or zero intervals are rejected.
func parseLoopInterval(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return 0, false
	}
	unit := s[len(s)-1]
	numStr := s[:len(s)-1]
	n, err := strconv.Atoi(numStr)
	if err != nil || n <= 0 {
		return 0, false
	}
	switch unit {
	case 's':
		return int64(n), true
	case 'm':
		return int64(n) * 60, true
	case 'h':
		return int64(n) * 3600, true
	case 'd':
		return int64(n) * 86400, true
	default:
		return 0, false
	}
}

// formatList builds a compact human-readable listing for /loop with no
// args. Empty case shows a hint how to start one — same UX class as
// v0.80.19 /sessions discoverability.
func formatList(loopList []*loops.Loop) string {
	if len(loopList) == 0 {
		return "No loops configured.\n\nStart one with:\n  /loop <task>           — self-paced (agent decides cadence)\n  /loop 30m <task>       — fires every 30 minutes"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%d loop(s):\n\n", len(loopList)))
	for _, l := range loopList {
		sb.WriteString(formatLoopLine(l))
		sb.WriteString("\n")
	}
	sb.WriteString("\nDetails: /loop status <id> | Stop: /loop stop <id>")
	return sb.String()
}

func formatLoopLine(l *loops.Loop) string {
	statusMark := "●"
	switch l.Status {
	case loops.StatusPaused:
		statusMark = "‖"
	case loops.StatusStopped:
		statusMark = "■"
	case loops.StatusCompleted:
		statusMark = "✓"
	}
	mode := "self-paced"
	if l.Mode == loops.ModeInterval {
		mode = fmt.Sprintf("every %s", formatDurationShort(time.Duration(l.IntervalSeconds)*time.Second))
	}
	taskPrev := previewTaskShort(l.Task)
	nextHint := ""
	if l.IsActive() && !l.NextRunAt.IsZero() {
		dur := time.Until(l.NextRunAt)
		if dur < 0 {
			nextHint = " — due now"
		} else {
			nextHint = " — next " + formatDurationShort(dur)
		}
	}
	return fmt.Sprintf("  %s %s  [%s]  %s%s\n      %s",
		statusMark, l.ID, mode, l.Status, nextHint, taskPrev)
}

func formatStatus(mgr LoopManager, id string) (string, error) {
	l, ok := mgr.Get(id)
	if !ok {
		return fmt.Sprintf("Loop %s not found. Run /loop to see available loops.", id), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Loop %s — %s\n", l.ID, l.Status))
	sb.WriteString(fmt.Sprintf("  Task: %s\n", l.Task))
	if l.Mode == loops.ModeInterval {
		sb.WriteString(fmt.Sprintf("  Mode: interval (every %s)\n",
			formatDurationShort(time.Duration(l.IntervalSeconds)*time.Second)))
	} else {
		sb.WriteString("  Mode: self-paced\n")
	}
	sb.WriteString(fmt.Sprintf("  Created: %s\n", l.CreatedAt.Format(time.RFC3339)))
	if !l.LastRunAt.IsZero() {
		sb.WriteString(fmt.Sprintf("  Last run: %s\n", l.LastRunAt.Format(time.RFC3339)))
	}
	if l.IsActive() && !l.NextRunAt.IsZero() {
		dur := time.Until(l.NextRunAt)
		if dur < 0 {
			sb.WriteString("  Next run: due now\n")
		} else {
			sb.WriteString(fmt.Sprintf("  Next run: in %s (%s)\n",
				formatDurationShort(dur), l.NextRunAt.Format(time.RFC3339)))
		}
	}
	sb.WriteString(fmt.Sprintf("  Iterations: %d", l.IterationCount))
	if l.MaxIterations > 0 {
		sb.WriteString(fmt.Sprintf(" / %d", l.MaxIterations))
	}
	sb.WriteString("\n")

	if len(l.Iterations) > 0 {
		sb.WriteString("\nRecent iterations:\n")
		// Show most recent first, last 5.
		count := 5
		if count > len(l.Iterations) {
			count = len(l.Iterations)
		}
		for i := len(l.Iterations) - 1; i >= len(l.Iterations)-count; i-- {
			it := l.Iterations[i]
			marker := "✓"
			if !it.OK {
				marker = "✗"
			}
			summary := it.Summary
			if summary == "" {
				summary = "(no summary)"
			}
			if r := []rune(summary); len(r) > 80 {
				summary = string(r[:77]) + "..."
			}
			sb.WriteString(fmt.Sprintf("  %s #%d  %s  (%s) — %s\n",
				marker, it.N,
				it.StartedAt.Format("2006-01-02 15:04"),
				formatDurationShort(it.Duration),
				summary))
		}
	}

	return sb.String(), nil
}

// LoopManager is the subset of *loops.Manager used by this command.
// Defining it as an interface keeps the command testable without
// pulling in real Storage.
type LoopManager interface {
	Add(task string, mode loops.Mode, intervalSeconds int64, opts ...loops.AddOption) (*loops.Loop, error)
	Get(id string) (*loops.Loop, bool)
	List() []*loops.Loop
	Active() []*loops.Loop
	Stop(id string) error
	Pause(id string) error
	Resume(id string) error
	FireNow(id string) error
	Remove(id string) error
}

// previewTaskShort caps a task description for inline display. Single-
// line, max 60 runes, ellipsized.
func previewTaskShort(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 60 {
		return string(r[:57]) + "..."
	}
	return s
}

// formatDurationShort renders durations like "5m", "1h30m", "2d3h".
// Cleaner than time.Duration.String() which produces "5m0s" / "1h0m0s".
func formatDurationShort(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) - days*24
	if hours == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, hours)
}
