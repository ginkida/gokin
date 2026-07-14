package ui

import (
	"fmt"
	"maps"
	"os"
	"reflect"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"gokin/internal/format"
	"gokin/internal/highlight"
)

const (
	// slowOperationThreshold is the time after which we show a "slow operation" warning
	slowOperationThreshold = 3 * time.Second
)

// Model represents the main TUI model.
type Model struct {
	input   InputModel
	output  OutputModel
	spinner spinner.Model
	styles  *Styles

	state            State
	width            int
	height           int
	currentTool      string
	currentToolInfo  string // Brief info about current tool operation (e.g., file path)
	toolStartTime    time.Time
	processingLabel  string   // Status label between tool calls: "Thinking", "Analyzing", "Running agent"
	currentActivity  string   // Live "what the agent is doing now" from the in-progress todo (active form); overrides the generic Thinking/Generating label, cleared at turn end
	loopIteration    int      // Current executor loop iteration (0 = first/unknown)
	loopToolsUsed    int      // Total tools used across loop iterations
	agentToolCount   int      // Tools used by current sub-agent (for progress display)
	agentRecentTools []string // Last N tool names for inline activity display
	todoItems        []string
	workDir          string

	// Streaming timeout protection
	streamStartTime time.Time
	streamTimeout   time.Duration

	// Slow operation warning
	lastActivityTime time.Time // Last time we received any activity (tool call, stream, etc.)
	slowWarningShown bool      // Whether we've shown the slow warning for current operation

	// Stream idle feedback (server slow to respond)
	streamIdleMsg string // Non-empty when stream is idle — shown in status line

	// Rate limiting / debounce for message submission
	lastSubmitTime time.Time
	minSubmitDelay time.Duration // Minimum delay between submissions (default: 500ms)

	// Type-ahead: number of messages queued behind the in-flight request
	// (updated via QueuedCountMsg; shown as a status-bar badge).
	queuedPending int

	// Response header tracking
	responseHeaderShown bool // True if assistant header was shown for current response

	// Token usage tracking
	tokenUsage     *TokenUsageMsg
	baseTokenCount int // Last authoritative total from TokenUsageMsg, before streaming estimate
	showTokens     bool

	// Project info
	projectType string
	projectName string

	// Git info
	gitBranch string

	// Version info
	version string

	// Plan progress tracking
	planProgress     *PlanProgressMsg
	planProgressMode bool // True when plan is actively executing

	// Session timing. Session COST deliberately has no Model field: chrome
	// (titlebar/status bar) stopped displaying it — per-response cost stays
	// in the metadata footer and /cost owns the cumulative view.
	sessionStart time.Time

	// Permission prompt state
	permRequest        *PermissionRequestMsg
	permSelectedOption int
	permShowDetails    bool
	permDetailScroll   int
	permNotice         string

	// Question prompt state
	questionRequest        *QuestionRequestMsg
	questionSelectedOption int
	questionCustomInput    bool
	questionInputModel     InputModel
	questionInputError     string

	// Plan approval state
	planRequest        *PlanApprovalRequestMsg
	planSelectedOption int
	planStepScroll     int
	planFeedbackMode   bool       // True when entering feedback for "Request Changes"
	planFeedbackInput  InputModel // Input model for feedback
	planFeedbackError  string
	planApprovalNotice string

	// Model selector state
	modelSelectedIndex       int
	availableModels          []ModelInfo
	currentModel             string
	onModelSelect            func(modelID string)
	modelSwitchPending       string
	modelSelectorReturnState State // Settings when opened as a nested picker; Input otherwise.
	modelSelectorNotice      string

	// Settings modal state (the /settings interactive screen)
	settingsItems            []SettingItem
	settingsCursor           int
	settingsModel            string
	settingsProvider         string
	settingsNotice           string
	settingsPending          map[string]bool // key -> optimistic value; survives modal close/reopen
	notificationSelected     int
	notificationSelectedID   int
	notificationScroll       int
	notificationDetail       bool
	notificationDetailScroll int
	notificationNotice       string
	onSettingToggle          func(key string, on bool)
	onOpenSettings           func() // Opens the /settings modal (Ctrl+S + palette action)

	// Masked API-key entry modal (/login <provider> with no key)
	keyEntryInput              textinput.Model
	keyEntryProvider           string
	keyEntryDisplayName        string
	keyEntrySetupURL           string
	keyEntryError              string
	keyEntryPendingProvider    string
	keyEntryPendingDisplayName string
	keyEntryPendingSetupURL    string
	onKeyEntrySubmit           func(provider, key string)

	// Diff preview state
	diffPreview         DiffPreviewModel
	diffRequest         *DiffPreviewRequestMsg
	multiDiffPreview    MultiDiffPreviewModel
	multiDiffRequest    *MultiDiffPreviewRequestMsg
	onDiffDecision      func(decision DiffDecision)
	onMultiDiffDecision func(decisions map[string]DiffDecision)

	// Search results state
	searchResults        SearchResultsModel
	searchRequest        *SearchResultsRequestMsg
	onSearchAction       func(action SearchAction)
	onSearchResultAction func(action SearchAction, filePath string, lineNumber int)

	// Git status state
	gitStatusModel    GitStatusModel
	gitStatusRequest  *GitStatusRequestMsg
	onGitAction       func(action GitAction)
	onGitStatusAction func(action GitAction, files []string, message string)

	// Scratchpad
	scratchpad string

	// File browser state
	fileBrowser                 FileBrowserModel
	fileBrowserActive           bool
	workspaceOverlayReturnState State
	onFileSelect                func(path string)

	// Progress state
	progressModel       ProgressModel
	progressActive      bool
	progressReturnState State

	// Tool progress bar (for long-running tool operations)
	toolProgressBar *ToolProgressBarModel

	// Tool output state (for expand/collapse)
	toolOutput          *ToolOutputModel
	lastToolOutputIndex int // Tool output index

	// pendingEditDiff carries an edit result's display-diff from the
	// ToolResultMsg handler into handleToolResultWithStatus (same Update
	// dispatch — stashed so the handler's signature, called directly by many
	// tests, stays unchanged). Consumed and cleared on use.
	pendingEditDiff *editDiffDisplay

	// pendingToolLines buffers consecutive title-only read/bash completions
	// for aggregated emission ("Read 2 files, ran 3 shell commands") —
	// flushed before anything else lands in scrollback. See tool_aggregation.go.
	pendingToolLines []aggToolEntry

	// Callbacks
	onSubmit                   func(message string)
	onQuit                     func()
	onPermission               func(reqID string, decision PermissionDecision)
	onQuestion                 func(answer string)
	onPlanApproval             func(decision PlanApprovalDecision)
	onPlanApprovalWithFeedback func(decision PlanApprovalDecision, feedback string) // Extended callback with feedback
	onInterrupt                func()                                               // Called after the UI handles Esc/Ctrl+C interruption
	onCancel                   func()                                               // Cancels current backend processing
	onPermissionsToggle        func() bool                                          // Called to toggle permissions

	// Todos visibility state
	todosVisible bool

	// Permissions enabled state
	permissionsEnabled bool

	// Planning mode state
	planningModeEnabled  bool
	onPlanningModeToggle func() // Called to toggle planning mode (async, no return)

	// Session-mode cycle (Normal → Plan → YOLO → Normal) bound to
	// Shift+Tab. When set, OVERRIDES onPlanningModeToggle — the cycle
	// handles plan mode too. Result arrives via SessionModeCycledMsg so
	// the TUI can emit a toast + refresh the status bar. Left nil in
	// tests that don't wire the App.
	onSessionModeCycle func()

	// sessionMode mirrors App.SessionMode so the status bar can render
	// distinct states without querying App on every frame. Updated when
	// SessionModeCycledMsg arrives. "normal" / "plan" / "yolo" — empty
	// string means "unknown, fall back to planningModeEnabled flag".
	sessionMode string

	// Sandbox state
	sandboxEnabled  bool
	onSandboxToggle func() bool
	getSandboxState func() bool

	// === PHASE 4: App reference for data providers ===
	app any // Reference to App instance (use any to avoid import cycle)

	// Hints system (welcome removed)
	hintsEnabled  bool // Enable contextual hints
	reducedMotion bool // Static activity indicators + instant auto-follow
	hintSystem    *HintSystem
	hintsShown    map[string]int // Track how many times each hint was shown

	// Coordinated task tracking (Phase 2)
	coordinatedTasks      map[string]*CoordinatedTaskState // taskID -> state
	coordinatedTaskOrder  []string                         // Ordered list of task IDs
	activeCoordinatedTask string                           // Currently active task ID

	// Command palette (Ctrl+P)
	commandPalette   *CommandPalette
	shortcutsOverlay *ShortcutsOverlay

	// Toast notifications
	toastManager *ToastManager

	// Plan progress panel (detailed plan execution view)
	planProgressPanel *PlanProgressPanel

	// Context technical observatory dashboard (Ctrl+O)
	observatoryPanel *ContextObservatoryPanel

	// Background task tracking
	backgroundTasks map[string]*BackgroundTaskState

	// Activity feed panel
	activityFeed    *ActivityFeedPanel
	agentTreePanel  *AgentTreePanel
	filePeek        *FilePeekPanel
	activeToolCalls []activeToolCall // Stack of active parallel tool calls

	// liveDetailExpanded is the Claude-Code-style live-activity verbosity
	// switch (Ctrl+O, works DURING Processing/Streaming too): false (default)
	// renders the live card as ONE dim unobtrusive line and suppresses the
	// activity feed panel; true renders the full multi-line card (todo
	// position, plan/parallel-work lines) and lets the feed panel show.
	// Session-scoped — persists across turns, resets on app restart.
	liveDetailExpanded bool

	// Compact mode
	CompactMode bool

	// Status line contextual information
	conversationMode   string // exploring/implementing/debugging
	mcpHealthy         int
	mcpTotal           int
	runtimeStatus      RuntimeStatusSnapshot
	lastRuntimeRefresh time.Time

	// Plan pause/resume UX block
	planPauseNotice *PlanProgressMsg

	// Syntax highlighting for tool output
	highlighter *highlight.Highlighter

	// Response timing breakdown
	responseToolDuration time.Duration // Total time spent in tool calls during current response
	responseToolCount    int           // Number of tool calls in current response
	responseToolFailures int           // Failed tool calls during current response

	// Request latency tracking
	lastRequestLatency time.Duration
	retryAttempt       int
	retryMax           int
	rateLimitWaitUntil time.Time

	// Error dedup
	lastErrorMsg          string // Last terminal error message for dedup
	lastErrorCount        int    // Consecutive terminal-error repeat count
	lastRecoverableStatus string // Last durable recoverable status in this recovery episode

	// Copy support: track last AI response
	lastResponseText   string           // Last AI response text (saved on ResponseDoneMsg)
	currentResponseBuf *strings.Builder // Accumulates current streaming response (pointer to survive Bubble Tea copies)

	// Terminal bell on prompts
	bellEnabled bool

	// Quit confirmation: requires two consecutive Ctrl+C presses. Any other
	// interaction clears the confirmation so a later Ctrl+C cannot quit with a
	// newly composed draft.
	quitConfirmTime time.Time // When first Ctrl+C was pressed

	// Resize debounce: buffer rapid WindowSizeMsg to prevent flickering
	pendingResize  *tea.WindowSizeMsg // nil if no pending resize
	resizeDeadline time.Time          // when to apply the pending resize
}

// BackgroundTaskState tracks the state of a background task for UI display.
type BackgroundTaskState struct {
	ID            string
	Type          string // "agent" or "shell"
	Description   string
	Status        string // "running", "completed", "failed", "cancelled"
	StartTime     time.Time
	Progress      float64 // 0.0-1.0
	CurrentStep   int
	TotalSteps    int
	CurrentAction string
	ToolsUsed     []string
}

// activeToolCall tracks a single in-flight tool call for parallel execution.
type activeToolCall struct {
	activityID string // ID in activity feed
	name       string // tool name
	args       map[string]any
	info       string    // tool info (e.g., file path)
	startTime  time.Time // when the tool call started
}

// CoordinatedTaskState tracks the state of a coordinated task for UI display.
type CoordinatedTaskState struct {
	ID                  string
	Message             string
	PlanType            string
	Status              string // pending, running, completed, failed
	Progress            float64
	StartTime           time.Time
	EndTime             time.Time // Set when task completes/fails; used for auto-cleanup
	Duration            time.Duration
	Error               error
	LastProgressBucket  int
	LastProgressMessage string
}

// NewModel creates a new TUI model.
func NewModel() *Model {
	// gokin ships a single Graphite + violet theme — DefaultStyles already
	// carries it via the bootstrap Color* vars in styles.go. No per-OS or
	// background-detection branching here on purpose.
	styles := DefaultStyles()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = styles.Spinner

	m := &Model{
		input:                NewInputModel(styles, ""), // Initialized with empty workDir, set later in SetWorkDir
		output:               NewOutputModel(styles),
		spinner:              s,
		styles:               styles,
		state:                StateInput,
		streamTimeout:        15 * time.Minute,       // Timeout for stuck streaming states (generous for long operations)
		minSubmitDelay:       500 * time.Millisecond, // Debounce: 500ms between submissions
		sessionStart:         time.Now(),
		diffPreview:          NewDiffPreviewModel(styles),
		multiDiffPreview:     NewMultiDiffPreviewModel(styles),
		searchResults:        NewSearchResultsModel(styles),
		gitStatusModel:       NewGitStatusModel(styles),
		fileBrowser:          NewFileBrowserModel(styles),
		progressModel:        NewProgressModel(styles),
		toolOutput:           NewToolOutputModel(styles),
		lastToolOutputIndex:  -1,
		todosVisible:         false, // Default to hidden
		permissionsEnabled:   true,  // Default to enabled
		sandboxEnabled:       true,  // Default to enabled
		hintsEnabled:         true,  // Enable contextual hints
		hintSystem:           NewHintSystem(styles),
		hintsShown:           make(map[string]int),
		coordinatedTasks:     make(map[string]*CoordinatedTaskState),
		coordinatedTaskOrder: make([]string, 0),
		commandPalette:       NewCommandPalette(styles),
		shortcutsOverlay:     NewShortcutsOverlay(styles),
		toastManager:         NewToastManager(styles),
		planProgressPanel:    NewPlanProgressPanel(styles),
		observatoryPanel:     NewContextObservatoryPanel(styles),
		backgroundTasks:      make(map[string]*BackgroundTaskState),
		toolProgressBar:      NewToolProgressBarModel(styles),
		activityFeed:         NewActivityFeedPanel(styles),
		agentTreePanel:       NewAgentTreePanel(styles),
		filePeek:             NewFilePeekPanel(styles),
		currentResponseBuf:   &strings.Builder{},
		bellEnabled:          true, // Terminal bell enabled by default
		highlighter:          highlight.New("monokai"),
	}
	// Workspace panels emit action messages, but this parent owns dispatching
	// them. Start explicitly unlinked; callback setters promote the panels from
	// viewer mode when an embedding handler is installed.
	m.searchResults.SetActionsLinked(false)
	m.gitStatusModel.SetActionsLinked(false)
	m.commandPalette.SetSubmissionLinked(false)
	return m
}

// Init initializes the TUI.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.input.Init(),
		m.spinner.Tick,
	}

	return tea.Batch(cmds...)
}

// ScratchpadMsg is sent when the agent scratchpad is updated.
type ScratchpadMsg string

// Update handles TUI events.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// Handle critical messages FIRST - always process these regardless of welcome screen
	switch msg := msg.(type) {
	case ScratchpadMsg:
		m.scratchpad = string(msg)
		return m, nil
	case tea.WindowSizeMsg:
		// Debounce resize: buffer rapid events, apply after 50ms of quiet
		m.pendingResize = &msg
		m.resizeDeadline = time.Now().Add(50 * time.Millisecond)

	case spinner.TickMsg:
		// Always process spinner ticks for animation
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)

		// Flush pending resize (debounced)
		if m.pendingResize != nil && time.Now().After(m.resizeDeadline) {
			resizeCmd := m.applyResize(m.pendingResize)
			m.pendingResize = nil
			if resizeCmd != nil {
				cmds = append(cmds, resizeCmd)
			}
		}

		// Flush pending viewport updates (debounced content)
		m.output.FlushPendingUpdate()

		// Advance smooth scroll animation (ease-out toward target)
		m.output.TickSmoothScroll()

		// Update toast manager (clean up expired toasts)
		if m.toastManager != nil {
			m.toastManager.Update()
		}

		// Update plan progress panel animation
		if m.planProgressPanel != nil {
			m.planProgressPanel.Tick()
		}

		// Update tool progress bar animation
		if m.toolProgressBar != nil {
			m.toolProgressBar.Tick()
		}

		// Update activity feed animation
		if m.activityFeed != nil {
			m.activityFeed.Tick()
		}
		if m.agentTreePanel != nil {
			m.agentTreePanel.Tick()
		}
		if m.filePeek != nil {
			m.filePeek.Tick()
		}

		// Refresh runtime health snapshot for status bar (async to avoid blocking on App locks).
		if m.lastRuntimeRefresh.IsZero() || time.Since(m.lastRuntimeRefresh) >= time.Second {
			m.lastRuntimeRefresh = time.Now() // Prevent re-scheduling until response arrives
			cmds = append(cmds, m.runtimeStatusCmd())
		}

		// Check for streaming timeout
		if (m.state == StateProcessing || m.state == StateStreaming) &&
			!m.streamStartTime.IsZero() &&
			time.Since(m.streamStartTime) > m.streamTimeout {
			m.state = StateInput
			m.streamStartTime = time.Time{}
			m.lastActivityTime = time.Time{}
			m.slowWarningShown = false
			m.currentTool = ""
			m.currentToolInfo = ""
			m.settleActiveToolCalls(false, "timed out")
			m.responseHeaderShown = false
			m.output.FlushStream() // Flush any remaining streamed content
			m.output.AppendLine("")
			m.output.AppendLine(m.styles.FormatError(fmt.Sprintf("request timed out after %v", m.streamTimeout)))
			m.output.AppendLine("")
			// Cancel the BACKEND request too — onCancel is the wired callback
			// (app.CancelProcessing), the same one the Esc path uses. This
			// used to call only onInterrupt, which has no production wiring
			// (SetInterruptCallback has zero callers), so the watchdog reset
			// the UI to input while the request kept running behind it.
			if m.onCancel != nil {
				m.onCancel()
			}
			if m.onInterrupt != nil {
				m.onInterrupt()
			}
			cmds = append(cmds, m.input.Focus())
		}

		// Check for slow operation warning (after 3 seconds of no activity)
		// Just set flag - hint shown inline in status, no toast to avoid jumping
		if (m.state == StateProcessing || m.state == StateStreaming) &&
			!m.slowWarningShown &&
			!m.lastActivityTime.IsZero() &&
			time.Since(m.lastActivityTime) > slowOperationThreshold {
			m.slowWarningShown = true
		}
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		cmd := m.handleKeyMsg(msg)
		if cmd != nil {
			return m, cmd
		}

	case tea.WindowSizeMsg:
		// Already handled above before welcome screen check
		// Just skip to avoid duplicate processing

	case spinner.TickMsg:
		// Already handled above before welcome screen check
		// Just skip to avoid duplicate processing

	case tea.MouseMsg:
		// Forward mouse events to output viewport for scrolling
		var cmd tea.Cmd
		m.output, cmd = m.output.Update(msg)
		cmds = append(cmds, cmd)
		// A wheel scroll pauses stream auto-follow while the user reads up (so the
		// next chunk doesn't snap them back), and resumes it once they're back at
		// the bottom — lets them scroll freely WHILE the agent is writing.
		if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
			m.output.SetFrozen(!m.output.IsAtBottom())
		}

	default:
		// Handle message types
		cmd := m.handleMessageTypes(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	// Update input in StateInput AND during processing/streaming (type-ahead:
	// the user can compose the next message while the agent works; Enter
	// queues it). Modals still take all keys. Safe key-conflict-wise: the only
	// bindings active during processing are Esc/Ctrl+C/Ctrl+B/F/G — no bare
	// printable keys (the 'e'/'?'-style bindings are StateInput-only).
	if m.typeAheadActive() {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// handleKeyMsg handles keyboard input.
func (m *Model) handleKeyMsg(msg tea.KeyMsg) tea.Cmd {
	// Handle permission prompt keys first
	if m.state == StatePermissionPrompt {
		return m.handlePermissionPromptKeys(msg)
	}

	// Handle question prompt keys
	if m.state == StateQuestionPrompt && m.questionRequest != nil {
		return m.handleQuestionPromptKeys(msg)
	}

	// Handle plan approval keys
	if m.state == StatePlanApproval && m.planRequest != nil {
		return m.handlePlanApprovalKeys(msg)
	}

	// Handle model selector keys
	if m.state == StateModelSelector {
		return m.handleModelSelectorKeys(msg)
	}

	// Handle settings modal keys
	if m.state == StateSettings {
		return m.handleSettingsKeys(msg)
	}

	// Handle masked API-key entry keys
	if m.state == StateAPIKeyEntry {
		return m.handleKeyEntryKeys(msg)
	}

	// Handle shortcuts overlay keys
	if m.state == StateShortcutsOverlay {
		return m.handleShortcutsOverlayKeys(msg)
	}

	if m.state == StateNotificationCenter {
		return m.handleNotificationCenterKeys(msg)
	}

	// Handle command palette keys
	if m.state == StateCommandPalette {
		return m.handleCommandPaletteKeys(msg)
	}

	// Handle diff preview keys
	if m.state == StateDiffPreview {
		var cmd tea.Cmd
		m.diffPreview, cmd = m.diffPreview.Update(msg)
		return cmd
	}

	// Handle multi diff preview keys
	if m.state == StateMultiDiffPreview {
		var cmd tea.Cmd
		m.multiDiffPreview, cmd = m.multiDiffPreview.Update(msg)
		return cmd
	}

	// Handle search results keys
	if m.state == StateSearchResults {
		var cmd tea.Cmd
		m.searchResults, cmd = m.searchResults.Update(msg)
		return cmd
	}

	// Handle git status keys
	if m.state == StateGitStatus {
		var cmd tea.Cmd
		m.gitStatusModel, cmd = m.gitStatusModel.Update(msg)
		return cmd
	}

	// Handle file browser keys
	if m.state == StateFileBrowser {
		var cmd tea.Cmd
		m.fileBrowser, cmd = m.fileBrowser.Update(msg)
		return cmd
	}

	// Handle batch progress keys
	if m.state == StateBatchProgress {
		var cmd tea.Cmd
		m.progressModel, cmd = m.progressModel.Update(msg)
		return cmd
	}

	// Handle global keys
	return m.handleGlobalKeys(msg)
}

// handlePermissionPromptKeys handles keys in permission prompt state.
// decidePermission applies a permission decision (from a number key, y/a/n, or
// the highlighted option) — one shared path so the numbered list, the arrow+Enter
// flow, and the quick keys can't drift. Deny returns to input + interrupts; allow
// variants resume processing.
func (m *Model) decidePermission(decision PermissionDecision) tea.Cmd {
	if m.onPermission == nil {
		if decision == PermissionDeny || decision == PermissionDenySession {
			return m.cancelUnavailablePermission()
		}
		m.permNotice = "Unavailable: cannot send permission response · Esc cancels the request"
		return nil
	}
	var reqID string
	if m.permRequest != nil {
		reqID = m.permRequest.ID
	}
	m.permRequest = nil
	m.permSelectedOption = 0
	m.permShowDetails = false
	m.permDetailScroll = 0
	m.permNotice = ""
	if decision == PermissionDeny || decision == PermissionDenySession {
		m.state = StateInput
		m.output.AppendLine(m.styles.Warning.Render(" Denied - operation cancelled"))
		m.output.AppendLine("")
		if m.onInterrupt != nil {
			m.onInterrupt()
		}
		m.onPermission(reqID, decision)
		return m.input.Focus()
	}
	m.state = StateProcessing
	m.onPermission(reqID, decision)
	return nil
}

func (m *Model) cancelUnavailablePermission() tea.Cmd {
	m.permRequest = nil
	m.permSelectedOption = 0
	m.permShowDetails = false
	m.permDetailScroll = 0
	m.permNotice = ""
	m.state = StateInput
	m.output.AppendLine(m.styles.Warning.Render(" Permission request cancelled · response handler unavailable"))
	m.output.AppendLine("")
	if m.onCancel != nil {
		m.onCancel()
	}
	if m.onInterrupt != nil {
		m.onInterrupt()
	}
	return m.input.Focus()
}

func (m *Model) handlePermissionPromptKeys(msg tea.KeyMsg) tea.Cmd {
	if m.permRequest == nil {
		m.state = StateInput
		return m.input.Focus()
	}
	m.permSelectedOption = permissionSelectedIndex(m.permSelectedOption)

	if m.permShowDetails {
		_, paletteWidth := m.permissionDetailBounds()
		lines := permissionDetailLines(m.permRequest, paletteWidth)
		page := permissionDetailVisibleCount(m.height, len(lines))
		maxScroll := max(0, len(lines)-page)
		switch msg.String() {
		case "up", "k":
			m.permDetailScroll = max(0, m.permDetailScroll-1)
			return nil
		case "down", "j":
			m.permDetailScroll = min(maxScroll, m.permDetailScroll+1)
			return nil
		case "pgup":
			m.permDetailScroll = max(0, m.permDetailScroll-page)
			return nil
		case "pgdown":
			m.permDetailScroll = min(maxScroll, m.permDetailScroll+page)
			return nil
		case "home":
			m.permDetailScroll = 0
			return nil
		case "end":
			m.permDetailScroll = maxScroll
			return nil
		case "enter", " ":
			// The decision rows are hidden in detail mode. Never confirm an
			// invisible selection; number/y/a/n shortcuts remain explicit.
			return nil
		}
	}

	switch msg.String() {
	case "up", "k":
		if m.permSelectedOption > 0 {
			m.permSelectedOption--
		}
	case "down", "j":
		if m.permSelectedOption < 2 {
			m.permSelectedOption++
		}
	case "enter", " ":
		return m.decidePermission(PermissionDecision(m.permSelectedOption))
	case "1":
		return m.decidePermission(PermissionAllow)
	case "2":
		return m.decidePermission(PermissionAllowSession)
	case "3":
		return m.decidePermission(PermissionDeny)
	case "y":
		return m.decidePermission(PermissionAllow)
	case "n", "esc":
		return m.decidePermission(PermissionDeny)
	case "a":
		return m.decidePermission(PermissionAllowSession)
	case "?":
		m.permShowDetails = !m.permShowDetails
		m.permDetailScroll = 0
	}
	return nil
}

func (m *Model) permissionDetailBounds() (visible, width int) {
	paletteWidth, _ := promptPaletteWidth(m.width)
	width = max(1, paletteWidth-6)
	if m.permRequest == nil {
		return 0, width
	}
	lines := permissionDetailLines(m.permRequest, width)
	return permissionDetailVisibleCount(m.height, len(lines)), width
}

// handleQuestionPromptKeys handles keys in question prompt state.
func (m *Model) handleQuestionPromptKeys(msg tea.KeyMsg) tea.Cmd {
	// If custom input mode, delegate to input model
	if m.questionCustomInput {
		switch msg.Type {
		case tea.KeyEnter:
			if msg.Alt {
				m.questionInputError = ""
				m.questionInputModel.InsertNewline()
				return nil
			}
			// Submit custom answer
			answer := strings.TrimSpace(m.questionInputModel.Value())
			if answer == "" {
				m.questionInputError = "Answer cannot be empty"
				return m.questionInputModel.Focus()
			}
			return m.submitQuestion(answer)
		case tea.KeyEsc:
			// Cancel custom input, return to options
			m.questionCustomInput = false
			m.questionInputError = ""
			resetPromptInput(&m.questionInputModel)
			return nil
		default:
			m.questionInputError = ""
			var cmd tea.Cmd
			m.questionInputModel, cmd = m.questionInputModel.Update(msg)
			return cmd
		}
	}

	// Option selection mode - guard against nil request
	if m.questionRequest == nil {
		m.state = StateInput
		return m.input.Focus()
	}

	optCount := len(m.questionRequest.Options)
	m.questionSelectedOption = min(max(m.questionSelectedOption, 0), optCount)
	if optCount == 0 {
		// No options - just free text input
		switch msg.Type {
		case tea.KeyEnter:
			if msg.Alt {
				m.questionInputError = ""
				m.questionInputModel.InsertNewline()
				return nil
			}
			answer := strings.TrimSpace(m.questionInputModel.Value())
			if answer == "" {
				m.questionInputError = "Answer cannot be empty"
				return m.questionInputModel.Focus()
			}
			return m.submitQuestion(answer)
		case tea.KeyEsc:
			return m.cancelQuestion()
		default:
			m.questionInputError = ""
			var cmd tea.Cmd
			m.questionInputModel, cmd = m.questionInputModel.Update(msg)
			return cmd
		}
	}

	switch msg.String() {
	case "esc":
		return m.cancelQuestion()
	case "up", "k":
		if m.questionSelectedOption > 0 {
			m.questionSelectedOption--
		}
	case "down", "j":
		if m.questionSelectedOption < optCount { // +1 for "Other" option
			m.questionSelectedOption++
		}
	case "home", "g":
		m.questionSelectedOption = 0
	case "end", "G":
		m.questionSelectedOption = optCount
	case "pgup":
		m.questionSelectedOption = max(0, m.questionSelectedOption-questionOptionVisibleCount(m.height, optCount+1))
	case "pgdown":
		m.questionSelectedOption = min(optCount, m.questionSelectedOption+questionOptionVisibleCount(m.height, optCount+1))
	case "enter", " ":
		if m.questionSelectedOption < optCount {
			// Selected an option
			answer := m.questionRequest.Options[m.questionSelectedOption]
			return m.submitQuestion(answer)
		} else {
			// Selected "Other" - switch to custom input
			m.questionCustomInput = true
			m.questionInputError = ""
			m.questionInputModel = NewInputModel(m.styles, m.workDir)
			m.questionInputModel.SetWidth(promptInputContentWidth(m.width))
			return m.questionInputModel.Focus()
		}
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		// Quick select by number
		idx := int(msg.String()[0] - '1')
		if idx < optCount {
			answer := m.questionRequest.Options[idx]
			return m.submitQuestion(answer)
		}
	}
	return nil
}

func (m *Model) submitQuestion(answer string) tea.Cmd {
	if m.onQuestion == nil {
		recovery := "Esc cancels the request"
		if m.questionCustomInput {
			recovery = "Esc returns to answers"
		}
		m.questionInputError = "Unavailable: cannot submit answer · " + recovery
		if m.questionCustomInput || (m.questionRequest != nil && len(m.questionRequest.Options) == 0) {
			return m.questionInputModel.Focus()
		}
		return nil
	}
	m.questionRequest = nil
	m.questionSelectedOption = 0
	m.questionCustomInput = false
	m.questionInputError = ""
	resetPromptInput(&m.questionInputModel)
	m.state = StateProcessing
	m.onQuestion(answer)
	return nil
}

// resetPromptInput is safe for option-only prompts, whose text editor is
// intentionally created lazily. bubbles/textarea.Reset dereferences internal
// viewport state and panics when called on its zero value.
func resetPromptInput(input *InputModel) {
	if input == nil {
		return
	}
	if input.styles == nil {
		*input = InputModel{}
		return
	}
	input.Reset()
}

func (m *Model) cancelQuestion() tea.Cmd {
	callbackAvailable := m.onQuestion != nil
	m.questionRequest = nil
	m.questionSelectedOption = 0
	m.questionCustomInput = false
	m.questionInputError = ""
	resetPromptInput(&m.questionInputModel)
	m.state = StateInput
	m.output.AppendLine("")
	m.output.AppendLine(m.styles.Warning.Render(" Question cancelled"))
	m.output.AppendLine("")
	if callbackAvailable {
		m.onQuestion("")
	} else if m.onCancel != nil {
		m.onCancel()
	}
	return m.input.Focus()
}

// handlePlanApprovalKeys handles keys in plan approval state.
func (m *Model) handlePlanApprovalKeys(msg tea.KeyMsg) tea.Cmd {
	if m.planRequest == nil {
		m.state = StateInput
		return m.input.Focus()
	}
	m.planSelectedOption = min(max(m.planSelectedOption, 0), 2)
	emptyPlan := len(m.planRequest.Steps) == 0
	if emptyPlan && m.planSelectedOption == int(PlanApproved) {
		m.planSelectedOption = int(PlanRejected)
	}
	visibleSteps := planApprovalStepVisibleCount(m.height, len(m.planRequest.Steps))
	m.planStepScroll = min(max(m.planStepScroll, 0), max(len(m.planRequest.Steps)-visibleSteps, 0))

	// If in feedback mode, handle input
	if m.planFeedbackMode {
		switch msg.Type {
		case tea.KeyEnter:
			if msg.Alt {
				m.planFeedbackError = ""
				m.planFeedbackInput.InsertNewline()
				return nil
			}
			// Submit feedback
			feedback := strings.TrimSpace(m.planFeedbackInput.Value())
			if feedback == "" {
				m.planFeedbackError = "Describe what should change"
				return m.planFeedbackInput.Focus()
			}
			if m.onPlanApprovalWithFeedback == nil && m.onPlanApproval == nil {
				m.planFeedbackError = "Unavailable: cannot submit feedback · Esc returns to decisions"
				return m.planFeedbackInput.Focus()
			}
			m.planRequest = nil
			m.planSelectedOption = 0
			m.planFeedbackMode = false
			m.planFeedbackError = ""
			resetPromptInput(&m.planFeedbackInput)
			m.state = StateProcessing
			// Send decision with feedback
			if m.onPlanApprovalWithFeedback != nil {
				m.onPlanApprovalWithFeedback(PlanModifyRequested, feedback)
			} else if m.onPlanApproval != nil {
				m.onPlanApproval(PlanModifyRequested)
			}
			return nil
		case tea.KeyEsc:
			// Cancel feedback, return to options
			m.planFeedbackMode = false
			m.planFeedbackError = ""
			resetPromptInput(&m.planFeedbackInput)
			return nil
		default:
			m.planFeedbackError = ""
			var cmd tea.Cmd
			m.planFeedbackInput, cmd = m.planFeedbackInput.Update(msg)
			return cmd
		}
	}

	switch msg.String() {
	case "up", "k":
		minOption := int(PlanApproved)
		if emptyPlan {
			minOption = int(PlanRejected)
		}
		if m.planSelectedOption > minOption {
			m.planSelectedOption--
		}
		m.planApprovalNotice = ""
	case "down", "j":
		if m.planSelectedOption < 2 {
			m.planSelectedOption++
		}
		m.planApprovalNotice = ""
	case "home", "g":
		m.planStepScroll = 0
	case "end", "G":
		m.planStepScroll = max(len(m.planRequest.Steps)-planApprovalStepVisibleCount(m.height, len(m.planRequest.Steps)), 0)
	case "pgup", "[":
		m.planStepScroll = max(m.planStepScroll-planApprovalStepVisibleCount(m.height, len(m.planRequest.Steps)), 0)
	case "pgdown", "]":
		maxStart := max(len(m.planRequest.Steps)-planApprovalStepVisibleCount(m.height, len(m.planRequest.Steps)), 0)
		m.planStepScroll = min(m.planStepScroll+planApprovalStepVisibleCount(m.height, len(m.planRequest.Steps)), maxStart)
	case "enter", " ":
		decision := PlanApprovalDecision(m.planSelectedOption)
		if emptyPlan && decision == PlanApproved {
			m.planSelectedOption = int(PlanRejected)
			m.planApprovalNotice = "Cannot approve an empty plan · request changes or reject"
			return nil
		}
		if decision == PlanModifyRequested {
			if m.onPlanApprovalWithFeedback == nil && m.onPlanApproval == nil {
				m.planApprovalNotice = "Unavailable: cannot send plan response · Esc cancels the request"
				return nil
			}
			// Enter feedback mode
			m.planFeedbackMode = true
			m.planFeedbackError = ""
			m.planFeedbackInput = NewInputModel(m.styles, m.workDir)
			m.planFeedbackInput.SetWidth(promptInputContentWidth(m.width))
			m.planFeedbackInput.SetPlaceholder("Enter your feedback for plan modifications...")
			return m.planFeedbackInput.Focus()
		}
		if m.onPlanApproval == nil {
			m.planApprovalNotice = "Unavailable: cannot send plan response · Esc cancels the request"
			return nil
		}
		// Initialize the plan progress panel when the plan is approved via Enter
		// (the "y" quick-approve path already does this; keep them in sync).
		if decision == PlanApproved && m.planRequest != nil && m.planProgressPanel != nil {
			m.planProgressPanel.StartPlan(
				"",
				m.planRequest.Title,
				m.planRequest.Description,
				m.planRequest.Steps,
			)
		}
		m.planRequest = nil
		m.planSelectedOption = 0
		m.planFeedbackError = ""
		m.state = StateProcessing
		m.onPlanApproval(decision)
	case "y", "1":
		// Quick approve (1 = the numbered "Approve" option)
		if emptyPlan {
			m.planSelectedOption = int(PlanRejected)
			m.planApprovalNotice = "Cannot approve an empty plan · request changes or reject"
			return nil
		}
		if m.onPlanApproval == nil {
			m.planApprovalNotice = "Unavailable: cannot send plan response · Esc cancels the request"
			return nil
		}
		// Initialize plan progress panel with the approved plan
		if m.planRequest != nil && m.planProgressPanel != nil {
			m.planProgressPanel.StartPlan(
				"", // planID - will be filled by progress updates
				m.planRequest.Title,
				m.planRequest.Description,
				m.planRequest.Steps,
			)
			// No toast needed - user just pressed approve, they know
		}
		m.planRequest = nil
		m.planSelectedOption = 0
		m.planFeedbackMode = false
		m.planFeedbackError = ""
		resetPromptInput(&m.planFeedbackInput)
		m.state = StateProcessing
		m.onPlanApproval(PlanApproved)
	case "n", "2":
		if m.onPlanApproval == nil {
			m.planApprovalNotice = "Unavailable: cannot send plan response · Esc cancels the request"
			return nil
		}
		// Quick reject (2 = the numbered "Reject" option)
		m.planRequest = nil
		m.planSelectedOption = 0
		m.planFeedbackMode = false
		m.planFeedbackError = ""
		resetPromptInput(&m.planFeedbackInput)
		m.planApprovalNotice = ""
		m.state = StateProcessing
		m.onPlanApproval(PlanRejected)
	case "m", "3":
		// Quick modify (3 = the numbered "Request changes" option) — enter feedback mode
		if m.onPlanApprovalWithFeedback == nil && m.onPlanApproval == nil {
			m.planApprovalNotice = "Unavailable: cannot send plan response · Esc cancels the request"
			return nil
		}
		m.planFeedbackMode = true
		m.planFeedbackError = ""
		m.planApprovalNotice = ""
		m.planFeedbackInput = NewInputModel(m.styles, m.workDir)
		m.planFeedbackInput.SetWidth(promptInputContentWidth(m.width))
		m.planFeedbackInput.SetPlaceholder("Enter your feedback for plan modifications...")
		return m.planFeedbackInput.Focus()
	case "esc":
		// ESC to interrupt plan approval and return to input with context.
		// This must ACTUALLY unblock the backend, not just reset the UI: the
		// foreground turn is synchronously blocked inside promptPlanApproval,
		// waiting on a.planApprovalChan (which only handlePlanApproval below
		// ever sends to) or its own 10-minute PlanApprovalTimeout. The old
		// code called only the never-wired m.onInterrupt and told the user
		// "you can now provide feedback" — but a.processing stayed true, so
		// anything the user typed next was silently QUEUED as type-ahead and
		// did nothing for up to 10 minutes (the plan-approval sibling of the
		// /loop Esc bug: UI says "go ahead", backend is still waiting).
		// m.onCancel (app.CancelProcessing) cancels the SAME ctx
		// promptPlanApproval selects on — its ctx.Done() branch fires
		// immediately, rejecting the plan and freeing the turn for real.
		m.planRequest = nil
		m.planSelectedOption = 0
		m.planFeedbackMode = false
		m.planFeedbackError = ""
		m.planApprovalNotice = ""
		m.state = StateInput
		m.output.AppendLine("")
		m.output.AppendLine(m.styles.Warning.Render(" Plan approval interrupted"))
		m.output.AppendLine(lipgloss.NewStyle().Foreground(ColorInfo).Render(" You can now provide feedback or modification requests"))
		m.output.AppendLine("")
		if m.onCancel != nil {
			m.onCancel()
		}
		if m.onInterrupt != nil {
			m.onInterrupt()
		}
		return m.input.Focus()
	}
	return nil
}

// handleCommandPaletteKeys handles keys in command palette state.
func (m *Model) handleCommandPaletteKeys(msg tea.KeyMsg) tea.Cmd {
	// Inline argument entry takes over every key while active.
	if m.commandPalette.InArgEntry() {
		return m.handlePaletteArgKeys(msg)
	}

	switch msg.Type {
	case tea.KeyEscape:
		m.commandPalette.Hide()
		m.state = StateInput
		return m.input.Focus()

	case tea.KeyEnter:
		if _, ready := m.commandPalette.directSlashCommandWithArgs(strings.TrimSpace(m.commandPalette.GetQuery())); ready && m.onSubmit == nil {
			m.commandPalette.SetSubmitError("Unavailable: command submission is not connected")
			return nil
		}
		if full, ok := m.commandPalette.DirectSlashLineWithArgs(); ok {
			m.state = StateProcessing
			m.streamStartTime = time.Now()
			m.responseToolDuration = 0
			m.responseToolCount = 0
			m.responseToolFailures = 0
			m.output.AppendLine(m.styles.FormatUserMessage(full))
			m.output.AppendLine("")
			if m.onSubmit != nil {
				m.onSubmit(full)
			}
			return nil
		}

		// Empty results and disabled commands are non-actions. Keep the palette
		// open so Enter cannot strand its visible/state flags or discard a query.
		selected := m.commandPalette.GetSelected()
		if selected == nil || !selected.Enabled {
			return nil
		}
		if selected.Type == CommandTypeSlash && m.onSubmit == nil {
			m.commandPalette.SetSubmitError("Unavailable: command submission is not connected")
			return nil
		}

		// A slash command that needs a required argument drops into the inline
		// arg-entry step instead of running bare (which would only print Usage).
		if PaletteNeedsArg(*selected) {
			m.commandPalette.BeginArgEntry(*selected)
			return nil
		}

		cmd := m.commandPalette.Execute()
		if cmd == nil {
			m.state = StateInput
			return m.input.Focus()
		}

		// Execute based on command type
		switch cmd.Type {
		case CommandTypeSlash:
			if strings.HasPrefix(cmd.Shortcut, "/") {
				// Slash command - submit to the app
				m.state = StateProcessing
				m.streamStartTime = time.Now()
				m.responseToolDuration = 0 // Reset tool timing for new response
				m.responseToolCount = 0
				m.responseToolFailures = 0
				m.output.AppendLine(m.styles.FormatUserMessage(cmd.Shortcut))
				m.output.AppendLine("")
				if m.onSubmit != nil {
					m.onSubmit(cmd.Shortcut)
				}
				return nil
			}
		case CommandTypeAction:
			// ID-based actions are dispatched on the LIVE model here (closures
			// captured at registration mutate a detached copy). Legacy actions
			// with no ID already ran their closure inside Execute(); preserve any
			// state they set (e.g. a model selector opening) rather than forcing
			// the UI back to input.
			if cmd.ActionID != "" {
				return m.dispatchPaletteAction(cmd.ActionID)
			}
			if m.state == StateCommandPalette {
				m.state = StateInput
				return m.input.Focus()
			}
			return nil
		}

		m.state = StateInput
		return m.input.Focus()

	case tea.KeyUp:
		m.commandPalette.SelectPrev()
		return nil

	case tea.KeyDown:
		m.commandPalette.SelectNext()
		return nil

	case tea.KeyHome:
		m.commandPalette.SelectFirst()
		return nil

	case tea.KeyEnd:
		m.commandPalette.SelectLast()
		return nil

	case tea.KeyPgUp:
		m.commandPalette.PageUp()
		return nil

	case tea.KeyPgDown:
		m.commandPalette.PageDown()
		return nil

	case tea.KeyTab:
		// Toggle preview panel
		m.commandPalette.TogglePreview()
		return nil

	case tea.KeyBackspace:
		m.commandPalette.BackspaceQuery()
		return nil

	default:
		// Handle text input for filtering
		if msg.Type == tea.KeyRunes {
			m.commandPalette.AppendQuery(string(msg.Runes))
		}
		return nil
	}
}

// handlePaletteArgKeys drives the inline argument-entry step. Enter submits the
// assembled "/name args" line, Esc returns to the command list, and the rest
// types into the argument field.
func (m *Model) handlePaletteArgKeys(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEnter:
		if !m.commandPalette.ValidateArgEntry() {
			return nil
		}
		if m.onSubmit == nil {
			m.commandPalette.SetArgError("Unavailable: command submission is not connected")
			return nil
		}
		full := m.commandPalette.SubmitArgEntry()
		if strings.TrimSpace(full) == "" {
			m.state = StateInput
			return m.input.Focus()
		}
		m.state = StateProcessing
		m.streamStartTime = time.Now()
		m.responseToolDuration = 0
		m.responseToolCount = 0
		m.responseToolFailures = 0
		m.output.AppendLine(m.styles.FormatUserMessage(full))
		m.output.AppendLine("")
		if m.onSubmit != nil {
			m.onSubmit(full)
		}
		return nil
	case tea.KeyEscape:
		m.commandPalette.CancelArgEntry()
		return nil
	case tea.KeyBackspace:
		m.commandPalette.BackspaceArg()
		return nil
	case tea.KeySpace:
		m.commandPalette.AppendArg(" ")
		return nil
	case tea.KeyRunes:
		m.commandPalette.AppendArg(string(msg.Runes))
		return nil
	}
	return nil
}

// dispatchPaletteAction runs a palette action by id on the LIVE model. This is
// the correct place for any effect that mutates a Model field — running it via
// the registration-time closure would mutate a detached copy (Bubble Tea value
// receiver). If the action does not open its own modal/state, the palette
// returns to the input.
func (m *Model) dispatchPaletteAction(id string) tea.Cmd {
	switch id {
	case paletteActionModelSelector:
		m.openModelSelector()
	case paletteActionSettings:
		m.requestOpenSettings()
	case paletteActionShortcuts:
		if m.shortcutsOverlay != nil {
			m.shortcutsOverlay.Show()
			m.state = StateShortcutsOverlay
		}
	case paletteActionNotifications:
		m.openNotificationCenter()
	case paletteActionTodos:
		m.toggleTodosPanel()
	case paletteActionLiveDetail:
		m.liveDetailExpanded = !m.liveDetailExpanded
		// Mirror the Ctrl+O handler: expanding explicitly opens the feed panel
		// too, so the palette action and the key can't behave differently.
		if m.liveDetailExpanded && m.activityFeed != nil {
			m.activityFeed.ShowExplicit()
		}
		if m.toastManager != nil {
			if m.liveDetailExpanded {
				m.toastManager.ShowInfo("Live activity: detailed")
			} else {
				m.toastManager.ShowInfo("Live activity: minimal")
			}
		}
	case paletteActionActivityFeed:
		if m.activityFeed != nil {
			m.activityFeed.Toggle()
			// The feed only renders inside live-detail mode. If the palette
			// action explicitly shows the feed while the compact live view is
			// active, expand detail too so "Activity feed shown" is actually
			// visible on the next frame. Hiding the feed leaves detail mode as-is.
			if m.activityFeed.IsVisible() {
				m.liveDetailExpanded = true
			}
			if m.toastManager != nil {
				m.toastManager.ShowInfo(toggleStateLabel("Activity feed", m.activityFeed.IsVisible()))
			}
		}
	case paletteActionAgentTree:
		m.toggleAgentTreePanel()
	case paletteActionObservatory:
		if m.observatoryPanel != nil {
			m.observatoryPanel.Toggle()
			if m.observatoryPanel.IsVisible() {
				m.state = StateContextObservatory
			}
		}
	case paletteActionPlanPanel:
		if m.planProgressPanel != nil && m.planProgressPanel.IsVisible() {
			m.planProgressPanel.Toggle()
		} else if m.toastManager != nil {
			// Honest feedback instead of a total silent no-op: the plan panel
			// only exists while a plan executes — say so.
			m.toastManager.ShowInfo("No plan panel — appears while a plan is executing")
		}
	case paletteActionPlanningMode:
		m.requestSessionModeCycle()
	case paletteActionClearScreen:
		m.clearOutputWithFeedback()
	case paletteActionCompactMode:
		m.toggleCompactMode()
	default:
		if m.toastManager != nil {
			m.toastManager.ShowWarning("This palette action is unavailable")
		}
	}

	// If the action transitioned to its own modal/state, keep it; otherwise
	// fall back to the input so the palette doesn't strand the UI.
	if m.state == StateCommandPalette {
		m.state = StateInput
		return m.input.Focus()
	}
	return nil
}

// requestOpenSettings keeps the direct shortcut and palette action honest in
// stripped-down sessions where the app did not wire a settings provider.
func (m *Model) requestOpenSettings() {
	if m.onOpenSettings != nil {
		m.onOpenSettings()
		return
	}
	if m.toastManager != nil {
		m.toastManager.ShowWarning("Settings are unavailable in this session")
	}
}

// requestSessionModeCycle is the one behavior behind every advertised
// Shift+Tab entry point. Prefer the canonical Normal → Plan → YOLO cycle and
// retain the legacy binary planning callback for older integrations.
func (m *Model) requestSessionModeCycle() {
	switch {
	case m.onSessionModeCycle != nil:
		m.onSessionModeCycle()
	case m.onPlanningModeToggle != nil:
		m.onPlanningModeToggle()
	default:
		if m.toastManager != nil {
			m.toastManager.ShowWarning("Session mode switching is unavailable")
		}
	}
}

func (m *Model) clearOutputWithFeedback() {
	m.output.Clear()
	if m.toastManager != nil {
		m.toastManager.ShowInfo("Output cleared")
	}
}

func overlayReturnState(state State) State {
	if state == StateProcessing || state == StateStreaming {
		return state
	}
	return StateInput
}

func isBlockingDecisionState(state State) bool {
	switch state {
	case StatePermissionPrompt, StateQuestionPrompt, StatePlanApproval,
		StateDiffPreview, StateMultiDiffPreview:
		return true
	}
	return false
}

func activeSurfaceLabel(state State) string {
	switch state {
	case StatePermissionPrompt:
		return "Permission prompt"
	case StateQuestionPrompt:
		return "Question prompt"
	case StatePlanApproval:
		return "Plan approval"
	case StateModelSelector:
		return "Model selector"
	case StateShortcutsOverlay:
		return "Keyboard shortcuts"
	case StateCommandPalette:
		return "Command Palette"
	case StateDiffPreview, StateMultiDiffPreview:
		return "Diff review"
	case StateSearchResults:
		return "Search results"
	case StateGitStatus:
		return "Git status"
	case StateFileBrowser:
		return "File browser"
	case StateBatchProgress:
		return "Batch progress"
	case StateContextObservatory:
		return "Context dashboard"
	case StateSettings:
		return "Settings"
	case StateAPIKeyEntry:
		return "API key entry"
	case StateNotificationCenter:
		return "Notifications"
	default:
		return "Active screen"
	}
}

func (m *Model) showSurfaceCollision(action, retry string) {
	if m.toastManager == nil {
		return
	}
	label := activeSurfaceLabel(m.state)
	if retry == "" {
		if isBlockingDecisionState(m.state) {
			retry = "resolve it and retry"
		} else {
			retry = "close it and retry"
		}
	} else if isBlockingDecisionState(m.state) {
		retry = strings.Replace(retry, "close it", "resolve it", 1)
	}
	m.toastManager.ShowWarning(action + " · " + label + " kept open · " + retry)
}

func (m *Model) showPromptCollision(kind, outcome string, resolved bool) {
	if m.toastManager == nil {
		return
	}
	label := activeSurfaceLabel(m.state)
	recovery := label + " kept open"
	if isBlockingDecisionState(m.state) {
		recovery = "finish the active prompt first"
	}
	message := fmt.Sprintf("Incoming %s %s · %s", kind, outcome, recovery)
	if !resolved {
		message = fmt.Sprintf("Incoming %s could not be resolved · response handler unavailable · %s", kind, recovery)
	}
	m.toastManager.ShowTagged("prompt-collision", ToastWarning, message, 6*time.Second)
}

func isWorkspaceOverlayState(state State) bool {
	return state == StateSearchResults || state == StateGitStatus || state == StateFileBrowser
}

func (m *Model) restoreWorkspaceOverlay() tea.Cmd {
	m.state = overlayReturnState(m.workspaceOverlayReturnState)
	m.workspaceOverlayReturnState = StateInput
	return m.input.Focus()
}

// finishFileBrowser turns chosen files into immediately usable composer
// references. The browser is a picker, not a dead-end viewer: Enter and the
// multi-select confirmation both preserve any existing draft and return to
// the state that was underneath it. ResponseDone/Error downgrade that saved
// state to Input while the browser is open.
func (m *Model) finishFileBrowser(paths []string) tea.Cmd {
	m.fileBrowserActive = false
	added := m.input.InsertFileReferences(paths, m.workDir)
	if added > 0 && m.toastManager != nil {
		label := "file reference"
		if added != 1 {
			label = "file references"
		}
		m.toastManager.ShowSuccess(fmt.Sprintf("Added %d %s to draft", added, label))
	} else if len(paths) > 0 && m.toastManager != nil {
		m.toastManager.ShowWarning("Selected path could not be added to the draft")
	}
	if m.onFileSelect != nil {
		seen := make(map[string]bool, len(paths))
		for _, path := range paths {
			if path != "" && !seen[path] && strings.IndexFunc(path, unicode.IsControl) < 0 {
				seen[path] = true
				m.onFileSelect(path)
			}
		}
	}
	return m.restoreWorkspaceOverlay()
}

// toggleCompactMode flips compact mode and resizes the output viewport. It is
// intentionally palette-only: terminals encode Ctrl+Shift+C exactly like the
// cancel chord Ctrl+C, so advertising that keyboard binding would be unsafe.
func (m *Model) toggleCompactMode() {
	m.CompactMode = !m.CompactMode
	if m.CompactMode {
		m.output.SetSize(m.width, max(m.height/3, 3))
	} else {
		m.output.SetSize(m.width, max(m.height-5, 3))
	}
	if m.toastManager != nil {
		m.toastManager.ShowInfo(toggleStateLabel("Compact mode", m.CompactMode))
	}
}

func (m *Model) handleShortcutsOverlayKeys(msg tea.KeyMsg) tea.Cmd {
	if m.shortcutsOverlay == nil {
		// The renderer already supplies a fallback overlay for degraded
		// construction paths. Persist the same fallback before handling input so
		// its advertised filtering/navigation actions work instead of the first
		// key unexpectedly closing the screen.
		m.shortcutsOverlay = NewShortcutsOverlay(m.styles)
		m.shortcutsOverlay.Show()
	}

	switch msg.Type {
	case tea.KeyEscape:
		if m.shortcutsOverlay.GetSearch() != "" {
			m.shortcutsOverlay.ClearSearch()
			return nil
		}
		m.shortcutsOverlay.Hide()
		m.state = StateInput
		return m.input.Focus()
	case tea.KeyUp:
		m.shortcutsOverlay.ScrollUp()
		return nil
	case tea.KeyDown:
		m.shortcutsOverlay.ScrollDown()
		return nil
	case tea.KeyPgUp:
		m.shortcutsOverlay.PageUp()
		return nil
	case tea.KeyPgDown:
		m.shortcutsOverlay.PageDown()
		return nil
	case tea.KeyHome:
		m.shortcutsOverlay.ScrollToStart()
		return nil
	case tea.KeyEnd:
		m.shortcutsOverlay.ScrollToEnd()
		return nil
	case tea.KeyBackspace:
		query := m.shortcutsOverlay.GetSearch()
		if query != "" {
			_, size := utf8.DecodeLastRuneInString(query)
			m.shortcutsOverlay.SetSearch(query[:len(query)-size])
		}
		return nil
	default:
		// FILTER-FIRST: every typed rune belongs to the search query. The old
		// single-letter interceptors (k/j scroll, q quit) ran BEFORE the
		// filter append, so the advertised "Type to filter" could not contain
		// those letters at all — typing "quit" dismissed the overlay on its
		// first keystroke. Navigation is ↑/↓ (what the footer advertises) and
		// close is Esc; there is deliberately no letter-key nav here.
		if msg.Type == tea.KeyRunes {
			m.shortcutsOverlay.SetSearch(m.shortcutsOverlay.GetSearch() + string(msg.Runes))
		}
		return nil
	}
}

// handleModelSelectorKeys handles keys in model selector state.
func (m *Model) handleModelSelectorKeys(msg tea.KeyMsg) tea.Cmd {
	if len(m.availableModels) > 0 {
		m.modelSelectedIndex = min(max(m.modelSelectedIndex, 0), len(m.availableModels)-1)
	} else {
		m.modelSelectedIndex = 0
	}
	switch msg.String() {
	case "up", "k":
		if m.modelSelectedIndex > 0 {
			m.modelSelectedIndex--
			m.modelSelectorNotice = ""
		}
	case "down", "j":
		if m.modelSelectedIndex < len(m.availableModels)-1 {
			m.modelSelectedIndex++
			m.modelSelectorNotice = ""
		}
	case "home":
		if len(m.availableModels) > 0 {
			m.modelSelectedIndex = 0
		}
		m.modelSelectorNotice = ""
	case "end":
		if len(m.availableModels) > 0 {
			m.modelSelectedIndex = len(m.availableModels) - 1
		}
		m.modelSelectorNotice = ""
	case "pgup":
		m.modelSelectedIndex = max(0, m.modelSelectedIndex-modelSelectorVisibleCount(m.height, len(m.availableModels)))
		m.modelSelectorNotice = ""
	case "pgdown":
		if len(m.availableModels) > 0 {
			m.modelSelectedIndex = min(len(m.availableModels)-1, m.modelSelectedIndex+modelSelectorVisibleCount(m.height, len(m.availableModels)))
		}
		m.modelSelectorNotice = ""
	case "enter", " ":
		return m.selectModelAt(m.modelSelectedIndex)
	case "esc", "q":
		return m.closeModelSelector()
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		// Quick select by number
		idx := int(msg.String()[0] - '1')
		return m.selectModelAt(idx)
	}
	return nil
}

func (m *Model) selectModelAt(index int) tea.Cmd {
	if index < 0 || index >= len(m.availableModels) {
		return nil
	}

	selected := m.availableModels[index]
	selected.ID = safeKeyEntryText(selected.ID)
	selected.Name = safeKeyEntryText(selected.Name)
	if selected.ID == "" {
		m.modelSelectorNotice = "This model has no selectable ID"
		return nil
	}
	if m.modelSwitchPending != "" {
		m.modelSelectorNotice = "Wait for the current model switch to finish"
		return nil
	}
	if selected.ID != m.currentModel {
		if m.onModelSelect == nil {
			m.modelSelectorNotice = "Model switching is unavailable in this session"
			return nil
		}
		name := selected.Name
		if name == "" {
			name = selected.ID
		}
		m.modelSwitchPending = selected.ID
		if m.toastManager != nil {
			m.toastManager.ShowTagged("model-switch", ToastInfo, fmt.Sprintf("Switching to %s…", name), 15*time.Second)
		}
		if m.modelSelectorReturnState == StateSettings {
			m.settingsNotice = fmt.Sprintf("Switching model to %s…", name)
		}
		m.onModelSelect(selected.ID)
	}
	return m.closeModelSelector()
}

func (m *Model) closeModelSelector() tea.Cmd {
	returnState := m.modelSelectorReturnState
	if returnState != StateSettings {
		returnState = StateInput
	}
	m.modelSelectorReturnState = StateInput
	m.modelSelectorNotice = ""
	m.state = returnState
	if returnState == StateInput {
		return m.input.Focus()
	}
	return nil
}

// keyConsumed is a non-nil no-op tea.Cmd returned by shortcut handlers that
// CONSUME a bare-printable key (e/E/[/]) in StateInput without producing a real
// command. handleKeyMsg's caller (Update) treats a non-nil return as "handled,
// stop" and returns early — so the consumed key is NOT also forwarded to the
// textarea via the type-ahead input.Update path (which would fire the shortcut
// AND type the char). Returning plain nil is ambiguous ("no cmd" vs "not
// handled") and falls through to typing; hence this explicit sentinel.
func keyConsumed() tea.Msg { return nil }

func (m *Model) clearQuitConfirmation() {
	if m.quitConfirmTime.IsZero() {
		return
	}
	m.quitConfirmTime = time.Time{}
	if m.toastManager != nil {
		m.toastManager.DismissTag("quit-confirm")
	}
}

// interruptActiveRequest is the single cancellation transaction shared by Esc
// and Ctrl+C. It stops backend work, settles every transient activity surface,
// preserves the type-ahead draft and records a calm timestamped stop marker.
func (m *Model) interruptActiveRequest() tea.Cmd {
	elapsed := time.Duration(0)
	if !m.streamStartTime.IsZero() {
		elapsed = time.Since(m.streamStartTime)
	}
	if m.onCancel != nil {
		m.onCancel()
	}
	m.clearQuitConfirmation()
	m.state = StateInput
	m.currentTool = ""
	m.currentToolInfo = ""
	m.processingLabel = ""
	m.streamIdleMsg = ""
	m.settleActiveToolCalls(false, "cancelled")
	m.streamStartTime = time.Time{}
	m.lastActivityTime = time.Time{}
	m.slowWarningShown = false
	m.responseHeaderShown = false
	m.output.FlushStream()
	if m.toolProgressBar != nil {
		m.toolProgressBar.Hide()
	}

	stopStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
	stopMsg := "⎯ stopped"
	if elapsed > 0 {
		stopMsg += " after " + format.Duration(elapsed)
	}
	m.output.AppendLine("")
	m.output.AppendLine(stopStyle.Render(stopMsg))
	m.output.AppendLine("")
	if m.onInterrupt != nil {
		m.onInterrupt()
	}
	return m.input.Focus()
}

// handleGlobalKeys handles global keyboard shortcuts.
func (m *Model) handleGlobalKeys(msg tea.KeyMsg) tea.Cmd {
	// Ctrl+C is local cancellation while reverse-history is open. Let the input
	// model restore its draft without arming the application quit flow.
	if msg.Type == tea.KeyCtrlC && m.state == StateInput && m.input.historySearchMode {
		return nil
	}
	// Quit is a deliberate double tap, not merely two Ctrl+C presses somewhere
	// within a time window. Typing, navigating or submitting in between cancels
	// the pending confirmation and removes its now-stale toast.
	if msg.Type != tea.KeyCtrlC {
		m.clearQuitConfirmation()
	}
	// Ctrl+K opens the model selector. The welcome panel and model selector
	// both advertise this binding, so keep the handler global for the input
	// state instead of hiding model choice behind slash commands only.
	// keyConsumed (round 8), not nil: returning nil here can fall through to
	// bubbles/textarea's OWN ctrl+k binding (DeleteAfterCursor). The selector
	// opens even when its list is empty, exposing recovery guidance while the
	// compose draft remains untouched.
	if msg.String() == "ctrl+k" && m.state == StateInput {
		m.openModelSelector()
		return keyConsumed
	}

	// Handle Ctrl+P for command palette (only when in input state)
	if msg.Type == tea.KeyCtrlP && m.state == StateInput {
		m.commandPalette.Show()
		m.state = StateCommandPalette
		return nil
	}

	// Ctrl+S opens the interactive settings modal — the richest no-typing
	// configure surface, so give it a direct key in addition to the palette
	// "Open Settings" action and the /settings command.
	if msg.Type == tea.KeyCtrlS && m.state == StateInput {
		m.requestOpenSettings()
		return keyConsumed
	}

	// Alt+N opens the user-visible notification history. ToastManager already
	// archives expired/evicted items; this binding makes that recovery path
	// reachable without colliding with textarea editing commands.
	if msg.String() == "alt+n" && m.state == StateInput {
		m.openNotificationCenter()
		return keyConsumed
	}

	// Ctrl+O — Claude-Code-style live-activity detail toggle. Deliberately
	// available DURING Processing/Streaming (the whole point is expanding the
	// detail while the agent works), not just StateInput. Toggles the ONE
	// verbosity flag: minimal (one dim line, feed suppressed) ⇄ detailed
	// (full card + activity feed panel). Expanding ALSO explicitly shows the
	// feed panel (ShowExplicit): the panel is hidden by default and its
	// auto-show only fires on parallel/sub-agent activity, so without this
	// the toggle flipped a flag but nothing visibly opened during ordinary
	// streaming — the panel must open deterministically on the keypress, even
	// empty ("No activity yet"). Collapsing needs no panel call — the render
	// gate (feedRendered) is off in minimal mode. The fine-grained feed-only
	// toggle remains on the palette for hiding the feed WITHIN detailed mode.
	// keyConsumed so the keystroke never also reaches the compose textarea
	// (round-8 rule).
	if msg.Type == tea.KeyCtrlO &&
		(m.state == StateInput || m.state == StateProcessing || m.state == StateStreaming) {
		m.liveDetailExpanded = !m.liveDetailExpanded
		if m.liveDetailExpanded && m.activityFeed != nil {
			m.activityFeed.ShowExplicit()
		}
		if m.toastManager != nil {
			if m.liveDetailExpanded {
				m.toastManager.ShowInfo("Live activity: detailed")
			} else {
				m.toastManager.ShowInfo("Live activity: minimal")
			}
		}
		return keyConsumed
	}

	// Handle Ctrl+A for agent tree panel toggle. Works DURING
	// Processing/Streaming too (the Ctrl+O/Ctrl+X pattern): the tree shows
	// running sub-agents, which is exactly a during-work surface — gating it
	// to StateInput meant the user could not open it while agents actually
	// ran. Ctrl-key, so it can't fight the type-ahead input. keyConsumed
	// (round 8), not nil: a bare `nil` return let bubbles/textarea's OWN
	// ctrl+a binding (LineStart) ALSO fire on the same keystroke — jumping
	// the compose cursor to column 0 every time the user toggled the panel.
	if msg.Type == tea.KeyCtrlA &&
		(m.state == StateInput || m.state == StateProcessing || m.state == StateStreaming) {
		m.toggleAgentTreePanel()
		return keyConsumed
	}

	// Handle Ctrl+H for context observatory dashboard. keyConsumed (round 8),
	// not nil: toggling the panel OFF leaves m.state at StateInput, and a
	// bare `nil` return let bubbles/textarea's OWN ctrl+h binding
	// (DeleteCharacterBackward) ALSO fire — deleting a character from the
	// compose buffer every time the user closed the panel. (Opening it DOES
	// change state, making keyConsumed a no-op difference there — this
	// covers the close case, found alongside the round-8 UI audit's other
	// global-shortcut/textarea collisions.)
	// Also matches StateContextObservatory so the key that OPENED the panel
	// can close it again (toggle symmetry) — previously only Esc closed it,
	// while the panel's own footer advertised keys that didn't work.
	if msg.Type == tea.KeyCtrlH && (m.state == StateInput || m.state == StateContextObservatory) {
		if m.observatoryPanel != nil {
			m.observatoryPanel.Toggle()
			if m.observatoryPanel.IsVisible() {
				m.state = StateContextObservatory
			} else if m.state == StateContextObservatory {
				m.state = StateInput
				return m.input.Focus()
			}
		}
		return keyConsumed
	}

	// Handle Ctrl+X for plan panel expand/collapse. Unlike the other panel
	// toggles this works during Processing/Streaming too — the plan panel is
	// on screen exactly while a plan executes, which is when the user wants
	// to peek at the full step list. Ctrl (not bare printable) so it can't
	// conflict with ordinary typed characters. It is still consumed explicitly:
	// control-key bindings may be added to the textarea now or in the future.
	if msg.Type == tea.KeyCtrlX && (m.state == StateInput || m.state == StateProcessing || m.state == StateStreaming) {
		if m.planProgressPanel != nil && m.planProgressPanel.IsVisible() {
			m.planProgressPanel.Toggle()
		} else if m.toastManager != nil {
			m.toastManager.ShowInfo("No plan panel — appears while a plan is executing")
		}
		return keyConsumed
	}

	// Handle Ctrl+T for todos toggle. Works DURING Processing/Streaming too
	// (the Ctrl+O/Ctrl+X pattern): the todo list tracks the agent's plan
	// WHILE it works — gating to StateInput meant the advertised "Ctrl+T
	// tasks N" hint didn't work exactly when the tasks were being executed.
	// Ctrl-key, so it can't fight the type-ahead input. keyConsumed (round
	// 8), not nil: a bare `nil` return let bubbles/textarea's OWN ctrl+t
	// binding (TransposeCharacterBackward) ALSO fire on the same keystroke,
	// swapping the two characters left of the cursor in the compose buffer
	// (e.g. "hello" -> "helol") every time the user opened/closed the panel.
	if msg.Type == tea.KeyCtrlT &&
		(m.state == StateInput || m.state == StateProcessing || m.state == StateStreaming) {
		m.toggleTodosPanel()
		return keyConsumed
	}

	// Scroll shortcuts (work in input and streaming states). ctrl+b/ctrl+f
	// return keyConsumed (round 8), not nil: these are pure OUTPUT-scroll
	// commands with no reason to ALSO touch the compose buffer, but a bare
	// `nil` return let bubbles/textarea's OWN ctrl+b/ctrl+f bindings
	// (CharacterBackward/CharacterForward) fire too — moving the compose
	// cursor sideways on every scroll keystroke. This is active during
	// Processing/Streaming (type-ahead composing while the agent works) as
	// well as StateInput, contrary to what the comment above previously
	// implied was already safe.
	if m.state == StateInput || m.state == StateStreaming || m.state == StateProcessing {
		switch msg.String() {
		case "ctrl+b": // Scroll up
			newOffset := max(m.output.viewport.YOffset-3, 0)
			m.output.viewport.SetYOffset(newOffset)
			m.output.SetFrozen(true)
			return keyConsumed
		case "ctrl+f": // Scroll down
			maxOffset := max(m.output.viewport.TotalLineCount()-m.output.viewport.Height, 0)
			newOffset := min(m.output.viewport.YOffset+3, maxOffset)
			m.output.viewport.SetYOffset(newOffset)
			// Use the exact IsAtBottom() check the mouse-wheel/PgUp/PgDn fix
			// (v0.100.59) established — NOT a "within N lines" proximity
			// heuristic. A proximity tolerance unfreezes auto-follow a line
			// or two short of the true bottom; the very next streamed chunk
			// then snaps the viewport the rest of the way with no further
			// user input, reproducing the exact snap-back bug that fix
			// removed from output.go, just via ctrl+f instead of the wheel.
			m.output.SetFrozen(!m.output.IsAtBottom())
			return keyConsumed
		case "ctrl+g": // Toggle freeze scroll (for text selection)
			frozen := !m.output.IsFrozen()
			m.output.SetFrozen(frozen)
			if m.toastManager != nil {
				if frozen {
					m.toastManager.ShowInfo("Scroll frozen — select text freely")
				} else {
					m.toastManager.ShowInfo("Scroll unfrozen")
				}
			}
			return keyConsumed
		}
	}

	// Ctrl+J: insert newline (standard terminal newline, works in all terminals)
	// Ctrl+J inserts a newline — including DURING Processing/Streaming, where
	// the type-ahead compose box is live: gating to StateInput made multi-line
	// composing impossible exactly while the agent works. Insert explicitly and
	// consume the key so a future textarea ctrl+j binding cannot add a second
	// newline through Update's fall-through path.
	if msg.Type == tea.KeyCtrlJ &&
		(m.state == StateInput || m.state == StateProcessing || m.state == StateStreaming) {
		m.input.InsertNewline()
		return keyConsumed
	}

	// Ctrl+U: context-aware — clear input when has text, scroll half-page up
	// when empty. The empty branch returns keyConsumed (round 8) for the same
	// reason as Ctrl+D below (gated to an empty textarea, so the collision
	// with textarea's ctrl+u DeleteBeforeCursor was already harmless — kept
	// consistent rather than left as an exception). The NON-empty fall-through
	// is deliberate: textarea's own DeleteBeforeCursor IS the "clear input"
	// behavior — don't consume it.
	// Works during Processing/Streaming too — the 3-line scrolls Ctrl+B/Ctrl+F
	// were already extended there, so the half-page siblings staying
	// StateInput-only made the scroll set inconsistent mid-stream.
	if msg.Type == tea.KeyCtrlU &&
		(m.state == StateInput || m.state == StateProcessing || m.state == StateStreaming) {
		if m.input.textarea.Value() == "" {
			newOffset := max(m.output.viewport.YOffset-m.output.viewport.Height/2, 0)
			m.output.viewport.SetYOffset(newOffset)
			m.output.SetFrozen(true)
			return keyConsumed
		}
		// Fall through to default handler (clears input)
	}

	// Ctrl+D mirrors Ctrl+U: scroll half-page down when the input is empty.
	// The shortcuts overlay has advertised this binding for a while; wiring
	// it here keeps navigation discoverable and predictable. keyConsumed
	// (round 8): gated to an EMPTY textarea, so bubbles/textarea's own
	// ctrl+d (DeleteCharacterForward) firing too was already harmless
	// (nothing to delete) — kept consistent with the other collision fixes
	// in this function rather than left as the one exception.
	if msg.Type == tea.KeyCtrlD &&
		(m.state == StateInput || m.state == StateProcessing || m.state == StateStreaming) &&
		m.input.textarea.Value() == "" {
		maxOffset := max(m.output.viewport.TotalLineCount()-m.output.viewport.Height, 0)
		newOffset := min(m.output.viewport.YOffset+m.output.viewport.Height/2, maxOffset)
		m.output.viewport.SetYOffset(newOffset)
		total := m.output.viewport.TotalLineCount()
		bottom := m.output.viewport.YOffset + m.output.viewport.Height
		m.output.SetFrozen(total-bottom > 2)
		return keyConsumed
	}

	// NOTE: there is deliberately NO "ctrl+shift+c" binding. bubbletea v1 has
	// no such key name (ctrl+shift exists only for home/end/arrows), and a
	// real terminal encodes Ctrl+Shift+C as byte 0x03 — identical to Ctrl+C,
	// the CANCEL key. The old branch here could never match, and the shortcuts
	// overlay advertising it steered users into cancelling their own request.
	// Compact mode remains reachable via the palette ("Toggle Compact Mode").

	// Handle 'E' (shift+e) for toggle all tool outputs (only when input is empty)
	if msg.String() == "E" && m.state == StateInput && m.input.Value() == "" {
		if m.toolOutput != nil && m.toolOutput.EntryCount() > 0 {
			m.toolOutput.ToggleAll()
			// Honest toast: scrollback is append-only, so flipping AllExpanded
			// cannot re-render cards already on screen — it sets the default
			// for entries emitted from now on (AddEntry inherits AllExpanded).
			// The old "All tool outputs expanded" claimed an on-screen change
			// that never happened; Ctrl+E (single, re-emits content) is the
			// key that actually expands an existing card.
			if m.toolOutput.AllExpanded {
				m.toastManager.ShowInfo("New tool outputs will render expanded (Ctrl+E expands the last one)")
			} else {
				m.toastManager.ShowInfo("New tool outputs will render collapsed")
			}
			// Consumed as a shortcut — must NOT also be typed into the textarea
			// (StateInput is type-ahead-active, so a nil return would leak 'E').
			return keyConsumed
		}
		// No tool output to toggle — fall through so 'E' types normally.
	}

	// Handle Ctrl+E / plain 'e' for tool output expand/collapse. Ctrl+E
	// is the Claude Code-style primary binding — works even with text
	// typed in the input (Ctrl combos don't fight the textarea's normal
	// keys). Plain 'e' kept as a shortcut for users mid-flow with empty
	// input. Both call the same toggle.
	ctrlE := msg.Type == tea.KeyCtrlE && m.state == StateInput
	plainE := msg.String() == "e" && m.state == StateInput && m.input.Value() == ""
	if ctrlE || plainE {
		if m.toolOutput != nil && m.lastToolOutputIndex >= 0 {
			entry := m.toolOutput.GetEntry(m.lastToolOutputIndex)
			if entry != nil {
				wasExpanded := m.toolOutput.IsExpanded(m.lastToolOutputIndex)
				m.toolOutput.ToggleExpand(m.lastToolOutputIndex)
				if wasExpanded {
					m.toastManager.ShowInfo("Marked compact; existing scrollback is unchanged")
				} else {
					m.toastManager.ShowInfo("Full tool output appended below")
					m.output.AppendLine(m.styles.ToolResult.Render("   " + strings.ReplaceAll(entry.FullContent, "\n", "\n    ")))
				}
				// Consumed the toggle — don't also type 'e' into the textarea.
				return keyConsumed
			}
		}
		// Nothing to toggle: fall through, do NOT consume. Plain 'e' then types
		// normally; Ctrl+E reaches the bubbles textarea's own binding (ctrl+e =
		// jump to line end, the emacs sibling of the still-working Ctrl+A =
		// line start). Returning keyConsumed here would swallow that LineEnd
		// binding — a real regression, since Ctrl+E is a live forwarded textarea
		// key, not an inert control combo.
	}

	// Option+C: copy last AI response to clipboard. keyConsumed (round 8),
	// not nil: m.state stays StateInput here, so a bare `nil` return let
	// bubbles/textarea's OWN alt+c binding (CapitalizeWordForward) ALSO
	// fire — capitalizing a word in the compose buffer every time the user
	// copied the last response.
	// Works during Processing/Streaming too: m.lastResponseText still holds
	// the PRIOR completed response while a new turn streams — copying it is
	// exactly the "grab that answer while the next one runs" move. Alt-key,
	// so it can't fight type-ahead typing.
	if msg.String() == "alt+c" &&
		(m.state == StateInput || m.state == StateProcessing || m.state == StateStreaming) {
		if m.lastResponseText != "" {
			showClipboardCopyFeedback(m.toastManager, "Copied last response", copyTextToClipboard(m.lastResponseText))
		} else if m.toastManager != nil {
			m.toastManager.ShowWarning("No completed response to copy")
		}
		return keyConsumed
	}

	// '?' opens keyboard shortcuts overlay while the composer is empty.
	// Code-block [/] selection used to live here, but selection had no visual
	// representation or follow-up action and only swallowed ordinary brackets.
	if m.state == StateInput && m.input.Value() == "" {
		if msg.String() == "?" {
			if m.shortcutsOverlay != nil {
				m.shortcutsOverlay.Show()
			}
			m.state = StateShortcutsOverlay
			return nil
		}
	}

	// All commands accessible via Ctrl+P (Command Palette) and slash commands

	switch msg.Type {
	case tea.KeyCtrlC:
		// During an active turn Ctrl+C means interrupt, exactly like Esc. The old
		// path armed application quit unless a specific progress bar happened to
		// be visible, contradicting the shortcut help and making a second press
		// terminate the app while the request was still running.
		if m.state == StateProcessing || m.state == StateStreaming {
			return m.interruptActiveRequest()
		}
		// Double Ctrl+C confirmation: quit only on a consecutive second press.
		now := time.Now()
		if !m.quitConfirmTime.IsZero() && now.Sub(m.quitConfirmTime) < 3*time.Second {
			m.clearQuitConfirmation()
			if m.onQuit != nil {
				m.onQuit()
			}
			return tea.Quit
		}
		m.quitConfirmTime = now
		if m.toastManager != nil {
			message := "Press Ctrl+C again to quit"
			if m.input.Value() != "" {
				message = "Draft preserved — press Ctrl+C again to quit"
			}
			m.toastManager.ShowTagged("quit-confirm", ToastWarning, message, 3*time.Second)
		}
		return keyConsumed

	case tea.KeyEscape:
		// Handle Esc for Context Observatory
		if m.state == StateContextObservatory {
			if m.observatoryPanel != nil {
				m.observatoryPanel.Hide()
			}
			m.state = StateInput
			return m.input.Focus()
		}

		// ESC interrupts processing/streaming and returns to input
		if m.state == StateProcessing || m.state == StateStreaming {
			return m.interruptActiveRequest()
		}

		// Esc at an IDLE prompt while a background /loop iteration is firing:
		// the streaming the user sees is the loop's sub-agent — the foreground
		// cancel has nothing to cancel, so route to onCancel anyway; the app
		// side kills the in-flight loop iteration when no foreground request
		// exists (the "застопил loop, но стриминг не прекратился даже после
		// Esc" report). Gated to an empty, suggestion-free input so Esc keeps
		// its editing meanings while composing.
		if m.state == StateInput && m.runtimeStatus.LoopFiring &&
			m.input.Value() == "" && !m.input.ShowingSuggestions() {
			if m.onCancel != nil {
				m.onCancel()
			}
			return keyConsumed
		}

	case tea.KeyEnter:
		// KeyEnter is a round-8 collision-class member too: bubbles/textarea
		// binds "enter" to InsertNewline (never disabled in NewInputModel), so
		// any branch below that returns bare nil while the model stays in a
		// typeAheadActive state lets the SAME Enter ALSO insert a newline into
		// the compose textarea. The submit paths leaked an invisible "\n" into
		// the just-Reset input — Value()'s TrimSpace hid it, but the slash-
		// suggestion refresh checks HasPrefix on the RAW value, so autocomplete
		// silently died for the rest of the session after the first submit;
		// a debounce-swallowed Enter inserted an INTERIOR newline at a
		// mid-string cursor, corrupting the eventually-submitted message.
		// Every consuming branch below must return keyConsumed. The ONE
		// deliberate forward is suggestions-showing (the outer guards skip all
		// branches) — InputModel's own KeyEnter case accepts the highlighted
		// suggestion via the forwarded key.
		//
		// Alt+Enter: insert newline for multi-line input, including type-ahead
		// while a request is Processing/Streaming. Returning nil here is safe
		// (textarea's InsertNewline binding matches "enter"/"ctrl+m", not
		// "alt+enter") and deliberate: the forwarded key lets InputModel run
		// its normal post-key refresh.
		if (m.state == StateInput || m.state == StateProcessing || m.state == StateStreaming) &&
			!m.isModalState() && msg.Alt {
			m.input.InsertNewline()
			return nil
		}
		// Send message on Enter (when input is not empty and no suggestions are shown)
		if m.state == StateInput && !m.input.SuggestionsBlockSubmit() {
			value := m.input.Value()
			if value != "" {
				if m.onSubmit == nil {
					m.showSubmitUnavailable()
					return keyConsumed
				}
				// Rate limiting: prevent rapid message spam. keyConsumed, not
				// nil — a swallowed Enter must not fall through to the
				// textarea and insert a newline at the cursor.
				if time.Since(m.lastSubmitTime) < m.minSubmitDelay {
					return keyConsumed
				}
				m.lastSubmitTime = time.Now()

				// Expand collapsed-paste chips to their real content for the
				// model BEFORE Reset (which clears the paste store). The echo
				// stays collapsed (value); history keeps the expanded form so a
				// recalled paste-message resubmits correctly.
				expanded := m.input.ExpandedValue()
				m.input.AddToHistory(expanded)
				m.input.Reset()
				m.state = StateProcessing
				m.streamStartTime = time.Now()  // Start timeout tracking
				m.lastActivityTime = time.Now() // Start activity tracking
				m.slowWarningShown = false      // Reset slow warning
				m.responseHeaderShown = false   // Reset for new response
				m.responseToolDuration = 0      // Reset tool timing for new response
				m.responseToolCount = 0
				m.responseToolFailures = 0
				m.output.SetFrozen(false) // new turn → follow the response from the bottom (clears any leftover scroll-up freeze)
				m.flushPendingToolLines()
				m.output.AppendLine(m.styles.FormatUserMessage(value))
				m.output.AppendLine("")

				if m.onSubmit != nil {
					m.onSubmit(expanded)
				}
				// keyConsumed, not nil: m.state is now StateProcessing (which
				// IS typeAheadActive), so a bare nil forwarded this Enter to
				// the textarea — leaving an invisible "\n" in the just-Reset
				// compose buffer (the autocomplete-death leak).
				return keyConsumed
			}
		}
		// Type-ahead Enter: queue the composed message behind the in-flight
		// request. State stays Processing/Streaming — the app dequeues FIFO
		// when the current request completes. The message is rendered to the
		// scrollback NOW (it won't re-render at dispatch time).
		if (m.state == StateProcessing || m.state == StateStreaming) &&
			!m.isModalState() && !m.input.SuggestionsBlockSubmit() {
			value := m.input.Value()
			if value != "" {
				if m.onSubmit == nil {
					m.showSubmitUnavailable()
					return keyConsumed
				}
				if time.Since(m.lastSubmitTime) < m.minSubmitDelay {
					return keyConsumed // swallowed Enter must not reach the textarea
				}
				m.lastSubmitTime = time.Now()

				// Expand collapsed-paste chips before Reset (clears the store);
				// echo stays collapsed (value), history + model get expanded.
				expanded := m.input.ExpandedValue()
				m.input.AddToHistory(expanded)
				m.input.Reset()
				m.flushPendingToolLines() // queued echo lands mid-turn — keep order
				m.output.AppendLine(m.styles.FormatUserMessage(value))
				m.output.AppendLine("")

				if m.onSubmit != nil {
					m.onSubmit(expanded)
				}
				return keyConsumed // same leak as the main submit path above
			}
		}
		// Bare Enter with nothing to submit (empty input, no suggestions
		// showing): consume it. Falling through to the function tail's
		// `return nil` forwarded it to the textarea, piling up an invisible
		// "\n" per press — which broke the raw-value checks downstream
		// (slash-suggestion HasPrefix, Ctrl+U's empty-input gate). Gated to
		// the typeAhead states so Enter in other states is untouched, and to
		// !ShowingSuggestions so the dropdown-accept forward stays intact.
		if (m.state == StateInput || m.state == StateProcessing || m.state == StateStreaming) &&
			!m.isModalState() && !m.input.SuggestionsBlockSubmit() {
			return keyConsumed
		}

	case tea.KeyCtrlL:
		// Clear output screen
		if m.state == StateInput {
			m.clearOutputWithFeedback()
			return keyConsumed
		}

	case tea.KeyCtrlU:
		// Clear input line
		if m.state == StateInput {
			m.input.Reset()
			return keyConsumed
		}

	case tea.KeyShiftTab:
		// Claude Code-style session-mode cycle: Normal → Plan → YOLO →
		// Normal. One tap enters plan mode, two taps enables YOLO
		// (permissions off + sandbox off), three taps returns to
		// Normal. Falls back to the older plan-only toggle when the
		// cycle callback isn't wired (older call sites / tests).
		// Async so the Bubble Tea event loop doesn't block on the
		// cascaded /plan + /permissions + /sandbox toggles inside.
		if m.state == StateInput {
			m.requestSessionModeCycle() // feedback via the async result message
			return keyConsumed
		}

	case tea.KeyPgUp, tea.KeyPgDown:
		// Forward page up/down to output viewport for scrolling
		var cmd tea.Cmd
		m.output, cmd = m.output.Update(msg)
		// Pause/resume stream auto-follow by whether the user paged away from the
		// bottom — lets them page through history while the agent is writing.
		m.output.SetFrozen(!m.output.IsAtBottom())
		return cmd
	}

	return nil
}

func (m *Model) showSubmitUnavailable() {
	const notice = "Unavailable: message submission is not connected · draft preserved"
	m.input.suggestionNotice = notice
	if m.toastManager != nil {
		m.toastManager.ShowTagged("submit-unavailable", ToastError, "Message submission is unavailable · draft preserved", 6*time.Second)
	}
}

func (m *Model) openModelSelector() {
	m.modelSelectorReturnState = StateInput
	m.modelSelectorNotice = ""
	if m.modelSwitchPending != "" {
		m.modelSelectorNotice = "A model switch is still applying"
	}
	if m.state == StateSettings {
		m.modelSelectorReturnState = StateSettings
	}
	m.state = StateModelSelector
	m.modelSelectedIndex = 0
	for i, model := range m.availableModels {
		if model.ID == m.currentModel {
			m.modelSelectedIndex = i
			break
		}
	}
}

// markResponseStarted marks the first content of a new response. It also clears
// any leftover scroll-up freeze so the response is followed from the bottom —
// covers BOTH a direct submit and a type-ahead/dequeued submit (the latter
// re-enters the app's handleSubmit, never the UI KeyEnter reset). It fires only
// on the first chunk (responseHeaderShown false→true), so a mid-response
// scroll-up stays frozen for the rest of that response.
func (m *Model) markResponseStarted() {
	if !m.responseHeaderShown {
		m.responseHeaderShown = true
		m.output.SetFrozen(false)
		// Round 7: retryAttempt/retryMax were previously cleared ONLY by
		// StatusStreamResume — a narrower signal than "a retry succeeded"
		// (it fires only if THIS stream itself went idle-then-resumed, plus
		// 2 unrelated reuses of the same message type for MCP
		// reconnect/tools-changed notifications). A retried request whose
		// new stream starts and completes cleanly, with no idle gap, never
		// triggered it — leaving a stale "retry N/M" badge (ColorWarning,
		// second-highest render priority in the engine-status badge) shown
		// for every subsequent turn until an unrelated event happened to
		// clear it. The first real content of ANY new response — succeeded
		// on the first attempt or the Nth — is the correct "no longer
		// retrying" signal.
		m.retryAttempt = 0
		m.retryMax = 0
	}
}

// handleMessageTypes handles various message types.
func (m *Model) handleMessageTypes(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case QueuedCountMsg:
		m.queuedPending = max(int(msg), 0)
		return nil

	case QueuedMessageRejectedMsg:
		if msg.Waiting > 0 {
			m.queuedPending = msg.Waiting
		}
		restored := m.input.RestoreDraft(msg.Message)
		reason := safeKeyEntryText(msg.Reason)
		if reason == "" {
			reason = "Queue full"
		}
		feedback := reason + " — message restored to composer"
		durable := "↳ not queued — restored to composer"
		if !restored {
			feedback = reason + " — current draft kept; rejected message is in history (↑)"
			durable = "↳ not queued — kept in history; current draft unchanged"
		}
		if m.toastManager != nil {
			m.toastManager.ShowTagged("queue-rejected", ToastWarning, feedback, 6*time.Second)
		}
		m.output.AppendLine(lipgloss.NewStyle().Foreground(ColorWarning).Render(durable))
		if restored && m.state == StateInput {
			return m.input.Focus()
		}
		return nil

	case DebugStateRequestMsg:
		// Computed on THIS goroutine (the Update loop, which owns every
		// mutation to Model's fields, incl. backgroundTasks) — see the type
		// doc for why a cross-goroutine direct DebugState() call is unsafe.
		// Non-blocking send: the contract says Resp is buffered (cap 1), so
		// this always succeeds for a compliant caller even after its timeout
		// fired — but if a future caller ever violates the contract with an
		// unbuffered channel and isn't parked on the receive, dropping the
		// response (it times out) beats blocking the whole Update loop forever.
		if msg.Resp != nil {
			select {
			case msg.Resp <- m.DebugState():
			default:
			}
		}
		return nil

	case ContextHealthMsg:
		if m.observatoryPanel != nil {
			m.observatoryPanel.UpdateHealth(msg)
		}
		// Sync the main tokenUsage so the status bar and progress bar show the active context
		m.tokenUsage = &TokenUsageMsg{
			Tokens:      msg.TotalTokens,
			MaxTokens:   msg.MaxTokens,
			PercentUsed: msg.PercentUsed,
			NearLimit:   msg.PercentUsed >= 0.80,
		}
		m.baseTokenCount = msg.TotalTokens
		return nil

	case StreamThinkingMsg:
		m.streamStartTime = time.Now()
		m.lastActivityTime = time.Now()
		m.slowWarningShown = false
		// Never clobber an ACTIVE modal (permission/question/plan-approval)
		// raised by a DIFFERENT source — a background /loop iteration's
		// sub-agent hitting a LevelAsk tool, or a coordinate sub-task. Round-4
		// guarded modal-OPENING messages against an already-active modal, but
		// missed the symmetric case: a modal that just opened getting
		// overwritten by the NEXT chunk of an unrelated, already-in-flight
		// foreground stream milliseconds later — silently vanishing the
		// prompt while promptPermission/promptPlanApproval stays blocked
		// (possibly indefinitely, per the -1 "wait forever" timeout option).
		// The stream content still gets appended below — nothing is lost,
		// only the state transition is deferred until the modal resolves.
		if !m.isModalState() {
			m.state = StateStreaming
		} else if isWorkspaceOverlayState(m.state) {
			m.workspaceOverlayReturnState = StateStreaming
		}

		m.markResponseStarted()

		m.flushPendingToolLines()
		m.output.AppendThinkingStream(string(msg))

	case StreamTextMsg:
		m.streamStartTime = time.Now() // Reset timeout on streaming activity
		m.lastActivityTime = time.Now()
		m.slowWarningShown = false
		m.streamIdleMsg = "" // Server responded — clear idle warning
		if !m.isModalState() {
			m.state = StateStreaming
		} else if isWorkspaceOverlayState(m.state) {
			m.workspaceOverlayReturnState = StateStreaming
		}
		m.processingLabel = "" // Text streaming is the feedback itself

		// Close thinking block when text starts
		m.output.EndThinking()

		// Mark response as started (no header in Claude Code style)
		m.markResponseStarted()

		m.flushPendingToolLines()
		m.output.AppendTextStream(string(msg))
		m.currentResponseBuf.WriteString(string(msg))

	case ToolCallMsg:
		m.streamStartTime = time.Now() // Reset timeout on tool activity
		m.lastActivityTime = time.Now()
		m.slowWarningShown = false
		m.streamIdleMsg = ""   // Server responded — clear idle warning
		m.processingLabel = "" // Tool name takes over in status bar

		// Close thinking block when tool call starts
		m.output.EndThinking()

		// Mark response as started (no header in Claude Code style)
		m.markResponseStarted()

		// Generate tool info for status line display
		toolInfo := m.extractToolInfoFromArgs(msg.Name, msg.Args)

		// Update status bar spinner with the latest tool (last one shown)
		m.currentTool = msg.Name
		m.currentToolInfo = toolInfo
		m.toolStartTime = time.Now()

		// Update plan progress panel with current tool (live activity)
		if m.planProgressPanel != nil && m.planProgressPanel.IsVisible() {
			m.planProgressPanel.SetCurrentTool(msg.Name, toolInfo)
		}

		// Add to activity feed and track in activeToolCalls
		toolID := fmt.Sprintf("tool-%d", time.Now().UnixNano())
		m.activeToolCalls = append(m.activeToolCalls, activeToolCall{
			activityID: toolID,
			name:       msg.Name,
			args:       maps.Clone(msg.Args),
			info:       toolInfo,
			startTime:  time.Now(),
		})
		if m.activityFeed != nil {
			m.activityFeed.AddEntry(ActivityFeedEntry{
				ID:          toolID,
				Type:        ActivityTypeTool,
				Name:        msg.Name,
				Description: formatToolActivity(msg.Name, msg.Args),
				Status:      ActivityRunning,
				StartTime:   time.Now(),
				Details:     msg.Args,
			})
		}

		// No scrollback line on START — the live activity card shows the running
		// tool (spinner + name + target + elapsed). The single merged line
		// (▪ Name(target) · outcome) is emitted on COMPLETION instead, so a tool
		// occupies ONE row, not a call row + a result row that repeat the name.

	case ToolResultMsg:
		m.streamStartTime = time.Now() // Reset timeout on tool result
		m.lastActivityTime = time.Now()
		m.slowWarningShown = false

		// Strip machine-facing context blocks ([context:…] enrichment, the
		// edit tool's model-facing instruction) ONCE here — msg is a local
		// copy, so this covers every user-facing consumer below (card body,
		// expand store, activity-feed summary) without touching the model's
		// own tool-result stream.
		msg.Content = stripModelFacingContext(msg.Content)

		// Stash the edit display-diff for handleToolResultWithStatus (which
		// keeps its signature — a dozen tests call it directly). Consumed and
		// cleared synchronously within this same Update dispatch.
		if msg.Diff != "" && !msg.Failed {
			m.pendingEditDiff = &editDiffDisplay{Text: msg.Diff, Added: msg.DiffAdded, Removed: msg.DiffRemoved}
		} else {
			m.pendingEditDiff = nil
		}

		matchIdx := m.findActiveToolCall(msg.Name, msg.Args)

		if matchIdx >= 0 {
			matched := m.activeToolCalls[matchIdx]

			// Track tool execution time for response timing breakdown
			if !matched.startTime.IsZero() {
				m.responseToolDuration += time.Since(matched.startTime)
				m.responseToolCount++
				if msg.Failed {
					m.responseToolFailures++
				}
			}

			// Complete entry in activity feed with result summary
			if m.activityFeed != nil {
				summary := GenerateResultSummary(matched.name, msg.Content)
				if msg.Failed && strings.TrimSpace(msg.Error) != "" {
					summary = strings.TrimSpace(msg.Error)
				}
				m.activityFeed.CompleteEntry(matched.activityID, !msg.Failed, summary)
			}

			// Handle tool result using matched tool's info and timing
			m.handleToolResultWithStatus(msg.Content, matched.name, matched.info, matched.startTime, msg.Failed, msg.Error)

			// Remove matched entry from slice
			m.activeToolCalls = append(m.activeToolCalls[:matchIdx], m.activeToolCalls[matchIdx+1:]...)

			// Update status bar from remaining active calls (or clear if empty)
			if len(m.activeToolCalls) > 0 {
				last := m.activeToolCalls[len(m.activeToolCalls)-1]
				m.currentTool = last.name
				m.currentToolInfo = last.info
				m.toolStartTime = last.startTime
			} else {
				m.currentTool = ""
				m.currentToolInfo = ""
				if m.processingLabel == "" {
					m.processingLabel = "Analyzing"
				}
			}
		} else {
			// Fallback: no matching active call (shouldn't happen, but be safe)
			m.handleToolResultWithStatus(msg.Content, msg.Name, "", time.Time{}, msg.Failed, msg.Error)
			m.currentTool = ""
			m.currentToolInfo = ""
			if m.processingLabel == "" {
				m.processingLabel = "Analyzing"
			}
		}

		// Hide tool progress bar when no active tools remain
		if len(m.activeToolCalls) == 0 {
			if m.toolProgressBar != nil {
				m.toolProgressBar.Hide()
			}
			// Clear current tool in plan progress panel
			if m.planProgressPanel != nil && m.planProgressPanel.IsVisible() {
				m.planProgressPanel.ClearCurrentTool()
			}
		}

	case InlineDiffMsg:
		m.renderInlineDiff(msg)

	case ToolProgressMsg:
		// Reset timeout on progress heartbeat (keeps UI alive during long operations)
		m.streamStartTime = time.Now()
		m.lastActivityTime = time.Now()
		m.slowWarningShown = false
		// Update tool progress bar
		if m.toolProgressBar != nil {
			m.toolProgressBar.Update(msg)
		}

	case FilePeekMsg:
		m.lastActivityTime = time.Now()
		if m.filePeek != nil {
			m.filePeek.ShowPeek(msg)
		}

	case ThinkingTickMsg:
		// Model is actively thinking — refresh the view so elapsed time updates
		m.lastActivityTime = time.Now()

	case ResponseDoneMsg:
		m.flushPendingToolLines() // end of turn — emit any buffered tool lines
		m.output.EndThinking()
		// Finalize the turn's bookkeeping unconditionally (the backend really
		// is done), but don't clobber an ACTIVE modal raised by a DIFFERENT
		// source (a background /loop iteration's permission prompt, a
		// coordinate sub-task's question) with StateInput — that would vanish
		// the prompt while its handler stays blocked waiting on a decision
		// nothing can now deliver (worse with the -1 "wait forever" permission
		// timeout). The modal's own close path returns to StateInput/whatever
		// state it decides once the user resolves it.
		modalActive := m.isModalState()
		if isWorkspaceOverlayState(m.state) {
			m.workspaceOverlayReturnState = StateInput
		}
		if !modalActive {
			m.state = StateInput
		}
		m.currentTool = ""
		m.currentToolInfo = ""
		m.processingLabel = ""
		m.currentActivity = "" // clear the live "doing X" label at turn end
		m.streamIdleMsg = ""   // Clear idle warning
		m.loopIteration = 0
		m.loopToolsUsed = 0
		m.settleActiveToolCalls(false, "ended without result")
		m.streamStartTime = time.Time{}  // Reset timeout tracking
		m.lastActivityTime = time.Time{} // Reset activity tracking
		m.slowWarningShown = false       // Reset slow warning
		m.responseHeaderShown = false    // Reset for next response
		m.lastErrorMsg = ""              // Reset error dedup
		m.lastErrorCount = 0
		m.lastRecoverableStatus = ""
		if m.currentResponseBuf.Len() > 0 {
			m.lastResponseText = m.currentResponseBuf.String()
			m.currentResponseBuf.Reset()
		}
		m.output.FlushStream() // Flush any remaining streamed content
		m.output.AppendLine("")
		if !modalActive {
			cmds = append(cmds, m.input.Focus())
		}

	case ResponseMetadataMsg:
		// Track request latency for status bar
		if msg.Duration > 0 {
			m.lastRequestLatency = msg.Duration
		}
		// Render the response metadata footer. When there's nothing measurable,
		// emit NO ruler — turns are separated by whitespace + the next user echo
		// (CC idiom), so a bare "───" between every turn is chrome we don't want.
		if footer := m.renderResponseMetadata(msg); footer != "" {
			m.output.AppendLine(footer)
		}
		m.output.AppendLine("")
		m.responseToolDuration = 0
		m.responseToolCount = 0
		m.responseToolFailures = 0

	case ErrorMsg:
		m.flushPendingToolLines() // preserve ordering before the error card
		errStr := msg.Error()
		// Same modal-preservation rule as ResponseDoneMsg above — an error
		// from an UNRELATED foreground/background source must not silently
		// dismiss an active permission/question/plan-approval modal.
		modalActive := m.isModalState()
		if isWorkspaceOverlayState(m.state) {
			m.workspaceOverlayReturnState = StateInput
		}
		if !modalActive {
			m.state = StateInput
		}
		m.currentTool = ""
		m.currentToolInfo = ""
		m.processingLabel = ""
		m.currentActivity = "" // clear the live "doing X" label at turn end
		m.streamIdleMsg = ""   // Clear idle warning
		m.loopIteration = 0
		m.loopToolsUsed = 0
		m.settleActiveToolCalls(false, errStr)
		m.streamStartTime = time.Time{}  // Reset timeout tracking
		m.lastActivityTime = time.Time{} // Reset activity tracking
		m.slowWarningShown = false       // Reset slow warning
		m.responseHeaderShown = false    // Reset for next response
		m.responseToolDuration = 0       // Reset tool timing
		m.responseToolCount = 0
		m.responseToolFailures = 0
		m.lastRecoverableStatus = ""
		m.currentResponseBuf.Reset() // Discard partial response on error
		m.output.FlushStream()       // Flush any remaining streamed content

		if errStr == m.lastErrorMsg {
			m.lastErrorCount++
			dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
			m.output.AppendLine(dimStyle.Render(fmt.Sprintf("  ✗ Same error repeated (%dx)", m.lastErrorCount+1)))
		} else {
			m.lastErrorMsg = errStr
			m.lastErrorCount = 0
			m.output.AppendLine("")
			m.output.AppendLine(FormatErrorWithGuidance(m.styles, errStr))
			m.output.AppendLine("")
		}
		if !modalActive {
			cmds = append(cmds, m.input.Focus())
		}

	case TodoUpdateMsg:
		m.todoItems = msg

	case ActivityLabelMsg:
		// Live "doing X" label from the in-progress todo. Unlike phaseLabel this
		// does NOT force state/timers — only the View label selection reads it,
		// and only while already processing.
		m.currentActivity = safeKeyEntryText(string(msg))

	case LoopIterationMsg:
		m.loopIteration = msg.Iteration
		m.loopToolsUsed = msg.ToolsUsed

	case StreamTokenUpdateMsg:
		if m.tokenUsage == nil {
			m.tokenUsage = &TokenUsageMsg{}
		}
		// Track estimated output tokens separately from input context.
		// Output tokens don't count toward the current context limit
		// (they become input on the next turn).
		m.tokenUsage.OutputTokens = msg.EstimatedOutputTokens
		m.tokenUsage.IsEstimate = true

	case TokenUsageMsg:
		wasNearLimit := m.tokenUsage != nil && m.tokenUsage.NearLimit
		m.tokenUsage = &msg
		m.baseTokenCount = msg.Tokens

		// Warn once when context approaches the limit
		if msg.NearLimit && !wasNearLimit && m.toastManager != nil {
			pct := int(msg.PercentUsed * 100)
			m.toastManager.ShowWarning(fmt.Sprintf("Context %d%% full — use /compact to free space", pct))
		}

	case RuntimeStatusMsg:
		m.handleRuntimeStatusMsg(msg)

	case ProjectInfoMsg:
		m.projectType = msg.ProjectType
		m.projectName = msg.ProjectName
		// Update iTerm2 badge when project info is received
		if runtime.GOOS == "darwin" && m.projectName != "" {
			badge := "Gokin: " + m.projectName
			if m.gitBranch != "" {
				badge += " (" + m.gitBranch + ")"
			}
			cmds = append(cmds, SetBadgeCmd(badge))
		}

	case PermissionRequestMsg:
		// Guard: don't overwrite an active modal prompt (prevents lost responses).
		// Auto-deny so the blocked caller doesn't hang until timeout. Deny
		// THIS incoming request specifically (msg.ID) — NOT whatever request
		// is currently displayed — so the still-showing prompt's own decision
		// can never be misattributed to this one, or vice versa.
		if m.isModalState() {
			resolved := m.onPermission != nil
			if resolved {
				m.onPermission(msg.ID, PermissionDeny)
			}
			m.showPromptCollision("permission request", "denied", resolved)
		} else {
			m.permRequest = &msg
			m.permSelectedOption = 0
			m.permShowDetails = false
			m.permDetailScroll = 0
			m.permNotice = ""
			m.state = StatePermissionPrompt
			cmds = append(cmds, m.bellCmd())
		}

	case QuestionRequestMsg:
		if m.isModalState() {
			resolved := m.onQuestion != nil
			if resolved {
				m.onQuestion("")
			}
			m.showPromptCollision("question", "cancelled", resolved)
		} else {
			m.questionRequest = &msg
			m.questionSelectedOption = questionDefaultIndex(msg.Options, msg.Default)
			m.questionCustomInput = false
			m.questionInputError = ""
			m.state = StateQuestionPrompt
			cmds = append(cmds, m.bellCmd())
			// If no options, initialize input model for free text
			if len(msg.Options) == 0 {
				m.questionInputModel = NewInputModel(m.styles, m.workDir)
				m.questionInputModel.SetWidth(promptInputContentWidth(m.width))
				cmds = append(cmds, m.questionInputModel.Focus())
			}
		}

	case PlanApprovalRequestMsg:
		if m.isModalState() {
			resolved := m.onPlanApproval != nil
			if resolved {
				m.onPlanApproval(PlanRejected)
			}
			m.showPromptCollision("plan", "rejected", resolved)
		} else {
			m.planRequest = &msg
			m.planSelectedOption = int(PlanApproved)
			if len(msg.Steps) == 0 {
				m.planSelectedOption = int(PlanRejected)
			}
			m.planStepScroll = 0
			m.planFeedbackMode = false
			m.planFeedbackError = ""
			m.planApprovalNotice = ""
			m.state = StatePlanApproval
			cmds = append(cmds, m.bellCmd())
		}

	case PlanProgressMsg:
		m.planProgress = &msg
		m.planProgressMode = (msg.Status == "in_progress" || msg.Status == "paused" || (msg.Status == "completed" && msg.Completed < msg.TotalSteps))

		// Update plan progress panel
		if m.planProgressPanel != nil {
			oldStepID := m.planProgressPanel.currentStepID
			newStepID := msg.CurrentStepID

			// Handle different status types (no toasts - updates shown in status line)
			switch msg.Status {
			case "in_progress":
				// New step started
				if newStepID != oldStepID && newStepID > 0 {
					m.planProgressPanel.StartStep(newStepID)
				}

			case "completed":
				// Step completed
				stepID := msg.CurrentStepID
				if stepID > 0 {
					m.planProgressPanel.CompleteStep(stepID, "", msg.Reason)
				}
				// WHOLE plan done (the per-step "completed" carries the
				// running totals; the final step's message is the terminal
				// signal — same discriminator planProgressMode uses above).
				// Start the linger → auto-hide window. Failed/paused plans
				// deliberately stay pinned: those states are actionable and
				// the user must see why execution stopped.
				if msg.TotalSteps > 0 && msg.Completed >= msg.TotalSteps {
					m.planProgressPanel.EndPlan()
				}

			case "failed":
				// Step failed - show toast only for failures
				stepID := msg.CurrentStepID
				if stepID > 0 {
					m.planProgressPanel.FailStep(stepID, msg.Reason, msg.Reason)
					if m.toastManager != nil {
						m.toastManager.ShowError(fmt.Sprintf("Step %d failed", stepID))
					}
				}

			case "skipped":
				// Step skipped
				stepID := msg.CurrentStepID
				if stepID > 0 {
					m.planProgressPanel.SkipStep(stepID, msg.Reason)
				}
			case "paused":
				stepID := msg.CurrentStepID
				if stepID > 0 {
					m.planProgressPanel.PauseStep(stepID, msg.Reason)
				}
			}
		}

		if msg.Status == "paused" {
			copyMsg := msg
			m.planPauseNotice = &copyMsg
		} else {
			// A pause notice describes one specific transition. Any subsequent
			// state supersedes it; retaining it after failed/skipped rendered a
			// stale "Continue" action for a step that was no longer paused.
			m.planPauseNotice = nil
		}

	case PlanCompleteMsg:
		m.planProgressMode = false
		m.planProgress = nil
		m.planPauseNotice = nil

		// End plan in progress panel
		if m.planProgressPanel != nil {
			m.planProgressPanel.EndPlan()
			// Only show toast for failures - success is obvious from output
			if m.toastManager != nil && !msg.Success {
				m.toastManager.ShowError("Plan execution failed")
			}
		}

	case DiffPreviewRequestMsg:
		// Guard: don't overwrite an active modal (prevents lost responses —
		// same class as PermissionRequestMsg above). This message arrives
		// async via safeSendToProgram from a goroutine independent of
		// whatever else may have just opened a modal (e.g. a background
		// /loop iteration's diff prompt racing a foreground Ctrl+S). Without
		// this, opening it would silently clobber an in-flight
		// Permission/Question/PlanApproval/Diff prompt, orphaning its blocked
		// decision channel until DiffDecisionTimeout/ctx cancellation.
		// Auto-reject THIS incoming request so its caller doesn't hang.
		if m.isModalState() {
			resolved := m.onDiffDecision != nil
			if resolved {
				m.onDiffDecision(DiffReject)
			}
			m.showPromptCollision("diff", "rejected", resolved)
		} else {
			// Emit a compact inline diff card into the chat stream as a
			// persistent record of what's being proposed, then open the
			// full-screen modal. The card stays in history after accept /
			// reject; the modal is the actual decision surface.
			if card := renderInlineDiffCard(m.width, msg); card != "" {
				for line := range strings.SplitSeq(card, "\n") {
					m.output.AppendLine("  " + line)
				}
				m.output.AppendLine("")
			}
			m.diffRequest = &msg
			m.diffPreview.SetSize(m.width, m.height)
			m.diffPreview.SetContent(msg.FilePath, msg.OldContent, msg.NewContent, msg.ToolName, msg.IsNewFile)
			m.state = StateDiffPreview
		}

	case MultiDiffPreviewRequestMsg:
		if m.isModalState() {
			resolved := m.onMultiDiffDecision != nil
			if resolved {
				rejected := make(map[string]DiffDecision, len(msg.Files))
				for _, f := range msg.Files {
					rejected[f.FilePath] = DiffReject
				}
				m.onMultiDiffDecision(rejected)
			}
			m.showPromptCollision("multi-file diff", "rejected", resolved)
		} else {
			m.multiDiffRequest = &msg
			m.multiDiffPreview.SetSize(m.width, m.height)
			m.multiDiffPreview.SetFiles(msg.Files)
			m.state = StateMultiDiffPreview
		}

	case DiffPreviewResponseMsg:
		if msg.Decision == DiffApply || msg.Decision == DiffApplyAll {
			if m.onDiffDecision == nil {
				m.diffPreview.MarkResponseUnavailable()
				m.state = StateDiffPreview
				if m.toastManager != nil {
					m.toastManager.ShowError("Diff decision handler is unavailable · press Esc to cancel")
				}
				break
			}
			m.diffRequest = nil
			m.state = StateProcessing
			m.onDiffDecision(msg.Decision)
		} else {
			m.diffRequest = nil
			m.state = StateInput
			m.output.AppendLine(m.styles.Warning.Render(" Changes rejected"))
			m.output.AppendLine("")
			if m.onDiffDecision != nil {
				m.onDiffDecision(msg.Decision)
			} else if m.onCancel != nil {
				m.onCancel()
			}
			cmds = append(cmds, m.input.Focus())
		}

	case MultiDiffPreviewResponseMsg:
		applied, rejected := 0, 0
		for _, decision := range msg.Decisions {
			if decision == DiffApply || decision == DiffApplyAll {
				applied++
			} else {
				rejected++
			}
		}
		if applied > 0 && m.onMultiDiffDecision == nil {
			m.multiDiffPreview.MarkResponseUnavailable()
			m.state = StateMultiDiffPreview
			if m.toastManager != nil {
				m.toastManager.ShowError("Diff decision handler is unavailable · press Esc to cancel")
			}
			break
		}
		m.multiDiffRequest = nil
		switch {
		case len(msg.Decisions) == 0:
			// Empty review is a close/no-op, not vacuous "all applied". The old
			// allApplied loop left this true and falsely returned to Processing.
			m.state = StateInput
			if m.toastManager != nil {
				m.toastManager.ShowInfo("No changes to review")
			}
			cmds = append(cmds, m.input.Focus())
		case applied > 0:
			// The workspace review contract preserves per-file decisions, so a
			// mixed result still has approved work to apply in the background.
			m.state = StateProcessing
			if rejected > 0 && m.toastManager != nil {
				m.toastManager.ShowInfo(fmt.Sprintf("%s approved · %s rejected", pluralCount(applied, "file", "files"), pluralCount(rejected, "file", "files")))
			}
		default:
			m.state = StateInput
			m.output.AppendLine(m.styles.Warning.Render(" All changes rejected"))
			m.output.AppendLine("")
			cmds = append(cmds, m.input.Focus())
		}
		if m.onMultiDiffDecision != nil {
			m.onMultiDiffDecision(msg.Decisions)
		} else if m.onCancel != nil {
			m.onCancel()
		}

	case SearchResultsRequestMsg:
		// Guard: don't clobber an active modal — this isn't a blocking call
		// (no decision channel to auto-resolve), so just drop it with a toast
		// rather than silently discarding whatever's currently showing.
		if m.isModalState() {
			query := safeKeyEntryText(msg.Query)
			action := "Search results were not opened"
			if query != "" {
				action = fmt.Sprintf("Search results for %q were not opened", truncateForWidth(query, 32))
			}
			m.showSurfaceCollision(action, "close it and run the search again")
		} else {
			m.workspaceOverlayReturnState = overlayReturnState(m.state)
			m.searchRequest = &msg
			m.searchResults.SetSize(m.width, m.height)
			m.searchResults.SetResults(msg.Query, msg.Tool, msg.Results)
			m.state = StateSearchResults
		}

	case SearchResultsActionMsg:
		if msg.Action == SearchActionClose {
			m.searchRequest = nil
			cmds = append(cmds, m.restoreWorkspaceOverlay())
			break
		}

		if msg.Action == SearchActionCopyPath {
			if msg.FilePath == "" {
				if m.toastManager != nil {
					m.toastManager.ShowWarning("No path is available to copy")
				}
				break
			}
			showClipboardCopyFeedback(m.toastManager, "Copied result path", copyTextToClipboard(msg.FilePath))
			m.dispatchSearchResultAction(msg)
			break
		}

		if !m.dispatchSearchResultAction(msg) {
			if m.toastManager != nil {
				m.toastManager.ShowWarning("Search result actions are unavailable in this session")
			}
			break
		}
		m.searchRequest = nil
		cmds = append(cmds, m.restoreWorkspaceOverlay())

	case GitStatusRequestMsg:
		if m.isModalState() {
			m.showSurfaceCollision("Git status update was not opened", "close it and reopen Git status")
		} else {
			m.workspaceOverlayReturnState = overlayReturnState(m.state)
			m.gitStatusRequest = &msg
			m.gitStatusModel.SetSize(m.width, m.height)
			m.gitStatusModel.SetStatus(msg.Entries, msg.Branch, msg.Upstream, msg.AheadBehind)
			m.state = StateGitStatus
		}

	case GitStatusActionMsg:
		if msg.Action == GitActionClose {
			m.gitStatusRequest = nil
			cmds = append(cmds, m.restoreWorkspaceOverlay())
			break
		}
		// Diff is an inline preview request, not a terminal action. Keep Git
		// Status open while the embedding application loads the selected file.
		if msg.Action == GitActionDiff {
			if !m.dispatchGitStatusAction(msg) {
				m.gitStatusModel.pendingDiffPath = ""
				m.gitStatusModel.SetDiff("Diff preview unavailable\nNo diff provider is connected")
				if m.toastManager != nil {
					m.toastManager.ShowWarning("Diff preview is unavailable in this session")
				}
			}
			break
		}
		if !m.dispatchGitStatusAction(msg) {
			if m.toastManager != nil {
				m.toastManager.ShowWarning("Git status actions are unavailable in this session")
			}
			break
		}
		m.gitStatusRequest = nil
		cmds = append(cmds, m.restoreWorkspaceOverlay())

	case GitStatusDiffMsg:
		// Selection changes may issue overlapping asynchronous requests. Only
		// the response for the currently loading path may update the preview.
		if m.state != StateGitStatus || !m.gitStatusModel.diffVisible() ||
			msg.FilePath == "" || msg.FilePath != m.gitStatusModel.pendingDiffPath {
			break
		}
		m.gitStatusModel.pendingDiffPath = ""
		if errText := safeKeyEntryText(msg.Error); errText != "" {
			m.gitStatusModel.SetDiffError("Unable to load diff · " + errText)
			if m.toastManager != nil {
				m.toastManager.ShowError("Unable to load diff: " + errText)
			}
			break
		}
		m.gitStatusModel.SetDiff(msg.Content)

	case FileBrowserRequestMsg:
		if m.isModalState() {
			m.showSurfaceCollision("File browser did not open", "close it and retry")
		} else {
			previousReturnState := m.workspaceOverlayReturnState
			m.workspaceOverlayReturnState = overlayReturnState(m.state)
			m.fileBrowser.SetSize(m.width, m.height)
			if err := m.fileBrowser.SetPath(msg.StartPath); err == nil {
				m.fileBrowserActive = true
				m.state = StateFileBrowser
			} else {
				m.workspaceOverlayReturnState = previousReturnState
				m.fileBrowserActive = false
				errorText := "Could not open file browser: " + safeKeyEntryText(err.Error())
				m.output.AppendLine(m.styles.FormatError(errorText))
				m.output.AppendLine("")
				if m.toastManager != nil {
					m.toastManager.ShowError(errorText)
				}
			}
		}

	case FileBrowserActionMsg:
		switch msg.Action {
		case FileBrowserActionClose:
			cmds = append(cmds, m.finishFileBrowser(nil))
		case FileBrowserActionOpen:
			cmds = append(cmds, m.finishFileBrowser([]string{msg.Path}))
		case FileBrowserActionSelect:
			cmds = append(cmds, m.finishFileBrowser(msg.Files))
		}

	case ProgressUpdateMsg:
		if !m.progressActive {
			m.progressModel.Start(m.progressModel.title, msg.Total)
			m.progressActive = true
		}
		m.progressModel, _ = m.progressModel.Update(msg)
		if m.state == StateInput || m.state == StateProcessing || m.state == StateStreaming {
			m.progressReturnState = m.state
			m.state = StateBatchProgress
		}

	case ProgressCompleteMsg:
		if !m.progressActive {
			m.progressModel.Start(m.progressModel.title, msg.TotalItems)
			m.progressActive = true
		}
		m.progressModel, _ = m.progressModel.Update(msg)
		if m.state == StateInput || m.state == StateProcessing || m.state == StateStreaming {
			m.progressReturnState = m.state
			m.state = StateBatchProgress
		}

	case CloseOverlayMsg:
		if m.state == StateBatchProgress {
			m.progressActive = false
			m.state = m.progressReturnState
			if m.state != StateProcessing && m.state != StateStreaming {
				m.state = StateInput
				cmds = append(cmds, m.input.Focus())
			}
		} else if m.progressActive {
			// A higher-priority modal may have kept batch progress in the
			// background. Reveal it after that overlay closes instead of losing
			// the only completion/error summary.
			m.progressReturnState = StateInput
			m.state = StateBatchProgress
		} else {
			m.state = StateInput
			cmds = append(cmds, m.input.Focus())
		}

	// Config update message - refresh UI state
	case ConfigUpdateMsg:
		m.permissionsEnabled = msg.PermissionsEnabled
		m.sandboxEnabled = msg.SandboxEnabled
		m.planningModeEnabled = msg.PlanningModeEnabled
		m.SetReducedMotion(msg.ReducedMotion)
		if msg.ModelName != "" {
			m.currentModel = msg.ModelName
			setTerminalTitle(fmt.Sprintf("Gokin · %s · %s", m.currentModel, shortenPath(m.workDir, 30)))
		}

	case ModelSelectResultMsg:
		requestedID := safeKeyEntryText(msg.RequestedID)
		// Only the active request owns this transition. A duplicated or late
		// result must not clear feedback for a newer switch.
		if requestedID == "" || requestedID != m.modelSwitchPending {
			break
		}
		m.modelSwitchPending = ""
		modelID := safeKeyEntryText(msg.ModelID)
		if modelID != "" {
			m.currentModel = modelID
			m.settingsModel = modelID
			setTerminalTitle(fmt.Sprintf("Gokin · %s · %s", modelID, shortenPath(m.workDir, 30)))
		}
		detail := safeKeyEntryText(msg.Message)
		confirmed := msg.Success && modelID != ""
		if confirmed {
			if detail == "" {
				detail = "Switched to " + modelID
			}
			if m.state == StateSettings {
				m.settingsNotice = detail
			}
			if m.state == StateModelSelector {
				m.modelSelectorNotice = detail
			}
			if m.toastManager != nil {
				m.toastManager.ShowTagged("model-switch", ToastSuccess, detail, 3*time.Second)
			}
			m.output.AppendLine(m.styles.Spinner.Render(detail))
			break
		}
		if detail == "" {
			if msg.Success {
				detail = "Couldn't confirm model switch · previous model remains active"
			} else {
				detail = "Couldn't switch model · previous model restored"
			}
		}
		if m.state == StateSettings {
			m.settingsNotice = detail
		}
		if m.state == StateModelSelector {
			m.modelSelectorNotice = detail
		}
		if m.toastManager != nil {
			m.toastManager.ShowTagged("model-switch", ToastError, detail, 15*time.Second)
		}
		m.output.AppendLine(lipgloss.NewStyle().Foreground(ColorWarning).Render("  ⚠ " + detail))

	// Open the interactive settings modal (/settings)
	case OpenSettingsMsg:
		// Guard: Ctrl+S/the /settings command dispatches this async from a
		// different goroutine than whatever else may be mid-modal (e.g. a
		// background /loop iteration's permission prompt). Opening
		// unconditionally would silently clobber an active
		// Permission/Question/PlanApproval/Diff prompt, orphaning its blocked
		// decision channel until timeout/ctx cancellation.
		if m.isModalState() {
			m.showSurfaceCollision("Settings did not open", "")
		} else {
			m.openSettings(msg)
		}

	case SettingToggleResultMsg:
		key := strings.ToLower(strings.TrimSpace(msg.Key))
		delete(m.settingsPending, key)
		name := key
		live := true
		found := false
		for i := range m.settingsItems {
			if m.settingsItems[i].Key != key {
				continue
			}
			m.settingsItems[i].On = msg.On
			m.settingsItems[i].Pending = false
			name = m.settingsItems[i].Name
			live = m.settingsItems[i].Live
			found = true
			break
		}
		if name = safeKeyEntryText(name); name == "" {
			name = "Setting"
		}
		detail := safeKeyEntryText(msg.Message)
		if msg.Success {
			feedback := fmt.Sprintf("%s: %s", name, onOffLabel(msg.On))
			if !live {
				feedback += " · restart required"
			}
			if m.state == StateSettings && found {
				m.settingsNotice = feedback
			} else if m.toastManager != nil {
				m.toastManager.ShowTagged("setting-"+key, ToastSuccess, feedback, 3*time.Second)
			}
			break
		}
		if detail == "" {
			detail = "Couldn't apply setting — restored previous value"
		}
		feedback := fmt.Sprintf("%s: couldn't apply — restored %s", name, onOffLabel(msg.On))
		if m.state == StateSettings && found {
			m.settingsNotice = feedback
		}
		if m.toastManager != nil {
			m.toastManager.ShowTagged("setting-"+key, ToastError, detail, 15*time.Second)
		}
		m.output.AppendLine(lipgloss.NewStyle().Foreground(ColorWarning).Render("  ⚠ " + detail))

	// Open the masked API-key entry modal (/login <provider> with no key)
	case OpenKeyEntryMsg:
		if m.isModalState() {
			m.showSurfaceCollision("API key entry did not open", "")
		} else {
			cmds = append(cmds, m.openKeyEntry(msg))
		}

	case KeyEntryResultMsg:
		provider := safeKeyEntryProvider(msg.Provider)
		if provider == "" || provider != m.keyEntryPendingProvider {
			break
		}
		displayName := m.keyEntryPendingDisplayName
		setupURL := m.keyEntryPendingSetupURL
		m.keyEntryPendingProvider = ""
		m.keyEntryPendingDisplayName = ""
		m.keyEntryPendingSetupURL = ""
		detail := safeKeyEntryText(msg.Message)
		if msg.Success {
			if detail == "" {
				detail = displayName + " API key saved"
			}
			if m.toastManager != nil {
				toastType := ToastSuccess
				duration := 3 * time.Second
				if msg.Warning {
					toastType = ToastWarning
					duration = 8 * time.Second
				}
				m.toastManager.ShowTagged("login-"+provider, toastType, detail, duration)
			}
			break
		}
		if detail == "" {
			detail = "Login failed · check the key and try again"
		}
		if m.toastManager != nil {
			m.toastManager.ShowTagged("login-"+provider, ToastError, detail, 15*time.Second)
		}
		m.output.AppendLine(lipgloss.NewStyle().Foreground(ColorWarning).Render("  ⚠ " + detail))
		// Reopen only when it cannot clobber work or another decision modal. The
		// secret was already destroyed; the retry field is intentionally empty.
		if m.state == StateInput {
			cmds = append(cmds, m.openKeyEntry(OpenKeyEntryMsg{
				Provider: provider, DisplayName: displayName, SetupURL: setupURL,
			}))
			m.keyEntryError = detail
		}

	// Planning mode toggle result (async)
	case PlanningModeToggledMsg:
		m.planningModeEnabled = msg.Enabled
		if msg.Enabled {
			m.output.AppendLine(m.styles.Spinner.Render("Planning mode enabled — complex tasks will be broken into steps"))
		} else {
			m.output.AppendLine(m.styles.Dim.Render("Planning mode disabled — direct execution"))
		}
		m.output.AppendLine("")

	// Session-mode cycle result (Shift+Tab). Emitted from
	// App.CycleSessionModeAsync after the cascaded toggles complete.
	// Renders a single toast describing the new mode so users get
	// one-tap feedback instead of three scattered status lines.
	case SessionModeCycledMsg:
		m.sessionMode = msg.Mode
		// Mirror the individual flags from the canonical mode so the
		// status bar can render immediately without waiting for the
		// three ConfigUpdateMsg that applySessionMode triggers.
		switch msg.Mode {
		case "plan":
			m.planningModeEnabled = true
			m.permissionsEnabled = true
			m.sandboxEnabled = true
			m.output.AppendLine(m.styles.Spinner.Render(
				"✓ Plan mode — agent explores read-only, then proposes a plan for approval. Shift+Tab → YOLO."))
		case "yolo":
			m.planningModeEnabled = false
			m.permissionsEnabled = false
			m.sandboxEnabled = false
			m.output.AppendLine(m.styles.Warning.Render(
				"⚠ YOLO mode — permissions OFF, sandbox OFF. Agent runs every command without asking. Shift+Tab → Normal."))
		default: // "normal"
			m.planningModeEnabled = false
			m.permissionsEnabled = true
			m.sandboxEnabled = true
			m.output.AppendLine(m.styles.Dim.Render(
				"✓ Normal mode — agent asks before write/edit/bash. Shift+Tab → Plan."))
		}
		m.output.AppendLine("")

	case TaskStartedEvent:
		m.handleTaskStarted(msg)
	case TaskCompletedEvent:
		if cmd := m.handleTaskCompleted(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case TaskProgressEvent:
		m.handleTaskProgress(msg)

	case CoordinatedTaskCleanupMsg:
		m.removeCoordinatedTask(msg.ID)

	// Background task tracking (agent start/complete, sent from builder.go).
	// handleBackgroundTask carries Status across the full lifecycle
	// (running/completed/failed/cancelled) — do NOT narrow this to a
	// start-only message, completion handling (bell, untrack, failure
	// toast) lives here too.
	case BackgroundTaskMsg:
		cmds = append(cmds, m.handleBackgroundTask(msg))
	case BackgroundTaskProgressMsg:
		if task, ok := m.backgroundTasks[msg.ID]; ok {
			task.Progress = msg.Progress
			task.CurrentStep = msg.CurrentStep
			task.TotalSteps = msg.TotalSteps
			task.CurrentAction = msg.CurrentAction
			task.ToolsUsed = msg.ToolsUsed
		}
		// Also forward to activity feed for sub-agent progress
		if m.activityFeed != nil {
			m.activityFeed.UpdateSubAgentProgress(msg.ID, msg.Progress, msg.CurrentStep, msg.TotalSteps)
		}

	// Sub-agent activity tracking
	case SubAgentActivityMsg:
		// Sub-agent lines land in scrollback — flush buffered foreground tool
		// lines first so the aggregate stays in chronological position.
		m.flushPendingToolLines()
		// Agent activity shown inline with dim prefix showing agent type.
		// Background agents are async — their events interleave with other output,
		// so we use a flat prefix format instead of tree connectors.

		switch msg.Status {
		case "start":
			m.processingLabel = fmt.Sprintf("Agent: %s", msg.AgentType)
			m.streamStartTime = time.Now()
			m.lastActivityTime = time.Now()
			m.agentToolCount = 0
			m.agentRecentTools = nil
			// Surface WHAT the agent was dispatched to do. Prefer the real task
			// (msg.Task carries the dispatch prompt on start) so foreground
			// sub-agents — which aren't tracked in backgroundTasks — no longer
			// show a bare "Agent started" with no context. Fall back to the
			// tracked background-task description when no prompt was captured.
			description := ""
			if task, ok := m.backgroundTasks[msg.AgentID]; ok {
				description = task.Description
			}
			if strings.TrimSpace(msg.Task) != "" {
				description = summarizeSubAgentTask(msg.Task, msg.AgentType)
			}
			m.output.AppendLine("")
			m.output.AppendLine(m.renderAgentTimelineStart(msg.AgentType, description))
		case "tool_start":
			m.agentToolCount++
			info := m.extractToolInfoFromArgs(msg.ToolName, msg.ToolArgs)

			// Track recent tools for inline activity display
			toolSummary := msg.ToolName
			if info != "" {
				toolSummary += " " + info
			}
			m.agentRecentTools = append(m.agentRecentTools, toolSummary)
			if len(m.agentRecentTools) > 5 {
				m.agentRecentTools = m.agentRecentTools[len(m.agentRecentTools)-5:]
			}

			if info != "" {
				m.processingLabel = fmt.Sprintf("Agent: %s → %s %s", msg.AgentType, msg.ToolName, info)
			} else {
				m.processingLabel = fmt.Sprintf("Agent: %s → %s", msg.AgentType, msg.ToolName)
			}
			m.currentTool = msg.ToolName
			m.currentToolInfo = info
			m.toolStartTime = time.Now()
			m.lastActivityTime = time.Now()
			// No scrollback row on tool START — the live activity card / status
			// bar show the running tool. The merged line (with the OUTCOME) is
			// emitted on tool_end, matching the foreground calm one-line style.
		case "tool_recovery":
			msgTxt := msg.ToolName
			if msg.ToolArgs != nil {
				if r, ok := msg.ToolArgs["reason"].(string); ok {
					msgTxt = r
				}
			}
			m.processingLabel = fmt.Sprintf("Recovery: %s → %s", msg.AgentType, msg.ToolName)
			m.output.AppendLine(m.styles.Warning.Render(fmt.Sprintf("    ↻ Auto-Fixing: %s Error (%s)", msg.ToolName, msgTxt)))
			m.lastActivityTime = time.Now()
		case "tool_end":
			// Emit the merged one-line tool result with its OUTCOME (✓/✗ +
			// summary) — this is the "meaningful output" a sub-agent's tool was
			// missing; previously tool_end rendered nothing in scrollback.
			info := m.extractToolInfoFromArgs(msg.ToolName, msg.ToolArgs)
			target := conciseToolTarget(msg.ToolName, info)
			m.output.AppendLine(m.styles.FormatAgentToolLine(msg.AgentType, msg.ToolName, target, msg.Summary, msg.Success))
			// Between tool calls, show "thinking" with tool count so user knows agent is alive
			m.processingLabel = fmt.Sprintf("Agent: %s · thinking (%d tools used)", msg.AgentType, m.agentToolCount)
			m.currentTool = ""
			m.currentToolInfo = ""
			m.lastActivityTime = time.Now()
		case "complete":
			m.processingLabel = ""
			m.currentTool = ""
			m.currentToolInfo = ""
			// Round 7: agentRecentTools/agentToolCount were previously reset
			// ONLY on the next SubAgentActivityMsg{"start"} — a finished
			// sub-agent's tool trail lingered (readable at the fallback
			// processing-indicator block below) until then, which could be
			// much later or in an entirely UNRELATED turn that spawns no
			// sub-agent at all. Clear here too, symmetric with "start".
			m.agentToolCount = 0
			m.agentRecentTools = nil
			var elapsed time.Duration
			if m.activityFeed != nil {
				if state := m.activityFeed.GetSubAgentState(msg.AgentID); state != nil {
					elapsed = time.Since(state.StartTime)
				}
			}
			m.output.AppendLine(m.styles.FormatAgentComplete(msg.AgentType, elapsed))
		case "failed":
			m.processingLabel = ""
			m.currentTool = ""
			m.currentToolInfo = ""
			m.agentToolCount = 0
			m.agentRecentTools = nil
			var elapsed time.Duration
			if m.activityFeed != nil {
				if state := m.activityFeed.GetSubAgentState(msg.AgentID); state != nil {
					elapsed = time.Since(state.StartTime)
				}
			}
			m.output.AppendLine(m.styles.FormatAgentFailed(msg.AgentType, elapsed))
		}

		// Activity feed panel (Ctrl+O)
		if m.activityFeed != nil {
			switch msg.Status {
			case "start":
				// Prefer the real task (dispatch prompt) over the generic
				// "Sub-agent: <type>" so users can see what each agent is
				// actually working on at a glance. Fall back to the type
				// when no prompt was captured (shouldn't happen, but the
				// UI must never show an empty row).
				desc := summarizeSubAgentTask(msg.Task, msg.AgentType)
				m.activityFeed.StartSubAgent(msg.AgentID, msg.AgentType, desc)
			case "tool_start":
				m.activityFeed.UpdateSubAgentTool(msg.AgentID, msg.ToolName, msg.ToolArgs)
			case "tool_end":
				m.activityFeed.UpdateSubAgentTool(msg.AgentID, "", nil)
			case "complete":
				m.activityFeed.CompleteSubAgent(msg.AgentID, true, "")
			case "failed":
				m.activityFeed.CompleteSubAgent(msg.AgentID, false, "")
			}
		}

	// Agent tree updates from Coordinator
	case AgentTreeUpdateMsg:
		if m.agentTreePanel != nil {
			m.agentTreePanel.UpdateTree(msg.Nodes)
		}

	// Learning insight notification
	case LearningInsightMsg:
		if m.toastManager != nil && msg.Message != "" {
			m.toastManager.Show(ToastInfo, "", msg.Message, 4*time.Second)
		}

	// Status updates from client (retry, rate limit, stream idle)
	case StatusUpdateMsg:
		// Status payloads can originate in providers, tools, hooks, and remote
		// servers. Normalize once before any value reaches a persistent live
		// label: toast rendering already collapsed whitespace, but the raw copy
		// in streamIdleMsg/processingLabel could inject rows into the activity
		// card and destabilize the frame after the toast disappeared.
		statusText := safeKeyEntryText(msg.Message)
		// Ring the bell for events a stepped-away user should HEAR (e.g. a
		// background /loop reaching a terminal state). Gated on bellEnabled
		// inside bellCmd; same audible signal background tasks use.
		if msg.Bell {
			cmds = append(cmds, m.bellCmd())
		}
		switch msg.Type {
		case StatusRetry:
			if attempt, ok := msg.Details["attempt"].(int); ok {
				m.retryAttempt = attempt
			}
			if maxAttempts, ok := msg.Details["maxAttempts"].(int); ok {
				m.retryMax = maxAttempts
			}
			if m.toastManager != nil && statusText != "" {
				// Collapse consecutive retries into a single live toast so rapid
				// attempts don't stack up and push other toasts off screen.
				m.toastManager.ShowTagged("retry", ToastWarning, statusText, 4*time.Second)
			}
		case StatusRateLimit:
			if statusText == "" {
				statusText = "Rate limit reached"
			}
			if m.toastManager != nil {
				m.toastManager.ShowWarning(statusText)
			}
			if wt, ok := msg.Details["waitTime"].(time.Duration); ok {
				m.rateLimitWaitUntil = time.Now().Add(wt)
			}
		case StatusStreamIdle:
			firstWarning := m.streamIdleMsg == ""
			if statusText != "" {
				m.streamIdleMsg = statusText
			}
			if firstWarning && m.toastManager != nil && statusText != "" {
				m.toastManager.ShowWarning(statusText)
			}
		case StatusStreamResume:
			m.streamIdleMsg = ""
			m.retryAttempt = 0
			m.retryMax = 0
			m.rateLimitWaitUntil = time.Time{}
			m.lastRecoverableStatus = ""
		case StatusRecoverableError:
			if statusText == "" {
				statusText = "Recoverable error"
			}
			if m.toastManager != nil {
				// Use the hint-enhanced variant so the toast tells the user
				// what to try next, not just what went wrong.
				m.toastManager.ShowErrorWithHint(statusText)
			}
			// Recoverable failures used to exist only in an expiring toast. Toast
			// history has no user-facing surface, so a stepped-away user could not
			// recover the reason or suggested action. Preserve one compact line per
			// distinct recovery episode in scrollback; identical retry callbacks
			// still refresh the toast without spamming durable output.
			if statusText != m.lastRecoverableStatus {
				durable := statusText
				if hint := GetCompactHint(statusText); hint != "" {
					durable += " → " + hint
				}
				m.output.AppendLine(lipgloss.NewStyle().Foreground(ColorWarning).Render("  ⚠ " + durable))
				m.lastRecoverableStatus = statusText
			}
		case StatusCancelled:
			// Cancellation: clear any in-flight idle/thinking hints so the user
			// sees a clean state while the operation winds down.
			m.streamIdleMsg = ""
			m.currentTool = ""
			m.currentToolInfo = ""
			if statusText == "" {
				statusText = "Operation cancelled"
			}
			m.settleActiveToolCalls(false, statusText)
			if m.toastManager != nil {
				m.toastManager.ShowWarning(statusText)
			}
		case StatusWarning:
			if m.toastManager != nil && statusText != "" {
				if tag, _ := msg.Details["tag"].(string); tag != "" {
					m.toastManager.ShowTagged(tag, ToastWarning, statusText, 4*time.Second)
				} else {
					m.toastManager.ShowWarning(statusText)
				}
			}
		case StatusInfo:
			if label, _ := msg.Details["phaseLabel"].(string); safeKeyEntryText(label) != "" {
				m.processingLabel = safeKeyEntryText(label)
				m.state = StateProcessing
				m.streamStartTime = time.Now()
				m.lastActivityTime = time.Now()
				m.slowWarningShown = false
				m.streamIdleMsg = ""
			}
			silent, _ := msg.Details["silent"].(bool)
			if !silent && m.toastManager != nil && statusText != "" {
				m.toastManager.Show(ToastInfo, "", statusText, 4*time.Second)
			}
		case StatusThinkingIdle:
			// Model is deliberately thinking — show distinct from generic "idle"
			firstWarning := m.streamIdleMsg == ""
			if statusText != "" {
				m.streamIdleMsg = statusText
			}
			if firstWarning && m.toastManager != nil && statusText != "" {
				m.toastManager.Show(ToastInfo, "", statusText, 6*time.Second)
			}
		}
	}

	if len(cmds) > 0 {
		return tea.Batch(cmds...)
	}
	return nil
}

// extractToolInfoFromArgs generates tool info for status line display.
func (m *Model) extractToolInfoFromArgs(name string, args map[string]any) string {
	// Use larger limits to show more of the file path (important for clarity)
	const pathLimit = 80      // Increased from 40 for better visibility
	const pathLimitShort = 60 // For cases with additional info

	switch name {
	case "read":
		if path, ok := args["file_path"].(string); ok {
			return shortenPath(path, pathLimit)
		}
	case "write":
		if path, ok := args["file_path"].(string); ok {
			info := shortenPath(path, pathLimitShort)
			if content, ok := args["content"].(string); ok {
				lines := len(strings.Split(content, "\n"))
				info += fmt.Sprintf(" (%d lines)", lines)
			}
			return info
		}
	case "edit":
		if path, ok := args["file_path"].(string); ok {
			return shortenPath(path, pathLimit)
		}
	case "copy", "move":
		return formatPathPair(toolStringArg(args, "source"), toolStringArg(args, "destination"), pathLimit)
	case "delete", "mkdir":
		if path := toolStringArg(args, "path"); path != "" {
			return shortenPath(path, pathLimit)
		}
	case "run_tests":
		return formatRunTestsTarget(args, pathLimitShort)
	case "verify_code":
		path := toolStringArg(args, "path")
		if path == "" {
			path = "."
		}
		return shortenPath(path, pathLimit)
	case "git_status":
		path := toolStringArg(args, "path")
		if path == "" {
			path = "."
		}
		info := shortenPath(path, pathLimitShort)
		if toolBoolArg(args, "short") {
			info += " --short"
		}
		return info
	case "git_diff":
		return formatGitDiffTarget(args, pathLimitShort)
	case "git_add":
		return formatGitAddTarget(args, pathLimitShort)
	case "git_branch":
		info := strings.TrimSpace(toolStringArg(args, "action") + " " + toolStringArg(args, "name"))
		if info != "" {
			return info
		}
	case "git_log":
		if file := toolStringArg(args, "file"); file != "" {
			return shortenPath(file, pathLimitShort)
		}
		if grep := toolStringArg(args, "grep"); grep != "" {
			return "grep=" + compactInline(grep, 36)
		}
		if author := toolStringArg(args, "author"); author != "" {
			return "author=" + compactInline(author, 36)
		}
		if count := toolIntArg(args, "count"); count > 0 {
			return fmt.Sprintf("%d commits", count)
		}
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			preview := cmd
			if runes := []rune(preview); len(runes) > 60 {
				preview = string(runes[:57]) + "..."
			}
			preview = strings.ReplaceAll(preview, "\n", " ")
			return "$ " + preview
		}
	case "grep":
		if pattern, ok := args["pattern"].(string); ok {
			info := pattern
			if runes := []rune(info); len(runes) > 30 {
				info = string(runes[:27]) + "..."
			}
			if path, ok := args["path"].(string); ok {
				info += " in " + shortenPath(path, 40)
			}
			return info
		}
	case "glob":
		if pattern, ok := args["pattern"].(string); ok {
			return pattern
		}
	case "list_dir", "tree":
		path := "."
		if p, ok := args["directory_path"].(string); ok && p != "" {
			path = p
		}
		return shortenPath(path, pathLimit)
	case "web_fetch":
		if url, ok := args["url"].(string); ok {
			return shortenPath(url, pathLimit)
		}
	case "web_search":
		if query, ok := args["query"].(string); ok {
			return query
		}
	}

	// Fallback: use extractToolInfo
	if len(args) > 0 {
		return extractToolInfo(args)
	}
	return ""
}

// collapsedByDefault reports whether a tool's successful output should
// skip the inline head+tail preview and show only a summary line + expand
// hint. For these tools the ✓ success line itself (with the operation
// payload — path for read, command for bash) carries all the immediately
// useful signal; the actual content body is either redundant (Read —
// the file is on disk) or accessible via the `e` expand shortcut.
//
// grep / glob deliberately stay uncollapsed: their output IS the user's
// primary signal (matching lines, found paths) and a head+tail preview
// is worth the rows.
// toggleStateLabel builds a clear "X shown"/"X hidden" toast message so panel
// toggles (Ctrl+O/A/T, compact mode) give the user visible confirmation instead
// of silently doing nothing-looking.
func toggleStateLabel(name string, on bool) string {
	if on {
		return name + " shown"
	}
	return name + " hidden"
}

// toggleTodosPanel flips the todo panel and emits the HONEST toast — shared by
// the Ctrl+T key handler and the palette "Toggle Task List" action so the two
// entry points can't drift (the exact drift the round-of-Ctrl+O hunt caught:
// the key got the empty-state label, the palette kept claiming "shown" while
// nothing rendered).
func (m *Model) toggleTodosPanel() {
	m.todosVisible = !m.todosVisible
	// When opening the panel, sweep completed tasks older than 5s.
	if m.todosVisible {
		m.cleanupStaleCoordinatedTasks()
	}
	label := toggleStateLabel("Todos", m.todosVisible)
	if m.todosVisible && len(m.todoItems) == 0 {
		// The todos block renders only when items exist — don't claim "shown"
		// for an empty list. The toggle still arms the panel: it appears with
		// the agent's first todo update.
		label = "Todos shown — appears when the agent creates tasks"
	}
	if m.toastManager != nil {
		m.toastManager.ShowInfo(label)
	}
}

// toggleAgentTreePanel flips the agent tree panel with the honest toast —
// shared by the Ctrl+A key handler and the palette "Toggle Agent Tree" action
// (same anti-drift rule as toggleTodosPanel).
func (m *Model) toggleAgentTreePanel() {
	if m.agentTreePanel == nil {
		return
	}
	m.agentTreePanel.Toggle()
	label := toggleStateLabel("Agent tree", m.agentTreePanel.IsVisible())
	if m.agentTreePanel.IsVisible() && m.agentTreePanel.NodeCount() == 0 {
		// With zero nodes the panel View renders "" even when visible — say
		// so instead of claiming it's on screen. The toggle still arms the
		// panel: it appears the moment sub-agents start reporting.
		label = "Agent tree shown — appears when sub-agents run"
	}
	if m.toastManager != nil {
		m.toastManager.ShowInfo(label)
	}
}

func collapsedByDefault(toolName string) bool {
	switch toolName {
	case "read", "bash":
		return true
	case "todo":
		// The live checklist widget (emitTodoUpdate) already shows current
		// state; re-printing the full list inline on every todo call is noise.
		return true
	}
	return false
}

// handleToolResultWithInfo handles tool result message with clean, minimal formatting.
// It uses the provided tool name, info, and start time from the matched active call
// rather than relying on m.currentTool/m.toolStartTime (which may have been overwritten
// by later parallel tool calls).
func (m *Model) handleToolResultWithInfo(content, toolName, toolInfo string, startTime time.Time) {
	m.handleToolResultWithStatus(content, toolName, toolInfo, startTime, false, "")
}

func (m *Model) handleToolResultWithStatus(content, toolName, toolInfo string, startTime time.Time, failed bool, errText string) {
	contentStyle := lipgloss.NewStyle().Foreground(ColorMuted)

	// Build the one-line subject (parenthesized target) + outcome + duration.
	// The target/outcome are split so neither is repeated: the call subject sits
	// in parens on the SAME row as its outcome.
	target := conciseToolTarget(toolName, toolInfo)
	outcome := toolOutcomeSummary(toolName, content)
	var duration time.Duration
	if !startTime.IsZero() {
		duration = time.Since(startTime)
	}

	name := toolName
	if name == "" {
		name = "tool"
	}

	// Dedup-stub path: when the read tool's content has been replaced with
	// the "[Unchanged since turn N · X chars · path]" marker, the file
	// hasn't changed since a prior read on this turn. Render a single dim
	// italic line — no success-block, no card. The model still sees the
	// marker via the tool-result stream; the user gets a quiet "we already
	// have this" note instead of a full row.
	if !failed && toolName == "read" && strings.HasPrefix(strings.TrimSpace(content), "[Unchanged since turn ") {
		m.flushPendingToolLines()
		// Show only the concise "[Unchanged …]" marker, not the model-facing
		// reuse guidance the stub appends after the closing bracket — the user
		// just needs the quiet "we already have this" note.
		marker := strings.TrimSpace(content)
		if i := strings.IndexByte(marker, ']'); i >= 0 {
			marker = marker[:i+1]
		}
		stubStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
		m.output.AppendLine("    " + stubStyle.Render("⎿ "+marker))
		m.output.AppendLine("")
		return
	}

	if failed {
		detail := strings.TrimSpace(errText)
		if detail == "" {
			detail = strings.TrimSpace(content)
		}
		if detail == "" {
			detail = "tool failed"
		}
		// Flat failure line matching the success one-liner — `▪ ✗ Name(target)`
		// with the error nested under the ⎿ corner — instead of a rounded red
		// box. Pass and fail now share one calm shape.
		m.flushPendingToolLines()
		titleLine := m.styles.FormatToolFailureLine(name, conciseToolTarget(toolName, toolInfo), duration)
		m.emitToolResultCard(titleLine, failureBody(detail, toolName))
		return
	}

	titleLine := m.styles.FormatToolLine(name, target, outcome, duration)

	// Edit display-diff (Claude-Code style): "Added 9 lines, removed 2
	// lines" + red/green ±hunks, computed by the edit tool from the real
	// old/new contents. Replaces the generic body for the USER; the model
	// still receives the v0.88 "Updated region" snippet via Content.
	if d := m.pendingEditDiff; d != nil {
		m.pendingEditDiff = nil
		m.flushPendingToolLines()
		m.emitToolResultCard(titleLine, renderEditDiffBody(d))
		return
	}

	// Body (expand-state aware) — built once, fed either into a bordered
	// card or the legacy 4-space-indented flat form depending on width.
	// buildToolResultBody ALSO stores the full output in the expand store,
	// so a buffered (aggregated) line's details remain reachable.
	body := m.buildToolResultBody(toolName, toolInfo, content, contentStyle)

	// Title-only read/bash completions aggregate ("Read 2 files, ran 3 shell
	// commands") instead of stacking near-identical rows; anything with a
	// visible body flushes the buffer first so ordering is preserved.
	if body == "" && aggregatableToolLine(toolName) {
		m.bufferToolLine(name, target, outcome, duration)
		return
	}
	m.flushPendingToolLines()

	m.emitToolResultCard(titleLine, body)
}

// buildToolResultBody returns the rendered body of a tool result (preview
// or full content), with every line pre-styled so callers can emit the
// result verbatim. Returns "" when there's nothing to show — collapsed-
// by-default tools (e.g. Read) deliberately render silently so the card
// stays at one row; the `e` keyboard shortcut to expand is documented in
// the shortcuts overlay and the contextual status-bar hint.
func (m *Model) buildToolResultBody(
	toolName, toolInfo, content string,
	contentStyle lipgloss.Style,
) string {
	if content == "" {
		return ""
	}

	// Store for expand/collapse and decide if we should collapse-by-default.
	expanded := false
	needsTruncation := false
	compactLongOutput := false
	if m.toolOutput != nil {
		m.lastToolOutputIndex = m.toolOutput.AddEntry(toolName, content)
		expanded = m.toolOutput.IsExpanded(m.lastToolOutputIndex)
		needsTruncation = m.toolOutput.NeedsTruncation(content)
		// Read results collapse by default — the success line already
		// carries the line count + path + duration. Other tools keep the
		// head+tail preview because their output IS the signal. Global
		// compact mode (E / Ctrl+E) forces collapse for all.
		compactLongOutput = needsTruncation && !expanded &&
			(m.toolOutput.CompactModeActive() || collapsedByDefault(toolName))
	}

	if compactLongOutput {
		// Silent collapse — title line carries the success signal; user
		// already knows about `e` from the shortcuts overlay.
		return ""
	}

	// Short output (below the collapse threshold) renders in full — truncating a
	// 9-line result to a 6-line preview with "⋯ N more" is just noise. The
	// genuinely-long, non-collapsed case still gets the head+tail preview.
	display := FormatToolOutput(content, 6, expanded || !needsTruncation)

	// Apply syntax highlighting for read tool AFTER preview/full selection.
	highlighted := false
	if toolName == "read" && toolInfo != "" && m.highlighter != nil {
		lang := m.highlighter.DetectLanguage(toolInfo)
		if lang != "" && lang != "text" {
			display = m.highlighter.Highlight(display, lang)
			highlighted = true
		}
	}

	// Re-style each non-empty line with contentStyle when not highlighted.
	// Empty AND visually-blank (ANSI-only) lines drop out so the card
	// doesn't carry phantom blank rows from chroma's reset sequences
	// around source blank lines.
	var b strings.Builder
	first := true
	for line := range strings.SplitSeq(display, "\n") {
		if isVisuallyBlank(line) {
			continue
		}
		if !first {
			b.WriteString("\n")
		}
		first = false
		if highlighted {
			b.WriteString(line)
		} else {
			b.WriteString(contentStyle.Render(line))
		}
	}
	return b.String()
}

// emitToolResultCard writes the success line and body to the chat
// viewport as flat 4-space-indented lines — same weight as any other
// agent output.
//
// The rounded-border version (Gokin Classic scene B) was removed: in
// the codex-style flow (read/bash collapsed by default, just titles)
// the border framed mostly empty space, and even when bodies showed
// the chrome competed with the diff card (scene C) and error card
// (scene D) for "modal" status. Tool success is the *quiet* surface —
// it doesn't need the visual segmentation that diff approval or
// failure cards do. Higher-stakes events still get borders; routine
// success blends with the rest of the agent's output.
func (m *Model) emitToolResultCard(titleLine, body string) {
	// One merged line per tool: `▪ Name(target) · outcome`. Any expandable body
	// (non-collapsed tools) nests beneath it under the ⎿ corner. Collapsed tools
	// (read/bash/todo) have no body → just the single row.
	m.output.AppendLine("  " + titleLine)
	if body != "" {
		first := true
		for line := range strings.SplitSeq(body, "\n") {
			if first {
				m.output.AppendLine("    ⎿ " + line)
				first = false
			} else {
				m.output.AppendLine("      " + line)
			}
		}
	}
	m.output.AppendLine("")
}

func firstNonEmptyLine(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func (m *Model) settleActiveToolCalls(success bool, summary string) {
	if len(m.activeToolCalls) == 0 {
		return
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		if success {
			summary = "completed"
		} else {
			summary = "stopped before result"
		}
	}
	if m.activityFeed != nil {
		for _, tc := range m.activeToolCalls {
			m.activityFeed.CompleteEntry(tc.activityID, success, summary)
		}
	}
	m.activeToolCalls = nil
}

func (m *Model) findActiveToolCall(name string, args map[string]any) int {
	if len(args) > 0 {
		for i, tc := range m.activeToolCalls {
			if tc.name == name && reflect.DeepEqual(tc.args, args) {
				return i
			}
		}
	}
	for i, tc := range m.activeToolCalls {
		if tc.name == name {
			return i
		}
	}
	return -1
}

// View renders the TUI.
func (m Model) View() string {
	var builder strings.Builder
	const outputMarker = "\x00gokin-main-output\x00"

	// Toast notifications are top-aligned on normal screens. On short panes the
	// final compositor keeps the bottom rows, so top-aligned notifications were
	// precisely what got clipped; defer the severity-limited view near the
	// status bar there. The active manager still retains every notification.
	toastView := ""
	deferToasts := false
	if m.state != StateNotificationCenter && m.toastManager != nil && m.toastManager.Count() > 0 {
		toastView = m.toastManager.ViewLimit(m.width, toastVisibleLineLimit(m.height))
		deferToasts = m.height > 0 && m.height <= 12
		if toastView != "" && !deferToasts {
			builder.WriteString(toastView)
			builder.WriteString("\n")
		}
	}

	// The output viewport height is resolved after all dynamic footer panels
	// have rendered. A marker lets the final compositor measure those panels
	// first, then replace this slot with a viewport sized to the remaining rows.
	isModal := m.isModalState()
	builder.WriteString(outputMarker)
	builder.WriteString("\n")

	// Welcome panel (mockup scene A) when output is empty and user is at
	// input. Replaces the prior single-line dim hint with a structured
	// idle screen: wordmark, version, tips, project info, mode hint.
	if m.output.IsEmpty() && m.state == StateInput && !deferToasts {
		builder.WriteString(m.renderWelcomePanel())
		builder.WriteString("\n")
	}

	// Background panels (dimmed when modal is active)
	var panelBuilder strings.Builder

	// Resolve the agent tree FIRST: when it renders, the activity feed panel
	// is suppressed for this frame — both auto-show on sub-agent activity
	// and the tree is the richer surface for the same information. Stacking
	// them showed every background agent twice. The live card needs to know
	// whether the feed ACTUALLY renders (not just IsVisible) so its
	// Recent/Next lines come back whenever the feed is suppressed.
	agentTreeView := ""
	if m.agentTreePanel != nil {
		agentTreeView = m.agentTreePanel.View(m.width)
	}
	// liveDetailExpanded gates the feed: in minimal mode (default) the whole
	// live-activity surface is one dim line — the feed only renders once the
	// user expands with Ctrl+O. Deliberately NOT gated on HasActiveEntries:
	// the expand must open the panel deterministically (Claude-Code style),
	// and the panel itself renders recent completed activity or an honest
	// "No activity yet" placeholder when nothing is running — a toggle that
	// only works while entries happen to be active reads as broken. IsVisible
	// stays in the gate so the palette's feed-only toggle can still hide the
	// feed WITHIN detailed mode.
	feedRendered := m.liveDetailExpanded && m.activityFeed != nil && m.activityFeed.IsVisible() &&
		agentTreeView == ""

	// Live "Now" card — compact summary of what the agent is doing right now.
	// The card's content overlaps with the processing/tool spinner-status
	// block rendered further below (same spinner, same label, same tool
	// info), so we track whether the card actually drew anything and
	// suppress the duplicate block in that case.
	cardRendered := false
	if card := m.renderLiveActivityCard(feedRendered); card != "" {
		panelBuilder.WriteString(card)
		panelBuilder.WriteString("\n")
		cardRendered = true
	}

	// Scratchpad panel
	if m.scratchpad != "" {
		panelBuilder.WriteString(m.renderScratchpad())
		panelBuilder.WriteString("\n")
	}

	// Plan progress panel (when actively executing a plan). Resolve it once so
	// the pause fallback below can avoid repeating the same step and reason in
	// a second bordered panel.
	planProgressView := ""
	if m.planProgressPanel != nil && m.planProgressPanel.IsVisible() {
		planProgressView = m.planProgressPanel.View(m.width, m.height)
	}
	if planProgressView != "" {
		panelBuilder.WriteString(planProgressView)
		panelBuilder.WriteString("\n")
	}

	if m.planPauseNotice != nil && planProgressView == "" {
		panelBuilder.WriteString(m.renderPlanPauseBlock(*m.planPauseNotice))
		panelBuilder.WriteString("\n")
	}

	// Activity feed panel — suppressed while the agent tree renders (see the
	// feedRendered computation above the live card).
	if feedRendered {
		panelBuilder.WriteString(m.activityFeed.View(m.width))
		panelBuilder.WriteString("\n")
	}

	// Agent tree panel. View itself decides emptiness (visibility, node
	// count, AND the post-completion linger) — gate on its output so an
	// expired linger doesn't leave a stray blank line in the panel stack.
	if agentTreeView != "" {
		panelBuilder.WriteString(agentTreeView)
		panelBuilder.WriteString("\n")
	}

	// Status line with todos
	if len(m.todoItems) > 0 && m.todosVisible {
		panelBuilder.WriteString(m.renderTodos())
		panelBuilder.WriteString("\n")
	}

	// Tool progress bar (for long-running operations)
	if m.toolProgressBar != nil && m.toolProgressBar.IsVisible() {
		panelBuilder.WriteString(m.toolProgressBar.View(m.width))
		panelBuilder.WriteString("\n")
	}

	// File peek (one-line status indicator, auto-dismissed after a short TTL).
	// SUPPRESSED while the live card renders: the card already carries the
	// current tool + file (and the finished tool is a scrollback line), so
	// during work the peek added no information — but its 1.5s TTL made it
	// pop in/out every couple of seconds, shifting the whole panel stack and
	// making the card's "Writing:" line JUMP (field report: «строка прыгает
	// из-за всплывающей второй ниже строки»). One activity surface per frame,
	// same rule as feed-vs-tree (v0.91.0).
	if !cardRendered && m.filePeek != nil && m.filePeek.IsVisible() {
		if line := m.filePeek.View(m.width); line != "" {
			panelBuilder.WriteString(line)
			panelBuilder.WriteString("\n")
		}
	}

	// Write panels (dimmed if modal)
	if panelContent := panelBuilder.String(); panelContent != "" {
		if isModal {
			panelContent = lipgloss.NewStyle().Faint(true).Render(panelContent)
		}
		builder.WriteString(panelContent)
	}

	// Processing indicator — minimal braille spinner. Skip when:
	//   (a) the tool progress bar has already claimed the role, or
	//   (b) the live activity card above already rendered a spinner+status
	//       line — the two were showing the same spinner+label in parallel
	//       ("⠇ Analyzing" twice), which is the dup the card was meant to
	//       replace.
	toolProgressVisible := m.toolProgressBar != nil && m.toolProgressBar.IsVisible()
	if (m.state == StateProcessing || m.state == StateStreaming) && !toolProgressVisible && !cardRendered {
		dimStyle := lipgloss.NewStyle().Foreground(ColorDim)

		// Braille spinner
		spinnerFrames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		elapsed := time.Duration(0)
		if !m.streamStartTime.IsZero() {
			elapsed = time.Since(m.streamStartTime)
		}
		frameIdx := int(elapsed.Milliseconds()/80) % len(spinnerFrames)
		activityGlyph := spinnerFrames[frameIdx]
		if m.reducedMotion {
			activityGlyph = "●"
		}

		if m.currentTool != "" {
			// Tool execution: spinner + name in tool's color + info
			toolColor := GetToolIconColor(m.currentTool)
			spinnerStyle := lipgloss.NewStyle().Foreground(toolColor)
			spinner := spinnerStyle.Render(activityGlyph)

			toolNameStyle := lipgloss.NewStyle().Foreground(toolColor).Bold(true)
			icon := GetToolIcon(m.currentTool)
			status := spinner + " " + toolNameStyle.Render(icon+" "+m.currentTool)

			// Batch awareness: when several tools run in parallel, prefix the
			// longest-running (currentTool) with a count so the user sees the
			// full workload instead of a single tool name flickering through.
			if active := len(m.activeToolCalls); active > 1 {
				batchStyle := lipgloss.NewStyle().Foreground(ColorDim)
				status = spinner + " " + batchStyle.Render(fmt.Sprintf("[%d tools]", active)) + " " + toolNameStyle.Render(icon+" "+m.currentTool)
			}

			if m.currentToolInfo != "" {
				infoStyle := lipgloss.NewStyle().Foreground(ColorMuted)
				arrowStyle := lipgloss.NewStyle().Foreground(ColorDim)
				status += " " + arrowStyle.Render("→") + " " + infoStyle.Render(m.currentToolInfo)
			}

			if !m.toolStartTime.IsZero() {
				toolElapsed := time.Since(m.toolStartTime)
				durationColor := ColorDim
				if toolElapsed > 30*time.Second {
					durationColor = ColorRose
				} else if toolElapsed > 5*time.Second {
					durationColor = ColorWarning
				}
				status += "  " + lipgloss.NewStyle().Foreground(durationColor).Render(format.Duration(toolElapsed))
			}
			builder.WriteString(status)
			builder.WriteString("\n")
		} else if m.state == StateProcessing || m.processingLabel != "" {
			// Thinking/Planning/Analyzing: spinner in primary color, label in muted
			spinnerStyle := lipgloss.NewStyle().Foreground(ColorPrimary)
			spinner := spinnerStyle.Render(activityGlyph)
			labelStyle := lipgloss.NewStyle().Foreground(ColorMuted)

			label := m.processingLabel
			if label == "" && m.currentActivity != "" {
				// The agent's own in-progress todo ("Implementing backup/restore")
				// is far more informative than a generic "Generating" — show it so
				// the user always sees WHAT the agent is working on.
				label = m.currentActivity
			}
			if label == "" {
				// Pick a phase-appropriate label based on elapsed time so the
				// user sees something is happening even before the first chunk.
				switch {
				case m.planningModeEnabled && !m.planProgressMode:
					label = "Planning"
				case elapsed < 800*time.Millisecond:
					label = "Connecting"
				case elapsed < 3*time.Second:
					label = "Thinking"
				default:
					label = "Generating"
				}
			}
			status := spinner + " " + labelStyle.Render(label)

			// Show recent agent tools inline for visibility during long agent runs
			if len(m.agentRecentTools) > 0 && m.currentTool == "" {
				recentStyle := lipgloss.NewStyle().Foreground(ColorDim)
				// Show last 3 tools as breadcrumb trail
				start := 0
				if len(m.agentRecentTools) > 3 {
					start = len(m.agentRecentTools) - 3
				}
				trail := strings.Join(m.agentRecentTools[start:], " → ")
				if runes := []rune(trail); len(runes) > 60 {
					trail = string(runes[:57]) + "..."
				}
				status += "\n  " + recentStyle.Render(trail)
			}

			// Show elapsed after 3 seconds with color escalation
			if elapsed >= 3*time.Second {
				durationColor := ColorDim
				if elapsed > 2*time.Minute {
					durationColor = ColorRose
				} else if elapsed > 30*time.Second {
					durationColor = ColorWarning
				}
				status += "  " + lipgloss.NewStyle().Foreground(durationColor).Render(format.Duration(elapsed))
			}

			// Show stream idle indicator when server is slow to respond
			if m.streamIdleMsg != "" {
				idleStyle := lipgloss.NewStyle().Foreground(ColorWarning)
				status += "  " + idleStyle.Render("· "+m.streamIdleMsg)
			} else if m.slowWarningShown && m.currentTool != "" {
				// Tool is running longer than expected — show a brief hint
				// with elapsed time so the user knows *how* long without
				// having to track it themselves. Format-matches the tool-
				// success duration grammar (e.g. "4s", "1.2m") so the two
				// reads as one timekeeping system.
				slowStyle := lipgloss.NewStyle().Foreground(ColorDim)
				hint := "· running"
				if !m.toolStartTime.IsZero() {
					hint = "· running " + format.Duration(time.Since(m.toolStartTime))
				}
				status += "  " + slowStyle.Render(hint)
			}

			// Plan step context
			if m.planProgress != nil && m.planProgressMode {
				stepInfo := fmt.Sprintf(" [step %d/%d]",
					m.planProgress.CurrentStepID,
					m.planProgress.TotalSteps)
				status += " " + dimStyle.Render(stepInfo)
			}

			// Loop iteration context
			if m.loopIteration > 1 {
				iterInfo := fmt.Sprintf(" (iteration %d, %d tools)", m.loopIteration, m.loopToolsUsed)
				status += " " + dimStyle.Render(iterInfo)
			}
			builder.WriteString(status)
			builder.WriteString("\n")
		}
		// Streaming-without-tool-or-label intentionally renders nothing
		// here — the live activity card above already shows an animated
		// spinner inline with the "Writing: …" text, so a separate line
		// would just waste a row and create a visual gap.
	}

	// Permission prompt
	if m.state == StatePermissionPrompt && m.permRequest != nil {
		builder.WriteString(m.renderPermissionPrompt())
		builder.WriteString("\n")
	}

	// Question prompt
	if m.state == StateQuestionPrompt && m.questionRequest != nil {
		builder.WriteString(m.renderQuestionPrompt())
		builder.WriteString("\n")
	}

	// Plan approval prompt
	if m.state == StatePlanApproval && m.planRequest != nil {
		builder.WriteString(m.renderPlanApproval())
		builder.WriteString("\n")
	}

	// Model selector
	if m.state == StateModelSelector {
		builder.WriteString(m.renderModelSelector())
		builder.WriteString("\n")
	}

	// Settings modal
	if m.state == StateSettings {
		builder.WriteString(m.renderSettings())
		builder.WriteString("\n")
	}

	// Masked API-key entry modal
	if m.state == StateAPIKeyEntry {
		builder.WriteString(m.renderKeyEntry())
		builder.WriteString("\n")
	}

	// Shortcuts overlay
	if m.state == StateShortcutsOverlay {
		builder.WriteString(m.renderShortcutsOverlay())
		builder.WriteString("\n")
	}

	if m.state == StateNotificationCenter {
		builder.WriteString(m.renderNotificationCenter())
		builder.WriteString("\n")
	}

	// Command palette
	if m.state == StateCommandPalette {
		builder.WriteString(m.commandPalette.View(m.width, m.height))
		builder.WriteString("\n")
	}

	// Diff preview
	if m.state == StateDiffPreview {
		builder.WriteString(m.diffPreview.View())
		builder.WriteString("\n")
	}

	// Multi-file diff preview
	if m.state == StateMultiDiffPreview {
		builder.WriteString(m.multiDiffPreview.View())
		builder.WriteString("\n")
	}

	// Search results
	if m.state == StateSearchResults {
		builder.WriteString(m.searchResults.View())
		builder.WriteString("\n")
	}

	// Git status
	if m.state == StateGitStatus {
		builder.WriteString(m.gitStatusModel.View())
		builder.WriteString("\n")
	}

	// File browser
	if m.state == StateFileBrowser {
		builder.WriteString(m.fileBrowser.View())
		builder.WriteString("\n")
	}

	// Batch progress
	if m.state == StateBatchProgress {
		builder.WriteString(m.progressModel.View())
		builder.WriteString("\n")
	}

	// Input area. During processing/streaming it appears only after the user
	// starts composing type-ahead, so an idle busy state gives one more row to
	// output.
	if m.shouldRenderInputArea() {
		builder.WriteString(m.input.View())
	}
	if m.state == StateInput {
		// (No under-input mode cue: the active mode already shows persistently in
		// the bottom status bar — `YOLO`/`!SANDBOX` safetyBadge + `ℹ plan mode` —
		// and a full description + "Shift+Tab → next" appears in scrollback the
		// moment you switch. A separate whole-line cue here just duplicated both.)
		// Command hints
		inputText := m.input.Value()
		if strings.HasPrefix(inputText, "/") && len(inputText) > 1 {
			hint := m.getCommandHint(inputText)
			if hint != "" {
				hintStyle := lipgloss.NewStyle().
					Foreground(ColorMuted).
					Italic(true).
					MarginTop(0)
				builder.WriteString("\n" + hintStyle.Render("  "+hint))
			}
		}

	}

	// Overlay Context Observatory panel if visible
	if m.observatoryPanel != nil && m.observatoryPanel.IsVisible() {
		observatoryView := m.observatoryPanel.View(m.width, m.height)
		if observatoryView != "" {
			builder.WriteString("\n")
			builder.WriteString(observatoryView)
			builder.WriteString("\n")
		}
	}
	if deferToasts && toastView != "" {
		builder.WriteString("\n")
		builder.WriteString(toastView)
	}

	preStatus := builder.String()
	withoutOutput := strings.Replace(preStatus, outputMarker, "", 1)
	statusBar := m.renderStatusBar()

	// Reserve one separator row and the final status row. Dynamic panels,
	// multi-line input and toasts consume space from the viewport instead of
	// growing the whole frame past the terminal height.
	// Budget identity: the marker sits on its OWN row of preStatus, and the
	// substitution REPLACES that row with the output view — so
	// bodyRows = Height(withoutOutput) - 1 + outputBudget, and the frame is
	// body + "\n" + statusBar (the status bar directly below the last body
	// row, NO separator: the old "-1 reservation + gap" math wedged 2-3 blank
	// rows between the input box and the status bar). Solving
	// bodyRows + Height(statusBar) = m.height gives the +1.
	outputBudget := m.height - lipgloss.Height(withoutOutput) - lipgloss.Height(statusBar) + 1
	// ViewWithHeight GUARANTEES exactly outputBudget rows (see its contract) —
	// no post-hoc correction here. The old shrink loop that "fit" an
	// overshooting render walked the budget down to ZERO on short content
	// (styled viewport padding used to add one extra row), blanking the output
	// and floating the input box to the top of the screen.
	outputView := m.output.ViewWithHeight(max(outputBudget, 0))
	if isModal && outputView != "" {
		outputView = lipgloss.NewStyle().Faint(true).Render(outputView)
	}

	body := strings.Replace(preStatus, outputMarker, outputView, 1)
	body = fitVisualWidth(body, m.width)
	maxBodyHeight := max(m.height-lipgloss.Height(statusBar), 0)
	body = tailVisualRows(body, maxBodyHeight)
	// Status bar directly below the body — no separator rows. Degenerate
	// shapes (tiny terminals clamping the budget to 0) can leave the frame
	// short; pad ABOVE the status bar so it stays on the terminal's final row.
	frame := body + "\n" + statusBar
	if missing := m.height - lipgloss.Height(frame); missing > 0 {
		frame = body + strings.Repeat("\n", 1+missing) + statusBar
	}
	return m.styles.App.Render(frame)
}

func toastVisibleLineLimit(height int) int {
	switch {
	case height <= 0 || height > 18:
		return 5
	case height <= 8:
		return 1
	case height <= 12:
		return 2
	default:
		return 3
	}
}

// tailVisualRows keeps the bottom-most rendered rows of s. The dynamic footer
// (input, prompts, activity) is more important than old output when an unusually
// short terminal cannot fit both. ANSI styling emitted by lipgloss is line
// scoped in these views, so newline slicing preserves valid rendered rows.
func tailVisualRows(s string, maxRows int) string {
	if maxRows <= 0 {
		return ""
	}
	for lipgloss.Height(s) > maxRows {
		newline := strings.IndexByte(s, '\n')
		if newline < 0 {
			return ""
		}
		s = s[newline+1:]
	}
	return s
}

// fitVisualWidth is the last-resort horizontal safety rail for dynamic and
// third-party views. Individual components still adapt their own layout, but a
// long runtime label or modal row must never wrap the terminal and destabilize
// the frame geometry.
func fitVisualWidth(s string, width int) string {
	if width <= 0 || s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = fitStatusText(line, width)
	}
	return strings.Join(lines, "\n")
}

func (m Model) shouldRenderInputArea() bool {
	if m.state == StateInput {
		return true
	}
	if m.state != StateProcessing && m.state != StateStreaming {
		return false
	}
	return m.input.Value() != "" ||
		m.input.historySearchMode ||
		m.input.showSuggestions ||
		m.input.showArgHints
}

// applyResize applies a buffered WindowSizeMsg to all components.
func (m *Model) applyResize(msg *tea.WindowSizeMsg) tea.Cmd {
	m.width = max(msg.Width, 10)
	m.height = max(msg.Height, 6)
	m.input.SetWidth(m.width)
	m.progressModel.SetSize(m.width, m.height)
	// Full-screen models own internal viewport geometry. They may stay open
	// while the terminal is resized, so updating only the outer compositor
	// leaves stale split panes, footer budgets and selection windows behind.
	// Route only to the active heavy model: File Browser SetSize may reload a
	// preview from disk, and inactive request-backed models are sized again when
	// opened using the already-current m.width/m.height.
	switch m.state {
	case StateDiffPreview:
		m.diffPreview.SetSize(m.width, m.height)
	case StateMultiDiffPreview:
		m.multiDiffPreview.SetSize(m.width, m.height)
	case StateSearchResults:
		m.searchResults.SetSize(m.width, m.height)
	case StateGitStatus:
		m.gitStatusModel.SetSize(m.width, m.height)
	case StateFileBrowser:
		m.fileBrowser.SetSize(m.width, m.height)
	case StateCommandPalette:
		if m.commandPalette != nil {
			m.commandPalette.SetSize(m.width, m.height)
		}
	}
	if m.CompactMode {
		m.output.SetSize(m.width, max(m.height/3, 1))
	} else {
		m.output.SetSize(m.width, max(m.height-5, 1))
	}
	if m.planFeedbackMode {
		m.planFeedbackInput.SetWidth(promptInputContentWidth(m.width))
	}
	if m.state == StateQuestionPrompt {
		m.questionInputModel.SetWidth(promptInputContentWidth(m.width))
	}
	if m.state == StateAPIKeyEntry {
		m.keyEntryInput.Width = keyEntryInputWidth(m.width)
	}
	var cmd tea.Cmd
	m.output, cmd = m.output.Update(*msg)
	return cmd
}

// SetBellEnabled enables or disables the terminal bell on prompts.
func (m *Model) SetBellEnabled(enabled bool) {
	m.bellEnabled = enabled
}

// bellCmd returns a tea.Cmd that emits a terminal bell if bell is enabled.
// Must be used instead of writing directly to stdout inside Update().
func (m *Model) bellCmd() tea.Cmd {
	if !m.bellEnabled {
		return nil
	}
	return func() tea.Msg {
		fmt.Fprintf(os.Stderr, "\a")
		return nil
	}
}

// SetHintsEnabled enables or disables contextual hints.
func (m *Model) SetHintsEnabled(enabled bool) {
	m.hintsEnabled = enabled
}

// ShowHint displays a contextual hint if hints are enabled and the hint hasn't
// been shown too many times (max 3 times per hint).
func (m *Model) ShowHint(hintID, text string) {
	if !m.hintsEnabled {
		return
	}
	if m.hintsShown[hintID] >= 3 {
		return // Already shown 3 times, auto-dismiss
	}
	m.hintsShown[hintID]++
	m.toastManager.ShowInfo(text)
}

// RemoveHint removes a hint by ID.
func (m *Model) RemoveHint(hintID string) {
	// Hints are shown as toasts which auto-dismiss; this is a no-op.
}

// handleTaskStarted handles the TaskStartedEvent message.
func (m *Model) handleTaskStarted(msg TaskStartedEvent) {
	// Sweep stale completed tasks before adding a new one
	m.cleanupStaleCoordinatedTasks()

	taskState := &CoordinatedTaskState{
		ID:                 msg.TaskID,
		Message:            msg.Message,
		PlanType:           msg.PlanType,
		Status:             "running",
		Progress:           0,
		StartTime:          time.Now(),
		LastProgressBucket: -1,
	}

	m.coordinatedTasks[msg.TaskID] = taskState
	taskIndex := 0
	for i, id := range m.coordinatedTaskOrder {
		if id == msg.TaskID {
			taskIndex = i + 1
			break
		}
	}
	if taskIndex == 0 {
		m.coordinatedTaskOrder = append(m.coordinatedTaskOrder, msg.TaskID)
		taskIndex = len(m.coordinatedTaskOrder)
	}
	m.activeCoordinatedTask = msg.TaskID

	m.output.AppendLine("")
	m.output.AppendLine(m.renderTaskTimelineStart(taskIndex, msg.PlanType, msg.Message))
}

// cleanupStaleCoordinatedTasks removes completed or failed coordinated tasks
// whose EndTime is more than 5 seconds ago. This prevents the Ctrl+T task
// panel from accumulating stale entries indefinitely.
func (m *Model) cleanupStaleCoordinatedTasks() {
	const cleanupDelay = 5 * time.Second
	now := time.Now()
	var cleaned int
	for _, id := range m.coordinatedTaskOrder {
		ts, ok := m.coordinatedTasks[id]
		if !ok {
			continue
		}
		if (ts.Status == "completed" || ts.Status == "failed") && now.Sub(ts.EndTime) > cleanupDelay {
			delete(m.coordinatedTasks, id)
			cleaned++
		}
	}
	if cleaned > 0 {
		newOrder := make([]string, 0, len(m.coordinatedTaskOrder)-cleaned)
		for _, id := range m.coordinatedTaskOrder {
			if _, ok := m.coordinatedTasks[id]; ok {
				newOrder = append(newOrder, id)
			}
		}
		m.coordinatedTaskOrder = newOrder
	}
}

// handleTaskCompleted marks a coordinated task as completed or failed and
// schedules its auto-removal after a short delay.
func (m *Model) handleTaskCompleted(msg TaskCompletedEvent) tea.Cmd {
	ts, ok := m.coordinatedTasks[msg.TaskID]
	if !ok {
		return nil
	}
	{
		// Terminal events are idempotent. A replay must not append another
		// completion line or move EndTime, which would prolong stale UI state.
		if ts.Status != "running" {
			if m.activeCoordinatedTask == msg.TaskID {
				m.activeCoordinatedTask = m.latestRunningCoordinatedTask()
			}
			return nil
		}
		duration := max(msg.Duration, 0)
		if duration == 0 && !ts.StartTime.IsZero() {
			duration = max(time.Since(ts.StartTime), 0)
		}
		ts.Duration = duration
		ts.Error = msg.Error
		ts.Progress = 1.0
		if msg.Success {
			ts.Status = "completed"
		} else {
			ts.Status = "failed"
		}
		ts.EndTime = time.Now()

		m.output.AppendLine(m.renderTaskTimelineDone(msg.Success, duration.Round(time.Millisecond), msg.Error))
	}

	if m.activeCoordinatedTask == msg.TaskID {
		m.activeCoordinatedTask = m.latestRunningCoordinatedTask()
	}

	// Schedule auto-removal after 5 seconds
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg {
		return CoordinatedTaskCleanupMsg{ID: msg.TaskID}
	})
}

// handleTaskProgress handles the TaskProgressEvent message.
func (m *Model) handleTaskProgress(msg TaskProgressEvent) {
	if taskState, ok := m.coordinatedTasks[msg.TaskID]; ok {
		if taskState.Status != "running" {
			return
		}
		progress, determinate := normalizeTimelineProgress(msg.Progress)
		if !determinate {
			progress = -1
		}
		taskState.Progress = progress
		if message := normalizeTimelineText(msg.Message); message != "" {
			taskState.Message = message
		}
		progressBucket := timelineProgressBucket(progress)
		progressMessage := normalizeTimelineText(msg.Message)
		if progressBucket == taskState.LastProgressBucket && progressMessage == taskState.LastProgressMessage {
			return
		}
		taskState.LastProgressBucket = progressBucket
		taskState.LastProgressMessage = progressMessage
		m.output.AppendLine(m.renderTaskTimelineProgress(progress, msg.Message))
	}
}

func (m *Model) latestRunningCoordinatedTask() string {
	for i := len(m.coordinatedTaskOrder) - 1; i >= 0; i-- {
		id := m.coordinatedTaskOrder[i]
		if task, ok := m.coordinatedTasks[id]; ok && task.Status == "running" {
			return id
		}
	}
	return ""
}

// renderInlineDiff renders an inline diff for small text changes.
func (m *Model) renderInlineDiff(msg InlineDiffMsg) {
	oldLines := strings.Split(msg.OldText, "\n")
	newLines := strings.Split(msg.NewText, "\n")

	// Skip large diffs — they'll use fullscreen preview
	if len(oldLines)+len(newLines) > 20 {
		return
	}

	connStyle := lipgloss.NewStyle().Foreground(ColorDim)
	removedStyle := lipgloss.NewStyle().Foreground(ColorError)
	addedStyle := lipgloss.NewStyle().Foreground(ColorSuccess)

	m.output.AppendLine(connStyle.Render("  ⎿  diff:"))
	for _, line := range oldLines {
		if line != "" {
			m.output.AppendLine("    " + removedStyle.Render("- "+line))
		}
	}
	for _, line := range newLines {
		if line != "" {
			m.output.AppendLine("    " + addedStyle.Render("+ "+line))
		}
	}
}

// GetCoordinatedTasksSummary returns a summary of coordinated tasks for status display.
func (m *Model) GetCoordinatedTasksSummary() string {
	if len(m.coordinatedTasks) == 0 {
		return ""
	}

	completed := 0
	running := 0
	failed := 0

	for _, task := range m.coordinatedTasks {
		switch task.Status {
		case "completed":
			completed++
		case "running":
			running++
		case "failed":
			failed++
		}
	}

	total := len(m.coordinatedTasks)
	return fmt.Sprintf("%d/%d subtasks (running: %d, done: %d, failed: %d)", completed, total, running, completed, failed)
}

// ClearCoordinatedTasks clears all coordinated task tracking.
func (m *Model) ClearCoordinatedTasks() {
	m.coordinatedTasks = make(map[string]*CoordinatedTaskState)
	m.coordinatedTaskOrder = make([]string, 0)
	m.activeCoordinatedTask = ""
}

// removeCoordinatedTask removes a single task from coordinatedTasks and
// coordinatedTaskOrder. Used by CoordinatedTaskCleanupMsg handler to
// auto-remove completed/failed tasks after a short delay.
func (m *Model) removeCoordinatedTask(id string) {
	delete(m.coordinatedTasks, id)
	for i, tid := range m.coordinatedTaskOrder {
		if tid == id {
			m.coordinatedTaskOrder = append(m.coordinatedTaskOrder[:i], m.coordinatedTaskOrder[i+1:]...)
			break
		}
	}
	// Clear active if this was the active task
	if m.activeCoordinatedTask == id {
		m.activeCoordinatedTask = ""
	}
}

// ========== Background Task Handlers ==========

// handleBackgroundTask handles the BackgroundTaskMsg message.
func (m *Model) handleBackgroundTask(msg BackgroundTaskMsg) tea.Cmd {
	switch msg.Status {
	case "running":
		// New task started - track silently (shown in status bar)
		m.backgroundTasks[msg.ID] = &BackgroundTaskState{
			ID:          msg.ID,
			Type:        msg.Type,
			Description: msg.Description,
			Status:      "running",
			StartTime:   time.Now(),
		}

	case "completed", "failed", "cancelled":
		var cmd tea.Cmd
		// Bell on task completion or failure
		if msg.Status == "completed" || msg.Status == "failed" {
			cmd = m.bellCmd()
		}
		// Task finished - remove from tracking
		if task, ok := m.backgroundTasks[msg.ID]; ok {
			if m.toastManager != nil {
				desc := task.Description
				if runes := []rune(desc); len(runes) > 30 {
					desc = string(runes[:27]) + "..."
				}
				// Include a one-line gist of the result (conclusion on success,
				// error on failure) so a background agent's output is visible at
				// a glance; /tasks still has the full output.
				gist := ""
				if msg.Summary != "" {
					gist = " · " + msg.Summary
				}
				switch msg.Status {
				case "failed":
					m.toastManager.ShowError(fmt.Sprintf("Task failed: %s%s — /tasks for details", desc, gist))
				case "completed":
					// Point at /tasks so a finished background agent's output
					// is one command away, not lost to the scrollback.
					m.toastManager.ShowSuccess(fmt.Sprintf("Task done: %s%s — /tasks for output", desc, gist))
				}
			}
			delete(m.backgroundTasks, msg.ID)
		}
		return cmd
	}
	return nil
}

// GetBackgroundTaskCount returns the number of running background tasks.
func (m *Model) GetBackgroundTaskCount() int {
	return len(m.backgroundTasks)
}

// GetBackgroundTasks returns a snapshot of all background tasks.
func (m *Model) GetBackgroundTasks() map[string]*BackgroundTaskState {
	result := make(map[string]*BackgroundTaskState, len(m.backgroundTasks))
	maps.Copy(result, m.backgroundTasks)
	return result
}

// ========== Toast Notification Methods ==========

// ShowToastSuccess displays a success toast notification.
func (m *Model) ShowToastSuccess(message string) {
	if m.toastManager != nil {
		m.toastManager.ShowSuccess(message)
	}
}

// ShowToastError displays an error toast notification.
func (m *Model) ShowToastError(message string) {
	if m.toastManager != nil {
		m.toastManager.ShowError(message)
	}
}

// ShowToastInfo displays an info toast notification.
func (m *Model) ShowToastInfo(message string) {
	if m.toastManager != nil {
		m.toastManager.ShowInfo(message)
	}
}

// ShowToastWarning displays a warning toast notification.
func (m *Model) ShowToastWarning(message string) {
	if m.toastManager != nil {
		m.toastManager.ShowWarning(message)
	}
}

// ========== Command Palette Methods ==========

// ShowCommandPalette shows the command palette.
func (m *Model) ShowCommandPalette() {
	if m.commandPalette != nil {
		m.commandPalette.Show()
		m.state = StateCommandPalette
	}
}

// ========== Plan Progress Panel Methods ==========

// StartPlanExecution initializes the plan progress panel for a new plan.
func (m *Model) StartPlanExecution(planID, title, description string, steps []PlanStepInfo) {
	if m.planProgressPanel != nil {
		m.planProgressPanel.StartPlan(planID, title, description, steps)
	}
}

// UpdatePlanStep updates a step's status in the plan progress panel.
func (m *Model) UpdatePlanStep(stepID int, status PlanStepStatus, message string) {
	if m.planProgressPanel == nil {
		return
	}

	switch status {
	case PlanStepInProgress:
		m.planProgressPanel.StartStep(stepID)
	case PlanStepCompleted:
		m.planProgressPanel.CompleteStep(stepID, message, "")
	case PlanStepFailed:
		m.planProgressPanel.FailStep(stepID, message, "")
	case PlanStepSkipped:
		m.planProgressPanel.SkipStep(stepID, "")
	case PlanStepPaused:
		m.planProgressPanel.PauseStep(stepID, message)
	}
}

// EndPlanExecution marks the plan as finished.
func (m *Model) EndPlanExecution() {
	if m.planProgressPanel != nil {
		m.planProgressPanel.EndPlan()
	}
}

// UIDebugState is a serializable snapshot of key TUI fields for debugging.
// DebugStateRequestMsg asks the Update loop to compute DebugState() on its
// own goroutine — the SAME one that owns every mutation to Model's fields
// (incl. backgroundTasks, mutated both via map insert/delete in
// handleBackgroundTask and via in-place field writes for progress updates)
// — and deliver the result via Resp. Round 8: App.GetUIDebugState used to
// call m.tui.DebugState() directly from the command-execution goroutine,
// racing those mutations — a fatal, unrecoverable "concurrent map read and
// map write" crash risk (reachable via /debug-dump), not just a -race
// finding. Resp is buffered (size 1) so a slow/timed-out caller can never
// block the Update loop's send.
type DebugStateRequestMsg struct {
	Resp chan UIDebugState
}

type UIDebugState struct {
	Timestamp         time.Time                       `json:"timestamp"`
	State             string                          `json:"state"`
	Width             int                             `json:"width"`
	Height            int                             `json:"height"`
	CompactMode       bool                            `json:"compact_mode"`
	ProcessingLabel   string                          `json:"processing_label"`
	CurrentTool       string                          `json:"current_tool"`
	CurrentToolInfo   string                          `json:"current_tool_info"`
	ActiveToolCalls   int                             `json:"active_tool_calls"`
	BackgroundTasks   map[string]*BackgroundTaskState `json:"background_tasks"`
	PlanningMode      bool                            `json:"planning_mode"`
	PermissionsActive bool                            `json:"permissions_enabled"`
	SandboxEnabled    bool                            `json:"sandbox_enabled"`
	TokenUsagePct     float64                         `json:"token_usage_pct"`
	MCPHealthy        int                             `json:"mcp_healthy"`
	MCPTotal          int                             `json:"mcp_total"`
	ConversationMode  string                          `json:"conversation_mode"`
	GitBranch         string                          `json:"git_branch"`
	WorkDir           string                          `json:"work_dir"`
	SessionStart      time.Time                       `json:"session_start"`
	TodoCount         int                             `json:"todo_count"`
	FileBrowserActive bool                            `json:"file_browser_active"`
	ModelSelectorOpen bool                            `json:"model_selector_open"`
	CurrentModel      string                          `json:"current_model"`
	PlanProgressMode  bool                            `json:"plan_progress_mode"`
}

// DebugState returns a serializable snapshot of the current UI state.
func (m *Model) DebugState() UIDebugState {
	stateName := "unknown"
	switch m.state {
	case StateInput:
		stateName = "input"
	case StateProcessing:
		stateName = "processing"
	case StateStreaming:
		stateName = "streaming"
	case StatePermissionPrompt:
		stateName = "permission_prompt"
	case StateQuestionPrompt:
		stateName = "question_prompt"
	case StatePlanApproval:
		stateName = "plan_approval"
	case StateModelSelector:
		stateName = "model_selector"
	case StateSettings:
		stateName = "settings"
	case StateAPIKeyEntry:
		stateName = "api_key_entry"
	case StateNotificationCenter:
		stateName = "notification_center"
	case StateShortcutsOverlay:
		stateName = "shortcuts_overlay"
	case StateCommandPalette:
		stateName = "command_palette"
	case StateDiffPreview:
		stateName = "diff_preview"
	case StateMultiDiffPreview:
		stateName = "multi_diff_preview"
	case StateSearchResults:
		stateName = "search_results"
	case StateGitStatus:
		stateName = "git_status"
	case StateFileBrowser:
		stateName = "file_browser"
	case StateBatchProgress:
		stateName = "batch_progress"
	}

	// Round 8: deep-copy backgroundTasks rather than returning the live map.
	// The PRIMARY concurrency fix is DebugStateRequestMsg (see its type doc):
	// DebugState() now normally runs ON the Update-loop goroutine, so the
	// copy loop itself no longer races handleBackgroundTask's map mutations.
	// The deep copy protects the RETURNED snapshot: the caller (/debug-dump,
	// on the command-execution goroutine) iterates and JSON-marshals it
	// AFTER this returns, while the Update loop keeps inserting/deleting
	// entries AND mutating *BackgroundTaskState fields in place (progress
	// updates). A shallow map copy alone wouldn't be enough — the pointed-to
	// structs (and their ToolsUsed slice) are still mutated after insertion,
	// so this copies the struct VALUES too. Also covers the headless
	// direct-call fallback in GetUIDebugState (no Update loop running there).
	backgroundTasksCopy := make(map[string]*BackgroundTaskState, len(m.backgroundTasks))
	for id, task := range m.backgroundTasks {
		if task == nil {
			continue
		}
		cp := *task
		cp.ToolsUsed = append([]string(nil), task.ToolsUsed...)
		backgroundTasksCopy[id] = &cp
	}

	return UIDebugState{
		Timestamp:         time.Now(),
		State:             stateName,
		Width:             m.width,
		Height:            m.height,
		CompactMode:       m.CompactMode,
		ProcessingLabel:   m.processingLabel,
		CurrentTool:       m.currentTool,
		CurrentToolInfo:   m.currentToolInfo,
		ActiveToolCalls:   len(m.activeToolCalls),
		BackgroundTasks:   backgroundTasksCopy,
		PlanningMode:      m.planningModeEnabled,
		PermissionsActive: m.permissionsEnabled,
		SandboxEnabled:    m.sandboxEnabled,
		TokenUsagePct: func() float64 {
			if m.tokenUsage != nil {
				return m.tokenUsage.PercentUsed * 100
			}
			return 0
		}(),
		MCPHealthy:        m.mcpHealthy,
		MCPTotal:          m.mcpTotal,
		ConversationMode:  m.conversationMode,
		GitBranch:         m.gitBranch,
		WorkDir:           m.workDir,
		SessionStart:      m.sessionStart,
		TodoCount:         len(m.todoItems),
		FileBrowserActive: m.fileBrowserActive,
		ModelSelectorOpen: m.state == StateModelSelector,
		CurrentModel:      m.currentModel,
		PlanProgressMode:  m.planProgressMode,
	}
}

// Cleanup performs final UI cleanup before exit.
func (m *Model) Cleanup() {
	if runtime.GOOS == "darwin" && os.Getenv("TERM_PROGRAM") == "iTerm.app" {
		// Clear iTerm2 badge via escape sequence
		// Note: Cleanup runs after program finishes, so direct Print is okay here
		// but ideally should have been a Cmd if we were still in the loop.
		fmt.Print("\033]1337;SetBadgeFormat=\a")
	}
}

// HidePlanProgress hides the plan progress panel.
func (m *Model) HidePlanProgress() {
	if m.planProgressPanel != nil {
		m.planProgressPanel.Hide()
	}
}

// IsPlanProgressVisible returns whether the plan progress panel is visible.
func (m Model) IsPlanProgressVisible() bool {
	return m.planProgressPanel != nil && m.planProgressPanel.IsVisible()
}
