package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestInlineCommandHintsDoNotAdvertiseRemovedCommandsOrMutation(t *testing.T) {
	m := NewModel()
	configHint := strings.ToLower(m.getCommandHint("/config"))
	if !strings.Contains(configHint, "show current") || strings.Contains(configHint, "edit") {
		t.Fatalf("read-only /config hint is misleading: %q", configHint)
	}

	for _, removed := range []string{"/reasoning", "/restore", "/agents"} {
		if hint := m.getCommandHint(removed); hint != "" {
			t.Errorf("removed command %s still advertises hint %q", removed, hint)
		}
	}
}

func TestStateChangingCommandHintsExplainBehaviorAndIndependentSafety(t *testing.T) {
	m := NewModel()
	thinking := strings.ToLower(m.getCommandHint("/thinking"))
	for _, want := range []string{"adaptive", "reasoning", "token budget"} {
		if !strings.Contains(thinking, want) {
			t.Fatalf("thinking hint hides %q: %q", want, thinking)
		}
	}
	permissions := strings.ToLower(m.getCommandHint("/permissions"))
	if !strings.Contains(permissions, "sandbox is separate") {
		t.Fatalf("permissions hint hides independent sandbox: %q", permissions)
	}
	sandbox := strings.ToLower(m.getCommandHint("/sandbox"))
	if !strings.Contains(sandbox, "permission prompts are separate") {
		t.Fatalf("sandbox hint hides independent prompts: %q", sandbox)
	}
}

func TestInlineCommandHintCatalogCoversEveryAutocompleteCommand(t *testing.T) {
	m := NewModel()
	for _, command := range DefaultCommands() {
		if hint := strings.TrimSpace(m.getCommandHint("/" + command.Name)); hint == "" {
			t.Errorf("/%s has autocomplete copy but no inline hint after dismissal", command.Name)
		}
	}
	if hint := m.getCommandHint("/s"); hint != "" {
		t.Fatalf("ambiguous partial command picked a nondeterministic hint: %q", hint)
	}
	if hint := strings.ToLower(m.getCommandHint("/commit")); strings.Contains(hint, "ai-generated") {
		t.Fatalf("commit hint still hides documented positional messages: %q", hint)
	}
}

func TestInlineCommandHintDefersToStructuredComposerGuidance(t *testing.T) {
	m := NewModel()
	m.state = StateInput
	m.width = 30
	m.input.textarea.SetValue("/permissions")

	hint := m.inlineCommandHint()
	if hint == "" {
		t.Fatal("dismissed exact command should retain a compact inline hint")
	}
	if got := lipgloss.Width(hint); got > m.width {
		t.Fatalf("inline hint width=%d, want <=%d: %q", got, m.width, hint)
	}

	m.input.showSuggestions = true
	if got := m.inlineCommandHint(); got != "" {
		t.Fatalf("inline hint duplicated autocomplete guidance: %q", got)
	}
	m.input.showSuggestions = false
	m.input.showArgHints = true
	if got := m.inlineCommandHint(); got != "" {
		t.Fatalf("inline hint duplicated canonical usage guidance: %q", got)
	}

	m.input.showArgHints = false
	m.state = StateProcessing
	if got := m.inlineCommandHint(); got != "" {
		t.Fatalf("inline command hint leaked outside input state: %q", got)
	}
}
