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
