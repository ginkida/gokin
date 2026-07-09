package app

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gokin/internal/ui"
)

// newRealUIProgram creates a real *tea.Program backed by the ACTUAL ui.Model
// (not a stub) so DebugStateRequestMsg round-trips through the real
// handleMessageTypes wiring. Mirrors newCapturingProgram's setup
// (execute_command_ctx_test.go) — WithoutRenderer/WithoutSignalHandler avoid
// needing a real terminal, and a pipe input that never yields keeps Run()
// alive for the duration of the test.
func newRealUIProgram(t *testing.T) (*tea.Program, *ui.Model) {
	t.Helper()
	m := ui.NewModel()
	// Bell off: every "completed" BackgroundTaskMsg would otherwise return
	// bellCmd() (a "\a" write to stderr) — at this test's iteration counts
	// that's a sustained terminal bell storm for whoever runs the suite.
	m.SetBellEnabled(false)
	pr, pw := io.Pipe()
	p := tea.NewProgram(m,
		tea.WithoutRenderer(),
		tea.WithoutSignalHandler(),
		tea.WithInput(pr),
	)
	go func() { _, _ = p.Run() }()
	t.Cleanup(func() {
		p.Quit()
		_ = pw.Close()
		_ = pr.Close()
	})
	return p, m
}

// TestGetUIDebugState_ConcurrentWithBackgroundTasksNoCrash (round 8) pins
// the fix for a fatal, unrecoverable "concurrent map read and map write"
// crash risk: GetUIDebugState used to call a.tui.DebugState() directly from
// the command-execution goroutine, reading m.backgroundTasks (a plain Go
// map) while the Bubble Tea Update loop goroutine concurrently
// inserted/deleted entries via handleBackgroundTask (reachable via
// /debug-dump racing a background task starting/completing). This
// hammers BOTH sides concurrently under -race: many goroutines sending
// BackgroundTaskMsg (running/completed, i.e. real map insert+delete)
// while many goroutines call GetUIDebugState concurrently. A regression
// back to a direct a.tui.DebugState() call would reproduce Go's fatal
// concurrent-map crash (which -race also flags, and which is NOT
// recoverable via panic/recover) well within this test's iteration count.
func TestGetUIDebugState_ConcurrentWithBackgroundTasksNoCrash(t *testing.T) {
	prog, tuiModel := newRealUIProgram(t)
	a := &App{
		tui:     tuiModel,
		program: prog,
		ctx:     context.Background(),
	}

	const iterations = 3000
	var wg sync.WaitGroup

	// Writers: continuously start and complete background tasks — real map
	// insert (handleBackgroundTask's "running" case) and delete
	// ("completed" case).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			id := fmt.Sprintf("task-%d", i)
			prog.Send(ui.BackgroundTaskMsg{ID: id, Type: "agent", Description: "test task", Status: "running"})
			prog.Send(ui.BackgroundTaskMsg{ID: id, Type: "agent", Status: "completed", Summary: "done"})
		}
	}()

	// Readers: hammer GetUIDebugState concurrently with the writers above.
	errCh := make(chan error, iterations)
	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.GetUIDebugState()
			if err != nil {
				errCh <- err
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	// A genuine deadlock hangs forever, so a GENEROUS guard still catches it while
	// leaving ample headroom for 3000 iterations under `-race` on a slow/loaded CI
	// runner (locally ~6s under -race; the old 15s guard false-positived on shared
	// GitHub Actions 2-core runners). The `go test -timeout` default (10m) is the
	// ultimate backstop for a real hang.
	case <-time.After(120 * time.Second):
		t.Fatal("concurrent GetUIDebugState/BackgroundTaskMsg load deadlocked or hung")
	}
	close(errCh)

	for err := range errCh {
		t.Errorf("GetUIDebugState returned an error under concurrent load: %v", err)
	}
}

// TestGetUIDebugState_NoProgramFallsBackToDirect confirms the headless /
// pre-Run() fallback path still works (no program, no risk of concurrent
// access, so a direct read is fine and expected).
func TestGetUIDebugState_NoProgramFallsBackToDirect(t *testing.T) {
	m := ui.NewModel()
	a := &App{tui: m}

	state, err := a.GetUIDebugState()
	if err != nil {
		t.Fatalf("GetUIDebugState with no program: %v", err)
	}
	if _, ok := state.(ui.UIDebugState); !ok {
		t.Fatalf("expected a ui.UIDebugState, got %T", state)
	}
}

// TestGetUIDebugState_NilTUIErrors confirms the pre-existing nil guard is
// unaffected by the round-8 rewrite.
func TestGetUIDebugState_NilTUIErrors(t *testing.T) {
	a := &App{}
	if _, err := a.GetUIDebugState(); err == nil {
		t.Fatal("expected an error when a.tui is nil")
	}
}
