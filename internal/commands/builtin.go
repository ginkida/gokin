package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/logging"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
)

// HelpCommand shows help for commands.
type HelpCommand struct {
	handler *Handler
}

func (c *HelpCommand) Name() string        { return "help" }
func (c *HelpCommand) Description() string { return "Show help for commands" }
func (c *HelpCommand) Usage() string       { return "/help [command]" }
func (c *HelpCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryGettingStarted,
		Icon:     "help",
		Priority: 0,
		HasArgs:  true,
		ArgHint:  "[command]",
	}
}

func (c *HelpCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	if len(args) > 0 {
		// Show help for specific command
		cmd, exists := c.handler.GetCommand(args[0])
		if !exists {
			return fmt.Sprintf("%sUnknown command: /%s%s\nUse /help to see all commands.", colorRed, args[0], colorReset), nil
		}
		result := fmt.Sprintf("%s/%s%s — %s\n\n%sUsage:%s %s",
			colorGreen, cmd.Name(), colorReset, cmd.Description(),
			colorCyan, colorReset, cmd.Usage())

		if example := getCommandExample(cmd.Name()); example != "" {
			result += fmt.Sprintf("\n\n%sExamples:%s\n%s", colorCyan, colorReset, example)
		}
		if related := getRelatedCommands(cmd.Name()); related != "" {
			result += fmt.Sprintf("\n%sSee also:%s %s", colorCyan, colorReset, related)
		}

		return result, nil
	}

	var sb strings.Builder

	// Essential Commands — the most useful commands at a glance
	fmt.Fprintf(&sb, "\n%s─── Essential Commands ───%s\n\n", colorYellow, colorReset)

	essentials := []struct {
		name string
		desc string
	}{
		{"help", "Show help (this page) or /help <cmd> for details"},
		{"model", "Switch AI model"},
		{"clear", "Clear conversation history"},
		{"save", "Save current session"},
		{"commit", "Create a git commit with AI-generated message"},
		{"plan", "Toggle planning mode"},
		{"doctor", "Check environment and configuration"},
	}

	for _, e := range essentials {
		fmt.Fprintf(&sb, "  %s/%-10s%s %s\n", colorGreen, e.name, colorReset, e.desc)
	}

	// All Commands grouped by 6 categories
	fmt.Fprintf(&sb, "\n%s─── All Commands ───%s\n", colorYellow, colorReset)

	categories := []struct {
		name     string
		commands []string
	}{
		{"Getting Started", []string{"help", "quickstart", "shortcuts"}},
		{"Session", []string{"model", "thinking", "clear", "compact", "save", "resume", "sessions", "stats", "cost", "instructions", "memory", "undo", "redo"}},
		{"Auth & Setup", []string{"login", "logout", "keys", "provider", "status", "doctor", "config", "update", "restart", "whats-new", "changelog"}},
		{"Git", []string{"init", "commit", "pr", "diff", "log", "branches", "grep", "blame", "show"}},
		{"Planning", []string{"plan", "resume-plan", "checkpoints", "health", "policy", "ledger", "plan-proof", "journal", "recovery", "observability", "insights", "memory-governance", "tree-stats"}},
		{"Tools", []string{"browse", "open", "pwd", "mcp", "copy", "paste", "clear-todos", "ql", "permissions", "sandbox", "theme", "debug-dump",
			"register-agent-type", "list-agent-types", "unregister-agent-type"}},
	}

	// Build a map for quick lookup
	cmds := c.handler.ListCommands()
	cmdMap := make(map[string]Command)
	for _, cmd := range cmds {
		cmdMap[cmd.Name()] = cmd
	}

	for _, cat := range categories {
		var catCmds []Command
		for _, name := range cat.commands {
			if cmd, ok := cmdMap[name]; ok {
				catCmds = append(catCmds, cmd)
				delete(cmdMap, name)
			}
		}
		if len(catCmds) == 0 {
			continue
		}

		fmt.Fprintf(&sb, "\n  %s%s%s\n", colorBold, cat.name, colorReset)
		for _, cmd := range catCmds {
			fmt.Fprintf(&sb, "    %s/%-22s%s %s%s%s\n", colorGreen, cmd.Name(), colorReset, colorCyan, cmd.Description(), colorReset)
		}
	}

	// Show any uncategorized commands
	if len(cmdMap) > 0 {
		fmt.Fprintf(&sb, "\n  %sOther%s\n", colorBold, colorReset)
		var remaining []Command
		for _, cmd := range cmdMap {
			remaining = append(remaining, cmd)
		}
		sort.Slice(remaining, func(i, j int) bool {
			return remaining[i].Name() < remaining[j].Name()
		})
		for _, cmd := range remaining {
			fmt.Fprintf(&sb, "    %s/%-22s%s %s%s%s\n", colorGreen, cmd.Name(), colorReset, colorCyan, cmd.Description(), colorReset)
		}
	}

	// Keyboard Shortcuts
	fmt.Fprintf(&sb, "\n%s─── Keyboard Shortcuts ───%s\n\n", colorYellow, colorReset)
	shortcuts := []struct {
		key  string
		desc string
	}{
		{"Ctrl+P", "Command palette"},
		{"Shift+Tab", "Cycle mode: Normal → Plan → YOLO → Normal"},
		{"Ctrl+G", "Toggle mouse mode"},
		{"Ctrl+T", "Toggle task list"},
		{"Ctrl+O", "Toggle activity feed"},
		{"Ctrl+L", "Clear screen"},
		{"Ctrl+R", "Search input history"},
		{"Ctrl+C", "Exit"},
		{"Esc", "Cancel current operation"},
	}
	for _, s := range shortcuts {
		fmt.Fprintf(&sb, "  %s%-14s%s %s\n", colorGreen, s.key, colorReset, s.desc)
	}

	fmt.Fprintf(&sb, "\nTip: Use %sCtrl+P%s to access all commands quickly.\n", colorGreen, colorReset)

	return sb.String(), nil
}

// ClearCommand clears the conversation history.
type ClearCommand struct{}

func (c *ClearCommand) Name() string        { return "clear" }
func (c *ClearCommand) Description() string { return "Clear conversation history" }
func (c *ClearCommand) Usage() string       { return "/clear" }
func (c *ClearCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategorySession,
		Icon:     "clear",
		Priority: 10,
	}
}

func (c *ClearCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	// Count messages before clearing
	msgCount := 0
	if session := app.GetSession(); session != nil {
		msgCount = len(session.GetHistory())
	}

	// Save current plan before clearing (so it can be resumed with /resume-plan)
	planSaved := false
	if pm := app.GetPlanManager(); pm != nil {
		if currentPlan := pm.GetCurrentPlan(); currentPlan != nil && !currentPlan.IsComplete() {
			if err := pm.SaveCurrentPlan(); err != nil {
				logging.Warn("failed to save plan before clear", "error", err)
			} else {
				planSaved = true
			}
		}
	}

	app.ClearConversation()
	// Also clear todos
	if todoTool := app.GetTodoTool(); todoTool != nil {
		todoTool.ClearItems()
	}

	msg := fmt.Sprintf("Cleared %d messages and todos.", msgCount)
	if planSaved {
		msg += " Active plan saved for /resume-plan."
	}
	return msg, nil
}

// CompactCommand forces context compaction.
type CompactCommand struct{}

func (c *CompactCommand) Name() string        { return "compact" }
func (c *CompactCommand) Description() string { return "Force context compaction/summarization" }
func (c *CompactCommand) Usage() string       { return "/compact" }
func (c *CompactCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category:         CategorySession,
		Icon:             "compress",
		Priority:         20,
		LongRunning:      true,
		LongRunningLabel: "Compacting context — this calls the model to summarize history...",
	}
}

func (c *CompactCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cm := app.GetContextManager()
	if cm == nil {
		return "Context manager not available.", nil
	}

	// Capture token usage before compaction
	usageBefore := cm.GetTokenUsage()
	tokensBefore := 0
	if usageBefore != nil {
		tokensBefore = usageBefore.InputTokens
	}

	err := cm.ForceSummarize(ctx)
	usageAfter := cm.GetTokenUsage()
	tokensAfter := 0
	pctAfter := 0.0
	if usageAfter != nil {
		tokensAfter = usageAfter.InputTokens
		pctAfter = usageAfter.PercentUsed
	}
	return formatCompactionResult(err, tokensBefore, tokensAfter, pctAfter), nil
}

// formatCompactionResult renders the user-facing string for /compact, with
// distinct messages for each sentinel error and an honest accounting of
// the no-op cases that earlier versions reported as silent success.
func formatCompactionResult(err error, tokensBefore, tokensAfter int, pctAfter float64) string {
	if err != nil {
		switch {
		case errors.Is(err, appcontext.ErrSummarizerUnavailable):
			return "No-op: summarizer is not configured for this provider. /compact has no effect — context will only shrink via /clear."
		case errors.Is(err, appcontext.ErrHistoryTooShort):
			return "No-op: conversation is too short to compact. Add more messages and try again, or use /clear to start fresh."
		case errors.Is(err, appcontext.ErrNothingToSummarize):
			return "No-op: every message is pinned or already summarized — nothing left to compact."
		default:
			return fmt.Sprintf("Compaction failed: %v", err)
		}
	}

	if tokensBefore <= 0 {
		// Token counter wasn't populated yet (fresh session). The summarizer
		// ran without error, but we have nothing to compare against.
		return "Context compacted successfully."
	}

	saved := tokensBefore - tokensAfter
	if saved <= 0 {
		// Summarizer ran but produced a result no smaller than the
		// original — rare (model emitted a verbose summary), but worth
		// reporting honestly instead of claiming "compacted".
		return fmt.Sprintf("Compaction ran but freed no tokens (was %dk, now %dk). Try /clear if context still feels heavy.",
			tokensBefore/1000, tokensAfter/1000)
	}

	pct := int(pctAfter * 100)
	return fmt.Sprintf("Context compacted: %dk → %dk tokens (freed %dk, now %d%% full)",
		tokensBefore/1000, tokensAfter/1000, saved/1000, pct)
}

// SaveCommand saves the current session.
type SaveCommand struct{}

func (c *SaveCommand) Name() string        { return "save" }
func (c *SaveCommand) Description() string { return "Save current session" }
func (c *SaveCommand) Usage() string       { return "/save [name]" }
func (c *SaveCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategorySession,
		Icon:     "save",
		Priority: 30,
		HasArgs:  true,
		ArgHint:  "[name]",
	}
}

func (c *SaveCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	hm, err := app.GetHistoryManager()
	if err != nil {
		return fmt.Sprintf("Failed to get history manager: %v", err), nil
	}

	session := app.GetSession()
	if session == nil {
		return "No active session.", nil
	}

	// Use custom name if provided
	originalID := session.ID
	if len(args) > 0 {
		session.ID = args[0]
	}

	err = hm.SaveFull(session)
	if err != nil {
		session.ID = originalID // Restore original ID
		return fmt.Sprintf("Failed to save session: %v", err), nil
	}

	savedID := session.ID
	session.ID = originalID // Restore original ID

	msgCount := len(session.GetHistory())
	return fmt.Sprintf("Session saved as: %s (%d messages)\nTo restore: /resume %s", savedID, msgCount, savedID), nil
}

// ResumeCommand resumes a saved session.
type ResumeCommand struct{}

func (c *ResumeCommand) Name() string        { return "resume" }
func (c *ResumeCommand) Description() string { return "Resume a saved session" }
func (c *ResumeCommand) Usage() string       { return "/resume <session_id> [--force]" }
func (c *ResumeCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategorySession,
		Icon:     "resume",
		Priority: 40,
		HasArgs:  true,
		ArgHint:  "<id>",
	}
}

func (c *ResumeCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	hm, err := app.GetHistoryManager()
	if err != nil {
		return fmt.Sprintf("Failed to get history manager: %v", err), nil
	}

	// No args: show recent sessions for this project to pick from
	if len(args) == 0 {
		sessions, err := hm.ListSessions()
		if err != nil || len(sessions) == 0 {
			return "No saved sessions. Use /save to save the current session first.", nil
		}

		workDir := app.GetWorkDir()
		var sb strings.Builder
		sb.WriteString("Recent sessions (use /resume <id>):\n\n")
		shown := 0
		for _, info := range sessions {
			if workDir != "" && info.WorkDir != "" &&
				filepath.Clean(info.WorkDir) != filepath.Clean(workDir) {
				continue
			}
			summary := info.Summary
			if runes := []rune(summary); len(runes) > 60 {
				summary = string(runes[:57]) + "..."
			}
			if summary == "" {
				summary = "(no summary)"
			}
			age := formatTimeAgo(info.LastActive)
			fmt.Fprintf(&sb, "  %s%s%s  %d msgs, %s — %s\n",
				colorGreen, info.ID, colorReset, info.MessageCount, age, summary)
			shown++
			if shown >= 5 {
				break
			}
		}
		if shown == 0 {
			return "No sessions for current project. Use /sessions --all to see all.", nil
		}
		fmt.Fprintf(&sb, "\nExample: /resume %s", sessions[0].ID)
		return sb.String(), nil
	}

	sessionID := args[0]
	force := len(args) > 1 && args[1] == "--force"

	state, err := hm.LoadFull(sessionID)
	if err != nil {
		// Distinguish "not found" from real load failures (corrupt JSON,
		// schema drift, IO error). Without this, the user sees the raw
		// fs error path "open /Users/.../sessions/abc.json: no such file
		// or directory" — confusing because it leaks the storage layout.
		if os.IsNotExist(err) {
			return fmt.Sprintf("Session '%s' not found. Run /resume (no args) to list available sessions, or /sessions for the full list.", sessionID), nil
		}
		return fmt.Sprintf("Failed to load session '%s': %v", sessionID, err), nil
	}

	// Warn if session is from a different project
	currentDir := app.GetWorkDir()
	if !force && state.WorkDir != "" && currentDir != "" &&
		filepath.Clean(state.WorkDir) != filepath.Clean(currentDir) {
		return fmt.Sprintf("Session '%s' was created in %s (current: %s).\nUse /resume %s --force to load anyway.",
			sessionID, state.WorkDir, currentDir, sessionID), nil
	}

	session := app.GetSession()
	if session == nil {
		return "No active session to restore into.", nil
	}

	err = session.RestoreFromState(state)
	if err != nil {
		return fmt.Sprintf("Failed to restore session: %v", err), nil
	}

	// Compare on-disk count vs what actually loaded. RestoreFromState skips
	// malformed entries (FunctionCall/FunctionResponse pair mismatches,
	// nil parts, etc.) silently — without this check, a session that
	// dropped 3 of 50 messages would still be reported as 50 loaded, and
	// the next /save would overwrite the original on disk with the
	// truncated state.
	onDisk := len(state.History)
	loaded := len(session.GetHistory())

	var msg string
	switch {
	case onDisk == 0:
		msg = fmt.Sprintf("Session '%s' restored, but the saved state had no messages.", sessionID)
	case loaded < onDisk:
		msg = fmt.Sprintf("Session '%s' restored: %d of %d messages loaded (%d skipped — malformed or pair-mismatched entries; see logs).",
			sessionID, loaded, onDisk, onDisk-loaded)
	default:
		msg = fmt.Sprintf("Session '%s' restored. %d messages loaded.", sessionID, loaded)
	}
	if state.Summary != "" {
		summary := state.Summary
		if runes := []rune(summary); len(runes) > 100 {
			summary = string(runes[:97]) + "..."
		}
		msg += fmt.Sprintf("\nLast topic: %s", summary)
	}
	return msg, nil
}

// SessionsCommand lists saved sessions.
type SessionsCommand struct{}

func (c *SessionsCommand) Name() string        { return "sessions" }
func (c *SessionsCommand) Description() string { return "List saved sessions" }
func (c *SessionsCommand) Usage() string       { return "/sessions [--all]" }
func (c *SessionsCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategorySession,
		Icon:     "list",
		Priority: 50,
	}
}

func (c *SessionsCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	hm, err := app.GetHistoryManager()
	if err != nil {
		return fmt.Sprintf("Failed to get history manager: %v", err), nil
	}

	sessions, err := hm.ListSessions()
	if err != nil {
		return fmt.Sprintf("Failed to list sessions: %v", err), nil
	}

	if len(sessions) == 0 {
		return "No saved sessions found.", nil
	}

	workDir := app.GetWorkDir()
	showAll := len(args) > 0 && args[0] == "--all"

	var sb strings.Builder
	shown := 0

	if showAll {
		sb.WriteString("All saved sessions:\n")
	} else {
		sb.WriteString("Saved sessions (current project):\n")
	}

	for _, info := range sessions {
		if !showAll && workDir != "" {
			sessionDir := filepath.Clean(info.WorkDir)
			if info.WorkDir == "" || sessionDir != filepath.Clean(workDir) {
				continue
			}
		}

		summary := info.Summary
		if runes := []rune(summary); len(runes) > 80 {
			summary = string(runes[:77]) + "..."
		}
		if summary == "" {
			summary = "(no summary)"
		}

		dirLabel := ""
		if showAll && info.WorkDir != "" {
			dirLabel = fmt.Sprintf(" [%s]", filepath.Base(info.WorkDir))
		}

		age := formatTimeAgo(info.LastActive)
		fmt.Fprintf(&sb, "  %s (%d messages, %s) — %s%s\n", info.ID, info.MessageCount, age, summary, dirLabel)
		shown++
	}

	if shown == 0 {
		if showAll {
			return "No saved sessions found.", nil
		}
		return "No sessions for current project.\nUse /sessions --all to see sessions from all projects.", nil
	}

	if !showAll {
		sb.WriteString("\nUse /sessions --all to see sessions from all projects.")
	}

	return sb.String(), nil
}

// InitCommand initializes GOKIN.md for the project.
type InitCommand struct{}

func (c *InitCommand) Name() string        { return "init" }
func (c *InitCommand) Description() string { return "Initialize GOKIN.md for this project" }
func (c *InitCommand) Usage() string       { return "/init" }
func (c *InitCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryGit,
		Icon:     "init",
		Priority: 0,
	}
}

func (c *InitCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	workDir := app.GetWorkDir()
	gokinPath := filepath.Join(workDir, "GOKIN.md")

	// Atomic create-or-fail via O_EXCL. The older two-step Stat-then-Write
	// had a TOCTOU gap where a concurrent process (or a follow-up /init
	// double-click) could create GOKIN.md between the existence check and
	// the write, causing silent overwrite of the just-created file.
	template := c.detectTemplate(workDir)
	f, err := os.OpenFile(gokinPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if os.IsExist(err) {
			// Match v0.80.5 /compact pattern: "No-op:" prefix so visually
			// (and for users who skim) this is distinct from the
			// "Created GOKIN.md..." success message below. Same toast
			// styling but different first word — enough signal that
			// nothing was written.
			return "No-op: GOKIN.md already exists. Edit it manually or delete to reinitialize.", nil
		}
		return fmt.Sprintf("Failed to create GOKIN.md: %v", err), nil
	}
	if _, werr := f.WriteString(template); werr != nil {
		_ = f.Close()
		_ = os.Remove(gokinPath)
		return fmt.Sprintf("Failed to write GOKIN.md: %v", werr), nil
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Sprintf("Failed to close GOKIN.md: %v", cerr), nil
	}

	return "Created GOKIN.md with project-specific template. Edit it to refine instructions.", nil
}

func (c *InitCommand) detectTemplate(workDir string) string {
	// Detect Go
	if _, err := os.Stat(filepath.Join(workDir, "go.mod")); err == nil {
		return `# Project Instructions

## Build & Test
` + "```" + `bash
go build ./...
go vet ./...
go test -race ./...
` + "```" + `

## Architecture
<!-- Key packages and their responsibilities -->

## Coding Guidelines
- Follow standard Go conventions (gofmt, go vet)
- Handle errors explicitly; don't use panic in library code
- Write table-driven tests
`
	}

	// Detect Node.js
	if _, err := os.Stat(filepath.Join(workDir, "package.json")); err == nil {
		return `# Project Instructions

## Build & Test
` + "```" + `bash
npm install
npm test
npm run build
` + "```" + `

## Architecture
<!-- Key directories and their purpose -->

## Coding Guidelines
- Use TypeScript where possible
- Run linter before committing
`
	}

	// Detect Python
	for _, f := range []string{"pyproject.toml", "setup.py", "requirements.txt"} {
		if _, err := os.Stat(filepath.Join(workDir, f)); err == nil {
			return `# Project Instructions

## Build & Test
` + "```" + `bash
pip install -e .
pytest
` + "```" + `

## Architecture
<!-- Key modules and their purpose -->

## Coding Guidelines
- Follow PEP 8
- Use type hints
- Write docstrings for public functions
`
		}
	}

	// Detect Rust
	if _, err := os.Stat(filepath.Join(workDir, "Cargo.toml")); err == nil {
		return `# Project Instructions

## Build & Test
` + "```" + `bash
cargo build
cargo test
cargo clippy
` + "```" + `

## Architecture
<!-- Key crates and modules -->

## Coding Guidelines
- Run clippy and fix all warnings
- Use Result<T, E> for error handling
`
	}

	// Generic fallback
	return `# Project Instructions

## Project Overview
<!-- Describe your project -->

## Build & Test
<!-- How to build and test -->

## Architecture
<!-- Key files and their purpose -->

## Coding Guidelines
<!-- Project-specific standards -->
`
}

// DoctorCommand checks environment and configuration.
type DoctorCommand struct{}

func (c *DoctorCommand) Name() string        { return "doctor" }
func (c *DoctorCommand) Description() string { return "Check environment and configuration" }
func (c *DoctorCommand) Usage() string       { return "/doctor" }
func (c *DoctorCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryAuthSetup,
		Icon:     "doctor",
		Priority: 0,
	}
}

func (c *DoctorCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, `
%s╔═══════════════════════════════════════════════════════════════╗
║                    🔍 System Diagnostics                    ║
╚═══════════════════════════════════════════════════════════════╝%s
`, colorCyan, colorReset)

	if v := app.GetVersion(); v != "" {
		fmt.Fprintf(&sb, "  Version: %s%s%s\n", colorGreen, v, colorReset)
	}

	fmt.Fprintf(&sb, "\n%s─── Authentication ───%s\n", colorCyan, colorReset)

	cfg := app.GetConfig()
	issues := []string{}
	solutions := []string{}

	// Check API key via provider registry. Use GetActiveProvider() so
	// the fallback matches what the rest of the system sees as default
	// (was hardcoded to "gemini" — a removed provider — pre-v0.78.29).
	backend := "glm"
	if cfg != nil {
		backend = cfg.API.GetActiveProvider()
	}
	fmt.Fprintf(&sb, "  Backend: %s%s%s\n", colorGreen, backend, colorReset)

	hasKey := false
	if cfg != nil {
		hasKey = config.AnyProviderHasKey(&cfg.API)
	}
	if !hasKey {
		// Check legacy env var
		if os.Getenv("GOKIN_API_KEY") != "" {
			hasKey = true
		}
	}

	if hasKey {
		fmt.Fprintf(&sb, "  Status: %s✓ API key configured%s\n", colorGreen, colorReset)
	} else {
		fmt.Fprintf(&sb, "  Status: %s✗ API key not configured%s\n", colorRed, colorReset)
		issues = append(issues, "API key not found")
		// Default fallback was "GEMINI_API_KEY" — leftover from v0.65 when
		// Gemini was removed but this hint wasn't updated. Now defaults to
		// the GLM env var, which is the project's recommended primary
		// provider (per README and v0.71.0 model recommendations).
		envHint := "GOKIN_GLM_KEY"
		if p := config.GetProvider(backend); p != nil && len(p.EnvVars) > 0 {
			envHint = p.EnvVars[0]
		}
		solutions = append(solutions, fmt.Sprintf("Use /login <provider> <api_key> or set %s", envHint))
	}

	fmt.Fprintf(&sb, "\n%s─── Environment ───%s\n", colorCyan, colorReset)

	// Config file
	configPath := config.GetConfigPath()
	if _, err := os.Stat(configPath); err == nil {
		fmt.Fprintf(&sb, "  %s✓%s Config: %s\n", colorGreen, colorReset, configPath)
	} else {
		fmt.Fprintf(&sb, "  %s○%s Config not found (using defaults)\n", colorYellow, colorReset)
	}

	// Git
	if _, err := exec.LookPath("git"); err == nil {
		fmt.Fprintf(&sb, "  %s✓%s git installed\n", colorGreen, colorReset)
	} else {
		fmt.Fprintf(&sb, "  %s✗%s git not installed\n", colorRed, colorReset)
		issues = append(issues, "Git not installed")
		solutions = append(solutions, "Install git: apt install git / brew install git")
	}

	// GitHub CLI
	if _, err := exec.LookPath("gh"); err == nil {
		fmt.Fprintf(&sb, "  %s✓%s gh (GitHub CLI) installed\n", colorGreen, colorReset)
	} else {
		fmt.Fprintf(&sb, "  %s○%s gh (GitHub CLI) not installed (optional for /pr)\n", colorYellow, colorReset)
	}

	// Git repo check
	workDir := app.GetWorkDir()
	if _, err := os.Stat(filepath.Join(workDir, ".git")); err == nil {
		fmt.Fprintf(&sb, "  %s✓%s Working directory is a git repository\n", colorGreen, colorReset)
	} else {
		fmt.Fprintf(&sb, "  %s○%s Not a git repository (git tools will be limited)\n", colorYellow, colorReset)
	}

	// Project instruction file (GOKIN.md/CLAUDE.md and other supported paths)
	foundInstruction := ""
	for _, filename := range appcontext.InstructionFileNames() {
		path := filepath.Join(workDir, filename)
		if _, err := os.Stat(path); err == nil {
			foundInstruction = filename
			break
		}
	}
	if foundInstruction != "" {
		fmt.Fprintf(&sb, "  %s✓%s Instruction file found: %s\n", colorGreen, colorReset, foundInstruction)
	} else {
		fmt.Fprintf(&sb, "  %s○%s No project instruction file found (GOKIN.md/CLAUDE.md)\n", colorYellow, colorReset)
	}

	// Data directories
	dataDir, _ := getDataDir()
	fmt.Fprintf(&sb, "\n%s─── Directories ───%s\n", colorCyan, colorReset)
	fmt.Fprintf(&sb, "  Data: %s\n", dataDir)

	// Summary
	fmt.Fprintf(&sb, "\n%s─── Summary ───%s\n", colorCyan, colorReset)

	if len(issues) == 0 {
		fmt.Fprintf(&sb, "  %s✓ All systems working properly!%s\n", colorGreen, colorReset)
	} else {
		fmt.Fprintf(&sb, "  %s⚠ Issues detected:%s\n", colorYellow, colorReset)
		for i, issue := range issues {
			fmt.Fprintf(&sb, "    %d. %s\n", i+1, issue)
		}

		fmt.Fprintf(&sb, "\n%sSolutions:%s\n", colorGreen, colorReset)
		for i, solution := range solutions {
			fmt.Fprintf(&sb, "    %d. %s\n", i+1, solution)
		}
	}

	fmt.Fprintf(&sb, "\n%sCommands to fix issues:%s\n", colorCyan, colorReset)
	fmt.Fprintf(&sb, "  %s/login%s    - Set up authentication\n", colorGreen, colorReset)
	fmt.Fprintf(&sb, "  %s/test%s     - Test all settings\n", colorGreen, colorReset)
	fmt.Fprintf(&sb, "  %s/init%s     - Create GOKIN.md template\n", colorGreen, colorReset)

	return sb.String(), nil
}

// ShortcutsCommand displays keyboard shortcuts.
type ShortcutsCommand struct{}

func (c *ShortcutsCommand) Name() string        { return "shortcuts" }
func (c *ShortcutsCommand) Description() string { return "Show keyboard shortcuts" }
func (c *ShortcutsCommand) Usage() string       { return "/shortcuts" }
func (c *ShortcutsCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryGettingStarted,
		Icon:     "shortcuts",
	}
}

func (c *ShortcutsCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n%s Keyboard Shortcuts%s  (also: press ? in empty input)\n\n", colorCyan, colorReset)

	shortcuts := []struct {
		keys, desc string
	}{
		{"Enter", "Send message"},
		{"Ctrl+J / Alt+Enter", "Insert newline"},
		{"Tab", "Autocomplete command"},
		{"Ctrl+P", "Command palette"},
		{"Ctrl+R", "Search input history"},
		{"Ctrl+C", "Cancel operation / Quit"},
		{"Ctrl+L", "Clear screen"},
		{"Ctrl+B / Ctrl+F", "Scroll up / down"},
		{"Ctrl+U / Ctrl+D", "Scroll half page"},
		{"Ctrl+G", "Select mode (freeze + native copy)"},
		{"Ctrl+H", "Context observatory"},
		{"Ctrl+T", "Background tasks"},
		{"Ctrl+O", "Agent activity"},
		{"Shift+Tab", "Toggle plan mode"},
		{"Option+C", "Copy last response"},
		{"e / E", "Expand/collapse tool output"},
		{"?", "This shortcuts overlay"},
	}

	for _, s := range shortcuts {
		fmt.Fprintf(&sb, "  %s%-22s%s %s\n", colorGreen, s.keys, colorReset, s.desc)
	}

	return sb.String(), nil
}

// PwdCommand shows the current working directory.
type PwdCommand struct{}

func (c *PwdCommand) Name() string        { return "pwd" }
func (c *PwdCommand) Description() string { return "Show current working directory" }
func (c *PwdCommand) Usage() string       { return "/pwd" }
func (c *PwdCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "folder",
	}
}

func (c *PwdCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	return app.GetWorkDir(), nil
}

// KeysCommand is an alias for ShortcutsCommand.
type KeysCommand struct{ ShortcutsCommand }

func (c *KeysCommand) Name() string        { return "keys" }
func (c *KeysCommand) Description() string { return "Show keyboard shortcuts (alias for /shortcuts)" }
func (c *KeysCommand) Usage() string       { return "/keys" }

// ConfigCommand shows current configuration.
type ConfigCommand struct{}

func (c *ConfigCommand) Name() string        { return "config" }
func (c *ConfigCommand) Description() string { return "Show current configuration" }
func (c *ConfigCommand) Usage() string       { return "/config" }
func (c *ConfigCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryAuthSetup,
		Icon:     "config",
		Priority: 10,
	}
}

func (c *ConfigCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Configuration not available.", nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%sCurrent Configuration%s\n\n", colorCyan, colorReset)

	// API
	fmt.Fprintf(&sb, "%s─── API ───%s\n", colorCyan, colorReset)
	fmt.Fprintf(&sb, "  Provider: %s%s%s\n", colorGreen, cfg.API.GetActiveProvider(), colorReset)

	// Model
	fmt.Fprintf(&sb, "\n%s─── Model ───%s\n", colorCyan, colorReset)
	fmt.Fprintf(&sb, "  Name:        %s%s%s\n", colorGreen, cfg.Model.Name, colorReset)
	fmt.Fprintf(&sb, "  Temperature: %.1f\n", cfg.Model.Temperature)
	fmt.Fprintf(&sb, "  Max Output:  %d tokens\n", cfg.Model.MaxOutputTokens)

	// UI
	fmt.Fprintf(&sb, "\n%s─── UI ───%s\n", colorCyan, colorReset)
	fmt.Fprintf(&sb, "  Theme:  %s\n", cfg.UI.Theme)
	fmt.Fprintf(&sb, "  Tokens: %v  Stream: %v  Bell: %v\n",
		cfg.UI.ShowTokenUsage, cfg.UI.StreamOutput, cfg.UI.Bell)

	// Context
	fmt.Fprintf(&sb, "\n%s─── Context ───%s\n", colorCyan, colorReset)
	maxInput := cfg.Context.MaxInputTokens
	if maxInput == 0 {
		sb.WriteString("  Max Input: (model default)\n")
	} else {
		fmt.Fprintf(&sb, "  Max Input: %d tokens\n", maxInput)
	}
	fmt.Fprintf(&sb, "  Auto-Compact: %v\n", cfg.Context.EnableAutoSummary)

	// Plan
	fmt.Fprintf(&sb, "\n%s─── Plan ───%s\n", colorCyan, colorReset)
	fmt.Fprintf(&sb, "  Delegate: %v  Clear Context: %v\n",
		cfg.Plan.DelegateSteps, cfg.Plan.ClearContext)

	// Permissions
	fmt.Fprintf(&sb, "\n%s─── Permissions ───%s\n", colorCyan, colorReset)
	fmt.Fprintf(&sb, "  Enabled: %v  Policy: %s\n",
		cfg.Permission.Enabled, cfg.Permission.DefaultPolicy)

	// Config path
	configPath := config.GetConfigPath()
	fmt.Fprintf(&sb, "\n%sConfig file:%s %s\n", colorCyan, colorReset, configPath)

	return sb.String(), nil
}

// PermissionsCommand toggles permission prompts.
type PermissionsCommand struct{}

func (c *PermissionsCommand) Name() string        { return "permissions" }
func (c *PermissionsCommand) Description() string { return "Toggle permission prompts" }
func (c *PermissionsCommand) Usage() string {
	return `/permissions      - Show status
/permissions on   - Enable prompts
/permissions off  - YOLO mode`
}
func (c *PermissionsCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "shield",
		Priority: 20,
		HasArgs:  true,
		ArgHint:  "on|off",
	}
}

func (c *PermissionsCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Config not available", nil
	}

	// No args - show current status
	if len(args) == 0 {
		if cfg.Permission.Enabled {
			return "permissions: on", nil
		}
		return "permissions: off (YOLO)", nil
	}

	// Toggle based on argument
	switch strings.ToLower(args[0]) {
	case "on", "true", "1", "enable":
		cfg.Permission.Enabled = true
		if err := app.ApplyConfig(cfg); err != nil {
			return fmt.Sprintf("Failed: %v", err), nil
		}
		return "permissions: on", nil

	case "off", "false", "0", "disable":
		cfg.Permission.Enabled = false
		if err := app.ApplyConfig(cfg); err != nil {
			return fmt.Sprintf("Failed: %v", err), nil
		}
		return "permissions: off (YOLO)", nil

	default:
		return "/permissions on | off", nil
	}
}

// SandboxCommand toggles bash sandbox mode.
type SandboxCommand struct{}

func (c *SandboxCommand) Name() string        { return "sandbox" }
func (c *SandboxCommand) Description() string { return "Toggle bash sandbox mode" }
func (c *SandboxCommand) Usage() string {
	return `/sandbox      - Show status
/sandbox on   - Safe mode
/sandbox off  - Unrestricted`
}
func (c *SandboxCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "sandbox",
		Priority: 30,
		HasArgs:  true,
		ArgHint:  "on|off",
		Advanced: true,
	}
}

func (c *SandboxCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Config not available", nil
	}

	// No args - show current status
	if len(args) == 0 {
		if cfg.Tools.Bash.Sandbox {
			return "sandbox: on", nil
		}
		return "sandbox: off (!SANDBOX)", nil
	}

	// Toggle based on argument
	switch strings.ToLower(args[0]) {
	case "on", "true", "1", "enable":
		cfg.Tools.Bash.Sandbox = true
		if err := app.ApplyConfig(cfg); err != nil {
			return fmt.Sprintf("Failed: %v", err), nil
		}
		return "sandbox: on", nil

	case "off", "false", "0", "disable":
		cfg.Tools.Bash.Sandbox = false
		if err := app.ApplyConfig(cfg); err != nil {
			return fmt.Sprintf("Failed: %v", err), nil
		}
		return "sandbox: off (!SANDBOX)", nil

	default:
		return "/sandbox on | off", nil
	}
}

// ClearTodosCommand clears all todo items.
type ClearTodosCommand struct{}

func (c *ClearTodosCommand) Name() string        { return "clear-todos" }
func (c *ClearTodosCommand) Description() string { return "Clear all todo items" }
func (c *ClearTodosCommand) Usage() string       { return "/clear-todos" }
func (c *ClearTodosCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "clear",
		Priority: 10,
	}
}

func (c *ClearTodosCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	todoTool := app.GetTodoTool()
	if todoTool == nil {
		return "Todo tool not available.", nil
	}
	todoTool.ClearItems()
	return "Todo list cleared.", nil
}

// BrowseCommand opens an interactive file browser.
type BrowseCommand struct{}

func (c *BrowseCommand) Name() string        { return "browse" }
func (c *BrowseCommand) Description() string { return "Open interactive file browser" }
func (c *BrowseCommand) Usage() string       { return "/browse [path]" }
func (c *BrowseCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "folder",
		Priority: 0,
		HasArgs:  true,
		ArgHint:  "[path]",
	}
}

func (c *BrowseCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	startPath := app.GetWorkDir()
	if len(args) > 0 {
		startPath = args[0]
		// Handle relative paths
		if !filepath.IsAbs(startPath) {
			startPath = filepath.Join(app.GetWorkDir(), startPath)
		}
	}

	// Verify path exists
	info, err := os.Stat(startPath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), nil
	}

	// If it's a file, use its directory
	if !info.IsDir() {
		startPath = filepath.Dir(startPath)
	}

	return "__browse:" + startPath, nil
}

// getDataDir returns the data directory for the application.
func getDataDir() (string, error) {
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dataDir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataDir, "gokin"), nil
}

// getCommandExample returns usage examples for a command.
func getCommandExample(name string) string {
	examples := map[string]string{
		"commit":   "  /commit              — AI generates commit message from staged changes\n  /commit fix typo     — commit with custom message",
		"model":    "  /model glm-5.1               — switch to GLM 5.1 (Z.AI Coding Plan default)\n  /model deepseek-v4-pro       — switch to DeepSeek V4 Pro\n  /model kimi-for-coding       — switch to Kimi K2.6\n  /model MiniMax-M2.7          — switch to MiniMax",
		"plan":     "  /plan                — toggle planning mode on/off\n  Then type a complex task and it will be broken into steps",
		"resume":   "  /resume abc123       — restore session abc123\n  /resume abc123 --force — restore even from different project",
		"save":     "  /save                — save current session for later /resume",
		"compact":  "  /compact             — summarize old messages to free context space",
		"clear":    "  /clear               — start fresh (saves active plan for /resume-plan)",
		"theme":    "  /theme dark          — soft purple/cyan dark theme\n  /theme macos          — Apple-inspired theme\n  /theme light          — for light terminal backgrounds",
		"doctor":   "  /doctor              — check API key, git, config, and project setup",
		"login":    "  /login glm <key>          — set Z.AI / GLM key\n  /login deepseek <key>     — set DeepSeek key\n  /login kimi <key>         — set Kimi Coding Plan key\n  /login minimax <key>      — set MiniMax key",
		"undo":     "  /undo                — revert the last file change made by the AI",
		"redo":     "  /redo                — re-apply the last undone change",
		"stats":    "  /stats               — show tokens, cost, cache hit rate, project info",
		"status":   "  /status              — show provider, model, API keys, workdir, version",
		"update":   "  /update              — check for new versions\n  /update install        — download and install latest\n  /update rollback       — revert to previous version",
		"provider": "  /provider glm         — switch to GLM (Z.AI)\n  /provider deepseek    — switch to DeepSeek\n  /provider kimi        — switch to Kimi\n  /provider minimax     — switch to MiniMax\n  /provider ollama      — switch to local Ollama",
		// v0.77.x git-inspect family + v0.78.12 /blame
		"diff":     "  /diff                — show working-tree diff\n  /diff --staged       — show staged-only diff\n  /diff --stat         — summary instead of patch\n  /diff main.go        — diff one file",
		"log":      "  /log                 — last 10 commits\n  /log 20              — last 20 commits\n  /log internal/app    — commits touching a path\n  /log 5 README.md     — last 5 touching README",
		"branches": "  /branches            — local branches sorted by activity\n  /branches --all      — include remote-tracking branches",
		"grep":     "  /grep TODO           — case-insensitive working-tree search\n  /grep -w foo         — whole-word match\n  /grep -C 2 panic     — 2 lines of context\n  /grep \"old func\" internal/  — scoped to a path",
		"blame":    "  /blame internal/app/app.go         — full-file authorship (capped 200 lines)\n  /blame internal/app/app.go 100-150 — line range\n  /blame README.md 1                 — single line",
		"show":     "  /show              — show HEAD (most recent commit)\n  /show abc123       — show a specific commit\n  /show HEAD~3       — three commits back\n  /show abc123 main.go — scope diff to one file",
		// v0.74–v0.76 release / upgrade feedback loop
		"whats-new": "  /whats-new           — release notes for the current version",
		"changelog": "  /changelog           — compact list of recent releases",
		"restart":   "  /restart             — re-exec into the latest installed binary (for self-update)",
		// v0.78.26 — fill out examples for the rest of the user-facing
		// complex commands. Trivial commands (/pwd /paste /ql /shortcuts
		// /sessions /logout) just have their Usage line and skip examples;
		// these benefit from concrete invocations.
		"pr":          "  /pr                          — show pending PR info\n  /pr --title \"Fix bug\"        — create PR with title\n  /pr --draft --title \"WIP\"    — create draft PR\n  /pr --base main --title \"…\"  — target a specific base",
		"mcp":         "  /mcp list             — show configured MCP servers\n  /mcp status           — connection health\n  /mcp add <name> <cmd> — register a server\n  /mcp remove <name>    — unregister\n  /mcp refresh <name>   — re-list tools",
		"memory":      "  /memory               — show all stored memories\n  Project memories live in .gokin/MEMORY.md",
		"sandbox":     "  /sandbox on           — gate bash on permission prompts\n  /sandbox off          — disable bash safety prompts (yolo)",
		"permissions": "  /permissions on       — prompt before write/edit/bash\n  /permissions off      — auto-approve (yolo mode)",
		"thinking":    "  /thinking on          — show provider's reasoning trace inline\n  /thinking off         — hide reasoning",
		"open":        "  /open main.go         — open file in $EDITOR (or vi)\n  /open internal/app/app.go",
		"resume-plan": "  /resume-plan          — restore the plan saved by the last /clear",
		"recovery":    "  /recovery             — show the recovery snapshot from the last unclean shutdown",
		"checkpoints": "  /checkpoints          — list session checkpoints (auto-saved every N messages)",
		"config":      "  /config               — print active config + which file it came from",
		"init":        "  /init                 — bootstrap GOKIN.md for this project",
		"sessions":    "  /sessions             — list saved sessions (most recent first)",
		"logout":      "  /logout               — clear API key for the current provider\n  /logout all           — clear all stored keys",
	}
	return examples[name]
}

// getRelatedCommands returns related commands for a command.
func getRelatedCommands(name string) string {
	related := map[string]string{
		"commit":      "/pr, /diff, /log, /save",
		"save":        "/resume, /sessions, /clear",
		"resume":      "/save, /sessions",
		"sessions":    "/save, /resume",
		"clear":       "/save, /compact, /resume-plan",
		"compact":     "/clear, /cost, /stats",
		"model":       "/provider, /config, /reasoning",
		"plan":        "/resume-plan, /tree-stats",
		"resume-plan": "/plan",
		"login":       "/logout, /provider, /doctor",
		"doctor":      "/login, /config, /status",
		"config":      "/doctor, /model, /theme",
		"theme":       "/config",
		"cost":        "/stats, /compact",
		"shortcuts":   "/help, /keys",
		"keys":        "/help, /shortcuts",
		"undo":        "/redo, /checkpoints",
		"redo":        "/undo",
		"stats":       "/cost, /compact, /status",
		"status":      "/doctor, /config, /stats",
		"update":      "/doctor, /status, /restart, /whats-new",
		"quickstart":  "/help, /doctor",
		"pr":          "/commit, /diff, /log",
		"permissions": "/sandbox, /config",
		"sandbox":     "/permissions",
		"provider":    "/model, /login, /status",
		"reasoning":   "/model",
		"checkpoints": "/undo, /plan",
		"pwd":         "/status, /browse",
		"browse":      "/pwd, /open",
		"init":        "/doctor, /instructions",
		// v0.77.x git-inspect set: cross-link the family so users find adjacent tools
		"diff":      "/log, /branches, /grep, /blame, /commit",
		"log":       "/diff, /branches, /grep, /blame, /commit",
		"branches":  "/log, /diff, /pr",
		"grep":      "/log, /diff, /blame, /open",
		"blame":     "/log, /diff, /grep, /show, /commit",
		"show":      "/log, /diff, /blame, /commit",
		"whats-new": "/changelog, /update, /restart",
		"changelog": "/whats-new, /update",
		"restart":   "/update, /whats-new",
		// v0.78.26 — fill out see-also for the rest of user-facing commands.
		"mcp":         "/permissions, /tools_list, /stats",
		"memory":      "/clear, /compact, /memory-governance",
		"thinking":    "/model, /reasoning",
		"logout":      "/login, /provider, /status",
		"open":        "/grep, /blame, /diff, /browse",
		"recovery":    "/journal, /resume-plan, /clear",
		"journal":     "/recovery, /policy, /ledger",
		"policy":      "/journal, /ledger, /sandbox",
		"ledger":      "/journal, /policy, /plan-proof",
		"plan-proof":  "/journal, /ledger, /policy",
		"observability": "/stats, /journal, /policy",
		"tree-stats":  "/plan, /stats, /resume-plan",
		"memory-governance": "/memory, /compact, /clear",
		"health":      "/stats, /policy, /observability",
	}
	return related[name]
}

// formatTimeAgo returns a human-readable relative time string.
func formatTimeAgo(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "yesterday"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}
