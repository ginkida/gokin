package ui

import (
	"github.com/charmbracelet/lipgloss"
)

// ThemeType represents different UI themes.
type ThemeType string

const (
	ThemeDark  ThemeType = "dark"  // Default dark theme (soft purple/cyan)
	ThemeMacOS ThemeType = "macos" // Apple-inspired theme
	ThemeLight ThemeType = "light" // Light theme for light terminal backgrounds
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
		ThemeMacOS: {
			Name:       "Apple (MacOS)",
			Primary:    lipgloss.Color("#007AFF"), // SF Blue
			Secondary:  lipgloss.Color("#5856D6"), // SF Purple
			Success:    lipgloss.Color("#34C759"), // SF Green
			Warning:    lipgloss.Color("#FF9500"), // SF Orange
			Error:      lipgloss.Color("#FF3B30"), // SF Red
			Muted:      lipgloss.Color("#8E8E93"), // SF Gray
			Text:       lipgloss.Color("#FFFFFF"), // White
			Background: lipgloss.Color("#1C1C1E"), // Dark Mode Gray
			Border:     lipgloss.Color("#3A3A3C"), // Separator Gray
			Highlight:  lipgloss.Color("#64D2FF"), // SF Sky
			Accent:     lipgloss.Color("#FF2D55"), // SF Pink
			Info:       lipgloss.Color("#00C7BE"), // SF Mint
		},
		ThemeLight: {
			Name:       "Light",
			Primary:    lipgloss.Color("#7C3AED"), // Purple 600
			Secondary:  lipgloss.Color("#0891B2"), // Cyan 600
			Success:    lipgloss.Color("#059669"), // Emerald 600
			Warning:    lipgloss.Color("#D97706"), // Amber 600
			Error:      lipgloss.Color("#DC2626"), // Red 600
			Muted:      lipgloss.Color("#6B7280"), // Gray 500
			Text:       lipgloss.Color("#1E293B"), // Slate 800
			Background: lipgloss.Color("#F8FAFC"), // Slate 50
			Border:     lipgloss.Color("#CBD5E1"), // Slate 300
			Highlight:  lipgloss.Color("#7C3AED"), // Purple 600
			Accent:     lipgloss.Color("#DB2777"), // Pink 600
			Info:       lipgloss.Color("#0D9488"), // Teal 600
			Dim:        lipgloss.Color("#9CA3AF"), // Gray 400
			Running:    lipgloss.Color("#2563EB"), // Blue 600
			Context:    lipgloss.Color("#475569"), // Slate 600
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
