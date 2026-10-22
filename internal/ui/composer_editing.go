package ui

import (
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// Snapshots retain paste identities, not just their visible labels. Otherwise
// undoing or stashing a draft could silently lose its attached pasted content.
type composerSnapshot struct {
	value        string
	cursor       int
	pastes       map[int]string
	spans        []pasteSpan
	sequence     int
	historyIndex int
	savedInput   string
	savedSpans   []pasteSpan
}

func (m InputModel) snapshot() composerSnapshot {
	pastes := make(map[int]string, len(m.pastes))
	for id, text := range m.pastes {
		pastes[id] = text
	}
	return composerSnapshot{m.textarea.Value(), textareaCursorByteOffset(m.textarea), pastes,
		clonePasteSpans(m.pasteSpans), m.pasteSeq, m.historyIndex, m.savedInput, clonePasteSpans(m.savedPasteSpans)}
}

func (m *InputModel) restoreDraft(s composerSnapshot) {
	m.textarea.SetValue(s.value)
	m.setComposerCursor(s.cursor)
	m.pastes = make(map[int]string, len(s.pastes))
	for id, text := range s.pastes {
		m.pastes[id] = text
	}
	m.pasteSpans, m.pasteSeq = clonePasteSpans(s.spans), s.sequence
	m.historyIndex, m.savedInput, m.savedPasteSpans = s.historyIndex, s.savedInput, clonePasteSpans(s.savedSpans)
	m.leaveHistorySearch()
	m.syncTextareaHeight()
	m.refreshCompletionStateForValue()
}

func (m *InputModel) setComposerCursor(offset int) {
	value := m.textarea.Value()
	offset = min(max(offset, 0), len(value))
	row := strings.Count(value[:offset], "\n")
	for m.textarea.Line() > row {
		m.textarea.CursorUp()
	}
	for m.textarea.Line() < row {
		m.textarea.CursorDown()
	}
	start := strings.LastIndex(value[:offset], "\n") + 1
	m.textarea.SetCursor(utf8.RuneCountInString(value[start:offset]))
}

func (m *InputModel) rememberUndo(s composerSnapshot) {
	m.undoDrafts = append(m.undoDrafts, s)
	if len(m.undoDrafts) > 50 {
		m.undoDrafts = append([]composerSnapshot(nil), m.undoDrafts[len(m.undoDrafts)-50:]...)
	}
}

func (m *InputModel) toggleStashedDraft() string {
	defer m.updatePlaceholder()
	current := m.snapshot()
	stored := m.stashedDraft
	if current.value == "" && stored == nil {
		return "No draft to stash"
	}
	if stored == nil {
		m.Reset()
		m.stashedDraft = &current
		return "Draft stashed · Ctrl+S restores it"
	}
	m.restoreDraft(*stored)
	m.undoDrafts = nil
	if current.value == "" {
		m.stashedDraft = nil
		return "Draft restored"
	}
	m.stashedDraft = &current
	return "Drafts swapped · Ctrl+S swaps back"
}

func (m InputModel) updateReadline(msg tea.Msg) (InputModel, tea.Cmd) {
	keyMsg, isKey := msg.(tea.KeyMsg)
	if isKey && !keyMsg.Paste && !m.historySearchMode {
		if (keyMsg.Type == tea.KeyUp || keyMsg.Type == tea.KeyCtrlP) && m.textarea.Value() == "" && m.clearedDraft != nil {
			saved := *m.clearedDraft
			m.clearedDraft = nil
			m.restoreDraft(saved)
			return m, nil
		}
		switch keyMsg.String() {
		case "ctrl+_":
			if n := len(m.undoDrafts); n > 0 {
				saved := m.undoDrafts[n-1]
				m.undoDrafts = m.undoDrafts[:n-1]
				m.restoreDraft(saved)
			}
			return m, nil
		case "ctrl+y":
			if len(m.killRing) == 0 {
				return m, nil
			}
			before := m.snapshot()
			value := m.killRing[len(m.killRing)-1]
			if isLargePaste(value) {
				m = m.collapsePaste(value)
			} else {
				cursor := textareaCursorByteOffset(m.textarea)
				m.textarea.InsertString(value)
				m.reconcilePasteEdit(before.value, m.textarea.Value(), cursor, cursor)
				m.syncTextareaHeight()
				m.refreshCompletionStateForValue()
			}
			if before.value != m.textarea.Value() {
				m.rememberUndo(before)
			}
			return m, nil
		case "ctrl+p":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "ctrl+n":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		}
	}
	before := m.snapshot()
	next, cmd := m.updateInput(msg)
	if before.value != next.textarea.Value() {
		next.rememberUndo(before)
		if isKey && !keyMsg.Paste {
			switch keyMsg.String() {
			case "ctrl+k", "ctrl+u", "ctrl+w", "alt+d", "alt+backspace":
				if cut := removedComposerText(before, next.textarea.Value(), textareaCursorByteOffset(next.textarea)); cut != "" {
					next.killRing = append(next.killRing, cut)
					if len(next.killRing) > 10 {
						next.killRing = append([]string(nil), next.killRing[len(next.killRing)-10:]...)
					}
				}
			}
		}
	}
	return next, cmd
}

func removedComposerText(before composerSnapshot, after string, afterCursor int) string {
	start := min(before.cursor, afterCursor)
	end := start + len(before.value) - len(after)
	if start < 0 || end <= start || end > len(before.value) || before.value[:start]+before.value[end:] != after {
		return ""
	}
	var result strings.Builder
	cursor := start
	for _, span := range before.spans {
		if span.start < cursor || span.end > end {
			continue
		}
		content, exists := before.pastes[span.id]
		if !exists || before.value[span.start:span.end] != pasteDisplayLabel(span.id, content) {
			continue
		}
		result.WriteString(before.value[cursor:span.start])
		result.WriteString(content)
		cursor = span.end
	}
	result.WriteString(before.value[cursor:end])
	return result.String()
}
