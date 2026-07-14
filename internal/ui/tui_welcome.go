package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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
// output buffer is empty and the user is at input.
//
// Layout (Gokin Classic scene A):
//
//	┌─┐ ┌─┐ ┬┌─ ┬ ┌┐┌
//	│ ┬ │ │ ├┴┐ │ │││         ← violet wordmark (single brand color)
//	└─┘ └─┘ ┴ ┴ ┴ ┘└┘
//	v0.84.8                   ← dim version
//
//	tips                      ← muted section header
//	  type a question or paste a stack trace
//	  / for slash commands  ·  @ to pin a file
//	  Ctrl+K switches model ·  ? for shortcuts
//
//	project                   ← muted section header (omitted if no data)
//	  ~/code/payments-api
//	  branch: main
//
//	Plan mode is on — …       ← mode hint (only when active)
//
// A previous pass split each letter (g/o/k/i/n) into its own color hue —
// that broke the brand mark into a "rainbow logo" feel and competed with
// the chat content below. Mono-violet reads as a tag, which is what a
// brand-mark on an idle screen should be.
func (m Model) renderWelcomePanel() string {
	logoStyle := lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	versionStyle := lipgloss.NewStyle().Foreground(ColorDim)
	headerStyle := lipgloss.NewStyle().Foreground(ColorMuted).Bold(true)
	tipStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	pathStyle := lipgloss.NewStyle().Foreground(ColorText)
	branchStyle := lipgloss.NewStyle().Foreground(ColorSecondary)
	// Key-cap look: bold accent on a muted bracket. No background pill —
	// background-filled pills competed with the body type and looked
	// like cells in a textbook table when stacked 4-deep in the tips
	// block. Brackets carry the cap metaphor without the visual weight.
	kbdStyle := lipgloss.NewStyle().Foreground(ColorHighlight).Bold(true)
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}

	version := safeKeyEntryText(m.version)
	if version != "" && !strings.HasPrefix(version, "v") && !strings.HasPrefix(version, "V") {
		version = "v" + version
	}

	// Tiny terminals need recovery/discovery affordances more than a
	// decorative wordmark. Both essentials fit on a nine-cell line.
	if width < 28 || height < 11 {
		title := logoStyle.Render("Gokin")
		if version != "" && width >= 14 {
			title += versionStyle.Render(" " + version)
		}
		return joinWelcomeLines(width, max(height-4, 2), []string{
			title,
			kbdStyle.Render("Ctrl+P") + tipStyle.Render("  ") + kbdStyle.Render("?"),
		})
	}

	// Medium-width or short terminals use a dense quick-start instead of the
	// 3-row logo and multi-line project card. This preserves room for input.
	if width < 60 || height < 22 {
		lines := []string{logoStyle.Render("Gokin")}
		if version != "" {
			lines[0] += versionStyle.Render("  " + version)
		}
		lines = append(lines,
			tipStyle.Render("Type a question or paste an error"),
			kbdStyle.Render("Ctrl+P")+tipStyle.Render(" actions  ·  ")+kbdStyle.Render("?")+tipStyle.Render(" shortcuts"),
			kbdStyle.Render("/")+tipStyle.Render(" commands  ·  ")+kbdStyle.Render("@")+tipStyle.Render(" files"),
		)
		if project := m.welcomeProjectSummary(); project != "" {
			lines = append(lines, pathStyle.Render(project))
		}
		if hint := m.welcomeModeHint(); hint != "" {
			lines = append(lines, hint)
		}
		return joinWelcomeLines(width, max(height-4, 2), lines)
	}

	var lines []string

	// Wordmark + version.
	for _, line := range welcomeWordmark {
		lines = append(lines, logoStyle.Render(line))
	}
	if version != "" {
		lines = append(lines, "  "+versionStyle.Render(version))
	}
	lines = append(lines, "")

	// Tips section.
	lines = append(lines,
		headerStyle.Render("  tips"),
		tipStyle.Render("    type a question, or paste a stack trace"),
		tipStyle.Render("    ")+kbdStyle.Render("/")+tipStyle.Render(" for slash commands · ")+kbdStyle.Render("@")+tipStyle.Render(" to pin a file"),
		tipStyle.Render("    ")+kbdStyle.Render("Ctrl+P")+tipStyle.Render(" all actions · ")+kbdStyle.Render("Ctrl+S")+tipStyle.Render(" settings · ")+kbdStyle.Render("Ctrl+K")+tipStyle.Render(" model · ")+kbdStyle.Render("?")+tipStyle.Render(" shortcuts"),
	)
	if sel := getTextSelectionHint(); sel != "" {
		lines = append(lines, tipStyle.Render("    "+safeKeyEntryText(sel)))
	}

	// Project section — only when we actually have something to show.
	if section := m.welcomeProjectSection(headerStyle, pathStyle, branchStyle, tipStyle); section != "" {
		lines = append(lines, "")
		lines = append(lines, strings.Split(strings.TrimSuffix(section, "\n"), "\n")...)
	}

	// Mode hint carried over from the prior 1-line welcome.
	if hint := m.welcomeModeHint(); hint != "" {
		lines = append(lines, "", "  "+hint)
	}

	return joinWelcomeLines(width, max(height-4, 2), lines)
}

// joinWelcomeLines is the welcome surface's geometry safety rail. Four rows
// are reserved for input and status; styled lines are ANSI-aware truncated so
// runtime metadata can never wrap the terminal.
func joinWelcomeLines(width, maxLines int, lines []string) string {
	maxLines = min(max(maxLines, 1), len(lines))
	lines = lines[:maxLines]
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func (m Model) welcomeProjectSummary() string {
	parts := make([]string, 0, 2)
	project := safeKeyEntryText(statusBarProjectPath(m.workDir))
	name := safeKeyEntryText(m.projectName)
	if project != "" {
		if name != "" {
			project += " (" + name + ")"
		}
		parts = append(parts, project)
	} else if name != "" {
		parts = append(parts, name)
	}
	if branch := safeKeyEntryText(m.gitBranch); branch != "" {
		parts = append(parts, "branch: "+branch)
	}
	return strings.Join(parts, "  ·  ")
}

// welcomeProjectSection renders the project block. Returns "" when there's
// no project context to show (fresh shell in an unfamiliar cwd).
func (m Model) welcomeProjectSection(
	headerStyle, pathStyle, branchStyle, tipStyle lipgloss.Style,
) string {
	pretty := safeKeyEntryText(statusBarProjectPath(m.workDir))
	projectName := safeKeyEntryText(m.projectName)
	gitBranch := safeKeyEntryText(m.gitBranch)
	if pretty == "" && projectName == "" && gitBranch == "" {
		return ""
	}

	var b strings.Builder
	b.WriteString(headerStyle.Render("  project"))
	b.WriteString("\n")

	if pretty != "" {
		display := pretty
		if projectName != "" {
			display += " (" + projectName + ")"
		}
		b.WriteString("    ")
		b.WriteString(pathStyle.Render(display))
		b.WriteString("\n")
	} else if projectName != "" {
		b.WriteString("    ")
		b.WriteString(pathStyle.Render(projectName))
		b.WriteString("\n")
	}
	if gitBranch != "" {
		b.WriteString("    ")
		b.WriteString(tipStyle.Render("branch: "))
		b.WriteString(branchStyle.Render(gitBranch))
		b.WriteString("\n")
	}

	return b.String()
}

// welcomeModeHint renders the active mode on the recurring idle panel. Normal
// mode stays clean (no badge — the common case shouldn't carry chrome); for
// plan/YOLO it reuses the EXACT wording of the first-run box (welcomeModeBadge)
// so the two welcome surfaces can't drift in glyph or prose.
func (m Model) welcomeModeHint() string {
	if m.permissionsEnabled && m.sandboxEnabled && !m.planningModeEnabled {
		return ""
	}
	return m.welcomeModeBadge()
}
