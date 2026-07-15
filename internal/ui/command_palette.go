package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// CommandType defines what kind of command this is
type CommandType int

const (
	CommandTypeSlash  CommandType = iota // Executes a slash command
	CommandTypeAction                    // Executes a direct action
)

// PaletteCategoryInfo contains display information for a category (local copy to avoid import cycle).
type PaletteCategoryInfo struct {
	ID       string
	Name     string
	Icon     string
	Priority int
}

// PaletteCommandData is the interface that command data must implement.
type PaletteCommandData interface {
	GetName() string
	GetDescription() string
	GetUsage() string
	GetCategoryName() string
	GetCategoryIcon() string
	GetCategoryPriority() int
	GetIcon() string
	GetArgHint() string
	IsEnabled() bool
	GetReason() string
	GetPriority() int
	IsAdvanced() bool
}

// PaletteProvider is an interface for fetching palette commands.
// Uses any slice to avoid import cycle issues - caller must return []PaletteCommandData.
type PaletteProvider interface {
	GetPaletteCommandsForUI() []any
}

// EnhancedPaletteCommand represents a command in the enhanced command palette.
type EnhancedPaletteCommand struct {
	Name        string
	Description string
	Usage       string
	Shortcut    string // /command format or keyboard shortcut
	Category    PaletteCategoryInfo
	Icon        string
	ArgHint     string
	Enabled     bool
	Reason      string // Why disabled
	Priority    int    // For sorting
	IsRecent    bool   // In recently used
	Type        CommandType
	Action      func() // Direct action (for CommandTypeAction) — LEGACY: closures
	// captured at registration mutate a DETACHED Model copy in production (the
	// Bubble Tea value-receiver trap), so they only work for shared-pointer /
	// external-callback side effects. Prefer ActionID for anything that mutates
	// Model fields (state, panel-visible bools): it is dispatched on the LIVE
	// model in handleCommandPaletteKeys. Action is kept for tests + back-compat.
	ActionID string // Stable id dispatched on the live model (see dispatchPaletteAction)
	Advanced bool   // Hidden from default view, visible when searching
}

// CommandPalette provides quick access to commands via Ctrl+P.
type CommandPalette struct {
	visible         bool
	query           string
	commands        []EnhancedPaletteCommand
	filtered        []EnhancedPaletteCommand
	selected        int
	styles          *Styles
	maxHeight       int
	scroll          int
	width           int
	height          int
	history         *CommandHistory
	showPreview     bool
	previewCmd      *EnhancedPaletteCommand
	paletteProvider PaletteProvider
	actionCommands  []EnhancedPaletteCommand
	commandAliases  map[string]string

	// Inline argument entry: when the user picks a slash command that needs an
	// argument (ArgHint contains "<"), the palette switches into a one-line arg
	// field instead of running the verb bare (which would only print a Usage
	// error). This is what makes the palette no-typing for arg-taking commands
	// too — you pick from the list, then fill just the value.
	argEntry     bool
	argCmd       EnhancedPaletteCommand
	argValue     string
	argError     string
	submitError  string
	submitLinked bool
	submitKnown  bool
}

// NewCommandPalette creates a new command palette.
func NewCommandPalette(styles *Styles) *CommandPalette {
	return &CommandPalette{
		visible:   false,
		query:     "",
		commands:  nil,
		filtered:  nil,
		selected:  0,
		styles:    styles,
		maxHeight: 20,
		scroll:    0,
		history:   NewCommandHistory(),
	}
}

// SetPaletteProvider sets the provider for fetching commands.
func (p *CommandPalette) SetPaletteProvider(provider PaletteProvider) {
	p.paletteProvider = provider
}

// SetSubmissionLinked tells the reusable palette whether its parent can send
// slash commands. Direct UI actions remain runnable without this link.
func (p *CommandPalette) SetSubmissionLinked(linked bool) {
	p.submitLinked = linked
	p.submitKnown = true
}

func (p *CommandPalette) submissionAvailable() bool {
	// Preserve standalone rendering semantics until an owning Model explicitly
	// declares responsibility for slash-command dispatch.
	return !p.submitKnown || p.submitLinked
}

func (p *CommandPalette) selectedRunnable() bool {
	selected := p.GetSelected()
	if selected == nil || !selected.Enabled {
		return false
	}
	return selected.Type != CommandTypeSlash || p.submissionAvailable()
}

const (
	compactPaletteListTargetMinWidth = 7 // border + padding + "> " + one target cell
	compactPaletteArgTargetMinWidth  = 9 // border + padding + "Run " + one target cell
	paletteSelectionResizeError      = "Resize to reveal the selected command before running it"
	paletteArgumentResizeError       = "Resize to reveal the command and argument before running it"
)

// targetVisibleForEnter reports whether compact rendering has enough rows to
// show the command that Enter would execute. The compact body always reserves
// its final row for recovery keys and gives the filter a higher priority than
// the selected target, so the target first becomes visible with two body rows
// (terminal height five). Zero is the pre-WindowSize sentinel used by tests and
// embedders, not a real one-cell terminal; preserve its historical behavior.
func (p *CommandPalette) targetVisibleForEnter() bool {
	if p.width <= 0 || p.height <= 0 {
		return true
	}
	paletteWidth := commandPaletteWidth(p.width)
	if paletteWidth < compactPaletteListTargetMinWidth {
		return false
	}
	if paletteWidth >= 32 && p.height >= 14 {
		return true
	}
	contentRows := max(p.height-2, 1)
	bodyRows := max(contentRows-1, 0)
	return bodyRows >= 2
}

// argTargetVisibleForEnter additionally requires the compact "Run <command>"
// row and argument field. The final frame reserves a status row, so at four
// rows the field may survive while the command identity is still cropped.
func (p *CommandPalette) argTargetVisibleForEnter() bool {
	if p.width <= 0 || p.height <= 0 {
		return true
	}
	paletteWidth := commandPaletteWidth(p.width)
	if paletteWidth < compactPaletteArgTargetMinWidth {
		return false
	}
	if paletteWidth >= 32 && p.height >= 12 {
		return true
	}
	minHeight := 5
	if p.argError != "" {
		// The inline error is higher-priority than the command identity. Require
		// its extra row before Enter can act, so the target remains fully visible.
		minHeight++
	}
	return p.height >= minHeight
}

// SetActionCommands sets direct action commands (keyboard shortcuts) for the palette.
func (p *CommandPalette) SetActionCommands(actions []EnhancedPaletteCommand) {
	p.actionCommands = append([]EnhancedPaletteCommand(nil), actions...)
	// Live config/results can change an action's direction or availability while
	// the palette is open. Refresh the visible snapshot immediately; RefreshCommands
	// keeps the current query and normalizes the existing selection.
	if p.visible {
		p.RefreshCommands()
	}
}

func (p *CommandPalette) SetCommandAliases(aliases map[string]string) {
	if aliases == nil {
		p.commandAliases = nil
		return
	}
	out := make(map[string]string, len(aliases))
	for k, v := range aliases {
		key := safeKeyEntryProvider(k)
		value := safeKeyEntryProvider(v)
		if key != "" && value != "" {
			out[strings.ToLower(key)] = strings.ToLower(value)
		}
	}
	p.commandAliases = out
}

// RefreshCommands refreshes the command list from the provider.
func (p *CommandPalette) RefreshCommands() {
	recentCmds := p.history.GetRecentCommands(5)
	recentSet := make(map[string]bool)
	for _, c := range recentCmds {
		recentSet[c] = true
	}

	var paletteCmds []any
	if p.paletteProvider != nil {
		paletteCmds = p.paletteProvider.GetPaletteCommandsForUI()
	}

	p.commands = make([]EnhancedPaletteCommand, 0, len(paletteCmds)+len(p.actionCommands))
	for _, item := range paletteCmds {
		pc, ok := item.(PaletteCommandData)
		if !ok {
			continue
		}
		name := safeKeyEntryProvider(pc.GetName())
		if name == "" {
			continue
		}
		p.commands = append(p.commands, EnhancedPaletteCommand{
			Name:        name,
			Description: safeKeyEntryText(pc.GetDescription()),
			Usage:       sanitizePaletteUsage(pc.GetUsage()),
			Shortcut:    "/" + name,
			Category: PaletteCategoryInfo{
				Name:     safeKeyEntryText(pc.GetCategoryName()),
				Icon:     safeKeyEntryText(pc.GetCategoryIcon()),
				Priority: pc.GetCategoryPriority(),
			},
			Icon:     safeKeyEntryText(pc.GetIcon()),
			ArgHint:  safeKeyEntryText(pc.GetArgHint()),
			Enabled:  pc.IsEnabled(),
			Reason:   safeKeyEntryText(pc.GetReason()),
			Priority: pc.GetPriority(),
			IsRecent: recentSet[name],
			Type:     CommandTypeSlash,
			Advanced: pc.IsAdvanced(),
		})
	}

	// Append action commands (keyboard shortcuts)
	for _, action := range p.actionCommands {
		action.Name = safeKeyEntryText(action.Name)
		if action.Name == "" {
			continue
		}
		action.Description = safeKeyEntryText(action.Description)
		action.Usage = sanitizePaletteUsage(action.Usage)
		action.Shortcut = safeKeyEntryText(action.Shortcut)
		action.Category.Name = safeKeyEntryText(action.Category.Name)
		action.Category.Icon = safeKeyEntryText(action.Category.Icon)
		action.Icon = safeKeyEntryText(action.Icon)
		action.ArgHint = safeKeyEntryText(action.ArgHint)
		action.Reason = safeKeyEntryText(action.Reason)
		p.commands = append(p.commands, action)
	}

	p.sortCommands()
	p.filterCommands(p.query)
	p.normalizeSelection()
	p.syncPreview()
}

func sanitizePaletteUsage(usage string) string {
	usage = ansi.Strip(usage)
	lines := strings.Split(usage, "\n")
	if len(lines) > 8 {
		lines = append(lines[:7], "…")
	}
	for i := range lines {
		lines[i] = safeKeyEntryText(lines[i])
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// sortCommands sorts commands by: recent first (by real timestamp), then by category priority, then by priority within category.
func (p *CommandPalette) sortCommands() {
	sort.SliceStable(p.commands, func(i, j int) bool {
		// Recent commands first
		if p.commands[i].IsRecent && !p.commands[j].IsRecent {
			return true
		}
		if !p.commands[i].IsRecent && p.commands[j].IsRecent {
			return false
		}

		// For recent commands, sort by actual timestamp (most recent first)
		if p.commands[i].IsRecent && p.commands[j].IsRecent {
			ti := p.history.GetTimestamp(p.commands[i].Name)
			tj := p.history.GetTimestamp(p.commands[j].Name)
			return ti.After(tj)
		}

		// Then by priority (lower = higher priority)
		return p.commands[i].Priority < p.commands[j].Priority
	})
}

// Show displays the command palette.
func (p *CommandPalette) Show() {
	p.visible = true
	p.query = ""
	p.selected = 0
	p.scroll = 0
	p.showPreview = false
	p.previewCmd = nil
	p.submitError = ""
	p.RefreshCommands()
}

// Hide hides the command palette.
func (p *CommandPalette) Hide() {
	p.visible = false
	p.query = ""
	p.selected = 0
	p.scroll = 0
	p.showPreview = false
	p.previewCmd = nil
	p.argEntry = false
	p.argValue = ""
	p.argError = ""
	p.argCmd = EnhancedPaletteCommand{}
	p.submitError = ""
}

// PaletteNeedsArg reports whether picking this command should drop into the
// inline arg-entry step rather than running it bare. True only for slash
// commands whose ArgHint marks a REQUIRED argument ("<...>"); optional-arg
// commands ("[...]") and parameterless ones still run on a single Enter, which
// keeps the common no-typing path instant.
func PaletteNeedsArg(cmd EnhancedPaletteCommand) bool {
	return cmd.Type == CommandTypeSlash && cmd.Enabled && strings.Contains(cmd.ArgHint, "<")
}

// InArgEntry reports whether the palette is collecting an argument.
func (p *CommandPalette) InArgEntry() bool { return p.argEntry }

// BeginArgEntry switches the palette into the inline arg-entry step for cmd.
func (p *CommandPalette) BeginArgEntry(cmd EnhancedPaletteCommand) {
	p.argEntry = true
	p.argCmd = cmd
	p.argValue = ""
	p.argError = ""
	p.submitError = ""
	p.showPreview = false
	p.previewCmd = nil
}

// CancelArgEntry returns from arg-entry back to the command list.
func (p *CommandPalette) CancelArgEntry() {
	p.argEntry = false
	p.argValue = ""
	p.argError = ""
	p.argCmd = EnhancedPaletteCommand{}
}

// AppendArg appends text to the argument being entered.
func (p *CommandPalette) AppendArg(s string) {
	p.argValue += sanitizePaletteInput(s)
	p.argError = ""
}

func (p *CommandPalette) SetSubmitError(message string) {
	p.submitError = safeKeyEntryText(message)
}

func (p *CommandPalette) SetArgError(message string) {
	p.argError = safeKeyEntryText(message)
}

// BackspaceArg removes the last visible character from the argument being
// entered. A visible character can span several runes (skin-tone emoji,
// combining marks, ZWJ sequences), so rune-based deletion leaves a misleading
// fragment behind.
func (p *CommandPalette) BackspaceArg() {
	p.argError = ""
	p.argValue = removeLastGrapheme(p.argValue)
}

func (p *CommandPalette) ValidateArgEntry() bool {
	if strings.TrimSpace(p.argValue) != "" {
		p.argError = ""
		return true
	}
	p.argError = "Argument required"
	return false
}

// SubmitArgEntry records usage, hides the palette, and returns the full command
// line ("/name args", or "/name" if no value was typed) for the caller to
// submit. Hiding clears all arg-entry state.
func (p *CommandPalette) SubmitArgEntry() string {
	cmd := p.argCmd
	value := strings.TrimSpace(p.argValue)
	if cmd.Name != "" {
		p.history.RecordUsage(cmd.Name)
	}
	p.Hide() // clears argEntry/argValue/argCmd
	if value == "" {
		return cmd.Shortcut
	}
	return cmd.Shortcut + " " + formatPaletteArgEntryValue(cmd, value)
}

func (p *CommandPalette) DirectSlashLineWithArgs() (string, bool) {
	line := strings.TrimSpace(p.query)
	cmd, ok := p.directSlashCommandWithArgs(line)
	if !ok {
		return "", false
	}
	p.history.RecordUsage(cmd.Name)
	p.Hide()
	return line, true
}

// directSlashCommandWithArgs recognizes a complete slash command without
// changing palette state. View uses it to avoid presenting a runnable command
// as an empty search result; DirectSlashLineWithArgs performs the side effects.
func (p *CommandPalette) directSlashCommandWithArgs(line string) (*EnhancedPaletteCommand, bool) {
	if !strings.HasPrefix(line, "/") {
		return nil, false
	}
	rest := strings.TrimPrefix(line, "/")
	nameEnd := strings.IndexFunc(rest, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' })
	if nameEnd <= 0 {
		return nil, false
	}
	name := rest[:nameEnd]
	if strings.TrimSpace(rest[nameEnd:]) == "" {
		return nil, false
	}
	canonicalName := strings.ToLower(name)
	if target, ok := p.commandAliases[canonicalName]; ok {
		canonicalName = target
	}
	for i := range p.commands {
		cmd := &p.commands[i]
		if cmd.Type == CommandTypeSlash && cmd.Enabled && strings.EqualFold(cmd.Name, canonicalName) {
			return cmd, true
		}
	}
	return nil, false
}

func formatPaletteArgEntryValue(cmd EnhancedPaletteCommand, value string) string {
	if !strings.ContainsAny(value, " \t\r\n") || paletteArgAlreadyQuoted(value) {
		return value
	}
	hint := strings.TrimSpace(cmd.ArgHint)
	switch hint {
	case "<file>", "<path>":
		return quotePaletteArg(value)
	}
	if strings.HasPrefix(hint, "<file> ") || strings.HasPrefix(hint, "<path> ") {
		if filePart, suffix, ok := splitPalettePathAndSuffix(value); ok {
			// Don't re-quote a filePart the user already quoted ("my file.txt" 10)
			// — wrapping it again produced a filename with LITERAL quote chars
			// after splitCommandFields decoded the outer layer. Quoting is also
			// the user's escape hatch for filenames that legitimately end in a
			// bare number (a file named "Chapter 12"), so it must round-trip.
			if paletteArgAlreadyQuoted(filePart) {
				return filePart + " " + suffix
			}
			return quotePaletteArg(filePart) + " " + suffix
		}
		return quotePaletteArg(value)
	}
	return value
}

func paletteArgAlreadyQuoted(value string) bool {
	if len(value) < 2 {
		return false
	}
	first := value[0]
	last := value[len(value)-1]
	return (first == '"' && last == '"') || (first == '\'' && last == '\'')
}

func quotePaletteArg(value string) string {
	return `"` + escapePaletteQuotedArg(value) + `"`
}

type paletteArgField struct {
	start int
	end   int
	text  string
}

func splitPalettePathAndSuffix(value string) (filePart, suffix string, ok bool) {
	fields := paletteArgFields(value)
	if len(fields) < 2 {
		return "", "", false
	}
	if len(fields) >= 3 {
		last := fields[len(fields)-1]
		prev := fields[len(fields)-2]
		if isPositivePaletteInt(prev.text) && isPositivePaletteInt(last.text) {
			prefix := strings.TrimSpace(value[:prev.start])
			if prefix != "" {
				return prefix, prev.text + " " + last.text, true
			}
		}
	}
	last := fields[len(fields)-1]
	if isPaletteLineRange(last.text) {
		prefix := strings.TrimSpace(value[:last.start])
		if prefix != "" {
			return prefix, last.text, true
		}
	}
	return "", "", false
}

func paletteArgFields(value string) []paletteArgField {
	var fields []paletteArgField
	start := -1
	end := 0
	for i, r := range value {
		if unicode.IsSpace(r) {
			if start >= 0 {
				fields = append(fields, paletteArgField{start: start, end: i, text: value[start:i]})
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
		end = i + utf8.RuneLen(r)
	}
	if start >= 0 {
		fields = append(fields, paletteArgField{start: start, end: end, text: value[start:end]})
	}
	return fields
}

func isPaletteLineRange(value string) bool {
	if isPositivePaletteInt(value) {
		return true
	}
	parts := strings.SplitN(value, "-", 2)
	if len(parts) != 2 {
		return false
	}
	start, ok := parsePositivePaletteInt(parts[0])
	if !ok {
		return false
	}
	end, ok := parsePositivePaletteInt(parts[1])
	return ok && end >= start
}

func isPositivePaletteInt(value string) bool {
	_, ok := parsePositivePaletteInt(value)
	return ok
}

func parsePositivePaletteInt(value string) (int, bool) {
	n, err := strconv.Atoi(value)
	return n, err == nil && n > 0
}

func escapePaletteQuotedArg(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r == '"' || r == '\\' {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Toggle toggles the visibility.
func (p *CommandPalette) Toggle() {
	if p.visible {
		p.Hide()
	} else {
		p.Show()
	}
}

// IsVisible returns whether the palette is visible.
func (p *CommandPalette) IsVisible() bool {
	return p.visible
}

// SetQuery sets the search query and filters commands.
func (p *CommandPalette) SetQuery(query string) {
	p.query = sanitizePaletteInput(query)
	p.submitError = ""
	p.filterCommands(p.query)
	p.selected = 0
	p.scroll = 0
	p.syncPreview()
}

func sanitizePaletteInput(value string) string {
	value = ansi.Strip(value)
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
}

// AppendQuery appends a character to the query.
func (p *CommandPalette) AppendQuery(char string) {
	p.SetQuery(p.query + char)
}

// BackspaceQuery removes the last visible character from the query.
func (p *CommandPalette) BackspaceQuery() {
	if next := removeLastGrapheme(p.query); next != p.query {
		p.SetQuery(next)
	}
}

// GetQuery returns the current query.
func (p *CommandPalette) GetQuery() string {
	return p.query
}

// filterCommands filters commands based on the query with fuzzy matching.
func (p *CommandPalette) filterCommands(query string) {
	if query == "" {
		// Hide advanced commands in default view
		p.filtered = make([]EnhancedPaletteCommand, 0, len(p.commands))
		for _, cmd := range p.commands {
			if !cmd.Advanced {
				p.filtered = append(p.filtered, cmd)
			}
		}
		return
	}

	query = strings.ToLower(query)
	slashCommandQuery := false
	// The palette accepts both search text ("plan") and command-shaped input
	// ("/plan"). A single slash token should select the same command, including
	// configured aliases such as /p -> /plan; previously it produced an empty
	// list, making Execute's documented exact-slash path unreachable.
	if strings.HasPrefix(query, "/") {
		commandName := strings.TrimPrefix(query, "/")
		if commandName != "" && !strings.ContainsAny(commandName, " \t\r\n") {
			slashCommandQuery = true
			if canonical, ok := p.commandAliases[commandName]; ok {
				commandName = canonical
			}
			query = commandName
		}
	}
	type scoredCommand struct {
		cmd   EnhancedPaletteCommand
		score int
	}
	var matches []scoredCommand

	for _, cmd := range p.commands {
		if slashCommandQuery && cmd.Type != CommandTypeSlash {
			continue
		}
		if score := paletteMatchScore(cmd, query); score > 0 {
			matches = append(matches, scoredCommand{cmd: cmd, score: score})
		}
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		if matches[i].cmd.Priority != matches[j].cmd.Priority {
			return matches[i].cmd.Priority < matches[j].cmd.Priority
		}
		return matches[i].cmd.Name < matches[j].cmd.Name
	})

	p.filtered = make([]EnhancedPaletteCommand, 0, len(matches))
	for _, match := range matches {
		p.filtered = append(p.filtered, match.cmd)
	}
}

func paletteMatchScore(cmd EnhancedPaletteCommand, query string) int {
	name := strings.ToLower(cmd.Name)
	desc := strings.ToLower(cmd.Description)
	shortcut := strings.ToLower(cmd.Shortcut)
	shortcutName := strings.TrimPrefix(shortcut, "/")
	category := strings.ToLower(cmd.Category.Name)

	score := 0
	switch {
	case name == query || shortcutName == query:
		score = 1000
	case strings.HasPrefix(name, query) || strings.HasPrefix(shortcutName, query):
		score = 850
	case strings.Contains(name, query) || strings.Contains(shortcutName, query):
		score = 650
	case strings.HasPrefix(category, query):
		score = 450
	case strings.Contains(category, query):
		score = 350
	case strings.Contains(desc, query):
		score = 250
	case fuzzyMatch(name, query) || fuzzyMatch(shortcutName, query):
		score = 100
	default:
		return 0
	}

	if cmd.IsRecent {
		score += 25
	}
	if cmd.Type == CommandTypeAction {
		score += 10
	}
	return score
}

// fuzzyMatch checks if all characters in query appear in str in order.
// Both str and query are iterated as runes for correct Unicode handling.
func fuzzyMatch(str, query string) bool {
	qRunes := []rune(query)
	qi := 0
	for _, c := range str {
		if qi < len(qRunes) && c == qRunes[qi] {
			qi++
		}
	}
	return qi == len(qRunes)
}

// SelectNext moves selection to the next item.
func (p *CommandPalette) SelectNext() {
	p.submitError = ""
	if len(p.filtered) > 0 && p.selected < len(p.filtered)-1 {
		p.selected++
		p.adjustScroll()
		p.syncPreview()
	}
}

// SelectPrev moves selection to the previous item.
func (p *CommandPalette) SelectPrev() {
	p.submitError = ""
	if p.selected > 0 {
		p.selected--
		p.adjustScroll()
		p.syncPreview()
	}
}

// SelectFirst and SelectLast provide predictable boundary navigation for long
// command catalogs. Page movement uses the current rendered capacity rather
// than the historical fixed 20-row assumption.
func (p *CommandPalette) SelectFirst() {
	p.submitError = ""
	if len(p.filtered) == 0 {
		return
	}
	p.selected = 0
	p.adjustScroll()
	p.syncPreview()
}

func (p *CommandPalette) SelectLast() {
	p.submitError = ""
	if len(p.filtered) == 0 {
		return
	}
	p.selected = len(p.filtered) - 1
	p.adjustScroll()
	p.syncPreview()
}

func (p *CommandPalette) PageUp() {
	if len(p.filtered) == 0 {
		return
	}
	p.selected = max(0, p.selected-p.listCapacity())
	p.adjustScroll()
	p.syncPreview()
}

func (p *CommandPalette) PageDown() {
	if len(p.filtered) == 0 {
		return
	}
	p.selected = min(len(p.filtered)-1, p.selected+p.listCapacity())
	p.adjustScroll()
	p.syncPreview()
}

func (p *CommandPalette) normalizeSelection() {
	if len(p.filtered) == 0 {
		p.selected = 0
		p.scroll = 0
		return
	}
	if p.selected < 0 {
		p.selected = 0
	}
	if p.selected >= len(p.filtered) {
		p.selected = len(p.filtered) - 1
	}
	p.adjustScroll()
}

func (p *CommandPalette) syncPreview() {
	if !p.showPreview || len(p.filtered) == 0 || p.selected < 0 || p.selected >= len(p.filtered) {
		p.previewCmd = nil
		return
	}
	p.previewCmd = &p.filtered[p.selected]
}

// adjustScroll ensures the selected item is visible.
func (p *CommandPalette) adjustScroll() {
	if len(p.filtered) == 0 {
		p.scroll = 0
		return
	}
	p.scroll = min(max(p.scroll, 0), len(p.filtered)-1)

	// Moving away from the first page introduces an "items above" row and can
	// reduce the number of command rows by one. Re-evaluate until the selected
	// item fits the final window, rather than calculating against the old page
	// and leaving the cursor just below the rendered viewport.
	for {
		visibleItems := p.listCapacity()
		next := p.scroll
		if p.selected < p.scroll {
			next = p.selected
		} else if p.selected >= p.scroll+visibleItems {
			next = p.selected - visibleItems + 1
		}
		next = min(max(next, 0), len(p.filtered)-1)
		if next == p.scroll {
			return
		}
		p.scroll = next
	}
}

func (p *CommandPalette) listCapacity() int {
	if p.height > 0 && p.height < 14 {
		return 1
	}
	rowBudget := max(commandPaletteHeight(p.height, p.maxHeight)-8, 1)
	// The base frame has one spare row, enough for either the search result
	// count or an inline submit error. When both are present, reserve the second
	// row explicitly instead of pushing the footer below the terminal.
	dynamicRows := p.regularDynamicRows()
	rowBudget = max(rowBudget-max(dynamicRows-1, 0), 1)
	if len(p.filtered) <= rowBudget {
		return max(len(p.filtered), 1)
	}

	// Overflow indicators are real rows in the modal. Reserve their space so a
	// middle page (which needs both ↑ and ↓) never pushes the footer below the
	// terminal. The first/last page can use the row for the absent indicator.
	reserved := 0
	if p.scroll > 0 {
		reserved++
	}
	capacity := max(rowBudget-reserved, 1)
	if p.scroll+capacity < len(p.filtered) && capacity > 1 {
		capacity--
	}
	return max(capacity, 1)
}

func (p *CommandPalette) regularDynamicRows() int {
	rows := 0
	if p.query != "" {
		rows++
	}
	if p.submitError != "" {
		rows++
	}
	return rows
}

// TogglePreview toggles the preview panel.
func (p *CommandPalette) TogglePreview() {
	if len(p.filtered) == 0 || p.selected < 0 || p.selected >= len(p.filtered) {
		return
	}
	p.showPreview = !p.showPreview
	p.syncPreview()
}

// GetSelected returns the currently selected command, or nil if none.
func (p *CommandPalette) GetSelected() *EnhancedPaletteCommand {
	if len(p.filtered) == 0 || p.selected < 0 || p.selected >= len(p.filtered) {
		return nil
	}
	return &p.filtered[p.selected]
}

// Execute executes the selected command and returns it.
// If the query starts with "/" and matches a command name exactly, that command
// is executed directly without requiring list navigation.
func (p *CommandPalette) Execute() *EnhancedPaletteCommand {
	// Direct slash command execution: if query starts with "/" and matches a command name exactly
	if strings.HasPrefix(p.query, "/") {
		queryName := strings.ToLower(p.query[1:]) // strip leading "/"
		if canonical, ok := p.commandAliases[queryName]; ok {
			queryName = canonical
		}
		for i := range p.commands {
			if p.commands[i].Type == CommandTypeSlash && strings.EqualFold(p.commands[i].Name, queryName) && p.commands[i].Enabled {
				p.history.RecordUsage(p.commands[i].Name)
				cmd := p.commands[i]
				if cmd.Type == CommandTypeAction && cmd.Action != nil {
					cmd.Action()
				}
				p.Hide()
				return &cmd
			}
		}
	}

	cmd := p.GetSelected()
	if cmd == nil {
		p.Hide()
		return nil
	}

	// Don't execute disabled commands
	if !cmd.Enabled {
		return nil
	}

	// Record usage
	p.history.RecordUsage(cmd.Name)

	switch cmd.Type {
	case CommandTypeAction:
		if cmd.Action != nil {
			cmd.Action()
		}
	}

	p.Hide()
	return cmd
}

// Flush synchronously saves command history to disk.
// Call during app shutdown to ensure pending async saves complete.
func (p *CommandPalette) Flush() error {
	if p.history != nil {
		return p.history.Flush()
	}
	return nil
}

// SetSize sets the available size for rendering.
func (p *CommandPalette) SetSize(width, height int) {
	p.width = width
	p.height = height
}

// View renders the command palette.
func (p *CommandPalette) View(width, height int) string {
	if !p.visible {
		return ""
	}

	// Use stored size or passed size, and remember the effective geometry so
	// paging and selection visibility use the same capacity as rendering.
	if width == 0 {
		width = p.width
	}
	if height == 0 {
		height = p.height
	}
	p.width = width
	p.height = height

	if p.argEntry {
		return p.renderArgEntry(width, height)
	}

	// Palette dimensions
	paletteWidth := commandPaletteWidth(width)
	paletteHeight := commandPaletteHeight(height, p.maxHeight)
	if paletteWidth < 32 || (height > 0 && height < 14) {
		return p.renderCompact(width, height)
	}
	innerWidth := max(paletteWidth-4, 1)

	// Styles
	containerStyle := lipgloss.NewStyle().
		Width(innerWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorSecondary).
		Padding(0, 1)

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorSecondary)

	subtitleStyle := lipgloss.NewStyle().
		Foreground(ColorDim).
		Italic(true)

	inputBoxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(0, 1).
		Width(max(innerWidth-4, 1))

	placeholderStyle := lipgloss.NewStyle().
		Foreground(ColorMuted).
		Italic(true)

	queryStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Bold(true)

	selectedBgStyle := lipgloss.NewStyle().
		Background(ColorSecondary).
		Foreground(ColorBg).
		Bold(true).
		Width(paletteWidth-6).
		Padding(0, 1)

	normalStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Width(paletteWidth-6).
		Padding(0, 1)

	disabledStyle := lipgloss.NewStyle().
		Foreground(ColorMuted).
		Width(paletteWidth-6).
		Padding(0, 1)

	selectedDisabledStyle := lipgloss.NewStyle().
		Background(ColorBorder).
		Foreground(ColorMuted).
		Bold(true).
		Width(paletteWidth-6).
		Padding(0, 1)

	shortcutStyle := lipgloss.NewStyle().
		Foreground(ColorAccent).
		Bold(true)

	shortcutDisabledStyle := lipgloss.NewStyle().
		Foreground(ColorMuted)

	descStyle := lipgloss.NewStyle().
		Foreground(ColorMuted)

	argHintStyle := lipgloss.NewStyle().
		Foreground(ColorDim).
		Italic(true)

	categoryStyle := lipgloss.NewStyle().
		Foreground(ColorDim).
		Bold(true).
		MarginTop(1)

	footerStyle := lipgloss.NewStyle().
		Foreground(ColorDim).
		Italic(true).
		Align(lipgloss.Center).
		Width(max(innerWidth-2, 1))

	scrollStyle := lipgloss.NewStyle().
		Foreground(ColorDim).
		Italic(true)

	recentBadgeStyle := lipgloss.NewStyle().
		Foreground(ColorAccent)

	var content strings.Builder

	// Header
	content.WriteString(titleStyle.Render("Command Palette"))
	content.WriteString("  ")
	content.WriteString(subtitleStyle.Render("Ctrl+P"))
	content.WriteString("\n\n")

	// Search input
	var inputContent string
	if p.query == "" {
		inputContent = placeholderStyle.Render("Filter...")
	} else {
		inputWidth := max(innerWidth-6, 1)
		query := truncateTailForWidth(p.query+"_", inputWidth)
		inputContent = queryStyle.Render(strings.TrimSuffix(query, "_"))
		if strings.HasSuffix(query, "_") {
			inputContent += placeholderStyle.Render("_")
		}
	}
	content.WriteString(inputBoxStyle.Render(inputContent))
	content.WriteString("\n")

	directCmd, directReady := p.directSlashCommandWithArgs(strings.TrimSpace(p.query))
	directRunnable := directReady && p.submissionAvailable()

	// Results count or direct-command readiness.
	if p.query != "" {
		resultLabel := "results"
		if len(p.filtered) == 1 {
			resultLabel = "result"
		}
		if directRunnable && len(p.filtered) == 0 {
			content.WriteString(descStyle.Render("  Ready to run " + directCmd.Shortcut + " with arguments"))
		} else if directReady && len(p.filtered) == 0 {
			content.WriteString(descStyle.Render("  Submission unavailable for " + directCmd.Shortcut))
		} else {
			content.WriteString(descStyle.Render("  " + itoa(len(p.filtered)) + " " + resultLabel))
		}
		content.WriteString("\n")
	}
	if p.submitError != "" {
		errorStyle := lipgloss.NewStyle().Foreground(ColorError).Bold(true)
		error := truncateForWidth("  "+p.submitError, max(innerWidth-2, 1))
		content.WriteString(errorStyle.Render(error))
		content.WriteString("\n")
	}

	// Commands list, or an in-place detail view. Replacing the list while
	// previewing keeps the modal within its height budget; the old append-only
	// preview pushed the footer below short terminals.
	if p.showPreview && p.previewCmd != nil {
		dynamicRows := p.regularDynamicRows()
		if dynamicRows < 2 {
			content.WriteString("\n")
		}
		content.WriteString(p.renderPreview(innerWidth, max(paletteHeight-9, 3)))
		if dynamicRows == 0 {
			content.WriteString("\n")
		}
	} else if len(p.filtered) == 0 {
		content.WriteString("\n")
		if directRunnable {
			content.WriteString(descStyle.Render("  Press Enter to run this command"))
		} else if directReady {
			content.WriteString(descStyle.Render("  Connect command submission or press Backspace to edit"))
		} else if strings.TrimSpace(p.query) != "" {
			noMatch := "  No matches for \"" + p.query + "\""
			content.WriteString(descStyle.Render(truncateMiddleForWidth(noMatch, innerWidth)))
			content.WriteString("\n")
			content.WriteString(descStyle.Render("  Try /help, /status, or Backspace"))
		} else {
			content.WriteString(descStyle.Render("  No commands available"))
		}
		content.WriteString("\n")
	} else {
		// Calculate visible range
		// View may be the first operation after a resize. Re-anchor the cursor
		// using the new height before deriving the visible window.
		p.adjustScroll()
		visibleItems := p.listCapacity()
		startIdx := p.scroll
		endIdx := min(startIdx+visibleItems, len(p.filtered))

		// Scroll indicator (top)
		if startIdx > 0 {
			content.WriteString(scrollStyle.Render("    ↑ " + itoa(startIdx) + " more"))
			content.WriteString("\n")
		}

		// Category chrome is useful only when it fits without stealing command
		// rows. A worst-case transition costs header+separator+item.
		showCategoryHeaders := p.query == "" && len(p.filtered) <= (visibleItems+1)/3

		// Track last category for headers
		var lastCategory string

		// Check if we're showing recent commands section
		showingRecent := false
		for i := startIdx; i < endIdx; i++ {
			if p.filtered[i].IsRecent && p.query == "" {
				showingRecent = true
				break
			}
		}

		if showingRecent && showCategoryHeaders {
			content.WriteString(fitStatusText(categoryStyle.Render("  Recently Used"), innerWidth))
			content.WriteString("\n")
		}

		for i := startIdx; i < endIdx; i++ {
			cmd := p.filtered[i]

			// Category header with icon and separator (only when not searching and not in recent section)
			if showCategoryHeaders && !cmd.IsRecent {
				catName := cmd.Category.Name
				if catName != lastCategory {
					if lastCategory != "" || showingRecent {
						// Separator line between categories
						sepStyle := lipgloss.NewStyle().Foreground(ColorBorder)
						content.WriteString(sepStyle.Render("  " + strings.Repeat("─", max(0, paletteWidth-8))))
						content.WriteString("\n")
					}
					icon := cmd.Category.Icon
					if icon == "" {
						icon = categoryIcon(catName)
					}
					if icon != "" {
						content.WriteString(fitStatusText(categoryStyle.Render("  "+icon+" "+catName), innerWidth))
					} else {
						content.WriteString(fitStatusText(categoryStyle.Render("  "+catName), innerWidth))
					}
					content.WriteString("\n")
					lastCategory = catName
				}
			}

			// Apply selection style
			// Reserve the two-cell selection marker before the row style applies
			// its own horizontal padding. Without this final cell-aware guard a
			// one-cell budget error wrapped every result into two visual rows.
			lineStr := renderPaletteCommandLine(cmd, max(paletteWidth-10, 1), shortcutStyle, shortcutDisabledStyle, descStyle, argHintStyle, recentBadgeStyle)
			if i == p.selected && !cmd.Enabled {
				content.WriteString(selectedDisabledStyle.Render("\u00d7 " + lineStr))
			} else if i == p.selected {
				content.WriteString(selectedBgStyle.Render("> " + lineStr))
			} else if !cmd.Enabled {
				content.WriteString(disabledStyle.Render("  " + lineStr))
			} else {
				content.WriteString(normalStyle.Render("  " + lineStr))
			}
			content.WriteString("\n")
		}

		// Scroll indicator (bottom)
		if endIdx < len(p.filtered) {
			content.WriteString(scrollStyle.Render("    ↓ " + itoa(len(p.filtered)-endIdx) + " more"))
			content.WriteString("\n")
		}
	}

	// Footer
	content.WriteString("\n")
	content.WriteString(footerStyle.Render(truncateForWidth(p.footerText(directReady), max(innerWidth-2, 1))))

	return containerStyle.Render(content.String())
}

// renderPaletteCommandLine gives the shortcut, description, and actionable
// metadata explicit cell budgets. Required arguments and disabled reasons are
// more useful than decorative prose, so they survive before the description
// when a terminal is narrow or uses wide Unicode glyphs.
func renderPaletteCommandLine(cmd EnhancedPaletteCommand, width int, shortcutStyle, shortcutDisabledStyle, descStyle, argHintStyle, recentBadgeStyle lipgloss.Style) string {
	if width <= 0 {
		return ""
	}
	shortcut := cmd.Shortcut
	if shortcut == "" {
		shortcut = cmd.Name
	}
	shortcutWidth := min(15, max(width/3, 4))
	shortcut = truncateForWidth(shortcut, shortcutWidth)
	shortcut = padRight(shortcut, shortcutWidth)
	if cmd.Enabled {
		shortcut = shortcutStyle.Render(shortcut)
	} else {
		shortcut = shortcutDisabledStyle.Render(shortcut)
	}
	prefix := shortcut
	if lipgloss.Width(prefix) < width {
		prefix += " "
	}
	remaining := max(width-lipgloss.Width(prefix), 0)
	if remaining == 0 {
		return fitStatusText(prefix, width)
	}

	metadata := ""
	metadataStyle := descStyle
	if cmd.Enabled && cmd.ArgHint != "" {
		metadata = cmd.ArgHint
		metadataStyle = argHintStyle
	} else if !cmd.Enabled && cmd.Reason != "" {
		metadata = "(" + cmd.Reason + ")"
	}
	badge := cmd.IsRecent && metadata != "" // a lone badge is added below
	if badge {
		metadata += " *"
	}

	metadataRendered := ""
	if metadata != "" {
		minDescription := min(lipgloss.Width(cmd.Description), 4)
		metadataBudget := max(remaining-minDescription-1, 1)
		metadata = truncateMiddleForWidth(metadata, metadataBudget)
		metadataRendered = metadataStyle.Render(metadata)
	}
	if cmd.IsRecent && metadata == "" {
		metadataRendered = recentBadgeStyle.Render("*")
	}

	descriptionBudget := remaining
	if metadataRendered != "" {
		descriptionBudget = max(remaining-lipgloss.Width(metadataRendered)-1, 0)
	}
	description := truncateForWidth(cmd.Description, descriptionBudget)
	line := prefix + description
	if metadataRendered != "" {
		if description != "" {
			line += " "
		}
		line += metadataRendered
	}
	return fitStatusText(line, width)
}

func (p *CommandPalette) footerText(directReady bool) string {
	if p.submitError != "" {
		if p.showPreview {
			return "Esc Close  ·  Tab List  ·  Submit unavailable"
		}
		return "Esc Close  ·  Submission unavailable"
	}
	if len(p.filtered) == 0 {
		if directReady {
			if !p.submissionAvailable() {
				return "Esc Close  Backspace Edit  Submission unavailable"
			}
			return "Esc Close  Enter Run  Backspace Edit"
		}
		if strings.TrimSpace(p.query) != "" {
			return "Esc Close  Backspace Edit"
		}
		return "Esc Close"
	}

	footer := "Esc Close  ↑/↓ Navigate"
	if len(p.filtered) == 1 {
		footer = "Esc Close"
	}
	if p.width >= 70 {
		footer += "  PgUp/PgDn Page"
	}
	if p.selectedRunnable() {
		footer += "  Enter Run"
	} else if selected := p.GetSelected(); selected != nil && selected.Enabled && selected.Type == CommandTypeSlash {
		footer += "  Submission unavailable"
	}
	if p.showPreview {
		return footer + "  Tab List"
	}
	return footer + "  Tab Details"
}

func commandPaletteWidth(width int) int {
	switch {
	case width <= 0:
		return 70
	case width <= 12:
		return max(width, 1)
	case width < 51:
		return width - 2
	default:
		return max(45, min(70, width-6))
	}
}

func commandPaletteHeight(height, maxHeight int) int {
	if maxHeight <= 0 {
		maxHeight = 20
	}
	if height <= 0 {
		return maxHeight
	}
	return min(maxHeight, max(4, height-2))
}

type compactPaletteFrame struct {
	paletteWidth, styleWidth  int
	contentWidth, contentRows int
	horizontalPadding         int
	bordered                  bool
}

// newCompactPaletteFrame keeps the component's own outer geometry within the
// terminal. A rounded border costs two cells in both directions, and one-cell
// horizontal padding needs another two; at degenerate sizes those decorations
// must yield to readable recovery content.
func newCompactPaletteFrame(width, height, defaultRows int) compactPaletteFrame {
	frame := compactPaletteFrame{paletteWidth: commandPaletteWidth(width)}
	frame.bordered = frame.paletteWidth >= 3 && (height <= 0 || height >= 3)
	borderCells := 0
	if frame.bordered {
		borderCells = 2
	}
	frame.styleWidth = max(frame.paletteWidth-borderCells, 1)
	if frame.styleWidth >= 3 {
		frame.horizontalPadding = 1
	}
	frame.contentWidth = max(frame.styleWidth-frame.horizontalPadding*2, 1)
	frame.contentRows = max(defaultRows, 1)
	if height > 0 {
		frame.contentRows = max(height-borderCells, 1)
	}
	return frame
}

// renderCompact is the palette's constrained-terminal mode. The regular
// search box alone consumes three rows; this flat layout keeps search, current
// selection and recovery keys visible in a 10x6 terminal without relying on
// the frame compositor to crop the header.
func (p *CommandPalette) renderCompact(width, height int) string {
	frame := newCompactPaletteFrame(width, height, 6)
	contentWidth, contentRows := frame.contentWidth, frame.contentRows
	style := lipgloss.NewStyle().
		Width(frame.styleWidth).
		Padding(0, frame.horizontalPadding)
	if frame.bordered {
		style = style.
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorSecondary)
	}
	titleStyle := lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true)
	muted := lipgloss.NewStyle().Foreground(ColorDim)
	keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)

	lines := []string{titleStyle.Render("Cmds")}
	// The body rows have different recovery value when space collapses. Keep
	// the filter and any submission error ahead of decorative chrome; the final
	// footer is reserved separately below and is never part of tail clipping.
	priorities := []int{0}
	query := "Filter…"
	if p.query != "" {
		query = truncateTailForWidth(p.query+"_", max(contentWidth-2, 1))
	}
	lines = append(lines, keyStyle.Render("› ")+query)
	priorities = append(priorities, 3)

	directCmd, directReady := p.directSlashCommandWithArgs(strings.TrimSpace(p.query))
	directRunnable := directReady && p.submissionAvailable()
	if selected := p.GetSelected(); selected != nil {
		label := selected.Shortcut
		if label == "" {
			label = selected.Name
		}
		prefix := "> "
		if !selected.Enabled {
			prefix = "× "
		}
		position := fmt.Sprintf(" %d/%d", p.selected+1, len(p.filtered))
		lines = append(lines, prefix+label+position)
	} else if directRunnable {
		lines = append(lines, "↵ "+directCmd.Shortcut)
	} else if directReady {
		lines = append(lines, "× submission unavailable")
	} else if strings.TrimSpace(p.query) != "" {
		lines = append(lines, muted.Render("No matches"))
	} else {
		lines = append(lines, muted.Render("No commands"))
	}
	priorities = append(priorities, 2)
	if p.submitError != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(ColorError).Bold(true).Render(p.submitError))
		priorities = append(priorities, 4)
	}

	footer := "Esc  ⌫"
	hasTarget := p.GetSelected() != nil || directReady
	if hasTarget && !p.targetVisibleForEnter() {
		footer = resizeRecoveryLabel(contentWidth, "Esc Close")
	} else if p.submitError == "" && (p.selectedRunnable() || directRunnable) {
		footer = "Esc  ↵  ↑↓"
	}
	bodyRows := max(contentRows-1, 0)
	for len(lines) > bodyRows {
		drop := 0
		for i := 1; i < len(priorities); i++ {
			if priorities[i] < priorities[drop] {
				drop = i
			}
		}
		lines = append(lines[:drop], lines[drop+1:]...)
		priorities = append(priorities[:drop], priorities[drop+1:]...)
	}
	lines = append(lines, muted.Render(footer))
	for i := range lines {
		lines[i] = fitStatusText(lines[i], contentWidth)
	}
	return style.Render(strings.Join(lines, "\n"))
}

func (p *CommandPalette) renderCompactArgEntry(width int) string {
	frame := newCompactPaletteFrame(width, p.height, 4)
	contentWidth := frame.contentWidth
	style := lipgloss.NewStyle().
		Width(frame.styleWidth).
		Padding(0, frame.horizontalPadding)
	if frame.bordered {
		style = style.
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorAccent)
	}
	keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	muted := lipgloss.NewStyle().Foreground(ColorDim)
	value := p.argValue
	if value == "" {
		value = p.argCmd.ArgHint
	} else {
		value += "_"
	}
	lines := []string{
		keyStyle.Render("Run ") + p.argCmd.Shortcut,
		keyStyle.Render("› ") + truncateTailForWidth(sanitizePaletteInput(value), max(contentWidth-2, 1)),
	}
	priorities := []int{3, 4}
	if p.argError != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(ColorError).Bold(true).Render(p.argError))
		priorities = append(priorities, 5)
	}
	footer := "Esc  ↵"
	if !p.argTargetVisibleForEnter() {
		footer = resizeRecoveryLabel(contentWidth, "Esc Back")
	} else if !p.submissionAvailable() || isUnavailablePromptNotice(p.argError) {
		footer = "Esc Back"
	}
	bodyRows := max(frame.contentRows-1, 0)
	for len(lines) > bodyRows {
		drop := 0
		for i := 1; i < len(priorities); i++ {
			if priorities[i] < priorities[drop] {
				drop = i
			}
		}
		lines = append(lines[:drop], lines[drop+1:]...)
		priorities = append(priorities[:drop], priorities[drop+1:]...)
	}
	lines = append(lines, muted.Render(footer))
	for i := range lines {
		lines[i] = fitStatusText(lines[i], contentWidth)
	}
	return style.Render(strings.Join(lines, "\n"))
}

// renderArgEntry draws the inline argument-entry step: the chosen command, its
// argument hint, and a focused one-line field. Picked from the palette list, so
// the user fills only the value — no need to type the command name.
func (p *CommandPalette) renderArgEntry(width, height int) string {
	paletteWidth := commandPaletteWidth(width)
	if paletteWidth < 32 || (height > 0 && height < 12) {
		return p.renderCompactArgEntry(width)
	}
	innerWidth := max(paletteWidth-4, 1)

	containerStyle := lipgloss.NewStyle().
		Width(innerWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(0, 1)

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary)
	cmdStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	descStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	hintStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
	queryStyle := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	placeholderStyle := lipgloss.NewStyle().Foreground(ColorMuted).Italic(true)
	inputBoxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(0, 1).
		Width(max(innerWidth-4, 1))
	footerStyle := lipgloss.NewStyle().
		Foreground(ColorDim).
		Italic(true).
		Align(lipgloss.Center).
		Width(innerWidth)

	var content strings.Builder

	content.WriteString(titleStyle.Render("Run "))
	content.WriteString(cmdStyle.Render(p.argCmd.Shortcut))
	content.WriteString("\n")
	if p.argCmd.Description != "" {
		content.WriteString(fitStatusText(descStyle.Render(safeKeyEntryText(p.argCmd.Description)), max(innerWidth-2, 1)))
		content.WriteString("\n")
	}
	content.WriteString("\n")

	// Argument field with the ArgHint shown as placeholder until the user types.
	var inputContent string
	if p.argValue == "" {
		hint := p.argCmd.ArgHint
		if hint == "" {
			hint = "type an argument..."
		}
		inputContent = placeholderStyle.Render(hint)
	} else {
		inputWidth := max(innerWidth-6, 1)
		value := truncateTailForWidth(p.argValue+"_", inputWidth)
		inputContent = queryStyle.Render(strings.TrimSuffix(value, "_"))
		if strings.HasSuffix(value, "_") {
			inputContent += placeholderStyle.Render("_")
		}
	}
	inputContent = fitStatusText(inputContent, max(innerWidth-6, 1))
	content.WriteString(inputBoxStyle.Render(inputContent))
	content.WriteString("\n")

	if p.argCmd.ArgHint != "" && p.argValue != "" {
		content.WriteString(fitStatusText(hintStyle.Render("  "+p.argCmd.ArgHint), innerWidth))
		content.WriteString("\n")
	}
	if p.argError != "" {
		errorStyle := lipgloss.NewStyle().Foreground(ColorError).Bold(true)
		content.WriteString(errorStyle.Render(truncateMiddleForWidth("  "+safeKeyEntryText(p.argError), innerWidth)))
		content.WriteString("\n")
	}

	content.WriteString("\n")
	footer := "Esc Back to list  ·  Enter Run"
	if !p.submissionAvailable() || isUnavailablePromptNotice(p.argError) {
		footer = "Esc Back to list  ·  Submission unavailable"
	}
	if paletteWidth < 45 {
		if !p.submissionAvailable() || isUnavailablePromptNotice(p.argError) {
			footer = "Esc Back  Submission unavailable"
		} else {
			footer = "Esc Back  Enter Run"
		}
	}
	content.WriteString(footerStyle.Render(truncateForWidth(footer, innerWidth)))

	return containerStyle.Render(content.String())
}

// renderPreview renders the preview panel for the selected command.
func (p *CommandPalette) renderPreview(width, maxRows int) string {
	if p.previewCmd == nil {
		return ""
	}

	cmd := p.previewCmd

	previewStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorDim).
		Padding(0, 1).
		Width(max(width-4, 1))

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorAccent)

	labelStyle := lipgloss.NewStyle().
		Foreground(ColorMuted)

	var content strings.Builder

	// Title
	content.WriteString(titleStyle.Render(cmd.Shortcut + " - " + cmd.Description))
	content.WriteString("\n\n")

	// Usage
	content.WriteString(labelStyle.Render("Usage:"))
	content.WriteString("\n")
	usage := cmd.Usage
	if usage == "" {
		usage = cmd.Shortcut
	}
	// Split multi-line usage
	for line := range strings.SplitSeq(usage, "\n") {
		content.WriteString("  " + line + "\n")
	}

	// Category
	content.WriteString("\n")
	content.WriteString(labelStyle.Render("Category: "))
	content.WriteString(cmd.Category.Name)

	// Status
	if !cmd.Enabled {
		content.WriteString("\n")
		content.WriteString(labelStyle.Render("Status: "))
		content.WriteString("Disabled - " + cmd.Reason)
	}

	contentText := strings.TrimSuffix(content.String(), "\n")
	contentLines := strings.Split(contentText, "\n")
	innerRows := max(maxRows-2, 1)
	if len(contentLines) > innerRows {
		contentLines = contentLines[:innerRows]
	}
	for i := range contentLines {
		contentLines[i] = fitStatusText(contentLines[i], max(width-4, 1))
	}
	return previewStyle.Render(strings.Join(contentLines, "\n"))
}

// categoryIcon returns a minimal icon for a palette category name.
func categoryIcon(name string) string {
	switch strings.ToLower(name) {
	case "getting started":
		return "›"
	case "session":
		return "◐"
	case "auth", "auth & setup":
		return "◇"
	case "git":
		return "⎇"
	case "planning":
		return "○"
	case "tools":
		return "▸"
	default:
		return "·"
	}
}

func padRight(s string, length int) string {
	w := lipgloss.Width(s)
	if w >= length {
		return s
	}
	return s + strings.Repeat(" ", length-w)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var result []byte
	negative := n < 0
	if negative {
		n = -n
	}
	for n > 0 {
		result = append([]byte{byte('0' + n%10)}, result...)
		n /= 10
	}
	if negative {
		result = append([]byte{'-'}, result...)
	}
	return string(result)
}
