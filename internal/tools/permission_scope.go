package tools

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"sort"
)

type bashSessionSnapshot struct {
	workDir string
	env     map[string]string
}

func (s *BashSession) snapshot() bashSessionSnapshot {
	if s == nil {
		return bashSessionSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	env := make(map[string]string, len(s.env))
	for key, value := range s.env {
		env[key] = value
	}
	return bashSessionSnapshot{workDir: s.workDir, env: env}
}

func newBashSessionFromSnapshot(snapshot bashSessionSnapshot) *BashSession {
	env := make(map[string]string, len(snapshot.env))
	for key, value := range snapshot.env {
		env[key] = value
	}
	return &BashSession{workDir: snapshot.workDir, env: env}
}

func (s *BashSession) replace(snapshot bashSessionSnapshot) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.workDir = snapshot.workDir
	s.env = make(map[string]string, len(snapshot.env))
	for key, value := range snapshot.env {
		s.env[key] = value
	}
}

// permissionScopeProvider supplies hidden runtime state that materially
// changes an invocation's effect but is not present in model arguments. The
// scope is used only for authorization/cache identity; tools still validate
// and execute the original arguments.
type permissionScopeProvider interface {
	permissionScope() map[string]any
}

// permissionScopedExecutor binds an already-approved runtime snapshot to the
// actual execution. Bash verifies the snapshot after entering its serialized
// execution gate, then runs from an immutable view so concurrent policy toggles
// affect only future invocations.
type permissionScopedExecutor interface {
	executeWithPermissionScope(ctx context.Context, args map[string]any, expected map[string]any) (ToolResult, error)
}

const permissionExecutionScopeArg = "__gokin_execution_scope"

// PermissionArgsForTool returns args enriched with a tool's current execution
// scope. It never mutates the model-owned argument map.
func PermissionArgsForTool(tool Tool, args map[string]any) map[string]any {
	provider, ok := tool.(permissionScopeProvider)
	if !ok {
		return args
	}
	scope := provider.permissionScope()
	if len(scope) == 0 {
		return args
	}

	enriched := make(map[string]any, len(args)+1)
	for key, value := range args {
		enriched[key] = value
	}
	enriched[permissionExecutionScopeArg] = scope
	return enriched
}

// ClonePermissionArgs returns a deep-enough defensive copy for passing across
// the interactive permission-handler boundary. The retained authorization
// snapshot must not be writable through Request.Args: otherwise a handler (or
// future UI normalizer) could accidentally rewrite the hidden Bash scope that
// ExecuteWithPermissionScope later treats as the approved lease.
func ClonePermissionArgs(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	cloned := make(map[string]any, len(args))
	for key, value := range args {
		cloned[key] = clonePermissionValue(value)
	}
	return cloned
}

func clonePermissionValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, nested := range typed {
			cloned[key] = clonePermissionValue(nested)
		}
		return cloned
	case map[string]string:
		cloned := make(map[string]string, len(typed))
		for key, nested := range typed {
			cloned[key] = nested
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for i, nested := range typed {
			cloned[i] = clonePermissionValue(nested)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}

// ExecuteWithPermissionScope executes tool under the exact hidden runtime
// scope that permission checking observed. authorizedArgs must be the value
// previously returned by PermissionArgsForTool. Tools without mutable hidden
// security state use their normal Execute implementation.
func ExecuteWithPermissionScope(ctx context.Context, tool Tool, args, authorizedArgs map[string]any) (ToolResult, error) {
	scoped, ok := tool.(permissionScopedExecutor)
	if !ok || authorizedArgs == nil {
		return tool.Execute(ctx, args)
	}
	if !permissionInvocationArgsEqual(args, authorizedArgs) {
		return NewErrorResult("Permission scope changed before execution: tool arguments no longer match the authorized invocation; retry the call"), nil
	}
	expected, ok := authorizedArgs[permissionExecutionScopeArg].(map[string]any)
	if !ok || len(expected) == 0 {
		return NewErrorResult("Permission scope changed before execution: authorized runtime scope is missing; retry the call"), nil
	}
	return scoped.executeWithPermissionScope(ctx, args, expected)
}

// permissionInvocationArgsEqual closes the second half of the authorization
// TOCTOU: the hidden runtime scope may still match while a caller concurrently
// replaces `command`/`stdin` in the model-owned argument map during a prompt or
// hook. PermissionArgsForTool owns a distinct top-level map, so scalar Bash
// arguments remain an immutable authorization snapshot.
func permissionInvocationArgsEqual(args, authorizedArgs map[string]any) bool {
	if _, reserved := args[permissionExecutionScopeArg]; reserved {
		return false
	}
	if len(authorizedArgs) != len(args)+1 {
		return false
	}
	for key, value := range args {
		authorized, ok := authorizedArgs[key]
		if !ok || !reflect.DeepEqual(value, authorized) {
			return false
		}
	}
	return true
}

// permissionScope makes a reusable bash approval specific to the shell state
// in which the command will run. The same text after `cd` or an env mutation
// is a different authorization decision.
func (t *BashTool) permissionScope() map[string]any {
	if t == nil {
		return nil
	}
	t.policyMu.RLock()
	defer t.policyMu.RUnlock()
	return t.permissionScopeLocked()
}

func (t *BashTool) permissionScopeLocked() map[string]any {
	session := t.executionSessionSnapshotLocked()
	return t.permissionScopeForSessionLocked(session)
}

func (t *BashTool) permissionScopeForSessionLocked(session bashSessionSnapshot) map[string]any {
	return map[string]any{
		"cwd": session.workDir,
		// Environment values can contain credentials. Permission args may be
		// rendered by a UI or retained in an in-memory decision cache, so bind
		// the lease to a deterministic fingerprint instead of exposing values.
		"env_fingerprint":              fingerprintEnvironment(session.env),
		"sandbox_enabled":              t.sandboxEnabled,
		"unrestricted_mode":            t.unrestrictedMode,
		"workspace_boundary_enabled":   t.workspaceBoundaryEnabled,
		"workspace_root":               t.workspaceRoot,
		"managed_workspace_apply_back": t.managedWorkspaceApplyBack,
		"task_manager_configured":      t.taskManager != nil,
		"policy_revision":              t.policyRevision,
	}
}

func fingerprintEnvironment(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	hash := sha256.New()
	var size [8]byte
	writePart := func(value string) {
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(value))
	}
	for _, key := range keys {
		writePart(key)
		writePart(env[key])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (t *BashTool) executeWithPermissionScope(ctx context.Context, args map[string]any, expected map[string]any) (ToolResult, error) {
	if err := t.acquireExecution(ctx); err != nil {
		return startCommandErrorResult(ctx, err), nil
	}
	defer t.releaseExecution()

	t.policyMu.RLock()
	session := t.executionSessionSnapshotLocked()
	current := t.permissionScopeForSessionLocked(session)
	if !reflect.DeepEqual(current, expected) {
		t.policyMu.RUnlock()
		return NewErrorResult("Permission scope changed before execution: bash security mode, workspace, cwd, or environment no longer matches the approved invocation; retry under the current policy"), nil
	}
	view, revision := t.executionViewForSessionLocked(session)
	t.policyMu.RUnlock()

	result, err := view.executeLocked(ctx, args)
	t.commitExecutionSession(view, revision)
	return result, err
}

// acquireExecution serializes use of the persistent shell state without making
// a cancelled invocation wait for an unrelated long-running command.
func (t *BashTool) acquireExecution(ctx context.Context) error {
	t.executionOnce.Do(func() {
		t.executionGate = make(chan struct{}, 1)
	})
	select {
	case t.executionGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *BashTool) releaseExecution() {
	<-t.executionGate
}

// executionSessionSnapshotLocked captures the session while policyMu is held,
// preserving the global policy -> session lock order used by policy setters.
func (t *BashTool) executionSessionSnapshotLocked() bashSessionSnapshot {
	if t.session == nil {
		return bashSessionSnapshot{workDir: t.workDir, env: map[string]string{}}
	}
	return t.session.snapshot()
}

func (t *BashTool) executionViewLocked() (*BashTool, uint64) {
	return t.executionViewForSessionLocked(t.executionSessionSnapshotLocked())
}

// executionViewForSessionLocked returns a private immutable view of the policy
// and shell session for one invocation. It deliberately has its own BashSession:
// a concurrent workspace toggle may adjust the live session for future calls
// without changing this already-authorized process halfway through startup.
func (t *BashTool) executionViewForSessionLocked(session bashSessionSnapshot) (*BashTool, uint64) {
	view := &BashTool{
		workDir:                   t.workDir,
		session:                   newBashSessionFromSnapshot(session),
		taskManager:               t.taskManager,
		timeout:                   t.timeout,
		sandboxEnabled:            t.sandboxEnabled,
		unrestrictedMode:          t.unrestrictedMode,
		workspaceRoot:             t.workspaceRoot,
		workspaceBoundaryEnabled:  t.workspaceBoundaryEnabled,
		managedWorkspaceApplyBack: t.managedWorkspaceApplyBack,
		backgroundAllowed:         t.backgroundAllowed,
		policyRevision:            t.policyRevision,
	}
	return view, t.policyRevision
}

// commitExecutionSession carries cwd/env changes back only when the live policy
// is still the one under which the invocation started. A sandbox/workspace
// toggle wins over stale shell state from an older in-flight command.
func (t *BashTool) commitExecutionSession(view *BashTool, revision uint64) {
	if view == nil || view.session == nil {
		return
	}
	session := view.session.snapshot()

	t.policyMu.Lock()
	defer t.policyMu.Unlock()
	if t.policyRevision != revision || t.session == nil {
		return
	}
	t.session.replace(session)
}
