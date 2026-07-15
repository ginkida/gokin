package ui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestVisibleBatchProgressSettlesUnderlyingTurnBeforeClose(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.Msg
	}{
		{name: "response done", msg: ResponseDoneMsg{}},
		{name: "response error", msg: ErrorMsg(errors.New("request failed"))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel()
			m.state = StateProcessing

			updated, _ := m.Update(ProgressCompleteMsg{TotalItems: 2, SuccessCount: 2})
			progress := updated.(Model)
			if progress.state != StateBatchProgress || !progress.progressActive || progress.progressReturnState != StateProcessing {
				t.Fatalf("completed batch setup: state=%v active=%v return=%v", progress.state, progress.progressActive, progress.progressReturnState)
			}

			updated, _ = progress.Update(tt.msg)
			progress = updated.(Model)
			if progress.state != StateBatchProgress || !progress.progressActive {
				t.Fatalf("turn settlement hid the readable batch summary: state=%v active=%v", progress.state, progress.progressActive)
			}
			if progress.progressReturnState != StateInput {
				t.Fatalf("settled turn retained stale batch return=%v, want input", progress.progressReturnState)
			}

			updated, _ = progress.Update(CloseOverlayMsg{})
			closed := updated.(Model)
			if closed.state != StateInput || closed.progressActive {
				t.Fatalf("closing settled batch resurrected activity: state=%v active=%v", closed.state, closed.progressActive)
			}
		})
	}
}

// completedBatchWithCloseCommand uses the real ProgressModel producer and
// verifies that rapid Enter auto-repeat cannot enqueue a second close while
// the first asynchronous command is in flight.
func completedBatchWithCloseCommand(t *testing.T, returnState State) (Model, CloseOverlayMsg) {
	t.Helper()
	m := NewModel()
	m.state = returnState

	updated, _ := m.Update(ProgressCompleteMsg{TotalItems: 1, SuccessCount: 1})
	progress := updated.(Model)
	if progress.state != StateBatchProgress || !progress.progressModel.isComplete {
		t.Fatalf("completed batch setup: state=%v complete=%v", progress.state, progress.progressModel.isComplete)
	}

	updated, firstClose := progress.Update(tea.KeyMsg{Type: tea.KeyEnter})
	progress = updated.(Model)
	updated, duplicateClose := progress.Update(tea.KeyMsg{Type: tea.KeyEnter})
	progress = updated.(Model)
	if firstClose == nil {
		t.Fatal("first Enter did not produce a close command")
	}
	if duplicateClose != nil {
		t.Fatalf("rapid Enter enqueued a duplicate close command: %v", duplicateClose)
	}
	firstMsg, ok := firstClose().(CloseOverlayMsg)
	if !ok {
		t.Fatalf("first producer emitted %T, want CloseOverlayMsg", firstClose())
	}
	if firstMsg.ownerGeneration == 0 || firstMsg.ownerGeneration != progress.progressOwner {
		t.Fatalf("close owner=%d does not match active batch owner=%d", firstMsg.ownerGeneration, progress.progressOwner)
	}
	if !progress.progressModel.closePending {
		t.Fatal("completed child did not retain in-flight close ownership")
	}
	if view := stripAnsi(progress.progressModel.View()); !strings.Contains(view, "Closing…") || strings.Contains(view, "Enter/Esc") {
		t.Fatalf("in-flight close kept advertising a blocked exit: %q", view)
	}
	if hints := progress.contextualShortcutHintPairs(); len(hints) != 0 {
		t.Fatalf("in-flight close kept contextual exit hints: %v", hints)
	}
	return progress, firstMsg
}

func TestBatchCloseIsIdempotentBeforeCommandReturns(t *testing.T) {
	progress, closeMsg := completedBatchWithCloseCommand(t, StateStreaming)

	updated, _ := progress.Update(closeMsg)
	closed := updated.(Model)
	if closed.state != StateStreaming || closed.progressActive {
		t.Fatalf("first close did not restore live stream: state=%v active=%v", closed.state, closed.progressActive)
	}

	// Replaying the already-consumed command is harmless even without relying
	// on the producer's closePending gate.
	updated, _ = closed.Update(closeMsg)
	got := updated.(Model)
	if got.state != StateStreaming || got.progressActive {
		t.Fatalf("replayed close reset live stream: state=%v active=%v", got.state, got.progressActive)
	}
}

func TestStaleBatchCloseDoesNotClobberNewOverlay(t *testing.T) {
	progress, closeMsg := completedBatchWithCloseCommand(t, StateInput)

	updated, _ := progress.Update(closeMsg)
	closed := updated.(Model)
	updated, _ = closed.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	palette := updated.(Model)
	if palette.state != StateCommandPalette || !palette.commandPalette.IsVisible() {
		t.Fatalf("command palette did not open after batch close: state=%v visible=%v", palette.state, palette.commandPalette.IsVisible())
	}

	updated, _ = palette.Update(closeMsg)
	got := updated.(Model)
	if got.state != StateCommandPalette || !got.commandPalette.IsVisible() {
		t.Fatalf("stale close clobbered newer overlay: state=%v visible=%v", got.state, got.commandPalette.IsVisible())
	}
}

func TestStaleBatchCloseFromAIsRejectedAfterBatchBOpens(t *testing.T) {
	progressA, closeA := completedBatchWithCloseCommand(t, StateInput)

	updated, _ := progressA.Update(closeA)
	closedA := updated.(Model)
	if closedA.progressActive || closedA.progressOwner != 0 {
		t.Fatalf("batch A did not release ownership: active=%v owner=%d", closedA.progressActive, closedA.progressOwner)
	}

	updated, _ = closedA.Update(ProgressUpdateMsg{
		Current:     1,
		Total:       3,
		CurrentItem: "batch-b.go",
	})
	batchB := updated.(Model)
	if batchB.state != StateBatchProgress || !batchB.progressActive {
		t.Fatalf("batch B did not open: state=%v active=%v", batchB.state, batchB.progressActive)
	}
	if batchB.progressOwner == 0 || batchB.progressOwner == closeA.ownerGeneration {
		t.Fatalf("batch B owner=%d did not advance beyond A=%d", batchB.progressOwner, closeA.ownerGeneration)
	}

	updated, _ = batchB.Update(closeA)
	got := updated.(Model)
	if got.state != StateBatchProgress || !got.progressActive || got.progressOwner != batchB.progressOwner {
		t.Fatalf("stale A close dismissed B: state=%v active=%v owner=%d want=%d", got.state, got.progressActive, got.progressOwner, batchB.progressOwner)
	}
	if got.progressModel.currentItem != "batch-b.go" || got.progressModel.current != 1 {
		t.Fatalf("stale A close mutated B content: current=%d item=%q", got.progressModel.current, got.progressModel.currentItem)
	}
}
