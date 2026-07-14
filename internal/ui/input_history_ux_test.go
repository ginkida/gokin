package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestHistorySearchCyclesEveryMatchWithoutSkipping(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetHistory([]string{"alpha oldest", "unrelated", "alpha middle", "alpha newest"})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("alpha")})
	if got := m.historySearchResult; got != "alpha newest" {
		t.Fatalf("initial reverse-search match=%q", got)
	}

	for _, want := range []string{"alpha middle", "alpha oldest", "alpha oldest"} {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
		if got := m.historySearchResult; got != want {
			t.Fatalf("next reverse-search match=%q, want %q", got, want)
		}
	}
}

func TestHistorySearchShowsNoMatchActionsAndPreservesDraft(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(48)
	m.SetHistory([]string{"previous message"})
	m.textarea.SetValue("current draft")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("missing")})

	view := m.View()
	plain := stripAnsi(view)
	for _, want := range []string{"History search", "No matching history", "Ctrl+R older", "Enter use", "Esc cancel"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("history no-match view missing %q:\n%s", want, plain)
		}
	}
	for row, line := range strings.Split(view, "\n")[:3] {
		if got := lipgloss.Width(line); got > m.textarea.Width() {
			t.Fatalf("history row %d width=%d, want <=%d: %q", row, got, m.textarea.Width(), stripAnsi(line))
		}
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if m.historySearchMode || m.textarea.Value() != "current draft" {
		t.Fatalf("cancel lost draft or retained mode: mode=%v draft=%q", m.historySearchMode, m.textarea.Value())
	}
}

func TestHistorySearchSanitizesDisplayButAcceptsOriginalEntry(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(40)
	m.SetHistory([]string{"first\n\x1b[31mmatch\x1b[0m line"})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("\x1b[32mmatch\n")})

	if got := m.historySearchQuery; got != "match " {
		t.Fatalf("sanitized history query=%q, want %q", got, "match ")
	}
	if strings.Contains(stripAnsi(m.View()), "\nmatch line\n") {
		t.Fatalf("multiline history result broke search layout:\n%s", stripAnsi(m.View()))
	}
	// Remove the trailing normalized space so the original entry matches.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.textarea.Value(); got != "first\nmatch line" {
		t.Fatalf("accepted history entry=%q, want sanitized multiline content", got)
	}
}

func TestInputResetClearsTransientComposeState(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.suggestions = []CommandInfo{{Name: "model"}}
	m.fileSuggestions = []string{"main.go"}
	m.showSuggestions = true
	m.suggestionIndex = 3
	m.ghostText = "ghost"
	m.showArgHints = true
	m.currentCommand = &CommandInfo{Name: "model", Args: []ArgInfo{{Name: "id"}}}
	m.historySearchMode = true
	m.historySearchQuery = "old"
	m.historySearchResult = "old result"
	m.historySearchIndex = 2
	m.Reset()

	if m.showSuggestions || len(m.suggestions) != 0 || len(m.fileSuggestions) != 0 || m.suggestionIndex != 0 ||
		m.ghostText != "" || m.showArgHints || m.currentCommand != nil || m.historySearchMode ||
		m.historySearchQuery != "" || m.historySearchResult != "" || m.historySearchIndex != -1 {
		t.Fatalf("Reset retained transient compose state: %+v", m)
	}
}

func TestInputHistoryUsesSnapshots(t *testing.T) {
	history := []string{"first", "second"}
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetHistory(history)
	history[0] = "mutated"
	got := m.GetHistory()
	got[1] = "mutated"
	if strings.Join(m.GetHistory(), ",") != "first,second" {
		t.Fatalf("history changed through caller-owned slice: %v", m.GetHistory())
	}
}

func TestInputHistoryPersistencePreservesMultilineAndReadsLegacy(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetHistory([]string{"first line\nsecond line", `say "hello"`})
	if err := m.SaveHistory(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dataDir, "gokin", historyFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("history permissions=%#o, want 0600", got)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(string(stored)), "\n") + 1; got != 2 {
		t.Fatalf("multiline history persisted as %d physical records, want 2:\n%s", got, stored)
	}

	reloaded := NewInputModel(DefaultStyles(), t.TempDir())
	if err := reloaded.LoadHistory(); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.GetHistory(); len(got) != 2 || got[0] != "first line\nsecond line" || got[1] != `say "hello"` {
		t.Fatalf("history round-trip=%q", got)
	}

	if err := os.WriteFile(path, []byte("legacy one\nlegacy two\n\"literal quotes\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := NewInputModel(DefaultStyles(), t.TempDir())
	if err := legacy.LoadHistory(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(legacy.GetHistory(), ","); got != "legacy one,legacy two,\"literal quotes\"" {
		t.Fatalf("legacy history migration=%q", got)
	}
}
