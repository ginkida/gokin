package permission

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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
	rules    *Rules
	enabled  bool
	revision uint64

	// parent/overrides turn this manager into a bounded invocation capability
	// (approved plan step). The parent remains the revocation authority: policy
	// changes and session-deny decisions are observed dynamically, while parent
	// session-allow decisions are deliberately not inherited.
	parent         *Manager
	overrides      map[string]Level
	parentRevision uint64

	// Session cache for "allow for session" and "deny for session" decisions
	sessionCache *cache.LRUCache[string, cachedDecision]

	// Prompt handler for asking the user
	promptHandler PromptHandler

	mu sync.RWMutex
}

// cachedDecision binds reusable authority to the policy revision under which
// it was granted. A config change can therefore never revive stale approval.
type cachedDecision struct {
	Decision Decision
	Revision uint64
}

// NewManager creates a new permission manager.
func NewManager(rules *Rules, enabled bool) *Manager {
	if rules == nil {
		rules = DefaultRules()
	}

	return &Manager{
		rules:        cloneRules(rules),
		enabled:      enabled,
		revision:     1,
		sessionCache: cache.NewLRUCache[string, cachedDecision](1000, 24*time.Hour),
	}
}

// SetPromptHandler sets the function to call when user input is needed.
func (m *Manager) SetPromptHandler(handler PromptHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.promptHandler = handler
}

// WithPolicyOverrides creates an invocation-local permission scope. It keeps
// the effective enabled state and prompt handler, defensively copies rules,
// applies only the supplied overrides, and deliberately does not inherit
// session decisions. Overrides are monotonic: an explicit/effective deny is a
// hard ceiling and can never be weakened by an invocation-local capability.
// This is suitable for bounded capabilities such as one approved plan step
// without turning approval into global session authority.
func (m *Manager) WithPolicyOverrides(overrides map[string]Level) *Manager {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	rules := cloneRules(m.rules)
	enabled := m.enabled
	handler := m.promptHandler
	parentRevision := m.revision
	m.mu.RUnlock()
	if rules == nil {
		rules = DefaultRules()
	}
	for tool, level := range overrides {
		if rules.GetPolicy(tool) == LevelDeny {
			continue
		}
		rules.SetPolicy(tool, level)
	}
	scoped := NewManager(rules, enabled)
	scoped.parent = m
	scoped.overrides = clonePolicyOverrides(overrides)
	scoped.parentRevision = parentRevision
	scoped.SetPromptHandler(handler)
	return scoped
}

func clonePolicyOverrides(overrides map[string]Level) map[string]Level {
	if len(overrides) == 0 {
		return nil
	}
	clone := make(map[string]Level, len(overrides))
	for tool, level := range overrides {
		clone[tool] = level
	}
	return clone
}

// refreshFromParent makes an invocation-local manager follow the live parent
// policy without inheriting ambient session allows. It returns a hard/session
// deny when the parent has revoked this exact invocation.
func (m *Manager) refreshFromParent(toolName string, args map[string]any) *Response {
	if m == nil || m.parent == nil {
		return nil
	}
	parent := m.parent
	parent.mu.RLock()
	rules := cloneRules(parent.rules)
	enabled := parent.enabled
	handler := parent.promptHandler
	parentRevision := parent.revision
	parent.mu.RUnlock()
	if rules == nil {
		rules = DefaultRules()
	}

	// Synchronize effective rules/handler first. Any local reusable decision is
	// revision-bound and therefore becomes unusable as soon as parent policy
	// changes; clearing reclaims it eagerly.
	m.mu.Lock()
	changed := m.parentRevision != parentRevision || m.enabled != enabled
	if changed {
		effective := cloneRules(rules)
		for tool, level := range m.overrides {
			if effective.GetPolicy(tool) != LevelDeny {
				effective.SetPolicy(tool, level)
			}
		}
		m.rules = effective
		m.enabled = enabled
		m.parentRevision = parentRevision
		m.revision++
	}
	m.promptHandler = handler
	m.mu.Unlock()
	if changed {
		m.sessionCache.Clear()
	}

	if !enabled {
		return nil
	}
	if rules.GetPolicy(toolName) == LevelDeny {
		return &Response{Allowed: false, Decision: DecisionDeny, Reason: "Tool is not permitted by parent configuration"}
	}

	// A parent session denial is a revocation floor. Parent session ALLOW is not
	// consulted: plan-step capabilities and their approvals stay invocation-local.
	key := parent.cacheKey(toolName, args)
	if cached, ok := parent.sessionCache.Get(key); ok && cached.Revision == parentRevision && cached.Decision == DecisionDenySession {
		return &Response{Allowed: false, Decision: DecisionDenySession, Reason: "Denied for parent session"}
	}

	return nil
}

// cacheKey generates a cache key for a tool invocation.
// For sensitive tools, the key includes a hash of relevant arguments.
func (m *Manager) cacheKey(toolName string, args map[string]any) string {
	switch toolName {
	case "write", "edit":
		// These tools execute file_path; ignore unrelated extra fields so they
		// cannot redirect the cache key away from the path being authorized.
		if path, ok := args["file_path"].(string); ok {
			return fmt.Sprintf("%s:%s", toolName, path)
		}
	}
	// Bash (including its injected cwd/env execution scope), MCP, unknown and
	// third-party tools may have argument-dependent effects. Scope reusable
	// decisions to the complete canonical invocation.
	if len(args) > 0 {
		payload, err := json.Marshal(args) // encoding/json sorts string map keys.
		if err != nil {
			payload = []byte(fmt.Sprintf("%#v", args))
		}
		hash := sha256.Sum256(payload)
		return fmt.Sprintf("%s:%x", toolName, hash[:8])
	}
	return toolName
}

// Check checks if a tool is allowed to execute.
// Returns a Response indicating whether execution is allowed.
func (m *Manager) Check(ctx context.Context, toolName string, args map[string]any) (*Response, error) {
	if denied := m.refreshFromParent(toolName, args); denied != nil {
		return denied, nil
	}
	// Snapshot config fields under lock to avoid races with SetEnabled/SetRules
	m.mu.RLock()
	enabled := m.enabled
	rules := m.rules
	revision := m.revision
	m.mu.RUnlock()

	// If permissions are disabled, allow everything
	if !enabled {
		return &Response{Allowed: true, Decision: DecisionAllow}, nil
	}

	// Get policy from rules
	policy := rules.GetPolicy(toolName)
	// Explicit deny is the hard policy boundary and always wins over a cached
	// decision made under an earlier/more-permissive configuration.
	if policy == LevelDeny {
		return &Response{
			Allowed:  false,
			Decision: DecisionDeny,
			Reason:   "Tool is not permitted by configuration",
		}, nil
	}

	// A normal LevelAllow is authoritative. Elevated bash commands are the one
	// exception: an identical, explicitly session-approved call may satisfy the
	// action-semantics confirmation floor below.
	if policy == LevelAllow && !isElevatedBash(toolName, args) {
		return &Response{Allowed: true, Decision: DecisionAllow}, nil
	}

	// Reusable decisions are scoped to both the normalized invocation and the
	// current policy revision. DecisionAllow never enters this cache.
	key := m.cacheKey(toolName, args)
	if cached, ok := m.sessionCache.Get(key); ok && cached.Revision == revision {
		switch cached.Decision {
		case DecisionAllowSession:
			return &Response{Allowed: true, Decision: cached.Decision}, nil
		case DecisionDenySession:
			return &Response{
				Allowed:  false,
				Decision: cached.Decision,
				Reason:   "Denied for session",
			}, nil
		}
	}

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
			return m.askUser(ctx, toolName, args, revision)
		}
		return &Response{Allowed: true, Decision: DecisionAllow}, nil

	case LevelAsk:
		return m.askUser(ctx, toolName, args, revision)
	}

	// Default to asking
	return m.askUser(ctx, toolName, args, revision)
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

// askUser prompts the user for permission.
func (m *Manager) askUser(ctx context.Context, toolName string, args map[string]any, revision uint64) (*Response, error) {
	m.mu.RLock()
	handler := m.promptHandler
	m.mu.RUnlock()

	if handler == nil {
		err := fmt.Errorf("permission required for %s, but no interactive approval handler is available; configure an explicit allow rule or disable permissions for intentional unattended execution", toolName)
		return &Response{Allowed: false, Decision: DecisionDeny, Reason: err.Error()}, err
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

	// A decision made against stale rules is not authority under the new rules.
	// In particular, a concurrent ask→deny update must beat a late Allow click.
	m.mu.RLock()
	currentRevision := m.revision
	m.mu.RUnlock()
	if currentRevision != revision {
		err := fmt.Errorf("permission policy changed while approval for %s was pending; retry the operation under the current policy", toolName)
		return &Response{Allowed: false, Decision: DecisionDeny, Reason: err.Error()}, err
	}

	// Generate cache key for session decisions
	key := m.cacheKey(toolName, args)

	// Handle the decision
	switch decision {
	case DecisionAllow:
		// "Allow" is deliberately one-shot. Only DecisionAllowSession creates
		// reusable authority.
		return &Response{Allowed: true, Decision: decision}, nil

	case DecisionAllowSession:
		if !m.rememberKeyIfCurrent(key, decision, revision) {
			err := fmt.Errorf("permission policy or session authority changed while approval for %s was being committed; retry the operation under the current policy", toolName)
			return &Response{Allowed: false, Decision: DecisionDeny, Reason: err.Error()}, err
		}
		return &Response{Allowed: true, Decision: decision}, nil

	case DecisionDeny:
		return &Response{
			Allowed:  false,
			Decision: decision,
			Reason:   "Denied by user",
		}, nil

	case DecisionDenySession:
		if !m.rememberKeyIfCurrent(key, decision, revision) {
			err := fmt.Errorf("permission policy or session authority changed while denial for %s was being committed; retry the operation under the current policy", toolName)
			return &Response{Allowed: false, Decision: DecisionDeny, Reason: err.Error()}, err
		}
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

// rememberKeyIfCurrent stores reusable authority only while the policy/session
// generation under which it was approved is still current. The manager lock is
// intentionally held across the cache write so ClearSession/Forget cannot
// revoke a key and then lose a race to a late approval re-inserting it.
func (m *Manager) rememberKeyIfCurrent(key string, decision Decision, revision uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.revision != revision {
		return false
	}
	m.sessionCache.Set(key, cachedDecision{Decision: decision, Revision: revision})
	return true
}

// RememberWithArgs stores a session-level decision for a tool with args.
func (m *Manager) RememberWithArgs(toolName string, args map[string]any, decision Decision) {
	key := m.cacheKey(toolName, args)
	m.mu.Lock()
	defer m.mu.Unlock()
	revision := m.revision
	m.sessionCache.Set(key, cachedDecision{Decision: decision, Revision: revision})
}

// Forget removes a session-level decision for a tool.
func (m *Manager) Forget(toolName string) {
	// Revocation must also beat an approval that is currently waiting on user
	// input. Bump the generation and clear every reusable grant: retaining
	// unrelated old-revision entries would be misleading because Check must
	// reject them after the generation change anyway.
	m.mu.Lock()
	m.revision++
	m.sessionCache.Clear()
	m.mu.Unlock()
}

// ForgetWithArgs removes a session-level decision for a tool with args.
func (m *Manager) ForgetWithArgs(toolName string, args map[string]any) {
	// See Forget: explicit revocation is a session-authority generation change,
	// not merely an LRU deletion that a late approval can undo.
	m.mu.Lock()
	m.revision++
	m.sessionCache.Clear()
	m.mu.Unlock()
}

// ClearSession clears all session-level decisions.
func (m *Manager) ClearSession() {
	m.mu.Lock()
	m.revision++
	m.sessionCache.Clear()
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
	changed := m.enabled != enabled
	m.enabled = enabled
	if changed {
		m.revision++
	}
	m.mu.Unlock()
	if changed {
		m.sessionCache.Clear()
	}
}

// GetRules returns the current rules.
func (m *Manager) GetRules() *Rules {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneRules(m.rules)
}

// SetRules sets new rules.
func (m *Manager) SetRules(rules *Rules) {
	if rules == nil {
		rules = DefaultRules()
	}
	m.mu.Lock()
	m.rules = cloneRules(rules)
	m.revision++
	m.mu.Unlock()
	m.sessionCache.Clear()
}

func cloneRules(rules *Rules) *Rules {
	if rules == nil {
		return nil
	}
	policies := make(map[string]Level, len(rules.ToolPolicies))
	for tool, policy := range rules.ToolPolicies {
		policies[tool] = policy
	}
	return &Rules{DefaultPolicy: rules.DefaultPolicy, ToolPolicies: policies}
}
