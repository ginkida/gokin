package ui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Palette action IDs. These are dispatched on the LIVE model in
// dispatchPaletteAction (handleCommandPaletteKeys), NOT via the registration-
// time Action closure — closures capture a detached Model copy in production
// (Bubble Tea value-receiver semantics), so anything mutating a Model field
// (state, panel-visible flags) must route through an ID instead.
const (
	paletteActionModelSelector = "model_selector"
	paletteActionSettings      = "settings"
	paletteActionShortcuts     = "shortcuts"
	paletteActionTodos         = "todos"
	paletteActionActivityFeed  = "activity_feed"
	paletteActionLiveDetail    = "live_detail"
	paletteActionAgentTree     = "agent_tree"
	paletteActionObservatory   = "observatory"
	paletteActionPlanPanel     = "plan_panel"
	paletteActionPlanningMode  = "planning_mode"
	paletteActionClearScreen   = "clear_screen"
	paletteActionCompactMode   = "compact_mode"
	paletteActionNotifications = "notifications"
)

// SetCallbacks sets the callback functions.
func (m *Model) SetCallbacks(onSubmit func(string), onQuit func()) {
	m.onSubmit = onSubmit
	m.onQuit = onQuit
	if m.commandPalette != nil {
		m.commandPalette.SetSubmissionLinked(onSubmit != nil)
	}
}

// SetPermissionCallback sets the permission decision callback. reqID
// identifies which in-flight permission request the decision answers.
func (m *Model) SetPermissionCallback(onPermission func(reqID string, decision PermissionDecision)) {
	m.onPermission = onPermission
}

// SetQuestionCallback sets the question answer callback.
func (m *Model) SetQuestionCallback(onQuestion func(string)) {
	m.onQuestion = onQuestion
}

// SetPlanApprovalCallback sets the plan approval callback.
func (m *Model) SetPlanApprovalCallback(onPlanApproval func(PlanApprovalDecision)) {
	m.onPlanApproval = onPlanApproval
}

// SetPlanApprovalWithFeedbackCallback sets the plan approval callback with feedback support.
func (m *Model) SetPlanApprovalWithFeedbackCallback(onPlanApproval func(PlanApprovalDecision, string)) {
	m.onPlanApprovalWithFeedback = onPlanApproval
}

// SetInterruptCallback sets the interrupt callback (called when user presses ESC).
func (m *Model) SetInterruptCallback(onInterrupt func()) {
	m.onInterrupt = onInterrupt
}

// SetCancelCallback sets the cancel callback for ESC interrupt.
// This is called when the user presses ESC to cancel the current processing.
func (m *Model) SetCancelCallback(onCancel func()) {
	m.onCancel = onCancel
}

// SetPermissionsToggleCallback sets the callback for permissions toggle.
func (m *Model) SetPermissionsToggleCallback(onToggle func() bool) {
	m.onPermissionsToggle = onToggle
}

// SetPermissionsEnabled sets the permissions enabled state for display.
func (m *Model) SetPermissionsEnabled(enabled bool) {
	m.permissionsEnabled = enabled
}

// GetPermissionsEnabled returns the current permissions enabled state.
func (m *Model) GetPermissionsEnabled() bool {
	return m.permissionsEnabled
}

// SetPlanningModeToggleCallback sets the callback for planning mode toggle.
// The callback is async and doesn't return a value - the result is sent via PlanningModeToggledMsg.
func (m *Model) SetPlanningModeToggleCallback(onToggle func()) {
	m.onPlanningModeToggle = onToggle
}

// SetSessionModeCycleCallback wires the Shift+Tab session-mode cycle.
// When set, Shift+Tab calls this instead of the plan-only toggle —
// enabling the full Normal → Plan → YOLO → Normal cycle users expect
// from Claude Code. Callback is async; result arrives via
// SessionModeCycledMsg so the TUI updates status + emits a toast in
// one round trip.
func (m *Model) SetSessionModeCycleCallback(onCycle func()) {
	m.onSessionModeCycle = onCycle
}

// SetPlanningModeEnabled sets the planning mode enabled state for display.
func (m *Model) SetPlanningModeEnabled(enabled bool) {
	m.planningModeEnabled = enabled
}

// SetReducedMotion replaces animated activity indicators with stable glyphs
// and makes output auto-follow jump directly. Housekeeping ticks continue so
// expiry, resize, health refresh, and stale-progress cleanup still work.
func (m *Model) SetReducedMotion(enabled bool) {
	m.reducedMotion = enabled
	m.output.SetReducedMotion(enabled)
	m.progressModel.SetReducedMotion(enabled)
	if m.toolProgressBar != nil {
		m.toolProgressBar.SetReducedMotion(enabled)
	}
	if m.planProgressPanel != nil {
		m.planProgressPanel.SetReducedMotion(enabled)
	}
	if m.activityFeed != nil {
		m.activityFeed.SetReducedMotion(enabled)
	}
	if m.agentTreePanel != nil {
		m.agentTreePanel.SetReducedMotion(enabled)
	}
}

// GetPlanningModeEnabled returns the current planning mode enabled state.
func (m *Model) GetPlanningModeEnabled() bool {
	return m.planningModeEnabled
}

// SetSandboxToggleCallback sets the callback for sandbox toggle.
func (m *Model) SetSandboxToggleCallback(onToggle func() bool, getState func() bool) {
	m.onSandboxToggle = onToggle
	m.getSandboxState = getState
}

// SetSandboxEnabled sets the sandbox enabled state for display.
func (m *Model) SetSandboxEnabled(enabled bool) {
	m.sandboxEnabled = enabled
}

// GetSandboxEnabled returns the current sandbox enabled state.
func (m *Model) GetSandboxEnabled() bool {
	return m.sandboxEnabled
}

// SetDiffDecisionCallback sets the callback for diff preview decisions.
func (m *Model) SetDiffDecisionCallback(onDiffDecision func(DiffDecision)) {
	m.onDiffDecision = onDiffDecision
}

// SetMultiDiffDecisionCallback sets the callback for multi-file diff preview decisions.
func (m *Model) SetMultiDiffDecisionCallback(onMultiDiffDecision func(map[string]DiffDecision)) {
	m.onMultiDiffDecision = onMultiDiffDecision
}

// SetSearchActionCallback sets the callback for search result actions.
func (m *Model) SetSearchActionCallback(onSearchAction func(SearchAction)) {
	m.onSearchAction = onSearchAction
	m.searchResults.SetActionsLinked(m.onSearchAction != nil || m.onSearchResultAction != nil)
}

// SetSearchResultActionCallback sets a payload-aware callback for search result
// actions. When set, it takes precedence over the legacy action-only callback.
func (m *Model) SetSearchResultActionCallback(onSearchResultAction func(SearchAction, string, int)) {
	m.onSearchResultAction = onSearchResultAction
	m.searchResults.SetActionsLinked(m.onSearchAction != nil || m.onSearchResultAction != nil)
}

// SetGitActionCallback sets the callback for git status actions.
func (m *Model) SetGitActionCallback(onGitAction func(GitAction)) {
	m.onGitAction = onGitAction
	m.gitStatusModel.SetActionsLinked(m.onGitAction != nil || m.onGitStatusAction != nil)
}

// SetGitStatusActionCallback sets a payload-aware callback for git status
// actions. When set, it takes precedence over the legacy action-only callback.
// Diff handlers should asynchronously send a GitStatusDiffMsg back to the
// Bubble Tea program; the status overlay remains open while it is loading.
func (m *Model) SetGitStatusActionCallback(onGitStatusAction func(GitAction, []string, string)) {
	m.onGitStatusAction = onGitStatusAction
	m.gitStatusModel.SetActionsLinked(m.onGitAction != nil || m.onGitStatusAction != nil)
}

func (m *Model) dispatchSearchResultAction(msg SearchResultsActionMsg) bool {
	if m.onSearchResultAction != nil {
		m.onSearchResultAction(msg.Action, msg.FilePath, msg.LineNumber)
		return true
	}
	if m.onSearchAction != nil {
		m.onSearchAction(msg.Action)
		return true
	}
	return false
}

func (m *Model) dispatchGitStatusAction(msg GitStatusActionMsg) bool {
	if m.onGitStatusAction != nil {
		m.onGitStatusAction(msg.Action, append([]string(nil), msg.Files...), msg.Message)
		return true
	}
	if m.onGitAction != nil {
		m.onGitAction(msg.Action)
		return true
	}
	return false
}

// SetFileSelectCallback sets an optional observer for file browser selections.
// The UI always inserts selected paths into the composer itself; this callback
// lets an embedding application react in addition to that core behavior.
func (m *Model) SetFileSelectCallback(onFileSelect func(string)) {
	m.onFileSelect = onFileSelect
}

// SetApplyCodeBlockCallback is retained as a no-op for backwards compatibility.
// The feature was never wired to a key binding and has no callers in the UI.
func (m *Model) SetApplyCodeBlockCallback(onApply func(filename, content string)) {
	_ = onApply
}

// SetWorkDir sets the working directory for display.
func (m *Model) SetWorkDir(dir string) {
	m.workDir = dir
}

// SetCommandAliases forwards the alias table to the input autocomplete
// ranker so typing /p resolves to plan as a top suggestion.
func (m *Model) SetCommandAliases(aliases map[string]string) {
	m.input.SetCommandAliases(aliases)
	if m.commandPalette != nil {
		m.commandPalette.SetCommandAliases(aliases)
	}
}

// AddCommands appends commands to the input autocomplete list — used for
// file-based custom commands loaded at boot (deduped by name inside
// AddCommand, so a re-wire can't double entries).
func (m *Model) AddCommands(infos []CommandInfo) {
	for _, info := range infos {
		m.input.AddCommand(info)
	}
}

// RecordRecentCommand marks the given command as most-recently-used in the
// input model. Called after successful command dispatch.
func (m *Model) RecordRecentCommand(name string) {
	m.input.RecordRecentCommand(name)
}

// SetShowTokens enables or disables token usage display.
func (m *Model) SetShowTokens(show bool) {
	m.showTokens = show
}

// SetProjectInfo sets the project information for display.
func (m *Model) SetProjectInfo(projectType, projectName string) {
	m.projectType = projectType
	m.projectName = projectName
}

// SetAvailableModels sets the list of available models for the selector.
func (m *Model) SetAvailableModels(models []ModelInfo) {
	m.availableModels = append([]ModelInfo(nil), models...)
	for i := range m.availableModels {
		m.availableModels[i].ID = safeKeyEntryText(m.availableModels[i].ID)
		m.availableModels[i].Name = safeKeyEntryText(m.availableModels[i].Name)
		m.availableModels[i].Description = safeKeyEntryText(m.availableModels[i].Description)
	}
}

// SetCurrentModel sets the current model name.
func (m *Model) SetCurrentModel(modelID string) {
	m.currentModel = safeKeyEntryText(modelID)
}

// SetModelSelectCallback sets the callback for model selection.
func (m *Model) SetModelSelectCallback(callback func(modelID string)) {
	m.onModelSelect = callback
}

// SetGitBranch sets the current git branch for display.
func (m *Model) SetGitBranch(branch string) {
	m.gitBranch = branch
}

// SetVersion sets the application version for display.
func (m *Model) SetVersion(version string) {
	m.version = version
}

// SetPaletteProvider sets the palette provider for command fetching.
func (m *Model) SetPaletteProvider(provider PaletteProvider) {
	if m.commandPalette != nil {
		m.commandPalette.SetPaletteProvider(provider)
	}
}

// RegisterPaletteActions registers keyboard shortcut actions in the command palette.
func (m *Model) RegisterPaletteActions() {
	if m.commandPalette == nil {
		return
	}

	actions := []EnhancedPaletteCommand{
		{
			Name:        "Open Settings",
			Description: "Toggle permissions, sandbox, thinking, diff and more",
			Shortcut:    "Ctrl+S",
			Category:    PaletteCategoryInfo{Name: "Auth & Setup", Icon: "lock", Priority: 2},
			Enabled:     true,
			Priority:    100,
			Type:        CommandTypeAction,
			ActionID:    paletteActionSettings,
		},
		{
			Name:        "Open Model Selector",
			Description: "Switch the active AI model",
			Shortcut:    "Ctrl+K",
			Category:    PaletteCategoryInfo{Name: "Session", Icon: "chat", Priority: 1},
			Enabled:     true,
			Priority:    149,
			Type:        CommandTypeAction,
			ActionID:    paletteActionModelSelector,
		},
		{
			Name:        "Keyboard Shortcuts",
			Description: "Show the keyboard shortcut cheat-sheet",
			Shortcut:    "?",
			Category:    PaletteCategoryInfo{Name: "Getting Started", Icon: "rocket", Priority: 0},
			Enabled:     true,
			Priority:    90,
			Type:        CommandTypeAction,
			ActionID:    paletteActionShortcuts,
		},
		{
			Name:        "Notification History",
			Description: "Review active, expired, and hidden notifications",
			Shortcut:    "Alt+N",
			Category:    PaletteCategoryInfo{Name: "Session", Icon: "chat", Priority: 1},
			Enabled:     true,
			Priority:    148,
			Type:        CommandTypeAction,
			ActionID:    paletteActionNotifications,
		},
		{
			Name:        "Toggle Task List",
			Description: "Show or hide the task list panel",
			Shortcut:    "Ctrl+T",
			Category:    PaletteCategoryInfo{Name: "Session", Icon: "chat", Priority: 1},
			Enabled:     true,
			Priority:    150,
			Type:        CommandTypeAction,
			ActionID:    paletteActionTodos,
		},
		{
			Name:        "Live Activity Detail",
			Description: "Expand or minimize the live activity view (works while streaming)",
			Shortcut:    "Ctrl+O",
			Category:    PaletteCategoryInfo{Name: "Session", Icon: "chat", Priority: 1},
			Enabled:     true,
			Priority:    150,
			Type:        CommandTypeAction,
			ActionID:    paletteActionLiveDetail,
		},
		{
			Name:        "Toggle Activity Feed",
			Description: "Show or hide the sub-agent feed panel (within detailed view)",
			Category:    PaletteCategoryInfo{Name: "Session", Icon: "chat", Priority: 1},
			Enabled:     true,
			Priority:    151,
			Type:        CommandTypeAction,
			ActionID:    paletteActionActivityFeed,
		},
		{
			Name:        "Toggle Agent Tree",
			Description: "Show or hide the sub-agent tree panel",
			Shortcut:    "Ctrl+A",
			Category:    PaletteCategoryInfo{Name: "Session", Icon: "chat", Priority: 1},
			Enabled:     true,
			Priority:    153,
			Type:        CommandTypeAction,
			ActionID:    paletteActionAgentTree,
		},
		{
			Name:        "Context Observatory",
			Description: "Open the context/usage dashboard",
			Shortcut:    "Ctrl+H",
			Category:    PaletteCategoryInfo{Name: "Tools", Icon: "gear", Priority: 5},
			Enabled:     true,
			Priority:    540,
			Type:        CommandTypeAction,
			ActionID:    paletteActionObservatory,
		},
		{
			Name:        "Toggle Plan Panel",
			Description: "Expand or collapse the plan progress panel",
			Shortcut:    "Ctrl+X",
			Category:    PaletteCategoryInfo{Name: "Planning", Icon: "tree", Priority: 4},
			Enabled:     true,
			Priority:    401,
			Type:        CommandTypeAction,
			ActionID:    paletteActionPlanPanel,
		},
		{
			Name:        "Cycle Session Mode",
			Description: "Cycle Normal, Plan, and YOLO session modes",
			Shortcut:    "Shift+Tab",
			Category:    PaletteCategoryInfo{Name: "Planning", Icon: "tree", Priority: 4},
			Enabled:     true,
			Priority:    400,
			Type:        CommandTypeAction,
			ActionID:    paletteActionPlanningMode,
		},
		{
			Name:        "Clear Screen",
			Description: "Clear the output display",
			Shortcut:    "Ctrl+L",
			Category:    PaletteCategoryInfo{Name: "Session", Icon: "chat", Priority: 1},
			Enabled:     true,
			Priority:    152,
			Type:        CommandTypeAction,
			ActionID:    paletteActionClearScreen,
		},
		{
			Name:        "Toggle Compact Mode",
			Description: "Switch between compact and normal display (available from the palette)",
			Category:    PaletteCategoryInfo{Name: "Tools", Icon: "gear", Priority: 5},
			Enabled:     true,
			Priority:    551,
			Type:        CommandTypeAction,
			ActionID:    paletteActionCompactMode,
		},
	}

	m.commandPalette.SetActionCommands(actions)
}

// SetOpenSettingsCallback wires the handler that opens the interactive settings
// modal (the app builds a fresh toggle snapshot + sends OpenSettingsMsg). Used
// by the Ctrl+S binding and the palette's "Open Settings" action.
func (m *Model) SetOpenSettingsCallback(cb func()) {
	m.onOpenSettings = cb
}

// RefreshPaletteCommands refreshes the command palette commands.
func (m *Model) RefreshPaletteCommands() {
	if m.commandPalette != nil {
		m.commandPalette.RefreshCommands()
	}
}

// SetState sets the UI state.
func (m *Model) SetState(state State) {
	m.state = state
}

// GetProgram returns a new Bubbletea program.
func (m *Model) GetProgram() *tea.Program {
	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithMouseCellMotion()}
	return tea.NewProgram(m, opts...)
}
