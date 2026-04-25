package ui

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/spinner"
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

	// Session timing and cost
	sessionStart time.Time
	sessionCost  float64 // Cumulative USD cost this session

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
	modelSelectorOpen  bool
	modelSelectedIndex int
	availableModels    []ModelInfo
	currentModel       string
	onModelSelect      func(modelID string)

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
	onPermission               func(decision PermissionDecision)
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
	commandPalette *CommandPalette

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

	// Compact mode
	CompactMode bool

	// Status line contextual information
	injectedContextCount int
	conversationMode     string // exploring/implementing/debugging
	mcpHealthy           int
	mcpTotal             int
	runtimeStatus        RuntimeStatusSnapshot
	lastRuntimeRefresh   time.Time

	// Plan pause/resume UX block
	planPauseNotice *PlanProgressMsg

	// Syntax highlighting for tool output
	highlighter *highlight.Highlighter

	// Response timing breakdown
	responseToolDuration time.Duration // Total time spent in tool calls during current response
	responseToolCount    int           // Number of tool calls in current response

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
	activityID string    // ID in activity feed
	name       string    // tool name
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
	Duration            time.Duration
	Error               error
	LastProgressBucket  int
	LastProgressMessage string
}

// NewModel creates a new TUI model.
func NewModel() *Model {
	styles := DefaultStyles()

	// Auto-detect terminal background and apply appropriate theme
	if lipgloss.HasDarkBackground() {
		if runtime.GOOS == "darwin" {
			styles.ApplyTheme(ThemeMacOS)
		}
		// else: ThemeDark already default
	} else {
		styles.ApplyTheme(ThemeLight)
	}

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
			m.activeToolCalls = nil
			m.responseHeaderShown = false
			m.output.FlushStream() // Flush any remaining streamed content
			m.output.AppendLine("")
			m.output.AppendLine(m.styles.FormatError(fmt.Sprintf("request timed out after %v", m.streamTimeout)))
			m.output.AppendLine("")
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

	default:
		// Handle message types
		cmd := m.handleMessageTypes(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	// Update input only in StateInput — prevents key leak into input during modals/processing
	if m.state == StateInput && !m.isModalState() {
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

	// Handle shortcuts overlay keys
	if m.state == StateShortcutsOverlay {
		// Any key closes the overlay
		m.state = StateInput
		return m.input.Focus()
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
		// User made a decision
		decision := PermissionDecision(m.permSelectedOption)
		m.permRequest = nil
		m.permSelectedOption = 0
		// Deny decisions return to input, allow continues processing
		if decision == PermissionDeny || decision == PermissionDenySession {
			m.state = StateInput
			m.output.AppendLine(m.styles.Warning.Render(" Denied - operation cancelled"))
			m.output.AppendLine("")
			if m.onInterrupt != nil {
				m.onInterrupt()
			}
			if m.onPermission != nil {
				m.onPermission(decision)
			}
			return m.input.Focus()
		}
		m.state = StateProcessing
		if m.onPermission != nil {
			m.onPermission(decision)
		}
	case "y":
		// Quick allow
		m.permRequest = nil
		m.permSelectedOption = 0
		m.state = StateProcessing
		if m.onPermission != nil {
			m.onPermission(PermissionAllow)
		}
	case "n", "esc":
		// Quick deny / ESC cancels
		m.permRequest = nil
		m.permSelectedOption = 0
		m.state = StateInput
		m.output.AppendLine(m.styles.Warning.Render(" Denied - operation cancelled"))
		m.output.AppendLine("")
		if m.onInterrupt != nil {
			m.onInterrupt()
		}
		if m.onPermission != nil {
			m.onPermission(PermissionDeny)
		}
		return m.input.Focus()
	case "a":
		// Allow for session
		m.permRequest = nil
		m.permSelectedOption = 0
		m.state = StateProcessing
		if m.onPermission != nil {
			m.onPermission(PermissionAllowSession)
		}
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
		m.planRequest = nil
		m.planSelectedOption = 0
		m.state = StateProcessing
		if m.onPlanApproval != nil {
			m.onPlanApproval(decision)
		}
	case "y":
		// Quick approve
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
	case "n":
		// Quick reject
		m.planRequest = nil
		m.planSelectedOption = 0
		m.planFeedbackMode = false
		m.planFeedbackInput.Reset()
		m.state = StateProcessing
		if m.onPlanApproval != nil {
			m.onPlanApproval(PlanRejected)
		}
	case "m":
		// Quick modify - enter feedback mode
		m.planFeedbackMode = true
		m.planFeedbackInput = NewInputModel(m.styles, m.workDir)
		m.planFeedbackInput.SetWidth(m.width - 4)
		m.planFeedbackInput.SetPlaceholder("Enter your feedback for plan modifications...")
		return m.planFeedbackInput.Focus()
	case "esc":
		// ESC to interrupt plan approval and return to input with context
		m.planRequest = nil
		m.planSelectedOption = 0
		m.planFeedbackMode = false
		m.state = StateInput
		m.output.AppendLine("")
		m.output.AppendLine(m.styles.Warning.Render(" Plan approval interrupted"))
		m.output.AppendLine(lipgloss.NewStyle().Foreground(ColorInfo).Render(" You can now provide feedback or modification requests"))
		m.output.AppendLine("")
		if m.onInterrupt != nil {
			m.onInterrupt()
		}
		return m.input.Focus()
	}
	return nil
}

// handleCommandPaletteKeys handles keys in command palette state.
func (m *Model) handleCommandPaletteKeys(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEscape:
		m.commandPalette.Hide()
		m.state = StateInput
		return m.input.Focus()

	case tea.KeyEnter:
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
				m.output.AppendLine(m.styles.FormatUserMessage(cmd.Shortcut))
				m.output.AppendLine("")
				if m.onSubmit != nil {
					m.onSubmit(cmd.Shortcut)
				}
				return nil
			}
		case CommandTypeAction:
			if cmd.Action != nil {
				cmd.Action()
			}
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
		if m.modelSelectedIndex < len(m.availableModels) {
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
		if idx < len(m.availableModels) {
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

// handleGlobalKeys handles global keyboard shortcuts.
func (m *Model) handleGlobalKeys(msg tea.KeyMsg) tea.Cmd {
	// Handle Ctrl+P for command palette (only when in input state)
	if msg.Type == tea.KeyCtrlP && m.state == StateInput {
		m.commandPalette.Show()
		m.state = StateCommandPalette
		return nil
	}

	// Handle Ctrl+O for activity feed toggle
	if msg.Type == tea.KeyCtrlO && m.state == StateInput {
		if m.activityFeed != nil {
			m.activityFeed.Toggle()
		}
		return nil
	}

	// Handle Ctrl+A for agent tree panel toggle
	if msg.Type == tea.KeyCtrlA && m.state == StateInput {
		if m.agentTreePanel != nil {
			m.agentTreePanel.Toggle()
		}
		return nil
	}

	// Handle Ctrl+H for context observatory dashboard
	if msg.Type == tea.KeyCtrlH && m.state == StateInput {
		if m.observatoryPanel != nil {
			m.observatoryPanel.Toggle()
			if m.observatoryPanel.IsVisible() {
				m.state = StateContextObservatory
			}
		}
		return nil
	}

	// Handle Ctrl+T for todos toggle
	if msg.Type == tea.KeyCtrlT && m.state == StateInput {
		m.todosVisible = !m.todosVisible
		return nil
	}

	// Scroll shortcuts (work in input and streaming states)
	if m.state == StateInput || m.state == StateStreaming || m.state == StateProcessing {
		switch msg.String() {
		case "ctrl+b": // Scroll up
			newOffset := m.output.viewport.YOffset - 3
			if newOffset < 0 {
				newOffset = 0
			}
			m.output.viewport.SetYOffset(newOffset)
			m.output.SetFrozen(true)
			return nil
		case "ctrl+f": // Scroll down
			newOffset := m.output.viewport.YOffset + 3
			maxOffset := m.output.viewport.TotalLineCount() - m.output.viewport.Height
			if maxOffset < 0 {
				maxOffset = 0
			}
			if newOffset > maxOffset {
				newOffset = maxOffset
			}
			m.output.viewport.SetYOffset(newOffset)
			// Unfreeze if at bottom
			total := m.output.viewport.TotalLineCount()
			bottom := m.output.viewport.YOffset + m.output.viewport.Height
			if total-bottom <= 2 {
				m.output.SetFrozen(false)
			}
			return nil
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
	if msg.Type == tea.KeyCtrlJ && m.state == StateInput {
		m.input.InsertNewline()
		return nil
	}

	// Ctrl+U: context-aware — clear input when has text, scroll half-page up when empty
	if msg.Type == tea.KeyCtrlU && m.state == StateInput {
		if m.input.textarea.Value() == "" {
			newOffset := m.output.viewport.YOffset - m.output.viewport.Height/2
			if newOffset < 0 {
				newOffset = 0
			}
			m.output.viewport.SetYOffset(newOffset)
			m.output.SetFrozen(true)
			return nil
		}
		// Fall through to default handler (clears input)
	}

	// Handle Ctrl+Shift+C for compact mode toggle (only when in input state)
	if msg.String() == "ctrl+shift+c" && m.state == StateInput {
		m.CompactMode = !m.CompactMode
		if m.CompactMode {
			h := m.height / 3
			if h < 3 {
				h = 3
			}
			m.output.SetSize(m.width, h)
		} else {
			h := m.height - 5
			if h < 3 {
				h = 3
			}
			m.output.SetSize(m.width, h)
		}
		return nil
	}

	// Handle 'E' (shift+e) for toggle all tool outputs (only when input is empty)
	if msg.String() == "E" && m.state == StateInput && m.input.Value() == "" {
		if m.toolOutput != nil && m.toolOutput.EntryCount() > 0 {
			m.toolOutput.ToggleAll()
			if m.toolOutput.AllExpanded {
				m.toastManager.ShowInfo("All tool outputs expanded")
			} else {
				m.toastManager.ShowInfo("All tool outputs collapsed")
			}
		}
		return nil
	}

	// Handle 'e' key for tool output expand/collapse (only when input is empty)
	if msg.String() == "e" && m.state == StateInput && m.input.Value() == "" {
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
			}
		}
		return nil
	}

	// Option+C: copy last AI response to clipboard
	if msg.String() == "alt+c" && m.state == StateInput {
		if m.lastResponseText != "" {
			copyViaOSC52(m.lastResponseText)
			_ = clipboard.WriteAll(m.lastResponseText) // best-effort; OSC52 is primary
			if m.toastManager != nil {
				m.toastManager.ShowInfo("Copied last response")
			}
		}
		return nil
	}

	// Code block navigation and actions (only when input is empty)
	if m.state == StateInput && m.input.Value() == "" {
		codeBlocks := m.output.GetCodeBlocks()
		if codeBlocks != nil && codeBlocks.Count() > 0 {
			switch msg.String() {
			case "]":
				// Navigate to next code block
				if codeBlocks.SelectNext() {
					m.toastManager.ShowInfo(codeBlocks.RenderSelectionIndicator())
				}
				return nil
			case "[":
				// Navigate to previous code block
				if codeBlocks.SelectPrev() {
					m.toastManager.ShowInfo(codeBlocks.RenderSelectionIndicator())
				}
				return nil
			}
		}

		// '?' opens keyboard shortcuts overlay
		if msg.String() == "?" {
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
			m.activeToolCalls = nil
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
			// Cancel the current processing (API request)
			if m.onCancel != nil {
				m.onCancel()
			}
			m.state = StateInput
			m.currentTool = ""
			m.currentToolInfo = ""
			m.processingLabel = ""
			m.streamIdleMsg = "" // Clear any "X is thinking" / "waiting for response" hint
			m.activeToolCalls = nil
			m.streamStartTime = time.Time{} // Reset timeout tracking
			m.slowWarningShown = false
			// Hide tool progress bar if visible
			if m.toolProgressBar != nil {
				m.toolProgressBar.Hide()
			}
			m.output.AppendLine("")
			m.output.AppendLine(m.styles.Warning.Render(" Interrupted - request cancelled"))
			m.output.AppendLine("")
			if m.onInterrupt != nil {
				m.onInterrupt()
			}
			return m.input.Focus()
		}

	case tea.KeyEnter:
		// Alt+Enter: insert newline for multi-line input
		if m.state == StateInput && msg.Alt {
			m.input.InsertNewline()
			return nil
		}
		// Send message on Enter (when input is not empty and no suggestions are shown)
		if m.state == StateInput && !m.input.ShowingSuggestions() {
			value := m.input.Value()
			if value != "" {
				// Rate limiting: prevent rapid message spam
				if time.Since(m.lastSubmitTime) < m.minSubmitDelay {
					return nil // Ignore too-fast submissions
				}
				m.lastSubmitTime = time.Now()

				m.input.AddToHistory(value) // Save to history
				m.input.Reset()
				m.state = StateProcessing
				m.streamStartTime = time.Now()  // Start timeout tracking
				m.lastActivityTime = time.Now() // Start activity tracking
				m.slowWarningShown = false      // Reset slow warning
				m.responseHeaderShown = false   // Reset for new response
				m.responseToolDuration = 0      // Reset tool timing for new response
				m.responseToolCount = 0
				m.output.AppendLine(m.styles.FormatUserMessage(value))
				m.output.AppendLine("")

				if m.onSubmit != nil {
					m.onSubmit(value)
				}
				return nil
			}
		}

	case tea.KeyCtrlL:
		// Clear output screen
		if m.state == StateInput {
			m.output.Clear()
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
		return cmd
	}

	return nil
}

// handleMessageTypes handles various message types.
func (m *Model) handleMessageTypes(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case ContextHealthMsg:
		if m.observatoryPanel != nil {
			m.observatoryPanel.UpdateHealth(msg)
		}
		return nil

	case StreamThinkingMsg:
		m.streamStartTime = time.Now()
		m.lastActivityTime = time.Now()
		m.slowWarningShown = false
		m.state = StateStreaming

		if !m.responseHeaderShown {
			m.responseHeaderShown = true
		}

		m.output.AppendThinkingStream(string(msg))

	case StreamTextMsg:
		m.streamStartTime = time.Now() // Reset timeout on streaming activity
		m.lastActivityTime = time.Now()
		m.slowWarningShown = false
		m.streamIdleMsg = "" // Server responded — clear idle warning
		m.state = StateStreaming
		m.processingLabel = "" // Text streaming is the feedback itself

		// Close thinking block when text starts
		m.output.EndThinking()

		// Mark response as started (no header in Claude Code style)
		if !m.responseHeaderShown {
			m.responseHeaderShown = true
		}

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
		if !m.responseHeaderShown {
			m.responseHeaderShown = true
		}

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

		// Show tool call in output with enhanced block formatting
		m.output.AppendLine(m.styles.FormatToolExecutingBlock(msg.Name, msg.Args))

	case ToolResultMsg:
		m.streamStartTime = time.Now() // Reset timeout on tool result
		m.lastActivityTime = time.Now()
		m.slowWarningShown = false

		// Find the first matching active tool call by name
		matchIdx := -1
		for i, tc := range m.activeToolCalls {
			if tc.name == msg.Name {
				matchIdx = i
				break
			}
		}

		if matchIdx >= 0 {
			matched := m.activeToolCalls[matchIdx]

			// Track tool execution time for response timing breakdown
			if !matched.startTime.IsZero() {
				m.responseToolDuration += time.Since(matched.startTime)
				m.responseToolCount++
			}

			// Complete entry in activity feed with result summary
			if m.activityFeed != nil {
				summary := GenerateResultSummary(matched.name, msg.Content)
				m.activityFeed.CompleteEntry(matched.activityID, true, summary)
			}

			// Handle tool result using matched tool's info and timing
			m.handleToolResultWithInfo(msg.Content, matched.name, matched.info, matched.startTime)

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
			m.handleToolResultWithInfo(msg.Content, msg.Name, "", time.Time{})
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
		m.state = StateInput
		m.currentTool = ""
		m.currentToolInfo = ""
		m.processingLabel = ""
		m.streamIdleMsg = "" // Clear idle warning
		m.loopIteration = 0
		m.loopToolsUsed = 0
		m.activeToolCalls = nil
		m.streamStartTime = time.Time{}  // Reset timeout tracking
		m.lastActivityTime = time.Time{} // Reset activity tracking
		m.slowWarningShown = false       // Reset slow warning
		m.responseHeaderShown = false    // Reset for next response
		m.lastErrorMsg = ""              // Reset error dedup
		m.lastErrorCount = 0
		m.responseToolDuration = 0 // Reset tool timing
		m.responseToolCount = 0
		if m.currentResponseBuf.Len() > 0 {
			m.lastResponseText = m.currentResponseBuf.String()
			m.currentResponseBuf.Reset()
		}
		m.output.FlushStream() // Flush any remaining streamed content
		m.output.AppendLine("")
		cmds = append(cmds, m.input.Focus())

	case ResponseMetadataMsg:
		// Track request latency for status bar
		if msg.Duration > 0 {
			m.lastRequestLatency = msg.Duration
		}
		// Accumulate session cost
		if msg.Cost > 0 {
			m.sessionCost += msg.Cost
		}
		// Render response metadata footer (or minimal separator if no data)
		footer := m.renderResponseMetadata(msg)
		if footer == "" {
			dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
			footer = dimStyle.Render("───")
		}
		m.output.AppendLine(footer)
		m.output.AppendLine("")

	case ErrorMsg:
		m.state = StateInput
		m.currentTool = ""
		m.currentToolInfo = ""
		m.processingLabel = ""
		m.streamIdleMsg = "" // Clear idle warning
		m.loopIteration = 0
		m.loopToolsUsed = 0
		m.activeToolCalls = nil
		m.streamStartTime = time.Time{}  // Reset timeout tracking
		m.lastActivityTime = time.Time{} // Reset activity tracking
		m.slowWarningShown = false       // Reset slow warning
		m.responseHeaderShown = false    // Reset for next response
		m.responseToolDuration = 0       // Reset tool timing
		m.responseToolCount = 0
		m.currentResponseBuf.Reset() // Discard partial response on error
		m.output.FlushStream()       // Flush any remaining streamed content

		errStr := msg.Error()
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
		cmds = append(cmds, m.input.Focus())

	case TodoUpdateMsg:
		m.todoItems = msg

	case LoopIterationMsg:
		m.loopIteration = msg.Iteration
		m.loopToolsUsed = msg.ToolsUsed

	case StreamTokenUpdateMsg:
		if m.tokenUsage != nil {
			// Track estimated output tokens separately from input context.
			// Output tokens don't count toward the current context limit
			// (they become input on the next turn).
			m.tokenUsage.OutputTokens = msg.EstimatedOutputTokens
			m.tokenUsage.IsEstimate = true
		}

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
		// Auto-deny so the blocked caller doesn't hang until timeout.
		if m.isModalState() {
			if m.onPermission != nil {
				m.onPermission(PermissionDeny)
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
		m.diffRequest = &msg
		m.diffPreview.SetSize(m.width, m.height)
		m.diffPreview.SetContent(msg.FilePath, msg.OldContent, msg.NewContent, msg.ToolName, msg.IsNewFile)
		m.state = StateDiffPreview

	case MultiDiffPreviewRequestMsg:
		m.multiDiffRequest = &msg
		m.multiDiffPreview.SetSize(m.width, m.height)
		m.multiDiffPreview.SetFiles(msg.Files)
		m.state = StateMultiDiffPreview

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
		m.searchRequest = &msg
		m.searchResults.SetSize(m.width, m.height)
		m.searchResults.SetResults(msg.Query, msg.Tool, msg.Results)
		m.state = StateSearchResults

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
		m.gitStatusRequest = &msg
		m.gitStatusModel.SetSize(m.width, m.height)
		m.gitStatusModel.SetStatus(msg.Entries, msg.Branch, msg.Upstream, msg.AheadBehind)
		m.state = StateGitStatus

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
		m.fileBrowser.SetSize(m.width, m.height)
		if err := m.fileBrowser.SetPath(msg.StartPath); err == nil {
			m.fileBrowserActive = true
			m.state = StateFileBrowser
		} else {
			m.output.AppendLine(m.styles.FormatError(fmt.Sprintf("Could not open file browser: %s", err)))
			m.output.AppendLine("")
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

	// Coordinated task events (Phase 2)
	case TaskStartedEvent:
		m.handleTaskStarted(msg)
	case TaskCompletedEvent:
		m.handleTaskCompleted(msg)
	case TaskProgressEvent:
		m.handleTaskProgress(msg)

	// Background task tracking
	case BackgroundTaskMsg:
		cmds = append(cmds, m.handleBackgroundTask(msg))

	// Background task progress updates
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
			description := ""
			if task, ok := m.backgroundTasks[msg.AgentID]; ok {
				description = task.Description
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
			// Show tool call inline with agent type prefix
			m.output.AppendLine(m.styles.FormatAgentToolCall(msg.AgentType, msg.ToolName, msg.ToolArgs))
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
			// Between tool calls, show "thinking" with tool count so user knows agent is alive
			m.processingLabel = fmt.Sprintf("Agent: %s · thinking (%d tools used)", msg.AgentType, m.agentToolCount)
			m.currentTool = ""
			m.currentToolInfo = ""
			m.lastActivityTime = time.Now()
		case "complete":
			m.processingLabel = ""
			m.currentTool = ""
			m.currentToolInfo = ""
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
			if m.toastManager != nil && msg.Message != "" {
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
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			preview := cmd
			if len(preview) > 60 {
				preview = preview[:57] + "..."
			}
			preview = strings.ReplaceAll(preview, "\n", " ")
			return "$ " + preview
		}
	case "grep":
		if pattern, ok := args["pattern"].(string); ok {
			info := pattern
			if len(info) > 30 {
				info = info[:27] + "..."
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
// hint. For these tools the ✓ success line itself (with line count and
// path) carries all the useful signal; the content payload is either
// redundant (Read — the file is on disk, ask for it if you want) or
// self-descriptive elsewhere.
//
// bash / grep / glob deliberately stay uncollapsed: their output IS the
// user's primary signal and a head+tail preview is worth the rows.
func collapsedByDefault(toolName string) bool {
	switch toolName {
	case "read":
		return true
	}
	return false
}

// handleToolResultWithInfo handles tool result message with clean, minimal formatting.
// It uses the provided tool name, info, and start time from the matched active call
// rather than relying on m.currentTool/m.toolStartTime (which may have been overwritten
// by later parallel tool calls).
func (m *Model) handleToolResultWithInfo(content, toolName, toolInfo string, startTime time.Time) {
	contentStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	hintStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)

	// Build summary and duration
	var summary string
	var duration time.Duration
	if toolName != "" {
		summary = generateToolResultSummary(toolName, content, toolInfo)
	}
	if !startTime.IsZero() {
		duration = time.Since(startTime)
	}

	name := toolName
	if name == "" {
		name = "tool"
	}

	{
		m.output.AppendLine("    " + m.styles.FormatToolSuccessBlock(name, duration, summary))
	}

	if content == "" {
		// No content to show
		m.output.AppendLine("")
		return
	}

	// Store for expand/collapse
	expanded := false
	needsTruncation := false
	compactLongOutput := false
	if m.toolOutput != nil {
		m.lastToolOutputIndex = m.toolOutput.AddEntry(toolName, content)
		expanded = m.toolOutput.IsExpanded(m.lastToolOutputIndex)
		needsTruncation = m.toolOutput.NeedsTruncation(content)
		// Read results are collapsed by default — the "✓ Read 100 lines
		// from X" success line above already tells you what happened;
		// inlining 10 lines of random head+tail usually isn't signal,
		// just noise. User can press `e` to expand. Other tools (bash /
		// grep / glob) keep the head+tail preview because their output
		// IS the primary signal.
		//
		// Global compact mode (user toggled via `E` / Ctrl+E) still
		// forces the same behaviour for all tools.
		compactLongOutput = needsTruncation && !expanded &&
			(m.toolOutput.CompactModeActive() || collapsedByDefault(toolName))
	}

	if compactLongOutput {
		// Single minimal hint — the ✓ Read success line above already
		// carries the line count + path + duration, so we don't repeat
		// the "[read: 100 lines]" GetSummary here. Keeps the output to
		// TWO rows (✓ header + expand hint) per Read.
		m.output.AppendLine("    " + hintStyle.Render("⎿ press e to expand"))
		m.output.AppendLine("")
		return
	}

	// The "Preview below - press e for full output" hint that used to sit
	// here is gone: the "... +N lines ..." marker inside the preview plus
	// the status-bar's `e last output` hint already signal the same thing.
	// Showing it as a separate line in front of *every* truncated read
	// stole a row and visually separated the header ("✓ Read 100 lines
	// from X") from the actual content — readers perceived it as noise.

	// Content lines (preview or full) with simple indent.
	displayContent := FormatToolOutput(content, 6, expanded)

	// Apply syntax highlighting for read tool AFTER preview/full selection.
	highlighted := false
	if toolName == "read" && toolInfo != "" && m.highlighter != nil {
		lang := m.highlighter.DetectLanguage(toolInfo)
		if lang != "" && lang != "text" {
			displayContent = m.highlighter.Highlight(displayContent, lang)
			highlighted = true
		}
	}

	lines := strings.Split(displayContent, "\n")
	for _, line := range lines {
		if line != "" {
			if highlighted {
				// Already has ANSI color codes — don't wrap in contentStyle
				m.output.AppendLine("    " + line)
			} else {
				m.output.AppendLine("    " + contentStyle.Render(line))
			}
		}
	}
	m.output.AppendLine("")
}

// View renders the TUI.
func (m Model) View() string {
	var builder strings.Builder

	// Toast notifications (top of screen) - single line, minimal
	if m.toastManager != nil && m.toastManager.Count() > 0 {
		toasts := m.toastManager.View(m.width)
		if toasts != "" {
			builder.WriteString(toasts)
			builder.WriteString("\n")
		}
	}

	// Output viewport (dimmed when modal is active)
	outputView := m.output.View()
	isModal := m.isModalState()
	if isModal {
		outputView = lipgloss.NewStyle().Faint(true).Render(outputView)
	}
	builder.WriteString(outputView)
	builder.WriteString("\n")

	// Welcome hint when output is empty and user is at input.
	// Mentions the active session mode so a fresh user isn't surprised
	// when their first message triggers a plan-mode approval flow
	// instead of immediate execution. Modes are visually distinct in
	// the status bar too — this hint just makes the connection
	// explicit on the very first interaction.
	if m.output.IsEmpty() && m.state == StateInput {
		dimStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
		hint := getTextSelectionHint()
		modeHint := ""
		switch {
		case m.planningModeEnabled:
			modeHint = "Plan mode is on — agent will explore, then propose a plan for approval."
		case !m.permissionsEnabled || !m.sandboxEnabled:
			modeHint = "YOLO mode — agent runs everything without asking. Shift+Tab to step back."
		}
		base := "  Type a message to start. Press ? for shortcuts. " + hint
		if modeHint != "" {
			base += "\n  " + modeHint
		}
		builder.WriteString(dimStyle.Render(base))
		builder.WriteString("\n\n")
	}

	// Background panels (dimmed when modal is active)
	var panelBuilder strings.Builder

	// Live "Now" card — compact summary of what the agent is doing right now.
	// The card's content overlaps with the processing/tool spinner-status
	// block rendered further below (same spinner, same label, same tool
	// info), so we track whether the card actually drew anything and
	// suppress the duplicate block in that case.
	cardRendered := false
	if card := m.renderLiveActivityCard(); card != "" {
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

	// Activity feed panel
	if m.activityFeed != nil && m.activityFeed.IsVisible() && m.activityFeed.HasActiveEntries() {
		panelBuilder.WriteString(m.activityFeed.View(m.width))
		panelBuilder.WriteString("\n")
	}

	// Agent tree panel
	if m.agentTreePanel != nil && m.agentTreePanel.IsVisible() && m.agentTreePanel.NodeCount() > 0 {
		panelBuilder.WriteString(m.agentTreePanel.View(m.width))
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

	// File peek (one-line status indicator, auto-dismissed after a short TTL)
	if m.filePeek != nil && m.filePeek.IsVisible() {
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
		elapsed := time.Since(m.streamStartTime)
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
				if len(trail) > 60 {
					trail = trail[:57] + "..."
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
				// Tool is running longer than expected — show brief hint
				slowStyle := lipgloss.NewStyle().Foreground(ColorDim)
				status += "  " + slowStyle.Render("· running")
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

	// Input area
	if m.state == StateInput {
		builder.WriteString(m.input.View())

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

		// Hints for hidden panels with content
		var hiddenPanelHints []string
		if len(m.todoItems) > 0 && !m.todosVisible {
			hiddenPanelHints = append(hiddenPanelHints, fmt.Sprintf("Ctrl+T — tasks (%d)", len(m.todoItems)))
		}
		if m.activityFeed != nil && !m.activityFeed.IsVisible() && m.activityFeed.HasActiveEntries() {
			hiddenPanelHints = append(hiddenPanelHints, "Ctrl+O — activity")
		}
		if len(hiddenPanelHints) > 0 {
			hintStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
			builder.WriteString("\n" + hintStyle.Render("  "+strings.Join(hiddenPanelHints, " • ")))
		}
	}

	// Contextual shortcut hints (compact, state-aware)
	if hints := m.contextualShortcutHints(); hints != "" {
		builder.WriteString("\n")
		builder.WriteString(hints)
	}

	// Enhanced status bar
	builder.WriteString("\n")
	builder.WriteString(m.renderStatusBar())

	// Overlay Context Observatory panel if visible
	if m.observatoryPanel != nil && m.observatoryPanel.IsVisible() {
		observatoryView := m.observatoryPanel.View(m.width)
		if observatoryView != "" {
			builder.WriteString("\n")
			builder.WriteString(observatoryView)
			builder.WriteString("\n")
		}
	}

	return m.styles.App.Render(builder.String())
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

// ShowHint displays a contextual hint if hints are enabled and the hint
// hasn't been shown too many times (max 3 times per hint).
func (m *Model) ShowHint(hintID, text string) {
	if !m.hintsEnabled {
		return
	}
	if m.hintsShown[hintID] >= 3 {
		return // Already shown 3 times, auto-dismiss
	}
	m.hintsShown[hintID]++
	m.output.AppendLine(m.styles.FormatHint(text))
	m.output.AppendLine("")
}

// ========== Coordinated Task Handlers (Phase 2) ==========

// handleTaskStarted handles the TaskStartedEvent message.
func (m *Model) handleTaskStarted(msg TaskStartedEvent) {
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

// handleTaskCompleted handles the TaskCompletedEvent message.
func (m *Model) handleTaskCompleted(msg TaskCompletedEvent) {
	if taskState, ok := m.coordinatedTasks[msg.TaskID]; ok {
		taskState.Duration = msg.Duration
		taskState.Error = msg.Error

		if msg.Success {
			taskState.Status = "completed"
			taskState.Progress = 1.0
		} else {
			taskState.Status = "failed"
		}

		m.output.AppendLine(m.renderTaskTimelineDone(msg.Success, msg.Duration.Round(time.Millisecond), msg.Error))
	}

	// Update active task
	if m.activeCoordinatedTask == msg.TaskID {
		m.activeCoordinatedTask = ""
	}
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
	removedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
	addedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981"))

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

func (m *Model) renderProgressBar(progress float64, width int) string {
	filled := int(progress * float64(width))
	if filled > width {
		filled = width
	}

	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return fmt.Sprintf("[%s] %.0f%%", bar, progress*100)
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
			// Only show toast for failures
			if m.toastManager != nil && msg.Status == "failed" {
				desc := task.Description
				if runes := []rune(desc); len(runes) > 30 {
					desc = string(runes[:27]) + "..."
				}
				m.toastManager.ShowError(fmt.Sprintf("Task failed: %s", desc))
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
	for k, v := range m.backgroundTasks {
		result[k] = v
	}
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
		BackgroundTasks:   m.backgroundTasks,
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
