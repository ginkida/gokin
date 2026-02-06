package commands

import "runtime"

// CommandCategory represents a category for grouping commands.
type CommandCategory string

const (
	CategoryGettingStarted CommandCategory = "getting_started"
	CategorySession        CommandCategory = "session"
	CategoryAuthSetup      CommandCategory = "auth_setup"
	CategoryGit            CommandCategory = "git"
	CategoryPlanning       CommandCategory = "planning"
	CategoryTools          CommandCategory = "tools"
)

// CategoryInfo contains display information for a category.
type CategoryInfo struct {
	ID       CommandCategory
	Name     string
	Icon     string
	Priority int // Lower is higher priority (shown first)
}

// GetCategoryInfo returns display information for a category.
func GetCategoryInfo(cat CommandCategory) CategoryInfo {
	info, ok := categoryInfoMap[cat]
	if !ok {
		return CategoryInfo{ID: cat, Name: string(cat), Icon: "?", Priority: 999}
	}
	return info
}

// GetAllCategories returns all categories in display order.
func GetAllCategories() []CategoryInfo {
	return []CategoryInfo{
		categoryInfoMap[CategoryGettingStarted],
		categoryInfoMap[CategorySession],
		categoryInfoMap[CategoryAuthSetup],
		categoryInfoMap[CategoryGit],
		categoryInfoMap[CategoryPlanning],
		categoryInfoMap[CategoryTools],
	}
}

var categoryInfoMap = map[CommandCategory]CategoryInfo{
	CategoryGettingStarted: {ID: CategoryGettingStarted, Name: "Getting Started", Icon: "rocket", Priority: 0},
	CategorySession:        {ID: CategorySession, Name: "Session", Icon: "chat", Priority: 1},
	CategoryAuthSetup:      {ID: CategoryAuthSetup, Name: "Auth & Setup", Icon: "lock", Priority: 2},
	CategoryGit:            {ID: CategoryGit, Name: "Git", Icon: "git", Priority: 3},
	CategoryPlanning:       {ID: CategoryPlanning, Name: "Planning", Icon: "tree", Priority: 4},
	CategoryTools:          {ID: CategoryTools, Name: "Tools", Icon: "gear", Priority: 5},
}

// CommandMetadata contains extended information about a command.
type CommandMetadata struct {
	Category    CommandCategory
	Icon        string // Icon key for UI rendering
	Priority    int    // Sort priority within category (lower = higher)
	Platform    string // "darwin", "linux", "windows", or "" for all
	RequiresGit bool   // Requires git repository
	RequiresAPI bool   // Requires API key configured
	HasArgs     bool   // Command accepts arguments
	ArgHint     string // Short hint for arguments (e.g., "[name]", "-m msg")
	Hidden      bool   // Hide from palette (internal commands)
	Advanced    bool   // Hidden from default palette view, visible when searching
}

// DefaultMetadata returns a default metadata for commands without custom metadata.
func DefaultMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "command",
		Priority: 50,
	}
}

// MetadataProvider is an optional interface for commands to provide metadata.
type MetadataProvider interface {
	GetMetadata() CommandMetadata
}

// PaletteContext provides runtime context for determining command states.
type PaletteContext struct {
	IsGitRepo    bool
	HasAPIKey    bool
	Platform     string
	HasHistory   bool // Has undo history
	SessionCount int  // Number of saved sessions
}

// NewPaletteContext creates a new PaletteContext with detected values.
func NewPaletteContext(workDir string, hasAPIKey bool) PaletteContext {
	return PaletteContext{
		IsGitRepo:  isGitRepoCheck(workDir),
		HasAPIKey:  hasAPIKey,
		Platform:   runtime.GOOS,
		HasHistory: false, // Set by caller
	}
}

// isGitRepoCheck checks if the directory is a git repository.
func isGitRepoCheck(dir string) bool {
	_, err := runGitCommand(dir, "rev-parse", "--git-dir")
	return err == nil
}

// CommandState represents the enabled/disabled state of a command.
type CommandState struct {
	Enabled bool
	Reason  string // Why it's disabled (shown in UI)
}

// EnabledState returns an enabled state.
func EnabledState() CommandState {
	return CommandState{Enabled: true}
}

// DisabledState returns a disabled state with a reason.
func DisabledState(reason string) CommandState {
	return CommandState{Enabled: false, Reason: reason}
}

// PaletteCommand represents a command ready for display in the palette.
type PaletteCommand struct {
	Name        string
	Description string
	Usage       string
	Category    CategoryInfo
	Icon        string
	ArgHint     string
	State       CommandState
	Priority    int
	Advanced    bool
}

// PaletteProvider generates palette commands from the handler.
type PaletteProvider interface {
	GetPaletteCommands(ctx PaletteContext) []PaletteCommand
	GetCommandState(name string, ctx PaletteContext) CommandState
}
