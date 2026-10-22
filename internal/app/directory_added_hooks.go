package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"gokin/internal/hooks"
	"gokin/internal/logging"
	"gokin/internal/tools"
)

type directoryHookNote struct {
	hook *hooks.Hook
	text string
}

// Only explicit /add-dir commands publish this event. Boot flags, permission
// prompts and permission-editor grants keep the ordinary grant path.
func (a *App) GrantAllowedDirFromCommand(ctx context.Context, path string, persist bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	owner := a.ensureSessionStartHooks()
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	if owner != nil && !a.fileHookOwnerCurrentLocked(owner) {
		return "", fmt.Errorf("directory command belongs to a replaced conversation")
	}
	if lineage, ok := conversationLineageFromContext(ctx); ok && (a.session.GetID() != lineage.sessionID || a.recoveryEpoch.Load() != lineage.epoch) {
		return "", fmt.Errorf("directory command belongs to a stale turn")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if expanded := expandHomePath(path); !filepath.IsAbs(expanded) {
		path = filepath.Join(a.workDir, expanded)
	} else {
		path = expanded
	}
	resolved, err := resolveGrantTarget(path)
	if err != nil {
		return "", err
	}
	a.directoryGrantMu.Lock()
	defer a.directoryGrantMu.Unlock()
	covered := a.isDirAccessAllowed(resolved)
	granted, grantErr := a.grantAllowedDirLocked(resolved, persist)
	if granted != "" && !covered && owner != nil {
		a.startDirectoryAddedLocked(owner, granted, "slash_command")
	}
	return granted, grantErr
}

// The session lease pins the event identity until registration is published.
// Setup is observational, so no external hook runs under that lease.
func (a *App) startDirectoryAddedLocked(owner *sessionStartOwner, directory, source string) {
	owner.directoryMu.Lock()
	if owner.directoryClosed || owner.ctx.Err() != nil {
		owner.directoryMu.Unlock()
		return
	}
	owner.directoryWorkers.Add(1)
	owner.directoryMu.Unlock()
	ctx := owner.ctx
	info := hooks.RuntimeInfo{SessionID: owner.id, SessionTitle: a.session.GetTitle(), ProjectDir: a.workDir}
	if a.executor != nil {
		ctx = a.executor.SkillInvocationContext(ctx)
		base := hooks.RuntimeInfoFromContext(ctx)
		info.PermissionMode, info.Effort = base.PermissionMode, base.Effort
	} else {
		ctx = tools.ContextWithSkillHooks(ctx, a.registry, a.hooksManager)
	}
	ctx = hooks.ContextWithRuntimeInfo(ctx, info)
	ctx = hooks.ContextWithScope(ctx, hooks.ScopeFromContext(ctx).CaptureEvent(hooks.DirectoryAdded))
	ctx = hooks.ContextWithAsyncScope(ctx, owner.async)
	ctx = hooks.ContextWithDeferredHookResults(ctx, func(result hooks.Result) { a.applyDirectoryAddedResults(owner, source, []hooks.Result{result}) })
	cwd := owner.cwd
	go func() {
		defer owner.directoryWorkers.Done()
		defer func() {
			if failure := recover(); failure != nil {
				logging.Error("DirectoryAdded worker panicked", "panic", failure)
			}
		}()
		a.applyDirectoryAddedResults(owner, source, a.hooksManager.RunDirectoryAdded(ctx, cwd, directory, source))
	}()
}

func (a *App) applyDirectoryAddedResults(owner *sessionStartOwner, source string, results []hooks.Result) {
	a.withFileHookLease(owner.ctx, owner, func() {
		for _, result := range results {
			if result.Error != nil {
				logging.Debug("DirectoryAdded hook failed", "hook", result.Hook.DisplayName(), "error", result.Error)
			} else if result.SystemMessage != "" {
				logging.Debug("DirectoryAdded hook result", "source", source, "message", result.SystemMessage)
			}
			if source != "slash_command" || !a.hooksManager.HookResultCurrent(result.Hook) {
				continue
			}
			text := result.SystemMessage
			if result.Error != nil {
				text = "[DirectoryAdded] 1 hook failed; see the debug log for details."
			}
			if text == "" {
				continue
			}
			// Bound this passive inbox independently of the model's turn count.
			if len(text) > 64<<10 {
				text = text[:64<<10]
			}
			owner.directoryMu.Lock()
			if !owner.directoryClosed {
				if len(owner.directoryNotes) >= 64 {
					owner.directoryNotes = owner.directoryNotes[1:]
				}
				owner.directoryNotes = append(owner.directoryNotes, directoryHookNote{hook: result.Hook, text: text})
			}
			owner.directoryMu.Unlock()
		}
	})
}

// Called only by the root inbox while its conversation lease is held. Child
// scopes cannot take these notes, and removed sources lose undelivered output.
func (a *App) takeDirectoryHookNotes(owner *sessionStartOwner) string {
	owner.directoryMu.Lock()
	notes := owner.directoryNotes
	owner.directoryNotes = nil
	owner.directoryMu.Unlock()
	var texts []string
	for _, note := range notes {
		if a.hooksManager.HookResultCurrent(note.hook) {
			texts = append(texts, note.text)
		}
	}
	return strings.Join(texts, "\n\n")
}
