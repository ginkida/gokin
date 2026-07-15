package ui

import (
	"strings"
	"testing"
)

func TestSingleItemSelectorsDoNotAdvertiseDeadNavigation(t *testing.T) {
	model := NewModel()
	model.width, model.height = 80, 24
	model.state = StateModelSelector
	model.availableModels = []ModelInfo{{ID: "fast", Name: "Fast"}}
	model.onModelSelect = func(string) {}
	assertShortcutHints(t, model,
		[]string{"Enter Select", "esc Close"},
		[]string{"↑↓ Navigate", "↑↓ Inspect"},
	)
	if view := stripAnsi(model.renderModelSelector()); strings.Contains(view, "↑/↓ Navigate") {
		t.Fatalf("single-model footer advertises dead navigation:\n%s", view)
	}

	settings := NewModel()
	settings.width, settings.height = 80, 24
	settings.state = StateSettings
	settings.settingsItems = []SettingItem{{Key: "tokens", Name: "Tokens"}}
	settings.onSettingToggle = func(string, bool) {}
	assertShortcutHints(t, settings,
		[]string{"Space Toggle", "m Model", "esc Close"},
		[]string{"↑↓ Navigate", "↑↓ Inspect"},
	)
	if view := stripAnsi(settings.renderSettings()); strings.Contains(view, "↑/↓ Navigate") {
		t.Fatalf("single-setting footer advertises dead navigation:\n%s", view)
	}

}

func TestNotificationAndPaletteHintsFollowListCardinality(t *testing.T) {
	notifications := NewModel()
	notifications.width, notifications.height = 80, 24
	notifications.toastManager.ShowInfo("Only notification")
	notifications.openNotificationCenter()
	assertShortcutHints(t, notifications,
		[]string{"Enter Details", "esc Back"},
		[]string{"↑↓ Select"},
	)
	if view := stripAnsi(notifications.renderNotificationCenter()); strings.Contains(view, "↑/↓ Select") || strings.Contains(view, "↑↓ Select") {
		t.Fatalf("single-notification footer advertises dead selection:\n%s", view)
	}

	palette := NewModel()
	palette.width, palette.height = 80, 24
	palette.state = StateCommandPalette
	palette.commandPalette.visible = true
	palette.commandPalette.commands = []EnhancedPaletteCommand{{Name: "settings", Shortcut: "Ctrl+S", Enabled: true, Type: CommandTypeAction}}
	palette.commandPalette.SetQuery("")
	assertShortcutHints(t, palette,
		[]string{"Enter Run", "Tab Details", "esc Close"},
		[]string{"↑↓ Navigate"},
	)
	if view := stripAnsi(palette.commandPalette.View(80, 24)); strings.Contains(view, "↑/↓ Navigate") {
		t.Fatalf("single-command footer advertises dead navigation:\n%s", view)
	}

	palette.commandPalette.commands = append(palette.commandPalette.commands,
		EnhancedPaletteCommand{Name: "notifications", Shortcut: "Alt+N", Enabled: true, Type: CommandTypeAction})
	palette.commandPalette.SetQuery("")
	assertShortcutHints(t, palette, []string{"↑↓ Navigate"}, nil)
}

func TestShortcutHintsHideBrowseForNoMatchOrFullyVisibleResult(t *testing.T) {
	m := NewModel()
	m.width, m.height = 80, 30
	m.state = StateShortcutsOverlay
	m.shortcutsOverlay.Show()
	m.shortcutsOverlay.SetSearch("definitely-no-such-shortcut")
	_ = m.shortcutsOverlay.View(m.width, m.height)
	assertShortcutHints(t, m,
		[]string{"Type Filter", "Backspace Edit", "esc Clear filter"},
		[]string{"↑↓ Browse"},
	)

	m.shortcutsOverlay.SetSearch("Open model selector")
	_ = m.shortcutsOverlay.View(m.width, m.height)
	assertShortcutHints(t, m,
		[]string{"Type Filter", "Backspace Edit", "esc Clear filter"},
		[]string{"↑↓ Browse"},
	)
}
