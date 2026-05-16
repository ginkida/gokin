package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// welcomeWordmark is the 3-line ASCII wordmark shown on the idle/welcome
// screen. Box-drawing glyphs render cleanly across every monospace font
// (no fallback boxes), and the silhouette stays light enough not to fight
// the chat content visually.
var welcomeWordmark = [3]string{
	"  ┌─┐ ┌─┐ ┬┌─ ┬ ┌┐┌",
	"  │ ┬ │ │ ├┴┐ │ │││",
	"  └─┘ └─┘ ┴ ┴ ┴ ┘└┘",
}

// renderWelcomePanel returns the multi-line idle screen shown when the
// output buffer is empty and the user is at input. Replaces the prior
// one-line "Type a message…" hint.
//
// Layout (Gokin Classic scene A):
//
//	┌─┐ ┌─┐ ┬┌─ ┬ ┌┐┌
//	│ ┬ │ │ ├┴┐ │ │││         ← violet wordmark
//	└─┘ └─┘ ┴ ┴ ┴ ┘└┘
//	v0.82.4                   ← dim version (v-prefixed)
//
//	tips                      ← muted section header
//	  type a question or paste a stack trace
//	  / for slash commands · @ to pin a file
//	  ⌘K to switch model    · ? for shortcuts
//
//	project                   ← muted section header (omitted if no data)
//	  ~/code/payments-api
//	  branch: main
//
//	Plan mode is on — …       ← mode hint (only when active)
//
// Width-aware in the sense that long paths get middle-truncated; we don't
// attempt to wrap or rewrap any of the static lines, so very narrow
// terminals (< ~50 cols) will see the static text overflow rather than
// crash or misalign. That's the right trade-off — narrower than 50 is
// unsupported territory and the status bar still works.
func (m Model) renderWelcomePanel() string {
	var b strings.Builder

	logoStyle := lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	versionStyle := lipgloss.NewStyle().Foreground(ColorDim)
	headerStyle := lipgloss.NewStyle().Foreground(ColorMuted).Bold(true)
	tipStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	pathStyle := lipgloss.NewStyle().Foreground(ColorText)
	branchStyle := lipgloss.NewStyle().Foreground(ColorSecondary)
	kbdStyle := lipgloss.NewStyle().Foreground(ColorHighlight)

	// Wordmark + version.
	for _, line := range welcomeWordmark {
		b.WriteString(logoStyle.Render(line))
		b.WriteString("\n")
	}
	if v := strings.TrimSpace(m.version); v != "" {
		// Prefix "v" so the line reads as a tag (v0.81.1), not a bare
		// number. Idempotent — already-"v"-prefixed strings pass through.
		if !strings.HasPrefix(v, "v") && !strings.HasPrefix(v, "V") {
			v = "v" + v
		}
		b.WriteString("  ")
		b.WriteString(versionStyle.Render(v))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Tips section.
	b.WriteString(headerStyle.Render("  tips"))
	b.WriteString("\n")
	b.WriteString(tipStyle.Render("    type a question, or paste a stack trace"))
	b.WriteString("\n")
	b.WriteString(tipStyle.Render("    "))
	b.WriteString(kbdStyle.Render("/"))
	b.WriteString(tipStyle.Render(" for slash commands · "))
	b.WriteString(kbdStyle.Render("@"))
	b.WriteString(tipStyle.Render(" to pin a file"))
	b.WriteString("\n")
	b.WriteString(tipStyle.Render("    "))
	b.WriteString(kbdStyle.Render("⌘K"))
	b.WriteString(tipStyle.Render(" switches model · "))
	b.WriteString(kbdStyle.Render("?"))
	b.WriteString(tipStyle.Render(" for shortcuts"))
	b.WriteString("\n")
	if sel := getTextSelectionHint(); sel != "" {
		b.WriteString(tipStyle.Render("    " + sel))
		b.WriteString("\n")
	}

	// Project section — only when we actually have something to show.
	if section := m.welcomeProjectSection(headerStyle, pathStyle, branchStyle, tipStyle); section != "" {
		b.WriteString("\n")
		b.WriteString(section)
	}

	// Mode hint carried over from the prior 1-line welcome — separate line
	// so it doesn't blend into the tips block.
	if hint := m.welcomeModeHint(); hint != "" {
		b.WriteString("\n  ")
		b.WriteString(hint)
		b.WriteString("\n")
	}

	return b.String()
}

// welcomeProjectSection renders the project block. Returns "" when there's
// no project context to show (fresh shell in an unfamiliar cwd).
func (m Model) welcomeProjectSection(
	headerStyle, pathStyle, branchStyle, tipStyle lipgloss.Style,
) string {
	pretty := statusBarProjectPath(m.workDir)
	if pretty == "" && m.projectName == "" && m.gitBranch == "" {
		return ""
	}

	var b strings.Builder
	b.WriteString(headerStyle.Render("  project"))
	b.WriteString("\n")

	if pretty != "" {
		display := pretty
		if m.projectName != "" {
			display += " (" + m.projectName + ")"
		}
		b.WriteString("    ")
		b.WriteString(pathStyle.Render(display))
		b.WriteString("\n")
	}
	if m.gitBranch != "" {
		b.WriteString("    ")
		b.WriteString(tipStyle.Render("branch: "))
		b.WriteString(branchStyle.Render(m.gitBranch))
		b.WriteString("\n")
	}

	return b.String()
}

// welcomeModeHint returns the same mode-hint string the prior 1-line
// welcome rendered, styled for visibility. Returns "" when no mode is
// active (default permissioned execution).
func (m Model) welcomeModeHint() string {
	switch {
	case m.planningModeEnabled:
		return lipgloss.NewStyle().Foreground(ColorWarning).Render(
			"Plan mode is on — agent will explore, then propose a plan for approval.",
		)
	case !m.permissionsEnabled || !m.sandboxEnabled:
		return lipgloss.NewStyle().Foreground(ColorError).Render(
			"YOLO mode — agent runs everything without asking. Shift+Tab to step back.",
		)
	}
	return ""
}
