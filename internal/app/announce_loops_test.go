package app

import (
	"context"
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gokin/internal/loops"
	"gokin/internal/ui"
)

// appTestLoopStorage is a no-op in-memory Storage so a loops.Manager can be
// built in the app package without touching disk.
type appTestLoopStorage struct{}

func (appTestLoopStorage) Load() ([]*loops.Loop, []error) { return nil, nil }
func (appTestLoopStorage) Save(*loops.Loop) error         { return nil }
func (appTestLoopStorage) Delete(string) error            { return nil }

// TestAnnounceRestoredLoops_DoesNotBlockStartup pins the v0.100.76 hotfix for a
// FATAL startup hang: announceRestoredLoops runs inside App.Run() BEFORE
// a.program.Run() starts draining Bubble Tea's unbuffered msgs channel. The
// old code did a DIRECT, synchronous a.safeSendToProgram when active loops were
// restored — program.Send then blocked forever on the un-drained channel, so
// the whole process hung on "Starting Gokin..." for any user with a running
// loop persisted on disk.
//
// This test builds a REAL (non-running) *tea.Program — exactly the state at the
// call site — so a regression to a synchronous send would block program.Send
// forever and trip the timeout. The fix replays the toast from a goroutine, so
// announceRestoredLoops must return promptly.
func TestAnnounceRestoredLoops_DoesNotBlockStartup(t *testing.T) {
	mgr := loops.NewManager(appTestLoopStorage{})
	if _, err := mgr.Add("test task", loops.ModeInterval, 60); err != nil {
		t.Fatalf("Add loop: %v", err)
	}
	if got := len(mgr.Active()); got != 1 {
		t.Fatalf("expected 1 active loop, got %d", got)
	}

	// A real program that is NOT running: program.Send blocks on the unbuffered
	// msgs channel, so a synchronous send would deadlock here.
	pr, pw := io.Pipe()
	prog := tea.NewProgram(ui.NewModel(),
		tea.WithoutRenderer(),
		tea.WithoutSignalHandler(),
		tea.WithInput(pr),
	)
	t.Cleanup(func() { prog.Kill(); _ = pw.Close(); _ = pr.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	a := &App{loopManager: mgr, program: prog, ctx: ctx}

	done := make(chan struct{})
	go func() {
		a.announceRestoredLoops()
		close(done)
	}()

	select {
	case <-done:
		// Returned promptly — the send was deferred to a goroutine. Cancel the
		// context now so that goroutine (still inside its 800ms delay select)
		// exits via ctx.Done() instead of reaching the blocking program.Send.
		cancel()
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("announceRestoredLoops blocked with active loops — startup would hang forever")
	}
}

// TestAnnounceRestoredLoops_NilManagerNoOp guards the nil-manager fast path.
func TestAnnounceRestoredLoops_NilManagerNoOp(t *testing.T) {
	a := &App{ctx: context.Background()}
	done := make(chan struct{})
	go func() { a.announceRestoredLoops(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("announceRestoredLoops blocked with a nil loop manager")
	}
	_ = ui.StatusInfo // keep the ui import honest across build tags
}
