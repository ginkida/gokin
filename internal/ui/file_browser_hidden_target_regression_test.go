package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Positive geometry means the component knows what is actually visible. At a
// two-row height the tiny browser can render only its recovery footer, so Enter
// must not open the selected file whose identity is completely absent. The
// untouched 0x0 size remains the legacy/headless sentinel and must not disable
// standalone action-message consumers before Bubble Tea announces a size.
func TestFileBrowserHiddenTargetFailsClosedWithoutBreakingZeroSizeSentinel(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "hidden-target.go")
	if err := os.WriteFile(file, []byte("package hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tiny := NewFileBrowserModel(DefaultStyles())
	if err := tiny.SetPath(dir); err != nil {
		t.Fatal(err)
	}
	selectFileBrowserPath(t, &tiny, file)
	tiny.SetSize(20, 2)
	if view := stripAnsi(tiny.View()); strings.Contains(view, "hidden-target.go") {
		t.Fatalf("setup: target unexpectedly visible in two-row browser:\n%s", view)
	} else if !strings.Contains(view, "Resize") || !strings.Contains(view, "Esc/q") {
		t.Fatalf("hidden-target browser lost resize/recovery affordance:\n%s", view)
	}
	tinyCalls := 0
	tiny.SetActionCallback(func(FileBrowserAction, string, []string) { tinyCalls++ })
	_, tinyCmd := tiny.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if tinyCalls != 0 || tinyCmd != nil {
		t.Fatalf("Enter acted on hidden file: callbacks=%d command=%v", tinyCalls, tinyCmd != nil)
	}

	for _, size := range []struct{ width, height int }{
		{width: 20, height: 1},
		{width: 20, height: 3},
		{width: 20, height: 4},
		{width: minFileBrowserTargetWidth - 1, height: 12},
	} {
		for _, key := range []tea.KeyMsg{
			{Type: tea.KeyEnter},
			{Type: tea.KeySpace},
			{Type: tea.KeyRunes, Runes: []rune{'p'}},
			{Type: tea.KeyRunes, Runes: []rune{'y'}},
		} {
			m := NewFileBrowserModel(DefaultStyles())
			if err := m.SetPath(dir); err != nil {
				t.Fatal(err)
			}
			selectFileBrowserPath(t, &m, file)
			m.selectedFiles[file] = true // makes y a real terminal action
			m.SetSize(size.width, size.height)
			calls := 0
			m.SetActionCallback(func(FileBrowserAction, string, []string) { calls++ })
			updated, cmd := m.Update(key)
			if calls != 0 || cmd != nil || updated.previewEnabled != m.previewEnabled || len(updated.selectedFiles) != len(m.selectedFiles) {
				t.Errorf("%dx%d hidden key %q escaped gate: calls=%d cmd=%v preview=%v selection=%v", size.width, size.height, key.String(), calls, cmd != nil, updated.previewEnabled, updated.selectedFiles)
			}
		}
	}

	readable := NewFileBrowserModel(DefaultStyles())
	if err := readable.SetPath(dir); err != nil {
		t.Fatal(err)
	}
	selectFileBrowserPath(t, &readable, file)
	readable.SetSize(20, 5)
	if view := stripAnsi(readable.View()); !strings.Contains(view, "hidden-target") {
		t.Fatalf("readable boundary did not show a distinguishable target:\n%s", view)
	}
	readableCalls := 0
	readable.SetActionCallback(func(FileBrowserAction, string, []string) { readableCalls++ })
	_, readableCmd := readable.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if readableCalls != 1 || readableCmd == nil {
		t.Fatalf("readable target was over-blocked: callbacks=%d command=%v", readableCalls, readableCmd != nil)
	}

	sentinel := NewFileBrowserModel(DefaultStyles())
	if err := sentinel.SetPath(dir); err != nil {
		t.Fatal(err)
	}
	selectFileBrowserPath(t, &sentinel, file)
	sentinelCalls := 0
	sentinel.SetActionCallback(func(FileBrowserAction, string, []string) { sentinelCalls++ })
	_, sentinelCmd := sentinel.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if sentinelCalls != 1 || sentinelCmd == nil {
		t.Fatalf("0x0 sentinel blocked standalone Enter: callbacks=%d command=%v", sentinelCalls, sentinelCmd != nil)
	}
}
