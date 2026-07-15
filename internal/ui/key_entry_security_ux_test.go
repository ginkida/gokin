package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestKeyEntryRejectsWhitespaceWithoutLeakingSecret(t *testing.T) {
	m := NewModel()
	m.width = 80
	called := false
	m.SetKeyEntrySubmitCallback(func(string, string) { called = true })
	m.openKeyEntry(OpenKeyEntryMsg{Provider: "glm", DisplayName: "GLM"})
	secret := "sk-private accidentally-commented"
	m.keyEntryInput.SetValue(secret)

	_ = m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if called || m.state != StateAPIKeyEntry {
		t.Fatalf("invalid key escaped modal: callback=%v state=%v", called, m.state)
	}
	view := m.renderKeyEntry()
	if strings.Contains(view, secret) || strings.Contains(stripAnsi(view), secret) {
		t.Fatal("raw invalid secret leaked into validation render")
	}
	if got := stripAnsi(view); !strings.Contains(got, "contains whitespace") {
		t.Fatalf("actionable whitespace error missing:\n%s", got)
	}
}

func TestKeyEntryNormalizesWrappingQuotesBeforeSubmit(t *testing.T) {
	m := NewModel()
	m.width = 80
	var got string
	m.SetKeyEntrySubmitCallback(func(_, key string) { got = key })
	m.openKeyEntry(OpenKeyEntryMsg{Provider: "glm"})
	m.keyEntryInput.SetValue("  'sk-valid-value'  ")

	_ = m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if got != "sk-valid-value" {
		t.Fatalf("submitted key=%q want normalized key", got)
	}
	if m.keyEntryInput.Value() != "" || m.keyEntryProvider != "" || m.keyEntrySetupURL != "" {
		t.Fatal("submitted secret or metadata remained reachable")
	}
}

func TestKeyEntryUnavailableNeverCapturesTypedSecret(t *testing.T) {
	for _, provider := range []string{"", "bad provider", "bad\nprovider"} {
		m := NewModel()
		m.width = 80
		// A callback is present so this specifically exercises provider validity.
		m.SetKeyEntrySubmitCallback(func(string, string) { t.Fatal("unavailable entry submitted") })
		m.openKeyEntry(OpenKeyEntryMsg{Provider: provider})
		_ = m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sk-must-not-be-captured")})
		if m.keyEntryInput.Value() != "" || m.keyEntryInput.Focused() {
			t.Fatalf("provider=%q captured/focused secret input", provider)
		}
		view := stripAnsi(m.renderKeyEntry())
		if !strings.Contains(view, "Login is unavailable") || !strings.Contains(view, "Esc Close") {
			t.Fatalf("provider=%q missing disabled state:\n%s", provider, view)
		}
	}
}

func TestKeyEntryMetadataCannotInjectTerminalControls(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.SetKeyEntrySubmitCallback(func(string, string) {})
	m.openKeyEntry(OpenKeyEntryMsg{
		Provider:    "glm",
		DisplayName: "\x1b[31mEvil\x1b[0m\nProvider",
		SetupURL:    "https://example.test/key\nINJECTED",
	})
	plain := stripAnsi(m.renderKeyEntry())
	if strings.Contains(plain, "\x1b") || !strings.Contains(plain, "Evil Provider") || !strings.Contains(plain, "key INJECTED") {
		t.Fatalf("metadata was not safely flattened:\n%s", plain)
	}
}

func TestKeyEntryCompactFrameKeepsSafetyAndActionsVisible(t *testing.T) {
	m := NewModel()
	m.SetKeyEntrySubmitCallback(func(string, string) {})
	m.applyResize(&tea.WindowSizeMsg{Width: 72, Height: 12})
	m.openKeyEntry(OpenKeyEntryMsg{
		Provider:    "glm",
		DisplayName: strings.Repeat("Very Long Provider ", 8),
		SetupURL:    strings.Repeat("https://example.test/account/api-keys/", 8),
	})
	m.keyEntryInput.SetValue("sk-hidden-secret")

	view := m.View()
	plain := stripAnsi(view)
	for _, want := range []string{"Set API key", "masked and never sent", "Enter Save", "Esc Cancel"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("compact key frame clipped %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "sk-hidden-secret") {
		t.Fatal("compact frame leaked raw key")
	}
	if got := lipgloss.Height(view); got != 12 {
		t.Fatalf("frame height=%d want 12", got)
	}
}

func TestKeyEntryPromptFitsHeightWithoutFrameCropping(t *testing.T) {
	for _, height := range []int{10, 12, 16, 24} {
		for _, available := range []bool{false, true} {
			m := NewModel()
			m.width = 72
			m.height = height
			if available {
				m.SetKeyEntrySubmitCallback(func(string, string) {})
			}
			m.openKeyEntry(OpenKeyEntryMsg{
				Provider:    "glm",
				DisplayName: strings.Repeat("Very Long Provider ", 8),
				SetupURL:    strings.Repeat("https://example.test/account/api-keys/", 8),
			})
			m.keyEntryError = "API key contains whitespace — paste only the key"
			m.keyEntryInput.SetValue("sk-never-render-this-secret")

			view := m.renderKeyEntry()
			if got := lipgloss.Height(view); got > height {
				t.Fatalf("height=%d available=%v key entry rendered %d rows:\n%s", height, available, got, stripAnsi(view))
			}
			plain := stripAnsi(view)
			if strings.Contains(plain, "sk-never-render-this-secret") {
				t.Fatalf("height=%d available=%v leaked raw key", height, available)
			}
			for _, want := range []string{"Set API key", "Esc"} {
				if !strings.Contains(plain, want) {
					t.Fatalf("height=%d available=%v missing %q:\n%s", height, available, want, plain)
				}
			}
		}
	}
}

func TestKeyEntryFitsNarrowWidthsInAvailableAndDisabledStates(t *testing.T) {
	for width := 10; width <= 48; width++ {
		for _, available := range []bool{false, true} {
			m := NewModel()
			m.width = width
			m.height = 12
			if available {
				m.SetKeyEntrySubmitCallback(func(string, string) {})
			}
			m.openKeyEntry(OpenKeyEntryMsg{
				Provider:    "glm",
				DisplayName: strings.Repeat("Provider 界 ", 10),
				SetupURL:    strings.Repeat("https://example.test/key/", 10),
			})
			m.keyEntryInput.SetValue("sk-never-visible")

			view := m.renderKeyEntry()
			if strings.Contains(stripAnsi(view), "sk-never-visible") {
				t.Fatalf("width=%d available=%v leaked secret", width, available)
			}
			for row, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > width {
					t.Fatalf("width=%d available=%v row=%d overflow=%d: %q", width, available, row, got, stripAnsi(line))
				}
			}
		}
	}
}
