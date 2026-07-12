package ui

import (
	"fmt"
	"maps"
	"os"
	"reflect"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atotto/clipboard"
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

	// Question prompt state
	questionRequest        *QuestionRequestMsg
	questionSelectedOption int
	questionCustomInput    bool
	questionInputModel     InputModel

	// Plan approval state
	planRequest        *PlanApprovalRequestMsg
	planSelectedOption int
	planFeedbackMode   bool       // True when entering feedback for "Request Changes"
	planFeedbackInput  InputModel // Input model for feedback

	// Model selector state
	modelSelectedIndex int
	availableModels    []ModelInfo
	currentModel       string
	onModelSelect      func(modelID string)

	// Settings modal state (the /settings interactive screen)
	settingsItems    []SettingItem
	settingsCursor   int
	settingsModel    string
	settingsProvider string
	onSettingToggle  func(key string, on bool)
	onOpenSettings   func() // Opens the /settings modal (Ctrl+S + palette action)

	// Masked API-key entry modal (/login <provider> with no key)
	keyEntryInput       textinput.Model
	keyEntryProvider    string
	keyEntryDisplayName string
	keyEntrySetupURL    string
	onKeyEntrySubmit    func(provider, key string)

	// Diff preview state
	diffPreview         DiffPreviewModel
	diffRequest         *DiffPreviewRequestMsg
	multiDiffPreview    MultiDiffPreviewModel
	multiDiffRequest    *MultiDiffPreviewRequestMsg
	onDiffDecision      func(decision DiffDecision)
	onMultiDiffDecision func(decisions map[string]DiffDecision)

	// Search results state
	searchResults  SearchResultsModel
	searchRequest  *SearchResultsRequestMsg
	onSearchAction func(action SearchAction)

	// Git status state
	gitStatusModel   GitStatusModel
	gitStatusRequest *GitStatusRequestMsg
	onGitAction      func(action GitAction)

	// Scratchpad
	scratchpad string

	// File browser state
	fileBrowser       FileBrowserModel
	fileBrowserActive bool
	onFileSelect      func(path string)

	// Progress state
	progressModel  ProgressModel
	progressActive bool

	// Tool progress bar (for long-running tool operations)
	toolProgressBar *ToolProgressBarModel

	// Tool output state (for expand/collapse)
	toolOutput          *ToolOutputModel
	lastToolOutputIndex int // Tool output index

	// Callbacks
	onSubmit                   func(message string)
	onQuit                     func()
	onPermission               func(reqID string, decision PermissionDecision)
	onQuestion                 func(answer string)
	onPlanApproval             func(decision PlanApprovalDecision)
	onPlanApprovalWithFeedback func(decision PlanApprovalDecision, feedback string) // Extended callback with feedback
	onInterrupt                func()                                               // Called when user presses ESC to interrupt
	onCancel                   func()                                               // Called when user presses ESC to cancel current processing
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
	hintsEnabled bool // Enable contextual hints
	hintSystem   *HintSystem
	hintsShown   map[string]int // Track how many times each hint was shown

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
	lastErrorMsg   string // Last error message for dedup
	lastErrorCount int    // Consecutive repeat count

	// Copy support: track last AI response
	lastResponseText   string           // Last AI response text (saved on ResponseDoneMsg)
	currentResponseBuf *strings.Builder // Accumulates current streaming response (pointer to survive Bubble Tea copies)

	// Terminal bell on prompts
	bellEnabled bool

	// Quit confirmation: requires double Ctrl+C within 2 seconds
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

	return &Model{
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
	var reqID string
	if m.permRequest != nil {
		reqID = m.permRequest.ID
	}
	m.permRequest = nil
	m.permSelectedOption = 0
	if decision == PermissionDeny || decision == PermissionDenySession {
		m.state = StateInput
		m.output.AppendLine(m.styles.Warning.Render(" Denied - operation cancelled"))
		m.output.AppendLine("")
		if m.onInterrupt != nil {
			m.onInterrupt()
		}
		if m.onPermission != nil {
			m.onPermission(reqID, decision)
		}
		return m.input.Focus()
	}
	m.state = StateProcessing
	if m.onPermission != nil {
		m.onPermission(reqID, decision)
	}
	return nil
}

func (m *Model) handlePermissionPromptKeys(msg tea.KeyMsg) tea.Cmd {
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
		// Show tool details
		if m.permRequest != nil {
			infoStyle := lipgloss.NewStyle().Foreground(ColorInfo)
			m.output.AppendLine("")
			m.output.AppendLine(infoStyle.Render("  Tool: " + m.permRequest.ToolName))
			if m.permRequest.Reason != "" {
				m.output.AppendLine(m.styles.Dim.Render("  " + m.permRequest.Reason))
			}
			m.output.AppendLine("")
		}
	}
	return nil
}

// handleQuestionPromptKeys handles keys in question prompt state.
func (m *Model) handleQuestionPromptKeys(msg tea.KeyMsg) tea.Cmd {
	// If custom input mode, delegate to input model
	if m.questionCustomInput {
		switch msg.Type {
		case tea.KeyEnter:
			// Submit custom answer
			answer := m.questionInputModel.Value()
			m.questionRequest = nil
			m.questionCustomInput = false
			m.questionInputModel.Reset()
			m.state = StateProcessing
			if m.onQuestion != nil {
				m.onQuestion(answer)
			}
			return nil
		case tea.KeyEsc:
			// Cancel custom input, return to options
			m.questionCustomInput = false
			m.questionInputModel.Reset()
			return nil
		default:
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
	if optCount == 0 {
		// No options - just free text input
		switch msg.Type {
		case tea.KeyEnter:
			answer := m.questionInputModel.Value()
			m.questionRequest = nil
			m.questionInputModel.Reset()
			m.state = StateProcessing
			if m.onQuestion != nil {
				m.onQuestion(answer)
			}
			return nil
		default:
			var cmd tea.Cmd
			m.questionInputModel, cmd = m.questionInputModel.Update(msg)
			return cmd
		}
	}

	switch msg.String() {
	case "esc":
		m.questionRequest = nil
		m.questionSelectedOption = 0
		m.state = StateInput
		m.output.AppendLine("")
		m.output.AppendLine(m.styles.Warning.Render(" Question cancelled"))
		m.output.AppendLine("")
		if m.onQuestion != nil {
			m.onQuestion("")
		}
		return m.input.Focus()
	case "up", "k":
		if m.questionSelectedOption > 0 {
			m.questionSelectedOption--
		}
	case "down", "j":
		if m.questionSelectedOption < optCount { // +1 for "Other" option
			m.questionSelectedOption++
		}
	case "enter", " ":
		if m.questionSelectedOption < optCount {
			// Selected an option
			answer := m.questionRequest.Options[m.questionSelectedOption]
			m.questionRequest = nil
			m.questionSelectedOption = 0
			m.state = StateProcessing
			if m.onQuestion != nil {
				m.onQuestion(answer)
			}
		} else {
			// Selected "Other" - switch to custom input
			m.questionCustomInput = true
			m.questionInputModel = NewInputModel(m.styles, m.workDir)
			m.questionInputModel.SetWidth(m.width)
			return m.questionInputModel.Focus()
		}
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		// Quick select by number
		idx := int(msg.String()[0] - '1')
		if idx < optCount {
			answer := m.questionRequest.Options[idx]
			m.questionRequest = nil
			m.questionSelectedOption = 0
			m.state = StateProcessing
			if m.onQuestion != nil {
				m.onQuestion(answer)
			}
		}
	}
	return nil
}

// handlePlanApprovalKeys handles keys in plan approval state.
func (m *Model) handlePlanApprovalKeys(msg tea.KeyMsg) tea.Cmd {
	// If in feedback mode, handle input
	if m.planFeedbackMode {
		switch msg.Type {
		case tea.KeyEnter:
			// Submit feedback
			feedback := m.planFeedbackInput.Value()
			m.planRequest = nil
			m.planSelectedOption = 0
			m.planFeedbackMode = false
			m.planFeedbackInput.Reset()
			m.state = StateInput
			// Send decision with feedback
			if m.onPlanApprovalWithFeedback != nil {
				m.onPlanApprovalWithFeedback(PlanModifyRequested, feedback)
			} else if m.onPlanApproval != nil {
				m.onPlanApproval(PlanModifyRequested)
			}
			return m.input.Focus()
		case tea.KeyEsc:
			// Cancel feedback, return to options
			m.planFeedbackMode = false
			m.planFeedbackInput.Reset()
			return nil
		default:
			var cmd tea.Cmd
			m.planFeedbackInput, cmd = m.planFeedbackInput.Update(msg)
			return cmd
		}
	}

	switch msg.String() {
	case "up", "k":
		if m.planSelectedOption > 0 {
			m.planSelectedOption--
		}
	case "down", "j":
		if m.planSelectedOption < 2 {
			m.planSelectedOption++
		}
	case "enter", " ":
		decision := PlanApprovalDecision(m.planSelectedOption)
		if decision == PlanModifyRequested {
			// Enter feedback mode
			m.planFeedbackMode = true
			m.planFeedbackInput = NewInputModel(m.styles, m.workDir)
			m.planFeedbackInput.SetWidth(m.width - 4)
			m.planFeedbackInput.SetPlaceholder("Enter your feedback for plan modifications...")
			return m.planFeedbackInput.Focus()
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
		m.state = StateProcessing
		if m.onPlanApproval != nil {
			m.onPlanApproval(decision)
		}
	case "y", "1":
		// Quick approve (1 = the numbered "Approve" option)
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
		m.planFeedbackInput.Reset()
		m.state = StateProcessing
		if m.onPlanApproval != nil {
			m.onPlanApproval(PlanApproved)
		}
	case "n", "2":
		// Quick reject (2 = the numbered "Reject" option)
		m.planRequest = nil
		m.planSelectedOption = 0
		m.planFeedbackMode = false
		m.planFeedbackInput.Reset()
		m.state = StateProcessing
		if m.onPlanApproval != nil {
			m.onPlanApproval(PlanRejected)
		}
	case "m", "3":
		// Quick modify (3 = the numbered "Request changes" option) — enter feedback mode
		m.planFeedbackMode = true
		m.planFeedbackInput = NewInputModel(m.styles, m.workDir)
		m.planFeedbackInput.SetWidth(m.width - 4)
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

		// A slash command that needs a required argument drops into the inline
		// arg-entry step instead of running bare (which would only print Usage).
		if sel := m.commandPalette.GetSelected(); sel != nil && PaletteNeedsArg(*sel) {
			m.commandPalette.BeginArgEntry(*sel)
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
		if m.onOpenSettings != nil {
			m.onOpenSettings()
		}
	case paletteActionShortcuts:
		if m.shortcutsOverlay != nil {
			m.shortcutsOverlay.Show()
			m.state = StateShortcutsOverlay
		}
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
		if m.onPlanningModeToggle != nil {
			m.onPlanningModeToggle()
		}
	case paletteActionClearScreen:
		m.output.Clear()
	case paletteActionCompactMode:
		m.toggleCompactMode()
	}

	// If the action transitioned to its own modal/state, keep it; otherwise
	// fall back to the input so the palette doesn't strand the UI.
	if m.state == StateCommandPalette {
		m.state = StateInput
		return m.input.Focus()
	}
	return nil
}

// toggleCompactMode flips compact mode and resizes the output viewport. Shared
// by the Ctrl+Shift+C binding and the palette "Toggle Compact Mode" action so
// the two can't drift.
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
		m.state = StateInput
		return m.input.Focus()
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
	switch msg.String() {
	case "up", "k":
		if m.modelSelectedIndex > 0 {
			m.modelSelectedIndex--
		}
	case "down", "j":
		if m.modelSelectedIndex < len(m.availableModels)-1 {
			m.modelSelectedIndex++
		}
	case "enter", " ":
		// Select model
		if m.modelSelectedIndex >= 0 && m.modelSelectedIndex < len(m.availableModels) {
			selected := m.availableModels[m.modelSelectedIndex]
			if selected.ID != m.currentModel {
				m.currentModel = selected.ID
				m.output.AppendLine(m.styles.Spinner.Render(fmt.Sprintf("Switched to %s", selected.Name)))
				if m.onModelSelect != nil {
					m.onModelSelect(selected.ID)
				}
			}
			m.state = StateInput
			return m.input.Focus()
		}
	case "esc", "q":
		// Cancel selection
		m.state = StateInput
		return m.input.Focus()
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		// Quick select by number
		idx := int(msg.String()[0] - '1')
		if idx >= 0 && idx < len(m.availableModels) {
			selected := m.availableModels[idx]
			if selected.ID != m.currentModel {
				m.currentModel = selected.ID
				m.output.AppendLine(m.styles.Spinner.Render(fmt.Sprintf("Switched to %s", selected.Name)))
				if m.onModelSelect != nil {
					m.onModelSelect(selected.ID)
				}
			}
			m.state = StateInput
			return m.input.Focus()
		}
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

// handleGlobalKeys handles global keyboard shortcuts.
func (m *Model) handleGlobalKeys(msg tea.KeyMsg) tea.Cmd {
	// Ctrl+K opens the model selector. The welcome panel and model selector
	// both advertise this binding, so keep the handler global for the input
	// state instead of hiding model choice behind slash commands only.
	// keyConsumed (round 8), not nil: returning nil here falls through to
	// bubbles/textarea's OWN ctrl+k binding (DeleteAfterCursor) whenever
	// m.state is STILL StateInput when handleKeyMsg's caller checks
	// typeAheadActive() — normally openModelSelector() changes m.state away
	// from StateInput, making this moot, but its own early-return when
	// availableModels is empty leaves state unchanged, so ctrl+k with no
	// models loaded wiped everything after the cursor in the compose buffer.
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
		if m.onOpenSettings != nil {
			m.onOpenSettings()
		}
		return nil
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
	// fight the type-ahead input.
	if msg.Type == tea.KeyCtrlX && (m.state == StateInput || m.state == StateProcessing || m.state == StateStreaming) {
		if m.planProgressPanel != nil && m.planProgressPanel.IsVisible() {
			m.planProgressPanel.Toggle()
		}
		return nil
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
			return nil
		}
	}

	// Ctrl+J: insert newline (standard terminal newline, works in all terminals)
	// Ctrl+J inserts a newline — including DURING Processing/Streaming, where
	// the type-ahead compose box is live: gating to StateInput made multi-line
	// composing impossible exactly while the agent works (the textarea itself
	// does not bind ctrl+j, so the fall-through was a silent no-op).
	if msg.Type == tea.KeyCtrlJ &&
		(m.state == StateInput || m.state == StateProcessing || m.state == StateStreaming) {
		m.input.InsertNewline()
		return nil
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
					m.toastManager.ShowInfo("Tool output collapsed")
				} else {
					m.toastManager.ShowInfo("Tool output expanded")
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
			copyViaOSC52(m.lastResponseText)
			_ = clipboard.WriteAll(m.lastResponseText) // best-effort; OSC52 is primary
			if m.toastManager != nil {
				m.toastManager.ShowInfo("Copied last response")
			}
		}
		return keyConsumed
	}

	// Code block navigation and actions (only when input is empty)
	if m.state == StateInput && m.input.Value() == "" {
		codeBlocks := m.output.GetCodeBlocks()
		if codeBlocks != nil && codeBlocks.Count() > 0 {
			switch msg.String() {
			case "]":
				// Navigate to next code block. Consumed — in this context (empty
				// input + code blocks on screen) ']' is a nav key, not a char to
				// type; a nil return would leak ']' into the textarea.
				if codeBlocks.SelectNext() {
					m.toastManager.ShowInfo(codeBlocks.RenderSelectionIndicator())
				}
				return keyConsumed
			case "[":
				// Navigate to previous code block (see ']').
				if codeBlocks.SelectPrev() {
					m.toastManager.ShowInfo(codeBlocks.RenderSelectionIndicator())
				}
				return keyConsumed
			}
		}

		// '?' opens keyboard shortcuts overlay
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
		// If tool progress bar is visible and cancellable, cancel the operation
		if m.toolProgressBar != nil && m.toolProgressBar.IsVisible() && m.toolProgressBar.IsCancellable() {
			if m.onCancel != nil {
				m.onCancel()
			}
			m.toolProgressBar.Hide()
			m.state = StateInput
			m.currentTool = ""
			m.currentToolInfo = ""
			m.settleActiveToolCalls(false, "cancelled")
			m.output.AppendLine("")
			m.output.AppendLine(m.styles.Warning.Render(" Operation cancelled"))
			m.output.AppendLine("")
			return m.input.Focus()
		}
		// Double Ctrl+C confirmation: quit only if pressed twice within 2 seconds
		now := time.Now()
		if !m.quitConfirmTime.IsZero() && now.Sub(m.quitConfirmTime) < 3*time.Second {
			if m.onQuit != nil {
				m.onQuit()
			}
			return tea.Quit
		}
		m.quitConfirmTime = now
		if m.toastManager != nil {
			m.toastManager.ShowWarning("Press Ctrl+C again to quit")
		}

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
			// Capture elapsed BEFORE the reset clears streamStartTime —
			// surfacing "stopped after Xs" turns a generic cancel into a
			// useful signal ("how long was I waiting?").
			elapsed := time.Duration(0)
			if !m.streamStartTime.IsZero() {
				elapsed = time.Since(m.streamStartTime)
			}

			// Cancel the current processing (API request)
			if m.onCancel != nil {
				m.onCancel()
			}
			m.state = StateInput
			m.currentTool = ""
			m.currentToolInfo = ""
			m.processingLabel = ""
			m.streamIdleMsg = "" // Clear any "X is thinking" / "waiting for response" hint
			m.settleActiveToolCalls(false, "cancelled")
			m.streamStartTime = time.Time{} // Reset timeout tracking
			m.slowWarningShown = false
			// Hide tool progress bar if visible
			if m.toolProgressBar != nil {
				m.toolProgressBar.Hide()
			}

			// Cancellation is a user choice, not a warning — render in
			// ColorDim italic so the line records what happened without
			// the visual alarm of ⚠ Warning styling.
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
		if m.state == StateInput && !m.input.ShowingSuggestions() {
			value := m.input.Value()
			if value != "" {
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
			!m.isModalState() && !m.input.ShowingSuggestions() {
			value := m.input.Value()
			if value != "" {
				if time.Since(m.lastSubmitTime) < m.minSubmitDelay {
					return keyConsumed // swallowed Enter must not reach the textarea
				}
				m.lastSubmitTime = time.Now()

				// Expand collapsed-paste chips before Reset (clears the store);
				// echo stays collapsed (value), history + model get expanded.
				expanded := m.input.ExpandedValue()
				m.input.AddToHistory(expanded)
				m.input.Reset()
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
			!m.isModalState() && !m.input.ShowingSuggestions() {
			return keyConsumed
		}

	case tea.KeyCtrlL:
		// Clear output screen
		if m.state == StateInput {
			m.output.Clear()
			m.toastManager.ShowInfo("Output cleared")
			return nil
		}

	case tea.KeyCtrlU:
		// Clear input line
		if m.state == StateInput {
			m.input.Reset()
			return nil
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
			switch {
			case m.onSessionModeCycle != nil:
				m.onSessionModeCycle() // feedback via SessionModeCycledMsg
			case m.onPlanningModeToggle != nil:
				m.onPlanningModeToggle() // feedback via PlanningModeToggledMsg
			}
			return nil
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

func (m *Model) openModelSelector() {
	if len(m.availableModels) == 0 {
		if m.toastManager != nil {
			m.toastManager.ShowWarning("No model choices loaded")
		}
		return
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
		m.queuedPending = int(msg)
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
		}

		m.markResponseStarted()

		m.output.AppendThinkingStream(string(msg))

	case StreamTextMsg:
		m.streamStartTime = time.Now() // Reset timeout on streaming activity
		m.lastActivityTime = time.Now()
		m.slowWarningShown = false
		m.streamIdleMsg = "" // Server responded — clear idle warning
		if !m.isModalState() {
			m.state = StateStreaming
		}
		m.processingLabel = "" // Text streaming is the feedback itself

		// Close thinking block when text starts
		m.output.EndThinking()

		// Mark response as started (no header in Claude Code style)
		m.markResponseStarted()

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
		errStr := msg.Error()
		// Same modal-preservation rule as ResponseDoneMsg above — an error
		// from an UNRELATED foreground/background source must not silently
		// dismiss an active permission/question/plan-approval modal.
		modalActive := m.isModalState()
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
		m.currentActivity = string(msg)

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
			if m.onPermission != nil {
				m.onPermission(msg.ID, PermissionDeny)
			}
		} else {
			m.permRequest = &msg
			m.permSelectedOption = 0
			m.state = StatePermissionPrompt
			cmds = append(cmds, m.bellCmd())
		}

	case QuestionRequestMsg:
		if m.isModalState() {
			if m.onQuestion != nil {
				m.onQuestion("")
			}
		} else {
			m.questionRequest = &msg
			m.questionSelectedOption = 0
			m.questionCustomInput = false
			m.state = StateQuestionPrompt
			cmds = append(cmds, m.bellCmd())
			// If no options, initialize input model for free text
			if len(msg.Options) == 0 {
				m.questionInputModel = NewInputModel(m.styles, m.workDir)
				m.questionInputModel.SetWidth(m.width)
				cmds = append(cmds, m.questionInputModel.Focus())
			}
		}

	case PlanApprovalRequestMsg:
		if m.isModalState() {
			if m.onPlanApproval != nil {
				m.onPlanApproval(PlanRejected)
			}
		} else {
			m.planRequest = &msg
			m.planSelectedOption = 0
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
		} else if msg.Status == "in_progress" || msg.Status == "completed" {
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
			if m.onDiffDecision != nil {
				m.onDiffDecision(DiffReject)
			}
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
			if m.onMultiDiffDecision != nil {
				rejected := make(map[string]DiffDecision, len(msg.Files))
				for _, f := range msg.Files {
					rejected[f.FilePath] = DiffReject
				}
				m.onMultiDiffDecision(rejected)
			}
		} else {
			m.multiDiffRequest = &msg
			m.multiDiffPreview.SetSize(m.width, m.height)
			m.multiDiffPreview.SetFiles(msg.Files)
			m.state = StateMultiDiffPreview
		}

	case DiffPreviewResponseMsg:
		m.diffRequest = nil
		if msg.Decision == DiffApply || msg.Decision == DiffApplyAll {
			m.state = StateProcessing
			if m.onDiffDecision != nil {
				m.onDiffDecision(msg.Decision)
			}
		} else {
			m.state = StateInput
			m.output.AppendLine(m.styles.Warning.Render(" Changes rejected"))
			m.output.AppendLine("")
			if m.onDiffDecision != nil {
				m.onDiffDecision(msg.Decision)
			}
			cmds = append(cmds, m.input.Focus())
		}

	case MultiDiffPreviewResponseMsg:
		m.multiDiffRequest = nil
		allApplied := true
		for _, decision := range msg.Decisions {
			if decision != DiffApply {
				allApplied = false
				break
			}
		}
		if allApplied {
			m.state = StateProcessing
		} else {
			m.state = StateInput
			m.output.AppendLine(m.styles.Warning.Render(" Changes rejected"))
			m.output.AppendLine("")
			cmds = append(cmds, m.input.Focus())
		}
		if m.onMultiDiffDecision != nil {
			m.onMultiDiffDecision(msg.Decisions)
		}

	case SearchResultsRequestMsg:
		// Guard: don't clobber an active modal — this isn't a blocking call
		// (no decision channel to auto-resolve), so just drop it with a toast
		// rather than silently discarding whatever's currently showing.
		if m.isModalState() {
			if m.toastManager != nil {
				m.toastManager.ShowWarning("Search results ready but another prompt is active")
			}
		} else {
			m.searchRequest = &msg
			m.searchResults.SetSize(m.width, m.height)
			m.searchResults.SetResults(msg.Query, msg.Tool, msg.Results)
			m.state = StateSearchResults
		}

	case SearchResultsActionMsg:
		m.searchRequest = nil
		if msg.Action == SearchActionClose {
			m.state = StateInput
			cmds = append(cmds, m.input.Focus())
		} else {
			// Reset state for any other action (Open, Edit, CopyPath)
			m.state = StateInput
			cmds = append(cmds, m.input.Focus())
			if m.onSearchAction != nil {
				m.onSearchAction(msg.Action)
			}
		}

	case GitStatusRequestMsg:
		if m.isModalState() {
			if m.toastManager != nil {
				m.toastManager.ShowWarning("Git status ready but another prompt is active")
			}
		} else {
			m.gitStatusRequest = &msg
			m.gitStatusModel.SetSize(m.width, m.height)
			m.gitStatusModel.SetStatus(msg.Entries, msg.Branch, msg.Upstream, msg.AheadBehind)
			m.state = StateGitStatus
		}

	case GitStatusActionMsg:
		// Always reset state and request for any action
		m.gitStatusRequest = nil
		m.state = StateInput
		cmds = append(cmds, m.input.Focus())

		// Call action handler for non-close actions
		if msg.Action != GitActionClose && m.onGitAction != nil {
			m.onGitAction(msg.Action)
		}

	case FileBrowserRequestMsg:
		if m.isModalState() {
			if m.toastManager != nil {
				m.toastManager.ShowWarning("File browser requested but another prompt is active")
			}
		} else {
			m.fileBrowser.SetSize(m.width, m.height)
			if err := m.fileBrowser.SetPath(msg.StartPath); err == nil {
				m.fileBrowserActive = true
				m.state = StateFileBrowser
			} else {
				m.output.AppendLine(m.styles.FormatError(fmt.Sprintf("Could not open file browser: %s", err)))
				m.output.AppendLine("")
			}
		}

	case FileBrowserActionMsg:
		if msg.Action == FileBrowserActionClose {
			m.fileBrowserActive = false
			m.state = StateInput
			cmds = append(cmds, m.input.Focus())
		} else if msg.Action == FileBrowserActionOpen {
			m.fileBrowserActive = false
			m.state = StateInput
			cmds = append(cmds, m.input.Focus())
			if m.onFileSelect != nil {
				m.onFileSelect(msg.Path)
			}
		} else if msg.Action == FileBrowserActionSelect {
			m.fileBrowserActive = false
			m.state = StateInput
			cmds = append(cmds, m.input.Focus())
		}

	case ProgressUpdateMsg:
		m.progressModel.UpdateProgress(msg.Current, msg.CurrentItem, msg.Message)

	case ProgressCompleteMsg:
		m.progressModel.Complete()
		m.progressActive = false
		if m.state == StateBatchProgress {
			m.state = StateInput
			cmds = append(cmds, m.input.Focus())
		}

	case CloseOverlayMsg:
		m.state = StateInput
		cmds = append(cmds, m.input.Focus())

	// Config update message - refresh UI state
	case ConfigUpdateMsg:
		m.permissionsEnabled = msg.PermissionsEnabled
		m.sandboxEnabled = msg.SandboxEnabled
		m.planningModeEnabled = msg.PlanningModeEnabled
		if msg.ModelName != "" {
			m.currentModel = msg.ModelName
			setTerminalTitle(fmt.Sprintf("Gokin · %s · %s", m.currentModel, shortenPath(m.workDir, 30)))
		}

	// Open the interactive settings modal (/settings)
	case OpenSettingsMsg:
		// Guard: Ctrl+S/the /settings command dispatches this async from a
		// different goroutine than whatever else may be mid-modal (e.g. a
		// background /loop iteration's permission prompt). Opening
		// unconditionally would silently clobber an active
		// Permission/Question/PlanApproval/Diff prompt, orphaning its blocked
		// decision channel until timeout/ctx cancellation.
		if m.isModalState() {
			if m.toastManager != nil {
				m.toastManager.ShowWarning("Resolve the active prompt before opening Settings")
			}
		} else {
			m.openSettings(msg)
		}

	// Open the masked API-key entry modal (/login <provider> with no key)
	case OpenKeyEntryMsg:
		if m.isModalState() {
			if m.toastManager != nil {
				m.toastManager.ShowWarning("Resolve the active prompt before entering an API key")
			}
		} else {
			cmds = append(cmds, m.openKeyEntry(msg))
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
			if m.toastManager != nil && msg.Message != "" {
				// Collapse consecutive retries into a single live toast so rapid
				// attempts don't stack up and push other toasts off screen.
				m.toastManager.ShowTagged("retry", ToastWarning, msg.Message, 4*time.Second)
			}
		case StatusRateLimit:
			if m.toastManager != nil {
				m.toastManager.ShowWarning(msg.Message)
			}
			if wt, ok := msg.Details["waitTime"].(time.Duration); ok {
				m.rateLimitWaitUntil = time.Now().Add(wt)
			}
		case StatusStreamIdle:
			firstWarning := m.streamIdleMsg == ""
			if msg.Message != "" {
				m.streamIdleMsg = msg.Message
			}
			if firstWarning && m.toastManager != nil && msg.Message != "" {
				m.toastManager.ShowWarning(msg.Message)
			}
		case StatusStreamResume:
			m.streamIdleMsg = ""
			m.retryAttempt = 0
			m.retryMax = 0
			m.rateLimitWaitUntil = time.Time{}
		case StatusRecoverableError:
			if m.toastManager != nil {
				// Use the hint-enhanced variant so the toast tells the user
				// what to try next, not just what went wrong.
				m.toastManager.ShowErrorWithHint(msg.Message)
			}
		case StatusCancelled:
			// Cancellation: clear any in-flight idle/thinking hints so the user
			// sees a clean state while the operation winds down.
			m.streamIdleMsg = ""
			m.currentTool = ""
			m.currentToolInfo = ""
			m.settleActiveToolCalls(false, msg.Message)
			if m.toastManager != nil {
				m.toastManager.ShowWarning(msg.Message)
			}
		case StatusWarning:
			if m.toastManager != nil && msg.Message != "" {
				if tag, _ := msg.Details["tag"].(string); tag != "" {
					m.toastManager.ShowTagged(tag, ToastWarning, msg.Message, 4*time.Second)
				} else {
					m.toastManager.ShowWarning(msg.Message)
				}
			}
		case StatusInfo:
			if label, _ := msg.Details["phaseLabel"].(string); strings.TrimSpace(label) != "" {
				m.processingLabel = strings.TrimSpace(label)
				m.state = StateProcessing
				m.streamStartTime = time.Now()
				m.lastActivityTime = time.Now()
				m.slowWarningShown = false
				m.streamIdleMsg = ""
			}
			silent, _ := msg.Details["silent"].(bool)
			if !silent && m.toastManager != nil && msg.Message != "" {
				m.toastManager.Show(ToastInfo, "", msg.Message, 4*time.Second)
			}
		case StatusThinkingIdle:
			// Model is deliberately thinking — show distinct from generic "idle"
			firstWarning := m.streamIdleMsg == ""
			if msg.Message != "" {
				m.streamIdleMsg = msg.Message
			}
			if firstWarning && m.toastManager != nil && msg.Message != "" {
				m.toastManager.Show(ToastInfo, "", msg.Message, 6*time.Second)
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
		titleLine := m.styles.FormatToolFailureLine(name, conciseToolTarget(toolName, toolInfo), duration)
		m.emitToolResultCard(titleLine, failureBody(detail, toolName))
		return
	}

	titleLine := m.styles.FormatToolLine(name, target, outcome, duration)

	// Body (expand-state aware) — built once, fed either into a bordered
	// card or the legacy 4-space-indented flat form depending on width.
	body := m.buildToolResultBody(toolName, toolInfo, content, contentStyle)

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

	// Toast notifications (top of screen) - single line, minimal
	if m.toastManager != nil && m.toastManager.Count() > 0 {
		toasts := m.toastManager.View(m.width)
		if toasts != "" {
			builder.WriteString(toasts)
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
	if m.output.IsEmpty() && m.state == StateInput {
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

	// Plan progress panel (when actively executing a plan)
	if m.planProgressPanel != nil && m.planProgressPanel.IsVisible() {
		panelBuilder.WriteString(m.planProgressPanel.View(m.width))
		panelBuilder.WriteString("\n")
	}

	if m.planPauseNotice != nil {
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

		if m.currentTool != "" {
			// Tool execution: spinner + name in tool's color + info
			toolColor := GetToolIconColor(m.currentTool)
			spinnerStyle := lipgloss.NewStyle().Foreground(toolColor)
			spinner := spinnerStyle.Render(spinnerFrames[frameIdx])

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
			spinner := spinnerStyle.Render(spinnerFrames[frameIdx])
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
		observatoryView := m.observatoryPanel.View(m.width)
		if observatoryView != "" {
			builder.WriteString("\n")
			builder.WriteString(observatoryView)
			builder.WriteString("\n")
		}
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
	if m.CompactMode {
		m.output.SetSize(m.width, max(m.height/3, 1))
	} else {
		m.output.SetSize(m.width, max(m.height-5, 1))
	}
	if m.planFeedbackMode {
		m.planFeedbackInput.SetWidth(max(m.width-4, 1))
	}
	if m.state == StateQuestionPrompt {
		m.questionInputModel.SetWidth(max(m.width-4, 1))
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
	m.coordinatedTaskOrder = append(m.coordinatedTaskOrder, msg.TaskID)
	m.activeCoordinatedTask = msg.TaskID

	m.output.AppendLine("")
	m.output.AppendLine(m.renderTaskTimelineStart(len(m.coordinatedTaskOrder), msg.PlanType, msg.Message))
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
	if ts, ok := m.coordinatedTasks[msg.TaskID]; ok {
		ts.Duration = msg.Duration
		ts.Error = msg.Error
		ts.Progress = 1.0
		if msg.Success {
			ts.Status = "completed"
		} else {
			ts.Status = "failed"
		}
		ts.EndTime = time.Now()

		m.output.AppendLine(m.renderTaskTimelineDone(msg.Success, msg.Duration.Round(time.Millisecond), msg.Error))
	}

	if m.activeCoordinatedTask == msg.TaskID {
		m.activeCoordinatedTask = ""
	}

	// Schedule auto-removal after 5 seconds
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg {
		return CoordinatedTaskCleanupMsg{ID: msg.TaskID}
	})
}

// handleTaskProgress handles the TaskProgressEvent message.
func (m *Model) handleTaskProgress(msg TaskProgressEvent) {
	if taskState, ok := m.coordinatedTasks[msg.TaskID]; ok {
		taskState.Progress = msg.Progress
		taskState.Message = msg.Message
		progressBucket := timelineProgressBucket(msg.Progress)
		progressMessage := normalizeTimelineText(msg.Message)
		if progressBucket == taskState.LastProgressBucket && progressMessage == taskState.LastProgressMessage {
			return
		}
		taskState.LastProgressBucket = progressBucket
		taskState.LastProgressMessage = progressMessage
		m.output.AppendLine(m.renderTaskTimelineProgress(msg.Progress, msg.Message))
	}
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
