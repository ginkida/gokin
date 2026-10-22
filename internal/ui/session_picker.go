package ui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"gokin/internal/chat"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type SessionChoice struct {
	ID, Summary, Detail string
	Current             bool
}

type OpenSessionPickerMsg struct {
	Choices []SessionChoice
	Note    string
}

type sessionPicker struct {
	choices   []SessionChoice
	haystacks []string
	filtered  []int
	selected  int
	query     string
	note      string
}

func (p *sessionPicker) filter() {
	p.filtered = nil
	terms := strings.Fields(strings.ToLower(p.query))
	for i, haystack := range p.haystacks {
		matches := true
		for _, term := range terms {
			if !strings.Contains(haystack, term) {
				matches = false
				break
			}
		}
		if matches {
			p.filtered = append(p.filtered, i)
		}
	}
	p.selected = 0
}

func (m *Model) openSessionPicker(msg OpenSessionPickerMsg) {
	if m.isModalState() {
		if m.toastManager != nil {
			m.toastManager.ShowInfo("Close the current dialog, then run /resume again")
		}
		return
	}
	p := &sessionPicker{note: cleanRewindLabel(msg.Note)}
	for _, choice := range msg.Choices {
		if chat.ValidateSessionID(choice.ID) != nil {
			continue
		}
		choice.Summary = cleanRewindLabel(choice.Summary)
		choice.Detail = cleanRewindLabel(choice.Detail)
		p.choices = append(p.choices, choice)
		p.haystacks = append(p.haystacks, strings.ToLower(choice.ID+" "+choice.Summary+" "+choice.Detail))
	}
	p.filter()
	m.sessionPicker = p
	m.enterTransientOverlay(StateSessionPicker)
}

func (m *Model) sessionPickerReadable() bool {
	width, height := m.width, m.height
	if m.pendingResize != nil {
		width = min(width, m.pendingResize.Width)
		height = min(height, m.pendingResize.Height)
	}
	return width >= 32 && height >= 8
}

func (m *Model) sessionPickerPageSize() int { return max((m.height-5)/2, 1) }

func (p *sessionPicker) move(delta int) {
	p.selected = min(max(p.selected+delta, 0), max(len(p.filtered)-1, 0))
}

func (m *Model) handleSessionPickerKeys(msg tea.KeyMsg) tea.Cmd {
	p := m.sessionPicker
	if msg.Type == tea.KeyEscape || msg.Type == tea.KeyCtrlC {
		m.sessionPicker = nil
		m.armSurfaceExitKeyGuard(msg.String())
		return m.restoreTransientOverlay()
	}
	if p == nil {
		return keyConsumed
	}
	switch msg.Type {
	case tea.KeyUp, tea.KeyCtrlP:
		p.move(-1)
	case tea.KeyDown, tea.KeyCtrlN:
		p.move(1)
	case tea.KeyPgUp:
		p.move(-m.sessionPickerPageSize())
	case tea.KeyPgDown:
		p.move(m.sessionPickerPageSize())
	case tea.KeyHome:
		p.selected = 0
	case tea.KeyEnd:
		p.selected = max(len(p.filtered)-1, 0)
	case tea.KeyEnter:
		if !m.sessionPickerReadable() || p.selected >= len(p.filtered) || m.onSubmit == nil {
			return keyConsumed
		}
		choice := p.choices[p.filtered[p.selected]]
		if chat.ValidateSessionID(choice.ID) != nil {
			return keyConsumed
		}
		m.sessionPicker = nil
		m.armModalEnterGuard()
		_ = m.restoreTransientOverlay()
		return m.submitPaletteLine("/resume " + strconv.Quote(choice.ID))
	case tea.KeyCtrlU:
		p.query = ""
		p.filter()
	case tea.KeyBackspace, tea.KeyCtrlH:
		p.query = removeLastGrapheme(p.query)
		p.filter()
	case tea.KeySpace, tea.KeyRunes:
		if msg.Alt {
			return keyConsumed
		}
		text := string(msg.Runes)
		if msg.Type == tea.KeySpace {
			text = " "
		}
		text = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, text)
		if utf8.RuneCountInString(p.query)+utf8.RuneCountInString(text) <= 256 {
			p.query += text
			p.filter()
		}
	}
	return keyConsumed
}

func (m Model) renderSessionPicker() string {
	width, height := max(m.width, 1), max(m.height, 1)
	if width < 32 || height < 8 || m.sessionPicker == nil {
		return truncateForWidth("Resume · resize · Esc closes", width)
	}
	p := m.sessionPicker
	title := "Resume session · current project"
	rows := []string{lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render(truncateForWidth(title, width))}
	query := p.query
	if query == "" {
		query = "type to search by topic or ID"
	}
	rows = append(rows, truncateForWidth("Search: "+query, width), "")
	page := m.sessionPickerPageSize()
	start := min(max(p.selected-page/2, 0), max(len(p.filtered)-page, 0))
	end := min(start+page, len(p.filtered))
	for _, index := range p.filtered[start:end] {
		choice := p.choices[index]
		summary := choice.Summary
		if summary == "" {
			summary = choice.ID
		}
		if choice.Current {
			summary += " · current"
		}
		prefix := "  "
		selected := p.filtered[p.selected] == index
		if selected {
			prefix = "› "
		}
		label := truncateForWidth(prefix+summary, width)
		if selected {
			label = lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render(label)
		}
		rows = append(rows, label, truncateForWidth("  "+choice.ID+" · "+choice.Detail, width))
	}
	if len(p.filtered) == 0 {
		rows = append(rows, truncateForWidth("No matching sessions · Ctrl+U clears search", width))
	}
	for len(rows) < height-2 {
		rows = append(rows, "")
	}
	note := fmt.Sprintf("%d of %d sessions", len(p.filtered), len(p.choices))
	if p.note != "" {
		note += " · " + p.note
	}
	rows = append(rows, truncateForWidth(note, width), truncateForWidth("↑/↓ Select · Enter Resume · Esc Cancel · Ctrl+U Clear", width))
	return strings.Join(rows, "\n")
}
