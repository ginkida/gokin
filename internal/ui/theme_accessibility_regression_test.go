package ui

import (
	"io"
	"math"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestGraphiteThemeOwnsBackgroundAndMeetsTextContrastFloor(t *testing.T) {
	theme := GetTheme(ThemeDark)
	semanticText := map[string]lipgloss.Color{
		"primary":   theme.Primary,
		"secondary": theme.Secondary,
		"success":   theme.Success,
		"warning":   theme.Warning,
		"error":     theme.Error,
		"muted":     theme.Muted,
		"text":      theme.Text,
		"highlight": theme.Highlight,
		"accent":    theme.Accent,
		"info":      theme.Info,
		"dim":       theme.Dim,
		"running":   theme.Running,
		"context":   theme.Context,
	}
	for name, color := range semanticText {
		if ratio := themeContrastRatio(t, color, theme.Background); ratio < 4.5 {
			t.Errorf("%s contrast %.2f:1 on Graphite, want >=4.5:1", name, ratio)
		}
	}

	styles := DefaultStyles()
	if got := styles.App.GetBackground(); got != ColorBg {
		t.Fatalf("Graphite root background=%v, want %v so light terminals cannot override the palette", got, ColorBg)
	}
	if got := styles.Input.GetBorderTopForeground(); got != ColorDim {
		t.Fatalf("composer border=%v, want accessible neutral %v", got, ColorDim)
	}
	if ratio := themeContrastRatio(t, ColorDim, ColorBg); ratio < 3 {
		t.Fatalf("composer boundary contrast %.2f:1, want >=3:1", ratio)
	}
	if ratio := themeContrastRatio(t, ColorText, ColorBorder); ratio < 4.5 {
		t.Fatalf("shortcut keycap contrast %.2f:1, want >=4.5:1", ratio)
	}
	if ratio := themeContrastRatio(t, ColorMuted, ColorBorder); ratio < 4.5 {
		t.Fatalf("disabled selected text contrast %.2f:1, want >=4.5:1", ratio)
	}
}

func TestGraphiteRootBackgroundSurvivesNestedANSIReset(t *testing.T) {
	originalRenderer := lipgloss.DefaultRenderer()
	renderer := lipgloss.NewRenderer(io.Discard)
	renderer.SetColorProfile(termenv.TrueColor)
	lipgloss.SetDefaultRenderer(renderer)
	t.Cleanup(func() { lipgloss.SetDefaultRenderer(originalRenderer) })

	styles := DefaultStyles()
	inner := renderer.NewStyle().Foreground(ColorPrimary).Render("b")
	got := styles.App.Render("a" + inner + "c")
	backgroundPrefix := ansiStylePrefix(t, renderer.NewStyle().Background(ColorBg), "x")

	if !strings.Contains(got, "\x1b[0m"+backgroundPrefix+"c") {
		t.Fatalf("root background is not reapplied after nested reset before trailing text: %q", got)
	}
	if gotWidth := lipgloss.Width(got); gotWidth != 3 {
		t.Fatalf("persistent root background changed visible width to %d, want 3", gotWidth)
	}
}

func TestGraphiteRootBackgroundEmitsNoANSIWithoutColor(t *testing.T) {
	originalRenderer := lipgloss.DefaultRenderer()
	renderer := lipgloss.NewRenderer(io.Discard)
	renderer.SetColorProfile(termenv.Ascii)
	lipgloss.SetDefaultRenderer(renderer)
	t.Cleanup(func() { lipgloss.SetDefaultRenderer(originalRenderer) })

	styles := DefaultStyles()
	inner := renderer.NewStyle().Foreground(ColorPrimary).Render("b")
	if got := styles.App.Render("a" + inner + "c"); got != "abc" {
		t.Fatalf("no-color root render=%q, want plain text without ANSI", got)
	}
}

func TestSemanticThemeGlyphsAreSafeSingleCellSignals(t *testing.T) {
	groups := map[string]map[string]string{
		"message": MessageIcons,
		"status":  StatusIcons,
		"tool":    ToolIcons,
	}
	toast := make(map[string]string, len(ToastIcons))
	for kind, glyph := range ToastIcons {
		toast[strconv.Itoa(int(kind))] = glyph
	}
	groups["toast"] = toast

	for group, glyphs := range groups {
		for name, glyph := range glyphs {
			if got := lipgloss.Width(glyph); got != 1 {
				t.Errorf("%s %q glyph %q occupies %d cells, want exactly 1", group, name, glyph, got)
			}
			if strings.IndexFunc(glyph, unicode.IsControl) >= 0 || strings.Contains(glyph, "\x1b") {
				t.Errorf("%s %q glyph contains terminal controls: %q", group, name, glyph)
			}
		}
	}

	// Core message states must remain distinguishable without relying on color.
	seen := make(map[string]string)
	for _, state := range []string{"success", "error", "warning", "info", "pending", "active"} {
		glyph := MessageIcons[state]
		if prior, exists := seen[glyph]; exists {
			t.Errorf("message states %q and %q share glyph %q and differ only by color", prior, state, glyph)
		}
		seen[glyph] = state
	}
}

func themeContrastRatio(t *testing.T, foreground, background lipgloss.Color) float64 {
	t.Helper()
	fg := themeRelativeLuminance(t, foreground)
	bg := themeRelativeLuminance(t, background)
	return (math.Max(fg, bg) + 0.05) / (math.Min(fg, bg) + 0.05)
}

func themeRelativeLuminance(t *testing.T, color lipgloss.Color) float64 {
	t.Helper()
	hex := strings.TrimPrefix(string(color), "#")
	if len(hex) != 6 {
		t.Fatalf("theme color %q is not six-digit RGB", color)
	}
	channels := make([]float64, 3)
	for i := range channels {
		value, err := strconv.ParseUint(hex[i*2:i*2+2], 16, 8)
		if err != nil {
			t.Fatalf("parse theme color %q: %v", color, err)
		}
		channel := float64(value) / 255
		if channel <= 0.04045 {
			channels[i] = channel / 12.92
		} else {
			channels[i] = math.Pow((channel+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*channels[0] + 0.7152*channels[1] + 0.0722*channels[2]
}

func ansiStylePrefix(t *testing.T, style lipgloss.Style, marker string) string {
	t.Helper()
	rendered := style.Render(marker)
	index := strings.Index(rendered, marker)
	if index <= 0 {
		t.Fatalf("style rendered without an ANSI prefix: %q", rendered)
	}
	return rendered[:index]
}
