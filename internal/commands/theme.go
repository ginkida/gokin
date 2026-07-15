package commands

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/ui"
)

// ThemeCommand reports the active UI theme.
//
// As of the design refresh, gokin ships a single unified theme (Graphite +
// violet). The command no longer accepts a theme name argument — it exists
// only as a discoverable way to see what's active and confirm the theme
// system is healthy.
type ThemeCommand struct{}

func (c *ThemeCommand) Name() string        { return "theme" }
func (c *ThemeCommand) Description() string { return "Show the active UI theme" }
func (c *ThemeCommand) Usage() string       { return "/theme" }
func (c *ThemeCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "theme",
		Priority: 40,
		HasArgs:  false,
	}
}

func (c *ThemeCommand) Execute(_ context.Context, args []string, app AppInterface) (string, error) {
	current := activeThemeID(app)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Active theme: %s — %s", current, themeDescription(current))
	if configured := configuredThemeValue(app); configured != "" && configured != string(current) {
		fmt.Fprintf(&sb, "\nConfigured ui.theme: %s (legacy/unsupported; not applied)", configured)
	}
	if len(args) > 0 {
		sb.WriteString("\n\nNo setting changed: gokin currently ships one unified theme.")
	}
	return sb.String(), nil
}

func activeThemeID(app AppInterface) ui.ThemeType {
	if setter, ok := app.(ThemeSetter); ok {
		requested := ui.ThemeType(strings.ToLower(strings.TrimSpace(setter.GetTheme())))
		for _, available := range ui.GetAvailableThemes() {
			if requested == available.ID {
				return requested
			}
		}
	}
	return ui.ThemeDark
}

func configuredThemeValue(app AppInterface) string {
	if app == nil || app.GetConfig() == nil {
		return ""
	}
	// Config is user-controlled text rendered into the terminal. Collapse
	// controls/newlines and cap it so a stale value cannot forge output rows.
	value := strings.Join(strings.Fields(app.GetConfig().UI.Theme), " ")
	if len([]rune(value)) > 40 {
		value = string([]rune(value)[:37]) + "..."
	}
	return strings.ToLower(value)
}

// themeDescription returns a short human-readable description for a theme ID.
func themeDescription(theme ui.ThemeType) string {
	if theme == ui.ThemeDark {
		return "Graphite + violet (warm graphite background, single violet accent)"
	}
	return "(custom)"
}

// ThemeSetter is the optional contract an App may implement so the /theme
// command can read the currently-active theme. SetTheme is kept on the
// interface for forward compatibility but is no longer invoked by the
// command (single theme — nothing to switch to).
type ThemeSetter interface {
	GetTheme() string
	SetTheme(theme ui.ThemeType)
}

// ConfigSetter defines the interface for saving config changes.
type ConfigSetter interface {
	SetConfigValue(key, value string) error
}
