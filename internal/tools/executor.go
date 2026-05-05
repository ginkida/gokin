package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"gokin/internal/audit"
	"gokin/internal/cache"
	"gokin/internal/client"
	"gokin/internal/format"
	"gokin/internal/hooks"
	"gokin/internal/logging"
	"gokin/internal/permission"
	"gokin/internal/robustness"
	"gokin/internal/security"

	"google.golang.org/genai"
)

// Limits for parallel tool execution to prevent resource exhaustion.
const (
	// MaxFunctionCallsPerResponse limits the number of function calls processed per response.
	MaxFunctionCallsPerResponse = 10
	// MaxConcurrentToolExecutions limits parallel goroutines for tool execution.
	MaxConcurrentToolExecutions = 5
	// defaultModelRoundTimeout caps a single model round to prevent zombie requests.
	// Keep this generous for reasoning-heavy models; SSE idle timeout still protects
	// against truly stalled streams.
	defaultModelRoundTimeout = 5 * time.Minute
)

// ResultCompactor interface for compacting tool results.
type ResultCompactor interface {
	CompactForType(toolName string, result ToolResult) ToolResult
}

// ResponseCompressor compresses structured fields of a genai.FunctionResponse
// that ResultCompactor can't reach. Applied after the per-tool Content-level
// compaction but before the response lands in history. Intentionally an
// interface (not a direct dep on internal/context) to avoid an import cycle.
type ResponseCompressor interface {
	CompressContent(part *genai.Part) *genai.Part
}

type resultSummaryProvider interface {
	SummarizeForPrune(toolName, content string) string
}

// FallbackConfig holds configurable fallback text for when AI doesn't respond after tool use.
type FallbackConfig struct {
	ToolResponseText  string // Text shown when tools were used but no response
	EmptyResponseText string // Text shown when no response at all
}

// DefaultFallbackConfig returns the default fallback configuration.
func DefaultFallbackConfig() FallbackConfig {
	return FallbackConfig{
		ToolResponseText:  "I've completed the requested operations. Based on the tools I used, here's what I found:\n\nPlease let me know if you'd like me to analyze this further or if you have other questions.",
		EmptyResponseText: "I'm here to help with software development tasks. What would you like me to do?",
	}
}

// SmartFallbackConfig contains tool-specific fallback messages.
var SmartFallbackConfig = map[string]struct {
	Success string // Message when tool succeeds but model doesn't respond
	Empty   string // Message when tool returns empty results
	Error   string // Message template for errors
}{
	"read": {
		Success: "I've read the file. Here's a summary of what I found:\n\n[The file was read successfully. Key observations should be provided above.]\n\nWould you like me to explain any specific part?",
		Empty:   "The file appears to be empty or could not be read. Would you like me to:\n1. Check if the file exists\n2. Try reading a different file\n3. Search for similar files",
		Error:   "I encountered an issue reading the file: %s\n\nSuggestions:\n- Check if the file path is correct\n- Verify file permissions\n- Try reading a different file",
	},
	"grep": {
		Success: "Here's what I found from searching:\n\n[Search results should be analyzed above.]\n\nWould you like me to read any of these files in detail?",
		Empty:   "No matches found for this pattern. This could mean:\n1. The pattern doesn't exist in this codebase\n2. Try a different search pattern\n3. The content might be in different files\n\nWould you like me to try a different search?",
		Error:   "Search encountered an issue: %s\n\nSuggestions:\n- Check regex syntax\n- Try a simpler pattern\n- Narrow down the search path",
	},
	"glob": {
		Success: "I found these files. Here's an overview:\n\n[File list should be categorized above.]\n\nWhich files would you like me to examine?",
		Empty:   "No files match this pattern. Possible reasons:\n1. Pattern syntax might be incorrect\n2. Files might have different extensions\n3. Directory might not exist\n\nTry patterns like:\n- `**/*.go` for Go files\n- `**/*.ts` for TypeScript\n- `**/config.*` for config files",
		Error:   "File search failed: %s\n\nCheck:\n- Pattern syntax (use ** for recursive)\n- Directory path exists\n- Permissions on directories",
	},
	"bash": {
		Success: "Command executed. Here's what happened:\n\n[Command output should be explained above.]\n\nIs there anything else you'd like me to run?",
		Empty:   "The command completed successfully but produced no output. This is often normal for:\n- File operations (cp, mv, mkdir)\n- Configuration changes\n- Silent success modes\n\nThe operation was likely successful.",
		Error:   "Command failed: %s\n\nSuggestions:\n- Check command syntax\n- Verify paths and permissions\n- Look at the error message for clues",
	},
	"write": {
		Success: "File written successfully. The changes include:\n\n[File contents should be summarized above.]\n\nWould you like me to verify the file or make additional changes?",
		Empty:   "File operation completed.",
		Error:   "Failed to write file: %s\n\nCheck:\n- Directory exists\n- Write permissions\n- Disk space",
	},
	"edit": {
		Success: "File edited. Here's what changed:\n\n[Changes should be detailed above.]\n\nWould you like me to verify the changes or make additional modifications?",
		Empty:   "No changes were needed - the content already matches.",
		Error:   "Edit failed: %s\n\nThis usually means:\n- The search text wasn't found\n- File content has changed\n- Try reading the file first to see current content",
	},
}

// formatToolPanic returns a user-facing message for a recovered tool panic.
// The "panic:" prefix is preserved so error-classifier matchers in
// context/compactor.go and context/tool_summarizer.go still recognise the
// result as an error during compaction. The hint about the log file gives
// the user a path to a full stack trace without bloating the model context.
func formatToolPanic(toolName string, r any) string {
	return fmt.Sprintf("panic: tool %q crashed: %v (likely a bug; full stack trace in gokin log)", toolName, r)
}

// GetSmartFallback returns an appropriate fallback message based on tool and result.
func GetSmartFallback(toolName string, result ToolResult, toolsUsed []string) string {
	// Get tool-specific config
	config, hasConfig := SmartFallbackConfig[toolName]

	// Build context about what tools were used
	var toolContext string
	if len(toolsUsed) > 0 {
		toolContext = fmt.Sprintf("\n\n**Tools used:** %s", strings.Join(toolsUsed, ", "))
	}

	// Handle case where result is empty/default (tools were called but result not tracked properly)
	// This is a graceful fallback - don't show error messages for missing data
	isEmptyResult := result.Content == "" && result.Error == "" && !result.Success

	// Handle error cases - but only if there's an actual error message
	if !result.Success && result.Error != "" {
		if hasConfig {
			return "[Auto] " + fmt.Sprintf(config.Error, result.Error) + toolContext
		}
		return "[Auto] " + fmt.Sprintf("Operation encountered an issue: %s\n\nWould you like me to try a different approach?", result.Error) + toolContext
	}

	// Handle empty/default result - provide helpful generic message
	if isEmptyResult {
		if hasConfig {
			return "[Auto] " + config.Success + toolContext
		}
		return "[Auto] I've completed the requested operations. The results should be shown above.\n\nIs there anything specific you'd like me to explain or analyze further?" + toolContext
	}

	// Handle empty content results (tool ran but returned nothing)
	if result.Content == "" || result.Content == "(empty file)" || result.Content == "(no output)" || result.Content == "No matches found." {
		if hasConfig {
			return "[Auto] " + config.Empty + toolContext
		}
		return "[Auto] The operation completed but returned no results. This may be expected depending on the context." + toolContext
	}

	// Handle success with content
	if hasConfig {
		return "[Auto] " + config.Success + toolContext
	}

	return "[Auto] I've completed the operation. The results are shown above. Would you like me to analyze them further?" + toolContext
}

// ToolRegistry is an interface for tool registries.
// Both Registry and LazyRegistry implement this interface.
type ToolRegistry interface {
	Get(name string) (Tool, bool)
	List() []Tool
	Names() []string
	Declarations() []*genai.FunctionDeclaration
	GeminiTools() []*genai.Tool
	Register(tool Tool) error
}

// Executor handles the function calling loop with enhanced safety and user awareness.
type Executor struct {
	registry          ToolRegistry
	client            client.Client
	workDir           string
	timeout           time.Duration
	modelRoundTimeout time.Duration
	handler           *ExecutionHandler
	compactor         ResultCompactor
	permissions       *permission.Manager
	hooks             *hooks.Manager
	auditLogger       *audit.Logger
	sessionID         string
	fallback          FallbackConfig
	clientMu          sync.RWMutex // Protects client field

	// Enhanced safety features
	safetyValidator  SafetyValidator
	preFlightChecks  bool                     // Enable pre-flight safety checks
	userNotified     bool                     // Track if user was notified about tool execution
	notificationMgr  *NotificationManager     // User notifications
	redactor         *security.SecretRedactor // Redact secrets from output
	unrestrictedMode bool                     // Full freedom when both sandbox and permissions are off

	// Token usage from last response (from API usage metadata).
	// Protected by tokenMu for concurrent read/write safety.
	tokenMu                 sync.Mutex
	lastInputTokens         int
	lastOutputTokens        int
	lastCacheCreationTokens int
	lastCacheReadTokens     int

	// Prompt cache break detection
	cacheTracker *client.CacheTracker

	// Circuit breakers for tools
	breakerMu    sync.Mutex
	toolBreakers map[string]*robustness.CircuitBreaker

	// Tool result cache
	toolCache *ToolResultCache

	// Search cache (grep/glob results) for invalidation on writes
	searchCache *cache.SearchCache

	// File read deduplication tracker
	readTracker *FileReadTracker

	// File write tracker — records paths modified by write/edit/refactor so
	// injectContinuationHint can remind the model after compaction.
	writeTracker *FileWriteTracker

	// phaseObserver (optional) receives per-tool timing and outcome. Wired from
	// internal/app so the tools package stays free of metrics dependencies.
	phaseObserver func(tool string, d time.Duration, success bool)

	// planModeCheck (optional) reports whether the agent is in Claude Code-style
	// plan mode. When it returns true, doExecuteTool blocks every call whose
	// tool name is not in the plan-mode allow-list — defense in depth against
	// a model that hallucinates a tool call for a tool the filtered schema
	// didn't expose (e.g., from pre-trained priors). Nil means "always full
	// access". Wired from internal/app.
	planModeCheck func() bool

	// toolStatsLookup (optional) returns observed p95 and success rate for a
	// tool, or ok=false if there aren't enough samples yet (threshold enforced
	// on the caller side — typically 5 samples). Used by the executor to
	// downgrade parallel groups to sequential when a batched tool fails often,
	// and to cap concurrency for slow tools.
	toolStatsLookup func(tool string) (p95 time.Duration, successRate float64, ok bool)

	// Auto-formatter for write/edit operations
	formatter *Formatter

	// Delta-check guardrail for mutating operations.
	deltaCheckEnabled     bool
	deltaCheckWarnOnly    bool
	deltaCheckTimeout     time.Duration
	deltaCheckMaxModules  int
	deltaCheckMu          sync.Mutex
	deltaCheckBlocked     bool
	deltaCheckBlockReason string
	deltaCheckLastHash    string
	deltaCheckLastResult  *deltaCheckResult
	deltaBaselineCaptured bool
	deltaBaselinePaths    map[string]struct{}
	deltaPendingPaths     map[string]struct{}

	// Retry-time side effect deduplication (write/bash tools).
	sideEffectDedupEnabled bool
	sideEffectLedgerMu     sync.Mutex
	sideEffectsByCallID    map[string]ToolResult
	sideEffectsBySignature map[string]ToolResult

	// Pending background task completion notifications
	pendingNotifications []string
	pendingNotifMu       sync.Mutex

	// Checkpoint journal for tool execution recovery on API failure.
	checkpoint *CheckpointJournal

	// Smart validation: semantic validators for post-write checks.
	semanticValidators *SemanticValidatorRegistry

	// Smart validation: context enrichment for post-write hints.
	contextEnricher *ContextEnricher

	// Smart validation: self-review injection for multi-file mutations.
	selfReviewThreshold  int
	mutatedFilesThisTurn map[string]bool
	mutatedFilesMu       sync.Mutex

	// Smart validation: persistent learning from validator warnings.
	errorLearner ErrorLearner

	// Kimi-specific per-turn tool budget. 0 disables the check; stagnation
	// detection still runs. Set via SetKimiToolBudget from app/builder.
	// Only enforced when the active model is in the "kimi" family — GLM/
	// MiniMax/Ollama don't see this limit. Rationale: Kimi's tool-call
	// enthusiasm routinely balloons to 50+ calls on a single task, and
	// stagnation detection only catches identical repeats, not diverse
	// over-exploration. A hard cap forces finalization before the context
	// budget is eaten alive.
	kimiToolBudget int

	// Optional deep-compressor for structured function-response fields.
	// ResultCompactor trims Content strings per-tool; this compresses
	// anything else (Data maps, nested arrays) before the response joins
	// history. nil = no-op.
	responseCompressor ResponseCompressor
}

// ExecutionInfo holds information about an active tool execution
type ExecutionInfo struct {
	StartTime      time.Time
	ToolName       string
	Args           map[string]any
	SafetyLevel    SafetyLevel
	Summary        *ExecutionSummary
	PreFlightCheck *PreFlightCheck
	ParentTool     string // For nested tool calls
}

// ExecutionHandler provides callbacks for execution events.
type ExecutionHandler struct {
	// OnText is called when text is streamed from the model.
	OnText func(text string)

	// OnThinking is called when thinking/reasoning content is streamed.
	OnThinking func(text string)

	// OnToolStart is called when a tool begins execution.
	OnToolStart func(name string, args map[string]any)

	// OnToolEnd is called when a tool finishes execution.
	OnToolEnd func(name string, args map[string]any, result ToolResult)

	// OnToolProgress is called periodically during long-running tool execution.
	// This helps keep the UI alive and prevents timeout during long operations.
	OnToolProgress func(name string, elapsed time.Duration)

	// OnToolDetailedProgress is called when a tool reports granular progress.
	// Tools use ProgressCallback to report progress, currentStep, and bytes processed.
	OnToolDetailedProgress func(name string, progress float64, currentStep string)

	// OnToolValidating is called before tool execution to show safety checks.
	OnToolValidating func(name string, check *PreFlightCheck)

	// OnToolApproved is called when user approves a tool execution.
	OnToolApproved func(name string, summary *ExecutionSummary)

	// OnToolDenied is called when user denies a tool execution.
	OnToolDenied func(name string, reason string)

	// OnError is called when an error occurs.
	OnError func(err error)

	// OnWarning is called when a warning is issued.
	OnWarning func(warning string)

	// OnInlineDiff is called after successful edit to show inline diff.
	OnInlineDiff func(filePath, oldText, newText string)

	// OnLoopIteration is called at the start of each executor loop iteration (from 2nd onwards).
	OnLoopIteration func(iteration int, totalToolsUsed int)

	// OnTokenUpdate is called when the streaming response provides token usage from the API.
	OnTokenUpdate func(inputTokens, outputTokens int)

	// OnFilePeek is called to show a transient high-resolution snippet of a file.
	OnFilePeek func(filePath, title, content, action string)

	// OnMemoryNotify is called when a memory operation completes (save, recall, forget).
	OnMemoryNotify func(action, summary string)
}

// NewExecutor creates a new tool executor.
func NewExecutor(registry *Registry, c client.Client, timeout time.Duration) *Executor {
	return newExecutorInternal(registry, c, timeout)
}

// NewExecutorLazy creates a new tool executor with a LazyRegistry.
func NewExecutorLazy(registry *LazyRegistry, c client.Client, timeout time.Duration) *Executor {
	return newExecutorInternal(registry, c, timeout)
}

// newExecutorInternal creates the executor with any ToolRegistry implementation.
func newExecutorInternal(registry ToolRegistry, c client.Client, timeout time.Duration) *Executor {
	return &Executor{
		registry:               registry,
		client:                 c,
		timeout:                timeout,
		modelRoundTimeout:      defaultModelRoundTimeout,
		handler:                &ExecutionHandler{},
		fallback:               DefaultFallbackConfig(),
		safetyValidator:        NewDefaultSafetyValidator(),
		preFlightChecks:        true,
		notificationMgr:        NewNotificationManager(),
		toolBreakers:           make(map[string]*robustness.CircuitBreaker),
		redactor:               security.NewSecretRedactor(),
		deltaCheckEnabled:      true,
		deltaCheckWarnOnly:     false,
		deltaCheckTimeout:      90 * time.Second,
		deltaCheckMaxModules:   8,
		deltaBaselinePaths:     make(map[string]struct{}),
		deltaPendingPaths:      make(map[string]struct{}),
		sideEffectsByCallID:    make(map[string]ToolResult),
		sideEffectsBySignature: make(map[string]ToolResult),
		checkpoint:             NewCheckpointJournal(),
		cacheTracker:           client.NewCacheTracker(),
	}
}

// AddPendingNotification queues a background task completion notification.
func (e *Executor) AddPendingNotification(msg string) {
	e.pendingNotifMu.Lock()
	defer e.pendingNotifMu.Unlock()
	e.pendingNotifications = append(e.pendingNotifications, msg)
}

// drainPendingNotifications returns and clears all queued notifications.
func (e *Executor) drainPendingNotifications() []string {
	e.pendingNotifMu.Lock()
	defer e.pendingNotifMu.Unlock()
	if len(e.pendingNotifications) == 0 {
		return nil
	}
	n := e.pendingNotifications
	e.pendingNotifications = nil
	return n
}

// SetClient updates the underlying client.
// SetPlanModeCheck wires the plan-mode predicate. The callback is consulted
// on every tool dispatch and must be cheap — typically reads a boolean
// guarded by App.mu. Pass nil to disable plan-mode gating (main agent in
// normal mode, all sub-agents, tests).
func (e *Executor) SetPlanModeCheck(check func() bool) {
	e.clientMu.Lock()
	defer e.clientMu.Unlock()
	e.planModeCheck = check
}

func (e *Executor) SetClient(c client.Client) {
	e.clientMu.Lock()
	e.client = c
	e.clientMu.Unlock()
}

// SetWorkDir sets the working directory used by guardrails that need repository context.
func (e *Executor) SetWorkDir(workDir string) {
	e.workDir = strings.TrimSpace(workDir)
}

// SetDeltaCheckConfig configures automatic post-edit delta-check behavior.
func (e *Executor) SetDeltaCheckConfig(enabled bool, timeout time.Duration, warnOnly bool, maxModules int) {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	if maxModules <= 0 {
		maxModules = 8
	}

	e.deltaCheckMu.Lock()
	defer e.deltaCheckMu.Unlock()
	e.deltaCheckEnabled = enabled
	e.deltaCheckTimeout = timeout
	e.deltaCheckWarnOnly = warnOnly
	e.deltaCheckMaxModules = maxModules
}

// SetSemanticValidators configures post-write semantic validators.
func (e *Executor) SetSemanticValidators(r *SemanticValidatorRegistry) {
	e.semanticValidators = r
}

// SetContextEnricher configures post-write context enrichment.
func (e *Executor) SetContextEnricher(ce *ContextEnricher) {
	e.contextEnricher = ce
}

// GetContextEnricher returns the context enricher for late-binding (e.g. predictor wiring).
func (e *Executor) GetContextEnricher() *ContextEnricher {
	return e.contextEnricher
}

// ErrorLearner persists error/warning patterns for future prevention.
type ErrorLearner interface {
	LearnError(errorType, pattern, solution string, tags []string) error
}

// SetErrorLearner configures persistent learning from validator warnings.
func (e *Executor) SetErrorLearner(el ErrorLearner) {
	e.errorLearner = el
}

// SetSelfReviewThreshold sets the number of mutated files per turn
// that triggers a self-review injection. 0 disables.
func (e *Executor) SetSelfReviewThreshold(n int) {
	e.selfReviewThreshold = n
	if n > 0 {
		e.mutatedFilesThisTurn = make(map[string]bool)
	}
}

// SetKimiToolBudget sets the per-turn tool call cap applied to Kimi-family
// models. n <= 0 disables the cap entirely. Values below 10 are clamped
// up because a Kimi turn that can only make 9 tool calls will under-deliver
// on legitimate multi-step tasks.
func (e *Executor) SetKimiToolBudget(n int) {
	if n <= 0 {
		e.kimiToolBudget = 0
		return
	}
	if n < 10 {
		n = 10
	}
	e.kimiToolBudget = n
}

// getClient returns the current client under read lock.
func (e *Executor) getClient() client.Client {
	e.clientMu.RLock()
	c := e.client
	e.clientMu.RUnlock()
	return c
}

// SetModelRoundTimeout sets timeout for a single model round.
// Non-positive values reset to the default timeout.
func (e *Executor) SetModelRoundTimeout(timeout time.Duration) {
	if timeout <= 0 {
		e.modelRoundTimeout = defaultModelRoundTimeout
		return
	}
	e.modelRoundTimeout = timeout
}

// SetSideEffectDedup enables/disables deduplication for side-effecting tools.
// Intended for retry windows to avoid duplicate write/bash effects.
func (e *Executor) SetSideEffectDedup(enabled bool) {
	e.sideEffectLedgerMu.Lock()
	defer e.sideEffectLedgerMu.Unlock()
	e.sideEffectDedupEnabled = enabled
}

// GetCheckpointJournal returns the checkpoint journal for inspection or persistence.
func (e *Executor) GetCheckpointJournal() *CheckpointJournal {
	return e.checkpoint
}

// ResetSideEffectLedger clears deduplication ledger for a new top-level request.
func (e *Executor) ResetSideEffectLedger() {
	e.sideEffectLedgerMu.Lock()
	defer e.sideEffectLedgerMu.Unlock()
	e.sideEffectsByCallID = make(map[string]ToolResult)
	e.sideEffectsBySignature = make(map[string]ToolResult)

	// Also clear the checkpoint journal — it belongs to the previous request.
	if e.checkpoint != nil {
		e.checkpoint.Clear()
	}
}

// SetSafetyValidator sets the safety validator.
func (e *Executor) SetSafetyValidator(validator SafetyValidator) {
	e.safetyValidator = validator
}

// EnablePreFlightChecks enables or disables pre-flight safety checks.
func (e *Executor) EnablePreFlightChecks(enabled bool) {
	e.preFlightChecks = enabled
}

// SetUnrestrictedMode enables or disables unrestricted mode.
// When enabled (both sandbox and permissions are off), preflight check errors
// are converted to warnings and don't block execution.
func (e *Executor) SetUnrestrictedMode(enabled bool) {
	e.unrestrictedMode = enabled
}

// IsUnrestrictedMode returns whether unrestricted mode is enabled.
func (e *Executor) IsUnrestrictedMode() bool {
	return e.unrestrictedMode
}

// SetFallbackConfig sets the fallback configuration for tool responses.
func (e *Executor) SetFallbackConfig(config FallbackConfig) {
	e.fallback = config
}

// SetHandler sets the execution event handler.
func (e *Executor) SetHandler(handler *ExecutionHandler) {
	if handler != nil {
		e.handler = handler
	}
}

// SetCompactor sets the result compactor for tool output optimization.
func (e *Executor) SetCompactor(compactor ResultCompactor) {
	e.compactor = compactor
}

// SetResponseCompressor installs the optional deep compressor applied to
// each function-response before it joins history. Nil = disabled.
func (e *Executor) SetResponseCompressor(c ResponseCompressor) {
	e.responseCompressor = c
}

// SetPermissions sets the permission manager for tool execution.
func (e *Executor) SetPermissions(mgr *permission.Manager) {
	e.permissions = mgr
}

// SetHooks sets the hooks manager for tool execution.
func (e *Executor) SetHooks(mgr *hooks.Manager) {
	e.hooks = mgr
}

// SetFormatter sets the auto-formatter for post-write/edit formatting.
func (e *Executor) SetFormatter(f *Formatter) {
	e.formatter = f
}

// SetAuditLogger sets the audit logger for tool execution.
func (e *Executor) SetAuditLogger(logger *audit.Logger) {
	e.auditLogger = logger
}

// SetSessionID sets the session ID for audit logging.
func (e *Executor) SetSessionID(id string) {
	e.sessionID = id
}

// SetToolCache sets the tool result cache for caching read-only tool results.
func (e *Executor) SetToolCache(c *ToolResultCache) {
	e.toolCache = c
}

// SetSearchCache sets the search cache for invalidation on write operations.
func (e *Executor) SetSearchCache(c *cache.SearchCache) {
	e.searchCache = c
}

// SetReadTracker sets the file read deduplication tracker.
func (e *Executor) SetReadTracker(tracker *FileReadTracker) {
	e.readTracker = tracker
}

// SetWriteTracker sets the file write tracker. When set, successful write/edit/
// refactor/copy/move calls record the modified path so the agent can surface
// them in post-compaction continuation hints.
func (e *Executor) SetWriteTracker(tracker *FileWriteTracker) {
	e.writeTracker = tracker
}

// GetReadTracker returns the executor's read tracker, or nil if none is
// configured. Consumers (Agent, commands) read this to surface which files
// the session has already seen — useful for post-compaction hints and
// cache-hit heuristics.
func (e *Executor) GetReadTracker() *FileReadTracker {
	return e.readTracker
}

// ResetSession resets all per-conversation accumulator state so that /clear
// starts fresh. Circuit breakers and tool caches are intentionally preserved
// (connection failures and cache hits persist across /clear).
func (e *Executor) ResetSession() {
	if e.readTracker != nil {
		e.readTracker.Reset()
	}
	if e.writeTracker != nil {
		e.writeTracker.Reset()
	}
	e.deltaBaselineCaptured = false
	e.deltaCheckLastResult = nil
	e.deltaCheckBlocked = false
}

// SetPhaseObserver wires a callback that's invoked after each tool execution
// with the tool name and its wall-clock duration. The app-layer metrics
// collector uses this to build p50/p95 charts per tool without making
// internal/tools depend on internal/app. Nil-safe: no calls when unset.
func (e *Executor) SetPhaseObserver(fn func(tool string, d time.Duration, success bool)) {
	e.phaseObserver = fn
}

// SetToolStatsLookup wires a callback that returns observed p95 / success
// rate per tool. The executor uses this to make adaptive concurrency
// decisions (serialize unreliable tools, cap concurrency for slow ones).
func (e *Executor) SetToolStatsLookup(fn func(tool string) (p95 time.Duration, successRate float64, ok bool)) {
	e.toolStatsLookup = fn
}

// GetNotificationManager returns the notification manager.
func (e *Executor) GetNotificationManager() *NotificationManager {
	return e.notificationMgr
}

// GetLastTokenUsage returns the token usage from the last API response.
// Returns (inputTokens, outputTokens). Values are 0 if metadata was unavailable.
func (e *Executor) GetLastTokenUsage() (int, int) {
	e.tokenMu.Lock()
	in, out := e.lastInputTokens, e.lastOutputTokens
	e.tokenMu.Unlock()
	return in, out
}

// GetLastCacheMetrics returns the prompt cache metrics from the last API response.
// Returns (cacheCreationInputTokens, cacheReadInputTokens).
func (e *Executor) GetLastCacheMetrics() (int, int) {
	e.tokenMu.Lock()
	creation, read := e.lastCacheCreationTokens, e.lastCacheReadTokens
	e.tokenMu.Unlock()
	return creation, read
}

// GetCacheTracker returns the prompt cache break tracker.
func (e *Executor) GetCacheTracker() *client.CacheTracker {
	return e.cacheTracker
}

// Execute processes a user message through the function calling loop.
func (e *Executor) Execute(ctx context.Context, history []*genai.Content, message string) ([]*genai.Content, string, error) {
	// Add user message to history
	userContent := genai.NewContentFromText(message, genai.RoleUser)
	history = append(history, userContent)

	return e.executeLoop(ctx, history)
}

// executeLoop runs the function calling loop until a final text response is received.
func (e *Executor) executeLoop(ctx context.Context, history []*genai.Content) ([]*genai.Content, string, error) {
	// Snapshot client once per executeLoop to avoid racing with SetClient.
	cl := e.getClient()

	// Reset token counters for this execution cycle.
	// OutputTokens accumulates across rounds (each round generates new output).
	// InputTokens is overwritten (last round has full context).
	e.tokenMu.Lock()
	e.lastInputTokens = 0
	e.lastOutputTokens = 0
	e.lastCacheCreationTokens = 0
	e.lastCacheReadTokens = 0
	e.tokenMu.Unlock()

	// NOTE: checkpoint journal is NOT cleared here — it persists across retries
	// within the same handleSubmit() so that write operations completed before
	// an API failure can be replayed from cache instead of re-executed.
	// The journal is cleared in ResetSideEffectLedger() at the start of each
	// new top-level user request.

	// === IMPROVEMENT 3: Dynamic max iterations based on context complexity ===
	maxIterations := e.calculateMaxIterations(history)

	var finalText string
	var toolsUsed []string          // Track which tools were used for smart fallback
	var lastToolResult ToolResult   // Track the last tool result for context
	var recentToolPatterns []string // Track tool name patterns per iteration for stagnation detection
	var lastWorkingMemoryNote string
	var lastKimiRecoveryNote string
	// synthesisNudgeInjected ensures the per-turn consolidation reminder
	// fires at most once. Without this guard the prompt would repeat every
	// iteration past the threshold, training Kimi to output boilerplate
	// synthesis blocks instead of actually thinking.
	synthesisNudgeInjected := false
	// intentNudgeInjected gates the "announce your plan before tools"
	// reminder to the first iteration only. Firing it twice in a turn
	// just adds noise — the model saw it already.
	intentNudgeInjected := false
	amnesiaWarned := map[string]bool{}
	stagnationRecoveries := map[string]int{}
	const (
		stagnationLimit           = 5  // Consecutive identical tool patterns before aborting
		stagnationWindowSize      = 15 // Rolling window for amnesia (non-consecutive) repeats
		stagnationWindowRepeatMin = 5  // Repeats within window to trigger a warning (not an abort)
	)
	streamRetries := 0
	partialStreamRetries := 0
	retryPolicy := client.DefaultStreamRetryPolicy()
	// Retries are orchestrated at App/message processor level to avoid retry multiplication.
	retryPolicy.MaxRetries = 0
	retryPolicy.MaxPartialRetries = 0

	for i := range maxIterations {
		// Check context cancellation between iterations
		select {
		case <-ctx.Done():
			return history, finalText, client.ContextErr(ctx)
		default:
		}

		// Notify about loop iteration progress (from 2nd iteration onwards)
		if i > 0 && e.handler != nil && e.handler.OnLoopIteration != nil {
			e.handler.OnLoopIteration(i+1, len(toolsUsed))
		}

		// Get response from model
		resp, err := e.getModelResponse(ctx, cl, history)
		if err != nil {
			ft := client.DetectFailureTelemetry(err)
			logging.Warn("model round failed",
				"reason", ft.Reason,
				"partial", ft.Partial,
				"timeout", ft.Timeout,
				"provider", ft.Provider,
				"retry_count", streamRetries,
				"partial_retry_count", partialStreamRetries,
				"error", err)

			decision := client.DecideStreamRetry(
				retryPolicy,
				err,
				streamRetries,
				partialStreamRetries,
				ctx,
				client.StreamRetryOptions{AllowPartial: false}, // partial continuation is handled at App level
			)
			if decision.ShouldRetry {
				if decision.Partial {
					partialStreamRetries++
				} else {
					streamRetries++
				}
				if e.handler != nil && e.handler.OnWarning != nil {
					e.handler.OnWarning(fmt.Sprintf(
						"Request failed (%s), retrying (%d/%d) in %s...",
						decision.Reason,
						streamRetries,
						retryPolicy.MaxRetries,
						decision.Delay.Round(time.Second),
					))
				}
				if err := waitForRetry(ctx, decision.Delay); err != nil {
					return history, finalText, err
				}
				continue // retry outer loop
			}
			// Preserve partial response in history — include all structured content
			// (text, thinking, function calls) not just text.
			if resp != nil {
				if parts := e.buildResponsePartsRaw(resp); len(parts) > 0 {
					history = append(history, &genai.Content{
						Role:  genai.RoleModel,
						Parts: parts,
					})
				}
			}
			return history, "", fmt.Errorf("model response error (%s): %w", ft.Reason, err)
		}
		streamRetries = 0        // reset on success
		partialStreamRetries = 0 // reset on success

		// Text-based tool call fallback for models without native function calling
		if len(resp.FunctionCalls) == 0 && resp.Text != "" {
			if fallbackClient, ok := cl.(interface{ NeedsToolCallFallback() bool }); ok && fallbackClient.NeedsToolCallFallback() {
				if parsed := client.ParseToolCallsFromText(resp.Text); len(parsed) > 0 {
					resp.FunctionCalls = parsed
					// Strip the JSON from text since we're treating it as a tool call
					resp.Text = ""
					logging.Info("fallback: parsed tool calls from text", "count", len(parsed))
				}
			}
		}

		// Add model response to history
		modelContent := &genai.Content{
			Role:  genai.RoleModel,
			Parts: e.buildResponseParts(resp),
		}

		history = append(history, modelContent)

		// Intent announcement nudge: if the very first assistant-turn of
		// this request jumped straight into tool calls with no preceding
		// prose, queue a reminder for the NEXT round asking for a
		// one-line plan. Kimi-family only — Strong-tier models typically
		// self-narrate and don't need this. Once per turn, gated by
		// len(toolsUsed)==0 rather than i==0 so outer-loop retries on
		// transient errors (which bump i without executing tools) don't
		// miss the first real response.
		if !intentNudgeInjected && shouldInjectIntentNudge(len(toolsUsed), cl.GetModel(), resp) {
			intentNudgeInjected = true
			e.AddPendingNotification(intentNudgeMessage)
		}

		// Handle chained function calls in an inner loop
		for len(resp.FunctionCalls) > 0 {
			// Check context cancellation between chained tool calls (Esc pressed)
			select {
			case <-ctx.Done():
				return history, finalText, client.ContextErr(ctx)
			default:
			}

			// Track tools being called, including distinguishing arguments.
			// This prevents false stagnation when the same tool is called
			// multiple times with different targets (e.g., writing 5 different
			// files, running 5 different bash commands, searching 5 patterns).
			iterationTools := make([]string, 0, len(resp.FunctionCalls))
			for _, fc := range resp.FunctionCalls {
				toolsUsed = append(toolsUsed, fc.Name)
				toolKey := fc.Name + ":" + stagnationFingerprint(fc.Name, fc.Args)
				iterationTools = append(iterationTools, toolKey)
			}
			sort.Strings(iterationTools)
			pattern := strings.Join(iterationTools, ",")
			recentToolPatterns = append(recentToolPatterns, pattern)

			var results []*genai.FunctionResponse

			// Kimi-specific tool budget: cap total tool calls per turn to
			// prevent runaway exploration. Unlike stagnation (which catches
			// identical repeats), this catches diverse-but-wasteful fan-out.
			// We count already-consumed calls in toolsUsed, not toolsUsed +
			// current batch, so Kimi gets to finish a small final batch if
			// it's right at the edge — the clamp is on the NEXT batch.
			if e.shouldEnforceKimiToolBudget(cl.GetModel(), len(toolsUsed)-len(resp.FunctionCalls)) {
				logging.Warn("kimi tool budget exceeded: forcing finalization",
					"budget", e.kimiToolBudget,
					"consumed", len(toolsUsed)-len(resp.FunctionCalls),
					"model", cl.GetModel())
				if e.handler != nil && e.handler.OnWarning != nil {
					e.handler.OnWarning(fmt.Sprintf(
						"Kimi tool budget (%d) reached — forcing final answer instead of running %d more tools",
						e.kimiToolBudget, len(resp.FunctionCalls)))
				}
				results = e.buildKimiToolBudgetExhaustedResults(resp.FunctionCalls)
			}

			// Stagnation detection: if the same tool pattern repeats too many times, abort
			if results == nil && len(recentToolPatterns) >= stagnationLimit {
				tail := recentToolPatterns[len(recentToolPatterns)-stagnationLimit:]
				allSame := true
				for _, p := range tail[1:] {
					if p != tail[0] {
						allSame = false
						break
					}
				}
				if allSame {
					recoveryAttempt := stagnationRecoveries[pattern]
					if shouldAttemptStagnationRecovery(cl.GetModel(), resp.FunctionCalls, recoveryAttempt) {
						recoveryAttempt++
						stagnationRecoveries[pattern] = recoveryAttempt
						logging.Warn("executor stagnation detected: sending recovery hint instead of aborting",
							"pattern", pattern,
							"count", stagnationLimit,
							"model", cl.GetModel(),
							"recovery_attempt", recoveryAttempt)
						if e.handler != nil && e.handler.OnWarning != nil {
							e.handler.OnWarning(buildStagnationWarningMessage(resp.FunctionCalls, stagnationLimit))
						}
						results = e.buildStagnationRecoveryResults(resp.FunctionCalls, stagnationLimit)
					} else {
						logging.Warn("executor stagnation detected: same tool pattern repeated",
							"pattern", pattern,
							"count", stagnationLimit,
							"model", cl.GetModel(),
							"recovery_attempts", recoveryAttempt)
						errMsg := fmt.Sprintf("executor stagnation: tool pattern %q repeated %d times consecutively", pattern, stagnationLimit)
						if recoveryAttempt > 0 {
							errMsg += " after recovery hint"
						}
						return history, "", errors.New(errMsg)
					}
				}
			}

			// Amnesia detection: model may interleave other tools but keep coming back
			// to the same pattern (classic "forgot I tried this" loop). Warn — don't
			// abort — so the user can step in before consecutive stagnation kicks in.
			if len(recentToolPatterns) >= stagnationWindowSize && !amnesiaWarned[pattern] {
				windowStart := len(recentToolPatterns) - stagnationWindowSize
				count := 0
				for _, p := range recentToolPatterns[windowStart:] {
					if p == pattern {
						count++
					}
				}
				if count >= stagnationWindowRepeatMin {
					amnesiaWarned[pattern] = true
					logging.Warn("executor amnesia detected: pattern repeated within window",
						"pattern", pattern, "count", count, "window", stagnationWindowSize)
					if e.handler != nil && e.handler.OnWarning != nil {
						e.handler.OnWarning(fmt.Sprintf(
							"Model may be looping: tool pattern repeated %d times in last %d iterations",
							count, stagnationWindowSize))
					}
				}
			}

			if results == nil {
				results, err = e.executeTools(ctx, resp.FunctionCalls)
				if err != nil {
					return history, "", fmt.Errorf("tool execution error: %w", err)
				}
			}

			// Track last result for smart fallback
			if len(results) > 0 {
				lastResult := results[len(results)-1]
				respMap := lastResult.Response // Already map[string]any
				content, _ := respMap["content"].(string)
				success, _ := respMap["success"].(bool)
				errMsg, _ := respMap["error"].(string)
				lastToolResult = ToolResult{Content: content, Success: success, Error: errMsg}
			}

			// Self-review injection: if many files were mutated this turn,
			// add a notification nudging the model to verify consistency.
			if e.selfReviewThreshold > 0 {
				e.mutatedFilesMu.Lock()
				count := len(e.mutatedFilesThisTurn)
				var files []string
				if count >= e.selfReviewThreshold {
					for f := range e.mutatedFilesThisTurn {
						files = append(files, f)
					}
					sort.Strings(files)
				}
				e.mutatedFilesThisTurn = make(map[string]bool)
				e.mutatedFilesMu.Unlock()

				if count >= e.selfReviewThreshold {
					hint := fmt.Sprintf("[smart-validation] You modified %d files this turn: %s. Briefly verify version consistency and correctness across these files.", count, strings.Join(files, ", "))
					e.AddPendingNotification(hint)
				}
			}

			if note := e.buildModelWorkingMemoryNotification(cl.GetModel(), resp.FunctionCalls, results); note != "" && note != lastWorkingMemoryNote {
				lastWorkingMemoryNote = note
				e.AddPendingNotification(note)
			}

			if note := buildKimiToolErrorRecoveryNotification(cl.GetModel(), results); note != "" && note != lastKimiRecoveryNote {
				lastKimiRecoveryNote = note
				e.AddPendingNotification(note)
			}

			// Synthesis nudge: when Kimi has issued >=3 tool calls in this
			// turn without any intervening final text, inject a once-per-turn
			// consolidation reminder. It is appended to the current tool result
			// instead of inserted as a separate user message; Anthropic-compatible
			// APIs require tool_result blocks to immediately follow tool_use blocks.
			// Not emitted for non-Kimi models — Strong-tier models self-
			// consolidate, and spamming GLM/Opus with pause reminders
			// regresses their behaviour.
			if !synthesisNudgeInjected && shouldInjectSynthesisNudge(cl.GetModel(), len(toolsUsed)) {
				synthesisNudgeInjected = true
				e.AddPendingNotification(buildSynthesisNudgeMessage(len(toolsUsed)))
			}

			if notifications := e.drainPendingNotifications(); len(notifications) > 0 {
				notifText := "[System: " + strings.Join(notifications, "; ") + "]"
				appendNotificationToFunctionResults(results, notifText)
			}

			// Add function results to history BEFORE sending to model.
			// This ensures tool results are preserved even if the API call fails.
			funcResultParts := make([]*genai.Part, len(results))
			for j, result := range results {
				part := genai.NewPartFromFunctionResponse(result.Name, result.Response)
				part.FunctionResponse.ID = result.ID
				// Deep compression for structured response fields that
				// ResultCompactor's string-based Content pass can't reach
				// (nested Data maps, arrays). Safe when not wired: nil
				// compressor leaves the part untouched.
				if e.responseCompressor != nil {
					compressed := e.responseCompressor.CompressContent(part)
					// Defensive: an implementation that returns nil or
					// drops the FunctionResponse would silently break the
					// call/response pairing expected by the client. Fall
					// back to the uncompressed part in those cases.
					if compressed != nil && compressed.FunctionResponse != nil {
						// Re-apply ID — CompressFunctionResponse clones
						// the inner struct but preserves ID from input,
						// so this is usually a no-op. Kept explicit so
						// a future compressor that resets ID can't break
						// the tool-call/result pairing.
						compressed.FunctionResponse.ID = result.ID
						part = compressed
					}
				}
				funcResultParts[j] = part
			}
			funcResultContent := &genai.Content{
				Role:  genai.RoleUser,
				Parts: funcResultParts,
			}

			historyBeforeResults := len(history)
			history = append(history, funcResultContent)

			// Send function responses back to model.
			// Pass history without the just-appended tool results since
			// all client implementations append results themselves.
			for {
				roundCtx, roundCancel := e.withModelRoundTimeout(ctx)
				stream, err := cl.SendFunctionResponse(roundCtx, history[:historyBeforeResults], results)
				if err != nil {
					roundCancel()
					return history, "", fmt.Errorf("function response error: %w", err)
				}

				// Collect the response
				resp, err = e.collectStreamWithHandler(roundCtx, stream)
				roundCancel()
				if err == nil {
					streamRetries = 0
					partialStreamRetries = 0
					break
				}

				ft := client.DetectFailureTelemetry(err)
				logging.Warn("function response round failed",
					"reason", ft.Reason,
					"partial", ft.Partial,
					"timeout", ft.Timeout,
					"provider", ft.Provider,
					"retry_count", streamRetries,
					"partial_retry_count", partialStreamRetries,
					"error", err)

				decision := client.DecideStreamRetry(
					retryPolicy,
					err,
					streamRetries,
					partialStreamRetries,
					ctx,
					client.StreamRetryOptions{AllowPartial: false}, // partial continuation is handled at App level
				)
				if !decision.ShouldRetry {
					// Preserve partial response from chained tool call — include all
					// structured content (text, thinking, function calls).
					if resp != nil {
						if parts := e.buildResponsePartsRaw(resp); len(parts) > 0 {
							history = append(history, &genai.Content{
								Role:  genai.RoleModel,
								Parts: parts,
							})
						}
					}
					return history, "", fmt.Errorf("function response error (%s): %w", ft.Reason, err)
				}

				if decision.Partial {
					partialStreamRetries++
				} else {
					streamRetries++
				}
				if e.handler != nil && e.handler.OnWarning != nil {
					e.handler.OnWarning(fmt.Sprintf(
						"Request after tool results failed (%s), retrying (%d/%d) in %s...",
						decision.Reason,
						streamRetries,
						retryPolicy.MaxRetries,
						decision.Delay.Round(time.Second),
					))
				}
				if err := waitForRetry(ctx, decision.Delay); err != nil {
					return history, "", err
				}
			}

			// Text-based tool call fallback for chained calls
			if len(resp.FunctionCalls) == 0 && resp.Text != "" {
				if fallbackClient, ok := cl.(interface{ NeedsToolCallFallback() bool }); ok && fallbackClient.NeedsToolCallFallback() {
					if parsed := client.ParseToolCallsFromText(resp.Text); len(parsed) > 0 {
						resp.FunctionCalls = parsed
						resp.Text = ""
						logging.Info("fallback: parsed chained tool calls from text", "count", len(parsed))
					}
				}
			}

			// Add model response to history
			modelContent = &genai.Content{
				Role:  genai.RoleModel,
				Parts: e.buildResponseParts(resp),
			}

			history = append(history, modelContent)
		}

		// No more function calls, we have the final response
		finalText = resp.Text

		// Warn user when response was truncated by token limit
		if resp.FinishReason == genai.FinishReasonMaxTokens {
			finalText += "\n\n⚠ Response truncated (max_tokens limit reached). Use /config to increase max_output_tokens."
			logging.Warn("response truncated by max_tokens limit",
				"output_tokens", resp.OutputTokens)
		}

		// Capture token usage metadata from API response.
		// InputTokens: use latest value (last round includes full history context).
		// OutputTokens: accumulate across rounds (each round generates new output).
		e.tokenMu.Lock()
		if resp.InputTokens > 0 {
			e.lastInputTokens = resp.InputTokens
		}
		if resp.OutputTokens > 0 {
			e.lastOutputTokens += resp.OutputTokens
		}
		e.lastCacheCreationTokens = resp.CacheCreationInputTokens
		e.lastCacheReadTokens = resp.CacheReadInputTokens
		e.tokenMu.Unlock()

		// Track prompt cache usage for cache break detection
		if e.cacheTracker != nil && (resp.CacheCreationInputTokens > 0 || resp.CacheReadInputTokens > 0) {
			e.cacheTracker.RecordUsage(resp.CacheCreationInputTokens, resp.CacheReadInputTokens)
		}

		// Detect empty response — model returned no text and no function calls.
		// Break immediately instead of looping, the fallback below will provide a message.
		if finalText == "" {
			logging.Warn("model returned empty response",
				"finish_reason", string(resp.FinishReason),
				"input_tokens", resp.InputTokens,
				"output_tokens", resp.OutputTokens,
				"iteration", i)
			break
		}

		break
	}

	// CRITICAL: Ensure we always have a response
	if finalText == "" {
		// Check if tools were used in THIS request (not all history)
		// toolsUsed is populated during execution, so it only contains current turn's tools
		if len(toolsUsed) > 0 {
			// Use smart fallback based on the last tool used
			lastToolName := toolsUsed[len(toolsUsed)-1]
			finalText = GetSmartFallback(lastToolName, lastToolResult, toolsUsed)
			if e.handler != nil && e.handler.OnText != nil {
				e.handler.OnText(finalText)
			}
		} else {
			// No tools used and no text — model returned empty response
			finalText = fmt.Sprintf("⚠ Model returned an empty response. This may indicate:\n"+
				"- The model doesn't support the current configuration\n"+
				"- ThinkingBudget may need adjustment (try /config)\n"+
				"- Try switching to a different model with /model\n\n"+
				"Model: %s", cl.GetModel())
			if e.handler != nil && e.handler.OnText != nil {
				e.handler.OnText(finalText)
			}
		}
	}

	return history, finalText, nil
}

// calculateMaxIterations determines the optimal iteration limit based on context complexity.
// More complex tasks with longer history get more iterations.
func (e *Executor) calculateMaxIterations(history []*genai.Content) int {
	baseLimit := 20

	// Count conversation turns (pairs of user/model messages)
	turnCount := len(history) / 2

	// Estimate complexity based on history length
	switch {
	case turnCount > 30:
		// Very long conversation - allow more iterations
		return baseLimit + 30
	case turnCount > 20:
		// Long conversation - moderately more iterations
		return baseLimit + 20
	case turnCount > 10:
		// Medium conversation - slightly more iterations
		return baseLimit + 10
	default:
		// Short conversation - base limit
		return baseLimit
	}
}

// getModelResponse gets an initial response from the model.
// The cl parameter is a snapshot of the client taken at the start of the execute loop.
func (e *Executor) getModelResponse(ctx context.Context, cl client.Client, history []*genai.Content) (*client.Response, error) {
	if len(history) == 0 {
		return nil, fmt.Errorf("cannot get model response: empty history")
	}

	var message string
	lastContent := history[len(history)-1]
	if lastContent.Role == genai.RoleUser {
		for _, part := range lastContent.Parts {
			if part.Text != "" {
				message = part.Text
				break
			}
		}
	}

	historyWithoutLast := history[:len(history)-1]

	roundCtx, cancel := e.withModelRoundTimeout(ctx)
	defer cancel()

	stream, err := cl.SendMessageWithHistory(roundCtx, historyWithoutLast, message)
	if err != nil {
		return nil, err
	}

	return e.collectStreamWithHandler(roundCtx, stream)
}

func (e *Executor) withModelRoundTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := e.modelRoundTimeout
	if timeout <= 0 {
		timeout = defaultModelRoundTimeout
	}
	if deadline, ok := parent.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.WithCancel(parent)
		}
		if remaining < timeout {
			// Parent context is stricter; preserve its cause chain.
			return context.WithTimeout(parent, remaining)
		}
	}
	return context.WithTimeoutCause(parent, timeout, client.NewModelRoundTimeoutError(timeout))
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return client.ContextErr(ctx)
	}
}

// collectStreamWithHandler collects streaming response while calling handlers.
func (e *Executor) collectStreamWithHandler(ctx context.Context, stream *client.StreamingResponse) (*client.Response, error) {
	// Note: Don't pass OnError here - the executor handles error display
	// to avoid duplicate error messages
	var onText func(string)
	var onThinking func(string)
	var onTokenUpdate func(int, int)
	if e.handler != nil {
		onText = e.handler.OnText
		onThinking = e.handler.OnThinking
		onTokenUpdate = e.handler.OnTokenUpdate
	}
	return client.ProcessStream(ctx, stream, &client.StreamHandler{
		OnText:        onText,
		OnThinking:    onThinking,
		OnTokenUpdate: onTokenUpdate,
	})
}

// executeTools executes a list of function calls with enhanced safety checks.
func (e *Executor) executeTools(ctx context.Context, calls []*genai.FunctionCall) ([]*genai.FunctionResponse, error) {
	// Limit number of function calls to prevent resource exhaustion
	if len(calls) > MaxFunctionCallsPerResponse {
		logging.Warn("truncating function calls",
			"original_count", len(calls),
			"max_allowed", MaxFunctionCallsPerResponse)
		calls = calls[:MaxFunctionCallsPerResponse]
	}

	results := make([]*genai.FunctionResponse, len(calls))

	// Classify tools into dependency groups for optimal parallel execution.
	// Read-only tools (read, grep, glob, etc.) run in parallel within their group.
	// Write tools (edit, write, bash, etc.) run sequentially in their own group.
	// Groups execute sequentially to preserve write-before-read ordering.
	groups := defaultClassifier.classifyDependencies(calls)

	// Adaptive downgrade: if any tool in a parallel group has observed
	// success rate below 50% (with enough samples to trust the number),
	// switch the group to sequential. A batch of flaky tools tends to
	// compound into one giant failure; serial execution lets us stop early
	// on the first error and not multiply tokens on unrelated retries.
	if e.toolStatsLookup != nil {
		for i := range groups {
			if !groups[i].Parallel || len(groups[i].Calls) <= 1 {
				continue
			}
			if shouldSerializeGroup(groups[i].Calls, e.toolStatsLookup) {
				groups[i].Parallel = false
			}
		}
	}

	resultIdx := 0                                 // tracks position in the flat results array
	callToIdx := make(map[*genai.FunctionCall]int) // map call pointer to results index
	for i, call := range calls {
		callToIdx[call] = i
	}

	for _, group := range groups {
		if group.Parallel && len(group.Calls) > 1 {
			// Execute read-only tools in parallel
			var wg sync.WaitGroup
			var mu sync.Mutex
			semaphore := make(chan struct{}, MaxConcurrentToolExecutions)

			for _, call := range group.Calls {
				idx := callToIdx[call]
				wg.Add(1)
				go func(i int, fc *genai.FunctionCall) {
					defer wg.Done()
					select {
					case semaphore <- struct{}{}:
						defer func() { <-semaphore }()
					case <-ctx.Done():
						mu.Lock()
						results[i] = &genai.FunctionResponse{ID: fc.ID, Name: fc.Name, Response: NewErrorResult("cancelled").ToMap()}
						mu.Unlock()
						return
					}
					defer func() {
						if r := recover(); r != nil {
							stack := make([]byte, 4096)
							length := runtime.Stack(stack, false)
							logging.Error("tool execution panic", "tool", fc.Name, "panic", r, "stack", string(stack[:length]))
							mu.Lock()
							results[i] = &genai.FunctionResponse{
								ID: fc.ID, Name: fc.Name,
								Response: NewErrorResult(formatToolPanic(fc.Name, r)).ToMap(),
							}
							mu.Unlock()
						}
					}()
					// Use a goroutine-local context to avoid racing on the
					// shared ctx variable when multiple tools run in parallel.
					toolCtx := ctx
					if e.handler != nil && e.handler.OnFilePeek != nil {
						toolCtx = ContextWithFilePeekCallback(toolCtx, e.handler.OnFilePeek)
					}

					result := e.executeTool(toolCtx, fc)
					mu.Lock()
					results[i] = &genai.FunctionResponse{ID: fc.ID, Name: fc.Name, Response: result.ToMap()}
					mu.Unlock()
				}(idx, call)
			}
			wg.Wait()
		} else {
			// Execute sequentially (write tools or single tool)
			for _, call := range group.Calls {
				idx := callToIdx[call]
				select {
				case <-ctx.Done():
					results[idx] = &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: NewErrorResult("cancelled").ToMap()}
					continue
				default:
				}
				// Use a tool-local context (same pattern as the parallel path) so
				// the shared ctx variable is never mutated across iterations.
				toolCtx := ctx
				if e.handler != nil && e.handler.OnFilePeek != nil {
					toolCtx = ContextWithFilePeekCallback(toolCtx, e.handler.OnFilePeek)
				}
				// Recover from panics in sequential execution (parallel path already has this)
				func() {
					defer func() {
						if r := recover(); r != nil {
							stack := make([]byte, 4096)
							length := runtime.Stack(stack, false)
							logging.Error("panic in sequential tool execution", "tool", call.Name, "panic", r, "stack", string(stack[:length]))
							results[idx] = &genai.FunctionResponse{
								ID: call.ID, Name: call.Name,
								Response: NewErrorResult(formatToolPanic(call.Name, r)).ToMap(),
							}
						}
					}()
					result := e.executeTool(toolCtx, call)
					results[idx] = &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: result.ToMap()}
				}()
			}
		}
		resultIdx += len(group.Calls)
	}

	return results, nil
}

// executeTool executes a single tool call with enhanced safety and user awareness.
func (e *Executor) executeTool(ctx context.Context, call *genai.FunctionCall) ToolResult {
	// Step 0: Check circuit breaker
	e.breakerMu.Lock()
	breaker, ok := e.toolBreakers[call.Name]
	if !ok {
		// Initialize with default threshold (5 failures) and timeout (1 minute)
		breaker = robustness.NewCircuitBreaker(5, 1*time.Minute)
		e.toolBreakers[call.Name] = breaker
	}
	e.breakerMu.Unlock()

	var result ToolResult
	err := breaker.Execute(ctx, func() error {
		res := e.doExecuteTool(ctx, call)
		result = res
		if !res.Success && isExecutionFailure(res.Error) {
			return fmt.Errorf("tool execution failed: %s", res.Error)
		}
		return nil // validation/permission errors don't count as circuit breaker failures
	})

	if err != nil {
		if errors.Is(err, robustness.ErrCircuitOpen) {
			// Try self-healing before returning circuit open error
			if healed := e.trySelfHeal(ctx, call); healed.Success {
				logging.Info("self-healing succeeded", "tool", call.Name)
				return healed
			}
			return NewErrorResult(fmt.Sprintf("Circuit breaker for '%s' is open. Too many failures. Please wait before trying again.", call.Name))
		}
		// If it's not circuit open error, it's the wrapped tool error, which is already in 'result'
	}

	return result
}

// isExecutionFailure returns true if the error message represents a real tool execution
// failure (as opposed to validation, permission, or lookup errors that should not
// trip the circuit breaker).
func isExecutionFailure(errMsg string) bool {
	for _, prefix := range []string{
		"validation error:", "Permission denied:", "permission error:",
		"Safety check failed:", "unknown tool:",
	} {
		if strings.HasPrefix(errMsg, prefix) {
			return false
		}
	}
	return true
}

// doExecuteTool handles the actual execution logic (previously body of executeTool).
func (e *Executor) doExecuteTool(ctx context.Context, call *genai.FunctionCall) (result ToolResult) {
	toolStart := time.Now()
	defer func() {
		// Fire phase observer even on early returns. Success is determined by
		// the final ToolResult — unknown-tool / validation errors all count as
		// failures in the metrics.
		if e.phaseObserver != nil {
			e.phaseObserver(call.Name, time.Since(toolStart), result.Success)
		}
	}()

	// Step 1: Basic tool lookup and validation
	tool, ok := e.registry.Get(call.Name)
	if !ok {
		return NewErrorResult(formatUnknownToolError(call.Name, e.registry.Names()))
	}

	// Plan-mode gate (defense in depth). The LLM should never emit a call for
	// a non-read-only tool because the schema filter strips those — but models
	// occasionally hallucinate calls based on pre-training, especially when
	// the user's prompt names a write-style operation. Without this block, a
	// hallucinated `write` or `bash` call would slip past the schema and
	// actually execute. With it, the model gets a targeted error and re-plans.
	if e.planModeCheck != nil && e.planModeCheck() && !IsReadOnlyForPlanMode(call.Name) {
		return NewErrorResult(fmt.Sprintf(
			"tool %q is not allowed in plan mode — only read-only tools (read, glob, grep, list_dir, tree, diff, git_status/diff/log/blame/branch, web_fetch, web_search, memory, ask_user, etc.) + enter_plan_mode/exit_plan_mode are available. Call enter_plan_mode with your proposed plan when ready to request user approval.",
			call.Name,
		))
	}

	// Guard against nil args — some model responses may omit the args field
	// entirely, producing a nil map that panics on Validate.
	if call.Args == nil {
		call.Args = make(map[string]any)
	}

	if err := tool.Validate(call.Args); err != nil {
		return NewErrorResult(fmt.Sprintf("validation error: %s", err))
	}

	// Retry dedup guard: skip duplicate side-effect calls during retry windows.
	if deduped, reason, ok := e.lookupDuplicateSideEffect(call); ok {
		logging.Warn("skipping duplicate side-effect tool call",
			"tool", call.Name,
			"call_id", call.ID,
			"reason", reason)
		if e.handler != nil && e.handler.OnWarning != nil {
			e.handler.OnWarning(fmt.Sprintf("Skipped duplicate side-effect tool call '%s' (%s). Reusing previous result.", call.Name, reason))
		}
		return deduped
	}

	// Checkpoint recovery: reuse result for write operations that already completed
	// in a previous iteration (e.g., API failure after tool executed successfully).
	if e.checkpoint != nil && isWriteOperation(call.Name) {
		if cached, reason, ok := e.checkpoint.Lookup(call); ok {
			logging.Info("checkpoint recovery: reusing previous tool result",
				"tool", call.Name,
				"reason", reason)
			if e.handler != nil && e.handler.OnWarning != nil {
				e.handler.OnWarning(fmt.Sprintf("Recovered result for '%s' from checkpoint (%s).", call.Name, reason))
			}
			return cached
		}
	}

	// Pre-mutation barrier: if previous delta-check failed, block further mutating calls.
	if shouldRunDeltaCheckCall(call) {
		if reason, blocked := e.enforceDeltaCheckBarrier(ctx, call); blocked {
			if e.handler != nil && e.handler.OnToolDenied != nil {
				e.handler.OnToolDenied(call.Name, reason)
			}
			if e.notificationMgr != nil {
				e.notificationMgr.NotifyDenied(call.Name, reason)
			}
			return NewErrorResult(reason)
		}
	}

	// Step 2: Pre-flight safety checks
	var preFlight *PreFlightCheck
	if e.preFlightChecks && e.safetyValidator != nil {
		var err error
		preFlight, err = e.safetyValidator.ValidateSafety(ctx, call.Name, call.Args)
		if err != nil {
			logging.Warn("safety check failed", "tool", call.Name, "error", err)
		}

		// Notify user about validation
		if e.handler != nil && e.handler.OnToolValidating != nil {
			e.handler.OnToolValidating(call.Name, preFlight)
		}

		// Emit warnings to notification system
		if preFlight != nil && len(preFlight.Warnings) > 0 {
			if e.notificationMgr != nil {
				e.notificationMgr.NotifyValidation(call.Name, preFlight)
			}
			if e.handler != nil && e.handler.OnWarning != nil {
				for _, warning := range preFlight.Warnings {
					e.handler.OnWarning(fmt.Sprintf("[%s] %s", call.Name, warning))
				}
			}
		}

		// Block execution if safety check failed (unless unrestricted mode is on)
		if preFlight != nil && !preFlight.IsValid {
			reasons := strings.Join(preFlight.Errors, "; ")

			if e.unrestrictedMode {
				// In unrestricted mode, convert errors to warnings and allow execution
				preFlight.Warnings = append(preFlight.Warnings, preFlight.Errors...)
				preFlight.Errors = nil
				preFlight.IsValid = true
				logging.Warn("unrestricted mode: safety check bypassed",
					"tool", call.Name,
					"original_errors", reasons)
				if e.handler != nil && e.handler.OnWarning != nil {
					e.handler.OnWarning(fmt.Sprintf("[%s] Bypassed in unrestricted mode: %s", call.Name, reasons))
				}
			} else {
				if e.handler != nil && e.handler.OnToolDenied != nil {
					e.handler.OnToolDenied(call.Name, reasons)
				}
				if e.notificationMgr != nil {
					e.notificationMgr.NotifyDenied(call.Name, reasons)
				}
				return NewErrorResult(fmt.Sprintf("Safety check failed: %s", reasons))
			}
		}
	}

	// Step 3: Get execution summary for user awareness
	var summary *ExecutionSummary
	if e.safetyValidator != nil {
		summary = e.safetyValidator.GetSummary(call.Name, call.Args)
	}

	// Step 4: Permission check
	if e.permissions != nil {
		resp, err := e.permissions.Check(ctx, call.Name, call.Args)
		if err != nil {
			if e.handler != nil && e.handler.OnToolDenied != nil {
				e.handler.OnToolDenied(call.Name, err.Error())
			}
			if e.notificationMgr != nil {
				e.notificationMgr.NotifyDenied(call.Name, err.Error())
			}
			return NewErrorResult(fmt.Sprintf("permission error: %s", err))
		}
		if !resp.Allowed {
			reason := resp.Reason
			if reason == "" {
				reason = "permission denied"
			}
			if e.handler != nil && e.handler.OnToolDenied != nil {
				e.handler.OnToolDenied(call.Name, reason)
			}
			if e.notificationMgr != nil {
				e.notificationMgr.NotifyDenied(call.Name, reason)
			}
			return NewErrorResult(fmt.Sprintf("Permission denied: %s", reason))
		}
	}

	// Step 4.5: Check tool result cache for read-only tools
	if e.toolCache != nil {
		if cached, hit := e.toolCache.Get(call.Name, call.Args); hit {
			logging.Debug("tool cache hit", "tool", call.Name)
			// For read tool: replace cached content with stub if file unchanged,
			// saving context window space on repeated reads.
			if call.Name == "read" && e.readTracker != nil && cached.Success {
				filePath, _ := call.Args["file_path"].(string)
				offset := 1
				limit := 2000
				if v, ok := call.Args["offset"].(float64); ok {
					offset = int(v)
				}
				if v, ok := call.Args["limit"].(float64); ok {
					limit = int(v)
				}
				if filePath != "" {
					isDup, origRec, _ := e.readTracker.CheckAndRecord(filePath, offset, limit, len(cached.Content))
					if isDup && origRec != nil {
						cached.Content = fmt.Sprintf(
							"[File already read at turn %d, content unchanged (%d chars). Path: %s]",
							origRec.TurnIndex, origRec.ContentLen, filePath)
						return cached
					}
				}
			}
			return cached
		}
	}

	// Step 5: Notify user about approved execution
	if summary != nil && summary.UserVisible {
		if e.notificationMgr != nil {
			e.notificationMgr.NotifyApproved(call.Name, summary)
		}
		if e.handler != nil && e.handler.OnToolApproved != nil {
			e.handler.OnToolApproved(call.Name, summary)
		}
	}

	// Step 6: Run pre-tool hooks
	if e.hooks != nil {
		e.hooks.RunPreTool(ctx, call.Name, call.Args)
	}

	// Step 7: Create execution context
	execInfo := &ExecutionInfo{
		StartTime:      time.Now(),
		ToolName:       call.Name,
		Args:           call.Args,
		Summary:        summary,
		PreFlightCheck: preFlight,
	}
	if summary != nil {
		execInfo.SafetyLevel = summary.RiskLevel
	}

	// Notify start
	if e.handler != nil && e.handler.OnToolStart != nil {
		e.handler.OnToolStart(call.Name, call.Args)
	}

	// If history suggests this tool is slow, give the user a heads-up before
	// it blocks the status bar for many seconds. Threshold matches adaptive
	// timeout floor — below 5s we don't consider it "slow" enough to warn.
	if e.handler != nil && e.handler.OnWarning != nil && e.toolStatsLookup != nil {
		if p95, _, ok := e.toolStatsLookup(call.Name); ok && p95 >= 5*time.Second {
			e.handler.OnWarning(fmt.Sprintf(
				"%s typically takes ~%v (p95 from session history)",
				call.Name, p95.Round(time.Second)))
		}
	}

	// Step 8: Execute with timeout. If we have enough observations for this
	// tool, auto-tune the timeout via adaptiveToolTimeout so fast tools fail
	// fast while slow tools get historically-observed room to breathe.
	var observedP95 time.Duration
	var haveStats bool
	if e.toolStatsLookup != nil {
		p95, _, ok := e.toolStatsLookup(call.Name)
		observedP95 = p95
		haveStats = ok
	}
	toolTimeout := adaptiveToolTimeout(e.timeout, observedP95, haveStats)
	execCtx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()

	// Inject streaming callback so tools (e.g. task/agent) can stream output to UI
	if e.handler != nil && e.handler.OnText != nil {
		execCtx = ContextWithStreamingCallback(execCtx, StreamingCallback(e.handler.OnText))
	}

	// Inject progress callback so tools can report granular progress to UI
	if e.handler != nil && e.handler.OnToolDetailedProgress != nil {
		toolName := call.Name
		onDetailed := e.handler.OnToolDetailedProgress
		execCtx = ContextWithProgressCallback(execCtx, func(progress float64, currentStep string) {
			onDetailed(toolName, progress, currentStep)
		})
	}

	// Inject memory notification callback
	if e.handler != nil && e.handler.OnMemoryNotify != nil {
		execCtx = ContextWithMemoryNotify(execCtx, e.handler.OnMemoryNotify)
	}

	start := time.Now()

	// Start progress heartbeat for long-running operations
	// Use 500ms interval for responsive UI feedback (was 5s)
	done := make(chan struct{})
	if e.handler != nil && e.handler.OnToolProgress != nil {
		onProgress := e.handler.OnToolProgress // snapshot to avoid nil checks in goroutine
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logging.Error("panic in tool progress goroutine", "tool", call.Name, "panic", r)
				}
			}()

			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					onProgress(call.Name, time.Since(start))
				case <-done:
					return
				case <-execCtx.Done():
					return
				}
			}
		}()
	}

	// Check if tool supports streaming for large outputs
	var err error
	if streamingTool, ok := tool.(StreamingTool); ok && streamingTool.SupportsStreaming() {
		result, err = e.executeStreamingTool(execCtx, streamingTool, call.Args)
	} else {
		result, err = tool.Execute(execCtx, call.Args)
	}
	close(done)
	duration := time.Since(start)

	if err != nil {
		// Provide informative timeout message with tool name and duration
		if errors.Is(err, context.DeadlineExceeded) {
			errMsg := fmt.Sprintf("tool '%s' timed out after %v (limit: %v)", call.Name, duration.Round(time.Millisecond), toolTimeout)
			logging.Error("tool execution timed out",
				"tool", call.Name,
				"duration", duration,
				"timeout", e.timeout)
			result = NewErrorResult(errMsg)
		} else {
			logging.Error("tool execution failed",
				"tool", call.Name,
				"error", err,
				"duration", duration)
			result = NewErrorResult(err.Error())
		}
	}

	// Enrich error results with actionable suggestions
	if !result.Success && result.Error != "" {
		if suggestion := getErrorSuggestion(result.Error); suggestion != "" {
			result.Error = result.Error + "\nSuggestion: " + suggestion
		}
	}

	// Enrich empty successful results with diagnostic hints
	if result.Success && result.Content == "" {
		if hint := getEmptyResultHint(call.Name); hint != "" {
			result.Content = hint
		}
	}

	// Enrich result with execution metadata
	if result.ExecutionSummary == nil && summary != nil {
		result.ExecutionSummary = summary
	}
	result.SafetyLevel = execInfo.SafetyLevel
	result.Duration = format.Duration(duration)

	// Step 9: Log to audit
	if e.auditLogger != nil {
		entry := audit.NewEntry(e.sessionID, call.Name, call.Args)
		entry.Complete(result.Content, result.Success, result.Error, duration)

		// Add safety context to audit log
		if preFlight != nil {
			entry.Args["safety_warnings"] = preFlight.Warnings
			entry.Args["safety_level"] = string(execInfo.SafetyLevel)
		}

		if err := e.auditLogger.Log(entry); err != nil {
			logging.Warn("failed to write audit log", "error", err, "tool", call.Name)
		}
	}

	// Step 10: Run post-tool or on-error hooks
	if e.hooks != nil {
		if result.Success {
			e.hooks.RunPostTool(ctx, call.Name, call.Args, result.Content)
		} else {
			e.hooks.RunOnError(ctx, call.Name, call.Args, result.Error)
		}
	}

	// Step 10.5: Auto-format written files
	if e.formatter != nil && result.Success {
		if call.Name == "write" || call.Name == "edit" {
			if filePath, ok := call.Args["file_path"].(string); ok {
				_ = e.formatter.Format(ctx, filePath)
			}
		}
	}

	// Step 10.7: Emit inline diff for edit operations
	if e.handler != nil && e.handler.OnInlineDiff != nil && result.Success {
		if call.Name == "edit" {
			oldText, _ := call.Args["old_string"].(string)
			newText, _ := call.Args["new_string"].(string)
			if oldText != "" && oldText != newText {
				filePath, _ := call.Args["file_path"].(string)
				e.handler.OnInlineDiff(filePath, oldText, newText)
			}
		}
	}

	// Step 11: apply redaction
	if e.redactor != nil {
		result.Content = e.redactor.Redact(result.Content)
		result.Error = e.redactor.Redact(result.Error)
		if result.Data != nil {
			result.Data = e.redactor.RedactAny(result.Data)
		}
	}

	// Step 12: Apply compaction if configured
	if e.compactor != nil && result.Success {
		result = e.compactor.CompactForType(call.Name, result)
	}

	// Step 12.5: Cache result or invalidate cache for write operations
	if isWriteOperation(call.Name) {
		// Invalidate all caches that might reference the modified file
		filePaths := extractFilePaths(call)
		if e.toolCache != nil {
			for _, p := range filePaths {
				e.toolCache.InvalidateByFile(p)
			}
			// Git state changes when files are modified
			e.toolCache.InvalidateByTool("git_status")
			e.toolCache.InvalidateByTool("git_diff")
			// tree/list_dir may be stale after file create/delete
			if call.Name == "write" || call.Name == "delete" || call.Name == "mkdir" ||
				call.Name == "copy" || call.Name == "move" {
				e.toolCache.InvalidateByTool("tree")
				e.toolCache.InvalidateByTool("list_dir")
			}
		}
		if e.searchCache != nil {
			for _, p := range filePaths {
				e.searchCache.InvalidateByPath(p)
			}
		}
	} else if e.toolCache != nil && result.Success {
		// Only cache read-only tool results
		e.toolCache.Put(call.Name, call.Args, result)
	}

	// Step 12.7: Deduplicate read content for history optimization
	if e.readTracker != nil {
		if call.Name == "read" && result.Success {
			filePath, _ := call.Args["file_path"].(string)
			offset := 1
			limit := 2000
			if v, ok := call.Args["offset"].(float64); ok {
				offset = int(v)
			}
			if v, ok := call.Args["limit"].(float64); ok {
				limit = int(v)
			}
			if filePath != "" {
				isDup, origRec, _ := e.readTracker.CheckAndRecord(filePath, offset, limit, len(result.Content))
				if isDup && origRec != nil {
					result.Content = fmt.Sprintf(
						"[File already read at turn %d, content unchanged (%d chars). Path: %s]",
						origRec.TurnIndex, origRec.ContentLen, filePath)
				}
			}
		}
		// Invalidate read tracker on write operations (all path-like arg keys,
		// including source/destination for copy/move).
		if isWriteOperation(call.Name) {
			for _, key := range []string{"file_path", "path", "source", "destination", "new_path"} {
				if p, ok := call.Args[key].(string); ok && p != "" {
					e.readTracker.InvalidateFile(p)
				}
			}
		}
	}

	// Record successfully modified files in the write tracker.
	if result.Success && e.writeTracker != nil && isFileModifyingTool(call.Name) {
		for _, key := range []string{"file_path", "path", "destination", "new_path"} {
			if p, ok := call.Args[key].(string); ok && p != "" {
				e.writeTracker.Record(p)
			}
		}
	}

	// Step 12.9: Post-edit delta-check on changed modules (format/lint/build; no tests).
	if result.Success && shouldRunDeltaCheckCall(call) {
		if delta := e.runDeltaCheckAfterMutation(ctx, call); delta != nil {
			if !delta.Passed {
				logDeltaCheckFailure(*delta)
				if e.handler != nil && e.handler.OnWarning != nil {
					e.handler.OnWarning(delta.Summary)
				}
				if result.Content != "" {
					result.Content += "\n\n" + delta.Summary
				} else {
					result.Content = delta.Summary
				}
				if detail := strings.TrimSpace(delta.Details); detail != "" {
					result.Content += "\n" + trimDeltaOutput(detail, 1200)
				}
			}
		}
	}

	// Step 12.9b: Semantic validators (test quality, CI workflow, version consistency).
	if result.Success && shouldRunDeltaCheckCall(call) && e.semanticValidators != nil {
		for _, fp := range extractFilePaths(call) {
			content, err := os.ReadFile(fp)
			if err != nil {
				continue
			}
			warnings := e.semanticValidators.RunAll(ctx, fp, content, e.workDir)
			if formatted := FormatWarnings(warnings); formatted != "" {
				result.Content += "\n\n" + formatted
			}
			// Learn from warnings for future prevention
			if e.errorLearner != nil {
				for _, w := range warnings {
					if w.Severity == "error" || w.Severity == "warning" {
						_ = e.errorLearner.LearnError(
							"validation_"+w.Validator,
							w.Message,
							"Before writing "+filepath.Ext(fp)+" files, check: "+w.Message,
							[]string{w.Validator, filepath.Ext(fp)},
						)
					}
				}
			}
		}
	}

	// Step 12.9c: Context enrichment (inject canonical versions, function signatures).
	if result.Success && shouldRunDeltaCheckCall(call) && e.contextEnricher != nil {
		for _, fp := range extractFilePaths(call) {
			if hint := e.contextEnricher.Enrich(fp); hint != "" {
				result.Content += "\n\n" + hint
			}
		}
	}

	// Step 12.9d: Track mutated files for self-review injection.
	if result.Success && isWriteOperation(call.Name) && e.selfReviewThreshold > 0 {
		e.mutatedFilesMu.Lock()
		for _, fp := range extractFilePaths(call) {
			e.mutatedFilesThisTurn[fp] = true
		}
		e.mutatedFilesMu.Unlock()
	}

	// Step 13: Notify completion and send notifications
	if e.handler != nil && e.handler.OnToolEnd != nil {
		e.handler.OnToolEnd(call.Name, call.Args, result)
	}

	if e.notificationMgr != nil {
		if result.Success {
			// Nil check for summary before calling String()
			var summaryStr string
			if summary != nil {
				summaryStr = summary.String()
			}
			e.notificationMgr.NotifySuccess(call.Name, summaryStr, summary, duration)
		} else {
			e.notificationMgr.NotifyError(call.Name, "Execution failed", result.Error)
		}
	}

	// Log execution metrics
	logging.Info("tool execution completed",
		"tool", call.Name,
		"success", result.Success,
		"duration", duration,
		"safety_level", execInfo.SafetyLevel)

	// Record side effects for retry deduplication.
	e.recordSideEffect(call, result)

	// Record in checkpoint journal for API failure recovery (write operations only).
	if e.checkpoint != nil && isWriteOperation(call.Name) {
		e.checkpoint.Record(call, result)
	}

	return result
}

func (e *Executor) lookupDuplicateSideEffect(call *genai.FunctionCall) (ToolResult, string, bool) {
	if call == nil || !isWriteOperation(call.Name) {
		return ToolResult{}, "", false
	}

	e.sideEffectLedgerMu.Lock()
	defer e.sideEffectLedgerMu.Unlock()

	if !e.sideEffectDedupEnabled {
		return ToolResult{}, "", false
	}

	if call.ID != "" {
		if prev, ok := e.sideEffectsByCallID[call.ID]; ok {
			return prev, "tool_call_id", true
		}
	}

	signature := sideEffectSignature(call.Name, call.Args)
	if signature != "" {
		if prev, ok := e.sideEffectsBySignature[signature]; ok {
			return prev, "fingerprint", true
		}
	}

	return ToolResult{}, "", false
}

func (e *Executor) recordSideEffect(call *genai.FunctionCall, result ToolResult) {
	if call == nil || !isWriteOperation(call.Name) {
		return
	}

	e.sideEffectLedgerMu.Lock()
	defer e.sideEffectLedgerMu.Unlock()

	// Store result as-is: failed side-effecting calls can still have partial effects.
	if call.ID != "" {
		e.sideEffectsByCallID[call.ID] = result
	}
	if signature := sideEffectSignature(call.Name, call.Args); signature != "" {
		e.sideEffectsBySignature[signature] = result
	}
}

func sideEffectSignature(toolName string, args map[string]any) string {
	if toolName == "" {
		return ""
	}

	normalized := make(map[string]any, len(args))
	for k, v := range args {
		key := strings.ToLower(strings.TrimSpace(k))
		switch key {
		case "timestamp", "ts", "nonce", "request_id":
			continue
		default:
			normalized[k] = v
		}
	}

	encoded, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(toolName + "|" + string(encoded)))
	return hex.EncodeToString(sum[:])
}

// buildResponseParts returns Parts from a response.
// Adds a fallback " " text part if the response has no content to ensure valid history.
func (e *Executor) buildResponseParts(resp *client.Response) []*genai.Part {
	parts := e.buildResponsePartsRaw(resp)
	if len(parts) == 0 {
		parts = append(parts, genai.NewPartFromText(" "))
	}
	return parts
}

// buildResponsePartsRaw returns Parts from a response without a fallback placeholder.
// Used for partial response preservation where empty means "nothing to preserve".
func (e *Executor) buildResponsePartsRaw(resp *client.Response) []*genai.Part {
	if len(resp.Parts) > 0 {
		return resp.Parts
	}

	var parts []*genai.Part

	if resp.Text != "" {
		parts = append(parts, genai.NewPartFromText(resp.Text))
	}

	for _, fc := range resp.FunctionCalls {
		parts = append(parts, &genai.Part{FunctionCall: fc})
	}

	return parts
}

// executeStreamingTool executes a streaming tool and collects results.
// Streams chunks to the handler for real-time feedback.
func (e *Executor) executeStreamingTool(ctx context.Context, tool StreamingTool, args map[string]any) (ToolResult, error) {
	stream, err := tool.ExecuteStreaming(ctx, args)
	if err != nil {
		return NewErrorResult(err.Error()), nil
	}

	// Collect streaming output, optionally streaming to handler
	var content strings.Builder
	chunkCount := 0

streamLoop:
	for {
		select {
		case chunk, ok := <-stream.Chunks:
			if !ok {
				// Chunks channel closed
				break streamLoop
			}
			content.WriteString(chunk)
			chunkCount++

			// Stream to handler for real-time feedback (every 10 chunks or significant content)
			if e.handler != nil && e.handler.OnText != nil && (chunkCount%10 == 0 || len(chunk) > 100) {
				// Note: OnText is for model responses, but we can use it for streaming tool output
				// This may need a separate handler in a future iteration
			}

		case err := <-stream.Error:
			if err != nil {
				return NewErrorResult(err.Error()), nil
			}

		case <-stream.Done:
			// Drain remaining chunks
			for chunk := range stream.Chunks {
				content.WriteString(chunk)
			}
			// Check for any error sent before complete() closed the channels.
			select {
			case streamErr := <-stream.Error:
				if streamErr != nil {
					return NewErrorResult(streamErr.Error()), nil
				}
			default:
			}
			break streamLoop

		case <-ctx.Done():
			// Drain chunks in background to unblock the producer goroutine.
			// Cap the drain so a stuck producer can't leak a goroutine forever.
			go func() {
				drainTimer := time.NewTimer(5 * time.Second)
				defer drainTimer.Stop()
				for {
					select {
					case _, ok := <-stream.Chunks:
						if !ok {
							return
						}
					case <-drainTimer.C:
						return
					}
				}
			}()
			return NewErrorResult("streaming cancelled"), client.ContextErr(ctx)
		}
	}

	result := content.String()
	if result == "" {
		return NewSuccessResult("No output."), nil
	}
	return NewSuccessResult(result), nil
}

// trySelfHeal attempts alternative approaches when a tool's circuit breaker is open.
func (e *Executor) trySelfHeal(ctx context.Context, call *genai.FunctionCall) ToolResult {
	switch call.Name {
	case "read":
		// If read fails, try glob to find similar files
		if path, ok := call.Args["file_path"].(string); ok {
			globTool, exists := e.registry.Get("glob")
			if exists {
				globResult, err := globTool.Execute(ctx, map[string]any{
					"pattern": path + "*",
				})
				if err == nil && globResult.Success {
					return ToolResult{
						Content: fmt.Sprintf("File read failed (circuit open). Similar files found:\n%s", globResult.Content),
						Success: true,
					}
				}
			}
		}

	case "bash":
		// If bash fails repeatedly, suggest debug mode
		if cmd, ok := call.Args["command"].(string); ok {
			return ToolResult{
				Content: fmt.Sprintf("Bash execution failed repeatedly (circuit open). Suggestion: try 'bash -x %s' for debug output, or check if the command requires different permissions.", cmd),
				Success: true,
			}
		}

	case "grep":
		// If grep fails, try read with manual search
		if pattern, ok := call.Args["pattern"].(string); ok {
			return ToolResult{
				Content: fmt.Sprintf("Grep circuit open. Try using 'read' tool to read specific files and search for '%s' manually.", pattern),
				Success: true,
			}
		}
	}

	return ToolResult{Success: false}
}

// writeTools is a set of tools that modify files/state, used for deduplication.
var writeTools = map[string]bool{
	"write":       true,
	"edit":        true,
	"delete":      true,
	"atomicwrite": true,
	"copy":        true,
	"move":        true,
	"mkdir":       true,
	"batch":       true,
	"refactor":    true,
	"bash":        true, // bash can modify anything
	"git_add":     true,
	"git_commit":  true,
}

// extractFilePaths returns all file paths from a tool call's arguments.
// Handles file_path, path, source, destination, and new_path argument names.
func extractFilePaths(call *genai.FunctionCall) []string {
	var paths []string
	for _, key := range []string{"file_path", "path", "source", "destination", "new_path"} {
		if p, ok := call.Args[key].(string); ok && p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// isWriteOperation returns true if the tool modifies files/state.
func isWriteOperation(toolName string) bool {
	return writeTools[toolName]
}

// fileModifyingTools is the subset of writeTools that produce a specific
// file path we can surface in post-compaction hints (excludes bash/git_commit
// whose "written path" is opaque or irrelevant as a hint).
var fileModifyingTools = map[string]bool{
	"write":       true,
	"edit":        true,
	"atomicwrite": true,
	"copy":        true,
	"move":        true,
	"refactor":    true,
}

// isFileModifyingTool returns true if the tool produces a named file path
// worth recording in the write tracker.
func isFileModifyingTool(toolName string) bool {
	return fileModifyingTools[toolName]
}

// getEmptyResultHint returns a diagnostic hint when a tool returns empty results.
// This helps the model understand what happened and suggest alternatives.
func getEmptyResultHint(toolName string) string {
	hints := map[string]string{
		"read": "The file is empty or could not be read. Check if the file exists and has content.",
		"grep": "No matches found. Try a simpler pattern, expand search scope, or check pattern syntax.",
		"glob": "No files match this pattern. Try a broader pattern like '**/*' or check the directory exists.",
		"bash": "Command produced no output. This may be expected (e.g., mkdir, cp succeed silently). Run with verbose flags (-v) for more details.",
	}
	if hint, ok := hints[toolName]; ok {
		return hint
	}
	return ""
}

// errorSuggestions maps common error patterns to helpful suggestions.
var errorSuggestions = []struct {
	pattern    string
	suggestion string
}{
	{"permission denied", "Try running with appropriate permissions or check file ownership."},
	{"no such file", "Use glob to find the correct path."},
	{"command not found", "Check if the tool is installed: which <command>."},
	{"connection refused", "Check if the service is running."},
	{"timed out", "Try a simpler operation or use run_in_background=true."},
}

// getErrorSuggestion returns a suggestion for a given error message.
func getErrorSuggestion(errMsg string) string {
	lower := strings.ToLower(errMsg)
	for _, es := range errorSuggestions {
		if strings.Contains(lower, es.pattern) {
			return es.suggestion
		}
	}
	return ""
}

// getToolFilePath extracts the primary file path from tool arguments.
func getToolFilePath(call *genai.FunctionCall) string {
	if call.Args == nil {
		return ""
	}
	if path, ok := call.Args["path"].(string); ok {
		return path
	}
	if path, ok := call.Args["file_path"].(string); ok {
		return path
	}
	return ""
}

// orderByDependencies reorders tool calls so that writes happen before reads on the same file.
// Returns the ordered calls and a flag indicating if reordering was needed.
func orderByDependencies(calls []*genai.FunctionCall) ([]*genai.FunctionCall, bool) {
	if len(calls) <= 1 {
		return calls, false
	}

	// Build file->writer and file->reader maps
	writers := make(map[string]int)   // file -> index of write call
	readers := make(map[string][]int) // file -> indices of read calls

	for i, call := range calls {
		path := getToolFilePath(call)
		if path == "" {
			continue
		}
		if isWriteOperation(call.Name) {
			writers[path] = i
		} else {
			readers[path] = append(readers[path], i)
		}
	}

	// Check if any reader depends on a writer
	hasConflict := false
	blockedBy := make(map[int]int) // reader_index -> writer_index it depends on

	for file, writerIdx := range writers {
		if readerIndices, ok := readers[file]; ok {
			for _, readerIdx := range readerIndices {
				blockedBy[readerIdx] = writerIdx
				hasConflict = true
			}
		}
	}

	if !hasConflict {
		return calls, false
	}

	// Simple topological sort: writers first, then readers
	ordered := make([]*genai.FunctionCall, 0, len(calls))
	added := make(map[int]bool)

	// First pass: add all writers and non-conflicting calls
	for i, call := range calls {
		if _, isBlocked := blockedBy[i]; !isBlocked {
			ordered = append(ordered, call)
			added[i] = true
		}
	}

	// Second pass: add blocked readers
	for i, call := range calls {
		if !added[i] {
			ordered = append(ordered, call)
		}
		_ = call // suppress unused warning
	}

	return ordered, true
}

// readIntArg extracts a positional integer argument from a tool call, tolerating
// both int and float64 (JSON unmarshal picks float64 by default). Missing or
// unparseable values return 0. Used to fingerprint paged reads — "0" is a fine
// sentinel for "no offset specified".
func readIntArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

// stagnationFingerprint returns a short string that distinguishes different
// invocations of the same tool. For example, writing 5 different files should
// produce 5 different fingerprints, while writing the same file 5 times
// produces the same fingerprint (true stagnation).
func stagnationFingerprint(toolName string, args map[string]any) string {
	// Extract the key distinguishing argument per tool
	switch toolName {
	case "read":
		// For read, a large file is legitimately paged in chunks — same
		// file_path but different offset/limit means forward progress, not
		// stagnation. Include the paging coordinates so the guard fires only
		// when the model truly requests the same range over and over.
		// Regression this fixes: reading ~3000-line tui.go in 2000-line
		// chunks tripped the 5-repeat abort when offset just kept growing.
		fp, ok := args["file_path"].(string)
		if !ok || fp == "" {
			// Missing required arg — match write/edit/delete fallback so
			// five bad reads in a row still trigger the stagnation abort.
			return ""
		}
		return fmt.Sprintf("%s@%d+%d",
			filepath.Base(fp),
			readIntArg(args, "offset"),
			readIntArg(args, "limit"))
	case "write", "edit", "delete":
		if fp, ok := args["file_path"].(string); ok {
			return filepath.Base(fp)
		}
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			// Strip leading "cd /path && " prefix — models often prepend this,
			// making all commands look identical in the first 40 chars.
			// Use first occurrence of " && " to split cd from actual command.
			if idx := strings.Index(cmd, " && "); idx >= 0 && strings.HasPrefix(strings.TrimSpace(cmd), "cd ") {
				cmd = cmd[idx+4:]
			}
			// Use first 60 runes of the actual command
			if runes := []rune(cmd); len(runes) > 60 {
				cmd = string(runes[:60])
			}
			return cmd
		}
	case "grep":
		if p, ok := args["pattern"].(string); ok {
			return p
		}
	case "glob":
		if p, ok := args["pattern"].(string); ok {
			return p
		}
	case "copy", "move":
		src, _ := args["source"].(string)
		dst, _ := args["destination"].(string)
		if src != "" {
			return filepath.Base(src) + "->" + filepath.Base(dst)
		}
	case "git_add":
		if p, ok := args["path"].(string); ok {
			return p
		}
	case "web_fetch":
		if u, ok := args["url"].(string); ok {
			if runes := []rune(u); len(runes) > 50 {
				u = string(runes[:50])
			}
			return u
		}
	case "web_search":
		if q, ok := args["query"].(string); ok {
			return q
		}
	}
	// Default: no distinguishing argument, tool name alone is the pattern
	return ""
}

func shouldAttemptStagnationRecovery(model string, calls []*genai.FunctionCall, attempts int) bool {
	if attempts > 0 || len(calls) == 0 {
		return false
	}
	if client.GetModelProfile(model).Family != "kimi" {
		return false
	}
	for _, call := range calls {
		if call == nil || !isKimiStagnationRecoveryTool(call.Name) {
			return false
		}
	}
	return true
}

// shouldEnforceKimiToolBudget reports whether the per-turn tool budget cap
// has been reached for the currently active Kimi-family model. Non-Kimi
// models and a disabled budget (0) always return false. `consumed` is the
// count of tool calls already executed in this turn before the current
// batch — callers pass len(toolsUsed)-len(newCalls) so the cap applies to
// the NEXT batch rather than trimming an in-flight one.
func (e *Executor) shouldEnforceKimiToolBudget(model string, consumed int) bool {
	if e == nil || e.kimiToolBudget <= 0 {
		return false
	}
	if client.GetModelProfile(model).Family != "kimi" {
		return false
	}
	return consumed >= e.kimiToolBudget
}

// buildKimiToolBudgetExhaustedResults synthesizes error-result FunctionResponses
// for the current batch of calls, one per call, carrying an explicit
// "budget reached — wrap up" hint. The IDs are preserved so the model's
// next-turn expectation (each function-call must be paired with a response)
// is satisfied, avoiding a malformed conversation history.
func (e *Executor) buildKimiToolBudgetExhaustedResults(calls []*genai.FunctionCall) []*genai.FunctionResponse {
	results := make([]*genai.FunctionResponse, len(calls))
	msg := buildKimiToolBudgetMessage(e.kimiToolBudget)
	for i, call := range calls {
		toolName := "tool"
		callID := ""
		if call != nil {
			toolName = call.Name
			callID = call.ID
		}
		results[i] = &genai.FunctionResponse{
			ID:       callID,
			Name:     toolName,
			Response: NewErrorResult(msg).ToMap(),
		}
	}
	return results
}

func buildKimiToolBudgetMessage(budget int) string {
	return fmt.Sprintf(
		"Tool budget guard: this turn already issued %d tool calls — the configured per-turn Kimi cap. Stop calling tools and write the final answer now, citing what you've already found. Do not re-request the same tools.",
		budget,
	)
}

// synthesisNudgeThreshold is the tool-call count at which the nudge
// fires. Three tools is the sweet spot: enough signal to consolidate
// (Read + grep + another read, or two greps + a read), not so many
// that the drift is already irreversible.
const synthesisNudgeThreshold = 3

// shouldInjectSynthesisNudge gates the per-turn synthesis reminder.
// Only families where empirical drift-after-3-tool-calls was observed —
// kimi and deepseek. GLM-5/Opus-class models self-consolidate and a
// reminder regresses their behaviour; we don't nudge them. Threshold
// comparison uses >= so the nudge lands immediately after the 3rd tool
// result, before the model picks a 4th call.
func shouldInjectSynthesisNudge(model string, totalToolsThisTurn int) bool {
	if totalToolsThisTurn < synthesisNudgeThreshold {
		return false
	}
	return isNudgeEligibleFamily(model)
}

// isNudgeEligibleFamily reports whether the given model belongs to a
// family that benefits from gokin's explicit runtime nudges (synthesis
// checkpoint, intent announcement, error-recovery hints). The list
// grows as we characterise new providers: adding a model family here
// must come with evidence of the specific drift the nudge prevents.
func isNudgeEligibleFamily(model string) bool {
	family := client.GetModelProfile(model).Family
	return family == "kimi" || family == "deepseek"
}

func buildSynthesisNudgeMessage(toolsCount int) string {
	return fmt.Sprintf(
		"Consolidation checkpoint: you've issued %d tool calls this turn without writing a final answer. Before any more tools, pause and write 2-3 concise lines: (1) Established — concrete facts you've verified, (2) Unknown — open questions the tools couldn't answer, (3) Next — the single next step. If Established already covers the user's request, STOP and write the final answer instead.",
		toolsCount,
	)
}

// intentNudgeMinPlanChars is the threshold for considering the
// assistant's pre-tool text "enough of a plan". 30 chars is one short
// sentence — low bar, but anything below it is almost certainly empty
// or a filler "Ok." / "Sure.". Tuned low on purpose so well-behaved
// models don't trip the nudge.
const intentNudgeMinPlanChars = 30

// intentNudgeMessage is the system reminder queued when the model skips
// the plan phase. Wording mirrors the Kimi operating-rules addendum so
// the model sees the same vocabulary in-prompt and in-runtime.
const intentNudgeMessage = "Plan-then-act reminder: your last response jumped to tool calls without a one-line plan. Before your next set of tool calls, write a single line starting with 'Plan:' that states the concrete objective (3-7 words), so the user can tell what you're about to do. Skip the plan only for a single trivial read/grep with no follow-up."

// shouldInjectIntentNudge decides whether to queue the announcement
// reminder. Fires only when:
//   - no tools have been executed yet in this turn (toolsExecuted == 0)
//     — covers the "first real response" case even when outer-loop
//     retries inflated the iteration counter
//   - the model produced tool calls (otherwise there's nothing to plan)
//   - the response text before the tool calls is under the threshold
//     (≈ no plan written)
//   - the model family is Kimi (Strong-tier models self-narrate)
func shouldInjectIntentNudge(toolsExecuted int, model string, resp *client.Response) bool {
	if toolsExecuted > 0 || resp == nil {
		return false
	}
	if len(resp.FunctionCalls) == 0 {
		return false
	}
	if !isNudgeEligibleFamily(model) {
		return false
	}
	// Count both the visible text and the thinking trace — thinking
	// counts as "plan visible to runtime" even when it doesn't show in
	// UI, since the model itself processed it.
	planLen := len(strings.TrimSpace(resp.Text)) + len(strings.TrimSpace(resp.Thinking))
	return planLen < intentNudgeMinPlanChars
}

func (e *Executor) buildStagnationRecoveryResults(calls []*genai.FunctionCall, repeatCount int) []*genai.FunctionResponse {
	results := make([]*genai.FunctionResponse, len(calls))
	for i, call := range calls {
		toolName := "tool"
		callID := ""
		args := map[string]any{}
		if call != nil {
			toolName = call.Name
			callID = call.ID
			if call.Args != nil {
				args = call.Args
			}
		}

		msg := buildStagnationRecoveryMessage(toolName, args, repeatCount)
		result := NewErrorResult(msg)
		if cached, ok := e.lookupCachedReadOnlyResult(toolName, args); ok && cached.Content != "" {
			result = NewErrorResultWithContext(msg, cached.Content)
		}

		results[i] = &genai.FunctionResponse{
			ID:       callID,
			Name:     toolName,
			Response: result.ToMap(),
		}
	}
	return results
}

func (e *Executor) lookupCachedReadOnlyResult(toolName string, args map[string]any) (ToolResult, bool) {
	if e.toolCache == nil || toolName == "" {
		return ToolResult{}, false
	}
	return e.toolCache.Get(toolName, args)
}

func buildStagnationRecoveryMessage(toolName string, args map[string]any, repeatCount int) string {
	return fmt.Sprintf(
		"Loop guard: identical %s request repeated %d times. This exact tool call already ran and repeating it will not make progress. Do not call it again. Reuse the earlier result, choose a different file/range, or answer the user.",
		describeStagnationTarget(toolName, args),
		repeatCount,
	)
}

func buildStagnationWarningMessage(calls []*genai.FunctionCall, repeatCount int) string {
	if len(calls) == 0 || calls[0] == nil {
		return fmt.Sprintf(
			"Kimi loop guard: repeated the same exploration tool pattern %d times. Sent a recovery hint instead of rerunning it.",
			repeatCount,
		)
	}
	if len(calls) == 1 {
		return fmt.Sprintf(
			"Kimi loop guard: repeated %s %d times. Sent a recovery hint instead of rerunning it.",
			describeStagnationTarget(calls[0].Name, calls[0].Args),
			repeatCount,
		)
	}
	return fmt.Sprintf(
		"Kimi loop guard: repeated the same %s exploration pattern %d times. Sent a recovery hint instead of rerunning it.",
		describeStagnationTarget(calls[0].Name, calls[0].Args),
		repeatCount,
	)
}

func isKimiStagnationRecoveryTool(toolName string) bool {
	switch toolName {
	case "read", "grep", "glob", "list_dir", "tree":
		return true
	default:
		return false
	}
}

func describeStagnationTarget(toolName string, args map[string]any) string {
	switch toolName {
	case "read":
		filePath, _ := args["file_path"].(string)
		target := "read"
		if filePath != "" {
			target += " " + filepath.Base(filePath)
		}
		offset := readIntArg(args, "offset")
		limit := readIntArg(args, "limit")
		if offset > 0 || limit > 0 {
			target = fmt.Sprintf("%s (offset %d, limit %d)", target, offset, limit)
		}
		return target
	case "grep":
		pattern, _ := args["pattern"].(string)
		target := "grep"
		if pattern != "" {
			target += fmt.Sprintf(" %q", pattern)
		}
		if path, ok := args["path"].(string); ok && path != "" {
			target += " in " + path
		} else if glob, ok := args["glob"].(string); ok && glob != "" {
			target += " matching " + glob
		}
		return target
	case "glob":
		pattern, _ := args["pattern"].(string)
		target := "glob"
		if pattern != "" {
			target += fmt.Sprintf(" %q", pattern)
		}
		if path, ok := args["path"].(string); ok && path != "" {
			target += " in " + path
		}
		return target
	case "list_dir", "tree":
		path, _ := args["path"].(string)
		if path == "" {
			return toolName
		}
		return fmt.Sprintf("%s %s", toolName, path)
	default:
		if fp := stagnationFingerprint(toolName, args); fp != "" {
			return fmt.Sprintf("%s (%s)", toolName, fp)
		}
		return toolName
	}
}

func (e *Executor) buildModelWorkingMemoryNotification(model string, calls []*genai.FunctionCall, results []*genai.FunctionResponse) string {
	if !shouldInjectKimiWorkingMemory(model, calls, results) {
		return ""
	}

	limit := min(len(calls), len(results))
	if limit == 0 {
		return ""
	}

	items := make([]string, 0, limit)
	for i := 0; i < limit && len(items) < 3; i++ {
		call := calls[i]
		result := results[i]
		if call == nil || result == nil {
			continue
		}

		target := describeStagnationTarget(call.Name, call.Args)
		summary := e.summarizeWorkingMemoryResult(call.Name, result)
		if target == "" || summary == "" {
			continue
		}
		items = append(items, trimInlineText(fmt.Sprintf("%s -> %s", target, summary), 240))
	}
	if len(items) == 0 {
		return ""
	}

	return "[Kimi working memory] Established: " +
		strings.Join(items, "; ") +
		". Before more tools: reuse these results, do not repeat the same read/grep/glob without a new hypothesis, prefer grep -> targeted read, and after 1-3 tool calls synthesize what is established and what remains unknown."
}

func shouldInjectKimiWorkingMemory(model string, calls []*genai.FunctionCall, results []*genai.FunctionResponse) bool {
	// Retained name for stability of log markers, but now eligibility
	// extends to any nudge-eligible family — DeepSeek benefits from the
	// same "don't re-explore, synthesise" reminder as Kimi.
	if !isNudgeEligibleFamily(model) {
		return false
	}
	if len(calls) == 0 || len(results) == 0 {
		return false
	}
	if len(calls) > 1 {
		return true
	}
	call := calls[0]
	if call == nil {
		return false
	}
	if isKimiExplorationTool(call.Name) {
		return true
	}
	return workingMemoryResponseSize(results[0]) >= 700
}

func buildKimiToolErrorRecoveryNotification(model string, results []*genai.FunctionResponse) string {
	// Name retained for log marker stability; eligibility now includes
	// deepseek so V4 users get the same concrete recovery steps Kimi
	// users do when read-before-edit / delta-check / ambiguity errors
	// fire. The message text is generic — not family-specific.
	if !isNudgeEligibleFamily(model) {
		return ""
	}
	for _, result := range results {
		if result == nil {
			continue
		}
		success, _ := result.Response["success"].(bool)
		if success {
			continue
		}
		errMsg, _ := result.Response["error"].(string)
		lower := strings.ToLower(strings.TrimSpace(errMsg))
		if lower == "" {
			continue
		}
		switch {
		case strings.Contains(lower, "read-before-edit"):
			return "[Kimi recovery] read-before-edit blocked the mutation. Next call must be read(file_path=...) for the exact file, then retry one edit using exact surrounding context. Do not issue another mutation first."
		case strings.Contains(lower, "delta-check guard") || strings.Contains(lower, "delta-check failed"):
			return "[Kimi recovery] delta-check failed after a mutation. Fix the reported build/typecheck/lint error before any unrelated edit, then rerun the same verification."
		case strings.Contains(lower, "old_string not found") ||
			(strings.Contains(lower, "ambiguous") && strings.Contains(lower, "old_string")) ||
			strings.Contains(lower, "fuzzy match"):
			return "[Kimi recovery] edit matching failed. Re-read the target region if needed, then retry with 3-5 exact surrounding lines in old_string or use line_start/line_end when the tool suggested it."
		}
	}
	return ""
}

func appendNotificationToFunctionResults(results []*genai.FunctionResponse, notification string) {
	notification = strings.TrimSpace(notification)
	if len(results) == 0 || notification == "" {
		return
	}

	result := results[len(results)-1]
	if result == nil {
		return
	}
	if result.Response == nil {
		result.Response = map[string]any{}
	}

	if errMsg, ok := result.Response["error"].(string); ok && strings.TrimSpace(errMsg) != "" {
		result.Response["error"] = appendTextBlock(errMsg, notification)
		return
	}
	if content, ok := result.Response["content"].(string); ok && strings.TrimSpace(content) != "" {
		result.Response["content"] = appendTextBlock(notification, content)
		return
	}
	result.Response["content"] = notification
}

func appendTextBlock(base, addition string) string {
	base = strings.TrimRight(base, "\n")
	addition = strings.TrimSpace(addition)
	if base == "" {
		return addition
	}
	if addition == "" {
		return base
	}
	return base + "\n\n" + addition
}

func isKimiExplorationTool(toolName string) bool {
	switch toolName {
	case "read", "grep", "glob", "list_dir", "tree":
		return true
	default:
		return false
	}
}

func workingMemoryResponseSize(result *genai.FunctionResponse) int {
	if result == nil {
		return 0
	}
	content, _ := result.Response["content"].(string)
	errMsg, _ := result.Response["error"].(string)
	return len(content) + len(errMsg)
}

func (e *Executor) summarizeWorkingMemoryResult(toolName string, result *genai.FunctionResponse) string {
	if result == nil {
		return ""
	}

	errMsg, _ := result.Response["error"].(string)
	if trimmedErr := strings.TrimSpace(errMsg); trimmedErr != "" {
		return trimInlineText("error: "+trimmedErr, 180)
	}

	content, _ := result.Response["content"].(string)
	if trimmedContent := strings.TrimSpace(content); trimmedContent != "" {
		if summarizer, ok := e.compactor.(resultSummaryProvider); ok {
			if summary := strings.TrimSpace(summarizer.SummarizeForPrune(toolName, trimmedContent)); summary != "" {
				return trimInlineText(summary, 180)
			}
		}
		return trimInlineText(trimmedContent, 180)
	}

	success, _ := result.Response["success"].(bool)
	if success {
		return "success"
	}
	return "no output"
}

func trimInlineText(text string, max int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	runes := []rune(text)
	if max <= 0 || len(runes) <= max {
		return text
	}
	if max == 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}

// adaptiveToolTimeout returns the effective timeout for a single tool call
// given the executor's baseline and observed p95 latency.
//
// Semantics:
//   - If no stats (ok=false) or non-positive p95: return base unchanged.
//   - Otherwise take max(base, 5×p95) so fast tools still fail fast while
//     historically-slow tools (e.g., a 5-minute build step) get room.
//   - Cap the result at 2×base so a genuinely hung tool can't run unbounded
//     even when its historical p95 was huge.
func adaptiveToolTimeout(base, p95 time.Duration, ok bool) time.Duration {
	if !ok || p95 <= 0 {
		return base
	}
	timeout := base
	if adaptive := p95 * 5; adaptive > timeout {
		timeout = adaptive
	}
	if ceiling := base * 2; timeout > ceiling {
		timeout = ceiling
	}
	return timeout
}
