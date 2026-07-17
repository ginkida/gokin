package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"gokin/internal/commands"
	"gokin/internal/config"
	"gokin/internal/ui"
)

// TestExecuteCommandCtxCompletionDispatchesTypeAheadQueue is a regression
// contract for type-ahead submitted while a slash command is in flight. A
// command owns the same foreground-processing slot as a model turn, so every
// terminal path must release that slot AND hand the FIFO head to the normal
// submit path. Otherwise the badge remains at 1 and the user's message is
// stranded until some unrelated later request happens to drain the queue.
func TestExecuteCommandCtxCompletionDispatchesTypeAheadQueue(t *testing.T) {
	tests := []struct {
		name   string
		finish func() (string, error)
	}{
		{name: "success", finish: func() (string, error) { return "done", nil }},
		{name: "error", finish: func() (string, error) { return "", errors.New("expected command failure") }},
		{name: "panic", finish: func() (string, error) { panic("expected command panic") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			queuedRan := make(chan struct{}, 1)

			foreground := &stubCommand{
				name: "foreground",
				fn: func(context.Context, []string, commands.AppInterface) (string, error) {
					close(started)
					<-release
					return tt.finish()
				},
			}
			queued := &stubCommand{
				name: "queued",
				fn: func(context.Context, []string, commands.AppInterface) (string, error) {
					queuedRan <- struct{}{}
					return "queued command ran", nil
				},
			}

			prog, model := newCapturingProgram(t)
			a := &App{
				commandHandler: commands.NewHandlerWithCommands(foreground, queued),
				program:        prog,
				ctx:            context.Background(),
				config:         &config.Config{},
			}
			a.mu.Lock()
			a.processing = true
			a.mu.Unlock()

			foregroundDone := make(chan struct{})
			go func() {
				a.executeCommandCtx(context.Background(), "foreground", nil)
				close(foregroundDone)
			}()

			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("foreground command did not start")
			}

			// This is the real type-ahead entry point. Slash commands cannot be
			// steered into an active turn, so /queued must enter the pending FIFO.
			a.handleSubmit("/queued")
			if got := a.pendingCount(); got != 1 {
				t.Fatalf("type-ahead queue length while command runs = %d, want 1", got)
			}
			close(release)

			select {
			case <-foregroundDone:
			case <-time.After(time.Second):
				t.Fatal("foreground command did not finish")
			}

			select {
			case <-queuedRan:
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("queued slash command was stranded after foreground %s; pending=%d UI queue counts=%v",
					tt.name, a.pendingCount(), capturedQueuedCounts(model))
			}

			if got := a.pendingCount(); got != 0 {
				t.Fatalf("pending queue after dispatch = %d, want 0", got)
			}
			if counts := capturedQueuedCounts(model); !containsQueuedCount(counts, 0) {
				t.Fatalf("UI never received QueuedCountMsg(0); counts=%v", counts)
			}
		})
	}
}

// TestFileCommandPromptResubmitCannotBeStranded covers the second entry into
// the same lifecycle: a file-backed command returns PromptMarker and asks the
// app to resubmit its expansion as a new foreground request. The dispatch is
// currently started before executeCommandCtx releases processing ownership;
// if that goroutine observes busy=true it joins the FIFO and needs the command
// finalizer to drain it just like user-authored type-ahead.
func TestFileCommandPromptResubmitCannotBeStranded(t *testing.T) {
	expandedRan := make(chan struct{}, 1)
	fileCommand := &stubCommand{
		name: "file-command",
		fn: func(context.Context, []string, commands.AppInterface) (string, error) {
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

	prog, model := newCapturingProgram(t)
	a := &App{
		commandHandler: commands.NewHandlerWithCommands(fileCommand, expanded),
		program:        prog,
		ctx:            context.Background(),
		config:         &config.Config{},
	}
	a.mu.Lock()
	a.processing = true
	a.mu.Unlock()

	// Occupy the Bubble Tea event loop so executeCommandCtx pauses at its
	// ResponseDoneMsg after spawning file-command-dispatch. That makes the
	// documented busy re-entry path deterministic without reaching into the
	// scheduler or production implementation. Deterministic gate — see
	// occupyEventLoop for why the old mu+Send idiom deadlocked on CI.
	releaseLoop := occupyEventLoop(t, prog)

	foregroundDone := make(chan struct{})
	go func() {
		a.executeCommandCtx(context.Background(), "file-command", nil)
		close(foregroundDone)
	}()

	// Give file-command-dispatch time to observe the still-owned processing
	// slot. A future implementation may deliberately dispatch only after the
	// slot is released, so absence from the FIFO here is also valid.
	deadline := time.Now().Add(250 * time.Millisecond)
	for a.pendingCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	wasQueued := a.pendingCount() > 0

	releaseLoop()

	select {
	case <-foregroundDone:
	case <-time.After(time.Second):
		t.Fatal("file command did not finish")
	}

	select {
	case <-expandedRan:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("file-command expansion was stranded; pending=%d UI queue counts=%v",
			a.pendingCount(), capturedQueuedCounts(model))
	}

	if got := a.pendingCount(); got != 0 {
		t.Fatalf("pending queue after file-command expansion = %d, want 0", got)
	}
	if counts := capturedQueuedCounts(model); wasQueued && !containsQueuedCount(counts, 0) {
		t.Fatalf("queued file-command expansion ran without clearing its UI badge; counts=%v", counts)
	}
}

// TestFileCommandPromptContinuationPrecedesLaterTypeAhead pins causal ordering.
// The prompt produced by /file-command is the continuation of that accepted
// command; a follow-up typed while it is expanding must run after that prompt.
// Dispatching PromptMarker from an independent goroutine before foreground
// ownership is released makes the order scheduler-dependent and can append the
// continuation behind the later user message.
func TestFileCommandPromptContinuationPrecedesLaterTypeAhead(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runOrder := make(chan string, 2)
	fileCommand := &stubCommand{
		name: "file-command",
		fn: func(context.Context, []string, commands.AppInterface) (string, error) {
			close(started)
			<-release
			return commands.PromptMarker + "/expanded", nil
		},
	}
	expanded := &stubCommand{
		name: "expanded",
		fn: func(context.Context, []string, commands.AppInterface) (string, error) {
			runOrder <- "expanded"
			return "expanded prompt ran", nil
		},
	}
	after := &stubCommand{
		name: "after",
		fn: func(context.Context, []string, commands.AppInterface) (string, error) {
			runOrder <- "after"
			return "later type-ahead ran", nil
		},
	}

	prog, model := newCapturingProgram(t)
	a := &App{
		commandHandler: commands.NewHandlerWithCommands(fileCommand, expanded, after),
		program:        prog,
		ctx:            context.Background(),
		config:         &config.Config{},
	}

	// Use the real entry point for both the foreground file command and the
	// later user-authored slash command.
	a.handleSubmit("/file-command")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("file command did not start")
	}
	a.handleSubmit("/after")
	if got := a.pendingSnapshot(); len(got) != 1 || got[0] != "/after" {
		t.Fatalf("later type-ahead was not queued behind file command: %v", got)
	}

	// Occupy the UI event loop at the terminal ResponseDone send. This gives
	// the PromptMarker goroutine a deterministic chance to take its current
	// busy path. A correct implementation may instead stage the continuation
	// synchronously or dispatch it after release; both still satisfy the only
	// externally relevant assertion below: expanded before after.
	// Deterministic gate — see occupyEventLoop for why the old mu+Send idiom
	// deadlocked on CI (this exact test hung 10m on the v0.100.93 run).
	releaseLoop := occupyEventLoop(t, prog)
	close(release)

	deadline := time.Now().Add(250 * time.Millisecond)
	for a.pendingCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	releaseLoop()

	gotOrder := make([]string, 0, 2)
	for len(gotOrder) < 2 {
		select {
		case name := <-runOrder:
			gotOrder = append(gotOrder, name)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for file continuation and type-ahead; order=%v pending=%v UI queue counts=%v",
				gotOrder, a.pendingSnapshot(), capturedQueuedCounts(model))
		}
	}

	if gotOrder[0] != "expanded" || gotOrder[1] != "after" {
		t.Fatalf("file-command continuation lost causal order: got %v, want [expanded after]", gotOrder)
	}
	if got := a.pendingCount(); got != 0 {
		t.Fatalf("pending queue after both requests = %d, want 0", got)
	}
	if counts := capturedQueuedCounts(model); !containsQueuedCount(counts, 0) {
		t.Fatalf("UI never received final QueuedCountMsg(0); counts=%v", counts)
	}
}

func capturedQueuedCounts(model *msgCapturingModel) []int {
	model.mu.Lock()
	defer model.mu.Unlock()
	var counts []int
	for _, msg := range model.msgs {
		if count, ok := msg.(ui.QueuedCountMsg); ok {
			counts = append(counts, int(count))
		}
	}
	return counts
}

func containsQueuedCount(counts []int, want int) bool {
	for _, count := range counts {
		if count == want {
			return true
		}
	}
	return false
}
