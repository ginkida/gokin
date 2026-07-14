package commands

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/config"
)

// fakeSetApp is a minimal AppInterface for /set: holds a config and records
// whether ApplyConfig was called.
type fakeSetApp struct {
	fakeAppForMCP
	cfg     *config.Config
	applied bool
}

func (a *fakeSetApp) GetConfig() *config.Config { return a.cfg }
func (a *fakeSetApp) ApplyConfig(cfg *config.Config) error {
	a.cfg = cfg
	a.applied = true
	return nil
}

func TestSetCommand_Toggle(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Permission.Enabled = true
	app := &fakeSetApp{cfg: cfg}
	cmd := &SetCommand{}

	// Turn permissions off — must mutate config AND apply.
	out, err := cmd.Execute(context.Background(), []string{"permissions", "off"}, app)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if app.cfg.Permission.Enabled {
		t.Error("permissions should be off after /set permissions off")
	}
	if !app.applied {
		t.Error("ApplyConfig must be called so the change takes effect live")
	}
	if !strings.Contains(out, "permissions: off") {
		t.Errorf("confirmation = %q, want 'permissions: off'", out)
	}

	// Accept aliases (true/1/enable).
	app.applied = false
	if _, err := cmd.Execute(context.Background(), []string{"sandbox", "true"}, app); err != nil {
		t.Fatalf("Execute(sandbox true) error: %v", err)
	}
	if !app.cfg.Tools.Bash.Sandbox || !app.applied {
		t.Error("sandbox true should enable + apply")
	}
}

// TestSharedToggleTable pins that /set and the /settings modal share one source
// of truth (SettableToggleStates + ApplySettingToggle over settableToggles).
func TestSharedToggleTable(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Bash.Sandbox = false

	states := SettableToggleStates(cfg)
	if len(states) != len(settableToggles) {
		t.Fatalf("SettableToggleStates len=%d, want %d", len(states), len(settableToggles))
	}

	if !ApplySettingToggle(cfg, "sandbox", true) {
		t.Fatal("ApplySettingToggle(sandbox) should succeed for a known key")
	}
	if !cfg.Tools.Bash.Sandbox {
		t.Error("sandbox should be on after ApplySettingToggle(sandbox, true)")
	}
	if ApplySettingToggle(cfg, "nope", true) {
		t.Error("ApplySettingToggle(unknown) must return false")
	}
	if !ApplySettingToggle(cfg, "reducedmotion", true) || !cfg.UI.ReducedMotion {
		t.Error("reducedmotion should be a live settable accessibility toggle")
	}

	// thinking is a boolean toggle mapping to the two day-to-day modes:
	// ON = force (mode "on"); OFF = auto (the router/runner decide by task).
	if _, ok := findToggle("thinking"); !ok {
		t.Error("thinking should be a settable toggle")
	}
	if !ApplySettingToggle(cfg, "thinking", true) ||
		config.ResolveThinkingMode(cfg.Model.ThinkingMode) != config.ThinkingModeOn || !cfg.Model.EnableThinking {
		t.Error("thinking toggle ON should set mode=on + enable + a usable budget")
	}
	if !ApplySettingToggle(cfg, "thinking", false) ||
		config.ResolveThinkingMode(cfg.Model.ThinkingMode) != config.ThinkingModeAuto {
		t.Error("thinking toggle OFF should set mode=auto (router decides), not force-off")
	}

	// /settings opens via the marker, never as displayed text.
	out, _ := (&SettingsCommand{}).Execute(context.Background(), nil, &fakeSetApp{cfg: cfg})
	if out != SettingsMarker {
		t.Errorf("/settings result = %q, want SettingsMarker", out)
	}
}

// TestSettableToggles_NewInAppSettings pins that the settings that used to need
// YAML editing are now in-app toggles, and that the live-vs-restart labeling is
// honest: a boot-wired toggle (live=false) must say "restart to apply", a
// live one must not.
func TestSettableToggles_NewInAppSettings(t *testing.T) {
	cfg := config.DefaultConfig()

	// Every new key is now a known, configurable toggle.
	for _, key := range []string{"session", "searchcache", "sessionmemory", "watcher", "glmsearch"} {
		if _, ok := findToggle(key); !ok {
			t.Errorf("%q should be a settable toggle (configurable in-app, not just YAML)", key)
		}
	}

	// Live flags reflect what ApplyConfig actually propagates this session:
	// sessionmemory is wired live; session/searchcache/watcher are boot-wired.
	wantLive := map[string]bool{
		"sessionmemory": true,
		"thinking":      true,
		"permissions":   true,
		"session":       false,
		"searchcache":   false,
		"watcher":       false,
		"glmsearch":     false, // boot-wired MCP server
	}
	live := map[string]bool{}
	for _, s := range SettableToggleStates(cfg) {
		live[s.Key] = s.Live
	}
	for key, want := range wantLive {
		if live[key] != want {
			t.Errorf("toggle %q Live=%v, want %v", key, live[key], want)
		}
	}

	// /set of a restart-required toggle persists AND tells the user to restart.
	app := &fakeSetApp{cfg: cfg}
	out, _ := (&SetCommand{}).Execute(context.Background(), []string{"watcher", "on"}, app)
	if !app.cfg.Watcher.Enabled {
		t.Error("watcher should be enabled in config after /set watcher on")
	}
	if !strings.Contains(out, "restart") {
		t.Errorf("restart-required toggle confirmation = %q, want a restart hint", out)
	}

	// A live toggle must NOT claim a restart is needed.
	out, _ = (&SetCommand{}).Execute(context.Background(), []string{"sessionmemory", "on"}, app)
	if strings.Contains(out, "restart") {
		t.Errorf("live toggle confirmation = %q, must not mention restart", out)
	}
}

func TestSetCommand_ListAndErrors(t *testing.T) {
	app := &fakeSetApp{cfg: config.DefaultConfig()}
	cmd := &SetCommand{}

	// No args lists every curated toggle.
	list, _ := cmd.Execute(context.Background(), nil, app)
	for _, key := range []string{"permissions", "sandbox", "diff", "tokens", "autocompact", "memory", "plan", "donegate"} {
		if !strings.Contains(list, key) {
			t.Errorf("/set listing is missing %q", key)
		}
	}
	if app.applied {
		t.Error("/set with no args must NOT apply anything")
	}

	// Unknown key — no apply, lists keys.
	out, _ := cmd.Execute(context.Background(), []string{"nope", "on"}, app)
	if app.applied {
		t.Error("unknown key must not apply")
	}
	if !strings.Contains(out, "Unknown setting") {
		t.Errorf("unknown-key output = %q", out)
	}

	// Invalid value — no apply.
	out, _ = cmd.Execute(context.Background(), []string{"permissions", "maybe"}, app)
	if app.applied {
		t.Error("invalid value must not apply")
	}
	if !strings.Contains(out, "Invalid value") {
		t.Errorf("invalid-value output = %q", out)
	}
}

// TestSettableToggles_NameAndCategoryGrouping pins the single-source-of-truth
// grouping: every toggle has a friendly Name + a valid Category, and the slice
// is category-CONTIGUOUS (a category never appears, ends, then reappears) so
// /set and the modal render identical groups.
func TestSettableToggles_NameAndCategoryGrouping(t *testing.T) {
	cfg := config.DefaultConfig()
	states := SettableToggleStates(cfg)
	if len(states) != len(settableToggles) {
		t.Fatalf("states len=%d, want %d", len(states), len(settableToggles))
	}
	seen := map[string]bool{}
	prevCat := ""
	for i, s := range states {
		if s.Name == "" {
			t.Errorf("toggle %q has empty Name", s.Key)
		}
		if s.Category == "" {
			t.Errorf("toggle %q has empty Category", s.Key)
		}
		if s.Category != prevCat {
			if seen[s.Category] {
				t.Errorf("category %q reappears at index %d — toggles must be category-contiguous", s.Category, i)
			}
			seen[s.Category] = true
			prevCat = s.Category
		}
	}
	// The five expected categories all appear.
	for _, want := range []string{"Safety", "Context & Memory", "Workflow", "Files & Search", "Interface & Web"} {
		if !seen[want] {
			t.Errorf("expected category %q to be present", want)
		}
	}
}

// TestApplyPreset pins the preset bundles: each flips its declared LIVE toggles
// to the right values, reports only actual changes, and rejects unknown names.
func TestApplyPreset(t *testing.T) {
	// safe = all guardrails on.
	cfg := config.DefaultConfig()
	cfg.Permission.Enabled = false
	cfg.Tools.Bash.Sandbox = false
	cfg.DiffPreview.Enabled = false
	cfg.DoneGate.Enabled = false
	changed, ok := ApplyPreset(cfg, "safe")
	if !ok {
		t.Fatal("safe should be a known preset")
	}
	if !cfg.Permission.Enabled || !cfg.Tools.Bash.Sandbox || !cfg.DiffPreview.Enabled || !cfg.DoneGate.Enabled {
		t.Errorf("safe preset did not enable all guardrails: %+v", cfg.Permission)
	}
	if len(changed) != 4 {
		t.Errorf("safe should report 4 changes, got %d: %v", len(changed), changed)
	}

	// fast = all those off.
	changed, ok = ApplyPreset(cfg, "fast")
	if !ok {
		t.Fatal("fast should be known")
	}
	if cfg.Permission.Enabled || cfg.Tools.Bash.Sandbox || cfg.DiffPreview.Enabled || cfg.DoneGate.Enabled {
		t.Error("fast preset did not disable guardrails")
	}
	if len(changed) != 4 {
		t.Errorf("fast should report 4 changes after safe, got %d", len(changed))
	}

	// Re-applying the same preset reports zero changes (idempotent).
	changed, _ = ApplyPreset(cfg, "fast")
	if len(changed) != 0 {
		t.Errorf("re-applying fast should be a no-op, got %v", changed)
	}

	// Unknown preset.
	if _, ok := ApplyPreset(cfg, "nope"); ok {
		t.Error("unknown preset should return ok=false")
	}

	// balanced = ask + verify, no sandbox/diff.
	ApplyPreset(cfg, "balanced")
	if !cfg.Permission.Enabled || !cfg.DoneGate.Enabled || cfg.Tools.Bash.Sandbox || cfg.DiffPreview.Enabled {
		t.Errorf("balanced preset wrong: perm=%v done=%v sandbox=%v diff=%v",
			cfg.Permission.Enabled, cfg.DoneGate.Enabled, cfg.Tools.Bash.Sandbox, cfg.DiffPreview.Enabled)
	}

	// Every preset touches only LIVE toggles (so the whole thing applies now).
	for _, p := range settingPresets {
		for key := range p.vals {
			tg, found := findToggle(key)
			if !found {
				t.Errorf("preset %q references unknown toggle %q", p.name, key)
				continue
			}
			if !tg.live {
				t.Errorf("preset %q includes boot-wired toggle %q (must be live-only)", p.name, key)
			}
		}
	}
}
