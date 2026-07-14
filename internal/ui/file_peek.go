package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// FilePeekPanel shows a compact one-line indicator of what file the agent is
// touching right now. It replaces a much larger bordered panel that rendered
// up to 10 lines of file content — feedback from real use was that the big
// panel drew too much attention and hid the answer the user was waiting for,
// while also burying the file path the user actually wanted to see.
//
// Current design: single line, no border, filename front-and-centre, dim
// metadata. Lives for ~1.5 seconds (matching success-toast duration) unless
// replaced by a fresher peek first.
type FilePeekPanel struct {
	visible bool
	peek    FilePeekMsg
	styles  *Styles
	expires time.Time
}

// filePeekTTL is how long a peek stays on screen if no newer one arrives.
// Kept short so a rapid read → edit → read sequence doesn't smear multiple
// stale indicators on top of each other.
const filePeekTTL = 1500 * time.Millisecond

// NewFilePeekPanel creates a new file peek panel.
func NewFilePeekPanel(styles *Styles) *FilePeekPanel {
	return &FilePeekPanel{styles: styles}
}

// ShowPeek installs a new peek and resets the TTL. Calling this while an
// earlier peek is still on screen simply replaces it — no stacking.
func (p *FilePeekPanel) ShowPeek(msg FilePeekMsg) {
	p.peek = msg
	p.visible = true
	p.expires = time.Now().Add(filePeekTTL)
}

// Hide hides the panel.
func (p *FilePeekPanel) Hide() {
	p.visible = false
}

// Tick is called on the UI frame timer to evict expired peeks.
func (p *FilePeekPanel) Tick() {
	if p.visible && time.Now().After(p.expires) {
		p.visible = false
	}
}

// IsVisible reports whether the panel should render this frame.
func (p *FilePeekPanel) IsVisible() bool {
	return p.visible && p.peek.FilePath != ""
}

// View renders the panel as a single dim status line. Returns "" when the
// panel is hidden or has no data — callers don't need to nil-check.
func (p *FilePeekPanel) View(width int) string {
	if !p.IsVisible() {
		return ""
	}

	icon := actionIcon(p.peek.Action)
	verb := actionVerb(p.peek.Action)
	if width <= 0 {
		width = 80
	}
	meta := formatPeekMeta(p.peek.Content)
	color := actionColor(p.peek.Action)

	label := fmt.Sprintf("%s %s ", icon, verb)
	if lipgloss.Width(label) >= width {
		label = icon + " "
		if lipgloss.Width(label) >= width {
			return lipgloss.NewStyle().Foreground(color).Render(truncateForWidth(icon, width))
		}
	}
	pathBudget := max(width-lipgloss.Width(label), 1)
	path := truncateForWidth(shortenPath(p.peek.FilePath, pathBudget), pathBudget)

	primary := lipgloss.NewStyle().
		Foreground(color).
		Render(label)
	pathStyled := lipgloss.NewStyle().
		Foreground(ColorText).
		Render(path)

	line := primary + pathStyled
	if meta != "" && lipgloss.Width(line)+2+lipgloss.Width(meta) <= width {
		metaStyled := lipgloss.NewStyle().
			Foreground(ColorDim).
			Render("  " + meta)
		line += metaStyled
	}
	return line
}

// actionIcon maps an action verb to an icon using the unified style system.
// Unknown actions fall back to a generic document icon so we never render a blank.
func actionIcon(action string) string {
	normalized := strings.ToLower(action)
	if icon, ok := FilePeekIcons[normalized]; ok {
		return icon
	}
	return FilePeekIcons["default"]
}

// actionColor returns the semantic color for a file peek action.
func actionColor(action string) lipgloss.Color {
	normalized := strings.ToLower(action)
	if color, ok := FilePeekColors[normalized]; ok {
		return color
	}
	return ColorMuted
}

// actionVerb picks a concise display verb. The raw action strings in the code
// are a mix of tenses ("read", "reading", "modifying", "created") — normalise
// to present participles for a consistent status line.
func actionVerb(action string) string {
	normalized := strings.ToLower(strings.TrimSpace(action))
	switch normalized {
	case "read", "reading":
		return "Reading"
	case "write":
		return "Writing"
	case "created":
		return "Created"
	case "edit", "editing":
		return "Editing"
	case "modifying":
		return "Modifying"
	case "inserting":
		return "Inserting"
	}
	if normalized == "" {
		return "Touching"
	}
	first, size := utf8.DecodeRuneInString(normalized)
	return string(unicode.ToUpper(first)) + normalized[size:]
}

// formatPeekMeta summarises the snippet payload as "N lines · X.Y KB". Returns
// "" when there's nothing to show so the View can skip the metadata chunk.
func formatPeekMeta(content string) string {
	if content == "" {
		return ""
	}
	lines := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		lines++
	}
	size := len(content)
	switch {
	case size >= 1024*1024:
		return fmt.Sprintf("%d lines · %.1f MB", lines, float64(size)/(1024*1024))
	case size >= 1024:
		return fmt.Sprintf("%d lines · %.1f KB", lines, float64(size)/1024)
	default:
		return fmt.Sprintf("%d lines · %d B", lines, size)
	}
}
