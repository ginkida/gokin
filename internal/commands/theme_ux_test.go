package commands

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/config"
	"gokin/internal/ui"
)

type fakeThemeApp struct {
	fakeAppForMCP
	active string
}

func (f *fakeThemeApp) GetTheme() string { return f.active }
func (f *fakeThemeApp) SetTheme(theme ui.ThemeType) {
	f.active = string(theme)
}

func TestThemeSurfacesDistinguishActiveFromLegacyConfiguredValue(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.UI.Theme = "light"
	app := &fakeThemeApp{
		fakeAppForMCP: fakeAppForMCP{cfg: cfg},
		active:        "light", // unsupported ThemeSetter output must not fake availability
	}

	themeOutput, err := (&ThemeCommand{}).Execute(context.Background(), []string{"light"}, app)
	if err != nil {
		t.Fatalf("theme Execute: %v", err)
	}
	for _, want := range []string{
		"Active theme: dark",
		"Graphite + violet",
		"Configured ui.theme: light (legacy/unsupported; not applied)",
		"No setting changed",
	} {
		if !strings.Contains(themeOutput, want) {
			t.Fatalf("theme output missing %q:\n%s", want, themeOutput)
		}
	}
	if strings.Contains(themeOutput, "Active theme: light") {
		t.Fatalf("legacy value was presented as active:\n%s", themeOutput)
	}

	configOutput, err := (&ConfigCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("config Execute: %v", err)
	}
	if !strings.Contains(configOutput, "Theme:  dark — Graphite + violet") ||
		!strings.Contains(configOutput, "legacy/unsupported and is not applied") {
		t.Fatalf("config output hides active/configured distinction:\n%s", configOutput)
	}
	if strings.Contains(configOutput, "Theme:  light") {
		t.Fatalf("config output presented legacy theme as active:\n%s", configOutput)
	}
}

func TestConfiguredThemeValueCannotForgeTerminalRows(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.UI.Theme = "light\nFORGED\tvalue"
	app := &fakeThemeApp{fakeAppForMCP: fakeAppForMCP{cfg: cfg}}

	output, err := (&ThemeCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(output, "\nFORGED") || !strings.Contains(output, "light forged value") {
		t.Fatalf("configured theme was not rendered safely:\n%s", output)
	}
}
