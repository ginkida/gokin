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

func TestHistorySearchAcceptsPhysicalSpaceForPhraseQuery(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetHistory([]string{"deploy staging now", "deploy production safely"})

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("deploy")})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("production")})

	if got := m.historySearchQuery; got != "deploy production" {
		t.Fatalf("phrase query=%q, want physical space preserved", got)
	}
	if got := m.historySearchResult; got != "deploy production safely" {
		t.Fatalf("phrase search result=%q", got)
	}
}

func TestHistorySearchEmptyCtrlRCyclesAndReturnsToDraft(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetHistory([]string{"oldest request", "newest request"})
	m.textarea.SetValue("current draft")
	m.textarea.CursorEnd()

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	if m.historySearchResult != "" {
		t.Fatalf("entering search should wait for query or another Ctrl+R: %q", m.historySearchResult)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	if got := m.historySearchResult; got != "newest request" {
		t.Fatalf("empty-query Ctrl+R match=%q, want newest", got)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	if got := m.historySearchResult; got != "oldest request" {
		t.Fatalf("second empty-query Ctrl+R match=%q, want oldest", got)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.textarea.Value(); got != "oldest request" {
		t.Fatalf("accepted history result=%q", got)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := m.textarea.Value(); got != "newest request" {
		t.Fatalf("Down after accepted search=%q, want newer history", got)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := m.textarea.Value(); got != "current draft" || m.historyIndex != -1 {
		t.Fatalf("history navigation did not return to draft: value=%q index=%d", got, m.historyIndex)
	}
}

func TestHistoryRecallRestoresMultilineComposerHeight(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetHistory([]string{"one\ntwo\nthree"})
	m.textarea.SetValue("draft\ncontinued")
	m.textarea.SetHeight(2)
	// History owns Up only at the first visual row; inside a multiline draft
	// the arrow remains ordinary editor navigation.
	m.textarea.CursorUp()
	m.textarea.CursorStart()

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := m.textarea.Value(); got != "one\ntwo\nthree" || m.Height() != 3 {
		t.Fatalf("multiline history recall value=%q height=%d, want height 3", got, m.Height())
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := m.textarea.Value(); got != "draft\ncontinued" || m.Height() != 2 {
		t.Fatalf("draft restore value=%q height=%d, want height 2", got, m.Height())
	}
}

func TestHistoryRecallRecollapsesLargePasteAndRestoresCollapsedDraft(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	draft := strings.Repeat("draft line\n", 9) + "draft end"
	historyEntry := strings.Repeat("history line\n", 11) + "history end"
	m = m.collapsePaste(draft)
	draftChip := m.Value()
	m.SetHistory([]string{historyEntry})

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if visible := m.Value(); visible == historyEntry || !strings.Contains(visible, "[Pasted text #2 +12 lines]") {
		t.Fatalf("large history entry was not recalled as a new compact chip: %q", visible)
	}
	if got := m.ExpandedValue(); got != historyEntry {
		t.Fatalf("expanded recalled history=%q, want original entry", got)
	}
	if got := m.Height(); got != 1 {
		t.Fatalf("large recalled history height=%d, want compact height 1", got)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := m.Value(); got != draftChip {
		t.Fatalf("Down restored draft=%q, want original chip %q", got, draftChip)
	}
	if got := m.ExpandedValue(); got != draft {
		t.Fatalf("expanded restored draft=%q, want original draft", got)
	}
	if got := m.Height(); got != 1 || m.historyIndex != -1 {
		t.Fatalf("restored draft height=%d index=%d, want height 1 index -1", got, m.historyIndex)
	}
}

func TestHistorySearchAcceptRecollapsesLargeMatch(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	historyEntry := "unique history match\n" + strings.Repeat("large recalled line\n", 8) + "end"
	m.SetHistory([]string{historyEntry})
	m.textarea.SetValue("current draft")
	m.textarea.CursorEnd()

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("unique")})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if visible := m.Value(); visible == historyEntry || !strings.Contains(visible, "[Pasted text #1 +10 lines]") {
		t.Fatalf("accepted large search match was not compact: %q", visible)
	}
	if got := m.ExpandedValue(); got != historyEntry {
		t.Fatalf("expanded accepted search match=%q, want original entry", got)
	}
	if got := m.Height(); got != 1 {
		t.Fatalf("accepted large search match height=%d, want 1", got)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := m.Value(); got != "current draft" || m.historyIndex != -1 {
		t.Fatalf("Down after search did not restore draft: value=%q index=%d", got, m.historyIndex)
	}
}

func TestRecalledLargeHistorySubmitsExpandedContent(t *testing.T) {
	m := NewModel()
	historyEntry := strings.Repeat("full payload line\n", 7) + "payload end"
	m.input.SetHistory([]string{historyEntry})
	m.minSubmitDelay = 0
	var submitted string
	m.SetCallbacks(func(value string) { submitted = value }, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	model := updated.(Model)
	m = &model
	if visible := m.input.Value(); visible == historyEntry || !strings.Contains(visible, "[Pasted text #") {
		t.Fatalf("recalled history should be compact before submit, got %q", visible)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	m = &model
	if submitted != historyEntry {
		t.Fatalf("submitted recalled history=%q, want full expanded content", submitted)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input was not reset after recalled history submit: %q", got)
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
	for _, want := range []string{"History search", "No matching history", "Keep typing", "Esc cancel"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("history no-match view missing %q:\n%s", want, plain)
		}
	}
	for _, dead := range []string{"Ctrl+R older", "Enter use"} {
		if strings.Contains(plain, dead) {
			t.Fatalf("history no-match view advertises dead action %q:\n%s", dead, plain)
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

func TestHistorySearchNarrowFooterKeepsCancelAndOnlyLiveActions(t *testing.T) {
	for _, width := range []int{8, 12, 20, 32} {
		m := NewInputModel(DefaultStyles(), t.TempDir())
		m.SetWidth(width)
		m.SetHistory([]string{"alpha only"})
		m.textarea.SetValue("draft")
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("alpha")})

		view := m.View()
		rows := strings.Split(view, "\n")
		footer := ""
		if len(rows) >= 3 {
			footer = stripAnsi(rows[2])
		}
		if !strings.Contains(footer, "Esc") && !strings.Contains(footer, "⎋") {
			t.Fatalf("width=%d match footer lost cancel:\n%s", width, stripAnsi(view))
		}
		if strings.Contains(footer, "Ctrl+R older") {
			t.Fatalf("width=%d oldest match advertised dead older action:\n%s", width, stripAnsi(view))
		}
		for row, line := range rows[:3] {
			if got := lipgloss.Width(line); got > m.textarea.Width() {
				t.Fatalf("width=%d row=%d rendered width=%d: %q", width, row, got, stripAnsi(line))
			}
		}

		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
		if m.historySearchMode || m.textarea.Value() != "draft" {
			t.Fatalf("width=%d cancel lost draft: mode=%v draft=%q", width, m.historySearchMode, m.textarea.Value())
		}
	}
}

func TestHistorySearchFooterTracksOlderMatchAvailability(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(64)
	m.SetHistory([]string{"alpha oldest", "unrelated", "alpha newest"})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	if neutral := stripAnsi(m.View()); !strings.Contains(neutral, "Ctrl+R newest") || strings.Contains(neutral, "Enter use") {
		t.Fatalf("neutral history footer advertises wrong actions:\n%s", neutral)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("alpha")})
	if newest := stripAnsi(m.View()); !strings.Contains(newest, "Enter use") || !strings.Contains(newest, "Ctrl+R older") {
		t.Fatalf("newest matching result lost live actions:\n%s", newest)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	if oldest := stripAnsi(m.View()); !strings.Contains(oldest, "Enter use") || strings.Contains(oldest, "Ctrl+R older") {
		t.Fatalf("oldest matching result advertises dead older action:\n%s", oldest)
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
	if got := m.Height(); got != 2 {
		t.Fatalf("accepted multiline history height=%d, want 2", got)
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

func TestShortHistorySearchKeepsMatchActionsAndDraft(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(40)
	m.SetViewportHeight(6)
	m.SetHistory([]string{"older request", "matched request"})
	m.textarea.SetValue("draft\ncontinued\nthird line")
	m.syncTextareaHeight()

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("matched")})
	view := m.View()
	plain := stripAnsi(view)
	if got := lipgloss.Height(view); got > 5 {
		t.Fatalf("short history search uses %d rows before status, want <=5:\n%s", got, plain)
	}
	for _, want := range []string{"Match", "matched request", "Esc", "Enter use"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("short history search lost %q:\n%s", want, plain)
		}
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if got := m.textarea.Value(); got != "draft\ncontinued\nthird line" {
		t.Fatalf("history cancel lost draft: %q", got)
	}
	if got := m.Height(); got != 3 {
		t.Fatalf("history cancel did not restore short-viewport draft height: %d", got)
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
