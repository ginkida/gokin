package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gokin/internal/hooks"
	"gokin/internal/logging"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

// This lifetime is independent of recoveryEpoch: Esc cancels the waiting
// prompt, while SessionStart continues until the conversation is replaced.
type sessionStartOwner struct {
	id, dir, envFile string
	cwd              string // session lease; separate from project/persistence identity
	ctx              context.Context
	cancel           context.CancelFunc
	async            *hooks.AsyncScope
	run              *sessionStartRun
	notes, initial   string // sessionStartMu
	fileHooksMu      sync.Mutex
	fileHooks        *sessionFileHooks
	fileEnvFile      string // fileHooksMu
	directoryMu      sync.Mutex
	directoryWorkers sync.WaitGroup
	directoryClosed  bool
	directoryNotes   []directoryHookNote       // directoryMu
	sdkRepositories  []sdkRegisteredRepository // sessionLeaseMu
}

type sessionStartRun struct {
	source  string
	done    chan struct{}
	results []hooks.Result // immutable after done closes
	applied bool           // session lease
}

func sessionStartResetSource(preserveGoal bool) string {
	if preserveGoal {
		return "compact"
	}
	return "clear"
}

func (a *App) setSessionStartSource(source string) {
	a.sessionStartMu.Lock()
	a.sessionStartSource = source
	a.sessionStartMu.Unlock()
}

func (a *App) ensureSessionStartHooks() *sessionStartOwner {
	if a == nil || a.session == nil || a.hooksManager == nil {
		return nil
	}
	a.ensureSDKRootBaseline()
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	return a.ensureSessionStartHooksLocked()
}

func (a *App) ensureSessionStartHooksLocked() *sessionStartOwner {
	a.sessionStartMu.Lock()
	owner, source := a.sessionStart, a.sessionStartSource
	a.sessionStartMu.Unlock()
	if owner != nil && owner.id == a.session.GetID() {
		a.ensureFileHooksLocked(owner, nil, false)
		return owner
	}
	if source == "" {
		source = "startup"
		if len(a.session.GetHistory()) > 0 {
			source = "resume"
		}
	}
	return a.startSessionStartLocked(source, true, true)
}

// The lease serializes publication with clear/resume/compaction. Hook workers
// never need it to finish, so /resume can join them while retaining the lease.
func (a *App) startSessionStartLocked(source string, replace bool, atLaunch ...bool) *sessionStartOwner {
	if a.hooksManager == nil || a.session == nil {
		return nil
	}
	a.sessionStartMu.Lock()
	previous := a.sessionStart
	a.sessionStartMu.Unlock()
	if replace || previous == nil || previous.id != a.session.GetID() {
		a.resetSDKRepositoryComponentsLocked()
	}
	a.sessionStartMu.Lock()
	owner := a.sessionStart
	if replace || owner == nil || owner.id != a.session.GetID() {
		if owner != nil {
			owner.cancel()
			owner.async.Cancel()
			go closeSessionStartOwner(owner)
		}
		parent := a.ctx
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithCancel(parent)
		owner = &sessionStartOwner{id: a.session.GetID(), ctx: ctx, cancel: cancel}
		owner.cwd = a.mainShellWorkingDirectory()
		dir, err := os.MkdirTemp("", "gokin-session-env-")
		if err == nil {
			owner.dir, owner.envFile = dir, filepath.Join(dir, "environment.sh")
			err = os.WriteFile(owner.envFile, nil, 0600)
		}
		if err != nil {
			logging.Warn("SessionStart environment file unavailable", "error", err)
			owner.envFile = ""
		}
		current := owner
		owner.async = hooks.NewAsyncScope(ctx, a.tryWakeAsyncHooks, func() error {
			a.sessionStartMu.Lock()
			valid := a.sessionStart == current
			a.sessionStartMu.Unlock()
			if !valid {
				return fmt.Errorf("SessionStart belongs to a replaced conversation")
			}
			return nil
		})
		a.sessionStart = owner
	}
	run := &sessionStartRun{source: source, done: make(chan struct{})}
	owner.run = run
	a.sessionStartSource = source
	a.sessionStartMu.Unlock()
	ctx := owner.ctx
	info := hooks.RuntimeInfo{SessionID: owner.id, SessionTitle: a.session.GetTitle(), ProjectDir: a.workDir, EnvFile: owner.envFile}
	info.SessionStartAtLaunch = len(atLaunch) > 0 && atLaunch[0]
	tools.BindSkillInvocationLedger(a.registry, a.session.InvocationLedger())
	if a.executor != nil {
		ctx = a.executor.SkillInvocationContext(ctx)
		base := hooks.RuntimeInfoFromContext(ctx)
		info.PermissionMode, info.Effort = base.PermissionMode, base.Effort
	} else {
		ctx = tools.ContextWithSkillHooks(ctx, a.registry, a.hooksManager)
	}
	if main := a.GetMainClient(); main != nil {
		info.Model = main.GetModel()
	}
	ctx = hooks.ContextWithRuntimeInfo(ctx, info)
	ctx = hooks.ContextWithScope(ctx, hooks.ScopeFromContext(ctx).CaptureEvent(hooks.SessionStart))
	ctx = hooks.ContextWithAsyncScope(ctx, owner.async)
	ctx = hooks.ContextWithDeferredHookResults(ctx, func(result hooks.Result) {
		if result.Error == nil && result.WatchPaths != nil {
			a.withFileHookLease(owner.ctx, owner, func() {
				if a.hooksManager.HookResultCurrent(result.Hook) {
					a.ensureFileHooksLocked(owner, result.WatchPaths, true)
				}
			})
		}
	})
	a.ensureFileHooksLocked(owner, nil, false)
	go func() {
		run.results = a.hooksManager.RunSessionStart(ctx, a.workDir, source)
		close(run.done)
		// Publish metadata even if the user never submits a prompt. A switch
		// may be joining this worker under the lease, so the completion signal
		// must precede publication, and cancellation must not wait for the lease.
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for owner.ctx.Err() == nil {
			if a.sessionLeaseMu.TryLock() {
				a.sessionStartMu.Lock()
				current := a.sessionStart == owner && owner.run == run
				a.sessionStartMu.Unlock()
				if current {
					a.applySessionStartLocked(owner, run)
				}
				a.sessionLeaseMu.Unlock()
				return
			}
			select {
			case <-owner.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return owner
}

func (a *App) beginSessionStartPromptWait(ctx context.Context, raw string) func() {
	if raw == "" || isStructuredOutputCorrection(ctx) {
		return func() {}
	}
	if _, automatic := ctx.Value(goalContinuationKey{}).(*goalContinuation); automatic {
		return func() {}
	}
	if _, automatic := ctx.Value(asyncHookWakeKey{}).(*hooks.AsyncScope); automatic {
		return func() {}
	}
	a.sessionStartMu.Lock()
	owner := a.sessionStart
	if owner == nil {
		a.sessionStartMu.Unlock()
		return func() {}
	}
	select {
	case <-owner.run.done:
		a.sessionStartMu.Unlock()
		return func() {}
	default:
	}
	a.sessionStartWaitID++
	id := a.sessionStartWaitID
	a.sessionStartMu.Unlock()
	a.safeSendToProgram(ui.SessionStartWaitMsg{ID: id, Prompt: raw, Waiting: true})
	return func() { a.safeSendToProgram(ui.SessionStartWaitMsg{ID: id}) }
}

func (a *App) withSessionStartHooks(ctx context.Context) context.Context {
	owner := a.ensureSessionStartHooks()
	if owner == nil {
		return ctx
	}
	ctx = hooks.ContextWithSessionEnvironment(ctx, func() []string {
		a.sessionStartMu.Lock()
		defer a.sessionStartMu.Unlock()
		if a.sessionStart != owner {
			return nil
		}
		var files []string
		if owner.envFile != "" {
			files = append(files, owner.envFile)
		}
		owner.fileHooksMu.Lock()
		if owner.fileEnvFile != "" {
			files = append(files, owner.fileEnvFile)
		}
		owner.fileHooksMu.Unlock()
		return files
	})
	ctx = a.withCwdChanges(ctx, owner)
	return hooks.ContextWithSessionStartGate(ctx, func(ctx context.Context) error {
		return a.awaitSessionStart(ctx, owner)
	}, func() string { return a.takeSessionStartNotes(owner) })
}

func (a *App) awaitSessionStart(ctx context.Context, owner *sessionStartOwner) error {
	for {
		a.sessionStartMu.Lock()
		current, run := a.sessionStart == owner, owner.run
		a.sessionStartMu.Unlock()
		if !current {
			return fmt.Errorf("SessionStart belongs to a replaced conversation")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-owner.ctx.Done():
			return owner.ctx.Err()
		case <-run.done:
		}
		a.sessionLeaseMu.Lock()
		a.sessionStartMu.Lock()
		current = a.sessionStart == owner && owner.run == run
		a.sessionStartMu.Unlock()
		if current && ctx.Err() == nil {
			a.applySessionStartLocked(owner, run)
		}
		a.sessionLeaseMu.Unlock()
		if current {
			return ctx.Err()
		}
	}
}

func (a *App) applySessionStartLocked(owner *sessionStartOwner, run *sessionStartRun) {
	if run.applied || owner.ctx.Err() != nil {
		return
	}
	run.applied = true
	var notes, initial []string
	var watchPaths []string
	pathsProvided := false
	for _, result := range run.results {
		if result.Error != nil {
			logging.Warn("SessionStart hook failed", "hook", result.Hook.DisplayName(), "error", result.Error)
			a.safeSendToProgramAsync(ui.StatusUpdateMsg{Type: ui.StatusWarning, Message: fmt.Sprintf("SessionStart hook %q failed: %v", result.Hook.DisplayName(), result.Error)})
			continue
		}
		if !a.hooksManager.HookEnabled(result.Hook) {
			continue
		}
		if result.AdditionalContext != "" {
			notes = append(notes, "[SessionStart hook context]\n"+result.AdditionalContext)
		}
		if result.InitialUserMessage != "" && (run.source == "startup" || run.source == "resume" || run.source == "fork") {
			initial = append(initial, result.InitialUserMessage)
		}
		if result.SessionTitle != "" && (run.source == "startup" || run.source == "resume" || run.source == "fork") {
			a.session.SetTitle(result.SessionTitle)
		}
		if result.SystemMessage != "" {
			a.safeSendToProgramAsync(ui.StatusUpdateMsg{Type: ui.StatusInfo, Message: result.SystemMessage})
		}
		if result.WatchPaths != nil {
			pathsProvided = true
			watchPaths = append(watchPaths, result.WatchPaths...)
		}
		if result.ReloadSkills && a.registry != nil {
			if registered, ok := a.registry.Get("skill"); ok {
				if skill, ok := registered.(*tools.SkillTool); ok {
					skill.ReloadCatalog()
				}
			}
			if a.agentTypeRegistry != nil {
				_ = a.agentTypeRegistry.SnapshotAgentTypes()
			}
		}
	}
	a.sessionStartMu.Lock()
	if len(notes) > 0 {
		owner.notes = strings.TrimSpace(owner.notes + "\n\n" + strings.Join(notes, "\n\n"))
	}
	if len(initial) > 0 {
		owner.initial = strings.Join(initial, "\n\n")
	}
	a.sessionStartMu.Unlock()
	a.ensureFileHooksLocked(owner, watchPaths, pathsProvided)
}

func (a *App) takeSessionStartNotes(owner *sessionStartOwner) string {
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	a.sessionStartMu.Lock()
	if a.sessionStart != owner || owner.ctx.Err() != nil {
		a.sessionStartMu.Unlock()
		return ""
	}
	notes := owner.notes
	owner.notes = ""
	a.sessionStartMu.Unlock()
	return strings.TrimSpace(notes + "\n\n" + hooks.TakeScopeNotes(owner.async) + "\n\n" + hooks.TakeScopeNotes(a.currentFileAsyncHooks(owner)) + "\n\n" + a.takeDirectoryHookNotes(owner))
}

func (a *App) takeSessionInitialMessage() string {
	a.sessionStartMu.Lock()
	defer a.sessionStartMu.Unlock()
	if a.sessionStart == nil {
		return ""
	}
	initial := a.sessionStart.initial
	a.sessionStart.initial = ""
	return initial
}

func closeSessionStartOwner(owner *sessionStartOwner) {
	owner.cancel()
	owner.directoryMu.Lock()
	owner.directoryClosed = true
	owner.directoryNotes = nil
	owner.directoryMu.Unlock()
	directoriesDone := make(chan struct{})
	go func() { owner.directoryWorkers.Wait(); close(directoriesDone) }()
	owner.fileHooksMu.Lock()
	files := owner.fileHooks
	owner.fileHooksMu.Unlock()
	if files != nil {
		files.Close()
	}
	owner.async.Close()
	select {
	case <-directoriesDone:
	case <-time.After(5 * time.Second):
		logging.Warn("DirectoryAdded cleanup exceeded wait limit")
	}
	select {
	case <-owner.run.done:
		if owner.dir != "" {
			_ = os.RemoveAll(owner.dir)
		}
	case <-time.After(4 * time.Second):
		logging.Warn("SessionStart cleanup exceeded wait limit")
	}
}

func (a *App) closeSessionStartHooks() {
	a.sessionStartMu.Lock()
	owner := a.sessionStart
	a.sessionStart = nil
	a.sessionStartMu.Unlock()
	if owner != nil {
		closeSessionStartOwner(owner)
	}
}
