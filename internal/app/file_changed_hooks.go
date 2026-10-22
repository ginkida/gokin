package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gokin/internal/hooks"
	"gokin/internal/logging"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

type sessionFileHooks struct {
	ctx        context.Context
	cancel     context.CancelFunc
	watcher    *hooks.FileWatcher
	done       chan struct{}
	mu         sync.Mutex
	dynamic    []string
	pathKey    string
	async      *hooks.AsyncScope // mu; explicit async work can be cancelled by Esc
	cwd        string
	dirCtx     context.Context
	dirCancel  context.CancelFunc
	generation uint64
	cwdWorkers sync.WaitGroup
	closed     bool
	closeOnce  sync.Once
	closedDone chan struct{}
}

// The session lease protects publication. The separate fileHooksMu lets
// shutdown detach this owner without waiting for a filesystem or hook worker.
func (a *App) ensureFileHooksLocked(owner *sessionStartOwner, dynamic []string, replace bool) {
	if owner.ctx.Err() != nil {
		return
	}
	a.mu.Lock()
	interactive := a.running && !a.headlessRunActive
	a.mu.Unlock()
	owner.fileHooksMu.Lock()
	defer owner.fileHooksMu.Unlock()
	files := owner.fileHooks
	if files == nil {
		configured := a.hooksManager.CachedClaudeHooks(hooks.FileChanged)
		configured = append(configured, a.hooksManager.CachedClaudeHooks(hooks.CwdChanged)...)
		configured = append(configured, a.configuredFileSkillHooks()...)
		present := false
		for _, hook := range configured {
			present = present || hook.Type == hooks.FileChanged || hook.Type == hooks.CwdChanged
		}
		if !present && len(dynamic) == 0 && !interactive {
			return
		}
		ctx, cancel := context.WithCancel(owner.ctx)
		files = &sessionFileHooks{ctx: ctx, cancel: cancel, done: make(chan struct{}), closedDone: make(chan struct{}), cwd: owner.cwd, generation: 1}
		files.dirCtx, files.dirCancel = context.WithCancel(ctx)
		if owner.dir != "" {
			path := filepath.Join(owner.dir, "file-changed.sh")
			if err := os.WriteFile(path, nil, 0600); err == nil {
				owner.fileEnvFile = path
			} else {
				logging.Warn("FileChanged environment unavailable", "error", err)
			}
		}
		files.watcher = hooks.NewFileWatcher(ctx, func(ctx context.Context, change hooks.FileChange) { a.runFileChanged(owner, files, ctx, change) })
		owner.fileHooks = files
		go a.watchFileHookPolicy(owner, files)
	}
	if replace {
		files.mu.Lock()
		files.dynamic = append([]string{}, dynamic...)
		files.mu.Unlock()
	}
	a.refreshFileHookPathsLocked(owner, files)
}

func (a *App) refreshFileHookPathsLocked(owner *sessionStartOwner, files *sessionFileHooks) {
	if files.ctx.Err() != nil {
		return
	}
	all := a.hooksManager.GetHooks()
	all = append(all, a.configuredFileSkillHooks()...)
	files.mu.Lock()
	cwd := files.cwd
	files.mu.Unlock()
	paths := map[string]bool{}
	for _, hook := range all {
		if hook.Type != hooks.FileChanged || hook.Claude == nil || !a.hooksManager.HookEnabled(hook) {
			continue
		}
		for _, literal := range strings.Split(hook.Claude.Matcher, "|") {
			if literal == "" {
				continue
			}
			path := literal
			if !filepath.IsAbs(path) {
				path = filepath.Join(cwd, path)
			}
			paths[filepath.Clean(path)] = true
		}
	}
	selected := make([]string, 0, len(paths))
	for path := range paths {
		selected = append(selected, path)
	}
	sort.Strings(selected)
	if len(selected) > hooks.MaxFileWatchPaths {
		logging.Warn("file hook watch limit reached", "limit", hooks.MaxFileWatchPaths)
		selected = selected[:hooks.MaxFileWatchPaths]
	}
	// Configured literal paths retain priority when dynamic output fills the
	// resource budget. A hook cannot evict another configured watch by returning
	// a long list of alphabetically earlier paths.
	files.mu.Lock()
	for _, path := range files.dynamic {
		path = filepath.Clean(path)
		if !paths[path] && len(selected) < hooks.MaxFileWatchPaths {
			selected = append(selected, path)
			paths[path] = true
		}
	}
	files.mu.Unlock()
	sort.Strings(selected)
	key := strings.Join(selected, "\x00")
	files.mu.Lock()
	changed := key != files.pathKey
	if changed {
		files.pathKey = key
	}
	files.mu.Unlock()
	if changed {
		if err := files.watcher.SetPaths(selected); err != nil && files.ctx.Err() == nil {
			logging.Warn("configure file hook watches", "error", err)
		}
	}
}

func (a *App) fileHookOwnerCurrentLocked(owner *sessionStartOwner) bool {
	a.sessionStartMu.Lock()
	current := a.sessionStart == owner
	a.sessionStartMu.Unlock()
	return current && owner.ctx.Err() == nil && a.session != nil && a.session.GetID() == owner.id
}

func (a *App) configuredFileSkillHooks() []*hooks.Hook {
	if a.registry != nil {
		if registered, ok := a.registry.Get("skill"); ok {
			if skill, ok := registered.(*tools.SkillTool); ok {
				return append(skill.HookScope().ConfiguredHooks(hooks.FileChanged), skill.HookScope().ConfiguredHooks(hooks.CwdChanged)...)
			}
		}
	}
	return nil
}

func (a *App) watchFileHookPolicy(owner *sessionStartOwner, files *sessionFileHooks) {
	defer close(files.done)
	defer files.watcher.Close()
	defer func() {
		if failure := recover(); failure != nil {
			files.cancel()
			logging.Error("file hook policy watcher panicked", "panic", failure)
		}
	}()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-files.ctx.Done():
			return
		case <-tick.C:
			if a.sessionLeaseMu.TryLock() {
				func() {
					defer a.sessionLeaseMu.Unlock()
					if a.fileHookOwnerCurrentLocked(owner) {
						a.refreshFileHookPathsLocked(owner, files)
					}
				}()
			}
		}
	}
}

func (a *App) withFileHookLease(ctx context.Context, owner *sessionStartOwner, action func()) {
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for ctx.Err() == nil {
		if a.sessionLeaseMu.TryLock() {
			func() {
				defer a.sessionLeaseMu.Unlock()
				if a.fileHookOwnerCurrentLocked(owner) {
					action()
				}
			}()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (a *App) runFileChanged(owner *sessionStartOwner, files *sessionFileHooks, ctx context.Context, change hooks.FileChange) {
	var eventCtx context.Context
	var cwd string
	a.withFileHookLease(ctx, owner, func() {
		if !files.watcher.Contains(change.Path) {
			return
		}
		files.mu.Lock()
		directoryCtx := files.dirCtx
		cwd = files.cwd
		files.mu.Unlock()
		eventCtx = tools.ContextWithSkillHooks(directoryCtx, a.registry, a.hooksManager)
		info := hooks.RuntimeInfo{SessionID: owner.id, SessionTitle: a.session.GetTitle(), ProjectDir: a.workDir}
		owner.fileHooksMu.Lock()
		info.EnvFile = owner.fileEnvFile
		owner.fileHooksMu.Unlock()
		eventCtx = hooks.ContextWithRuntimeInfo(eventCtx, info)
		eventCtx = hooks.ContextWithAsyncScope(eventCtx, a.fileAsyncScope(owner, files))
		guarded := eventCtx
		eventCtx = hooks.ContextWithPromptGuard(eventCtx, func() error { return tools.SkillHooksError(guarded) })
	})
	if eventCtx == nil {
		return
	}
	resultCtx := eventCtx
	eventCtx = hooks.ContextWithDeferredHookResults(eventCtx, func(result hooks.Result) { a.applyFileChangeResults(owner, files, resultCtx, []hooks.Result{result}) })
	results := a.hooksManager.RunFileChanged(eventCtx, cwd, change.Path, change.Event)
	a.applyFileChangeResults(owner, files, resultCtx, results)
}

func (a *App) applyFileChangeResults(owner *sessionStartOwner, files *sessionFileHooks, ctx context.Context, results []hooks.Result) {
	a.withFileHookLease(ctx, owner, func() {
		for _, result := range results {
			if result.Error != nil {
				logging.Warn("FileChanged hook failed", "hook", result.Hook.DisplayName(), "error", result.Error)
				continue
			}
			if !a.hooksManager.HookResultCurrent(result.Hook) {
				continue
			}
			if result.SystemMessage != "" {
				a.safeSendToProgramAsync(ui.StatusUpdateMsg{Type: ui.StatusInfo, Message: result.SystemMessage})
			}
			if result.WatchPaths != nil {
				files.mu.Lock()
				files.dynamic = append([]string{}, result.WatchPaths...)
				files.mu.Unlock()
				a.refreshFileHookPathsLocked(owner, files)
			}
		}
	})
}

func (s *sessionFileHooks) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.mu.Lock()
		s.closed = true
		scope := s.async
		s.async = nil
		s.mu.Unlock()
		go func() {
			var workers sync.WaitGroup
			workers.Add(3)
			go func() { defer workers.Done(); s.watcher.Close() }()
			go func() { defer workers.Done(); scope.Close() }()
			go func() { defer workers.Done(); s.cwdWorkers.Wait() }()
			workers.Wait()
			<-s.done
			close(s.closedDone)
		}()
	})
	select {
	case <-s.closedDone:
	case <-time.After(5 * time.Second):
		logging.Warn("FileChanged policy cleanup exceeded wait limit")
	}
}

func (a *App) fileAsyncScope(owner *sessionStartOwner, files *sessionFileHooks) *hooks.AsyncScope {
	files.mu.Lock()
	defer files.mu.Unlock()
	if files.async == nil {
		var scope *hooks.AsyncScope
		scope = hooks.NewAsyncScope(files.dirCtx, a.tryWakeAsyncHooks, func() error {
			files.mu.Lock()
			current := files.async == scope
			files.mu.Unlock()
			a.sessionStartMu.Lock()
			current = current && a.sessionStart == owner
			a.sessionStartMu.Unlock()
			if !current {
				return fmt.Errorf("FileChanged async owner was cancelled or replaced")
			}
			return nil
		})
		files.async = scope
	}
	return files.async
}

func (a *App) currentFileAsyncHooks(owner *sessionStartOwner) *hooks.AsyncScope {
	if owner == nil {
		return nil
	}
	owner.fileHooksMu.Lock()
	files := owner.fileHooks
	owner.fileHooksMu.Unlock()
	if files == nil {
		return nil
	}
	files.mu.Lock()
	scope := files.async
	files.mu.Unlock()
	return scope
}

func (a *App) pendingFileAsyncHooks() bool {
	a.sessionStartMu.Lock()
	owner := a.sessionStart
	a.sessionStartMu.Unlock()
	return a.currentFileAsyncHooks(owner).HasPendingWork()
}

func (a *App) cancelFileAsyncHooks() {
	a.sessionStartMu.Lock()
	owner := a.sessionStart
	a.sessionStartMu.Unlock()
	if owner == nil {
		return
	}
	owner.fileHooksMu.Lock()
	files := owner.fileHooks
	owner.fileHooksMu.Unlock()
	if files == nil {
		return
	}
	files.mu.Lock()
	scope := files.async
	files.async = nil
	files.mu.Unlock()
	scope.Cancel()
}

// A successful replacement closes all old observational workers before its
// SessionEnd cleanup. Failed transactions keep the old watchers and processes.
func (a *App) invalidateSessionStartLocked() {
	a.sessionStartMu.Lock()
	owner := a.sessionStart
	a.sessionStart = nil
	a.sessionStartMu.Unlock()
	if owner != nil {
		owner.cancel()
		owner.async.Cancel()
		go closeSessionStartOwner(owner)
	}
}
