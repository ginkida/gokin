package tools

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// LoopControlTool lets the MODEL manage the user's background /loop tasks —
// "отмени мой loop" in chat must just work, without the user hunting for an
// id and typing /loop pause <id> themselves. Callbacks are injected by the
// builder (mcp_admin pattern: the loops manager lives in internal/loops,
// which the app layer wires in — the tool itself stays dependency-free and
// testable). Callbacks are boot-set before the registry is shared, never
// mutated per-agent, so the shared instance needs no CloneToolForWorkDir case.
type LoopControlTool struct {
	list   func() []LoopControlInfo
	pause  func(id string) error
	resume func(id string) error
	stop   func(id string) error
}

// LoopControlInfo is the minimal per-loop view the tool needs (decoupled from
// internal/loops so tools never imports it).
type LoopControlInfo struct {
	ID     string
	Task   string
	Status string // "running", "paused", "stopped", "completed"
}

// NewLoopControlTool creates the tool with nil callbacks (returns a setup
// hint until the builder wires them).
func NewLoopControlTool() *LoopControlTool {
	return &LoopControlTool{}
}

// SetCallbacks wires the loop-manager operations. Called once at boot.
func (t *LoopControlTool) SetCallbacks(
	list func() []LoopControlInfo,
	pause, resume, stop func(id string) error,
) {
	t.list = list
	t.pause = pause
	t.resume = resume
	t.stop = stop
}

func (t *LoopControlTool) Name() string { return "loop_control" }

func (t *LoopControlTool) Description() string {
	return "Manage the user's background loops (recurring /loop tasks that keep firing across restarts): list them, or pause/resume/stop one. Use when the user asks to cancel, pause, or check their loop."
}

func (t *LoopControlTool) Declaration() *genai.FunctionDeclaration {
	return LoopControlToolDeclaration()
}

func (t *LoopControlTool) Validate(args map[string]any) error {
	action, _ := args["action"].(string)
	switch action {
	case "list", "pause", "resume", "stop":
		return nil
	default:
		return fmt.Errorf("action must be one of: list, pause, resume, stop")
	}
}

func (t *LoopControlTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if t.list == nil {
		return NewErrorResult("loop control is not wired in this build"), nil
	}
	action, _ := args["action"].(string)
	id, _ := args["id"].(string)
	id = strings.TrimSpace(id)

	loops := t.list()

	if action == "list" {
		if len(loops) == 0 {
			return NewSuccessResult("No loops exist. The user can start one with /loop <task>."), nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d loop(s):\n", len(loops))
		for _, l := range loops {
			fmt.Fprintf(&b, "- %s [%s] %s\n", l.ID, l.Status, l.Task)
		}
		b.WriteString("Stopping is terminal; pausing is reversible with resume. An in-flight iteration always finishes first.")
		return NewSuccessResult(b.String()), nil
	}

	// pause/resume/stop: resolve the target — explicit id, or the SOLE
	// eligible loop (the common single-loop case must not require an id).
	eligible := func(l LoopControlInfo) bool {
		switch action {
		case "pause":
			return l.Status == "running"
		case "resume":
			return l.Status == "paused"
		default: // stop
			return l.Status == "running" || l.Status == "paused"
		}
	}
	if id == "" {
		var candidates []LoopControlInfo
		for _, l := range loops {
			if eligible(l) {
				candidates = append(candidates, l)
			}
		}
		switch len(candidates) {
		case 0:
			return NewErrorResult(fmt.Sprintf("no loop is eligible to %s — call action=list to see loop states", action)), nil
		case 1:
			id = candidates[0].ID
		default:
			var b strings.Builder
			fmt.Fprintf(&b, "several loops are eligible to %s — pass the id of one:\n", action)
			for _, l := range candidates {
				fmt.Fprintf(&b, "- %s [%s] %s\n", l.ID, l.Status, l.Task)
			}
			return NewErrorResult(b.String()), nil
		}
	}

	var op func(string) error
	var verb string
	switch action {
	case "pause":
		op, verb = t.pause, "Paused"
	case "resume":
		op, verb = t.resume, "Resumed"
	case "stop":
		op, verb = t.stop, "Stopped"
	}
	if op == nil {
		return NewErrorResult("loop control is not wired in this build"), nil
	}
	if err := op(id); err != nil {
		return NewErrorResult(fmt.Sprintf("failed to %s loop %s: %v", action, id, err)), nil
	}
	var msg string
	if action == "stop" {
		// The builder-wired stop callback also cancels the loop's in-flight
		// iteration — stop is terminal, the on-screen work halts NOW.
		msg = fmt.Sprintf("Stopped loop %s. Its in-flight iteration (if any) was cancelled.", id)
	} else {
		msg = fmt.Sprintf("%s loop %s. Any in-flight iteration finishes before this takes effect.", verb, id)
		if action == "pause" {
			msg += " It can be resumed later (action=resume)."
		}
	}
	return NewSuccessResult(msg), nil
}

// LoopControlToolDeclaration is the single schema source (declarations.go
// references it too — the two-copy drift rule).
func LoopControlToolDeclaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "loop_control",
		Description: "Manage the user's background loops (recurring /loop tasks that persist across gokin restarts). Use when the user asks to cancel, pause, resume, or inspect their loop.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"action": {
					Type: genai.TypeString,
					Description: "What to do. " +
						"'list' — every loop with id, status, and task. " +
						"'pause' — reversible; the loop stops firing until resumed. " +
						"'resume' — restart a paused loop. " +
						"'stop' — terminal; the loop will not fire again. " +
						"An in-flight iteration always finishes before pause/stop takes effect.",
					Enum: []string{"list", "pause", "resume", "stop"},
				},
				"id": {
					Type:        genai.TypeString,
					Description: "Loop id (from action=list). Optional when exactly one loop is eligible — the common single-loop case resolves automatically.",
				},
			},
			Required: []string{"action"},
		},
	}
}
