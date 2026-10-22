package ui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func claudeComposer(t *testing.T) Model {
	t.Helper()
	m := *NewModel()
	m.SetKeyboardProfile("claude")
	m.applyResize(&tea.WindowSizeMsg{Width: 80, Height: 24})
	m.firstLaunchWelcomePending = false
	return m
}

func uxKey(m Model, key tea.KeyMsg) Model   { next, _ := m.Update(key); return next.(Model) }
func uxCtrl(m Model, key tea.KeyType) Model { return uxKey(m, tea.KeyMsg{Type: key}) }
func uxText(m Model, text string) Model {
	return uxKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
}

func TestClaudeComposerReadlineKeysKeepInputOwnership(t *testing.T) {
	for _, state := range []State{StateInput, StateProcessing, StateStreaming} {
		t.Run(fmt.Sprint(state), func(t *testing.T) {
			m := claudeComposer(t)
			m.state = state
			m = uxText(m, "hello")
			m = uxCtrl(m, tea.KeyCtrlA)
			m = uxText(m, "X")
			m = uxCtrl(m, tea.KeyCtrlE)
			m = uxText(m, "Y")
			m = uxCtrl(m, tea.KeyCtrlB)
			m = uxText(m, "Z")
			m = uxCtrl(m, tea.KeyCtrlF)
			m = uxCtrl(m, tea.KeyCtrlH)
			if m.input.Value() != "XhelloZ" {
				t.Fatalf("editing keys corrupted text: %q", m.input.Value())
			}
			if m.state != state || (m.agentTreePanel != nil && m.agentTreePanel.IsVisible()) {
				t.Fatal("editing opened a panel or changed request state")
			}
		})
	}
}

func TestClaudeComposerCutYankUndoAndUnicode(t *testing.T) {
	m := claudeComposer(t)
	m = uxText(m, "éê two words")
	m = uxCtrl(m, tea.KeyCtrlW)
	if m.input.Value() != "éê two" {
		t.Fatalf("word cut: %q", m.input.Value())
	}
	m = uxCtrl(m, tea.KeyCtrlY)
	if m.input.Value() != "éê two words" {
		t.Fatalf("word yank: %q", m.input.Value())
	}
	m = uxCtrl(m, tea.KeyCtrlA)
	m = uxCtrl(m, tea.KeyCtrlF)
	m = uxCtrl(m, tea.KeyCtrlK)
	if m.input.Value() != "é" {
		t.Fatalf("line cut: %q", m.input.Value())
	}
	m = uxCtrl(m, tea.KeyCtrlY)
	if m.input.Value() != "éê two words" {
		t.Fatalf("Unicode cut/yank lost bytes: %q", m.input.Value())
	}
	m = uxCtrl(m, tea.KeyCtrlUnderscore)
	if m.input.Value() != "é" {
		t.Fatalf("undo yank: %q", m.input.Value())
	}
	m = uxCtrl(m, tea.KeyCtrlUnderscore)
	if m.input.Value() != "éê two words" {
		t.Fatalf("undo cut: %q", m.input.Value())
	}
}

func TestClaudeStashSurvivesAnotherSendAndPreservesPasteIdentityAndCursor(t *testing.T) {
	m := claudeComposer(t)
	big := strings.Repeat("hidden pasted content\n", 20) + "last line"
	m = uxKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(big), Paste: true})
	m = uxText(m, " suffix")
	m = uxCtrl(m, tea.KeyCtrlA)
	m = uxCtrl(m, tea.KeyCtrlS)
	if m.input.Value() != "" || m.state != StateInput {
		t.Fatal("stash failed to clear composer")
	}
	m = uxText(m, "temporary question")
	m.input.Reset() // another prompt was sent
	m = uxCtrl(m, tea.KeyCtrlS)
	m = uxText(m, "prefix ")
	if got := m.input.ExpandedValue(); got != "prefix "+big+" suffix" {
		t.Fatalf("draft lost paste identity or cursor: %q", got)
	}
	if strings.Contains(m.input.Value(), "hidden pasted content") {
		t.Fatal("restoring a draft needlessly expanded its paste chip")
	}
	// Resetting a different draft must not alias/mutate the saved paste map.
	m = uxCtrl(m, tea.KeyCtrlS)
	m = uxText(m, "other")
	m = uxCtrl(m, tea.KeyCtrlS)
	if got := m.input.ExpandedValue(); got != "prefix "+big+" suffix" {
		t.Fatalf("draft swap lost content: %q", got)
	}
}

func TestClaudeCutPasteChipCanBeYankedAndUndoneWithoutHiddenContentLoss(t *testing.T) {
	m := claudeComposer(t)
	big := strings.Repeat("paste\n", 9) + "end"
	m = uxKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(big), Paste: true})
	m = uxCtrl(m, tea.KeyCtrlA)
	m = uxCtrl(m, tea.KeyCtrlK)
	m = uxCtrl(m, tea.KeyCtrlY)
	if m.input.ExpandedValue() != big {
		t.Fatal("yanking a cut paste chip lost its real content")
	}
	m = uxCtrl(m, tea.KeyCtrlUnderscore)
	m = uxCtrl(m, tea.KeyCtrlUnderscore)
	if m.input.ExpandedValue() != big {
		t.Fatal("undoing a chip cut lost paste identity")
	}
}

func TestClaudeHistoryKeysNavigateLinesBeforeHistory(t *testing.T) {
	m := claudeComposer(t)
	m.input.AddToHistory("older prompt")
	m = uxText(m, "first line")
	m = uxCtrl(m, tea.KeyCtrlJ)
	m = uxText(m, "second line")
	m = uxCtrl(m, tea.KeyCtrlP)
	if m.input.Value() != "first line\nsecond line" {
		t.Fatal("Ctrl+P skipped an editable line and recalled history")
	}
	m = uxCtrl(m, tea.KeyCtrlP)
	if m.input.Value() != "older prompt" {
		t.Fatalf("history not reachable at first visual line: %q", m.input.Value())
	}
	m = uxCtrl(m, tea.KeyCtrlN)
	if m.input.Value() != "first line\nsecond line" {
		t.Fatal("Ctrl+N did not restore draft")
	}
}

func TestClaudePlainLettersAndLineEndDoNotExpandToolOutput(t *testing.T) {
	for _, letter := range []string{"e", "E"} {
		m := claudeComposer(t)
		m.lastToolOutputIndex = m.toolOutput.AddEntry("bash", "full output")
		before := m.output.Content()
		m = uxText(m, letter)
		m = uxCtrl(m, tea.KeyCtrlE)
		if m.input.Value() != letter || m.output.Content() != before {
			t.Fatalf("ordinary input triggered a tool-output action for %q", letter)
		}
	}
}

func TestClaudeCancelRestoresDraftFromHistoryAndQuitKeysCannotCrossConfirm(t *testing.T) {
	m := claudeComposer(t)
	m = uxText(m, "unsent draft")
	m = uxCtrl(m, tea.KeyCtrlC)
	if m.input.Value() != "" {
		t.Fatal("Ctrl+C did not clear idle draft")
	}
	m = uxCtrl(m, tea.KeyUp)
	if m.input.Value() != "unsent draft" {
		t.Fatal("cleared draft could not be recovered with Up")
	}
	m.input.Reset()
	m = uxCtrl(m, tea.KeyCtrlC)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("Ctrl+D did not arm visible quit confirmation")
	}
	if _, quit := cmd().(tea.QuitMsg); quit {
		t.Fatal("different quit keys confirmed each other")
	}
	if !strings.Contains(stripAnsi(m.View()), "Ctrl+D") {
		t.Fatal("quit confirmation advertises wrong key")
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if cmd == nil {
		t.Fatal("second Ctrl+D did not quit")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatal("second Ctrl+D failed confirmation")
	}
}

func TestClaudeRedrawPreservesConversationAndDraft(t *testing.T) {
	m := claudeComposer(t)
	m.output.AppendLine("important history")
	m = uxText(m, "draft")
	before := m.output.Content()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	m = next.(Model)
	if cmd == nil || m.output.Content() != before || m.input.Value() != "draft" {
		t.Fatal("redraw destroyed history or draft")
	}
}

func TestClaudeKeyboardLabelsMatchReachableActions(t *testing.T) {
	m := claudeComposer(t)
	m.availableModels = []ModelInfo{{ID: "glm-5.3-flash", Name: "Flash"}}
	for _, key := range []tea.KeyMsg{{Type: tea.KeyF1}, {Type: tea.KeyRunes, Runes: []rune{'p'}, Alt: true}} {
		m = uxKey(m, key)
		if m.state != StateCommandPalette && m.state != StateModelSelector {
			t.Fatalf("new action key did not open intended surface: %v", m.state)
		}
		view := stripAnsi(m.View())
		if strings.Contains(view, "Ctrl+K") || strings.Contains(view, "Ctrl+P") {
			t.Fatalf("surface advertises original profile: %s", view)
		}
		m = uxCtrl(m, tea.KeyEsc)
	}
	var labels []string
	for _, category := range m.shortcutsOverlay.categories {
		for _, entry := range category.Shortcuts {
			labels = append(labels, strings.Join(entry.Keys, "+"))
		}
	}
	for _, key := range []string{"F1", "F2", "Alt+P", "Ctrl+S", "Ctrl+O"} {
		if !strings.Contains(strings.Join(labels, ","), key) {
			t.Fatalf("shortcut reference missing %s", key)
		}
	}
	m.SetKeyboardProfile("gokin")
	if !reflect.DeepEqual(m.shortcutsOverlay.categories, DefaultShortcuts()) {
		t.Fatal("switching back did not restore original keyboard reference")
	}
}

func TestTranscriptSearchToolsAndLiveReturnDoNotMutateConversation(t *testing.T) {
	m := claudeComposer(t)
	m.state = StateStreaming
	for i := 0; i < 90; i++ {
		m.output.AppendLine(fmt.Sprintf("line %d · 日本語", i))
	}
	m.lastToolOutputIndex = m.toolOutput.AddEntry("bash", "first\nsecret detail\nlast")
	m = uxText(m, "draft")
	before := m.output.Content()
	m = uxCtrl(m, tea.KeyCtrlO)
	if m.state != StateTranscript {
		t.Fatal("transcript unavailable during streaming")
	}
	m = uxText(m, "/line 3") // real terminals may coalesce fast typing
	m = uxCtrl(m, tea.KeyEnter)
	if len(m.transcript.matches) != 11 {
		t.Fatalf("search matched %d lines", len(m.transcript.matches))
	}
	m = uxCtrl(m, tea.KeyTab)
	if !strings.Contains(stripAnsi(m.View()), "secret detail") {
		t.Fatal("tools tab omits full tool content")
	}
	next, _ := m.Update(ResponseDoneMsg{})
	m = next.(Model)
	if m.state != StateTranscript {
		t.Fatal("completed stream dismissed transcript")
	}
	m = uxCtrl(m, tea.KeyEsc)
	if m.state != StateInput || m.input.Value() != "draft" {
		t.Fatal("closing transcript restored stale activity or lost draft")
	}
	if !strings.HasPrefix(m.output.Content(), before) {
		t.Fatal("transcript overwrote conversation")
	}
}

func TestTranscriptHandlesHostileSizesAndTerminalEscapes(t *testing.T) {
	for _, width := range []int{1, 2, 5, 12, 40, 80, 120} {
		for _, height := range []int{1, 2, 3, 8, 24} {
			m := claudeComposer(t)
			m.applyResize(&tea.WindowSizeMsg{Width: width, Height: height})
			m.toolOutput.AddEntry("bash", "\x1b]52;c;Y2xpcGJvYXJk\a\x1b[2J"+strings.Repeat("漢字🎉", 80))
			m.openTranscript()
			m = uxCtrl(m, tea.KeyTab)
			view := m.View()
			if lipgloss.Height(view) > height || lipgloss.Width(view) > width {
				t.Fatalf("transcript exceeds %dx%d: %dx%d", width, height, lipgloss.Width(view), lipgloss.Height(view))
			}
			if strings.Contains(view, "\x1b]52") || strings.Contains(view, "\x1b[2J") {
				t.Fatal("tool result injected terminal control commands")
			}
		}
	}
}

func TestTranscriptSearchFindsWrappedPhrasesAndKeepsPastedQuerySingleLine(t *testing.T) {
	m := claudeComposer(t)
	m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 8})
	m.output.AppendLine("prefix padding TARGET PHRASE after")
	m.openTranscript()
	m = uxText(m, "/target phrase")
	if len(m.transcript.matches) != 1 {
		t.Fatal("search lost a phrase split by visual wrapping")
	}
	m = uxKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("\npasted query"), Paste: true})
	if strings.Contains(m.transcript.query, "\n") || lipgloss.Height(m.View()) > 8 {
		t.Fatal("a pasted search query broke transcript geometry")
	}
}

func TestStashedDraftRemainsDiscoverableAfterToastExpires(t *testing.T) {
	m := claudeComposer(t)
	m = uxText(m, "draft")
	m = uxCtrl(m, tea.KeyCtrlS)
	if !strings.Contains(m.input.textarea.Placeholder, "Ctrl+S restores") {
		t.Fatal("stashed draft has no persistent recovery hint")
	}
}

func TestTranscriptYieldsToPermissionWithoutRejectingWorkOrLosingDraft(t *testing.T) {
	m := claudeComposer(t)
	m.state = StateStreaming
	m = uxText(m, "followup draft")
	m.openTranscript()
	decisions := 0
	m.SetPermissionCallback(func(string, PermissionDecision) { decisions++ })
	next, _ := m.Update(PermissionRequestMsg{ID: "edit-review", ToolName: "edit", Args: map[string]any{"file_path": "sample.go"}})
	m = next.(Model)
	if m.state != StatePermissionPrompt || decisions != 0 {
		t.Fatal("viewing the transcript implicitly rejected a permission request")
	}
	if m.input.Value() != "followup draft" || m.transcript == nil {
		t.Fatal("review prompt discarded the draft or transcript")
	}
}

func TestTranscriptCtrlCCancelsUnderlyingRequestAndPreservesDraft(t *testing.T) {
	m := claudeComposer(t)
	m.state = StateStreaming
	m = uxText(m, "draft")
	cancels := 0
	m.SetCancelCallback(func() { cancels++ })
	m.openTranscript()
	m = uxCtrl(m, tea.KeyCtrlC)
	if cancels != 1 || m.state != StateInput || m.input.Value() != "draft" {
		t.Fatal("Ctrl+C from transcript failed to cancel underlying work safely")
	}
}

func TestTranscriptIncludesEditDiffAndFailedResultsWithTheirTargets(t *testing.T) {
	m := claudeComposer(t)
	m.handleMessageTypes(ToolResultMsg{Name: "edit", Args: map[string]any{"file_path": "sample.go"}, Content: "changed file", Diff: "- return 1\n+ return 2", DiffAdded: 1, DiffRemoved: 1})
	m.handleMessageTypes(ToolResultMsg{Name: "read", Args: map[string]any{"file_path": "missing.go"}, Failed: true, Error: "file does not exist"})
	m.openTranscript()
	m = uxCtrl(m, tea.KeyTab)
	view := stripAnsi(m.View())
	for _, want := range []string{"sample.go", "return 1", "return 2", "missing.go", "failed", "file does not exist"} {
		if !strings.Contains(view, want) {
			t.Fatalf("full tool transcript missing %q:\n%s", want, view)
		}
	}
}

func TestClearingOutputAlsoClearsTranscriptBackingStores(t *testing.T) {
	m := claudeComposer(t)
	m.handleMessageTypes(ToolResultMsg{Name: "read", Args: map[string]any{"file_path": "private.go"}, Content: "old private tool result"})
	m.openTranscript()
	m.ClearOutput()
	m.openTranscript()
	m = uxCtrl(m, tea.KeyTab)
	if strings.Contains(stripAnsi(m.View()), "old private") || len(m.transcriptTools) != 0 || m.toolOutput.EntryCount() != 0 {
		t.Fatal("cleared tool results resurfaced in transcript")
	}
}
