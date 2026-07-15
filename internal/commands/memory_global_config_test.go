package commands

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/config"
)

func TestGlobalMemorySettingIsDiscoverableAndLive(t *testing.T) {
	cfg := config.DefaultConfig()
	app := &fakeSetApp{cfg: cfg}

	configOutput, err := (&ConfigCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Memory",
		"Global (cross-project): false",
		"/set globalmemory on|off",
	} {
		if !strings.Contains(configOutput, want) {
			t.Fatalf("/config output missing %q:\n%s", want, configOutput)
		}
	}

	setOutput, err := (&SetCommand{}).Execute(context.Background(), []string{"globalmemory", "on"}, app)
	if err != nil {
		t.Fatal(err)
	}
	if !app.applied || !app.cfg.Memory.AllowGlobal {
		t.Fatalf("globalmemory toggle did not apply live: applied=%v allow=%v", app.applied, app.cfg.Memory.AllowGlobal)
	}
	if strings.Contains(setOutput, "restart") || !strings.Contains(setOutput, "globalmemory: on") {
		t.Fatalf("globalmemory toggle confirmation is not live/honest: %q", setOutput)
	}

	found := false
	for _, state := range SettableToggleStates(app.cfg) {
		if state.Key != "globalmemory" {
			continue
		}
		found = true
		if !state.Live || !state.On || !strings.Contains(strings.ToLower(state.Desc), "user-wide") {
			t.Fatalf("globalmemory state is incomplete: %+v", state)
		}
	}
	if !found {
		t.Fatal("globalmemory missing from /set and /settings shared toggle table")
	}
}
