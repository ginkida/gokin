package commands

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"gokin/internal/config"
	"gokin/internal/ui"
)

func TestTimeoutCommandStatusAndLiveApply(t *testing.T) {
	cfg := config.DefaultConfig()
	app := &fakeSetApp{cfg: cfg}
	cmd := &TimeoutCommand{}

	status, err := cmd.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Model round timeout: 14m0s", "foreground and sub-agents", "shorter explicit quick-agent, plan, coordinate, loop, or headless deadline", "/timeout 20m"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status missing %q:\n%s", want, status)
		}
	}

	out, err := cmd.Execute(context.Background(), []string{"20m"}, app)
	if err != nil {
		t.Fatal(err)
	}
	if !app.applied || cfg.Tools.ModelRoundTimeout != 20*time.Minute {
		t.Fatalf("timeout not applied live: applied=%v timeout=%v", app.applied, cfg.Tools.ModelRoundTimeout)
	}
	if !strings.Contains(out, "saved and applied") || !strings.Contains(out, "20m0s") {
		t.Fatalf("apply confirmation is unclear: %q", out)
	}

	app.applied = false
	out, err = cmd.Execute(context.Background(), []string{"default"}, app)
	if err != nil {
		t.Fatal(err)
	}
	if !app.applied || cfg.Tools.ModelRoundTimeout != config.DefaultModelRoundTimeout {
		t.Fatalf("default not restored: applied=%v timeout=%v", app.applied, cfg.Tools.ModelRoundTimeout)
	}
	if !strings.Contains(out, config.DefaultModelRoundTimeout.String()) {
		t.Fatalf("default confirmation missing effective value: %q", out)
	}
}

func TestTimeoutCommandAcceptsResetAndZeroAliases(t *testing.T) {
	for _, alias := range []string{"reset", "0"} {
		t.Run(alias, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Tools.ModelRoundTimeout = 30 * time.Minute
			app := &fakeSetApp{cfg: cfg}
			if _, err := (&TimeoutCommand{}).Execute(context.Background(), []string{alias}, app); err != nil {
				t.Fatal(err)
			}
			if !app.applied || cfg.Tools.ModelRoundTimeout != config.DefaultModelRoundTimeout {
				t.Fatalf("alias %q did not restore default: applied=%v timeout=%v", alias, app.applied, cfg.Tools.ModelRoundTimeout)
			}
		})
	}
}

func TestTimeoutCommandRejectsUnsafeOrMalformedValues(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"30s"}, want: "minimum 1m0s"},
		{args: []string{"3h"}, want: "maximum 2h0m0s"},
		{args: []string{"20"}, want: "include a unit"},
		{args: []string{"forever"}, want: "Invalid duration"},
		{args: []string{"20m", "extra"}, want: "Expected one duration"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, "_"), func(t *testing.T) {
			cfg := config.DefaultConfig()
			before := cfg.Tools.ModelRoundTimeout
			app := &fakeSetApp{cfg: cfg}
			out, err := (&TimeoutCommand{}).Execute(context.Background(), tt.args, app)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, tt.want) {
				t.Fatalf("output %q missing %q", out, tt.want)
			}
			if app.applied || cfg.Tools.ModelRoundTimeout != before {
				t.Fatalf("invalid input mutated config: applied=%v timeout=%v", app.applied, cfg.Tools.ModelRoundTimeout)
			}
		})
	}
}

type failingTimeoutApp struct {
	fakeSetApp
	err error
}

func (a *failingTimeoutApp) ApplyConfig(*config.Config) error { return a.err }

type narrowTimeoutApp struct {
	fakeSetApp
	narrowApplied bool
	fullApplied   bool
}

func (a *narrowTimeoutApp) ApplyModelRoundTimeout(cfg *config.Config) (bool, error) {
	a.cfg = cfg
	a.narrowApplied = true
	return true, nil
}

type sessionOnlyTimeoutApp struct{ fakeSetApp }

func (a *sessionOnlyTimeoutApp) ApplyModelRoundTimeout(cfg *config.Config) (bool, error) {
	a.cfg = cfg
	return false, nil
}

func TestTimeoutCommandReportsSessionOnlyApplyHonestly(t *testing.T) {
	app := &sessionOnlyTimeoutApp{fakeSetApp: fakeSetApp{cfg: config.DefaultConfig()}}
	out, err := (&TimeoutCommand{}).Execute(context.Background(), []string{"20m"}, app)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"applied for this session", "NOT saved", "revert next launch"} {
		if !strings.Contains(out, want) {
			t.Fatalf("session-only result missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "✓") || strings.Contains(out, "saved and applied") {
		t.Fatalf("session-only result claimed durable success: %q", out)
	}
}

func (a *narrowTimeoutApp) ApplyConfig(*config.Config) error {
	a.fullApplied = true
	return nil
}

func TestTimeoutCommandPrefersNarrowApplyPath(t *testing.T) {
	app := &narrowTimeoutApp{fakeSetApp: fakeSetApp{cfg: config.DefaultConfig()}}
	out, err := (&TimeoutCommand{}).Execute(context.Background(), []string{"20m"}, app)
	if err != nil {
		t.Fatal(err)
	}
	if !app.narrowApplied || app.fullApplied {
		t.Fatalf("narrow apply=%v full apply=%v, want true/false", app.narrowApplied, app.fullApplied)
	}
	if !strings.Contains(out, "saved and applied") {
		t.Fatalf("narrow apply confirmation = %q", out)
	}
}

func TestTimeoutCommandReportsApplyFailure(t *testing.T) {
	app := &failingTimeoutApp{
		fakeSetApp: fakeSetApp{cfg: config.DefaultConfig()},
		err:        errors.New("disk full"),
	}
	out, err := (&TimeoutCommand{}).Execute(context.Background(), []string{"20m"}, app)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Failed to apply") || !strings.Contains(out, "disk full") || strings.Contains(out, "✓") {
		t.Fatalf("apply failure was misreported: %q", out)
	}
}

func TestTimeoutCommandAutocompleteAndMetadata(t *testing.T) {
	var info *ui.CommandInfo
	for _, command := range ui.DefaultCommands() {
		if command.Name == "timeout" {
			copy := command
			info = &copy
			break
		}
	}
	if info == nil || len(info.Args) != 1 {
		t.Fatalf("timeout autocomplete missing: %+v", info)
	}
	want := []string{"14m", "20m", "30m", "default"}
	if !reflect.DeepEqual(info.Args[0].Options, want) {
		t.Fatalf("timeout suggestions = %v, want %v", info.Args[0].Options, want)
	}
	meta := (&TimeoutCommand{}).GetMetadata()
	if !meta.HasArgs || meta.Category != CategoryTools || !strings.Contains(meta.ArgHint, "duration") {
		t.Fatalf("timeout metadata = %+v", meta)
	}
}

func TestEffectiveModelRoundTimeoutFallsBackForNilAndZero(t *testing.T) {
	if got := effectiveModelRoundTimeout(nil); got != config.DefaultModelRoundTimeout {
		t.Fatalf("nil config effective timeout = %v", got)
	}
	if got := effectiveModelRoundTimeout(&config.Config{}); got != config.DefaultModelRoundTimeout {
		t.Fatalf("zero config effective timeout = %v", got)
	}
}
