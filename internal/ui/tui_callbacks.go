package ui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// SetCallbacks sets the callback functions.
func (m *Model) SetCallbacks(onSubmit func(string), onQuit func()) {
	m.onSubmit = onSubmit
	m.onQuit = onQuit
}

// SetPermissionCallback sets the permission decision callback.
func (m *Model) SetPermissionCallback(onPermission func(PermissionDecision)) {
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

// SetPlanningModeEnabled sets the planning mode enabled state for display.
func (m *Model) SetPlanningModeEnabled(enabled bool) {
	m.planningModeEnabled = enabled
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

// SetSearchActionCallback sets the callback for search result actions.
func (m *Model) SetSearchActionCallback(onSearchAction func(SearchAction)) {
	m.onSearchAction = onSearchAction
}

// SetGitActionCallback sets the callback for git status actions.
func (m *Model) SetGitActionCallback(onGitAction func(GitAction)) {
	m.onGitAction = onGitAction
}

// SetFileSelectCallback sets the callback for file browser selections.
func (m *Model) SetFileSelectCallback(onFileSelect func(string)) {
	m.onFileSelect = onFileSelect
}

// SetApplyCodeBlockCallback sets the callback for applying code blocks.
func (m *Model) SetApplyCodeBlockCallback(onApply func(filename, content string)) {
	m.onApplyCodeBlock = onApply
}

// SetWorkDir sets the working directory for display.
func (m *Model) SetWorkDir(dir string) {
	m.workDir = dir
}

// SetShowTokens enables or disables token usage display.
func (m *Model) SetShowTokens(show bool) {
	m.showTokens = show
}

// SetMouseEnabled is a no-op kept for API compatibility.
// Mouse is always captured; text selection uses native terminal shortcuts.
func (m *Model) SetMouseEnabled(enabled bool) {
}

// SetProjectInfo sets the project information for display.
func (m *Model) SetProjectInfo(projectType, projectName string) {
	m.projectType = projectType
	m.projectName = projectName
}

// SetAvailableModels sets the list of available models for the selector.
func (m *Model) SetAvailableModels(models []ModelInfo) {
	m.availableModels = models
}

// SetCurrentModel sets the current model name.
func (m *Model) SetCurrentModel(modelID string) {
	m.currentModel = modelID
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
			Name:        "Toggle Task List",
			Description: "Show or hide the task list panel",
			Shortcut:    "Ctrl+T",
			Category:    PaletteCategoryInfo{Name: "Session", Icon: "chat", Priority: 1},
			Enabled:     true,
			Priority:    150,
			Type:        CommandTypeAction,
			Action: func() {
				m.todosVisible = !m.todosVisible
			},
		},
		{
			Name:        "Toggle Activity Feed",
			Description: "Show or hide the activity feed panel",
			Shortcut:    "Ctrl+O",
			Category:    PaletteCategoryInfo{Name: "Session", Icon: "chat", Priority: 1},
			Enabled:     true,
			Priority:    151,
			Type:        CommandTypeAction,
			Action: func() {
				if m.activityFeed != nil {
					m.activityFeed.Toggle()
				}
			},
		},
		{
			Name:        "Toggle Planning Mode",
			Description: "Enable or disable planning mode",
			Shortcut:    "Shift+Tab",
			Category:    PaletteCategoryInfo{Name: "Planning", Icon: "tree", Priority: 4},
			Enabled:     true,
			Priority:    400,
			Type:        CommandTypeAction,
			Action: func() {
				if m.onPlanningModeToggle != nil {
					m.onPlanningModeToggle()
				}
			},
		},
		{
			Name:        "Clear Screen",
			Description: "Clear the output display",
			Shortcut:    "Ctrl+L",
			Category:    PaletteCategoryInfo{Name: "Session", Icon: "chat", Priority: 1},
			Enabled:     true,
			Priority:    152,
			Type:        CommandTypeAction,
			Action: func() {
				m.output.Clear()
			},
		},
		{
			Name:        "Toggle Compact Mode",
			Description: "Switch between compact and normal display",
			Shortcut:    "Ctrl+Shift+C",
			Category:    PaletteCategoryInfo{Name: "Tools", Icon: "gear", Priority: 5},
			Enabled:     true,
			Priority:    551,
			Type:        CommandTypeAction,
			Action: func() {
				m.CompactMode = !m.CompactMode
			},
		},
	}

	m.commandPalette.SetActionCommands(actions)
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
