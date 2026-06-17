package commands

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/config"
)

// SetCommand is the unified in-app settings surface: `/set` lists the common
// toggles with their current values, `/set <key> <on|off>` changes one and
// applies it live via ApplyConfig (no YAML editing, no restart). It complements
// the read-only `/config` and folds the scattered per-setting commands
// (/permissions, /sandbox, …) into one discoverable place.
type SetCommand struct{}

func (c *SetCommand) Name() string        { return "set" }
func (c *SetCommand) Description() string { return "View or change a setting (live)" }
func (c *SetCommand) Usage() string       { return "/set [<key> <on|off>]" }
func (c *SetCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryAuthSetup,
		Icon:     "config",
		Priority: 9,
		HasArgs:  true,
		ArgHint:  "<key> <on|off>",
	}
}

// settingToggle is one user-facing boolean setting exposed by /set. Keep this
// list curated and HONEST — only settings that actually take effect in-process
// (ApplyConfig propagates them, verified) belong here, so toggling one is never
// a silent no-op the user has to restart to discover didn't work.
type settingToggle struct {
	key  string
	desc string
	get  func(*config.Config) bool
	set  func(*config.Config, bool)
}

var settableToggles = []settingToggle{
	{"permissions", "Ask before risky tool actions",
		func(c *config.Config) bool { return c.Permission.Enabled },
		func(c *config.Config, v bool) { c.Permission.Enabled = v }},
	{"sandbox", "Run bash commands in a sandbox",
		func(c *config.Config) bool { return c.Tools.Bash.Sandbox },
		func(c *config.Config, v bool) { c.Tools.Bash.Sandbox = v }},
	{"diff", "Show a diff approval card before edits",
		func(c *config.Config) bool { return c.DiffPreview.Enabled },
		func(c *config.Config, v bool) { c.DiffPreview.Enabled = v }},
	{"tokens", "Show token usage in the status bar",
		func(c *config.Config) bool { return c.UI.ShowTokenUsage },
		func(c *config.Config, v bool) { c.UI.ShowTokenUsage = v }},
	{"autocompact", "Auto-summarize history near the context limit",
		func(c *config.Config) bool { return c.Context.EnableAutoSummary },
		func(c *config.Config, v bool) { c.Context.EnableAutoSummary = v }},
	{"memory", "Enable the memory tool and recall",
		func(c *config.Config) bool { return c.Memory.Enabled },
		func(c *config.Config, v bool) { c.Memory.Enabled = v }},
	{"plan", "Enable plan-mode tools",
		func(c *config.Config) bool { return c.Plan.Enabled },
		func(c *config.Config, v bool) { c.Plan.Enabled = v }},
	{"donegate", "Verify build/test before finishing a task",
		func(c *config.Config) bool { return c.DoneGate.Enabled },
		func(c *config.Config, v bool) { c.DoneGate.Enabled = v }},
}

// ToggleState is a settable toggle's current value. Shared by /set and the
// interactive /settings modal so there is ONE source of truth for what is
// configurable — the modal renders these and reports flips back through
// ApplySettingToggle.
type ToggleState struct {
	Key  string
	Desc string
	On   bool
}

// SettableToggleStates returns every curated toggle with its current value,
// in display order.
func SettableToggleStates(cfg *config.Config) []ToggleState {
	states := make([]ToggleState, 0, len(settableToggles))
	for _, t := range settableToggles {
		states = append(states, ToggleState{Key: t.key, Desc: t.desc, On: t.get(cfg)})
	}
	return states
}

// ApplySettingToggle sets one toggle on cfg by key. Returns false if the key is
// not a known toggle (caller should not then ApplyConfig).
func ApplySettingToggle(cfg *config.Config, key string, on bool) bool {
	t, ok := findToggle(key)
	if !ok {
		return false
	}
	t.set(cfg, on)
	return true
}

// SettingsMarker is returned by /settings to tell the app to open the
// interactive settings modal — the same result-prefix mechanism as PromptMarker.
const SettingsMarker = "__settings:"

// SettingsCommand opens the interactive settings screen (a visual layer over the
// same toggles /set exposes).
type SettingsCommand struct{}

func (c *SettingsCommand) Name() string        { return "settings" }
func (c *SettingsCommand) Description() string { return "Open the interactive settings screen" }
func (c *SettingsCommand) Usage() string       { return "/settings" }
func (c *SettingsCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryAuthSetup,
		Icon:     "config",
		Priority: 8,
	}
}

func (c *SettingsCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	return SettingsMarker, nil
}

func findToggle(key string) (settingToggle, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, t := range settableToggles {
		if t.key == key {
			return t, true
		}
	}
	return settingToggle{}, false
}

// parseOnOff parses a boolean-ish value. ok=false when it's neither.
func parseOnOff(v string) (val bool, ok bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "1", "enable", "enabled", "yes":
		return true, true
	case "off", "false", "0", "disable", "disabled", "no":
		return false, true
	}
	return false, false
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func (c *SetCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Config not available.", nil
	}

	// No args — list every setting with its current value (the discoverable surface).
	if len(args) == 0 {
		return c.list(cfg), nil
	}

	t, ok := findToggle(args[0])
	if !ok {
		return fmt.Sprintf("Unknown setting: %s\n\n%s", args[0], c.list(cfg)), nil
	}

	// Key only — show its current value + how to change it.
	if len(args) < 2 {
		return fmt.Sprintf("%s: %s  (%s)\n\nChange with: /set %s on|off",
			t.key, onOff(t.get(cfg)), t.desc, t.key), nil
	}

	val, valid := parseOnOff(args[1])
	if !valid {
		return fmt.Sprintf("Invalid value %q — use on or off.\n\n/set %s on|off", args[1], t.key), nil
	}

	t.set(cfg, val)
	if err := app.ApplyConfig(cfg); err != nil {
		return fmt.Sprintf("Failed to apply: %v", err), nil
	}
	return fmt.Sprintf("%s: %s", t.key, onOff(val)), nil
}

func (c *SetCommand) list(cfg *config.Config) string {
	var sb strings.Builder
	sb.WriteString("Settings (use /set <key> on|off to change — applies live):\n\n")
	for _, t := range settableToggles {
		fmt.Fprintf(&sb, "  %-12s %-3s  %s\n", t.key, onOff(t.get(cfg)), t.desc)
	}
	sb.WriteString("\nModel/provider: /model, /provider, /login · Thinking: /thinking")
	return sb.String()
}
