package app

import (
	"context"
	"testing"
	"time"

	"gokin/internal/commands"
	"gokin/internal/config"
	"gokin/internal/ui"
)

// Once finishForegroundProcessing removes the FIFO head, that request is
// accepted foreground work. Cancellation in the handoff must therefore either
// cancel it or prevent it from starting. At present ownership is transferred
// under a.mu, but the new processingCancel is installed only later by
// startAcceptedSubmit, leaving Esc/Ctrl+C with no handle for either turn.
func TestCancelDuringAcceptedFIFOForegroundHandoffDoesNotStartNextRequest(t *testing.T) {
	queuedRan := make(chan struct{}, 1)
	queued := &stubCommand{
		name: "queued",
		fn: func(context.Context, []string, commands.AppInterface) (string, error) {
			queuedRan <- struct{}{}
			return "queued command ran", nil
		},
	}

	prog, model := newCapturingProgram(t)
	a := &App{
		commandHandler: commands.NewHandlerWithCommands(queued),
		program:        prog,
		ctx:            context.Background(),
		config:         &config.Config{},
	}
	a.processing = true
	if _, ok := a.enqueuePending("/queued"); !ok {
		t.Fatal("fixture could not enqueue next request")
	}

	// Hold Update so the finalizer blocks in its first Program.Send after it has
	// dequeued/accepted /queued but before startAcceptedSubmit installs a new
	// processingCancel.
	model.mu.Lock()
	modelLocked := true
	defer func() {
		if modelLocked {
			model.mu.Unlock()
		}
	}()
	prog.Send(ui.StreamTextMsg("occupy event loop"))

	finished := make(chan struct{})
	go func() {
		a.finishForegroundProcessing(nil)
		close(finished)
	}()

	deadline := time.Now().Add(time.Second)
	for a.pendingCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if a.pendingCount() != 0 {
		t.Fatal("finalizer did not accept the FIFO head")
	}
	a.processingMu.Lock()
	hasCancelOwner := a.processingCancel != nil
	a.processingMu.Unlock()
	if !hasCancelOwner {
		t.Fatal("accepted FIFO head has no cancellation owner")
	}

	a.CancelProcessing()
	model.mu.Unlock()
	modelLocked = false

	select {
	case <-queuedRan:
		t.Fatal("accepted FIFO request started after cancellation landed in the ownership handoff gap")
	case <-time.After(150 * time.Millisecond):
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("foreground finalizer did not return")
	}
}

// A PromptMarker continuation is owned by the slash command that produced it.
// If that command was explicitly cancelled, a late successful return from a
// context-insensitive command must not prepend and launch the continuation
// after CancelProcessing has already drained the queue.
func TestCancelledCommandCannotResurrectPromptMarkerContinuation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	expandedRan := make(chan struct{}, 1)
	fileCommand := &stubCommand{
		name: "file-command",
		fn: func(context.Context, []string, commands.AppInterface) (string, error) {
			close(started)
			<-release // Deliberately models a command that returns after ctx cancel.
			return commands.PromptMarker + "/expanded", nil
		},
	}
	expanded := &stubCommand{
		name: "expanded",
		fn: func(context.Context, []string, commands.AppInterface) (string, error) {
			expandedRan <- struct{}{}
			return "expanded prompt ran", nil
		},
	}

	prog, _ := newCapturingProgram(t)
	a := &App{
		commandHandler: commands.NewHandlerWithCommands(fileCommand, expanded),
		program:        prog,
		ctx:            context.Background(),
		config:         &config.Config{},
	}
	a.handleSubmit("/file-command")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("file command did not start")
	}

	a.CancelProcessing()
	close(release)

	select {
	case <-expandedRan:
		t.Fatal("cancelled command resurrected its PromptMarker continuation")
	case <-time.After(150 * time.Millisecond):
	}
}
