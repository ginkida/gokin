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

	// thinking folded in as a boolean toggle (it's effectively on/off + budget).
	if _, ok := findToggle("thinking"); !ok {
		t.Error("thinking should be a settable toggle")
	}
	cfg.Model.EnableThinking = false
	cfg.Model.ThinkingBudget = 0
	if !ApplySettingToggle(cfg, "thinking", false) || cfg.Model.ThinkingBudget == 0 {
		t.Error("thinking off should seed a non-zero budget so it doesn't auto-re-enable at startup")
	}
	if !ApplySettingToggle(cfg, "thinking", true) || !cfg.Model.EnableThinking {
		t.Error("thinking on should enable + clamp a usable budget")
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
	for _, key := range []string{"session", "searchcache", "sessionmemory", "watcher"} {
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
