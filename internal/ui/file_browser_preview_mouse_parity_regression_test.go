package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// A visible split preview owns wheel scrolling just like the equivalent
// Search Results and Git Status preview panes. Changing the selected file here
// makes a long preview impossible to read with a mouse: every wheel tick loads
// a different file and resets the preview to its first line.
func TestFileBrowserWheelScrollsVisiblePreviewWithoutChangingSelection(t *testing.T) {
	dir := t.TempDir()
	for i := range 6 {
		content := strings.Repeat(fmt.Sprintf("file %d preview line\n", i), 60)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%02d.go", i)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	m := NewFileBrowserModel(DefaultStyles())
	m.SetSize(100, 14)
	if err := m.SetPath(dir); err != nil {
		t.Fatal(err)
	}
	m.previewEnabled = true
	m.updateLayout()

	selected := -1
	for i, entry := range m.entries {
		if entry.Name == "file-00.go" {
			selected = i
			break
		}
	}
	if selected < 0 {
		t.Fatalf("setup: target file missing from entries: %+v", m.entries)
	}
	m.selectEntry(selected)
	selectedPath := m.entries[m.selectedIndex].Path
	if !m.canScrollPreview() {
		t.Fatalf("setup: preview is not scrollable: lines=%d height=%d", m.previewViewport.TotalLineCount(), m.previewViewport.Height)
	}

	m, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})

	if got := m.entries[m.selectedIndex].Path; got != selectedPath {
		t.Fatalf("wheel over visible preview changed selected file: got=%q want=%q", got, selectedPath)
	}
	if m.previewViewport.YOffset == 0 {
		t.Fatal("wheel over visible preview did not scroll preview content")
	}
}
