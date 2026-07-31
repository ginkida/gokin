package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/app"
	backgroundstore "gokin/internal/background"
)

type fakeSteerableHeadlessRunner struct {
	mu       sync.Mutex
	prompts  []string
	steer    bool
	started  chan string
	releases chan struct{}
	steers   chan string
}

func newFakeSteerableHeadlessRunner(steer bool) *fakeSteerableHeadlessRunner {
	return &fakeSteerableHeadlessRunner{
		steer:    steer,
		started:  make(chan string, 4),
		releases: make(chan struct{}, 4),
		steers:   make(chan string, 4),
	}
}

func (f *fakeSteerableHeadlessRunner) RunHeadlessWithOptions(
	ctx context.Context,
	prompt string,
	_ app.HeadlessOptions,
) (app.HeadlessResult, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, prompt)
	f.mu.Unlock()
	f.started <- prompt
	select {
	case <-f.releases:
		return app.HeadlessResult{Status: "success"}, nil
	case <-ctx.Done():
		return app.HeadlessResult{Status: "error"}, ctx.Err()
	}
}

func (f *fakeSteerableHeadlessRunner) TrySteerHeadless(message string) bool {
	f.steers <- message
	return f.steer
}

func TestBackgroundHeadlessLoopSteersClaimedInputIntoActiveTurn(t *testing.T) {
	store, worker := newBackgroundControlFixture(t)
	runner := newFakeSteerableHeadlessRunner(true)
	done := make(chan error, 1)
	go func() {
		done <- runBackgroundHeadlessLoop(context.Background(), runner, "initial", app.HeadlessOptions{
			OutputFormat: app.HeadlessOutputStreamJSON,
			Stdout:       io.Discard,
			Stderr:       io.Discard,
		}, worker)
	}()
	if got := <-runner.started; got != "initial" {
		t.Fatalf("initial prompt = %q", got)
	}
	control, err := store.EnqueueControl(worker.id, "adjust the active task")
	if err != nil {
		t.Fatal(err)
	}
	if got := waitString(t, runner.steers); got != control.Message {
		t.Fatalf("steer = %q", got)
	}
	runner.releases <- struct{}{}
	if err := waitError(t, done); err != nil {
		t.Fatalf("background loop: %v", err)
	}
	if next, err := store.ClaimNextControl(worker.id); err != nil || next != nil {
		t.Fatalf("completed steer remained in inbox: %+v, %v", next, err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.prompts) != 1 {
		t.Fatalf("steered input started extra turn: %#v", runner.prompts)
	}
}

func TestBackgroundHeadlessLoopFallsBackToNextTurn(t *testing.T) {
	store, worker := newBackgroundControlFixture(t)
	runner := newFakeSteerableHeadlessRunner(false)
	done := make(chan error, 1)
	go func() {
		done <- runBackgroundHeadlessLoop(context.Background(), runner, "initial", app.HeadlessOptions{
			OutputFormat: app.HeadlessOutputStreamJSON,
			Stdout:       io.Discard,
			Stderr:       io.Discard,
		}, worker)
	}()
	<-runner.started
	if _, err := store.EnqueueControl(worker.id, "run this as the next turn"); err != nil {
		t.Fatal(err)
	}
	if got := waitString(t, runner.steers); got != "run this as the next turn" {
		t.Fatalf("steer attempt = %q", got)
	}
	runner.releases <- struct{}{}
	if got := waitString(t, runner.started); got != "run this as the next turn" {
		t.Fatalf("next prompt = %q", got)
	}
	runner.releases <- struct{}{}
	if err := waitError(t, done); err != nil {
		t.Fatalf("background loop: %v", err)
	}
}

// The control that becomes the next turn's prompt stays `claimed` on disk for
// the whole of that turn. The loop's own 150ms inbox poll must not trip over
// it: before the guard, a follow-up turn that ran longer than one tick died
// with "ambiguous delivery", losing the answer and marking the job failed.
func TestBackgroundHeadlessLoopSurvivesItsOwnCarriedClaim(t *testing.T) {
	store, worker := newBackgroundControlFixture(t)
	runner := newFakeSteerableHeadlessRunner(false)
	done := make(chan error, 1)
	go func() {
		done <- runBackgroundHeadlessLoop(context.Background(), runner, "initial", app.HeadlessOptions{
			OutputFormat: app.HeadlessOutputStreamJSON,
			Stdout:       io.Discard,
			Stderr:       io.Discard,
		}, worker)
	}()
	<-runner.started
	if _, err := store.EnqueueControl(worker.id, "run this as the next turn"); err != nil {
		t.Fatal(err)
	}
	if got := waitString(t, runner.steers); got != "run this as the next turn" {
		t.Fatalf("steer attempt = %q", got)
	}
	runner.releases <- struct{}{}
	if got := waitString(t, runner.started); got != "run this as the next turn" {
		t.Fatalf("next prompt = %q", got)
	}

	// Hold the follow-up turn open across several ticker fires.
	select {
	case err := <-done:
		t.Fatalf("loop ended during its own follow-up turn: %v", err)
	case <-time.After(600 * time.Millisecond):
	}

	runner.releases <- struct{}{}
	if err := waitError(t, done); err != nil {
		t.Fatalf("background loop: %v", err)
	}
}

func TestBackgroundSendCommandQueuesOnlyForLeaseOwnedJob(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store, worker := newBackgroundControlFixture(t)
	lease, err := store.AcquireWorkerLease(worker.id)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	command := newBackgroundSendCmd()
	command.SetArgs([]string{worker.id[:8], "please", "also", "test"})
	if err := command.Execute(); err != nil {
		t.Fatalf("send command: %v", err)
	}
	control, err := store.ClaimNextControl(worker.id)
	if err != nil {
		t.Fatal(err)
	}
	if control == nil || control.Message != "please also test" {
		t.Fatalf("queued control = %+v", control)
	}
}

func TestBackgroundAttachQueuesInputAndDetachesWithoutStoppingWorker(t *testing.T) {
	store, worker := newBackgroundControlFixture(t)
	lease, err := store.AcquireWorkerLease(worker.id)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	command := newBackgroundAttachCmd()
	var stdout, stderr bytes.Buffer
	command.SetIn(strings.NewReader("check the race too\n/detach\n"))
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{worker.id[:8]})
	done := make(chan error, 1)
	go func() { done <- command.Execute() }()
	if err := waitError(t, done); err != nil {
		t.Fatalf("attach command: %v", err)
	}
	control, err := store.ClaimNextControl(worker.id)
	if err != nil {
		t.Fatal(err)
	}
	if control == nil || control.Message != "check the race too" {
		t.Fatalf("attached input = %+v", control)
	}
	held, err := store.WorkerLeaseHeld(worker.id)
	if err != nil || !held {
		t.Fatalf("attach stopped worker lease: held=%v err=%v", held, err)
	}
}

func newBackgroundControlFixture(t *testing.T) (*backgroundstore.Store, *backgroundWorkerContext) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store, err := backgroundstore.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	id := backgroundstore.NewJobID()
	if err := store.Create(backgroundstore.Job{
		ID: id, State: backgroundstore.StateStarting,
		WorkDir: t.TempDir(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkRunning(id, 12345); err != nil {
		t.Fatal(err)
	}
	return store, &backgroundWorkerContext{id: id, store: store}
}

func waitString(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for string")
		return ""
	}
}

func waitError(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for completion")
		return nil
	}
}
