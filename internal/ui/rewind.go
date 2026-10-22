package ui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

type RewindChoice struct {
	ID, Prompt        string
	HasCode           bool
	PreviousSessionID string
}
type OpenRewindMenuMsg struct{ Choices []RewindChoice }

func (m *Model) rewindActions() []string {
	if m.rewindSelected < 0 || m.rewindSelected >= len(m.rewindChoices) {
		return nil
	}
	if m.rewindChoices[m.rewindSelected].HasCode {
		return []string{"both", "conversation", "code", "summarize-from", "summarize-before", "cancel"}
	}
	return []string{"conversation", "summarize-from", "summarize-before", "cancel"}
}
func (m *Model) submitRewindCommand(command string) tea.Cmd {
	if m.onSubmit == nil {
		return keyConsumed
	}
	m.state = StateProcessing
	m.onSubmit(command)
	return keyConsumed
}
func (m *Model) handleRewindKeys(msg tea.KeyMsg) tea.Cmd {
	if msg.Type == tea.KeyEscape || msg.Type == tea.KeyCtrlC {
		if m.rewindChoosingAction {
			m.rewindChoosingAction = false
			return keyConsumed
		}
		return m.restoreTransientOverlay()
	}
	// Never activate an invisible action on a terminal too small for the menu.
	if m.width < 24 || m.height < 8 {
		return keyConsumed
	}
	limit := len(m.rewindChoices)
	selected := &m.rewindSelected
	if m.rewindChoosingAction {
		limit = len(m.rewindActions())
		selected = &m.rewindAction
	}
	if m.rewindChoosingAction && m.rewindAction >= 0 && m.rewindAction < limit {
		action := m.rewindActions()[m.rewindAction]
		if action == "summarize-from" || action == "summarize-before" {
			switch msg.Type {
			case tea.KeySpace:
				if len(m.rewindSummaryGuidance) < 4096 {
					m.rewindSummaryGuidance += " "
				}
				return keyConsumed
			case tea.KeyRunes:
				addition := strings.Map(func(r rune) rune {
					if unicode.IsControl(r) {
						return ' '
					}
					return r
				}, string(msg.Runes))
				if !msg.Alt && len(m.rewindSummaryGuidance)+len(addition) <= 4096 {
					m.rewindSummaryGuidance += addition
				}
				return keyConsumed
			case tea.KeyBackspace, tea.KeyCtrlH:
				runes := []rune(m.rewindSummaryGuidance)
				if len(runes) > 0 {
					m.rewindSummaryGuidance = string(runes[:len(runes)-1])
				}
				return keyConsumed
			}
		}
	}
	if m.rewindChoosingAction && msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] >= '1' && msg.Runes[0] <= '9' {
		action := int(msg.Runes[0] - '1')
		if action < limit {
			start, end := m.rewindVisibleRange(limit, m.rewindAction)
			m.rewindAction = action
			if action < start || action >= end {
				return keyConsumed
			}
			m.rewindSummaryGuidance = ""
			return m.handleRewindKeys(tea.KeyMsg{Type: tea.KeyEnter})
		}
	}
	switch msg.Type {
	case tea.KeyUp:
		if *selected > 0 {
			*selected--
		}
	case tea.KeyDown:
		if *selected+1 < limit {
			*selected++
		}
	case tea.KeyHome:
		*selected = 0
	case tea.KeyEnd:
		if limit > 0 {
			*selected = limit - 1
		}
	case tea.KeyEnter:
		if limit == 0 || *selected < 0 || *selected >= limit {
			return keyConsumed
		}
		if !m.rewindChoosingAction {
			if previous := m.rewindChoices[m.rewindSelected].PreviousSessionID; previous != "" {
				m.rewindChoices = nil
				_ = m.restoreTransientOverlay()
				return m.submitRewindCommand("/rewind previous " + strconv.Quote(previous))
			}
			m.rewindChoosingAction = true
			m.rewindAction = 0
			m.rewindSummaryGuidance = ""
			return keyConsumed
		}
		action := m.rewindActions()[m.rewindAction]
		if action == "cancel" {
			m.rewindChoosingAction = false
			return keyConsumed
		}
		id := m.rewindChoices[m.rewindSelected].ID
		// Choices arrive from the runtime, but still reject malformed identifiers
		// rather than interpolating arbitrary text into a slash command.
		if len(id) != 32 || strings.IndexFunc(id, func(r rune) bool { return !strings.ContainsRune("0123456789abcdef", r) }) >= 0 {
			return keyConsumed
		}
		m.rewindChoices = nil
		m.rewindChoosingAction = false
		_ = m.restoreTransientOverlay()
		command := "/rewind " + id + " " + action
		if (action == "summarize-from" || action == "summarize-before") && m.rewindSummaryGuidance != "" {
			command += " -- " + strconv.Quote(m.rewindSummaryGuidance)
		}
		m.rewindSummaryGuidance = ""
		return m.submitRewindCommand(command)
	}
	return keyConsumed
}
func cleanRewindLabel(value string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)), " ")
}
func (m *Model) renderRewindMenu() string {
	width := max(1, m.width-4)
	if m.width < 24 || m.height < 8 {
		return truncateForWidth("Rewind · resize · Esc closes", width)
	}
	rows := []string{"Rewind to before a prompt"}
	labels := make([]string, len(m.rewindChoices))
	selected := m.rewindSelected
	pointNumber := 0
	for i, p := range m.rewindChoices {
		if p.PreviousSessionID != "" {
			labels[i] = cleanRewindLabel(p.Prompt)
			continue
		}
		pointNumber++
		suffix := ""
		if !p.HasCode {
			suffix = " · conversation only"
		}
		labels[i] = fmt.Sprintf("%d. %s%s", pointNumber, cleanRewindLabel(p.Prompt), suffix)
	}
	if m.rewindChoosingAction {
		labels = nil
		selected = m.rewindAction
		for _, action := range m.rewindActions() {
			switch action {
			case "both":
				labels = append(labels, "Restore code and conversation")
			case "conversation":
				labels = append(labels, "Restore conversation")
			case "code":
				labels = append(labels, "Restore code")
			case "summarize-from":
				labels = append(labels, "Summarize from here")
			case "summarize-before":
				labels = append(labels, "Summarize up to here")
			case "cancel":
				labels = append(labels, "Never mind")
			}
		}
		for i := range labels {
			labels[i] = fmt.Sprintf("%d. %s", i+1, labels[i])
		}
	}
	start, end := m.rewindVisibleRange(len(labels), selected)
	for i := start; i < end; i++ {
		prefix := "  "
		if i == selected {
			prefix = "> "
		}
		rows = append(rows, truncateForWidth(prefix+labels[i], width))
	}
	if m.rewindChoosingAction && selected >= 0 && selected < len(m.rewindActions()) {
		action := m.rewindActions()[selected]
		if action == "summarize-from" || action == "summarize-before" {
			guide := m.rewindSummaryGuidance
			if guide == "" {
				guide = "add context (optional)"
			}
			rows = append(rows, "  "+cleanRewindLabel(guide))
		}
	}
	rows = append(rows, "↑/↓ select · Enter · Esc back")
	for i := range rows {
		rows[i] = truncateForWidth(rows[i], width)
	}
	return strings.Join(rows, "\n")
}

func (m *Model) rewindVisibleRange(total, selected int) (int, int) {
	visible := max(1, min(10, m.height-7))
	start := max(0, selected-visible+1)
	return start, min(total, start+visible)
}
