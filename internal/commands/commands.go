package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"gokin/internal/agent"
	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/hooks"
	"gokin/internal/mcp"
	"gokin/internal/plan"
	"gokin/internal/tools"
	"gokin/internal/undo"
)

// TokenStats holds token usage statistics for the session.
type TokenStats struct {
	InputTokens                int
	OutputTokens               int
	CacheCreationInputTokens   int
	CacheReadInputTokens       int
	PromptCacheBreaks          int
	LastPromptCacheBreakReason string
	TotalTokens                int
	EstimatedCost              float64
	CostTracked                bool
}

// Command represents a slash command.
type Command interface {
	Name() string
	Description() string
	Usage() string
	Execute(ctx context.Context, args []string, app AppInterface) (string, error)
}

// ModelSetter allows changing the current model.
type ModelSetter interface {
	GetModel() string
	SetModel(modelName string)
}

// AppInterface defines what commands need from the application.
type AppInterface interface {
	GetSession() *chat.Session
	GetHistoryManager() (*chat.HistoryManager, error)
	GetContextManager() *appcontext.ContextManager
	GetUndoManager() *undo.Manager
	GetWorkDir() string
	ClearConversation()
	// Directory access grants (Claude-Code-style /add-dir + ask-on-access).
	GrantAllowedDir(path string, persist bool) (string, error)
	RevokeGrantedDir(path string) (bool, error)
	ListAllowedDirs() []string
	GetTodoTool() *tools.TodoTool
	GetConfig() *config.Config
	GetTokenStats() TokenStats
	GetModelSetter() ModelSetter
	GetProjectInfo() *appcontext.ProjectInfo
	GetPlanManager() *plan.Manager
	GetTreePlanner() *agent.TreePlanner
	IsPlanningModeEnabled() bool
	TogglePlanningMode() bool // Returns new state
	ApplyConfig(cfg *config.Config) error
	GetVersion() string
	AddSystemMessage(msg string)
	GetAgentTypeRegistry() *agent.AgentTypeRegistry
	GetUIDebugState() (any, error)
	GetRuntimeHealthReport() string
	GetPolicyReport() string
	GetLedgerReport() string
	GetPlanProofReport(stepID int) string
	GetJournalReport() string
	GetRecoveryReport() string
	GetObservabilityReport() string
	GetSessionGovernanceReport() string
	GetMemoryReport() string
	GetPerformanceStats() string
	RefreshTokenCount()

	// MCP (Model Context Protocol) — used by /mcp commands to introspect
	// and mutate server configuration at runtime. All three may be nil when
	// MCP is disabled; callers must nil-check.
	GetMCPManager() *mcp.Manager
	GetToolRegistry() *tools.Registry
	GetMainClient() client.Client

	// EnableMCP / DisableMCP toggle MCP support at runtime without requiring
	// a config edit + restart. EnableMCP creates an empty manager so /mcp add
	// can bootstrap immediately.
	EnableMCP() error
	DisableMCP() error

	// Loop manager (v0.81+) — autonomous recurring task system. May be
	// nil if loops aren't wired (e.g. in unit tests of other commands).
	// /loop subcommands check for nil and return a clear "unavailable"
	// message rather than crashing.
	GetLoopManager() LoopManager
	// CancelInFlightLoopIteration kills the currently-executing iteration of
	// the given loop ("" = any). /loop stop uses it so stopping a loop also
	// stops the work on screen. Returns whether an iteration was cancelled.
	CancelInFlightLoopIteration(loopID string) bool

	// Agent task runner — used by /tasks to list background agents and
	// inspect their results. May be nil when the agent subsystem isn't
	// wired; the command nil-checks.
	GetAgentTaskRunner() AgentTaskRunner

	// Background shell task runner — used by /tasks to list/stop bash/ssh
	// run_in_background commands. Before this, only the MODEL could list
	// (task_output-equivalent doesn't exist for shell) or kill (kill_shell
	// tool) a background shell command; the user had no surface at all — a
	// stray `npm run dev &`-style task from a finished turn kept holding a
	// port with no way to find or stop it short of quitting gokin. May be
	// nil when the tasks subsystem isn't wired; the command nil-checks.
	GetBackgroundShellRunner() BackgroundShellRunner

	// Audit runner — used by /audit for its fixed find-then-verify recipe
	// (SpawnMultiple fan-out + GetResult). May be nil when the agent
	// subsystem isn't wired; the command nil-checks.
	GetAuditRunner() AuditRunner

	// Hooks manager — used by /hooks to list configured hooks. May be nil;
	// the command nil-checks.
	GetHooksManager() *hooks.Manager
}

type checkedConversationClearer interface {
	ClearConversationChecked() error
}

// ErrConversationClearedUncertainDurability marks a ClearConversationChecked
// error where the conversation genuinely cleared — every in-memory reset ran
// to completion — but its on-disk durability confirmation could not be
// verified (a rename couldn't be fsync-confirmed, not that nothing happened).
// App wraps its internal uncertain-durability sentinel with this one (see
// app.go's ClearConversationChecked) so commands can distinguish it WITHOUT
// importing internal/app (app already imports commands; the reverse would
// cycle). Callers of clearConversationChecked below must treat this case as
// SUCCESS — the same distinction app.ClearConversation and
// reportPendingRecoveryClearFailure already make — never as "nothing
// happened, abort the dependent operation".
var ErrConversationClearedUncertainDurability = errors.New("conversation cleared, durability unconfirmed")

func clearConversationChecked(app AppInterface) error {
	checked, ok := app.(checkedConversationClearer)
	if !ok {
		app.ClearConversation()
		return nil
	}
	err := checked.ClearConversationChecked()
	if err != nil && errors.Is(err, ErrConversationClearedUncertainDurability) {
		return nil
	}
	return err
}

// Handler manages slash commands.
// The commands map is populated during NewHandler() plus the boot-phase
// loaders (LoadFileCommands, like LoadAliasesFromFile for aliases) and is
// immutable once startup completes — the builder seals the boot phase via
// SealBootPhase after all loaders finish, before the handler is shared
// across goroutines. This makes Handler safe for concurrent use without a
// mutex on the commands map. Register() panics once NewHandler returns;
// the boot-phase loaders panic after SealBootPhase.
type Handler struct {
	commands map[string]Command
	frozen   bool
	// bootDone is set by SealBootPhase, called by the builder after all
	// boot-phase loaders (LoadFileCommands, LoadAliasesFromFile) finish.
	// Distinct from frozen: frozen blocks Register (inside NewHandler),
	// bootDone blocks the post-construction loaders. Two flags because
	// the boot loaders run AFTER NewHandler returns but BEFORE the handler
	// is shared — a single flag would either block the legitimate loaders
	// or fail to catch late Register calls.
	bootDone bool

	// aliases maps short names to canonical command names. Populated once via
	// LoadAliases during startup; treated as immutable afterwards. Resolved in
	// Parse before the canonical lookup so both the real name and the alias
	// work interchangeably.
	aliases map[string]string
}

// defaultAliases hard-codes common short forms so first-time users get useful
// shortcuts without writing ~/.config/gokin/aliases.yaml. Users can shadow
// these via the config file.
var defaultAliases = map[string]string{
	"p":  "plan",
	"c":  "commit",
	"m":  "model",
	"s":  "status",
	"u":  "undo",
	"r":  "redo",
	"h":  "help",
	"q":  "clear",
	"st": "stats",
	"pr": "pr",
}

// NewHandler creates a new command handler with built-in commands.
func NewHandler() *Handler {
	h := &Handler{
		commands: make(map[string]Command),
		aliases:  cloneAliases(defaultAliases),
	}

	// Register built-in commands
	h.Register(&HelpCommand{handler: h})
	h.Register(&ClearCommand{})
	h.Register(&CompactCommand{})
	h.Register(&SaveCommand{})
	h.Register(&ResumeCommand{})
	h.Register(&SessionsCommand{})
	h.Register(&LoopCommand{})
	// Register git commands
	h.Register(&CommitCommand{})
	h.Register(&PRCommand{})
	h.Register(&DiffCommand{})
	h.Register(&LogCommand{})
	h.Register(&BranchesCommand{})
	h.Register(&GrepCommand{})
	h.Register(&BlameCommand{})
	h.Register(&ShowCommand{})

	// Register utility commands
	h.Register(&InitCommand{})
	h.Register(&DoctorCommand{})
	h.Register(&ConfigCommand{})
	h.Register(&SetCommand{})
	h.Register(&SettingsCommand{})
	h.Register(&LoginCommand{})
	h.Register(&LogoutCommand{})
	h.Register(&ProviderCommand{})
	h.Register(&StatusCommand{})
	h.Register(&ModelCommand{})
	h.Register(&PermissionsCommand{})
	h.Register(&SandboxCommand{})
	h.Register(&ThinkingCommand{})

	// Register interactive commands
	h.Register(&BrowseCommand{})
	h.Register(&OpenCommand{})
	h.Register(&ClearTodosCommand{})

	// Register context commands
	h.Register(&InstructionsCommand{})

	// Register onboarding commands
	h.Register(&QuickstartCommand{})
	h.Register(&ShortcutsCommand{})
	h.Register(&KeysCommand{})

	// Register stats and memory commands
	h.Register(&StatsCommand{})
	h.Register(&TasksCommand{})
	h.Register(&AuditCommand{})
	h.Register(&HooksCommand{})
	h.Register(&SkillCommand{})
	h.Register(&AddDirCommand{})
	h.Register(&RemoveDirCommand{})
	h.Register(&MemoryCommand{})

	// Register theme command
	h.Register(&ThemeCommand{})

	// Register planning mode command
	h.Register(&PlanCommand{})
	h.Register(&ResumePlanCommand{})
	h.Register(&HealthCommand{})
	h.Register(&PolicyCommand{})
	h.Register(&LedgerCommand{})
	h.Register(&PlanProofCommand{})
	h.Register(&JournalCommand{})
	h.Register(&RecoveryCommand{})
	h.Register(&ObservabilityCommand{})
	h.Register(&MemoryGovernanceCommand{})
	h.Register(&InsightsCommand{})

	// Register tree planner command
	h.Register(&TreeStatsCommand{})

	// Register agent type commands
	h.Register(&RegisterAgentTypeCommand{})
	h.Register(&ListAgentTypesCommand{})
	h.Register(&UnregisterAgentTypeCommand{})

	// Register clipboard commands (cross-platform)
	h.Register(&CopyCommand{})
	h.Register(&PasteCommand{})
	h.Register(&QuickLookCommand{})

	// Register update command
	h.Register(&UpdateCommand{})
	h.Register(&RestartCommand{})
	h.Register(&WhatsNewCommand{})
	h.Register(&ChangelogCommand{})

	// Register debug command (hidden)
	h.Register(&DebugDumpCommand{})

	// Register undo/redo/cost commands
	h.Register(&UndoCommand{})
	h.Register(&RedoCommand{})
	h.Register(&CostCommand{})

	// Register checkpoint and utility commands
	h.Register(&CheckpointCommand{})
	h.Register(&PwdCommand{})

	// Register MCP (Model Context Protocol) command
	h.Register(&MCPCommand{})

	h.frozen = true
	return h
}

// NewHandlerWithCommands creates a frozen handler containing exactly the
// given commands (plus default aliases). Intended for tests that need to
// exercise a single command's Execute path without the full built-in set.
func NewHandlerWithCommands(cmds ...Command) *Handler {
	h := &Handler{
		commands: make(map[string]Command),
		aliases:  cloneAliases(defaultAliases),
	}
	for _, c := range cmds {
		h.commands[c.Name()] = c
	}
	h.frozen = true
	h.bootDone = true
	return h
}

// Register adds a command to the handler.
// Must only be called during NewHandler construction; panics if called after.
func (h *Handler) Register(cmd Command) {
	if h.frozen {
		panic("commands: Register called after NewHandler completed")
	}
	h.commands[cmd.Name()] = cmd
}

// SealBootPhase marks the boot phase as done. Called by the builder after all
// boot-phase loaders (LoadFileCommands, LoadAliasesFromFile) have finished.
// After this call the handler is considered immutable and shared across
// goroutines; any further loader call panics.
func (h *Handler) SealBootPhase() {
	h.bootDone = true
}

// Parse checks if input is a slash command and extracts name and args.
// Returns (name, args, isCommand).
// Important: paths like /home/user/... are NOT treated as commands.
func (h *Handler) Parse(input string) (string, []string, bool) {
	input = strings.TrimSpace(input)

	// Must start with /
	if !strings.HasPrefix(input, "/") {
		return "", nil, false
	}

	parts := splitCommandFields(input)
	if len(parts) == 0 {
		return "", nil, false
	}

	// Extract command name (without /). Slash commands are case-insensitive;
	// command names are registered in lowercase.
	name := strings.ToLower(strings.TrimPrefix(parts[0], "/"))

	// Alias resolution: if `name` isn't a direct command but matches a
	// known alias, swap to the canonical name before the existence check.
	if _, exists := h.commands[name]; !exists {
		if target, ok := h.aliases[name]; ok {
			if _, targetExists := h.commands[target]; targetExists {
				name = target
			}
		}
	}

	// Check if it's a known command (not a path)
	if _, exists := h.commands[name]; !exists {
		return "", nil, false
	}

	// Return name and args
	var args []string
	if len(parts) > 1 {
		args = parts[1:]
	}

	return name, args, true
}

func splitCommandFields(input string) []string {
	var fields []string
	var b strings.Builder
	var quote rune
	tokenStarted := false
	escaped := false

	for _, r := range input {
		if escaped {
			if r == quote || r == '\\' {
				b.WriteRune(r)
			} else {
				b.WriteRune('\\')
				b.WriteRune(r)
			}
			tokenStarted = true
			escaped = false
			continue
		}

		if quote != 0 {
			switch r {
			case '\\':
				escaped = true
			case quote:
				quote = 0
				tokenStarted = true
			default:
				b.WriteRune(r)
				tokenStarted = true
			}
			continue
		}

		switch {
		case (r == '"' || r == '\'') && b.Len() == 0:
			// A quote opens quote mode ONLY at token start. A mid-word quote
			// is literal: without this, `/grep don't` silently searched for
			// "dont" and `/loop fix Bob's PR --max-tokens 200k` merged
			// everything after the apostrophe into one arg — swallowing the
			// --max-tokens flag entirely (the budget silently not applied).
			// Prose arguments with contractions/apostrophes are far more
			// common in a chat CLI than mid-word quoting.
			quote = r
			tokenStarted = true
		case unicode.IsSpace(r):
			if tokenStarted {
				fields = append(fields, b.String())
				b.Reset()
				tokenStarted = false
			}
		default:
			b.WriteRune(r)
			tokenStarted = true
		}
	}

	if escaped {
		b.WriteRune('\\')
	}
	if tokenStarted {
		fields = append(fields, b.String())
	}
	return fields
}

// Execute runs a command by name.
func (h *Handler) Execute(ctx context.Context, name string, args []string, app AppInterface) (string, error) {
	cmd, exists := h.commands[name]
	if !exists {
		if closest := h.findClosestCommand(name); closest != "" {
			return "", fmt.Errorf("unknown command: /%s. Did you mean /%s?", name, closest)
		}
		return "", fmt.Errorf("unknown command: /%s", name)
	}

	return cmd.Execute(ctx, args, app)
}

// findClosestCommand finds the closest matching command name using edit distance.
//
// Tie-breaking: a first-character mismatch adds 1 to the distance. Typos
// rarely affect the leading letter, so among same-distance candidates we
// prefer the one that starts with the same character as the input. Also
// makes the result deterministic (without this, the map-iteration order
// decides ties — flaky across runs).
func (h *Handler) findClosestCommand(input string) string {
	bestMatch := ""
	bestDist := 3 // Max edit distance threshold

	for name := range h.commands {
		dist := levenshtein(input, name)
		if len(input) > 0 && len(name) > 0 && input[0] != name[0] {
			dist++
		}
		if dist < bestDist {
			bestDist = dist
			bestMatch = name
		}
	}

	return bestMatch
}

// levenshtein computes the edit distance between two strings.
func levenshtein(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	// Use single-row optimization
	prev := make([]int, lb+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		curr := make([]int, lb+1)
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(curr[j-1]+1, min(prev[j]+1, prev[j-1]+cost))
		}
		prev = curr
	}

	return prev[lb]
}

// ListCommands returns all registered commands.
func (h *Handler) ListCommands() []Command {
	cmds := make([]Command, 0, len(h.commands))
	for _, cmd := range h.commands {
		cmds = append(cmds, cmd)
	}
	return cmds
}

// GetCommand returns a command by name.
func (h *Handler) GetCommand(name string) (Command, bool) {
	cmd, exists := h.commands[name]
	return cmd, exists
}

// GetPaletteCommands returns all commands formatted for the palette.
func (h *Handler) GetPaletteCommands(ctx PaletteContext) []PaletteCommand {
	var result []PaletteCommand

	for _, cmd := range h.commands {
		meta := h.getCommandMetadata(cmd)

		// Skip hidden commands
		if meta.Hidden {
			continue
		}

		state := h.GetCommandState(cmd.Name(), ctx)
		catInfo := GetCategoryInfo(meta.Category)

		result = append(result, PaletteCommand{
			Name:        cmd.Name(),
			Description: cmd.Description(),
			Usage:       cmd.Usage(),
			Category:    catInfo,
			Icon:        meta.Icon,
			ArgHint:     meta.ArgHint,
			State:       state,
			Priority:    catInfo.Priority*100 + meta.Priority,
			Advanced:    meta.Advanced,
		})
	}

	return result
}

// GetCommandState returns the enabled/disabled state for a command.
func (h *Handler) GetCommandState(name string, ctx PaletteContext) CommandState {
	cmd, exists := h.commands[name]
	if !exists {
		return DisabledState("Unknown command")
	}

	meta := h.getCommandMetadata(cmd)

	// Platform check
	if meta.Platform != "" && meta.Platform != ctx.Platform {
		return DisabledState(meta.Platform + " only")
	}

	// Git requirement check
	if meta.RequiresGit && !ctx.IsGitRepo {
		return DisabledState("Not a git repo")
	}

	// API key requirement check
	if meta.RequiresAPI && !ctx.HasAPIKey {
		return DisabledState("API key required")
	}

	return EnabledState()
}

// getCommandMetadata retrieves metadata from a command.
func (h *Handler) getCommandMetadata(cmd Command) CommandMetadata {
	if provider, ok := cmd.(MetadataProvider); ok {
		return provider.GetMetadata()
	}
	return DefaultMetadata()
}

// PaletteProviderAdapter wraps a Handler with a context to implement ui.PaletteProvider.
type PaletteProviderAdapter struct {
	handler *Handler
	ctx     PaletteContext
}

// NewPaletteProvider creates a new palette provider adapter.
func NewPaletteProvider(handler *Handler, ctx PaletteContext) *PaletteProviderAdapter {
	return &PaletteProviderAdapter{handler: handler, ctx: ctx}
}

// UpdateContext updates the palette context.
func (p *PaletteProviderAdapter) UpdateContext(ctx PaletteContext) {
	p.ctx = ctx
}

// PaletteCommandForUI represents command info in UI-friendly format.
// It implements ui.PaletteCommandData interface.
type PaletteCommandForUI struct {
	name         string
	description  string
	usage        string
	categoryName string
	categoryIcon string
	categoryPrio int
	icon         string
	argHint      string
	enabled      bool
	reason       string
	priority     int
	advanced     bool
}

// Implement ui.PaletteCommandData interface
func (c *PaletteCommandForUI) GetName() string          { return c.name }
func (c *PaletteCommandForUI) GetDescription() string   { return c.description }
func (c *PaletteCommandForUI) GetUsage() string         { return c.usage }
func (c *PaletteCommandForUI) GetCategoryName() string  { return c.categoryName }
func (c *PaletteCommandForUI) GetCategoryIcon() string  { return c.categoryIcon }
func (c *PaletteCommandForUI) GetCategoryPriority() int { return c.categoryPrio }
func (c *PaletteCommandForUI) GetIcon() string          { return c.icon }
func (c *PaletteCommandForUI) GetArgHint() string       { return c.argHint }
func (c *PaletteCommandForUI) IsEnabled() bool          { return c.enabled }
func (c *PaletteCommandForUI) GetReason() string        { return c.reason }
func (c *PaletteCommandForUI) GetPriority() int         { return c.priority }
func (c *PaletteCommandForUI) IsAdvanced() bool         { return c.advanced }

// GetPaletteCommandsForUI implements ui.PaletteProvider interface.
// Returns []any where each element implements ui.PaletteCommandData.
func (p *PaletteProviderAdapter) GetPaletteCommandsForUI() []any {
	paletteCmds := p.handler.GetPaletteCommands(p.ctx)
	result := make([]any, 0, len(paletteCmds))

	for _, pc := range paletteCmds {
		result = append(result, &PaletteCommandForUI{
			name:         pc.Name,
			description:  pc.Description,
			usage:        pc.Usage,
			categoryName: pc.Category.Name,
			categoryIcon: pc.Category.Icon,
			categoryPrio: pc.Category.Priority,
			icon:         pc.Icon,
			argHint:      pc.ArgHint,
			enabled:      pc.State.Enabled,
			reason:       pc.State.Reason,
			priority:     pc.Priority,
			advanced:     pc.Advanced,
		})
	}

	return result
}
