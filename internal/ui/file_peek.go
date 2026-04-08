package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// FilePeekPanel displays a transient high-resolution snippet of a file.
type FilePeekPanel struct {
	visible bool
	peek    FilePeekMsg
	styles  *Styles
	frame   int
	expires time.Time
}

// NewFilePeekPanel creates a new file peek panel.
func NewFilePeekPanel(styles *Styles) *FilePeekPanel {
	return &FilePeekPanel{
		visible: false,
		styles:  styles,
	}
}

// ShowPeek displays a new peek and sets its expiration.
func (p *FilePeekPanel) ShowPeek(msg FilePeekMsg) {
	p.peek = msg
	p.visible = true
	p.expires = time.Now().Add(5 * time.Second)
	p.frame = 0
}

// Hide hides the panel.
func (p *FilePeekPanel) Hide() {
	p.visible = false
}

// Tick updates the animation and checks for expiration.
func (p *FilePeekPanel) Tick() {
	p.frame++
	if p.visible && time.Now().After(p.expires) {
		p.visible = false
	}
}

// View renders the file peek panel.
func (p *FilePeekPanel) View(width int) string {
	if !p.visible || p.peek.FilePath == "" {
		return ""
	}

	// Panel constraints
	panelWidth := width - 10
	if panelWidth < 40 {
		panelWidth = 40
	}
	if panelWidth > 100 {
		panelWidth = 100
	}

	// Styles
	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorSecondary).
		Padding(0, 1)

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorSecondary)

	actionStyle := lipgloss.NewStyle().
		Italic(true).
		Foreground(ColorDim)

	contentStyle := lipgloss.NewStyle().
		Foreground(ColorText)

	var builder strings.Builder

	// Header line
	icon := "📄"
	switch p.peek.Action {
	case "reading":
		icon = "🔍"
	case "modifying":
		icon = "📝"
	case "created":
		icon = "✨"
	}

	title := fmt.Sprintf("%s %s", icon, p.peek.FilePath)
	if p.peek.Title != "" {
		title = p.peek.Title
	}

	header := headerStyle.Render(title)
	action := actionStyle.Render(" (" + p.peek.Action + ")")
	
	builder.WriteString(header)
	builder.WriteString(action)
	builder.WriteString("\n\n")

	// Content with line numbers if needed (or just raw)
	lines := strings.Split(p.peek.Content, "\n")
	maxLines := 10
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines = append(lines, lipgloss.NewStyle().Foreground(ColorDim).Render("... (truncated)"))
	}

	for _, line := range lines {
		// Ensure line doesn't overflow panelWidth
		if lipgloss.Width(line) > panelWidth-4 {
			line = line[:panelWidth-7] + "..."
		}
		builder.WriteString(contentStyle.Render(line) + "\n")
	}

	return borderStyle.Width(panelWidth).Render(builder.String())
}
