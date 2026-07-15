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

// SetQuestionCallbackWithID correlates an answer with the exact asynchronous
// question waiter. Prefer it for integrations that can have overlapping or
// expiring prompts; SetQuestionCallback remains for legacy embedders.
func (m *Model) SetQuestionCallbackWithID(onQuestion func(reqID, answer string)) {
	m.onQuestionWithID = onQuestion
}

// SetPlanApprovalCallback sets the plan approval callback.
func (m *Model) SetPlanApprovalCallback(onPlanApproval func(PlanApprovalDecision)) {
	m.onPlanApproval = onPlanApproval
}

// SetPlanApprovalCallbackWithID correlates a decision with its plan request.
func (m *Model) SetPlanApprovalCallbackWithID(onPlanApproval func(reqID string, decision PlanApprovalDecision)) {
	m.onPlanApprovalWithID = onPlanApproval
}

// SetPlanApprovalWithFeedbackCallback sets the plan approval callback with feedback support.
func (m *Model) SetPlanApprovalWithFeedbackCallback(onPlanApproval func(PlanApprovalDecision, string)) {
	m.onPlanApprovalWithFeedback = onPlanApproval
}

// SetPlanApprovalWithFeedbackCallbackWithID correlates feedback with its plan
// request so a late editor submission cannot affect a newer approval.
func (m *Model) SetPlanApprovalWithFeedbackCallbackWithID(onPlanApproval func(reqID string, decision PlanApprovalDecision, feedback string)) {
	m.onPlanFeedbackWithID = onPlanApproval
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
	if m.toastManager != nil {
		m.toastManager.SetReducedMotion(enabled)
	}
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

// SetDiffDecisionCallbackWithID correlates a decision with the exact diff
// waiter. Late decisions for expired previews are ignored by the embedding app.
func (m *Model) SetDiffDecisionCallbackWithID(onDiffDecision func(string, DiffDecision)) {
	m.onDiffDecisionWithID = onDiffDecision
}

// SetMultiDiffDecisionCallback sets the callback for multi-file diff preview decisions.
func (m *Model) SetMultiDiffDecisionCallback(onMultiDiffDecision func(map[string]DiffDecision)) {
	m.onMultiDiffDecision = onMultiDiffDecision
}

// SetMultiDiffDecisionCallbackWithID correlates a batch review result with its
// exact asynchronous waiter.
func (m *Model) SetMultiDiffDecisionCallbackWithID(onMultiDiffDecision func(string, map[string]DiffDecision)) {
	m.onMultiDiffDecisionWithID = onMultiDiffDecision
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
	m.gitStatusModel.SetActionsLinked(m.onGitAction != nil || m.onGitStatusAction != nil || m.onGitStatusActionWithID != nil)
}

// SetGitStatusActionCallback sets a payload-aware callback for git status
// actions. When set, it takes precedence over the legacy action-only callback.
// Diff handlers should asynchronously send a GitStatusDiffMsg back to the
// Bubble Tea program; the status overlay remains open while it is loading.
func (m *Model) SetGitStatusActionCallback(onGitStatusAction func(GitAction, []string, string)) {
	m.onGitStatusAction = onGitStatusAction
	m.gitStatusModel.SetActionsLinked(m.onGitAction != nil || m.onGitStatusAction != nil || m.onGitStatusActionWithID != nil)
}

// SetGitStatusActionCallbackWithID preserves the operation ID used by inline
// diff loads. Embedders should echo it in GitStatusDiffMsg so a late response
// for the same path cannot replace a newer load.
func (m *Model) SetGitStatusActionCallbackWithID(onGitStatusAction func(GitAction, []string, string, string)) {
	m.onGitStatusActionWithID = onGitStatusAction
	m.gitStatusModel.SetActionsLinked(m.onGitAction != nil || m.onGitStatusAction != nil || m.onGitStatusActionWithID != nil)
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
	if m.onGitStatusActionWithID != nil {
		m.onGitStatusActionWithID(msg.Action, append([]string(nil), msg.Files...), msg.Message, msg.RequestID)
		return true
	}
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
	m.input.SetWorkDir(dir)
}

// ShowFirstLaunchWelcome arms the adaptive onboarding surface. The callback
// runs once, on the first key handled after real terminal geometry arrives;
// callers can persist the one-time flag without marking an unseen frame read.
func (m *Model) ShowFirstLaunchWelcome(onSeen func()) {
	m.firstLaunchWelcomePending = true
	m.onFirstLaunchWelcomeSeen = onSeen
	m.refreshTerminalTitle()
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

// SetModelSelectCallbackWithID correlates a selection with the exact async
// rebuild attempt. The same model can be retried, so model ID alone is not an
// operation identity.
func (m *Model) SetModelSelectCallbackWithID(callback func(requestID, modelID string)) {
	m.onModelSelectWithID = callback
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

	compactDescription := "Currently normal — switch to a compact transcript layout (palette-only)"
	if m.CompactMode {
		compactDescription = "Currently compact — restore the normal transcript layout (palette-only)"
	}
	settingsAvailable := m.onOpenSettings != nil
	settingsDescription := "Toggle permissions, sandbox, thinking, diff and more"
	settingsReason := ""
	if !settingsAvailable {
		settingsDescription = "Settings are unavailable in this session"
		settingsReason = "settings provider not connected"
	}
	todosDescription := "Currently hidden — show the task list panel"
	if m.todosVisible {
		todosDescription = "Currently shown — hide the task list panel"
	}
	liveDetailDescription := "Currently minimal — show detailed live activity"
	if m.liveDetailExpanded {
		liveDetailDescription = "Currently detailed — return to minimal live activity"
	}
	activityAvailable := m.activityFeed != nil
	activityDescription := "Currently hidden — show the activity feed and detailed view"
	activityReason := ""
	if !activityAvailable {
		activityDescription = "Activity feed is unavailable in this session"
		activityReason = "activity feed not connected"
	} else if m.activityFeed.IsVisible() {
		activityDescription = "Currently shown — hide the activity feed"
	}
	agentTreeAvailable := m.agentTreePanel != nil
	agentTreeDescription := "Currently hidden — show the sub-agent tree"
	agentTreeReason := ""
	if !agentTreeAvailable {
		agentTreeDescription = "Sub-agent tree is unavailable in this session"
		agentTreeReason = "agent tree not connected"
	} else if m.agentTreePanel.IsVisible() {
		agentTreeDescription = "Currently shown — hide the sub-agent tree"
	}
	observatoryAvailable := m.observatoryPanel != nil
	observatoryDescription := "Open the context and usage dashboard"
	observatoryReason := ""
	if !observatoryAvailable {
		observatoryDescription = "Context observatory is unavailable in this session"
		observatoryReason = "context observatory not connected"
	}
	planPanelVisible := m.planProgressPanel != nil && m.planProgressPanel.IsVisible()
	planPanelAvailable := planPanelVisible && planPanelDensityActionReadable(m.width, m.height)
	planPanelDescription := "Available while a plan is executing"
	planPanelReason := "no active plan panel"
	if planPanelVisible && !planPanelAvailable {
		planPanelDescription = "Resize the terminal to change plan detail"
		planPanelReason = "terminal too small for expanded plan progress"
	} else if planPanelAvailable {
		planPanelReason = ""
		if m.planProgressPanel.IsCollapsed() {
			planPanelDescription = "Currently compact — expand active plan progress"
		} else {
			planPanelDescription = "Currently expanded — compact active plan progress"
		}
	}
	sessionModeAvailable := m.onSessionModeCycle != nil || m.onPlanningModeToggle != nil
	sessionModeDescription := "Cycle Normal, Plan, and YOLO session modes"
	sessionModeReason := ""
	if !sessionModeAvailable {
		sessionModeDescription = "Session mode switching is unavailable"
		sessionModeReason = "session mode controller not connected"
	}
	actions := []EnhancedPaletteCommand{
		{
			Name:        "Open Settings",
			Description: settingsDescription,
			Shortcut:    "Ctrl+S",
			Category:    PaletteCategoryInfo{Name: "Auth & Setup", Icon: "lock", Priority: 2},
			Enabled:     settingsAvailable,
			Reason:      settingsReason,
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
			Description: todosDescription,
			Shortcut:    "Ctrl+T",
			Category:    PaletteCategoryInfo{Name: "Session", Icon: "chat", Priority: 1},
			Enabled:     true,
			Priority:    150,
			Type:        CommandTypeAction,
			ActionID:    paletteActionTodos,
		},
		{
			Name:        "Live Activity Detail",
			Description: liveDetailDescription + " (works while streaming)",
			Shortcut:    "Ctrl+O",
			Category:    PaletteCategoryInfo{Name: "Session", Icon: "chat", Priority: 1},
			Enabled:     true,
			Priority:    150,
			Type:        CommandTypeAction,
			ActionID:    paletteActionLiveDetail,
		},
		{
			Name:        "Toggle Activity Feed",
			Description: activityDescription,
			Category:    PaletteCategoryInfo{Name: "Session", Icon: "chat", Priority: 1},
			Enabled:     activityAvailable,
			Reason:      activityReason,
			Priority:    151,
			Type:        CommandTypeAction,
			ActionID:    paletteActionActivityFeed,
		},
		{
			Name:        "Toggle Agent Tree",
			Description: agentTreeDescription,
			Shortcut:    "Ctrl+A",
			Category:    PaletteCategoryInfo{Name: "Session", Icon: "chat", Priority: 1},
			Enabled:     agentTreeAvailable,
			Reason:      agentTreeReason,
			Priority:    153,
			Type:        CommandTypeAction,
			ActionID:    paletteActionAgentTree,
		},
		{
			Name:        "Context Observatory",
			Description: observatoryDescription,
			Shortcut:    "Ctrl+H",
			Category:    PaletteCategoryInfo{Name: "Tools", Icon: "gear", Priority: 5},
			Enabled:     observatoryAvailable,
			Reason:      observatoryReason,
			Priority:    540,
			Type:        CommandTypeAction,
			ActionID:    paletteActionObservatory,
		},
		{
			Name:        "Toggle Plan Panel",
			Description: planPanelDescription,
			Shortcut:    "Ctrl+X",
			Category:    PaletteCategoryInfo{Name: "Planning", Icon: "tree", Priority: 4},
			Enabled:     planPanelAvailable,
			Reason:      planPanelReason,
			Priority:    401,
			Type:        CommandTypeAction,
			ActionID:    paletteActionPlanPanel,
		},
		{
			Name:        "Cycle Session Mode",
			Description: sessionModeDescription,
			Shortcut:    "Shift+Tab",
			Category:    PaletteCategoryInfo{Name: "Planning", Icon: "tree", Priority: 4},
			Enabled:     sessionModeAvailable,
			Reason:      sessionModeReason,
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
			Description: compactDescription,
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
