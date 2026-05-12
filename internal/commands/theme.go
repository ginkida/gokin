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
	themeSetter, ok := app.(ThemeSetter)
	current := string(ui.ThemeDark)
	if ok {
		if t := themeSetter.GetTheme(); t != "" {
			current = t
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Active theme: %s — %s", current, themeDescription(ui.ThemeType(current)))
	if len(args) > 0 {
		sb.WriteString("\n\ngokin now ships a single unified theme. Arguments are ignored.")
	}
	return sb.String(), nil
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
