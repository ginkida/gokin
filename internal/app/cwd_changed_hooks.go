package app

import (
	"context"
	"os"
	"path/filepath"

	"gokin/internal/hooks"
	"gokin/internal/logging"
	"gokin/internal/tools"
)

func (a *App) mainShellWorkingDirectory() string {
	cwd := a.workDir
	if a.registry != nil {
		if registered, ok := a.registry.Get("bash"); ok {
			if bash, ok := registered.(*tools.BashTool); ok {
				cwd = bash.WorkingDirectory()
			}
		}
	}
	return cwd
}

func sameShellDirectory(first, second string) bool {
	if filepath.Clean(first) == filepath.Clean(second) {
		return true
	}
	left, leftErr := os.Stat(first)
	right, rightErr := os.Stat(second)
	return leftErr == nil && rightErr == nil && os.SameFile(left, right)
}

func (a *App) withCwdChanges(ctx context.Context, owner *sessionStartOwner) context.Context {
	return hooks.ContextWithCwdChanges(ctx, nil, func(ctx context.Context, commit func() (string, string)) {
		a.withFileHookLease(owner.ctx, owner, func() {
			if ctx.Err() != nil {
				return
			}
			if lineage, ok := conversationLineageFromContext(ctx); ok && (a.session.GetID() != lineage.sessionID || a.recoveryEpoch.Load() != lineage.epoch) {
				return
			}
			oldCwd, newCwd := commit()
			if oldCwd == "" || newCwd == "" || sameShellDirectory(oldCwd, newCwd) {
				return
			}
			a.cwdChangedLocked(owner, oldCwd, newCwd, hooks.RuntimeInfoFromContext(ctx))
		})
	})
}

// Publication is synchronous; observational work is not. Each directory has
// a private environment path and cancellation context so a previous hook can
// never refill the new directory's environment or update its watch list.
func (a *App) cwdChangedLocked(owner *sessionStartOwner, oldCwd, newCwd string, info hooks.RuntimeInfo) {
	owner.cwd = newCwd
	a.ensureFileHooksLocked(owner, nil, false)
	owner.fileHooksMu.Lock()
	files := owner.fileHooks
	if files == nil {
		owner.fileHooksMu.Unlock()
		return
	}
	files.mu.Lock()
	if files.closed || files.ctx.Err() != nil {
		files.mu.Unlock()
		owner.fileHooksMu.Unlock()
		return
	}
	files.dirCancel()
	oldAsync := files.async
	files.async = nil
	files.generation++
	files.cwd = newCwd
	files.dirCtx, files.dirCancel = context.WithCancel(files.ctx)
	ctx := files.dirCtx
	files.cwdWorkers.Add(1)
	files.mu.Unlock()
	owner.fileEnvFile = ""
	if owner.dir != "" {
		file, err := os.CreateTemp(owner.dir, "directory-env-*.sh")
		if err == nil {
			owner.fileEnvFile = file.Name()
			err = file.Close()
		}
		if err != nil {
			logging.Warn("CwdChanged environment unavailable", "error", err)
		}
	}
	envFile := owner.fileEnvFile
	owner.fileHooksMu.Unlock()
	oldAsync.Cancel()
	a.refreshFileHookPathsLocked(owner, files)
	ctx = tools.ContextWithSkillHooks(ctx, a.registry, a.hooksManager)
	info.SessionID, info.SessionTitle, info.ProjectDir, info.EnvFile = owner.id, a.session.GetTitle(), a.workDir, envFile
	info.AgentID, info.AgentType, info.ToolUseID, info.ToolNameOverride = "", "", "", ""
	ctx = hooks.ContextWithRuntimeInfo(ctx, info)
	ctx = hooks.ContextWithAsyncScope(ctx, a.fileAsyncScope(owner, files))
	guarded := ctx
	ctx = hooks.ContextWithPromptGuard(ctx, func() error { return tools.SkillHooksError(guarded) })
	resultCtx := ctx
	ctx = hooks.ContextWithDeferredHookResults(ctx, func(result hooks.Result) { a.applyFileChangeResults(owner, files, resultCtx, []hooks.Result{result}) })
	go func() {
		defer files.cwdWorkers.Done()
		defer func() {
			if failure := recover(); failure != nil {
				logging.Error("CwdChanged worker panicked", "panic", failure)
			}
		}()
		results := a.hooksManager.RunCwdChanged(ctx, oldCwd, newCwd)
		a.applyFileChangeResults(owner, files, resultCtx, results)
	}()
}
