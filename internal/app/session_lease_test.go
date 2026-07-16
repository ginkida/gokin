package app

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/commands"
	"gokin/internal/config"
)

type cancelOnErrCallContext struct {
	context.Context
	failOn int32
	calls  atomic.Int32
}

func (c *cancelOnErrCallContext) Err() error {
	if c.calls.Add(1) >= c.failOn {
		return context.Canceled
	}
	return nil
}

func saveSessionLeaseFixture(t *testing.T, id, provider, workDir, message string) *chat.SessionState {
	t.Helper()
	historyManager, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}
	session := chat.NewSession()
	session.SetID(id)
	session.SetWorkDir(workDir)
	session.SetProvider(provider)
	if message != "" {
		session.AddUserMessage(message)
	}
	if err := historyManager.SaveFull(session); err != nil {
		t.Fatalf("SaveFull(%s): %v", id, err)
	}
	state, err := historyManager.LoadFull(id)
	if err != nil {
		t.Fatalf("LoadFull(%s): %v", id, err)
	}
	return state
}

func newSessionLeaseTestApp(t *testing.T, id, provider, workDir, message string) *App {
	t.Helper()
	session := chat.NewSession()
	session.SetID(id)
	session.SetWorkDir(workDir)
	session.SetProvider(provider)
	if message != "" {
		session.AddUserMessage(message)
	}
	manager, err := chat.NewSessionManager(session, chat.DefaultSessionManagerConfig())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Model.Provider = provider
	application := &App{
		config:         cfg,
		workDir:        workDir,
		session:        session,
		sessionManager: manager,
	}
	lease, err := chat.AcquireSessionWriterLease(id)
	if err != nil {
		t.Fatalf("AcquireSessionWriterLease(%s): %v", id, err)
	}
	if err := application.AdoptSessionWriterLease(lease); err != nil {
		_ = lease.Release()
		t.Fatalf("AdoptSessionWriterLease(%s): %v", id, err)
	}
	t.Cleanup(func() {
		if err := application.ReleaseSessionWriterLease(); err != nil {
			t.Errorf("ReleaseSessionWriterLease(%s): %v", id, err)
		}
	})
	return application
}

func firstPersistedText(t *testing.T, state *chat.SessionState) string {
	t.Helper()
	if state == nil || len(state.History) == 0 || len(state.History[0].Parts) == 0 {
		t.Fatalf("state has no first persisted text: %+v", state)
	}
	return state.History[0].Parts[0].Text
}

func TestAdoptSessionWriterLeaseRejectsInactiveLease(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	session := chat.NewSession()
	session.SetID("inactive-lease")
	application := &App{session: session}
	lease, err := chat.AcquireSessionWriterLease("inactive-lease")
	if err != nil {
		t.Fatalf("AcquireSessionWriterLease: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := application.AdoptSessionWriterLease(lease); err == nil {
		t.Fatal("AdoptSessionWriterLease accepted an already released lease")
	}
}

func TestSwitchSessionConcurrentAutoResumeCandidateHasOneWriter(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	workDir := t.TempDir()
	candidate := saveSessionLeaseFixture(t, "shared-resume", "glm", workDir, "persisted target")
	first := newSessionLeaseTestApp(t, "fresh-one", "glm", workDir, "first current")
	second := newSessionLeaseTestApp(t, "fresh-two", "glm", workDir, "second current")

	type switchResult struct {
		application *App
		originalID  string
		err         error
	}
	start := make(chan struct{})
	results := make(chan switchResult, 2)
	for _, contender := range []struct {
		application *App
		originalID  string
	}{{first, "fresh-one"}, {second, "fresh-two"}} {
		contender := contender
		go func() {
			<-start
			_, err := contender.application.SwitchSession(context.Background(), candidate, false)
			results <- switchResult{contender.application, contender.originalID, err}
		}()
	}
	close(start)

	var winner, loser switchResult
	for range 2 {
		result := <-results
		if result.err == nil {
			if winner.application != nil {
				t.Fatal("more than one app acquired and restored the auto-resume candidate")
			}
			winner = result
			continue
		}
		if !errors.Is(result.err, chat.ErrSessionWriterLeaseBusy) {
			t.Fatalf("losing switch error = %v, want ErrSessionWriterLeaseBusy", result.err)
		}
		loser = result
	}
	if winner.application == nil || loser.application == nil {
		t.Fatalf("winner=%v loser=%v, want exactly one of each", winner.application != nil, loser.application != nil)
	}
	if got := winner.application.session.GetID(); got != "shared-resume" {
		t.Fatalf("winner session ID = %q, want shared-resume", got)
	}
	if got := loser.application.session.GetID(); got != loser.originalID {
		t.Fatalf("loser session ID = %q, want unchanged %q", got, loser.originalID)
	}

	if duplicate, err := chat.AcquireSessionWriterLease("shared-resume"); !errors.Is(err, chat.ErrSessionWriterLeaseBusy) {
		if err == nil {
			_ = duplicate.Release()
		}
		t.Fatalf("target lease error = %v, want busy while winner is active", err)
	}
	winnerOld, err := chat.AcquireSessionWriterLease(winner.originalID)
	if err != nil {
		t.Fatalf("winner did not release its previous lease: %v", err)
	}
	_ = winnerOld.Release()
	if duplicate, err := chat.AcquireSessionWriterLease(loser.originalID); !errors.Is(err, chat.ErrSessionWriterLeaseBusy) {
		if err == nil {
			_ = duplicate.Release()
		}
		t.Fatalf("loser current lease error = %v, want busy", err)
	}
}

func TestSwitchSessionBusyTargetRollsBackWithoutMutation(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	workDir := t.TempDir()
	candidate := saveSessionLeaseFixture(t, "busy-target", "glm", workDir, "target history")
	application := newSessionLeaseTestApp(t, "current-session", "glm", workDir, "unsaved current history")

	heldTarget, err := chat.AcquireSessionWriterLease("busy-target")
	if err != nil {
		t.Fatalf("hold target lease: %v", err)
	}
	defer heldTarget.Release()

	if _, err := application.SwitchSession(context.Background(), candidate, false); !errors.Is(err, chat.ErrSessionWriterLeaseBusy) {
		t.Fatalf("SwitchSession error = %v, want ErrSessionWriterLeaseBusy", err)
	}
	if got := application.session.GetID(); got != "current-session" {
		t.Fatalf("failed switch changed current ID to %q", got)
	}
	history := application.session.GetHistory()
	if len(history) != 1 || history[0].Parts[0].Text != "unsaved current history" {
		t.Fatalf("failed switch changed current history: %+v", history)
	}
	if duplicate, err := chat.AcquireSessionWriterLease("current-session"); !errors.Is(err, chat.ErrSessionWriterLeaseBusy) {
		if err == nil {
			_ = duplicate.Release()
		}
		t.Fatalf("current lease error after rollback = %v, want busy", err)
	}
}

func TestSwitchSessionValidationFailureKeepsCurrentAndReleasesTarget(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	workDir := t.TempDir()
	candidate := saveSessionLeaseFixture(t, "incompatible-target", "kimi", workDir, "target history")
	application := newSessionLeaseTestApp(t, "protected-current", "glm", workDir, "current history")

	if _, err := application.SwitchSession(context.Background(), candidate, false); !errors.Is(err, ErrSessionProviderMismatch) {
		t.Fatalf("SwitchSession error = %v, want ErrSessionProviderMismatch", err)
	}
	if got := application.session.GetID(); got != "protected-current" {
		t.Fatalf("validation failure changed current ID to %q", got)
	}
	history := application.session.GetHistory()
	if len(history) != 1 || history[0].Parts[0].Text != "current history" {
		t.Fatalf("validation failure changed current history: %+v", history)
	}
	if duplicate, err := chat.AcquireSessionWriterLease("protected-current"); !errors.Is(err, chat.ErrSessionWriterLeaseBusy) {
		if err == nil {
			_ = duplicate.Release()
		}
		t.Fatalf("current lease error after validation failure = %v, want busy", err)
	}
	targetLease, err := chat.AcquireSessionWriterLease("incompatible-target")
	if err != nil {
		t.Fatalf("failed switch leaked target lease: %v", err)
	}
	_ = targetLease.Release()
}

func TestSwitchFailureAfterRecoveryReleaseRearmsCurrentSession(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	workDir := t.TempDir()
	candidate := saveSessionLeaseFixture(t, "switch-failure-target", "glm", workDir, "target history")
	application := newSessionLeaseTestApp(t, "switch-failure-current", "glm", workDir, "current history")
	recovery := validSerializedRecovery(
		"switch-release-rearm", application.session.GetID(), chat.PendingRecoveryScheduled)
	recovery.NotBefore = time.Now().Add(time.Hour)
	recovery.GenerationSignature = chat.PendingRecoveryGenerationSignature(recovery)
	application.session.AddPendingRecovery(recovery, "")
	epoch := application.recoveryEpoch.Load()
	claimed, checkpoints, err := application.claimPendingRecovery(recovery.ID, recovery.SessionID, epoch)
	if err != nil {
		t.Fatalf("claimPendingRecovery: %v", err)
	}
	if _, ok := application.enqueueRecoveryPending(
		claimed.Message, claimed.UserMessage, claimed.ID, claimed.SessionID, epoch, checkpoints); !ok {
		t.Fatal("enqueueRecoveryPending rejected fixture")
	}
	application.markRecoveryDispatched(claimed.SessionID, claimed.ID)

	// SwitchSession checks Err once at entry, once after validating the target,
	// and once after releasing/flushing the current recovery. Fail at the third
	// boundary to exercise rollback liveness after the release transaction.
	ctx := &cancelOnErrCallContext{Context: context.Background(), failOn: 3}
	if _, err := application.SwitchSession(ctx, candidate, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("SwitchSession error = %v, want context.Canceled", err)
	}
	if got := application.session.GetID(); got != "switch-failure-current" {
		t.Fatalf("failed switch published session %q", got)
	}
	if got := application.pendingCount(); got != 0 {
		t.Fatalf("released recovery retained executable FIFO owner: %d", got)
	}
	recoveries := application.session.GetPendingRecoveries()
	if len(recoveries) != 1 || recoveries[0].ID != recovery.ID ||
		recoveries[0].State != chat.PendingRecoveryScheduled {
		t.Fatalf("current recovery after failed switch = %+v", recoveries)
	}
	key := recoveryTimerKey{sessionID: recovery.SessionID, recoveryID: recovery.ID}
	application.recoveryTimerMu.Lock()
	_, rearmed := application.recoveryTimers[key]
	timerCount := len(application.recoveryTimers)
	application.recoveryTimerMu.Unlock()
	if !rearmed || timerCount != 1 {
		t.Fatalf("failed switch timers: rearmed=%v count=%d, want one current-session timer", rearmed, timerCount)
	}
	application.cancelRecoveryTimersForSession(recovery.SessionID)
}

func TestResumeCommandFlushesCurrentSessionAndTransfersLease(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	workDir := t.TempDir()
	saveSessionLeaseFixture(t, "resume-target", "glm", workDir, "target history")
	application := newSessionLeaseTestApp(t, "current-unsaved", "glm", workDir, "latest unsaved turn")

	message, err := (&commands.ResumeCommand{}).Execute(context.Background(), []string{"resume-target"}, application)
	if err != nil {
		t.Fatalf("ResumeCommand.Execute: %v", err)
	}
	if !strings.Contains(message, "restored") {
		t.Fatalf("resume message = %q, want restored confirmation", message)
	}
	if got := application.session.GetID(); got != "resume-target" {
		t.Fatalf("active session ID = %q, want resume-target", got)
	}

	historyManager, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}
	flushed, err := historyManager.LoadFull("current-unsaved")
	if err != nil {
		t.Fatalf("old session was not synchronously flushed: %v", err)
	}
	if got := firstPersistedText(t, flushed); got != "latest unsaved turn" {
		t.Fatalf("flushed old history = %q, want latest unsaved turn", got)
	}

	oldLease, err := chat.AcquireSessionWriterLease("current-unsaved")
	if err != nil {
		t.Fatalf("old session lease was not released after switch: %v", err)
	}
	_ = oldLease.Release()
	if duplicate, err := chat.AcquireSessionWriterLease("resume-target"); !errors.Is(err, chat.ErrSessionWriterLeaseBusy) {
		if err == nil {
			_ = duplicate.Release()
		}
		t.Fatalf("target lease error = %v, want busy after switch", err)
	}
}
