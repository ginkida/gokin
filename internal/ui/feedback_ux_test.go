package ui

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestToastUsesTitleCollapsesLinesAndFitsCells(t *testing.T) {
	manager := NewToastManager(DefaultStyles())
	toast := Toast{
		Type:      ToastError,
		Title:     " API  Error ",
		Message:   "请求失败\n  retry later",
		Duration:  time.Minute,
		CreatedAt: time.Now(),
	}

	for _, width := range []int{1, 8, 20, 32} {
		view := manager.renderToast(toast, width)
		if strings.Contains(stripAnsi(view), "\n") {
			t.Fatalf("width=%d toast was not normalized to one line: %q", width, stripAnsi(view))
		}
		if got := lipgloss.Width(view); got > width {
			t.Fatalf("width=%d toast rendered %d cells: %q", width, got, stripAnsi(view))
		}
	}
	wide := stripAnsi(manager.renderToast(toast, 80))
	if !strings.Contains(wide, "API Error: 请求失败 retry later") {
		t.Fatalf("toast discarded title or whitespace normalization: %q", wide)
	}
}

func TestToastFallbacksNeverRenderBlank(t *testing.T) {
	manager := NewToastManager(DefaultStyles())
	toast := Toast{Type: ToastType(99), Duration: time.Minute, CreatedAt: time.Now()}
	view := stripAnsi(manager.renderToast(toast, 30))
	if !strings.Contains(view, "Notification") || strings.TrimSpace(view) == "" {
		t.Fatalf("unknown empty toast fallback=%q", view)
	}
}

func TestToastCoalescesDuplicateFeedbackAndRefreshesLifetime(t *testing.T) {
	manager := NewToastManager(DefaultStyles())
	manager.Show(ToastError, " Error ", "request\nfailed", 0)
	if manager.Count() != 1 || manager.toasts[0].Duration != 15*time.Second {
		t.Fatalf("initial toast was not normalized: %+v", manager.toasts)
	}
	id := manager.toasts[0].ID
	manager.toasts[0].CreatedAt = time.Now().Add(-time.Minute)
	manager.Show(ToastError, "Error", "request failed", 2*time.Second)

	if manager.Count() != 1 {
		t.Fatalf("duplicate feedback stacked %d toasts, want 1", manager.Count())
	}
	toast := manager.toasts[0]
	if toast.ID != id || toast.Duration != 2*time.Second || time.Since(toast.CreatedAt) > time.Second {
		t.Fatalf("duplicate feedback did not refresh in place: %+v", toast)
	}
}

func TestToastTaggedUpdatePromotesAndRemovalArchives(t *testing.T) {
	manager := NewToastManager(DefaultStyles())
	manager.maxToasts = 2
	manager.ShowTagged("retry", ToastWarning, "Attempt 1", time.Second)
	retryID := manager.toasts[0].ID
	manager.ShowInfo("Connected")
	manager.ShowTagged("retry", ToastError, "Attempt 2", 0)

	if manager.toasts[0].ID != retryID || manager.toasts[0].Message != "Attempt 2" || manager.toasts[0].Type != ToastError {
		t.Fatalf("tagged update was not promoted in place: %+v", manager.toasts)
	}
	manager.ShowInfo("Ready")
	if manager.Count() != 2 || manager.toasts[1].Type != ToastError {
		t.Fatalf("limit eviction did not preserve the error toast: %+v", manager.toasts)
	}
	if history := manager.History(); len(history) != 1 || history[0].Message != "Connected" {
		t.Fatalf("evicted toast was not archived: %+v", history)
	}

	manager.Dismiss(manager.toasts[0].ID)
	if history := manager.History(); len(history) != 2 || history[0].Message != "Ready" {
		t.Fatalf("dismissed toast was not archived newest-first: %+v", history)
	}
}

func TestToastExpirationHistoryRemainsNewestFirst(t *testing.T) {
	manager := NewToastManager(DefaultStyles())
	manager.ShowInfo("older")
	manager.ShowInfo("newer")
	for i := range manager.toasts {
		manager.toasts[i].CreatedAt = time.Now().Add(-time.Minute)
	}
	manager.Update()

	history := manager.History()
	if manager.Count() != 0 || len(history) != 2 || history[0].Message != "newer" || history[1].Message != "older" {
		t.Fatalf("expiration history order=%+v, want newer then older", history)
	}
}

func TestToolProgressBarClampsStateAndFitsEveryRow(t *testing.T) {
	bar := NewToolProgressBarModel(DefaultStyles())
	bar.Update(ToolProgressMsg{
		Name:           "download",
		Elapsed:        -time.Second,
		Progress:       1.75,
		CurrentStep:    strings.Repeat("处理文件", 30) + "\n" + strings.Repeat("second line ", 20),
		TotalBytes:     100,
		ProcessedBytes: 250,
		Cancellable:    true,
	})
	if bar.progress != 1 || bar.processedBytes != 100 || bar.elapsed != 0 {
		t.Fatalf("progress state not normalized: progress=%v bytes=%d elapsed=%v", bar.progress, bar.processedBytes, bar.elapsed)
	}

	for _, width := range []int{5, 10, 20, 40, 80} {
		view := bar.View(width)
		for row, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("width=%d row=%d rendered %d cells:\n%s", width, row, got, stripAnsi(view))
			}
		}
		if width >= 10 && !strings.Contains(stripAnsi(view), "Esc cancel") {
			t.Fatalf("width=%d hid the actionable cancel hint: %q", width, stripAnsi(view))
		}
	}
	if view := stripAnsi(bar.View(80)); strings.Contains(view, "175%") || strings.Contains(view, "250 B/100 B") {
		t.Fatalf("progress rendered values beyond their bounds: %q", view)
	}
}

func TestToolProgressBarNewOperationClearsTransientState(t *testing.T) {
	bar := NewToolProgressBarModel(DefaultStyles())
	bar.Update(ToolProgressMsg{Name: "old", Progress: .5, TotalBytes: 1000, ProcessedBytes: 500, Cancellable: true})
	bar.Show("new")
	if bar.totalBytes != 0 || bar.processedBytes != 0 || bar.cancellable {
		t.Fatalf("Show retained previous operation state: total=%d processed=%d cancellable=%v", bar.totalBytes, bar.processedBytes, bar.cancellable)
	}
	if view := stripAnsi(bar.View(80)); strings.Contains(view, "Esc cancel") || strings.Contains(view, "500") {
		t.Fatalf("new operation view retained stale detail: %q", view)
	}

	bar.Update(ToolProgressMsg{Name: "", Progress: math.NaN()})
	if bar.progress != -1 || !strings.Contains(stripAnsi(bar.View(80)), "New") {
		t.Fatalf("empty-name/NaN update corrupted active operation: progress=%v view=%q", bar.progress, stripAnsi(bar.View(80)))
	}
}

func TestErrorDisplayUsesWidthContextAndNilFallback(t *testing.T) {
	enhanced := &EnhancedError{
		OriginalError: errors.New("provider returned\n a very long authentication failure 请求失败"),
		Category:      ErrorCategoryAuth,
		Context:       "refreshing credentials for the selected provider",
		Suggestions:   []string{"Run setup and verify the configured API key", "Second suggestion"},
		RelatedFiles:  []string{"/very/long/path/to/configuration.yaml"},
		Documentation: "https://example.invalid/very/long/documentation/path",
	}
	model := NewErrorDisplayModel(enhanced)
	model.SetWidth(32)
	model.SetExpanded(true)
	view := model.View()
	plain := stripAnsi(view)
	if !strings.Contains(plain, "While:") {
		t.Fatalf("error context was discarded:\n%s", plain)
	}
	for row, line := range strings.Split(strings.TrimSuffix(view, "\n"), "\n") {
		if got := lipgloss.Width(line); got > 32 {
			t.Fatalf("error row %d width=%d, want <=32:\n%s", row, got, plain)
		}
	}
	if got := lipgloss.Width(model.ViewCompact()); got > 32 {
		t.Fatalf("compact error width=%d, want <=32: %q", got, stripAnsi(model.ViewCompact()))
	}

	nilError := NewErrorDisplayModel(&EnhancedError{Category: ErrorCategoryUnknown})
	if got := stripAnsi(nilError.View()); !strings.Contains(got, "Unknown error") {
		t.Fatalf("nil OriginalError fallback=%q", got)
	}
}

func TestErrorDisplaySnapshotsUsefulFeedbackAndShowsRetryReason(t *testing.T) {
	suggestions := []string{" ", "Check credentials"}
	files := []string{"", "/tmp/config.yaml"}
	retry := &RetryInfo{
		AttemptNumber: 1,
		MaxAttempts:   3,
		NextRetryIn:   250 * time.Millisecond,
		CanRetry:      true,
		RetryReason:   " temporary provider failure ",
	}
	enhanced := &EnhancedError{
		OriginalError: errors.New("request failed"),
		Category:      ErrorCategoryAPI,
		Suggestions:   suggestions,
		RelatedFiles:  files,
		RetryInfo:     retry,
	}
	model := NewErrorDisplayModel(enhanced)
	model.SetWidth(120)
	model.SetExpanded(true)

	suggestions[1] = "mutated"
	files[1] = "mutated"
	retry.RetryReason = "mutated"
	plain := stripAnsi(model.View())
	for _, want := range []string{"Check credentials", "Retry 1/3", "temporary provider failure", "next in <1s", "/tmp/config.yaml"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("error feedback missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "mutated") {
		t.Fatalf("error display changed through caller-owned state:\n%s", plain)
	}
}

func TestTruncateForWidthUsesTerminalCells(t *testing.T) {
	for _, tc := range []struct {
		input string
		width int
	}{
		{"abcdef", 4},
		{"请求失败", 5},
		{"🙂🙂🙂", 5},
		{"anything", 1},
		{"anything", 0},
	} {
		got := truncateForWidth(tc.input, tc.width)
		if width := lipgloss.Width(got); width > tc.width {
			t.Fatalf("truncateForWidth(%q,%d) rendered %d cells: %q", tc.input, tc.width, width, got)
		}
	}
}
