package ui

import (
	"github.com/charmbracelet/lipgloss"
)

// ThemeType represents a UI theme identifier.
//
// gokin ships with a single unified theme (Graphite + violet). The type and
// the lone ThemeDark constant are retained so the /theme command and config
// `ui.theme` field stay stable for users who already have settings on disk.
type ThemeType string

const (
	ThemeDark ThemeType = "dark" // Graphite + violet (only theme shipped)
)

// ThemeColorScheme defines the color palette for a theme.
type ThemeColorScheme struct {
	Name       string
	Primary    lipgloss.Color
	Secondary  lipgloss.Color
	Success    lipgloss.Color
	Warning    lipgloss.Color
	Error      lipgloss.Color
	Muted      lipgloss.Color
	Text       lipgloss.Color
	Background lipgloss.Color
	Border     lipgloss.Color
	Highlight  lipgloss.Color
	Accent     lipgloss.Color
	Info       lipgloss.Color

	// Extended semantic colors (optional — zero value means "keep default")
	Dim     lipgloss.Color
	Running lipgloss.Color
	Context lipgloss.Color
}

// predefinedThemes returns all available theme color schemes.
//
// As of the design refresh, gokin ships a single unified theme — the map
// shape is kept so GetTheme / GetAvailableThemes / ApplyTheme signatures
// stay stable for callers (the /theme command + tests).
func predefinedThemes() map[ThemeType]ThemeColorScheme {
	return map[ThemeType]ThemeColorScheme{
		ThemeDark: {
			Name:       "Graphite",
			Primary:    lipgloss.Color("#9B7BFF"), // Violet (mockup #7c4dff lifted for dark bg)
			Secondary:  lipgloss.Color("#5BA8C7"), // Calm Cyan
			Success:    lipgloss.Color("#5AB97B"), // Forest Green
			Warning:    lipgloss.Color("#D4A24A"), // Deep Amber
			Error:      lipgloss.Color("#D85A4A"), // Coral
			Muted:      lipgloss.Color("#807D75"), // Warm Gray
			Text:       lipgloss.Color("#E8E4D8"), // Warm Off-White
			Background: lipgloss.Color("#0E1116"), // Warm Graphite
			Border:     lipgloss.Color("#2A2C33"), // Subtle Border
			Highlight:  lipgloss.Color("#C0AEFF"), // Lavender
			Accent:     lipgloss.Color("#9B7BFF"), // Unified with Primary (single violet)
			Info:       lipgloss.Color("#6BAEB5"), // Calm Teal
			Dim:        lipgloss.Color("#5A5852"), // Deeper Warm Gray
			Running:    lipgloss.Color("#6B8AD4"), // Info Blue
			Context:    lipgloss.Color("#807D75"), // = Muted
		},
	}
}

// GetTheme returns the color scheme for a given theme type.
func GetTheme(themeType ThemeType) ThemeColorScheme {
	themes := predefinedThemes()
	if theme, ok := themes[themeType]; ok {
		return theme
	}
	return themes[ThemeDark] // Default to dark theme
}

// ApplyTheme applies a theme to the Styles struct.
func (s *Styles) ApplyTheme(theme ThemeType) {
	colors := GetTheme(theme)

	// Update all color constants
	ColorPrimary = colors.Primary
	ColorSecondary = colors.Secondary
	ColorSuccess = colors.Success
	ColorWarning = colors.Warning
	ColorError = colors.Error
	ColorMuted = colors.Muted
	ColorText = colors.Text
	ColorBg = colors.Background
	ColorBorder = colors.Border
	ColorHighlight = colors.Highlight
	ColorAccent = colors.Accent
	ColorInfo = colors.Info

	// Update extended semantic colors if provided by the theme
	if colors.Dim != "" {
		ColorDim = colors.Dim
	}
	if colors.Running != "" {
		ColorRunning = colors.Running
	}
	if colors.Context != "" {
		ColorContext = colors.Context
	}

	// Rebuild styles with new colors
	s.rebuildStyles()
}

// rebuildStyles rebuilds all styles with the current color constants.
func (s *Styles) rebuildStyles() {
	*s = *DefaultStyles()
}

// GetAvailableThemes returns a list of all available theme names and their IDs.
func GetAvailableThemes() []struct {
	ID   ThemeType
	Name string
} {
	themes := predefinedThemes()
	var result []struct {
		ID   ThemeType
		Name string
	}

	for id, theme := range themes {
		result = append(result, struct {
			ID   ThemeType
			Name string
		}{
			ID:   id,
			Name: theme.Name,
		})
	}

	return result
}
