package ui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestUnknownSlashCommandShowsDismissibleNoMatch(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(44)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/definitely-missing")})

	if m.showSuggestions || m.suggestionNotice == "" {
		t.Fatalf("unknown command state: suggestions=%v notice=%q", m.showSuggestions, m.suggestionNotice)
	}
	view := m.View()
	plain := stripAnsi(view)
	for _, want := range []string{"No commands match", "/definitely", "Esc dismiss", "Enter send anyway"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("unknown-command notice missing %q:\n%s", want, plain)
		}
	}
	for row, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > 44 {
			t.Fatalf("completion row %d width=%d, want <=44: %q", row, got, stripAnsi(line))
		}
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if m.suggestionNotice != "" || m.textarea.Value() != "/definitely-missing" {
		t.Fatalf("Esc did not dismiss notice without losing input: notice=%q input=%q", m.suggestionNotice, m.textarea.Value())
	}
}

func TestFileSuggestionNoMatchClearsStaleGhostAndResults(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewInputModel(DefaultStyles(), work)
	m.updateFileSuggestions("/open ma")
	if len(m.fileSuggestions) == 0 || m.ghostText == "" {
		t.Fatalf("test setup did not create a file completion: files=%v ghost=%q", m.fileSuggestions, m.ghostText)
	}

	m.updateFileSuggestions("/open definitely-missing.zzz")
	if m.showSuggestions || len(m.fileSuggestions) != 0 || m.ghostText != "" {
		t.Fatalf("no-match retained stale completion: show=%v files=%v ghost=%q", m.showSuggestions, m.fileSuggestions, m.ghostText)
	}
	if !strings.Contains(m.suggestionNotice, "No files match") {
		t.Fatalf("file no-match has no feedback: %q", m.suggestionNotice)
	}
}

func TestFileSuggestionUnavailableExplainsWorkingDirectory(t *testing.T) {
	m := NewInputModel(DefaultStyles(), filepath.Join(t.TempDir(), "missing"))
	m.SetWidth(80)
	m.textarea.SetValue("@main")
	m.updateAtFileSuggestions("@main")

	plain := stripAnsi(m.View())
	if !strings.Contains(plain, "File suggestions unavailable") || !strings.Contains(plain, "working directory") {
		t.Fatalf("unavailable file completion lacks recovery context:\n%s", plain)
	}
}

func TestArgHintsFitInputWidth(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(24)
	m.currentCommand = &CommandInfo{
		Name: strings.Repeat("длинная-команда", 4),
		Args: []ArgInfo{{Name: strings.Repeat("аргумент", 5), Required: true}},
	}
	if got := lipgloss.Width(m.renderArgHints()); got > m.textarea.Width() {
		t.Fatalf("argument hint width=%d, want <=%d: %q", got, m.textarea.Width(), stripAnsi(m.renderArgHints()))
	}
}

func TestArgHintsPreferCanonicalUsage(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(80)
	m.currentCommand = &CommandInfo{
		Name:  "clear-todos",
		Args:  []ArgInfo{{Name: "force", Required: false}},
		Usage: "/clear-todos [--force]",
	}

	plain := stripAnsi(m.renderArgHints())
	if !strings.Contains(plain, "Usage: /clear-todos [--force]") {
		t.Fatalf("argument hint lost canonical flag syntax: %q", plain)
	}
	if strings.Contains(plain, "[force]") {
		t.Fatalf("argument hint rebuilt a lossy field-name synopsis: %q", plain)
	}
}

func TestArgHintsWrapOnceToKeepTrailingSafetyFlagVisible(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(38)
	m.currentCommand = &CommandInfo{
		Name:  "loop",
		Args:  []ArgInfo{{Name: "task", Required: true}},
		Usage: "/loop [<interval>] <task> [--max-tokens <N>] | list|status|output|pause|resume|stop|now|remove [id]",
	}

	rendered := m.renderArgHints()
	plain := stripAnsi(rendered)
	lines := strings.Split(plain, "\n")
	if len(lines) != 2 {
		t.Fatalf("long usage rendered %d lines, want compact two-line hint: %q", len(lines), plain)
	}
	if !strings.Contains(plain, "--max-tokens") || !strings.Contains(plain, "↳") {
		t.Fatalf("wrapped hint hides trailing safety flag or continuation cue: %q", plain)
	}
	for _, line := range lines {
		if got := lipgloss.Width(line); got > m.textarea.Width() {
			t.Fatalf("usage line width=%d, want <=%d: %q", got, m.textarea.Width(), line)
		}
	}
}

func TestArgHintsKeepShortUsageOnOneLine(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(80)
	m.currentCommand = &CommandInfo{
		Name:  "clear",
		Args:  []ArgInfo{{Name: "force"}},
		Usage: "/clear [--force]",
	}
	plain := stripAnsi(m.renderArgHints())
	if strings.Contains(plain, "\n") || plain != "  Usage: /clear [--force]" {
		t.Fatalf("short usage should remain stable on one line: %q", plain)
	}
}

func TestTabCompletedCommandShowsExecutableSyntax(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(80)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/clear-to")})
	if !m.showSuggestions {
		t.Fatal("partial command did not open autocomplete")
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got := m.textarea.Value(); got != "/clear-todos " {
		t.Fatalf("Tab completion = %q, want /clear-todos with trailing space", got)
	}
	if !m.showArgHints || m.currentCommand == nil {
		t.Fatal("Tab completion did not open argument guidance")
	}
	if plain := stripAnsi(m.View()); !strings.Contains(plain, "Usage: /clear-todos [--force]") {
		t.Fatalf("completed command lacks executable syntax:\n%s", plain)
	}
}

func TestRuntimeCommandFlagsAreCompletableAfterPositionalArguments(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "/save checkpoint --", want: "--force"},
		{input: "/resume session-id --", want: "--force"},
		{input: "/sessions --", want: "--all"},
		{input: "/logout all --", want: "--force"},
		{input: "/loop review failures --m", want: "--max-tokens"},
		{input: "/register-agent-type reviewer review --p", want: "--prompt"},
		{input: "/diff --c", want: "--cached"},
		{input: "/commit -", want: "-m"},
	}

	for _, tc := range tests {
		t.Run(tc.want+"_in_"+strings.Fields(tc.input)[0], func(t *testing.T) {
			m := NewInputModel(DefaultStyles(), t.TempDir())
			m.SetWidth(100)
			m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.input)})
			if !m.showSuggestions || m.suggestionType != SuggestionArgument {
				t.Fatalf("%q did not open flag completion: show=%v type=%v suggestions=%v", tc.input, m.showSuggestions, m.suggestionType, m.argSuggestions)
			}
			found := false
			for _, suggestion := range m.argSuggestions {
				if suggestion == tc.want {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("%q suggestions = %v, want %q", tc.input, m.argSuggestions, tc.want)
			}
		})
	}
}

func TestEnterAndTabBothPerformAdvertisedCommandAndFileCompletion(t *testing.T) {
	keys := []struct {
		name string
		msg  tea.KeyMsg
	}{
		{name: "enter", msg: tea.KeyMsg{Type: tea.KeyEnter}},
		{name: "tab", msg: tea.KeyMsg{Type: tea.KeyTab}},
	}

	for _, key := range keys {
		t.Run("command_"+key.name, func(t *testing.T) {
			m := NewInputModel(DefaultStyles(), t.TempDir())
			m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/mode")})
			if !m.showSuggestions || m.suggestionType != SuggestionCommand {
				t.Fatal("command dropdown did not open")
			}
			m, _ = m.Update(key.msg)
			if got := m.textarea.Value(); got != "/model " {
				t.Fatalf("%s command completion = %q, want /model", key.name, got)
			}
		})

		t.Run("file_"+key.name, func(t *testing.T) {
			work := t.TempDir()
			if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main"), 0600); err != nil {
				t.Fatal(err)
			}
			m := NewInputModel(DefaultStyles(), work)
			m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/open ma")})
			if !m.showSuggestions || m.suggestionType != SuggestionFile {
				t.Fatal("file dropdown did not open")
			}
			m, _ = m.Update(key.msg)
			if got := m.textarea.Value(); got != "/open main.go " {
				t.Fatalf("%s file completion = %q, want main.go", key.name, got)
			}
		})
	}
}

func TestArgumentOptionsFilterAndChainWithTab(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(80)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/set p")})

	if !m.showSuggestions || m.suggestionType != SuggestionArgument {
		t.Fatalf("option query did not open argument completion: show=%v type=%v", m.showSuggestions, m.suggestionType)
	}
	if got, want := m.argSuggestions, []string{"permissions", "plan", "preset"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered setting options = %v, want %v", got, want)
	}
	plain := stripAnsi(m.View())
	for _, want := range []string{"permissions", "plan", "Tab complete", "Enter run as typed"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("argument dropdown missing %q:\n%s", want, plain)
		}
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got := m.textarea.Value(); got != "/set permissions " {
		t.Fatalf("first argument completion = %q", got)
	}
	if got, want := m.argSuggestions, []string{"on", "off"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("chained value options = %v, want %v", got, want)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got := m.textarea.Value(); got != "/set permissions off " {
		t.Fatalf("second argument completion = %q", got)
	}
	if m.showSuggestions || len(m.argSuggestions) != 0 {
		t.Fatalf("completed argument chain left a stale dropdown: show=%v args=%v", m.showSuggestions, m.argSuggestions)
	}
}

func TestSetPresetCompletionUsesContextualValues(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(100)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/set pre")})
	if got, want := m.argSuggestions, []string{"preset"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("preset entry completion = %v, want %v", got, want)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got := m.textarea.Value(); got != "/set preset " {
		t.Fatalf("preset entry inserted %q", got)
	}
	if got, want := m.argSuggestions, []string{"safe", "balanced", "fast"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("preset values = %v, want %v", got, want)
	}
	if slicesOverlap(m.argSuggestions, []string{"on", "off"}) {
		t.Fatalf("preset value completion leaked toggle values: %v", m.argSuggestions)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	if got, want := m.argSuggestions, []string{"balanced"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered preset values = %v, want %v", got, want)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got := m.textarea.Value(); got != "/set preset balanced " {
		t.Fatalf("preset completion inserted %q", got)
	}
	if m.showSuggestions || len(m.argSuggestions) != 0 {
		t.Fatalf("completed preset retained stale suggestions: show=%v args=%v", m.showSuggestions, m.argSuggestions)
	}
}

func slicesOverlap(left, right []string) bool {
	seen := make(map[string]bool, len(left))
	for _, value := range left {
		seen[value] = true
	}
	for _, value := range right {
		if seen[value] {
			return true
		}
	}
	return false
}

func TestFlagCompletionWorksAfterEarlierFlags(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/pr --draft --b")})
	if m.suggestionType != SuggestionArgument || !reflect.DeepEqual(m.argSuggestions, []string{"--base"}) {
		t.Fatalf("repeated flag completion = type %v options %v", m.suggestionType, m.argSuggestions)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got := m.textarea.Value(); got != "/pr --draft --base " {
		t.Fatalf("flag completion after prior flag = %q", got)
	}
}

func TestPathMetadataDrivesImmediateFileCompletion(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewInputModel(DefaultStyles(), work)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/grep needle ")})
	if !m.showSuggestions || m.suggestionType != SuggestionFile || len(m.fileSuggestions) != 1 {
		t.Fatalf("declared path argument did not open files: show=%v type=%v files=%v",
			m.showSuggestions, m.suggestionType, m.fileSuggestions)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got := m.textarea.Value(); got != "/grep needle main.go " {
		t.Fatalf("path completion inserted %q", got)
	}
}

func TestAbsoluteFileCompletionPreservesExternalPath(t *testing.T) {
	work := t.TempDir()
	outside := filepath.Join(t.TempDir(), "shared directory")
	m := NewInputModel(DefaultStyles(), work)
	m.textarea.SetValue("/add-dir " + filepath.Dir(outside) + string(filepath.Separator) + "sha")
	m.acceptFileSuggestion(outside)
	if got := m.textarea.Value(); got != "/add-dir \""+outside+"\" " {
		t.Fatalf("external path was relativized or left unquoted: %q", got)
	}
}

func TestArgumentEnterSubmitsWithoutImplicitForce(t *testing.T) {
	m := *NewModel()
	m.width = 80
	m.state = StateInput
	var submitted string
	m.SetCallbacks(func(value string) { submitted = value }, nil)
	m = typeText(t, m, "/clear-todos ")
	if !m.input.ShowingSuggestions() || m.input.SuggestionsBlockSubmit() {
		t.Fatalf("force hint should be visible but non-blocking: show=%v block=%v",
			m.input.ShowingSuggestions(), m.input.SuggestionsBlockSubmit())
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if submitted != "/clear-todos" {
		t.Fatalf("Enter submitted %q; highlighted --force must require Tab", submitted)
	}
	if strings.Contains(submitted, "--force") {
		t.Fatal("ordinary Enter implicitly accepted destructive --force option")
	}
	if m.input.Value() != "" {
		t.Fatalf("submitted composer was not reset: %q", m.input.Value())
	}
}

func TestEscapeDismissesArgumentOptionsWithoutLosingDraft(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/set p")})
	if m.suggestionType != SuggestionArgument || !m.showSuggestions {
		t.Fatal("test setup did not open argument options")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if got := m.textarea.Value(); got != "/set p" {
		t.Fatalf("Esc changed the draft: %q", got)
	}
	if m.showSuggestions || len(m.argSuggestions) != 0 || m.showArgHints || m.currentCommand != nil {
		t.Fatalf("Esc left argument UI state: show=%v args=%v hints=%v command=%v",
			m.showSuggestions, m.argSuggestions, m.showArgHints, m.currentCommand)
	}
}

func TestInputCommandsUseDeepSnapshotsAndRefreshOpenQuery(t *testing.T) {
	commands := []CommandInfo{{Name: "model", Args: []ArgInfo{{
		Name:              "id",
		Options:           []string{"one"},
		OptionsByPrevious: map[string][]string{"preset": {"safe"}},
	}}}}
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.textarea.SetValue("/mod")
	m.SetCommands(commands)
	if !m.showSuggestions || len(m.suggestions) != 1 || m.suggestions[0].Name != "model" {
		t.Fatalf("SetCommands did not refresh the open query: %+v", m.suggestions)
	}

	commands[0].Name = "mutated"
	commands[0].Args[0].Options[0] = "mutated"
	commands[0].Args[0].OptionsByPrevious["preset"][0] = "mutated"
	if m.commands[0].Name != "model" || m.commands[0].Args[0].Options[0] != "one" ||
		m.commands[0].Args[0].OptionsByPrevious["preset"][0] != "safe" {
		t.Fatalf("commands changed through caller-owned data: %+v", m.commands[0])
	}
}

// v0.100.106 field ask: «когда список появляется, по Enter их выбрать».
// Enter accepts the highlighted ARGUMENT option — but only after explicit
// ↑/↓ navigation; a bare Enter after typing stays submit-as-typed so a
// highlighted destructive flag (--force) is never inserted implicitly.
func TestArgumentSuggestions_EnterSelectsAfterNavigation(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetCommands([]CommandInfo{{
		Name: "mcp", Description: "mcp", Args: []ArgInfo{{
			Name: "action", Options: []string{"list", "status", "enable", "disable"},
		}},
	}})
	m.textarea.SetValue("/mcp ")
	if !m.updateArgumentSuggestions("/mcp ") {
		t.Fatal("argument suggestions must show for /mcp ")
	}

	// Bare Enter (no navigation): dropdown must NOT own Enter, and Enter in
	// direct use closes the dropdown without inserting an option.
	if m.SuggestionsBlockSubmit() {
		t.Fatal("un-navigated argument dropdown must not own Enter (submit-as-typed)")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.Value(); got != "/mcp" && got != "/mcp " {
		t.Fatalf("bare Enter must not insert an option, value=%q", got)
	}

	// Re-open, navigate ↓ once → Enter accepts the highlighted option.
	m.textarea.SetValue("/mcp ")
	if !m.updateArgumentSuggestions("/mcp ") {
		t.Fatal("argument suggestions must re-show")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if !m.argNavigated {
		t.Fatal("KeyDown must mark argument navigation")
	}
	if !m.SuggestionsBlockSubmit() {
		t.Fatal("navigated argument dropdown must own Enter")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.Value(); got != "/mcp status" {
		t.Fatalf("Enter after ↓ must insert the highlighted option, value=%q", got)
	}

	// Typing again regenerates the list → navigation state resets.
	m.textarea.SetValue("/mcp ")
	if !m.updateArgumentSuggestions("/mcp ") {
		t.Fatal("argument suggestions must re-show after typing")
	}
	if m.argNavigated {
		t.Fatal("regenerating suggestions must reset argNavigated")
	}
}

// v0.100.106: the /mcp dropdown covers the full runtime-control surface and
// offers the preset catalog as second-position options after `preset`.
func TestMCPSuggestions_RuntimeControlAndPresets(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	if !m.updateArgumentSuggestions("/mcp ") {
		t.Fatal("argument suggestions must show for /mcp ")
	}
	joined := strings.Join(m.argSuggestions, " ")
	for _, want := range []string{"enable", "disable", "pause", "resume", "edit", "preset", "setup"} {
		if !strings.Contains(joined, want) {
			t.Errorf("/mcp options missing %q: %v", want, m.argSuggestions)
		}
	}
	if !m.updateArgumentSuggestions("/mcp preset ") {
		t.Fatal("second-position suggestions must show after `preset`")
	}
	joined = strings.Join(m.argSuggestions, " ")
	for _, want := range []string{"github", "filesystem", "sqlite"} {
		if !strings.Contains(joined, want) {
			t.Errorf("preset catalog options missing %q: %v", want, m.argSuggestions)
		}
	}
}

// v0.100.107 field regression (the Enter-select feature's own bug): on a WIDE
// terminal the post-navigation footer ("Enter select · …", no literal "run")
// was branded unreadable by suggestionRenderingHasActions, so the SECOND
// arrow key closed the provider dropdown mid-selection. Narrow terminals
// happened to pick a width-fallback footer containing "run" and kept working —
// which is why the first repro attempt passed.
func TestArgumentSuggestions_WideTerminalNavigationKeepsDropdown(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetCommands(DefaultCommands())
	m.SetWidth(140)
	m.viewportHeight = 12 // constrained composer, like the real TUI
	m.textarea.SetValue("/provider ")
	if !m.updateArgumentSuggestions("/provider ") {
		t.Fatal("no argument suggestions for /provider ")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if !m.suggestionActionsReadable() {
		t.Fatal("navigated footer must stay readable on a wide terminal")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if !m.showSuggestions {
		t.Fatal("second arrow key closed the dropdown (the field-report regression)")
	}
	if m.suggestionIndex != 2 {
		t.Fatalf("suggestionIndex = %d, want 2 after two downs", m.suggestionIndex)
	}
	// And Enter still selects the highlighted provider.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.Value(); got != "/provider kimi" {
		t.Fatalf("Enter after navigation inserted %q, want /provider kimi", got)
	}
}
