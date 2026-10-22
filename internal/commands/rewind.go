package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gokin/internal/rewind"
)

type RewindCommand struct{}

func (*RewindCommand) Name() string { return "rewind" }
func (*RewindCommand) Description() string {
	return "Restore or summarize at a selected prompt"
}
func (*RewindCommand) Usage() string {
	return "/rewind [<point-id|number> <code|conversation|both|summarize-from|summarize-before> [-- guidance]] | previous | transcript <summary-id>"
}
func (*RewindCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{Category: CategorySession, Icon: "undo", Priority: 69, HasArgs: true, ArgHint: "[point code|conversation|both]"}
}

type rewindRuntime interface {
	ListRewindPoints(context.Context) ([]rewind.Point, error)
	RewindToPoint(context.Context, string, string) (string, error)
}

func (c *RewindCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	runtime, ok := app.(rewindRuntime)
	if !ok {
		return "", fmt.Errorf("turn rewind is unavailable in this runtime")
	}
	previousID := ""
	if previous, ok := app.(interface{ PreviousClearSessionID() string }); ok {
		previousID = previous.PreviousClearSessionID()
	}
	if len(args) > 0 && args[0] == "previous" {
		if len(args) > 2 {
			return "", fmt.Errorf("usage: /rewind previous")
		}
		expected := previousID
		if len(args) == 2 {
			expected = args[1]
		}
		if previous, ok := app.(interface {
			ResumePreviousClearSession(context.Context, string, bool) (string, error)
		}); ok {
			return previous.ResumePreviousClearSession(ctx, expected, false)
		}
		return "", fmt.Errorf("previous conversation resume is unavailable in this runtime")
	}
	if len(args) >= 1 && args[0] == "transcript" {
		if len(args) != 2 {
			return "", fmt.Errorf("usage: /rewind transcript <summary-id>")
		}
		if reader, ok := app.(interface {
			ReadSummaryTranscript(context.Context, string) (string, error)
		}); ok {
			return reader.ReadSummaryTranscript(ctx, args[1])
		}
		return "", fmt.Errorf("summary transcript reading is unavailable in this runtime")
	}
	if len(args) == 1 {
		return "", fmt.Errorf("usage: %s", c.Usage())
	}
	points, err := runtime.ListRewindPoints(ctx)
	if err != nil {
		if len(args) != 0 || previousID == "" {
			return "", err
		}
		points = nil
	}
	if len(points) == 0 && previousID == "" {
		return "No turn checkpoints yet. Each new prompt captures a checkpoint when session persistence is enabled.", nil
	}
	if len(args) == 0 {
		if menu, ok := app.(interface {
			ShowRewindMenu(context.Context, []rewind.Point) bool
		}); ok && menu.ShowRewindMenu(ctx, points) {
			return "", nil
		}
		var out strings.Builder
		if previousID != "" {
			fmt.Fprintf(&out, "/resume %s (previous session) — /rewind previous\n\n", singleLineDisplayText(previousID, 240))
		}
		out.WriteString("Rewind points (oldest first):\n")
		for i, p := range points {
			state := "conversation"
			for _, later := range points[i:] {
				if len(later.Events) > 0 {
					state = "code + conversation"
					break
				}
			}
			if p.Error != "" {
				state = "conversation; code snapshot incomplete"
			}
			fmt.Fprintf(&out, "  %d. %s  %s [%s]\n", i+1, p.ID, singleLineDisplayText(p.Prompt, 100), state)
		}
		out.WriteString("\n/rewind <number|point-id> code|conversation|both|summarize-from|summarize-before [-- guidance]\nRestores before that prompt. Summaries keep original messages; shell and external edits are not captured.")
		return out.String(), nil
	}
	mode := args[1]
	if mode != "code" && mode != "conversation" && mode != "both" && mode != "summarize-from" && mode != "summarize-before" {
		return "", fmt.Errorf("usage: %s", c.Usage())
	}
	id := args[0]
	if n, err := strconv.Atoi(id); err == nil {
		if n < 1 || n > len(points) {
			return "", fmt.Errorf("rewind point number is out of range")
		}
		id = points[n-1].ID
	}
	if mode == "summarize-from" || mode == "summarize-before" {
		if len(args) > 2 && (len(args) < 4 || args[2] != "--") {
			return "", fmt.Errorf("usage: %s", c.Usage())
		}
		guide := ""
		if len(args) > 3 {
			guide = strings.Join(args[3:], " ")
		}
		if runtime, ok := app.(interface {
			SummarizeAtPoint(context.Context, string, string, string) (string, error)
		}); ok {
			return runtime.SummarizeAtPoint(ctx, id, mode, guide)
		}
		return "", fmt.Errorf("targeted summarization is unavailable in this runtime")
	}
	if len(args) != 2 {
		return "", fmt.Errorf("usage: %s", c.Usage())
	}
	return runtime.RewindToPoint(ctx, id, mode)
}
