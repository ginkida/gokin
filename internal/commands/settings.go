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

// category groups settings for display. An ordered int enum (not a string)
// guarantees deterministic group order and rules out a typo'd category name.
type category int

const (
	catSafety category = iota
	catContext
	catWorkflow
	catFiles
	catInterface
)

// String is the human-readable category header shared by /set and the modal.
func (c category) String() string {
	switch c {
	case catSafety:
		return "Safety"
	case catContext:
		return "Context & Memory"
	case catWorkflow:
		return "Workflow"
	case catFiles:
		return "Files & Search"
	case catInterface:
		return "Interface & Web"
	}
	return ""
}

// settingToggle is one user-facing boolean setting exposed by /set. Keep this
// list curated and HONEST — only settings that actually take effect in-process
// (ApplyConfig propagates them, verified) belong here, so toggling one is never
// a silent no-op the user has to restart to discover didn't work.
type settingToggle struct {
	key  string
	name string // friendly display name (the modal/`/set` show this; key stays visible for discoverability)
	desc string
	cat  category
	// live is true when ApplyConfig propagates the change in-process this
	// session. A false toggle is still HONEST — it persists to the config and
	// takes effect on the next launch — but it is boot-wired, so the UI and /set
	// label it "restart to apply" instead of pretending it took hold now. This
	// is the no-silent-no-op rule made explicit rather than excluding the toggle.
	live bool
	get  func(*config.Config) bool
	set  func(*config.Config, bool)
}

// settableToggles is ordered by category (canonical group order); both /set and
// the modal inherit this single grouping. Boot-wired settings carry live=false
// (honest "restart to apply"). Keep entries in category-contiguous order — the
// invariant test pins that a category never appears, ends, then reappears.
var settableToggles = []settingToggle{
	// Safety
	{"permissions", "Ask before risky actions", "Ask before risky tool actions", catSafety, true,
		func(c *config.Config) bool { return c.Permission.Enabled },
		func(c *config.Config, v bool) { c.Permission.Enabled = v }},
	{"sandbox", "Sandbox bash commands", "Run bash commands in a sandbox", catSafety, true,
		func(c *config.Config) bool { return c.Tools.Bash.Sandbox },
		func(c *config.Config, v bool) { c.Tools.Bash.Sandbox = v }},
	{"diff", "Confirm edits with a diff", "Show a diff approval card before edits", catSafety, true,
		func(c *config.Config) bool { return c.DiffPreview.Enabled },
		func(c *config.Config, v bool) { c.DiffPreview.Enabled = v }},

	// Context & Memory
	{"autocompact", "Auto-compact long history", "Auto-summarize history near the context limit", catContext, true,
		func(c *config.Config) bool { return c.Context.EnableAutoSummary },
		func(c *config.Config, v bool) { c.Context.EnableAutoSummary = v }},
	{"memory", "Memory tool & recall", "Enable the memory tool and recall", catContext, true,
		func(c *config.Config) bool { return c.Memory.Enabled },
		func(c *config.Config, v bool) { c.Memory.Enabled = v }},
	{"sessionmemory", "Remember this session", "Auto-summarize the session into memory", catContext, true,
		func(c *config.Config) bool { return c.SessionMemory.Enabled },
		func(c *config.Config, v bool) { c.SessionMemory.Enabled = v }},

	// Workflow
	{"plan", "Plan mode", "Enable plan-mode tools", catWorkflow, true,
		func(c *config.Config) bool { return c.Plan.Enabled },
		func(c *config.Config, v bool) { c.Plan.Enabled = v }},
	{"donegate", "Verify before finishing", "Verify build/test before finishing a task", catWorkflow, true,
		func(c *config.Config) bool { return c.DoneGate.Enabled },
		func(c *config.Config, v bool) { c.DoneGate.Enabled = v }},
	{"thinking", "Always reason", "Force reasoning every turn (off = auto: reason only on hard tasks)", catWorkflow, true,
		func(c *config.Config) bool {
			return config.ResolveThinkingMode(c.Model.ThinkingMode) == config.ThinkingModeOn
		},
		func(c *config.Config, v bool) {
			// A boolean toggle maps to the two modes that matter day-to-day:
			// ON = force thinking; OFF = auto (the router/runner decide by task).
			// Full "off" (never reason) is the rare case, via /thinking off.
			if v {
				applyThinkingMode(c, config.ThinkingModeOn, 0)
			} else {
				applyThinkingMode(c, config.ThinkingModeAuto, 0)
			}
		}},

	// Files & Search — boot-wired (sessionManager / fileWatcher are nil when off,
	// the grep search-cache is wired into the tools at boot), so live=false: they
	// persist now and take effect next launch.
	{"session", "Save & resume conversations", "Save & resume conversations across restarts", catFiles, false,
		func(c *config.Config) bool { return c.Session.Enabled },
		func(c *config.Config, v bool) { c.Session.Enabled = v }},
	{"watcher", "Watch for file changes", "Detect external file changes", catFiles, false,
		func(c *config.Config) bool { return c.Watcher.Enabled },
		func(c *config.Config, v bool) { c.Watcher.Enabled = v }},
	{"searchcache", "Cache search results", "Cache grep/glob search results", catFiles, false,
		func(c *config.Config) bool { return c.Cache.Enabled },
		func(c *config.Config, v bool) { c.Cache.Enabled = v }},

	// Interface & Web
	{"tokens", "Show token usage", "Show token usage in the status bar", catInterface, true,
		func(c *config.Config) bool { return c.UI.ShowTokenUsage },
		func(c *config.Config, v bool) { c.UI.ShowTokenUsage = v }},
	{"reducedmotion", "Reduce motion", "Use static activity indicators and instant scrolling", catInterface, true,
		func(c *config.Config) bool { return c.UI.ReducedMotion },
		func(c *config.Config, v bool) { c.UI.ReducedMotion = v }},
	{"glmsearch", "GLM web search", "GLM Coding Plan web search (web_search_prime, uses your GLM key)", catInterface, false,
		func(c *config.Config) bool { return c.Web.GLMSearch },
		func(c *config.Config, v bool) { c.Web.GLMSearch = v }},
}

// ToggleState is a settable toggle's current value. Shared by /set and the
// interactive /settings modal so there is ONE source of truth for what is
// configurable — the modal renders these and reports flips back through
// ApplySettingToggle.
type ToggleState struct {
	Key      string
	Name     string // friendly display name
	Desc     string
	Category string // header text (resolved from the toggle's category)
	On       bool
	// Live mirrors settingToggle.live: false means the flip persists but applies
	// on next launch (the modal/`/set` show a "restart" hint so it's never a
	// silent no-op).
	Live bool
}

// SettableToggleStates returns every curated toggle with its current value,
// in display order (grouped by category — the slice is category-contiguous).
func SettableToggleStates(cfg *config.Config) []ToggleState {
	states := make([]ToggleState, 0, len(settableToggles))
	for _, t := range settableToggles {
		states = append(states, ToggleState{
			Key:      t.key,
			Name:     t.name,
			Desc:     t.desc,
			Category: t.cat.String(),
			On:       t.get(cfg),
			Live:     t.live,
		})
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

// settingPreset is a named bundle that flips a coherent set of LIVE safety/
// workflow toggles in one action — the "fewer decisions" surface. Only live
// toggles are included so the WHOLE preset applies immediately (never a mixed
// "some applied, some need restart" state).
type settingPreset struct {
	name string
	desc string
	vals map[string]bool // toggle key -> desired value (live toggles only)
}

var settingPresets = []settingPreset{
	{"safe", "Maximum guardrails — ask before risky actions, sandbox bash, confirm edits, verify before done",
		map[string]bool{"permissions": true, "sandbox": true, "diff": true, "donegate": true}},
	{"balanced", "Sensible default — ask before risky actions and verify before done, no sandbox/diff friction",
		map[string]bool{"permissions": true, "sandbox": false, "diff": false, "donegate": true}},
	{"fast", "Fewest interruptions — no prompts, sandbox, diff, or done-gate (use only on trusted work)",
		map[string]bool{"permissions": false, "sandbox": false, "diff": false, "donegate": false}},
}

func findPreset(name string) (settingPreset, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, p := range settingPresets {
		if p.name == name {
			return p, true
		}
	}
	return settingPreset{}, false
}

// ApplyPreset applies a named preset's toggle values to cfg (the caller then
// calls ApplyConfig once). Returns the human-readable list of ACTUAL changes
// (in display order) and ok=false for an unknown preset name. The table is
// curated to touch only live toggles, so every change takes effect immediately.
func ApplyPreset(cfg *config.Config, name string) (changed []string, ok bool) {
	p, found := findPreset(name)
	if !found {
		return nil, false
	}
	for _, t := range settableToggles {
		want, in := p.vals[t.key]
		if !in {
			continue
		}
		if t.get(cfg) != want {
			t.set(cfg, want)
			changed = append(changed, fmt.Sprintf("%s → %s", t.name, onOff(want)))
		}
	}
	return changed, true
}

// presetList renders the available presets (the /set preset discovery surface).
func presetList() string {
	var sb strings.Builder
	sb.WriteString("Quick presets (use /set preset <name>):\n\n")
	for _, p := range settingPresets {
		fmt.Fprintf(&sb, "  %-9s %s\n", p.name, p.desc)
	}
	return sb.String()
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

	// `/set preset <name>` — flip a coherent bundle in one action.
	if strings.ToLower(strings.TrimSpace(args[0])) == "preset" {
		if len(args) < 2 {
			return presetList(), nil
		}
		changed, ok := ApplyPreset(cfg, args[1])
		if !ok {
			return fmt.Sprintf("Unknown preset: %s\n\n%s", args[1], presetList()), nil
		}
		if err := app.ApplyConfig(cfg); err != nil {
			return fmt.Sprintf("Failed to apply: %v", err), nil
		}
		name := strings.ToLower(strings.TrimSpace(args[1]))
		if len(changed) == 0 {
			return fmt.Sprintf("Preset %q is already active — nothing changed.", name), nil
		}
		return fmt.Sprintf("Preset %q applied:\n  %s", name, strings.Join(changed, "\n  ")), nil
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
	if !t.live {
		return fmt.Sprintf("%s: %s — saved; restart gokin to apply", t.key, onOff(val)), nil
	}
	return fmt.Sprintf("%s: %s", t.key, onOff(val)), nil
}

func (c *SetCommand) list(cfg *config.Config) string {
	var sb strings.Builder
	sb.WriteString("Settings (use /set <key> on|off to change):\n")
	lastCat := category(-1)
	for _, t := range settableToggles {
		if t.cat != lastCat {
			fmt.Fprintf(&sb, "\n%s\n", t.cat.String())
			lastCat = t.cat
		}
		note := ""
		if !t.live {
			note = "  · restart to apply"
		}
		// Friendly name leads; the key stays visible so /set <key> is discoverable.
		fmt.Fprintf(&sb, "  %-3s  %-28s %s%s\n", onOff(t.get(cfg)), t.name, t.key, note)
	}
	sb.WriteString("\nQuick presets: /set preset safe · balanced · fast")
	sb.WriteString("\nLive changes apply immediately; \"restart to apply\" ones persist and take effect next launch.")
	sb.WriteString("\nModel/provider: /model, /provider, /login · Thinking: /thinking")
	return sb.String()
}
