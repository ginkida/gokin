package permission

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"gokin/internal/cache"
)

// PromptHandler is a function that prompts the user for permission.
// It receives a request and returns the user's decision.
type PromptHandler func(ctx context.Context, req *Request) (Decision, error)

// Manager handles permission checks and session caching.
type Manager struct {
	rules   *Rules
	enabled bool

	// Session cache for "allow for session" and "deny for session" decisions
	sessionCache *cache.LRUCache[string, Decision]

	// Auto-approved tool types: after user approves a caution-level tool once,
	// subsequent uses of the same tool type are auto-approved for the session
	autoApprovedTools map[string]bool

	// Prompt handler for asking the user
	promptHandler PromptHandler

	mu sync.RWMutex
}

// NewManager creates a new permission manager.
func NewManager(rules *Rules, enabled bool) *Manager {
	if rules == nil {
		rules = DefaultRules()
	}

	return &Manager{
		rules:             rules,
		enabled:           enabled,
		sessionCache:      cache.NewLRUCache[string, Decision](1000, 24*time.Hour),
		autoApprovedTools: make(map[string]bool),
	}
}

// SetPromptHandler sets the function to call when user input is needed.
func (m *Manager) SetPromptHandler(handler PromptHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.promptHandler = handler
}

// cacheKey generates a cache key for a tool invocation.
// For sensitive tools, the key includes a hash of relevant arguments.
func (m *Manager) cacheKey(toolName string, args map[string]any) string {
	switch toolName {
	case "bash":
		// Include command hash to differentiate different bash commands
		if cmd, ok := args["command"].(string); ok {
			hash := sha256.Sum256([]byte(cmd))
			return fmt.Sprintf("%s:%x", toolName, hash[:8])
		}
	case "write", "edit":
		// Include file path to differentiate different file operations
		if path, ok := args["path"].(string); ok {
			return fmt.Sprintf("%s:%s", toolName, path)
		}
		if path, ok := args["file_path"].(string); ok {
			return fmt.Sprintf("%s:%s", toolName, path)
		}
	case "mcp_admin":
		// Include the action so approving a harmless "list"/"status" call
		// doesn't cache-hit for a later "add"/"remove" call — mcp_admin's
		// risk varies wildly by action (list is a read; add spawns an
		// arbitrary local subprocess for stdio transport). Without this the
		// tool-name-only key would let one innocuous approval silently
		// pre-authorize a dangerous action under the SAME session-trust entry.
		action := "list"
		if a, ok := args["action"].(string); ok && a != "" {
			action = a
		}
		if action == "add" || action == "remove" {
			// Also hash the server config itself. Differentiating by action
			// alone isn't enough: a "DecisionAllowSession" ("Always allow")
			// on ONE add call would otherwise cache-hit for EVERY future add
			// call regardless of what's being spawned — the user consciously
			// approved one specific server, not "any add, forever". Hashing
			// the spawn-defining fields (same discipline bash uses for its
			// command hash) makes the per-key trust match only an IDENTICAL
			// future call for that exact server; a different server still
			// misses the cache and falls through to mustConfirmDespiteTrust.
			payload := fmt.Sprintf("%s|%s|%s|%s|%v",
				stringArg(args, "server"), stringArg(args, "transport"),
				stringArg(args, "command"), stringArg(args, "url"), args["args"])
			hash := sha256.Sum256([]byte(payload))
			return fmt.Sprintf("%s:%s:%x", toolName, action, hash[:8])
		}
		return fmt.Sprintf("%s:%s", toolName, action)
	}
	return toolName
}

// stringArg reads a string-typed arg, returning "" for any missing/wrong-typed
// key. Local helper (not tools.GetStringDefault) — the tools package imports
// permission, so a reverse import would cycle.
func stringArg(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

// Check checks if a tool is allowed to execute.
// Returns a Response indicating whether execution is allowed.
func (m *Manager) Check(ctx context.Context, toolName string, args map[string]any) (*Response, error) {
	// Snapshot config fields under lock to avoid races with SetEnabled/SetRules
	m.mu.RLock()
	enabled := m.enabled
	rules := m.rules
	m.mu.RUnlock()

	// If permissions are disabled, allow everything
	if !enabled {
		return &Response{Allowed: true, Decision: DecisionAllow}, nil
	}

	// Generate cache key that may include args for sensitive tools
	key := m.cacheKey(toolName, args)

	// Check session cache first
	if decision, ok := m.sessionCache.Get(key); ok {
		switch decision {
		case DecisionAllowSession:
			return &Response{Allowed: true, Decision: decision}, nil
		case DecisionDenySession:
			return &Response{
				Allowed:  false,
				Decision: decision,
				Reason:   "Denied for session",
			}, nil
		}
	}

	// Get policy from rules
	policy := rules.GetPolicy(toolName)

	switch policy {
	case LevelAllow:
		// Action-semantics floor: even when bash is configured to allow, an
		// irreversible / outward-facing / privilege-escalating command
		// (force-push, git reset --hard, sudo, curl|sh, recursive delete) must
		// be consciously confirmed. The name-based policy can't see this — it
		// only knows "bash". A previously session-approved identical command
		// already short-circuited via the cache above, so this never re-prompts
		// a command the user just OK'd.
		if isElevatedBash(toolName, args) {
			return m.askUser(ctx, toolName, args)
		}
		return &Response{Allowed: true, Decision: DecisionAllow}, nil

	case LevelDeny:
		return &Response{
			Allowed:  false,
			Decision: DecisionDeny,
			Reason:   "Tool is not permitted by configuration",
		}, nil

	case LevelAsk:
		// Auto-approve a tool already trusted this session (the user approved it
		// once → don't re-prompt for every subsequent call). Applies to ALL risk
		// levels — previously RiskMedium-only, which left bash asking on EVERY
		// distinct command (the main source of prompt fatigue). Full Lock to
		// avoid a TOCTOU race where two concurrent calls both see autoApproved
		// false and both prompt.
		m.mu.Lock()
		autoApproved := m.autoApprovedTools[toolName]
		m.mu.Unlock()
		if autoApproved {
			// Session trust never blanket-runs the floor classes: an elevated
			// bash command (rm -rf / sudo / force-push) or ANY ssh (outward-
			// facing to any host) still re-confirms even when the tool is trusted.
			if mustConfirmDespiteTrust(toolName, args) {
				return m.askUser(ctx, toolName, args)
			}
			return &Response{Allowed: true, Decision: DecisionAllowSession}, nil
		}
		return m.askUser(ctx, toolName, args)
	}

	// Default to asking
	return m.askUser(ctx, toolName, args)
}

// isElevatedBash reports whether a call is a bash command that must be
// consciously confirmed EVEN when bash is otherwise allowed or trusted for the
// session — the action-semantics floor (force-push, git reset --hard, sudo,
// curl|sh, recursive delete). The name-based policy + per-tool session trust
// can't see this; only the command text can. Shared by the LevelAllow path and
// the session-trust short-circuit so the floor can't drift between them.
//
// Best-effort by nature: it can't catch every obfuscation. It is NOT the only
// defense — the truly catastrophic class (rm -rf /, fork bombs, mkfs, reverse
// shells) is hard-blocked upstream regardless of permissions (bash.go
// security.ValidateCommand + the executor pre-flight). This floor adds a
// conscious-confirm step for the irreversible/privileged-but-not-catastrophic
// class so per-tool session trust never blanket-runs it.
func isElevatedBash(toolName string, args map[string]any) bool {
	if toolName != "bash" {
		return false
	}
	danger, _ := ClassifyBashArgs(args)
	return danger == BashDangerElevated
}

// mustConfirmDespiteTrust reports whether a call must STILL prompt even when its
// tool is trusted for the session. ssh is always outward-facing/irreversible to
// ANY host, so it never gets blanket session-trust (per-command "Always allow"
// via the cache is still honored — that's a conscious per-command choice). bash
// is gated only when the command itself is elevated. mcp_admin's spawn-capable
// actions are gated the same way bash's elevated commands are — see
// isMCPAdminSpawnAction.
func mustConfirmDespiteTrust(toolName string, args map[string]any) bool {
	if toolName == "ssh" {
		return true
	}
	if toolName == "mcp_admin" {
		return isMCPAdminSpawnAction(args)
	}
	return isElevatedBash(toolName, args)
}

// isMCPAdminSpawnAction reports whether an mcp_admin call registers/spawns a
// server (action "add") or removes one (action "remove") — as opposed to a
// harmless read-only action ("list"/"status"/"resources"/"prompts"). Without
// this floor, cacheKey's per-action differentiation (see cacheKey) is not
// enough on its own to stop an escalation: approving "add" once for a
// TRUSTED, previously-vetted server would otherwise blanket-trust EVERY
// future "add" call, including one with attacker-controlled command/args
// (e.g. a stdio transport spawning an arbitrary local subprocess) that the
// user never saw. So "add"/"remove" always re-confirm, exactly like ssh —
// there is no "Always allow" fast path for spawning a NEW server, only for
// re-approving calls with the identical action+cache key (list/status).
func isMCPAdminSpawnAction(args map[string]any) bool {
	action, _ := args["action"].(string)
	return action == "add" || action == "remove"
}

// askUser prompts the user for permission.
func (m *Manager) askUser(ctx context.Context, toolName string, args map[string]any) (*Response, error) {
	m.mu.RLock()
	handler := m.promptHandler
	m.mu.RUnlock()

	if handler == nil {
		// No handler set, default to allow (backwards compatibility)
		return &Response{Allowed: true, Decision: DecisionAllow}, nil
	}

	// Create permission request
	req := NewRequest(toolName, args)

	// Ask the user
	decision, err := handler(ctx, req)
	if err != nil {
		return &Response{
			Allowed:  false,
			Decision: DecisionDeny,
			Reason:   err.Error(),
		}, err
	}

	// Generate cache key for session decisions
	key := m.cacheKey(toolName, args)

	// Handle the decision
	switch decision {
	case DecisionAllow:
		// Trust this tool for the rest of the session (ALL risk levels — extends
		// the old RiskMedium-only behavior to bash et al, so one approval stops
		// the per-command re-prompting). Dangerous bash is still gated in Check
		// via isElevatedBash, so trust never auto-runs rm -rf / sudo / force-push.
		m.mu.Lock()
		m.autoApprovedTools[toolName] = true
		m.mu.Unlock()
		return &Response{Allowed: true, Decision: decision}, nil

	case DecisionAllowSession:
		m.rememberKey(key, decision)
		m.mu.Lock()
		m.autoApprovedTools[toolName] = true
		m.mu.Unlock()
		return &Response{Allowed: true, Decision: decision}, nil

	case DecisionDeny:
		return &Response{
			Allowed:  false,
			Decision: decision,
			Reason:   "Denied by user",
		}, nil

	case DecisionDenySession:
		m.rememberKey(key, decision)
		return &Response{
			Allowed:  false,
			Decision: decision,
			Reason:   "Denied for session",
		}, nil

	default:
		return &Response{
			Allowed:  false,
			Decision: DecisionDeny,
			Reason:   "Unknown decision",
		}, nil
	}
}

// rememberKey stores a session-level decision for a cache key.
func (m *Manager) rememberKey(key string, decision Decision) {
	m.sessionCache.Set(key, decision)
}

// RememberWithArgs stores a session-level decision for a tool with args.
func (m *Manager) RememberWithArgs(toolName string, args map[string]any, decision Decision) {
	key := m.cacheKey(toolName, args)
	m.rememberKey(key, decision)
}

// Forget removes a session-level decision for a tool.
func (m *Manager) Forget(toolName string) {
	m.sessionCache.Delete(toolName)
}

// ForgetWithArgs removes a session-level decision for a tool with args.
func (m *Manager) ForgetWithArgs(toolName string, args map[string]any) {
	key := m.cacheKey(toolName, args)
	m.sessionCache.Delete(key)
}

// ClearSession clears all session-level decisions.
func (m *Manager) ClearSession() {
	m.sessionCache.Clear()
	m.mu.Lock()
	m.autoApprovedTools = make(map[string]bool)
	m.mu.Unlock()
}

// IsEnabled returns whether the permission system is enabled.
func (m *Manager) IsEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.enabled
}

// SetEnabled enables or disables the permission system.
func (m *Manager) SetEnabled(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = enabled
}

// GetRules returns the current rules.
func (m *Manager) GetRules() *Rules {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rules
}

// SetRules sets new rules.
func (m *Manager) SetRules(rules *Rules) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rules = rules
}
