package app

import (
	"context"
	"fmt"

	"gokin/internal/chat"
	"gokin/internal/logging"
	"gokin/internal/rewind"
	"gokin/internal/tools"
	"gokin/internal/ui"
	"gokin/internal/undo"
)

func (a *App) turnRewindStore() (*rewind.Store, error) {
	if a.session == nil || !a.sessionManager.PersistenceEnabled() {
		return nil, fmt.Errorf("turn checkpoints require session persistence")
	}
	a.rewindMu.Lock()
	defer a.rewindMu.Unlock()
	id := a.session.GetID()
	if a.rewindStore != nil && a.rewindSessionID == id {
		return a.syncRewindHistoryWindows(a.rewindStore, id)
	}
	if a.undoManager != nil {
		a.undoManager.SetObserver(nil)
		a.undoManager.Clear()
	}
	root, err := chat.RewindStorageDir()
	if err != nil {
		return nil, err
	}
	workDir := a.GetWorkDir()
	if workDir == "" {
		workDir = a.session.GetWorkDir()
	}
	store, err := rewind.Open(root, id, workDir)
	if err != nil {
		return nil, err
	}
	a.rewindStore, a.rewindSessionID = store, id
	if a.registry != nil {
		if tool, ok := a.registry.Get("read"); ok {
			if reader, ok := tool.(*tools.ReadTool); ok {
				reader.SetSummaryReader(a.ReadSummaryResource)
			}
		}
	}
	if a.undoManager != nil {
		a.undoManager.SetObserver(func(ops []undo.Operation) {
			if err := store.Record(ops); err != nil {
				logging.Warn("could not persist turn file snapshots", "error", err)
			}
		})
	}
	return a.syncRewindHistoryWindows(store, id)
}

func (a *App) syncRewindHistoryWindows(store *rewind.Store, id string) (*rewind.Store, error) {
	current, windows := a.session.PendingHistoryWindows()
	if current != id {
		return nil, fmt.Errorf("history-window session ownership changed")
	}
	if err := store.ImportHistoryWindows(windows); err != nil {
		return nil, err
	}
	a.session.AcknowledgeHistoryWindows(id, windows)
	return store, nil
}

func (a *App) captureTurnRewind(prompt string) {
	if !a.sessionManager.PersistenceEnabled() {
		return
	}
	store, err := a.turnRewindStore()
	if err == nil {
		_, err = store.Begin(prompt, a.session.GetState())
	}
	if err != nil {
		logging.Warn("turn checkpoint unavailable", "error", err)
		a.safeSendToProgram(ui.StatusUpdateMsg{Type: ui.StatusWarning, Message: "Could not capture this turn's rewind checkpoint: " + err.Error()})
	}
}
func (a *App) ListRewindPoints(ctx context.Context) ([]rewind.Point, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store, err := a.turnRewindStore()
	if err != nil {
		return nil, err
	}
	return store.Points(), nil
}

func (a *App) ShowRewindMenu(ctx context.Context, points []rewind.Point) bool {
	a.mu.Lock()
	interactive := a.running && !a.headlessDirect && a.tui != nil
	a.mu.Unlock()
	if !interactive || ctx.Err() != nil {
		return false
	}
	choices := make([]ui.RewindChoice, len(points))
	hasCode := false
	incomplete := false
	for i := len(points) - 1; i >= 0; i-- {
		hasCode = hasCode || len(points[i].Events) > 0
		incomplete = incomplete || points[i].Error != ""
		choices[i] = ui.RewindChoice{ID: points[i].ID, Prompt: points[i].Prompt, HasCode: hasCode && !incomplete}
	}
	if previous := a.PreviousClearSessionID(); previous != "" {
		choices = append([]ui.RewindChoice{{Prompt: "/resume " + previous + " (previous session)", PreviousSessionID: previous}}, choices...)
	}
	a.safeSendToProgram(ui.OpenRewindMenuMsg{Choices: choices})
	return true
}
func (a *App) RewindToPoint(ctx context.Context, id, mode string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	code, conversation := mode == "code" || mode == "both", mode == "conversation" || mode == "both"
	if !code && !conversation {
		return "", fmt.Errorf("select code, conversation or both")
	}
	if runner := a.GetAgentTaskRunner(); runner != nil {
		for _, task := range runner.ListTaskSummaries() {
			if task.Status == "running" || task.Status == "pending" {
				return "", fmt.Errorf("stop background agents with /tasks before rewinding")
			}
		}
	}
	if shells := a.GetBackgroundShellRunner(); shells != nil {
		for _, task := range shells.List() {
			if task.Status == "running" || task.Status == "pending" {
				return "", fmt.Errorf("stop background shell tasks with /tasks before rewinding")
			}
		}
	}
	store, err := a.turnRewindStore()
	if err != nil {
		return "", err
	}
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	if a.sessionLease == nil {
		lease, err := chat.AcquireSessionWriterLease(a.session.GetID())
		if err != nil {
			return "", err
		}
		a.sessionLease = lease
	}
	if !a.sessionLease.IsActive() || a.sessionLease.SessionID() != a.session.GetID() || a.rewindSessionID != a.session.GetID() {
		return "", fmt.Errorf("checkpoint session ownership changed")
	}
	previous := a.session.GetState()
	var prompt string
	apply := func(snapshot *rewind.Snapshot) (func() error, error) {
		state := a.session.GetState()
		if snapshot != nil {
			state = snapshot.State
		}
		state.ID, state.StartTime, state.Provider, state.WorkDir = previous.ID, previous.StartTime, previous.Provider, previous.WorkDir
		state.LastActive = previous.LastActive
		state.Version = previous.Version + 1
		state.ToolCheckpoints, state.PendingRecoveries = nil, nil
		probe := chat.NewSession()
		if err := probe.RestoreFromState(state); err != nil {
			return nil, err
		}
		publish := func(target *chat.SessionState) error {
			return a.sessionManager.ApplyAndSave(func() (func(), error) {
				before := a.session.GetState()
				if err := a.session.RestoreFromState(target); err != nil {
					return nil, err
				}
				tools.BindSkillInvocationLedger(a.registry, a.session.InvocationLedger())
				if err := tools.RestoreSkillHooks(a.registry, a.hooksManager); err != nil {
					_ = a.session.RestoreFromState(before)
					tools.BindSkillInvocationLedger(a.registry, a.session.InvocationLedger())
					_ = tools.RestoreSkillHooks(a.registry, a.hooksManager)
					return nil, err
				}
				return func() {
					_ = a.session.RestoreFromState(before)
					tools.BindSkillInvocationLedger(a.registry, a.session.InvocationLedger())
					_ = tools.RestoreSkillHooks(a.registry, a.hooksManager)
				}, nil
			})
		}
		if err := publish(state); err != nil {
			return nil, err
		}
		if snapshot != nil {
			prompt = snapshot.Prompt
		}
		return func() error { return publish(previous) }, nil
	}
	var count int
	restore := func() error { var err error; count, err = store.Restore(id, code, conversation, apply); return err }
	if a.undoManager != nil {
		err = a.undoManager.ExclusiveRestore(code, restore)
	} else {
		err = restore()
	}
	if err != nil {
		return "", err
	}
	// A file rewind also invalidates delayed tool replay against the old tree.
	tools.ResetSkillPermissionGrants(a.registry)
	a.advanceConversationEpoch()
	a.cancelRecoveryTimersForSession(previous.ID)
	a.dropRecoveryPendingForSession(previous.ID)
	a.session.SetToolCheckpoints(nil)
	if a.executor != nil {
		a.executor.ResetSideEffectLedger()
		a.executor.SetSideEffectDedup(false)
	}
	if conversation {
		a.mu.Lock()
		a.scratchpad = a.session.GetScratchpad()
		scratchpad := a.scratchpad
		a.mu.Unlock()
		if a.agentRunner != nil {
			a.agentRunner.SetSharedScratchpad(scratchpad)
		}
		a.safeSendToProgram(ui.ScratchpadMsg(scratchpad))
		a.safeSendToProgram(ui.RewindPromptMsg{Prompt: prompt})
	}
	a.RefreshTokenCount()
	return fmt.Sprintf("Restored %s before the selected prompt (%d tracked file operation(s)).", mode, count), nil
}
