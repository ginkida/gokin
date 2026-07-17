package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gokin/internal/commands"
	"gokin/internal/config"
)

// msgCapturingModel is a minimal tea.Model that records every message it
// receives via Update. We need a real *tea.Program (App.program is that
// concrete type, not an interface), and Program.Send dispatches through the
// model's Update — so this model captures exactly what the UI would see.
type msgCapturingModel struct {
	mu   sync.Mutex // Update runs on the program's event-loop goroutine; the test reads from its own
	msgs []tea.Msg
}

func (m *msgCapturingModel) Init() tea.Cmd { return nil }
func (m *msgCapturingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.mu.Lock()
	m.msgs = append(m.msgs, msg)
	m.mu.Unlock()
	if gate, ok := msg.(occupyEventLoopMsg); ok {
		if gate.entered != nil {
			close(gate.entered)
		}
		if gate.release != nil {
			<-gate.release
		}
	}
	return m, nil
}
func (m *msgCapturingModel) View() string { return "" }

// occupyEventLoopMsg parks the capturing program's single event-loop
// goroutine INSIDE Update until release is closed. Tests use it to make an
// "event loop is busy" window deterministic.
type occupyEventLoopMsg struct {
	entered chan struct{}
	release chan struct{}
}

// occupyEventLoop deterministically occupies the program's event loop and
// returns an idempotent release func (also registered as t.Cleanup so a
// t.Fatal in the occupied window can't leave the loop parked — cleanups run
// LIFO, so this fires before newCapturingProgram's Quit).
//
// This REPLACES the old idiom `model.mu.Lock(); prog.Send(occupier)`, which
// deadlocked on CI (v0.100.93): between Lock and Send, any concurrent async
// app message could win the loop's receive first, park Update on model.mu,
// and then the test's own synchronous Send blocked forever while holding the
// very mutex the loop needed — the exact goroutine anatomy of the hung
// TestFileCommandPromptContinuationPrecedesLaterTypeAhead run (and the most
// plausible mechanism behind the v0.100.90 pending-recovery flake, where the
// same race shifted orderings without fully deadlocking). The gate never
// touches model.mu and confirms occupancy before returning: earlier queued
// messages simply drain first, then the occupier provably parks the loop.
func occupyEventLoop(t *testing.T, prog *tea.Program) func() {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	go prog.Send(occupyEventLoopMsg{entered: entered, release: release})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("event loop never picked up the occupier")
	}
	var once sync.Once
	rel := func() { once.Do(func() { close(release) }) }
	t.Cleanup(rel)
	return rel
}

// newCapturingProgram creates a real tea.Program backed by a capturing model.
// WithoutRenderer avoids needing a terminal; WithoutSignalHandler prevents
// the program from stealing the test runner's signal handling.
func newCapturingProgram(t *testing.T) (*tea.Program, *msgCapturingModel) {
	t.Helper()
	m := &msgCapturingModel{}
	// A pipe input that never yields keeps p.Run() alive in this headless test
	// env. With the default stdin (no TTY), Run() returns immediately and
	// cancels the program ctx, so every safeSendToProgram lands in the
	// ctx.Done() branch and the model captures nothing.
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

func (m *msgCapturingModel) hasMsgType(target string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, msg := range m.msgs {
		if fmt.Sprintf("%T", msg) == target {
			return true
		}
	}
	return false
}

// dump returns a race-safe snapshot of captured message types for failure output.
func (m *msgCapturingModel) dump() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	types := make([]string, len(m.msgs))
	for i, msg := range m.msgs {
		types[i] = fmt.Sprintf("%T", msg)
	}
	return types
}

// panickingHandler is a commands.Handler substitute whose Execute always
// panics. We can't register custom commands on a frozen NewHandler, so we
// inject the behavior by replacing the handler's Execute path through a
// minimal stub that implements the command directly.
type stubCommand struct {
	name string
	fn   func(ctx context.Context, args []string, app commands.AppInterface) (string, error)
}

func (c *stubCommand) Name() string        { return c.name }
func (c *stubCommand) Description() string { return "test-only" }
func (c *stubCommand) Usage() string       { return "test-only" }
func (c *stubCommand) Execute(ctx context.Context, args []string, app commands.AppInterface) (string, error) {
	return c.fn(ctx, args, app)
}

// newHandlerWithCommand creates a frozen handler that knows about exactly
// one command. Built by constructing the handler, injecting the command map
// entry before freeze, then freezing — mirroring how NewHandler works but
// with a single test command.
func newHandlerWithCommand(cmd commands.Command) *commands.Handler {
	return commands.NewHandlerWithCommands(cmd)
}

// TestExecuteCommandCtx_PanicSendsResponseDone verifies that when a command
// panics during execution, the UI still receives ResponseDoneMsg so it exits
// the "Generating" state. Before the fix, ResponseDoneMsg was a bare tail
// call after commandHandler.Execute — a panic skipped it and the UI hung
// in StateProcessing forever (the "/login glm hangs" bug).
func TestExecuteCommandCtx_PanicSendsResponseDone(t *testing.T) {
	handler := newHandlerWithCommand(&stubCommand{
		name: "panic-test-cmd",
		fn: func(ctx context.Context, args []string, app commands.AppInterface) (string, error) {
			panic("simulated command failure")
		},
	})

	prog, model := newCapturingProgram(t)
	app := &App{
		commandHandler: handler,
		program:        prog,
		ctx:            context.Background(),
		config:         &config.Config{},
	}

	// The caller (handleSubmit) sets processing=true before dispatch; replicate
	// it so the "defer clears processing" assertion below actually verifies the
	// defer rather than reading a field that was already false.
	app.mu.Lock()
	app.processing = true
	app.mu.Unlock()

	done := make(chan struct{})
	go func() {
		app.executeCommandCtx(context.Background(), "panic-test-cmd", nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("executeCommandCtx hung — panic was not recovered")
	}

	// Give the tea.Program event loop a moment to process queued messages.
	time.Sleep(100 * time.Millisecond)

	if !model.hasMsgType("*errors.errorString") && !model.hasMsgType("*fmt.wrapError") && !model.hasMsgType("ui.ErrorMsg") {
		t.Errorf("expected an error-type message after panic; got: %v", model.dump())
	}
	if !model.hasMsgType("ui.ResponseDoneMsg") {
		t.Fatalf("ResponseDoneMsg not sent after panic — UI would stay stuck in 'Generating'. msgs: %v", model.dump())
	}

	app.mu.Lock()
	processing := app.processing
	app.mu.Unlock()
	if processing {
		t.Error("processing flag still true after panicked command — next input would be queued forever")
	}
}

// TestExecuteCommandCtx_ErrorSendsResponseDone verifies the normal error path
// also sends ResponseDoneMsg. This was already handled, but we pin it so
// regressions are caught.
func TestExecuteCommandCtx_ErrorSendsResponseDone(t *testing.T) {
	handler := newHandlerWithCommand(&stubCommand{
		name: "error-test-cmd",
		fn: func(ctx context.Context, args []string, app commands.AppInterface) (string, error) {
			return "", errors.New("command failed")
		},
	})

	prog, model := newCapturingProgram(t)
	app := &App{
		commandHandler: handler,
		program:        prog,
		ctx:            context.Background(),
		config:         &config.Config{},
	}

	done := make(chan struct{})
	go func() {
		app.executeCommandCtx(context.Background(), "error-test-cmd", nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("executeCommandCtx hung on error path")
	}
	time.Sleep(100 * time.Millisecond)

	if !model.hasMsgType("*errors.errorString") && !model.hasMsgType("*fmt.wrapError") && !model.hasMsgType("ui.ErrorMsg") {
		t.Errorf("expected an error-type message on error path; got: %v", model.dump())
	}
	if !model.hasMsgType("ui.ResponseDoneMsg") {
		t.Fatalf("ResponseDoneMsg not sent on error path — UI would stay stuck. msgs: %v", model.dump())
	}
}

// TestExecuteCommandCtx_SuccessSendsResponseDone pins the happy path.
func TestExecuteCommandCtx_SuccessSendsResponseDone(t *testing.T) {
	handler := newHandlerWithCommand(&stubCommand{
		name: "ok-test-cmd",
		fn: func(ctx context.Context, args []string, app commands.AppInterface) (string, error) {
			return "success", nil
		},
	})

	prog, model := newCapturingProgram(t)
	app := &App{
		commandHandler: handler,
		program:        prog,
		ctx:            context.Background(),
		config:         &config.Config{},
	}

	done := make(chan struct{})
	go func() {
		app.executeCommandCtx(context.Background(), "ok-test-cmd", nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("executeCommandCtx hung on success path")
	}
	time.Sleep(100 * time.Millisecond)

	if !model.hasMsgType("ui.StreamTextMsg") {
		t.Error("expected StreamTextMsg with command result")
	}
	if !model.hasMsgType("ui.ResponseDoneMsg") {
		t.Fatal("ResponseDoneMsg not sent on success path")
	}
}
