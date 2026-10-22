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
	if err := mgr.ClaudeSettingsError(); err != nil {
		return "Claude settings hooks could not be loaded: " + err.Error(), nil
	}
	if provider, ok := app.(interface{ GetSkillHookRestoreError() error }); ok {
		if err := provider.GetSkillHookRestoreError(); err != nil {
			return "Required skill hooks are unavailable; tool execution is stopped: " + err.Error(), nil
		}
	}

	all := mgr.GetHooks()
	if provider, ok := app.(interface{ GetSkillHooks() []*hooks.Hook }); ok {
		all = append(all, provider.GetSkillHooks()...)
	}
	if len(all) == 0 {
		if mgr.ClaudeHooksDisabled() {
			return "Hooks are disabled by disableAllHooks in Claude settings.", nil
		}
		if mgr.ManagedHooksOnly() {
			return "Managed allowManagedHooksOnly policy is active; no managed hooks configured.", nil
		}
		return "No hooks configured.\n" +
			"Add them under `hooks:` in ~/.config/gokin/config.yaml, <project>/.gokin/hooks.yaml, or Claude settings.json/settings.local.json.\n" +
			"Events: pre_tool (fail_on_error blocks the call), post_tool, on_error, stop (fail_on_error asks the agent to continue), on_start, on_exit.", nil
	}

	var sb strings.Builder
	state := "enabled"
	if mgr.ClaudeHooksDisabled() && !mgr.IsEnabled() {
		state = "DISABLED (disableAllHooks in Claude settings)"
	} else if mgr.ManagedHooksOnly() {
		state = "managed hooks only (allowManagedHooksOnly)"
		if !mgr.IsEnabled() {
			state += " · no active managed handlers"
		}
	} else if !mgr.IsEnabled() {
		state = "DISABLED (hooks.enabled: false)"
	} else if !mgr.SkillHooksAllowed() {
		if mgr.ManagedHooksActive() {
			state += " · managed hooks active; ordinary hooks disabled"
		} else {
			state += " · session plugin hooks active; automatic hooks disabled"
		}
	}
	fmt.Fprintf(&sb, "Hooks: %d configured · system %s\n\n", len(all), state)

	for _, h := range all {
		marker := "●"
		if !mgr.HookEnabled(h) {
			marker = "○"
		}
		name := h.Name
		if name == "" {
			name = "(unnamed)"
		}
		scope := h.ToolName
		if h.Claude != nil {
			scope = h.Claude.Matcher
		}
		if scope == "" {
			scope = "*"
		}
		source := h.Source
		if source == "" {
			source = "config"
		}
		event := string(h.Type)
		if h.Claude != nil {
			event = h.Claude.Event
		}
		fmt.Fprintf(&sb, "%s %s — %s on %s [%s]\n", marker, name, event, scope, source)
		if h.Claude != nil && (h.Claude.Handler == "prompt" || h.Claude.Handler == "agent") {
			model := h.Claude.Model
			if model == "" {
				model = "small fast model"
			}
			fmt.Fprintf(&sb, "   %s (%s): %s\n", h.Claude.Handler, model, truncateHookCommand(h.Claude.Prompt))
		} else if h.Claude != nil && h.Claude.Handler == "http" {
			sb.WriteString("   HTTP POST (headers and URL hidden)\n")
		} else if h.Claude != nil && h.Claude.Handler == "mcp_tool" {
			fmt.Fprintf(&sb, "   MCP %s / %s\n", h.Claude.MCPServer, h.Claude.MCPTool)
		} else {
			fmt.Fprintf(&sb, "   %s\n", truncateHookCommand(h.Command))
		}
		var notes []string
		if h.Claude != nil && h.Claude.AsyncRewake {
			notes = append(notes, "background; wakes on exit 2")
		} else if h.Claude != nil && h.Claude.Async {
			notes = append(notes, "background; timeout ignored")
		}
		if h.Claude != nil && h.Claude.Once {
			notes = append(notes, "once after success")
		}
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
