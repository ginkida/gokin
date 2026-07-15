package ui

import (
	"fmt"
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

func TestProgressCompactListKeepsPriorityItemsAndHiddenSummary(t *testing.T) {
	m := NewProgressModel(DefaultStyles())
	m.SetSize(60, 10)
	m.Start("Index workspace", 8)
	m.UpdateProgress(4, "active.go", "Scanning symbols")
	m.SetActionCallback(func(ProgressAction) {})
	for i := 0; i < 8; i++ {
		status := ProgressStatusCompleted
		if i == 0 {
			status = ProgressStatusInProgress
		}
		if i == 7 {
			status = ProgressStatusFailed
		}
		errorText := ""
		if i == 7 {
			errorText = "compile failed"
		}
		m.AddItem(ProgressItem{Name: fmt.Sprintf("item-%d.go", i), Status: status, Error: errorText})
	}

	view := m.View()
	plain := stripAnsi(view)
	for _, want := range []string{"item-0.go", "item-7.go", "compile failed", "earlier or lower-priority", "Esc Cancel"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("compact progress lost %q:\n%s", want, plain)
		}
	}
	if got := lipgloss.Height(view); got > 10 {
		t.Fatalf("compact progress height=%d want <=10:\n%s", got, plain)
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
	if !completed.isComplete || completed.isCancelling || !completed.wasCancelled || completed.current != 4 {
		t.Fatalf("completion did not preserve cancelled state: complete=%v cancelling=%v cancelled=%v current=%d", completed.isComplete, completed.isCancelling, completed.wasCancelled, completed.current)
	}
	completedView := stripAnsi(completed.View())
	for _, want := range []string{"Index workspace cancelled", "4/10 · 40%", "Enter/Esc Close"} {
		if !strings.Contains(completedView, want) {
			t.Fatalf("cancelled completion missing %q:\n%s", want, completedView)
		}
	}
	for _, falseOutcome := range []string{"Index workspace complete", "10/10 · 100%"} {
		if strings.Contains(completedView, falseOutcome) {
			t.Fatalf("cancelled completion reported false outcome %q:\n%s", falseOutcome, completedView)
		}
	}
	if got := stripAnsi(completed.renderActions()); !strings.Contains(got, "Enter/Esc Close") {
		t.Fatalf("completed footer = %q, want Enter/Esc Close", got)
	}
}

func TestCompletedProgressIgnoresLateWorkerUpdates(t *testing.T) {
	m := NewProgressModel(DefaultStyles())
	m.Start("Index workspace", 5)
	m.UpdateProgress(5, "final.go", "Done")
	completed, _ := m.Update(ProgressCompleteMsg{TotalItems: 5, SuccessCount: 5, Duration: time.Second})

	late, _ := completed.Update(ProgressUpdateMsg{
		Current:     1,
		Total:       99,
		CurrentItem: "stale.go",
		Message:     "Still running",
		Items:       []ProgressItem{{Name: "stale.go", Status: ProgressStatusFailed}},
	})
	if late.total != 5 || late.current != 5 || late.successCount != 5 || late.failureCount != 0 || late.currentItem != "final.go" || late.message != "Done" {
		t.Fatalf("late update rewrote terminal result: %+v", late)
	}
	view := stripAnsi(late.View())
	if !strings.Contains(view, "Index workspace complete") || strings.Contains(view, "stale.go") || strings.Contains(view, "Still running") {
		t.Fatalf("late update leaked into completed view:\n%s", view)
	}
}

func TestCompletedProgressIgnoresDuplicateTerminalMessages(t *testing.T) {
	m := NewProgressModel(DefaultStyles())
	m.SetSize(80, 24)
	m.Start("Index workspace", 5)
	completed, _ := m.Update(ProgressCompleteMsg{
		TotalItems:   5,
		SuccessCount: 4,
		FailureCount: 1,
		Duration:     2 * time.Second,
	})

	duplicate, _ := completed.Update(ProgressCompleteMsg{
		TotalItems:   99,
		FailureCount: 99,
		Duration:     9 * time.Second,
	})
	if duplicate.total != 5 || duplicate.current != 5 || duplicate.successCount != 4 || duplicate.failureCount != 1 || duplicate.duration != 2*time.Second {
		t.Fatalf("duplicate completion rewrote terminal result: %+v", duplicate)
	}
	view := stripAnsi(duplicate.View())
	for _, want := range []string{"Index workspace completed with issues", "5/5 · 100%", "✓ 4", "✗ 1"} {
		if !strings.Contains(view, want) {
			t.Fatalf("preserved completion missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "99/99") || strings.Contains(view, "✗ 99") {
		t.Fatalf("duplicate completion leaked into terminal view:\n%s", view)
	}
}

func TestTerminalProgressHidesStaleRunningMessage(t *testing.T) {
	tests := []struct {
		name       string
		cancel     bool
		completion ProgressCompleteMsg
		heading    string
	}{
		{name: "success", completion: ProgressCompleteMsg{TotalItems: 3, SuccessCount: 3}, heading: "Publish complete"},
		{name: "failure", completion: ProgressCompleteMsg{TotalItems: 3, FailureCount: 3}, heading: "Publish failed"},
		{name: "cancelled", cancel: true, completion: ProgressCompleteMsg{TotalItems: 3, SuccessCount: 1}, heading: "Publish cancelled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewProgressModel(DefaultStyles())
			m.SetSize(80, 24)
			m.Start("Publish", 3)
			m.UpdateProgress(1, "draft.md", "Uploading draft")
			if tt.cancel {
				m.isCancelling = true
			}

			completed, _ := m.Update(tt.completion)
			view := stripAnsi(completed.View())
			if !strings.Contains(view, tt.heading) {
				t.Fatalf("terminal view missing heading %q:\n%s", tt.heading, view)
			}
			for _, stale := range []string{"Now · draft.md", "Uploading draft"} {
				if strings.Contains(view, stale) {
					t.Fatalf("terminal view retained stale running state %q:\n%s", stale, view)
				}
			}
		})
	}
}

func TestProgressCompletionReconcilesImpossibleTotals(t *testing.T) {
	m := NewProgressModel(DefaultStyles())
	m.Start("Batch", 2)
	completed, _ := m.Update(ProgressCompleteMsg{TotalItems: 2, SuccessCount: 2, FailureCount: 1})
	if completed.total != 3 || completed.current != 3 {
		t.Fatalf("completion kept impossible totals: current=%d total=%d", completed.current, completed.total)
	}
	if view := stripAnsi(completed.View()); !strings.Contains(view, "3/3 · 100%") {
		t.Fatalf("reconciled completion is not visible:\n%s", view)
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

	updated, _ = m5.Update(ProgressUpdateMsg{Current: 1, Total: 2, CurrentItem: "next-batch.go"})
	m6 := updated.(Model)
	if m6.state != StateBatchProgress || !m6.progressActive || m6.progressModel.isComplete || m6.progressModel.current != 1 || m6.progressModel.total != 2 || m6.progressModel.currentItem != "next-batch.go" {
		t.Fatalf("explicit close did not allow a new batch: state=%v active=%v model=%+v", m6.state, m6.progressActive, m6.progressModel)
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
