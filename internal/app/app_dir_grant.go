package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gokin/internal/logging"
	"gokin/internal/permission"
	"gokin/internal/security"
	"gokin/internal/tools"
)

// expandHomePath expands a leading ~ to the user's home directory. Mirrors the
// config loader's expandTilde (which is unexported).
func expandHomePath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// resolveGrantTarget normalizes a user-supplied directory into the canonical,
// symlink-resolved absolute form used everywhere else, and verifies it is a real
// directory. Returns an error for non-existent paths or files.
func resolveGrantTarget(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("no path given")
	}
	abs, err := filepath.Abs(filepath.Clean(expandHomePath(path)))
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("directory does not exist: %s", abs)
		}
		return "", fmt.Errorf("cannot access %s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", abs)
	}
	// Resolve symlinks so the stored form matches NewPathValidator's normalization.
	if resolved, rErr := filepath.EvalSymlinks(abs); rErr == nil {
		abs = resolved
	}
	return abs, nil
}

// GrantAllowedDir grants the agent access to a directory outside the workspace.
// Session-only by default; persist=true also writes it to config so it survives
// restart. Returns the resolved directory path. Refuses ungrantable locations
// (filesystem root, system dirs, .git, secret dirs) via security.IsGrantableDir.
func (a *App) GrantAllowedDir(path string, persist bool) (string, error) {
	resolved, err := resolveGrantTarget(path)
	if err != nil {
		return "", err
	}
	if err := security.IsGrantableDir(resolved); err != nil {
		return "", err
	}

	// Add to the session grant list (dedup).
	a.grantedDirsMu.Lock()
	already := false
	for _, d := range a.grantedDirs {
		if d == resolved {
			already = true
			break
		}
	}
	if !already {
		a.grantedDirs = append(a.grantedDirs, resolved)
	}
	a.grantedDirsMu.Unlock()

	// Propagate to every path-scoping tool (foreground + future sub-agents).
	a.applyGrantedDirsToTools()
	// Refresh the model's turn-context dir list so it sees the new grant THIS
	// turn (no locks held here — pushTurnContext re-reads a.mu/grantedDirsMu).
	a.pushTurnContext()

	if persist {
		a.mu.Lock()
		added := a.config.AddAllowedDir(resolved)
		var saveErr error
		if added {
			saveErr = a.config.Save()
		}
		a.mu.Unlock()
		if saveErr != nil {
			// Keep the session grant even if persistence failed (graceful, same as
			// checkAllowedDirs) — tell the caller it is session-only this run.
			logging.Warn("failed to persist granted dir", "dir", resolved, "error", saveErr)
			return resolved, fmt.Errorf("granted for this session, but failed to save to config: %w", saveErr)
		}
	}

	logging.Info("directory access granted", "dir", resolved, "persist", persist)
	return resolved, nil
}

// RevokeGrantedDir removes a SESSION grant (config-persisted dirs are not
// touched — those require editing config). Returns true if a grant was removed.
func (a *App) RevokeGrantedDir(path string) (bool, error) {
	resolved, err := resolveGrantTarget(path)
	if err != nil {
		return false, err
	}
	a.grantedDirsMu.Lock()
	removed := false
	kept := make([]string, 0, len(a.grantedDirs))
	for _, d := range a.grantedDirs {
		if d == resolved {
			removed = true
			continue
		}
		kept = append(kept, d)
	}
	a.grantedDirs = kept
	a.grantedDirsMu.Unlock()

	if removed {
		a.applyGrantedDirsToTools()
		a.pushTurnContext() // refresh the model's turn-context dir list
		logging.Info("directory access revoked", "dir", resolved)
	}
	return removed, nil
}

// applyGrantedDirsToTools is the single propagation chokepoint: it rebuilds the
// effective allow-list (persisted config dirs ++ session grants) and pushes it
// to every path-scoping tool. Snapshots under grantedDirsMu, releases, then
// reads a.mu — never holds both at once (see lock ordering).
func (a *App) applyGrantedDirsToTools() {
	a.grantedDirsMu.Lock()
	granted := append([]string(nil), a.grantedDirs...)
	a.grantedDirsMu.Unlock()

	a.mu.Lock()
	effective := append([]string(nil), a.config.Tools.AllowedDirs...)
	reg := a.registry
	runner := a.agentRunner
	a.mu.Unlock()
	effective = append(effective, granted...)

	tools.SetAllowedDirsOnRegistry(reg, effective)
	if runner != nil {
		runner.SetGrantedDirs(effective)
	}

	// Cache the effective list for the model's turn-context block (read under
	// grantedDirsMu only, never a.mu — see dirCtxSnapshot doc).
	a.grantedDirsMu.Lock()
	a.dirCtxSnapshot = effective
	a.grantedDirsMu.Unlock()
}

// isDirAccessAllowed reports whether path is already inside the workspace or an
// allowed/granted directory (so the access gate need not prompt). It uses the
// SAME containment + normalization as real enforcement (security.IsPathWithinAny).
func (a *App) isDirAccessAllowed(path string) bool {
	a.mu.Lock()
	dirs := make([]string, 0, len(a.config.Tools.AllowedDirs)+2)
	dirs = append(dirs, a.workDir)
	dirs = append(dirs, a.config.Tools.AllowedDirs...)
	a.mu.Unlock()

	a.grantedDirsMu.Lock()
	dirs = append(dirs, a.grantedDirs...)
	a.grantedDirsMu.Unlock()

	ok, err := security.IsPathWithinAny(path, dirs)
	return err == nil && ok
}

// ListAllowedDirs returns the human-readable list of directories the agent may
// access beyond the workspace: persisted config dirs (marked) plus session
// grants. Used by /add-dir with no args and by /status.
func (a *App) ListAllowedDirs() []string {
	a.mu.Lock()
	persisted := append([]string(nil), a.config.Tools.AllowedDirs...)
	a.mu.Unlock()
	a.grantedDirsMu.Lock()
	session := append([]string(nil), a.grantedDirs...)
	a.grantedDirsMu.Unlock()

	out := make([]string, 0, len(persisted)+len(session))
	seen := make(map[string]bool)
	for _, d := range persisted {
		if !seen[d] {
			out = append(out, d+" (persisted)")
			seen[d] = true
		}
	}
	for _, d := range session {
		if !seen[d] {
			out = append(out, d+" (session)")
			seen[d] = true
		}
	}
	return out
}

// dirGrantPrompt is the executor's ask-on-access handler: the agent tried to
// touch an absolute path outside the workspace. It resolves the directory to
// grant (the path itself if a dir, else its parent), refuses ungrantable
// locations outright, auto-grants in headless (no stdin to block on), and
// otherwise reuses the permission-prompt modal. Any "allow" decision grants the
// directory for the session (revocable via /remove-dir or /clear); deny returns
// false so the executor surfaces an actionable error. Returns (allowed, error).
func (a *App) dirGrantPrompt(ctx context.Context, toolName, path string) (bool, error) {
	// Determine the directory to grant: the path itself if it is a directory,
	// otherwise its containing directory (a write to a new file in an external dir).
	dir := path
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		dir = filepath.Dir(path)
	}
	if resolved, err := resolveGrantTarget(dir); err == nil {
		dir = resolved
	}

	// Never prompt for (or grant) an ungrantable location — deny outright.
	if err := security.IsGrantableDir(dir); err != nil {
		logging.Info("out-of-workspace access refused (non-grantable)", "dir", dir, "tool", toolName, "reason", err)
		return false, nil
	}

	// Headless / no interactive program: follow policy — auto-grant for the
	// session, symmetric with promptPermission's auto-allow. (Eval/scripts run
	// the user's configured policies without blocking on stdin.)
	if a.program == nil {
		if _, err := a.GrantAllowedDir(dir, false); err != nil {
			return false, nil
		}
		return true, nil
	}

	req := &permission.Request{
		ToolName:  "access directory",
		Args:      map[string]any{"directory": dir, "requested_by": toolName},
		RiskLevel: permission.RiskMedium,
		Reason: fmt.Sprintf(
			"The agent wants to access %s, which is OUTSIDE the workspace (requested by the %s tool). Allowing grants access to this directory for the session — revoke later with /remove-dir.",
			dir, toolName),
	}
	decision, err := a.promptPermission(ctx, req)
	if err != nil {
		return false, err
	}
	switch decision {
	case permission.DecisionAllow, permission.DecisionAllowSession:
		if _, gErr := a.GrantAllowedDir(dir, false); gErr != nil {
			return false, gErr
		}
		logging.Info("out-of-workspace access granted via prompt", "dir", dir, "tool", toolName)
		return true, nil
	default:
		return false, nil
	}
}

// resetGrantedDirs clears all session grants and re-propagates (called from
// ClearConversation — /clear must restart with a clean slate). Persisted config
// dirs are untouched.
func (a *App) resetGrantedDirs() {
	a.grantedDirsMu.Lock()
	had := len(a.grantedDirs) > 0
	a.grantedDirs = nil
	a.grantedDirsMu.Unlock()
	if had {
		a.applyGrantedDirsToTools()
	}
}
