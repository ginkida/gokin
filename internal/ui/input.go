package ui

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"gokin/internal/config"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// defaultProviderNames returns all provider names from the registry.
func defaultProviderNames() []string { return config.ProviderNames() }

// defaultKeyProviderNames returns provider names that require API keys.
func defaultKeyProviderNames() []string { return config.KeyProviderNames() }

// defaultAllProviderNames returns all provider names plus "all".
func defaultAllProviderNames() []string { return config.AllProviderNames() }

const (
	maxHistorySize = 100
	historyFile    = "input_history"
)

// ArgInfo describes a command argument for autocomplete hints.
type ArgInfo struct {
	Name     string   // Argument name (e.g., "message", "filename")
	Required bool     // True if argument is required
	Type     string   // "string", "path", "option", "number"
	Options  []string // Available options for "option" type
}

// CommandInfo contains information about a slash command for autocomplete.
type CommandInfo struct {
	Name        string
	Description string
	Category    string
	Args        []ArgInfo // Command arguments
	Usage       string    // Usage pattern (e.g., "/copy <n> [filename]")
}

// SuggestionType represents the type of autocomplete suggestion.
type SuggestionType int

const (
	SuggestionCommand SuggestionType = iota
	SuggestionFile
)

// InputModel represents the input component.
type InputModel struct {
	textarea     textarea.Model
	styles       *Styles
	history      []string // Command history
	historyIndex int      // Current position in history (-1 = new input)
	savedInput   string   // Saved current input when browsing history
	workDir      string   // Working directory for file suggestions

	// Autocomplete
	commands        []CommandInfo
	suggestions     []CommandInfo
	suggestionType  SuggestionType
	fileSuggestions []string
	suggestionIndex int
	showSuggestions bool

	// Alias map: user-typed short name → canonical command. Wired from
	// commands.Handler at startup so /p resolves to plan in autocomplete
	// even though "p" isn't in the commands slice. Written once at startup,
	// treated as read-only afterward — no locking needed.
	commandAliases map[string]string

	// Recent command names in most-recently-used order (≤ 5 entries). Small
	// additive score bump so frequently used commands rank first on ties.
	//
	// NOTE on synchronization: InputModel is embedded by value in ui.Model,
	// which Bubble Tea passes by value through Init/Update/View. This
	// prevents using sync.Mutex or atomic primitives (they carry noCopy and
	// would trip vet across 20+ call sites). RecordRecentCommand writes
	// from the app goroutine; updateSuggestions reads from the Bubble Tea
	// event loop. In practice slice-header assignment is word-sized on all
	// supported architectures, so the worst case of a race is one keystroke
	// seeing a stale ranking — not corruption. This matches the existing
	// pattern for Model setters (AddSystemMessage, SetPermissionsEnabled,
	// etc.) which also mutate Model state from app goroutines without
	// locks.
	recentCommands []string

	// Ghost text (inline completion hint)
	ghostText    string // Suggested completion shown in dim color
	ghostEnabled bool   // Whether ghost text is enabled

	// Argument hints
	showArgHints   bool         // Show argument hints after command
	currentCommand *CommandInfo // Current command for arg hints

	// History search (Ctrl+R)
	historySearchMode   bool
	historySearchQuery  string
	historySearchResult string
	historySearchIndex  int

	// Placeholder context
	activeTask string
}

// NewInputModel creates a new input model.
func NewInputModel(styles *Styles, workDir string) InputModel {
	ta := textarea.New()
	ta.Placeholder = "Message or /command (Tab: complete, ?: shortcuts)"
	ta.Focus()
	ta.CharLimit = 10000
	ta.ShowLineNumbers = false
	ta.SetHeight(1)

	return InputModel{
		textarea:           ta,
		styles:             styles,
		workDir:            workDir,
		history:            make([]string, 0, maxHistorySize),
		historyIndex:       -1,
		savedInput:         "",
		commands:           DefaultCommands(),
		suggestions:        nil,
		suggestionType:     SuggestionCommand,
		fileSuggestions:    nil,
		suggestionIndex:    0,
		showSuggestions:    false,
		ghostText:          "",
		ghostEnabled:       true, // Enable ghost text by default
		historySearchIndex: -1,
	}
}

// DefaultCommands returns the default list of slash commands shown in
// autocomplete suggestions. Exported so cross-package guard tests in
// internal/commands can verify every registered command appears here
// (see TestEveryRegisteredCommandIsInAutocomplete).
func DefaultCommands() []CommandInfo {
	return []CommandInfo{
		// Getting Started
		{Name: "help", Description: "Show help for commands", Category: "Getting Started",
			Args: []ArgInfo{{Name: "command", Required: false, Type: "string"}}, Usage: "/help [command]"},
		{Name: "quickstart", Description: "Interactive onboarding guide", Category: "Getting Started"},
		{Name: "shortcuts", Description: "Show keyboard shortcuts", Category: "Getting Started"},

		// Session
		{Name: "model", Description: "Switch AI model", Category: "Session",
			Args:  []ArgInfo{{Name: "model", Required: false, Type: "option", Options: []string{"gemini-3-flash-preview", "gemini-3-pro", "claude-sonnet-4-5", "gpt-5.4", "deepseek-chat", "ollama"}}},
			Usage: "/model [name]"},
		{Name: "reasoning", Description: "Set reasoning effort", Category: "Session",
			Args:  []ArgInfo{{Name: "level", Required: false, Type: "option", Options: []string{"none", "low", "medium", "high", "xhigh"}}},
			Usage: "/reasoning [none|low|medium|high|xhigh]"},
		{Name: "clear", Description: "Clear conversation history", Category: "Session"},
		{Name: "compact", Description: "Force context compaction", Category: "Session"},
		{Name: "save", Description: "Save current session", Category: "Session",
			Args: []ArgInfo{{Name: "name", Required: false, Type: "string"}}, Usage: "/save [name]"},
		{Name: "resume", Description: "Resume a saved session", Category: "Session",
			Args: []ArgInfo{{Name: "session", Required: true, Type: "string"}}, Usage: "/resume <session>"},
		{Name: "sessions", Description: "List saved sessions", Category: "Session"},
		{Name: "stats", Description: "Show session statistics", Category: "Session"},
		{Name: "memory", Description: "Show stored memories", Category: "Session"},
		// /undo and /redo are fundamental enough to be in Session autocomplete.
		// Were missing pre-v0.78.14 — caught by TestEveryRegisteredCommandIsInAutocomplete.
		{Name: "undo", Description: "Undo the last file change", Category: "Session",
			Args:  []ArgInfo{{Name: "n_or_list", Required: false, Type: "string"}},
			Usage: "/undo [N|list]"},
		{Name: "redo", Description: "Re-apply the last undone change", Category: "Session",
			Args:  []ArgInfo{{Name: "n", Required: false, Type: "number"}},
			Usage: "/redo [N]"},
		{Name: "instructions", Description: "Show project instructions", Category: "Session"},

		// Auth & Setup
		{Name: "login", Description: "Set API key for a provider", Category: "Auth",
			Args: []ArgInfo{{Name: "provider", Required: false, Type: "option", Options: defaultKeyProviderNames()},
				{Name: "key", Required: false, Type: "string"}}, Usage: "/login [provider] [key]"},
		{Name: "logout", Description: "Remove API key", Category: "Auth",
			Args:  []ArgInfo{{Name: "provider", Required: false, Type: "option", Options: defaultAllProviderNames()}},
			Usage: "/logout [provider|all]"},
		{Name: "provider", Description: "Switch AI provider", Category: "Auth",
			Args:  []ArgInfo{{Name: "provider", Required: false, Type: "option", Options: defaultProviderNames()}},
			Usage: "/provider [name]"},
		{Name: "status", Description: "Show configuration status", Category: "Auth"},
		{Name: "doctor", Description: "Check environment and configuration", Category: "Auth"},
		{Name: "config", Description: "Show current configuration", Category: "Auth"},
		{Name: "update", Description: "Check/install updates and rollback", Category: "Auth",
			Args:  []ArgInfo{{Name: "action", Required: false, Type: "option", Options: []string{"install", "backups", "rollback"}}},
			Usage: "/update [install|backups|rollback [backup-id]]"},
		// v0.74–v0.76 release feedback set. These were registered in
		// commands.go but missing from autocomplete — surfaced by
		// TestEveryRegisteredCommandIsInAutocomplete in v0.78.14.
		{Name: "restart", Description: "Re-exec into the latest installed binary", Category: "Auth"},
		{Name: "whats-new", Description: "Show release notes for the current version", Category: "Auth"},
		{Name: "changelog", Description: "Show compact list of recent releases", Category: "Auth"},

		// Git
		{Name: "init", Description: "Initialize GOKIN.md for this project", Category: "Git"},
		{Name: "commit", Description: "Create a git commit with AI-generated message", Category: "Git",
			Args: []ArgInfo{{Name: "message", Required: false, Type: "string"}}, Usage: "/commit [message]"},
		{Name: "pr", Description: "Create a pull request", Category: "Git"},
		// v0.77.x git-inspect family + v0.78.12 /blame. These were registered
		// in commands.go but missing from autocomplete — added here so the
		// suggestion list matches the actual available commands.
		{Name: "diff", Description: "Show pending changes (working tree / staged)", Category: "Git",
			Args:  []ArgInfo{{Name: "scope", Required: false, Type: "string"}},
			Usage: "/diff [--staged|--stat|<file>]"},
		{Name: "log", Description: "Show recent git commits", Category: "Git",
			Args:  []ArgInfo{{Name: "count_or_file", Required: false, Type: "string"}},
			Usage: "/log [count] [file]"},
		{Name: "branches", Description: "List local branches with current marker", Category: "Git",
			Args:  []ArgInfo{{Name: "scope", Required: false, Type: "option", Options: []string{"--all"}}},
			Usage: "/branches [--all]"},
		{Name: "grep", Description: "Search the working tree for a pattern", Category: "Git",
			Args:  []ArgInfo{{Name: "pattern", Required: true, Type: "string"}, {Name: "path", Required: false, Type: "path"}},
			Usage: "/grep <pattern> [path]"},
		{Name: "blame", Description: "Show line-by-line git blame for a file", Category: "Git",
			Args:  []ArgInfo{{Name: "file", Required: true, Type: "path"}, {Name: "range", Required: false, Type: "string"}},
			Usage: "/blame <file> [N|N-M|N M]"},
		{Name: "show", Description: "Show a specific commit (metadata + diff)", Category: "Git",
			Args:  []ArgInfo{{Name: "ref", Required: false, Type: "string"}, {Name: "file", Required: false, Type: "path"}},
			Usage: "/show [ref] [file]"},

		// Planning
		{Name: "plan", Description: "Toggle planning mode", Category: "Planning",
			Args:  []ArgInfo{{Name: "action", Required: false, Type: "option", Options: []string{"status"}}},
			Usage: "/plan [status]"},
		{Name: "resume-plan", Description: "Resume a saved plan", Category: "Planning"},
		{Name: "health", Description: "Show runtime health", Category: "Planning"},
		{Name: "policy", Description: "Show policy engine status", Category: "Planning"},
		{Name: "ledger", Description: "Show plan run ledger", Category: "Planning"},
		{Name: "plan-proof", Description: "Show contract/evidence proof for a step", Category: "Planning",
			Args: []ArgInfo{{Name: "step_id", Required: false, Type: "number"}}, Usage: "/plan-proof [step_id]"},
		{Name: "journal", Description: "Show execution journal", Category: "Planning"},
		{Name: "recovery", Description: "Show recovery snapshot", Category: "Planning"},
		{Name: "observability", Description: "Show reliability dashboard", Category: "Planning"},
		{Name: "memory-governance", Description: "Show session memory governance", Category: "Planning"},
		{Name: "tree-stats", Description: "Show tree planner statistics", Category: "Planning"},

		// Tools
		{Name: "pwd", Description: "Show current working directory", Category: "Tools"},
		{Name: "browse", Description: "Open interactive file browser", Category: "Tools",
			Args: []ArgInfo{{Name: "path", Required: false, Type: "path"}}, Usage: "/browse [path]"},
		{Name: "open", Description: "Open a file in your editor", Category: "Tools",
			Args: []ArgInfo{{Name: "file", Required: true, Type: "path"}}, Usage: "/open <file>"},
		{Name: "copy", Description: "Copy code block to clipboard", Category: "Tools",
			Args:  []ArgInfo{{Name: "n", Required: false, Type: "number"}, {Name: "filename", Required: false, Type: "path"}},
			Usage: "/copy [n] [filename]"},
		{Name: "paste", Description: "Paste from clipboard", Category: "Tools"},
		{Name: "clear-todos", Description: "Clear all todo items", Category: "Tools"},
		{Name: "ql", Description: "Quick look at a file", Category: "Tools",
			Args: []ArgInfo{{Name: "path", Required: true, Type: "path"}}, Usage: "/ql <path>"},
		{Name: "permissions", Description: "Toggle permission prompts", Category: "Tools",
			Args:  []ArgInfo{{Name: "mode", Required: false, Type: "option", Options: []string{"on", "off"}}},
			Usage: "/permissions [on|off]"},
		{Name: "sandbox", Description: "Toggle bash sandbox mode", Category: "Tools",
			Args:  []ArgInfo{{Name: "mode", Required: false, Type: "option", Options: []string{"on", "off"}}},
			Usage: "/sandbox [on|off]"},
		{Name: "thinking", Description: "Toggle extended thinking display", Category: "Tools",
			Args:  []ArgInfo{{Name: "value", Required: false, Type: "option", Options: []string{"on", "off"}}},
			Usage: "/thinking [on|off|<tokens>]"},
		{Name: "theme", Description: "Change UI theme", Category: "Tools",
			Args:  []ArgInfo{{Name: "name", Required: false, Type: "option", Options: []string{"dark", "macos", "light"}}},
			Usage: "/theme [dark|macos|light]"},
		{Name: "register-agent-type", Description: "Register a custom agent type", Category: "Tools",
			Args:  []ArgInfo{{Name: "name", Required: true, Type: "string"}, {Name: "description", Required: true, Type: "string"}},
			Usage: `/register-agent-type <name> "<desc>" [--tools ...]`},
		{Name: "list-agent-types", Description: "List all registered agent types", Category: "Tools"},
		{Name: "unregister-agent-type", Description: "Remove a custom agent type", Category: "Tools",
			Args: []ArgInfo{{Name: "name", Required: true, Type: "string"}}, Usage: "/unregister-agent-type <name>"},
		{Name: "mcp", Description: "Manage MCP (Model Context Protocol) servers", Category: "Tools",
			Args: []ArgInfo{{Name: "subcommand", Required: false, Type: "option",
				Options: []string{"list", "status", "add", "remove", "refresh", "help"}}},
			Usage: "/mcp [list|status|add|remove|refresh|help]"},
	}
}

// Init initializes the input model.
func (m InputModel) Init() tea.Cmd {
	return textarea.Blink
}

// Update handles input events.
func (m InputModel) Update(msg tea.Msg) (InputModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Handle history search mode
		if m.historySearchMode {
			return m.handleHistorySearch(msg)
		}

		switch msg.Type {
		case tea.KeyCtrlR:
			// Enter history search mode
			m.historySearchMode = true
			m.historySearchQuery = ""
			m.historySearchResult = ""
			m.historySearchIndex = len(m.history)
			return m, nil

		case tea.KeyTab:
			// Accept ghost text if visible (and no dropdown)
			if m.ghostText != "" && !m.showSuggestions {
				m.textarea.SetValue(m.textarea.Value() + m.ghostText)
				m.textarea.CursorEnd()
				m.ghostText = ""
				return m, nil
			}

			// Accept suggestion from dropdown
			if m.showSuggestions {
				if m.suggestionType == SuggestionCommand && len(m.suggestions) > 0 {
					selected := m.suggestions[m.suggestionIndex]
					m.textarea.SetValue("/" + selected.Name + " ")
					m.textarea.CursorEnd()
					m.showSuggestions = false
					m.suggestions = nil
					m.ghostText = ""
					// Show arg hints if command has arguments
					if len(selected.Args) > 0 {
						m.showArgHints = true
						m.currentCommand = &selected
					}
					return m, nil
				} else if m.suggestionType == SuggestionFile && len(m.fileSuggestions) > 0 {
					selected := m.fileSuggestions[m.suggestionIndex]
					rel, _ := filepath.Rel(m.workDir, selected)
					m.acceptFileSuggestion(rel)
					return m, nil
				}
			}

			// Try to trigger suggestions
			value := m.textarea.Value()
			if strings.HasPrefix(value, "/") && !strings.Contains(value, " ") {
				m.updateSuggestions(value)
				if len(m.suggestions) == 1 {
					// Auto-complete if only one match
					selected := m.suggestions[0]
					m.textarea.SetValue("/" + selected.Name + " ")
					m.textarea.CursorEnd()
					m.showSuggestions = false
					m.suggestions = nil
					m.ghostText = ""
					// Show arg hints
					if len(selected.Args) > 0 {
						m.showArgHints = true
						m.currentCommand = &selected
					}
				}
			} else if m.shouldSuggestFiles(value) {
				m.updateFileSuggestions(value)
			}
			return m, nil

		case tea.KeyEnter:
			// Handle autocomplete on Enter
			if m.showSuggestions {
				if m.suggestionType == SuggestionCommand && len(m.suggestions) > 0 {
					selected := m.suggestions[m.suggestionIndex]
					m.textarea.SetValue("/" + selected.Name + " ")
					m.textarea.CursorEnd()
					m.showSuggestions = false
					m.suggestions = nil
					m.ghostText = ""
					// Show arg hints
					if len(selected.Args) > 0 {
						m.showArgHints = true
						m.currentCommand = &selected
					}
					return m, nil
				} else if m.suggestionType == SuggestionFile && len(m.fileSuggestions) > 0 {
					selected := m.fileSuggestions[m.suggestionIndex]
					rel, _ := filepath.Rel(m.workDir, selected)
					m.acceptFileSuggestion(rel)
					return m, nil
				}
			}

		case tea.KeyUp:
			// Navigate suggestions or history
			if m.showSuggestions {
				count := 0
				if m.suggestionType == SuggestionCommand {
					count = len(m.suggestions)
				} else {
					count = len(m.fileSuggestions)
				}
				if count > 0 && m.suggestionIndex > 0 {
					m.suggestionIndex--
				}
				return m, nil
			}
			// Navigate to older history (rest of logic continues...)
			// Navigate to older history
			if len(m.history) > 0 {
				if m.historyIndex == -1 {
					m.savedInput = m.textarea.Value()
					m.historyIndex = len(m.history) - 1
				} else if m.historyIndex > 0 {
					m.historyIndex--
				}
				// Bounds check: historyIndex may exceed len if history was externally trimmed
				if m.historyIndex >= 0 && m.historyIndex < len(m.history) {
					m.textarea.SetValue(m.history[m.historyIndex])
					m.textarea.CursorEnd()
				} else {
					m.historyIndex = -1
				}
			}
			return m, nil

		case tea.KeyDown:
			// Navigate suggestions or history
			if m.showSuggestions {
				count := 0
				if m.suggestionType == SuggestionCommand {
					count = len(m.suggestions)
				} else {
					count = len(m.fileSuggestions)
				}
				if count > 0 && m.suggestionIndex < count-1 {
					m.suggestionIndex++
				}
				return m, nil
			}
			// Navigate to newer history
			if m.historyIndex >= 0 {
				if m.historyIndex < len(m.history)-1 {
					m.historyIndex++
					if m.historyIndex < len(m.history) {
						m.textarea.SetValue(m.history[m.historyIndex])
					} else {
						m.historyIndex = -1
						m.textarea.SetValue(m.savedInput)
					}
				} else {
					m.historyIndex = -1
					m.textarea.SetValue(m.savedInput)
				}
				m.textarea.CursorEnd()
			}
			return m, nil

		case tea.KeyEscape:
			// Cancel suggestions and ghost text
			if m.showSuggestions || m.ghostText != "" || m.showArgHints {
				m.showSuggestions = false
				m.suggestions = nil
				m.ghostText = ""
				m.showArgHints = false
				m.currentCommand = nil
				return m, nil
			}

		case tea.KeyShiftTab:
			// Ignore Shift+Tab - it's handled by the parent TUI for planning mode toggle
			// Don't pass to textarea to avoid unexpected behavior
			return m, nil
		}

		// Update textarea and check for suggestion updates
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)

		// Update suggestions based on input
		value := m.textarea.Value()
		if strings.HasPrefix(value, "/") && !strings.Contains(value, " ") {
			m.suggestionType = SuggestionCommand
			m.updateSuggestions(value)
			// Clear arg hints when typing command
			m.showArgHints = false
			m.currentCommand = nil
		} else if m.shouldSuggestFiles(value) {
			m.suggestionType = SuggestionFile
			m.updateFileSuggestions(value)
		} else {
			m.showSuggestions = false
			m.suggestions = nil
			m.fileSuggestions = nil
			m.ghostText = ""
			// Clear arg hints when not in command context
			if !strings.HasPrefix(value, "/") {
				m.showArgHints = false
				m.currentCommand = nil
			}
		}

		return m, cmd
	}

	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	return m, cmd
}

// handleHistorySearch handles key events in history search mode.
func (m InputModel) handleHistorySearch(msg tea.KeyMsg) (InputModel, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		// Accept search result
		if m.historySearchResult != "" {
			m.textarea.SetValue(m.historySearchResult)
			m.textarea.CursorEnd()
		}
		m.historySearchMode = false
		m.historySearchQuery = ""
		m.historySearchResult = ""
		return m, nil

	case tea.KeyEscape, tea.KeyCtrlC:
		// Cancel search
		m.historySearchMode = false
		m.historySearchQuery = ""
		m.historySearchResult = ""
		return m, nil

	case tea.KeyCtrlR:
		// Search for next match (older)
		if m.historySearchIndex > 0 {
			m.historySearchIndex--
			m.searchHistory()
		}
		return m, nil

	case tea.KeyBackspace:
		// Remove last character from query
		if len(m.historySearchQuery) > 0 {
			_, size := utf8.DecodeLastRuneInString(m.historySearchQuery)
			m.historySearchQuery = m.historySearchQuery[:len(m.historySearchQuery)-size]
			m.historySearchIndex = len(m.history)
			m.searchHistory()
		}
		return m, nil

	default:
		// Add character to search query
		if msg.Type == tea.KeyRunes {
			m.historySearchQuery += string(msg.Runes)
			m.historySearchIndex = len(m.history)
			m.searchHistory()
		}
		return m, nil
	}
}

// searchHistory searches history for the current query.
func (m *InputModel) searchHistory() {
	if m.historySearchQuery == "" {
		m.historySearchResult = ""
		return
	}

	query := strings.ToLower(m.historySearchQuery)
	for i := m.historySearchIndex - 1; i >= 0; i-- {
		if strings.Contains(strings.ToLower(m.history[i]), query) {
			m.historySearchResult = m.history[i]
			m.historySearchIndex = i
			return
		}
	}
	// No match found
	m.historySearchResult = ""
}

// fuzzyScore calculates a fuzzy match score for a query against a string.
// Returns 0 if no match, higher score for better matches.
func fuzzyScore(str, query string) int {
	str = strings.ToLower(str)
	query = strings.ToLower(query)

	if query == "" {
		return 0
	}

	score := 0
	qi := 0
	lastMatchIdx := -1
	consecutiveBonus := 0

	for i := 0; i < len(str) && qi < len(query); i++ {
		if str[i] == query[qi] {
			qi++
			score += 10 // Base score for each match

			// Bonus for consecutive matches
			if lastMatchIdx == i-1 {
				consecutiveBonus++
				score += consecutiveBonus * 5
			} else {
				consecutiveBonus = 0
			}

			// Bonus for match at start
			if i == 0 {
				score += 20
			}

			// Bonus for match after separator (like camelCase or word boundary)
			if i > 0 && (str[i-1] == '-' || str[i-1] == '_' || str[i-1] == ' ') {
				score += 15
			}

			lastMatchIdx = i
		}
	}

	// All query characters must be matched
	if qi == len(query) {
		return score
	}
	return 0
}

// scoredCommand holds a command with its match score.
type scoredCommand struct {
	cmd   CommandInfo
	score int
}

// updateSuggestions updates the autocomplete suggestions with fuzzy matching.
// Scoring priority (high → low):
//  1. Exact alias match (e.g. user typed "/p" — show "plan" first).
//  2. Exact prefix match on command name.
//  3. Substring match anywhere in the name (not just prefix).
//  4. Character-by-character fuzzy with consecutive-char bonus.
// Recent usage bumps all of the above by a small additive, so frequently
// used commands bubble up when multiple candidates tie.
func (m *InputModel) updateSuggestions(input string) {
	prefix := strings.TrimPrefix(input, "/")
	prefix = strings.ToLower(prefix)

	// Resolve alias up-front: if user typed something that matches an alias
	// exactly, surface the target command as a strong signal.
	aliasTarget := ""
	if target, ok := m.commandAliases[prefix]; ok {
		aliasTarget = strings.ToLower(target)
	}

	var scored []scoredCommand

	for _, cmd := range m.commands {
		cmdLower := strings.ToLower(cmd.Name)

		if aliasTarget != "" && cmdLower == aliasTarget {
			scored = append(scored, scoredCommand{cmd: cmd, score: 2000})
			continue
		}

		// Exact prefix match gets highest priority
		if strings.HasPrefix(cmdLower, prefix) {
			scored = append(scored, scoredCommand{
				cmd:   cmd,
				score: 1000 + (100 - len(cmd.Name)), // Shorter names rank higher
			})
			continue
		}

		// Substring match anywhere (e.g. "mit" finds "commit"). Lower than
		// prefix but higher than fuzzy so "mit" ranks commit above abstract
		// character-by-character matches.
		if prefix != "" && strings.Contains(cmdLower, prefix) {
			scored = append(scored, scoredCommand{
				cmd:   cmd,
				score: 500 + (100 - len(cmd.Name)),
			})
			continue
		}

		// Fuzzy match
		if score := fuzzyScore(cmd.Name, prefix); score > 0 {
			scored = append(scored, scoredCommand{cmd: cmd, score: score})
		}
	}

	// Recent-usage bonus — small additive so frequently used commands win
	// ties but don't override semantic matches. Most-recent = highest bonus.
	for i, name := range m.recentCommands {
		for j := range scored {
			if scored[j].cmd.Name == name {
				scored[j].score += 30 - i*3
			}
		}
	}

	// Sort by score (descending), then by name (ascending)
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].cmd.Name < scored[j].cmd.Name
	})

	// Extract commands
	m.suggestions = make([]CommandInfo, 0, len(scored))
	for _, sc := range scored {
		m.suggestions = append(m.suggestions, sc.cmd)
	}

	m.suggestionIndex = 0
	m.showSuggestions = len(m.suggestions) > 0
	m.suggestionType = SuggestionCommand

	// Update ghost text with top suggestion
	m.updateGhostText(prefix)
}

func (m *InputModel) updateGhostText(prefix string) {
	if !m.ghostEnabled {
		m.ghostText = ""
		return
	}

	if m.suggestionType == SuggestionCommand && len(m.suggestions) > 0 {
		topCmd := m.suggestions[0]
		cmdLower := strings.ToLower(topCmd.Name)
		prefixLower := strings.ToLower(prefix)

		// Only show ghost text for prefix matches (not fuzzy matches)
		if strings.HasPrefix(cmdLower, prefixLower) && len(topCmd.Name) > len(prefix) {
			m.ghostText = topCmd.Name[len(prefix):]
		} else {
			m.ghostText = ""
		}
	} else if m.suggestionType == SuggestionFile && len(m.fileSuggestions) > 0 {
		top := m.fileSuggestions[0]
		rel, err := filepath.Rel(m.workDir, top)
		if err == nil {
			words := strings.Fields(prefix)
			if len(words) > 0 {
				lastWord := words[len(words)-1]
				if strings.HasPrefix(rel, lastWord) && len(rel) > len(lastWord) {
					m.ghostText = rel[len(lastWord):]
				} else {
					m.ghostText = ""
				}
			}
		}
	} else {
		m.ghostText = ""
	}
}

// shouldSuggestFiles returns true if the input looks like a path or command argument.
func (m InputModel) shouldSuggestFiles(input string) bool {
	if input == "" {
		return false
	}

	// Suggest for specific commands
	if strings.HasPrefix(input, "/open ") || strings.HasPrefix(input, "/browse ") ||
		strings.HasPrefix(input, "/copy ") || strings.HasPrefix(input, "/ql ") {
		return true
	}

	// Suggest if current word looks like a path (contains /, ., or is part of a known path)
	words := strings.Fields(input)
	if len(words) == 0 {
		return false
	}
	lastWord := words[len(words)-1]
	return len(lastWord) >= 2 && (strings.Contains(lastWord, "/") || strings.Contains(lastWord, "."))
}

// updateFileSuggestions finds file matches for the current input.
func (m *InputModel) updateFileSuggestions(input string) {
	if m.workDir == "" {
		m.showSuggestions = false
		return
	}

	words := strings.Fields(input)
	if len(words) == 0 {
		m.showSuggestions = false
		return
	}
	lastWord := words[len(words)-1]

	// Simple glob matching for now
	pattern := filepath.Join(m.workDir, lastWord+"*")
	matches, _ := filepath.Glob(pattern)

	// Fallback: search in current dir if not absolute
	if len(matches) == 0 && !filepath.IsAbs(lastWord) {
		pattern = filepath.Join(m.workDir, "**", lastWord+"*")
		// Limit search for performance
		matches = m.searchFilesFuzzy(lastWord)
	}

	m.fileSuggestions = matches
	m.suggestionIndex = 0
	m.showSuggestions = len(m.fileSuggestions) > 0

	// Update ghost text for files
	if len(m.fileSuggestions) > 0 {
		top := m.fileSuggestions[0]
		rel, err := filepath.Rel(m.workDir, top)
		if err == nil {
			if strings.HasPrefix(rel, lastWord) && len(rel) > len(lastWord) {
				m.ghostText = rel[len(lastWord):]
			} else {
				m.ghostText = ""
			}
		}
	}
}

// searchFilesFuzzy performs a shallow search for files matching a query.
func (m InputModel) searchFilesFuzzy(query string) []string {
	var results []string
	maxResults := 10

	filepath.Walk(m.workDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || len(results) >= maxResults {
			return filepath.SkipDir
		}
		// Skip hidden dirs
		if info.IsDir() && strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
			return filepath.SkipDir
		}

		rel, _ := filepath.Rel(m.workDir, path)
		if fuzzyScore(rel, query) > 0 {
			results = append(results, path)
		}
		return nil
	})

	return results
}

// View renders the input component.
func (m InputModel) View() string {
	var result strings.Builder

	// Show history search mode
	if m.historySearchMode {
		searchStyle := lipgloss.NewStyle().
			Foreground(ColorSecondary).
			Bold(true)

		queryStyle := lipgloss.NewStyle().
			Foreground(ColorText)

		resultStyle := lipgloss.NewStyle().
			Foreground(ColorMuted)

		result.WriteString(searchStyle.Render("search:"))
		result.WriteString(queryStyle.Render("`" + m.historySearchQuery + "'"))
		result.WriteString(": ")
		if m.historySearchResult != "" {
			result.WriteString(resultStyle.Render(m.historySearchResult))
		}
		result.WriteString("\n")
	}

	// Show autocomplete suggestions
	if m.showSuggestions && len(m.suggestions) > 0 {
		suggestionBox := m.renderSuggestions()
		result.WriteString(suggestionBox)
		result.WriteString("\n")
	}

	// Show argument hints after command
	if m.showArgHints && m.currentCommand != nil && len(m.currentCommand.Args) > 0 {
		argHints := m.renderArgHints()
		result.WriteString(argHints)
		result.WriteString("\n")
	}

	// Input field with ghost text
	inputView := m.textarea.View()
	if m.ghostText != "" && m.ghostEnabled && !m.showSuggestions {
		// Render ghost text inline (after input)
		ghostStyle := lipgloss.NewStyle().Foreground(ColorDim)
		inputView = inputView + ghostStyle.Render(m.ghostText)
	}
	result.WriteString(m.styles.Input.Render(inputView))

	return result.String()
}

// renderArgHints renders argument hints for the current command.
func (m InputModel) renderArgHints() string {
	if m.currentCommand == nil || len(m.currentCommand.Args) == 0 {
		return ""
	}

	var builder strings.Builder

	hintStyle := lipgloss.NewStyle().Foreground(ColorDim)
	reqStyle := lipgloss.NewStyle().Foreground(ColorWarning) // Amber for required
	optStyle := lipgloss.NewStyle().Foreground(ColorMuted)   // Gray for optional

	builder.WriteString(hintStyle.Render("  Usage: /"))
	builder.WriteString(hintStyle.Render(m.currentCommand.Name))
	builder.WriteString(" ")

	for i, arg := range m.currentCommand.Args {
		if i > 0 {
			builder.WriteString(" ")
		}

		if arg.Required {
			builder.WriteString(reqStyle.Render("<" + arg.Name + ">"))
		} else {
			builder.WriteString(optStyle.Render("[" + arg.Name + "]"))
		}
	}

	return builder.String()
}

// renderSuggestions renders the autocomplete suggestion box.
func (m InputModel) renderSuggestions() string {
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(0, 1)

	selectedStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorSecondary)

	normalStyle := lipgloss.NewStyle().
		Foreground(ColorText)

	descStyle := lipgloss.NewStyle().
		Foreground(ColorDim)

	var lines []string
	maxShow := 6

	if m.suggestionType == SuggestionCommand {
		if len(m.suggestions) < maxShow {
			maxShow = len(m.suggestions)
		}

		// Determine visible range
		start := 0
		if m.suggestionIndex >= maxShow {
			start = m.suggestionIndex - maxShow + 1
		}
		end := min(start+maxShow, len(m.suggestions))

		for i := start; i < end; i++ {
			cmd := m.suggestions[i]
			style := normalStyle
			prefix := "  "
			if i == m.suggestionIndex {
				style = selectedStyle
				prefix = "> "
			}

			line := prefix + style.Render("/"+cmd.Name) + " " + descStyle.Render(cmd.Description)
			lines = append(lines, line)
		}

		// Add scroll indicator if needed
		if len(m.suggestions) > maxShow {
			indicator := lipgloss.NewStyle().Foreground(ColorDim).Render(
				fmt.Sprintf("↑↓ %d", len(m.suggestions)),
			)
			lines = append(lines, indicator)
		}
	} else {
		// File suggestions
		if len(m.fileSuggestions) < maxShow {
			maxShow = len(m.fileSuggestions)
		}

		start := 0
		if m.suggestionIndex >= maxShow {
			start = m.suggestionIndex - maxShow + 1
		}
		end := min(start+maxShow, len(m.fileSuggestions))

		for i := start; i < end; i++ {
			path := m.fileSuggestions[i]
			rel, _ := filepath.Rel(m.workDir, path)
			style := normalStyle
			prefix := "  "
			if i == m.suggestionIndex {
				style = selectedStyle
				prefix = "> "
			}

			line := prefix + style.Render(rel)
			lines = append(lines, line)
		}

		if len(m.fileSuggestions) > maxShow {
			indicator := lipgloss.NewStyle().Foreground(ColorDim).Render(
				fmt.Sprintf("↑↓ %d", len(m.fileSuggestions)),
			)
			lines = append(lines, indicator)
		}
	}

	return boxStyle.Render(strings.Join(lines, "\n"))
}

// Value returns the current input value.
func (m InputModel) Value() string {
	return strings.TrimSpace(m.textarea.Value())
}

// Reset clears the input and optionally saves to history.
func (m *InputModel) Reset() {
	m.textarea.Reset()
	m.textarea.SetHeight(1) // Shrink back to single line after send
	m.historyIndex = -1
	m.savedInput = ""
}

// InsertNewline adds a newline at the cursor position and grows the input height.
// Used for multi-line input with Alt+Enter.
func (m *InputModel) InsertNewline() {
	m.textarea.InsertString("\n")
	// Grow height to fit content, up to maxInputLines
	const maxInputLines = 6
	lines := min(strings.Count(m.textarea.Value(), "\n")+1, maxInputLines)
	if lines > 1 {
		m.textarea.SetHeight(lines)
	}
}

// Height returns the current input height in lines.
func (m InputModel) Height() int {
	return m.textarea.Height()
}

// AddToHistory adds a command to the history.
func (m *InputModel) AddToHistory(cmd string) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return
	}

	// Don't add duplicates of the last entry
	if len(m.history) > 0 && m.history[len(m.history)-1] == cmd {
		return
	}

	m.history = append(m.history, cmd)

	// Trim history if too large
	if len(m.history) > maxHistorySize {
		m.history = m.history[len(m.history)-maxHistorySize:]
	}
}

// SetWidth sets the input width.
func (m *InputModel) SetWidth(width int) {
	m.textarea.SetWidth(max(width-4, 1)) // Account for border padding, minimum 1
}

// Focus focuses the input.
func (m *InputModel) Focus() tea.Cmd {
	return m.textarea.Focus()
}

// Blur unfocuses the input.
func (m *InputModel) Blur() {
	m.textarea.Blur()
}

// Focused returns whether the input is focused.
func (m InputModel) Focused() bool {
	return m.textarea.Focused()
}

// GetHistory returns the current history slice.
func (m *InputModel) GetHistory() []string {
	return m.history
}

// SetHistory sets the history from an external source.
func (m *InputModel) SetHistory(history []string) {
	m.history = history
	if len(m.history) > maxHistorySize {
		m.history = m.history[len(m.history)-maxHistorySize:]
	}
}

// LoadHistory loads command history from file.
func (m *InputModel) LoadHistory() error {
	histPath, err := getHistoryPath()
	if err != nil {
		return err
	}

	file, err := os.Open(histPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No history file yet, that's fine
		}
		return err
	}
	defer file.Close()

	var history []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			history = append(history, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	// Keep only the last maxHistorySize entries
	if len(history) > maxHistorySize {
		history = history[len(history)-maxHistorySize:]
	}

	m.history = history
	return nil
}

// SaveHistory saves command history to file.
func (m *InputModel) SaveHistory() error {
	histPath, err := getHistoryPath()
	if err != nil {
		return err
	}

	// Ensure directory exists
	dir := filepath.Dir(histPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	file, err := os.Create(histPath)
	if err != nil {
		return err
	}
	defer file.Close()

	for _, cmd := range m.history {
		if _, err := file.WriteString(cmd + "\n"); err != nil {
			return err
		}
	}

	return nil
}

// getHistoryPath returns the path to the history file.
func getHistoryPath() (string, error) {
	// Use XDG_DATA_HOME or fallback to ~/.local/share
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dataDir = filepath.Join(home, ".local", "share")
	}

	return filepath.Join(dataDir, "gokin", historyFile), nil
}

// SetActiveTask sets the active task name for placeholder context.
func (m *InputModel) SetActiveTask(task string) {
	m.activeTask = task
	m.updatePlaceholder()
}

// SetCommandAliases wires the alias map from commands.Handler into the
// autocomplete ranker. Called once from App during wiring; thread-safe only
// when invoked before any goroutine dispatches UI events on this model.
func (m *InputModel) SetCommandAliases(aliases map[string]string) {
	if aliases == nil {
		m.commandAliases = nil
		return
	}
	out := make(map[string]string, len(aliases))
	for k, v := range aliases {
		out[strings.ToLower(k)] = strings.ToLower(v)
	}
	m.commandAliases = out
}

// RecordRecentCommand pushes `name` to the front of the recent-commands list
// (dedup, cap at 5). Called from the App after each successful command
// dispatch so autocomplete can favour commands the user actually runs.
//
// Builds the new slice in a fresh allocation rather than filtering in place —
// this is the one place where a concurrent reader could otherwise observe a
// mid-state corruption of the backing array. With a fresh slice the swap is a
// single slice-header assignment: readers see either the old or the new list.
func (m *InputModel) RecordRecentCommand(name string) {
	if name == "" {
		return
	}
	const maxRecent = 5
	next := make([]string, 0, maxRecent)
	next = append(next, name)
	for _, n := range m.recentCommands {
		if n == name || len(next) >= maxRecent {
			continue
		}
		next = append(next, n)
	}
	m.recentCommands = next
}

// SetPlaceholder sets the placeholder text for the input.
func (m *InputModel) SetPlaceholder(placeholder string) {
	m.textarea.Placeholder = placeholder
}

// updatePlaceholder updates the placeholder text based on context.
func (m *InputModel) updatePlaceholder() {
	if m.activeTask != "" {
		m.textarea.Placeholder = "Continue: " + m.activeTask
	} else {
		m.textarea.Placeholder = "Message or /command (Tab: complete)"
	}
}

// SetCommands sets the available commands for autocomplete.
func (m *InputModel) SetCommands(commands []CommandInfo) {
	m.commands = commands
}

// AddCommand adds a single command to the autocomplete list.
func (m *InputModel) AddCommand(cmd CommandInfo) {
	// Check if command already exists
	for i, existing := range m.commands {
		if existing.Name == cmd.Name {
			m.commands[i] = cmd
			return
		}
	}
	m.commands = append(m.commands, cmd)
}

// IsHistorySearchMode returns whether history search mode is active.
func (m *InputModel) IsHistorySearchMode() bool {
	return m.historySearchMode
}

// ShowingSuggestions returns whether suggestions are being shown.
func (m *InputModel) ShowingSuggestions() bool {
	return m.showSuggestions
}

// acceptFileSuggestion replaces the last word with the selected file path.
func (m *InputModel) acceptFileSuggestion(path string) {
	value := m.textarea.Value()
	words := strings.Fields(value)
	if len(words) == 0 {
		return
	}

	// Replace last word
	words[len(words)-1] = path
	m.textarea.SetValue(strings.Join(words, " ") + " ")
	m.textarea.CursorEnd()
	m.showSuggestions = false
	m.fileSuggestions = nil
	m.ghostText = ""
}
