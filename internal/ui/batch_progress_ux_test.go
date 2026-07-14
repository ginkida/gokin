package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestProgressModelNormalizesLifecycleState(t *testing.T) {
	m := NewProgressModel(DefaultStyles())
	m.Start("  \n ", -4)
	if m.title != "Batch operation" || m.total != 0 || m.duration != 0 {
		t.Fatalf("invalid start was not normalized: title=%q total=%d duration=%s", m.title, m.total, m.duration)
	}

	m.Start(" Build artifacts ", 3)
	m.UpdateProgress(20, "  current\n item ", "  nearly\t done ")
	if m.current != 3 || m.currentItem != "current item" || m.message != "nearly done" {
		t.Fatalf("progress update was not normalized: current=%d item=%q message=%q", m.current, m.currentItem, m.message)
	}
	m.AddItem(ProgressItem{Status: ProgressStatusCompleted, Name: "  "})
	if m.items[0].Name != "Untitled item" || m.successCount != 1 {
		t.Fatalf("item fallback/count missing: %+v success=%d", m.items[0], m.successCount)
	}

	m.Complete()
	finished := m.duration
	time.Sleep(time.Millisecond)
	m.Complete()
	if m.duration != finished || !m.isComplete || m.isPaused || m.current != m.total {
		t.Fatalf("completion must be terminal and idempotent: %+v", m)
	}
}

func TestProgressSnapshotReplacesItemsAndRecomputesStats(t *testing.T) {
	m := NewProgressModel(DefaultStyles())
	m.Start("Batch", 4)
	m.AddItem(ProgressItem{Name: "old failure", Status: ProgressStatusFailed})

	updated, _ := m.Update(ProgressUpdateMsg{
		Current: 9,
		Total:   2,
		Items: []ProgressItem{
			{Name: "done", Status: ProgressStatusCompleted},
			{Name: "skipped", Status: ProgressStatusSkipped},
		},
	})
	if updated.current != 2 || updated.total != 2 || len(updated.items) != 2 {
		t.Fatalf("snapshot bounds/items wrong: current=%d total=%d items=%d", updated.current, updated.total, len(updated.items))
	}
	if updated.successCount != 1 || updated.failureCount != 0 || updated.skippedCount != 1 {
		t.Fatalf("snapshot stats remained stale: %d/%d/%d", updated.successCount, updated.failureCount, updated.skippedCount)
	}
}

func TestProgressViewFitsAllWidthsAndShortHeights(t *testing.T) {
	for width := 1; width <= 64; width++ {
		m := NewProgressModel(DefaultStyles())
		m.SetSize(width, 7)
		m.Start(strings.Repeat("Long title 界 ", 12), 12)
		m.UpdateProgress(5, strings.Repeat("current item ", 12), strings.Repeat("status message ", 12))
		for i := 0; i < 12; i++ {
			status := ProgressStatusCompleted
			if i == 2 {
				status = ProgressStatusFailed
			}
			m.AddItem(ProgressItem{Name: strings.Repeat("item ", 20), Status: status, Error: strings.Repeat("error ", 10)})
		}

		got := m.View()
		if rows := lipgloss.Height(got); rows > 7 {
			t.Fatalf("width=%d compact view height=%d want <=7:\n%s", width, rows, stripAnsi(got))
		}
		for lineNo, line := range strings.Split(got, "\n") {
			if lineWidth := lipgloss.Width(line); lineWidth > width {
				t.Fatalf("width=%d line=%d overflow=%d: %q", width, lineNo, lineWidth, stripAnsi(line))
			}
		}

		m.SetActionCallback(func(ProgressAction) {})
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		cancelling := m.View()
		if rows := lipgloss.Height(cancelling); rows > 7 {
			t.Fatalf("width=%d cancelling height=%d want <=7:\n%s", width, rows, stripAnsi(cancelling))
		}
		for lineNo, line := range strings.Split(cancelling, "\n") {
			if lineWidth := lipgloss.Width(line); lineWidth > width {
				t.Fatalf("width=%d cancelling line=%d overflow=%d: %q", width, lineNo, lineWidth, stripAnsi(line))
			}
		}
	}
}

func TestProgressCompletionUsesHonestFailureHeading(t *testing.T) {
	m := NewProgressModel(DefaultStyles())
	m.SetSize(80, 24)
	m.Start("Publish", 2)
	updated, _ := m.Update(ProgressCompleteMsg{TotalItems: 2, FailureCount: 2, Duration: -time.Second})
	got := stripAnsi(updated.View())
	if !strings.Contains(got, "Publish failed") || strings.Contains(got, "✓ Publish complete") {
		t.Fatalf("all-failed operation rendered as success:\n%s", got)
	}
	if updated.duration != 0 || updated.current != 2 {
		t.Fatalf("completion values not normalized: duration=%s current=%d", updated.duration, updated.current)
	}
}

func TestProgressActionsOnlyAdvertiseWiredControls(t *testing.T) {
	m := NewProgressModel(DefaultStyles())
	m.Start("Batch", 2)
	if got := stripAnsi(m.View()); strings.Contains(got, "Cancel") || strings.Contains(got, "Pause") || !strings.Contains(got, "Please wait") {
		t.Fatalf("unwired controls were advertised: %q", got)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if updated.isPaused {
		t.Fatal("unwired pause key changed state")
	}

	var action ProgressAction
	m.SetActionCallback(func(got ProgressAction) { action = got })
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if !updated.isPaused || action != ProgressActionPause {
		t.Fatalf("wired pause did not update state/callback: paused=%v action=%v", updated.isPaused, action)
	}
}

func TestProgressCancellationAcknowledgesOnceAndDisablesStaleActions(t *testing.T) {
	m := NewProgressModel(DefaultStyles())
	m.SetSize(80, 8)
	m.Start("Index workspace", 10)
	m.UpdateProgress(4, "large-file.go", "Scanning symbols")
	var actions []ProgressAction
	m.SetActionCallback(func(action ProgressAction) { actions = append(actions, action) })

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !updated.isCancelling || updated.isPaused || cmd == nil {
		t.Fatalf("first Esc did not enter cancelling state: cancelling=%v paused=%v cmd=%v", updated.isCancelling, updated.isPaused, cmd)
	}
	if len(actions) != 1 || actions[0] != ProgressActionCancel {
		t.Fatalf("cancel actions = %v, want one cancel", actions)
	}
	if msg, ok := cmd().(ProgressActionMsg); !ok || msg.Action != ProgressActionCancel {
		t.Fatalf("cancel command = %#v", msg)
	}
	view := stripAnsi(updated.View())
	for _, want := range []string{"Index workspace · cancelling", "Cancellation requested", "waiting for the operation to stop"} {
		if !strings.Contains(view, want) {
			t.Fatalf("cancelling view missing %q:\n%s", want, view)
		}
	}
	for _, stale := range []string{"Esc Cancel", "p Pause", "p Resume", "Now · large-file.go", "Scanning symbols"} {
		if strings.Contains(view, stale) {
			t.Fatalf("cancelling view still advertises stale state %q:\n%s", stale, view)
		}
	}

	updatedAgain, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil || len(actions) != 1 || !updatedAgain.isCancelling {
		t.Fatalf("repeated Esc re-fired cancellation: actions=%v cmd=%v cancelling=%v", actions, cmd, updatedAgain.isCancelling)
	}
	updatedAgain, _ = updatedAgain.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if updatedAgain.isPaused || len(actions) != 1 {
		t.Fatalf("pause remained active while cancelling: paused=%v actions=%v", updatedAgain.isPaused, actions)
	}

	completed, _ := updatedAgain.Update(ProgressCompleteMsg{TotalItems: 10, SuccessCount: 4})
	if !completed.isComplete || completed.isCancelling {
		t.Fatalf("completion did not settle cancelling state: complete=%v cancelling=%v", completed.isComplete, completed.isCancelling)
	}
	if got := stripAnsi(completed.renderActions()); !strings.Contains(got, "Enter/Esc Close") {
		t.Fatalf("completed footer = %q, want Enter/Esc Close", got)
	}
}

func TestCompletedBatchProgressClosesWithEveryAdvertisedExitKey(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
	} {
		m := NewProgressModel(DefaultStyles())
		m.Start("Index workspace", 2)
		m.Complete()
		cancelCalls := 0
		m.SetActionCallback(func(action ProgressAction) {
			if action == ProgressActionCancel {
				cancelCalls++
			}
		})

		updated, cmd := m.Update(key)
		if cmd == nil || !updated.isComplete || cancelCalls != 0 {
			t.Fatalf("key=%q close cmd=%v complete=%v cancelCalls=%d", key.String(), cmd, updated.isComplete, cancelCalls)
		}
		if _, ok := cmd().(CloseOverlayMsg); !ok {
			t.Fatalf("key=%q emitted %T, want CloseOverlayMsg", key.String(), cmd())
		}
	}
}

func TestBatchProgressIntegrationKeepsCompletionUntilExplicitClose(t *testing.T) {
	m := NewModel()
	m.state = StateProcessing
	m.applyResize(&tea.WindowSizeMsg{Width: 60, Height: 14})

	updated, _ := m.Update(ProgressUpdateMsg{Current: 1, Total: 3, CurrentItem: "one", Items: []ProgressItem{{Name: "one", Status: ProgressStatusInProgress}}})
	m2 := updated.(Model)
	if m2.state != StateBatchProgress || !m2.progressActive || m2.progressModel.total != 3 || len(m2.progressModel.items) != 1 {
		t.Fatalf("progress event did not open/populate overlay: state=%v active=%v model=%+v", m2.state, m2.progressActive, m2.progressModel)
	}

	updated, _ = m2.Update(ProgressCompleteMsg{TotalItems: 3, SuccessCount: 2, FailureCount: 1, Duration: time.Second})
	m3 := updated.(Model)
	if m3.state != StateBatchProgress || !m3.progressActive || !m3.progressModel.isComplete {
		t.Fatalf("completion disappeared before it could be read: state=%v active=%v complete=%v", m3.state, m3.progressActive, m3.progressModel.isComplete)
	}

	updated, cmd := m3.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m4 := updated.(Model)
	if cmd == nil {
		t.Fatal("Enter on completed progress must emit close command")
	}
	updated, _ = m4.Update(cmd())
	m5 := updated.(Model)
	if m5.state != StateProcessing || m5.progressActive {
		t.Fatalf("close did not restore prior state: state=%v active=%v", m5.state, m5.progressActive)
	}
}

func TestBatchProgressDoesNotClobberHigherPriorityModal(t *testing.T) {
	m := NewModel()
	m.state = StatePermissionPrompt
	updated, _ := m.Update(ProgressUpdateMsg{Current: 1, Total: 2})
	got := updated.(Model)
	if got.state != StatePermissionPrompt || !got.progressActive {
		t.Fatalf("background progress clobbered modal or was lost: state=%v active=%v", got.state, got.progressActive)
	}

	updated, _ = got.Update(CloseOverlayMsg{})
	got = updated.(Model)
	if got.state != StateBatchProgress {
		t.Fatalf("pending progress was not revealed after modal close: state=%v", got.state)
	}
}
