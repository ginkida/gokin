package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/chat"
)

func TestGracefulShutdownWaitsForForegroundFinalization(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	workDir := t.TempDir()
	journal, err := NewExecutionJournal(workDir)
	if err != nil {
		t.Fatal(err)
	}
	appCtx, appCancel := context.WithCancel(context.Background())
	application := &App{
		ctx:     appCtx,
		cancel:  appCancel,
		session: chat.NewSession(),
		journal: journal,
	}
	if !application.foregroundWorkers.Add() {
		t.Fatal("foreground admission unexpectedly closed")
	}

	cancelled := make(chan struct{})
	release := make(chan struct{})
	application.mu.Lock()
	application.processing = true
	application.mu.Unlock()
	application.processingMu.Lock()
	application.processingCancel = func() {
		select {
		case <-cancelled:
		default:
			close(cancelled)
		}
	}
	application.processingMu.Unlock()

	go func() {
		<-cancelled
		<-release
		application.mu.Lock()
		application.processing = false
		application.mu.Unlock()
		application.foregroundWorkers.Done()
	}()

	shutdownDone := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		application.gracefulShutdown(ctx)
		close(shutdownDone)
	}()

	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("graceful shutdown did not cancel foreground owner")
	}
	select {
	case <-shutdownDone:
		t.Fatal("graceful shutdown returned before foreground finalization")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("graceful shutdown did not join finalized foreground work")
	}

	snapshot, err := journal.LoadRecovery()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || snapshot.Processing {
		t.Fatalf("clean shutdown recovery snapshot = %+v, want processing=false", snapshot)
	}
}

func TestGracefulShutdownPreservesInterruptedSnapshotAfterDeadline(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	journal, err := NewExecutionJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	application := &App{
		ctx:     context.Background(),
		session: chat.NewSession(),
		journal: journal,
	}
	if !application.foregroundWorkers.Add() {
		t.Fatal("foreground admission unexpectedly closed")
	}
	t.Cleanup(application.foregroundWorkers.Done)
	application.mu.Lock()
	application.processing = true
	application.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	application.gracefulShutdown(ctx)

	snapshot, err := journal.LoadRecovery()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || !snapshot.Processing {
		t.Fatalf("deadline shutdown recovery snapshot = %+v, want processing=true", snapshot)
	}
}

func TestExecutionJournalSaveRecoveryReturnsPersistenceFailure(t *testing.T) {
	blockingDirectory := t.TempDir()
	journal := &ExecutionJournal{
		recoveryPath: blockingDirectory,
	}

	err := journal.SaveRecovery(RecoverySnapshot{SessionID: "session"})
	if err == nil {
		t.Fatal("SaveRecovery hid persistence failure")
	}
}

func TestRecoverySnapshotFailureBecomesHeadlessTerminalOutcome(t *testing.T) {
	application := &App{
		session: chat.NewSession(),
		journal: &ExecutionJournal{recoveryPath: t.TempDir()},
	}
	if err := application.beginHeadlessPolicyTracking(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		application.mu.Lock()
		application.endHeadlessPolicyTrackingLocked()
		application.mu.Unlock()
	}()

	if err := application.saveRecoverySnapshot(); err == nil {
		t.Fatal("recovery snapshot persistence failure was hidden")
	}
	terminal := application.headlessTerminalOutcomeSnapshot()
	if terminal == nil || terminal.Kind != "persistence_failed" {
		t.Fatalf("headless terminal outcome = %+v, want persistence_failed", terminal)
	}
}

func TestBeginShutdownSealsForegroundAdmission(t *testing.T) {
	application := &App{}
	application.beginShutdown()

	if application.foregroundWorkers.Add() {
		application.foregroundWorkers.Done()
		t.Fatal("foreground work was admitted after shutdown boundary")
	}

	application.mu.Lock()
	shuttingDown := application.shuttingDown
	application.mu.Unlock()
	if !shuttingDown {
		t.Fatal("shutdown boundary was not published")
	}
}

func TestFinishForegroundProcessingDoesNotSendOrDispatchDuringShutdown(t *testing.T) {
	application := &App{}
	application.mu.Lock()
	application.processing = true
	application.shuttingDown = true
	application.mu.Unlock()

	var callbackCalls atomic.Int32
	application.finishForegroundProcessing(func() {
		callbackCalls.Add(1)
	})

	if got := callbackCalls.Load(); got != 0 {
		t.Fatalf("terminal UI callback ran %d times during shutdown, want 0", got)
	}
	application.mu.Lock()
	processing := application.processing
	dropLeftovers := application.dropSteerLeftovers
	application.mu.Unlock()
	if processing || !dropLeftovers {
		t.Fatalf("shutdown finalization state: processing=%v dropLeftovers=%v", processing, dropLeftovers)
	}
}

func TestHandleQuitDoesNotWaitInsideBubbleTeaCallback(t *testing.T) {
	application := &App{}
	if !application.foregroundWorkers.Add() {
		t.Fatal("foreground admission unexpectedly closed")
	}

	returned := make(chan struct{})
	go func() {
		application.handleQuit()
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("handleQuit blocked waiting for foreground work")
	}
	application.foregroundWorkers.Done()
}

func TestHeadlessForegroundParticipatesInShutdownJoin(t *testing.T) {
	application := &App{}
	claim, err := application.claimHeadlessForeground(func() {})
	if err != nil {
		t.Fatal(err)
	}
	application.beginShutdown()

	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := application.foregroundWorkers.WaitWithContext(waitCtx); err == nil {
		t.Fatal("headless invocation was not included in foreground join")
	}

	application.releaseHeadlessForeground(claim, false)
	if err := application.foregroundWorkers.WaitWithContext(context.Background()); err != nil {
		t.Fatalf("foreground join after headless release: %v", err)
	}
}
