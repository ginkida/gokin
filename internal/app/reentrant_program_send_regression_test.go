package app

import (
	"reflect"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gokin/internal/config"
	"gokin/internal/ui"
)

type reentrantProgramBarrierMsg struct {
	done chan struct{}
}

type orderedProgramSendMsg string

// noInitProgramProbe delegates to the real UI model but suppresses its
// perpetual spinner command. The ready signal proves Program.eventLoop has
// completed one Update and is accepting injected key messages before the
// regression trigger is sent.
type noInitProgramProbe struct {
	inner tea.Model
}

func (m noInitProgramProbe) Init() tea.Cmd { return nil }

func (m noInitProgramProbe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := m.inner.Update(msg)
	m.inner = updated
	if barrier, ok := msg.(reentrantProgramBarrierMsg); ok {
		close(barrier.done)
	}
	return m, cmd
}

func (m noInitProgramProbe) View() string { return m.inner.View() }

func startReentrantProgram(t *testing.T, a *App, model *ui.Model) *tea.Program {
	t.Helper()
	program := tea.NewProgram(
		noInitProgramProbe{inner: model},
		tea.WithInput(nil),
		tea.WithoutRenderer(),
	)
	a.program = program

	runDone := make(chan error, 1)
	go func() {
		_, err := program.Run()
		runDone <- err
	}()
	t.Cleanup(func() {
		program.Kill()
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
			t.Error("Bubble Tea program did not stop after cleanup")
		}
	})

	awaitProgramBarrier(t, program)
	return program
}

func awaitProgramBarrier(t *testing.T, program *tea.Program) {
	t.Helper()
	done := make(chan struct{})
	program.Send(reentrantProgramBarrierMsg{done: done})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Bubble Tea program did not complete an update barrier")
	}
}

func assertCallbackReturns(t *testing.T, returned <-chan struct{}, action string) {
	t.Helper()
	select {
	case <-returned:
	case <-time.After(500 * time.Millisecond):
		t.Errorf("%s deadlocked inside Update by synchronously re-entering Program.Send", action)
	}
}

// Bubble Tea's Program.msgs channel is unbuffered. An App callback invoked
// synchronously from Model.Update therefore cannot call Program.Send: the only
// receiver is the event loop that is waiting for Update itself to return.
// Ctrl+S reaches exactly that path through openSettingsModal.
func TestOpenSettingsCallbackDoesNotReenterProgramSend(t *testing.T) {
	a := &App{config: config.DefaultConfig()}
	model := ui.NewModel()
	callbackReturned := make(chan struct{})
	model.SetOpenSettingsCallback(func() {
		a.openSettingsModal()
		close(callbackReturned)
	})

	program := startReentrantProgram(t, a, model)
	program.Send(tea.KeyMsg{Type: tea.KeyCtrlS})
	assertCallbackReturns(t, callbackReturned, "Ctrl+S/openSettingsModal")
}

// Type-ahead Enter is an even more common instance of the same deadlock:
// App.handleSubmit acknowledges the accepted steer/FIFO position with UI
// messages before the Update callback can return.
func TestTypeAheadSubmitCallbackDoesNotReenterProgramSend(t *testing.T) {
	a := &App{config: config.DefaultConfig(), processing: true}
	model := ui.NewModel()
	model.SetState(ui.StateProcessing)
	callbackReturned := make(chan struct{})
	model.SetCallbacks(func(message string) {
		a.handleSubmit(message)
		close(callbackReturned)
	}, nil)

	program := startReentrantProgram(t, a, model)
	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("change direction")})
	awaitProgramBarrier(t, program)
	program.Send(tea.KeyMsg{Type: tea.KeyEnter})
	assertCallbackReturns(t, callbackReturned, "type-ahead Enter/handleSubmit")
}

// Esc normally calls CancelProcessing from Update. When type-ahead exists,
// cancellation drains it and sends QueuedCountMsg(0); that feedback must not
// block the very event loop which would receive it.
func TestCancelWithPendingFIFOCallbackDoesNotReenterProgramSend(t *testing.T) {
	a := &App{config: config.DefaultConfig(), processing: true}
	if _, ok := a.enqueuePending("queued follow-up"); !ok {
		t.Fatal("could not stage pending FIFO entry")
	}
	model := ui.NewModel()
	model.SetState(ui.StateProcessing)
	callbackReturned := make(chan struct{})
	model.SetCancelCallback(func() {
		a.CancelProcessing()
		close(callbackReturned)
	})

	program := startReentrantProgram(t, a, model)
	program.Send(tea.KeyMsg{Type: tea.KeyEsc})
	assertCallbackReturns(t, callbackReturned, "Esc/CancelProcessing with pending FIFO")
}

func TestAsyncProgramSendsPreserveReservationOrder(t *testing.T) {
	program, model := newCapturingProgram(t)
	a := &App{program: program}

	a.safeSendToProgramAsync(orderedProgramSendMsg("first"))
	a.safeSendToProgramAsync(orderedProgramSendMsg("second"))
	// The synchronous reservation cannot finish until both earlier async
	// reservations have been accepted by Bubble Tea.
	a.safeSendToProgram(orderedProgramSendMsg("third"))

	// Program.Send returns when the event loop accepts a message, just before
	// Update records it. Poll the race-safe capture until that final update has
	// run; the send order itself is asserted below.
	deadline := time.Now().Add(2 * time.Second)
	var got []string
	for {
		model.mu.Lock()
		got = got[:0]
		for _, msg := range model.msgs {
			if ordered, ok := msg.(orderedProgramSendMsg); ok {
				got = append(got, string(ordered))
			}
		}
		model.mu.Unlock()
		if len(got) == 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if want := []string{"first", "second", "third"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("program send order = %v, want %v", got, want)
	}
}
