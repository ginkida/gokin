package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
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
			return fmt.Sprintf(config.Error, result.Error) + toolContext
		}
		return fmt.Sprintf("Operation encountered an issue: %s\n\nWould you like me to try a different approach?", result.Error) + toolContext
	}

	// Handle empty/default result - provide helpful generic message
	if isEmptyResult {
		if hasConfig {
			return config.Success + toolContext
		}
		return "I've completed the requested operations. The results should be shown above.\n\nIs there anything specific you'd like me to explain or analyze further?" + toolContext
	}

	// Handle empty content results (tool ran but returned nothing)
	if result.Content == "" || result.Content == "(empty file)" || result.Content == "(no output)" || result.Content == "No matches found." {
		if hasConfig {
			return config.Empty + toolContext
		}
		return "The operation completed but returned no results. This may be expected depending on the context." + toolContext
	}

	// Handle success with content
	if hasConfig {
		return config.Success + toolContext
	}

	return "I've completed the operation. The results are shown above. Would you like me to analyze them further?" + toolContext
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
	timeout           time.Duration
	modelRoundTimeout time.Duration
	handler           *ExecutionHandler
	compactor         ResultCompactor
	permissions       *permission.Manager
	hooks             *hooks.Manager
	auditLogger       *audit.Logger
	sessionID         string
	fallback          FallbackConfig
	clientMu sync.RWMutex // Protects client field

	// Enhanced safety features
	safetyValidator  SafetyValidator
	preFlightChecks  bool                      // Enable pre-flight safety checks
	userNotified     bool                      // Track if user was notified about tool execution
	notificationMgr  *NotificationManager      // User notifications
	redactor         *security.SecretRedactor  // Redact secrets from output
	unrestrictedMode bool                      // Full freedom when both sandbox and permissions are off

	// Token usage from last response (from API usage metadata).
	// Protected by tokenMu for concurrent read/write safety.
	tokenMu                 sync.Mutex
	lastInputTokens         int
	lastOutputTokens        int
	lastCacheCreationTokens int
	lastCacheReadTokens     int

	// Circuit breakers for tools
	breakerMu    sync.Mutex
	toolBreakers map[string]*robustness.CircuitBreaker

	// Tool result cache
	toolCache *ToolResultCache

	// Search cache (grep/glob results) for invalidation on writes
	searchCache *cache.SearchCache

	// File read deduplication tracker
	readTracker *FileReadTracker

	// Auto-formatter for write/edit operations
	formatter *Formatter

	// Retry-time side effect deduplication (write/bash tools).
	sideEffectDedupEnabled bool
	sideEffectLedgerMu     sync.Mutex
	sideEffectsByCallID    map[string]ToolResult
	sideEffectsBySignature map[string]ToolResult

	// Pending background task completion notifications
	pendingNotifications []string
	pendingNotifMu       sync.Mutex
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
	OnToolEnd func(name string, result ToolResult)

	// OnToolProgress is called periodically during long-running tool execution.
	// This helps keep the UI alive and prevents timeout during long operations.
	OnToolProgress func(name string, elapsed time.Duration)

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
		sideEffectsByCallID:    make(map[string]ToolResult),
		sideEffectsBySignature: make(map[string]ToolResult),
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
func (e *Executor) SetClient(c client.Client) {
	e.clientMu.Lock()
	e.client = c
	e.clientMu.Unlock()
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

// ResetSideEffectLedger clears deduplication ledger for a new top-level request.
func (e *Executor) ResetSideEffectLedger() {
	e.sideEffectLedgerMu.Lock()
	defer e.sideEffectLedgerMu.Unlock()
	e.sideEffectsByCallID = make(map[string]ToolResult)
	e.sideEffectsBySignature = make(map[string]ToolResult)
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

	// === IMPROVEMENT 3: Dynamic max iterations based on context complexity ===
	maxIterations := e.calculateMaxIterations(history)

	var finalText string
	var toolsUsed []string        // Track which tools were used for smart fallback
	var lastToolResult ToolResult // Track the last tool result for context
	streamRetries := 0
	partialStreamRetries := 0
	retryPolicy := client.DefaultStreamRetryPolicy()
	// Retries are orchestrated at App/message processor level to avoid retry multiplication.
	retryPolicy.MaxRetries = 0
	retryPolicy.MaxPartialRetries = 0

	for i := 0; i < maxIterations; i++ {
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
			// Preserve partial response in history if available
			if resp != nil && resp.Text != "" {
				partialContent := &genai.Content{
					Role:  genai.RoleModel,
					Parts: []*genai.Part{genai.NewPartFromText(resp.Text)},
				}
				history = append(history, partialContent)
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

		// Handle chained function calls in an inner loop
		for len(resp.FunctionCalls) > 0 {
			// Check context cancellation between chained tool calls (Esc pressed)
			select {
			case <-ctx.Done():
				return history, finalText, client.ContextErr(ctx)
			default:
			}

			// Track tools being called
			for _, fc := range resp.FunctionCalls {
				toolsUsed = append(toolsUsed, fc.Name)
			}

			results, err := e.executeTools(ctx, resp.FunctionCalls)
			if err != nil {
				return history, "", fmt.Errorf("tool execution error: %w", err)
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

			// Add function results to history BEFORE sending to model.
			// This ensures tool results are preserved even if the API call fails.
			funcResultParts := make([]*genai.Part, len(results))
			for j, result := range results {
				funcResultParts[j] = genai.NewPartFromFunctionResponse(result.Name, result.Response)
				funcResultParts[j].FunctionResponse.ID = result.ID
			}
			funcResultContent := &genai.Content{
				Role:  genai.RoleUser,
				Parts: funcResultParts,
			}

			historyBeforeResults := len(history)
			history = append(history, funcResultContent)

			// Inject pending background task completion notifications
			if notifications := e.drainPendingNotifications(); len(notifications) > 0 {
				notifText := "[System: " + strings.Join(notifications, "; ") + "]"
				history = append(history, &genai.Content{
					Role:  genai.RoleUser,
					Parts: []*genai.Part{genai.NewPartFromText(notifText)},
				})
			}

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
					// Preserve partial response from chained tool call
					if resp != nil && resp.Text != "" {
						partialContent := &genai.Content{
							Role:  genai.RoleModel,
							Parts: []*genai.Part{genai.NewPartFromText(resp.Text)},
						}
						history = append(history, partialContent)
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

		// Capture token usage metadata from API response
		e.tokenMu.Lock()
		if resp.InputTokens > 0 || resp.OutputTokens > 0 {
			e.lastInputTokens = resp.InputTokens
			e.lastOutputTokens = resp.OutputTokens
		}
		e.lastCacheCreationTokens = resp.CacheCreationInputTokens
		e.lastCacheReadTokens = resp.CacheReadInputTokens
		e.tokenMu.Unlock()

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
	baseLimit := 50

	// Count conversation turns (pairs of user/model messages)
	turnCount := len(history) / 2

	// Estimate complexity based on history length
	switch {
	case turnCount > 30:
		// Very long conversation - allow many iterations
		return baseLimit + 100
	case turnCount > 20:
		// Long conversation - significantly more iterations
		return baseLimit + 50
	case turnCount > 10:
		// Medium conversation - moderately more iterations
		return baseLimit + 25
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
	if e.handler != nil {
		onText = e.handler.OnText
		onThinking = e.handler.OnThinking
	}
	return client.ProcessStream(ctx, stream, &client.StreamHandler{
		OnText:     onText,
		OnThinking: onThinking,
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

	// For single tool, execute directly
	if len(calls) == 1 {
		result := e.executeTool(ctx, calls[0])
		results[0] = &genai.FunctionResponse{
			ID:       calls[0].ID,
			Name:     calls[0].Name,
			Response: result.ToMap(),
		}
		return results, nil
	}

	// Check for file dependencies between parallel calls
	orderedCalls, reordered := orderByDependencies(calls)
	if reordered {
		logging.Debug("executing tool calls sequentially due to file dependencies",
			"count", len(orderedCalls))
		// Execute sequentially so write-before-read ordering is respected
		for i, call := range orderedCalls {
			select {
			case <-ctx.Done():
				results[i] = &genai.FunctionResponse{
					ID:       call.ID,
					Name:     call.Name,
					Response: NewErrorResult("cancelled").ToMap(),
				}
				continue
			default:
			}
			result := e.executeTool(ctx, call)
			results[i] = &genai.FunctionResponse{
				ID:       call.ID,
				Name:     call.Name,
				Response: result.ToMap(),
			}
		}
		return results, nil
	}

	// For multiple tools, execute in parallel with semaphore to limit concurrency
	var wg sync.WaitGroup
	var mu sync.Mutex
	semaphore := make(chan struct{}, MaxConcurrentToolExecutions)

	for i, call := range calls {
		wg.Add(1)
		go func(idx int, fc *genai.FunctionCall) {
			defer wg.Done()

			// Acquire semaphore slot
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }() // Release slot when done
			case <-ctx.Done():
				mu.Lock()
				results[idx] = &genai.FunctionResponse{
					ID:       fc.ID,
					Name:     fc.Name,
					Response: NewErrorResult("cancelled").ToMap(),
				}
				mu.Unlock()
				return
			}

			defer func() {
				if r := recover(); r != nil {
					stack := make([]byte, 4096)
					length := runtime.Stack(stack, false)
					logging.Error("tool execution panic",
						"tool", fc.Name,
						"panic", r,
						"stack", string(stack[:length]))

					mu.Lock()
					results[idx] = &genai.FunctionResponse{
						ID:       fc.ID,
						Name:     fc.Name,
						Response: NewErrorResult(fmt.Sprintf("panic: %v", r)).ToMap(),
					}
					mu.Unlock()
				}
			}()

			select {
			case <-ctx.Done():
				mu.Lock()
				results[idx] = &genai.FunctionResponse{
					ID:       fc.ID,
					Name:     fc.Name,
					Response: NewErrorResult("cancelled").ToMap(),
				}
				mu.Unlock()
				return
			default:
			}

			result := e.executeTool(ctx, fc)

			mu.Lock()
			results[idx] = &genai.FunctionResponse{
				ID:       fc.ID,
				Name:     fc.Name,
				Response: result.ToMap(),
			}
			mu.Unlock()
		}(i, call)
	}

	wg.Wait()
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
		if !res.Success {
			return fmt.Errorf("tool execution failed: %s", res.Error)
		}
		return nil
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

// doExecuteTool handles the actual execution logic (previously body of executeTool).
func (e *Executor) doExecuteTool(ctx context.Context, call *genai.FunctionCall) ToolResult {
	// Step 1: Basic tool lookup and validation
	tool, ok := e.registry.Get(call.Name)
	if !ok {
		return NewErrorResult(fmt.Sprintf("unknown tool: %s", call.Name))
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

	// Step 8: Execute with timeout
	execCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

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
	var result ToolResult
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
			errMsg := fmt.Sprintf("tool '%s' timed out after %v (limit: %v)", call.Name, duration.Round(time.Millisecond), e.timeout)
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
		// Invalidate read tracker on write operations
		if isWriteOperation(call.Name) {
			if path, ok := call.Args["path"].(string); ok {
				e.readTracker.InvalidateFile(path)
			}
			if path, ok := call.Args["file_path"].(string); ok {
				e.readTracker.InvalidateFile(path)
			}
		}
	}

	// Step 13: Notify completion and send notifications
	if e.handler != nil && e.handler.OnToolEnd != nil {
		e.handler.OnToolEnd(call.Name, result)
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
func (e *Executor) buildResponseParts(resp *client.Response) []*genai.Part {
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

	if len(parts) == 0 {
		parts = append(parts, genai.NewPartFromText(" "))
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
			break streamLoop

		case <-ctx.Done():
			// Drain chunks in background to unblock the producer goroutine.
			// The producer defers complete() which closes Chunks, so this goroutine will exit.
			go func() {
				for range stream.Chunks {
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
