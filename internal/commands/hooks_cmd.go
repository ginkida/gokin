package commands

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/hooks"
)

// HooksCommand lists the configured agent hooks and where they came from.
type HooksCommand struct{}

func (c *HooksCommand) Name() string        { return "hooks" }
func (c *HooksCommand) Description() string { return "List configured agent hooks and their sources" }
func (c *HooksCommand) Usage() string       { return "/hooks" }
func (c *HooksCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "hooks",
		Priority: 70,
	}
}

func (c *HooksCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	mgr := app.GetHooksManager()
	if mgr == nil {
		return "Hooks are unavailable in this build.", nil
	}

	all := mgr.GetHooks()
	if len(all) == 0 {
		return "No hooks configured.\n" +
			"Add them under `hooks:` in ~/.config/gokin/config.yaml or in <project>/.gokin/hooks.yaml.\n" +
			"Events: pre_tool (fail_on_error blocks the call), post_tool, on_error, stop (fail_on_error asks the agent to continue), on_start, on_exit.", nil
	}

	var sb strings.Builder
	state := "enabled"
	if !mgr.IsEnabled() {
		state = "DISABLED (hooks.enabled: false)"
	}
	fmt.Fprintf(&sb, "Hooks: %d configured · system %s\n\n", len(all), state)

	for _, h := range all {
		marker := "●"
		if !h.Enabled {
			marker = "○"
		}
		name := h.Name
		if name == "" {
			name = "(unnamed)"
		}
		scope := h.ToolName
		if scope == "" {
			scope = "*"
		}
		source := h.Source
		if source == "" {
			source = "config"
		}
		fmt.Fprintf(&sb, "%s %s — %s on %s [%s]\n", marker, name, h.Type, scope, source)
		fmt.Fprintf(&sb, "   %s\n", truncateHookCommand(h.Command))
		var notes []string
		if h.FailOnError {
			switch h.Type {
			case hooks.PreTool:
				notes = append(notes, "blocks the tool call on failure")
			case hooks.Stop:
				notes = append(notes, "asks the agent to continue on failure")
			default:
				notes = append(notes, "stops the hook chain on failure")
			}
		}
		if h.Condition != "" && h.Condition != hooks.ConditionAlways {
			notes = append(notes, "condition: "+string(h.Condition))
		}
		if h.DependsOn != "" {
			notes = append(notes, "after: "+h.DependsOn)
		}
		if len(notes) > 0 {
			fmt.Fprintf(&sb, "   %s\n", strings.Join(notes, " · "))
		}
	}

	return strings.TrimRight(sb.String(), "\n"), nil
}

func truncateHookCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if idx := strings.IndexByte(cmd, '\n'); idx >= 0 {
		cmd = strings.TrimSpace(cmd[:idx]) + " …"
	}
	runes := []rune(cmd)
	if len(runes) > 100 {
		return string(runes[:100]) + "…"
	}
	return cmd
}
