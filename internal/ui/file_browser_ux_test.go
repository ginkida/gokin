package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestFileBrowserFilterEmptyStateAndActions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewFileBrowserModel(DefaultStyles())
	m.SetSize(100, 30)
	if err := m.SetPath(dir); err != nil {
		t.Fatal(err)
	}
	m.filter = "missing"
	m.filterInput = m.filter
	if err := m.loadEntries(); err != nil {
		t.Fatal(err)
	}

	out := renderToPlain(m.View())
	for _, want := range []string{`No files match "missing"`, "Press c to clear the filter", "c Clear", "Esc/q Close"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Space Select") {
		t.Fatalf("parent directory must not advertise selection:\n%s", out)
	}
}

func TestFileBrowserFilterModeShowsOnlyEditingActions(t *testing.T) {
	m := NewFileBrowserModel(DefaultStyles())
	m.currentDir = t.TempDir()
	m.filter = "go"
	m.filterActive = true
	m.filterInput = "go"
	m.updateViewport()

	out := renderToPlain(m.View())
	for _, want := range []string{"Type Filter", "Backspace Delete", "Enter/Esc Done"} {
		if !strings.Contains(out, want) {
			t.Errorf("filter footer missing %q:\n%s", want, out)
		}
	}
	for _, unavailable := range []string{"Open", "Select", "Preview", "Close"} {
		if strings.Contains(out, unavailable) {
			t.Errorf("filter footer advertises unavailable action %q:\n%s", unavailable, out)
		}
	}
}

func TestFileBrowserEditingFilterPreservesQueryAndUnicode(t *testing.T) {
	m := NewFileBrowserModel(DefaultStyles())
	m.currentDir = t.TempDir()
	m.filter = "файл"

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if !updated.filterActive || updated.filterInput != "файл" {
		t.Fatalf("starting filter edit lost query: active=%v input=%q", updated.filterActive, updated.filterInput)
	}

	updated, _ = updated.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if updated.filter != "фай" || updated.filterInput != "фай" {
		t.Fatalf("unicode backspace = filter %q input %q, want %q", updated.filter, updated.filterInput, "фай")
	}
}

func TestFileBrowserFilterAcceptsSpacesInFileNames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"project notes.txt", "project-notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	m := NewFileBrowserModel(DefaultStyles())
	if err := m.SetPath(dir); err != nil {
		t.Fatal(err)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("project")})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("notes")})

	if m.filter != "project notes" || m.filterInput != "project notes" {
		t.Fatalf("space-containing filter = %q input=%q", m.filter, m.filterInput)
	}
	matched := make([]string, 0, len(m.entries))
	for _, entry := range m.entries {
		if entry.Name != ".." {
			matched = append(matched, entry.Name)
		}
	}
	if len(matched) != 1 || matched[0] != "project notes.txt" {
		t.Fatalf("space filter matches = %v, want [project notes.txt]", matched)
	}
}

func TestFileBrowserPastedFilterIsSafeAndKeepsEditingTailVisible(t *testing.T) {
	m := NewFileBrowserModel(DefaultStyles())
	m.SetSize(24, 12)
	m.currentDir = t.TempDir()
	m.filterActive = true
	m.updateLayout()
	m.updateViewport()

	paste := "\x1b[31malpha\n\tbeta\x00" + strings.Repeat("界", 40)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(paste)})
	if strings.ContainsAny(m.filterInput, "\x1b\x00\n\r\t") {
		t.Fatalf("pasted filter retained terminal controls: %q", m.filterInput)
	}
	if !strings.HasPrefix(m.filterInput, "alpha  beta") {
		t.Fatalf("pasted whitespace was not normalized: %q", m.filterInput)
	}

	view := m.View()
	filterLine := ""
	for _, line := range strings.Split(renderToPlain(view), "\n") {
		if strings.Contains(line, "▊") {
			filterLine = line
			break
		}
	}
	if filterLine == "" || !strings.Contains(filterLine, "…") || !strings.HasSuffix(filterLine, "▊") {
		t.Fatalf("long active filter lost its safe tail/cursor: %q\n%s", filterLine, renderToPlain(view))
	}
	for row, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > 24 {
			t.Fatalf("row %d width=%d after pasted filter, want <=24: %q", row, got, renderToPlain(line))
		}
	}
}

func TestFileBrowserFilteringClampsSelectionAndClearsPreview(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	m := NewFileBrowserModel(DefaultStyles())
	if err := m.SetPath(dir); err != nil {
		t.Fatal(err)
	}
	m.previewEnabled = true
	m.selectedIndex = len(m.entries) - 1
	m.loadPreview(m.entries[m.selectedIndex])
	m.filter = "missing"
	if err := m.loadEntries(); err != nil {
		t.Fatal(err)
	}

	if m.selectedIndex < 0 || m.selectedIndex >= len(m.entries) {
		t.Fatalf("selection index %d outside %d filtered entries", m.selectedIndex, len(m.entries))
	}
	if m.previewFilePath != "" || m.previewContent != "" {
		t.Fatalf("stale preview remained after selected file was filtered out: path=%q", m.previewFilePath)
	}
}

func TestFileBrowserSelectedFilesAreSorted(t *testing.T) {
	m := NewFileBrowserModel(DefaultStyles())
	m.selectedFiles = map[string]bool{"z.go": true, "a.go": true, "m.go": true}

	got := m.GetSelectedFiles()
	want := []string{"a.go", "m.go", "z.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("GetSelectedFiles() = %v, want %v", got, want)
	}
}

func TestFileBrowserNavigationKeepsSelectionVisible(t *testing.T) {
	m := NewFileBrowserModel(DefaultStyles())
	m.viewport.Height = 3
	for i := 0; i < 10; i++ {
		m.entries = append(m.entries, FileEntry{Name: string(rune('a' + i)), Path: string(rune('a' + i))})
	}
	m.updateViewport()

	for range 6 {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.selectedIndex != 6 {
		t.Fatalf("selected index = %d, want 6", m.selectedIndex)
	}
	if m.selectedIndex < m.viewport.YOffset || m.selectedIndex >= m.viewport.YOffset+m.viewport.Height {
		t.Fatalf("selection %d is outside viewport [%d,%d)", m.selectedIndex, m.viewport.YOffset, m.viewport.YOffset+m.viewport.Height)
	}

	for range 5 {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	}
	if m.selectedIndex != 1 || m.viewport.YOffset > m.selectedIndex {
		t.Fatalf("up navigation left selection hidden: selection=%d offset=%d", m.selectedIndex, m.viewport.YOffset)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.selectedIndex != 4 {
		t.Fatalf("PgDn selected index = %d, want one visible page to 4", m.selectedIndex)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if m.selectedIndex != 9 || m.selectedIndex < m.viewport.YOffset {
		t.Fatalf("End did not reveal last entry: selection=%d offset=%d", m.selectedIndex, m.viewport.YOffset)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.selectedIndex != 6 {
		t.Fatalf("PgUp selected index = %d, want one visible page to 6", m.selectedIndex)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	if m.selectedIndex != 0 || m.viewport.YOffset != 0 {
		t.Fatalf("Home did not reveal first entry: selection=%d offset=%d", m.selectedIndex, m.viewport.YOffset)
	}
}

func TestFileBrowserFastNavigationRefreshesPreviewAndIsDiscoverable(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt", "f.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("content of "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	m := NewFileBrowserModel(DefaultStyles())
	m.SetSize(140, 14)
	if err := m.SetPath(dir); err != nil {
		t.Fatal(err)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if m.selectedIndex != len(m.entries)-1 || m.previewFilePath != m.entries[m.selectedIndex].Path {
		t.Fatalf("End left preview stale: selection=%d preview=%q entry=%q", m.selectedIndex, m.previewFilePath, m.entries[m.selectedIndex].Path)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	if m.selectedIndex != 0 || m.previewFilePath != "" {
		t.Fatalf("Home did not synchronize parent preview: selection=%d preview=%q", m.selectedIndex, m.previewFilePath)
	}

	view := renderToPlain(m.View())
	for _, want := range []string{"↑/↓ Move", "PgUp/PgDn Page", "Home/End Jump", "h/l Folder"} {
		if !strings.Contains(view, want) {
			t.Fatalf("file-browser footer hides navigation %q:\n%s", want, view)
		}
	}
}

func TestFileBrowserPreviewToggleReflowsPanelsWithinTerminal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewFileBrowserModel(DefaultStyles())
	m.SetSize(80, 24)
	if err := m.SetPath(dir); err != nil {
		t.Fatal(err)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})

	if !m.previewEnabled {
		t.Fatal("preview did not enable at a supported width")
	}
	if m.viewport.Width != m.listWidth || m.previewViewport.Width != m.previewWidth {
		t.Fatalf("preview toggle did not reflow viewports: list=%d/%d preview=%d/%d", m.viewport.Width, m.listWidth, m.previewViewport.Width, m.previewWidth)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if got := lipgloss.Width(line); got > 80 {
			t.Fatalf("rendered line width = %d, want <= 80: %q", got, renderToPlain(line))
		}
	}
	if got := renderedLineCount(m.View()); got > 24 {
		t.Fatalf("rendered height = %d, want <= 24", got)
	}
	if !strings.Contains(renderToPlain(m.View()), "p Hide preview") {
		t.Fatalf("enabled preview footer does not advertise its inverse action:\n%s", renderToPlain(m.View()))
	}
}

func TestFileBrowserRejectsPreviewThatCannotFit(t *testing.T) {
	m := NewFileBrowserModel(DefaultStyles())
	m.SetSize(40, 18)
	m.currentDir = t.TempDir()
	m.updateViewport()
	initial := renderToPlain(m.View())
	if strings.Contains(initial, "p Preview") || !strings.Contains(initial, "Preview needs 64 columns") {
		t.Fatalf("narrow footer advertises unavailable preview or hides resize recovery:\n%s", initial)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if m.previewEnabled {
		t.Fatal("preview enabled even though two panels cannot fit")
	}
	out := renderToPlain(m.View())
	if !strings.Contains(out, "Preview needs at least 64 columns") {
		t.Fatalf("narrow preview rejection lacks actionable feedback:\n%s", out)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if got := lipgloss.Width(line); got > 40 {
			t.Fatalf("narrow line width = %d, want <= 40: %q", got, renderToPlain(line))
		}
	}
	if got := renderedLineCount(m.View()); got > 18 {
		t.Fatalf("narrow rendered height = %d, want <= 18", got)
	}
}

func renderedLineCount(view string) int {
	view = strings.TrimSuffix(view, "\n")
	if view == "" {
		return 0
	}
	return strings.Count(view, "\n") + 1
}

func TestFileBrowserHiddenTogglePreservesSelectedEntry(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".hidden", "a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	m := NewFileBrowserModel(DefaultStyles())
	if err := m.SetPath(dir); err != nil {
		t.Fatal(err)
	}
	for i := range m.entries {
		if m.entries[i].Name == "b.txt" {
			m.selectedIndex = i
		}
	}
	selectedPath := m.entries[m.selectedIndex].Path
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'.'}})

	if got := m.entries[m.selectedIndex].Path; got != selectedPath {
		t.Fatalf("hidden toggle moved selection from %q to %q", selectedPath, got)
	}
}

func TestFileBrowserPreviewErrorKeepsFileContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary.dat")
	if err := os.WriteFile(path, []byte{0, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewFileBrowserModel(DefaultStyles())
	m.loadPreview(FileEntry{Name: "binary.dat", Path: path, Size: 3})
	if m.previewLoadError != "Binary file" || m.previewRecovery != "Enter Add to draft" || m.previewFilePath != path {
		t.Fatalf("preview error lost file context or recovery: error=%q recovery=%q path=%q", m.previewLoadError, m.previewRecovery, m.previewFilePath)
	}
}

func TestFileBrowserPreviewReadErrorRetriesInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "recover.txt")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewFileBrowserModel(DefaultStyles())
	m.SetSize(90, 24)
	if err := m.SetPath(dir); err != nil {
		t.Fatal(err)
	}
	for i := range m.entries {
		if m.entries[i].Path == path {
			m.selectedIndex = i
			break
		}
	}
	selectedPath := m.entries[m.selectedIndex].Path
	m.previewEnabled = true
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	m.loadPreview(m.entries[m.selectedIndex])
	if m.previewLoadError == "" || m.previewRecovery != "r Retry preview" {
		t.Fatalf("read failure lacks retry state: error=%q recovery=%q", m.previewLoadError, m.previewRecovery)
	}
	plain := stripAnsi(m.View())
	for _, want := range []string{"Cannot read", "r Retry preview", "r Refresh"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("preview failure missing %q:\n%s", want, plain)
		}
	}

	if err := os.WriteFile(path, []byte("recovered content"), 0o600); err != nil {
		t.Fatal(err)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if updated.previewLoadError != "" || updated.previewRecovery != "" {
		t.Fatalf("retry retained error state: error=%q recovery=%q", updated.previewLoadError, updated.previewRecovery)
	}
	if updated.entries[updated.selectedIndex].Path != selectedPath {
		t.Fatalf("retry moved selection from %q to %q", selectedPath, updated.entries[updated.selectedIndex].Path)
	}
	if plain := stripAnsi(updated.previewViewport.View()); !strings.Contains(plain, "recovered content") {
		t.Fatalf("retry did not load recovered content:\n%s", plain)
	}
}

func TestFileBrowserPreviewStripsTerminalControlsAndKeepsUnicode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "payload.txt")
	content := "first\n\x1b]52;c;clipboard-hijack\aпривет 🙂\nlast\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewFileBrowserModel(DefaultStyles())
	m.SetSize(90, 24)
	m.loadPreview(FileEntry{Name: "payload.txt", Path: path, Size: int64(len(content))})
	preview := m.previewViewport.View()
	plain := stripAnsi(preview)
	if strings.Contains(preview, "\x1b]") || strings.Contains(plain, "clipboard-hijack") {
		t.Fatalf("preview retained terminal control payload: %q", preview)
	}
	for _, want := range []string{"first", "привет 🙂", "last"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("safe preview lost %q: %q", want, plain)
		}
	}
}

func TestFileBrowserRowsPrioritizeFilenameTailAndSize(t *testing.T) {
	entry := FileEntry{
		Name:    strings.Repeat("🙂очень-длинное-имя", 6) + ".go",
		Path:    "/tmp/raw-action-path.go",
		Size:    12 * 1024,
		ModTime: "Jul 14 12:34",
	}
	m := NewFileBrowserModel(DefaultStyles())
	m.entries = []FileEntry{entry}
	m.selectedIndex = 0
	m.SetSize(40, 14)
	m.updateViewport()

	compact := stripAnsi(m.viewport.View())
	for _, want := range []string{"> ", ".go", "12.0K"} {
		if !strings.Contains(compact, want) {
			t.Fatalf("compact file row lost %q:\n%s", want, compact)
		}
	}
	if strings.Contains(compact, "Jul 14") {
		t.Fatalf("compact row kept low-priority timestamp at filename expense:\n%s", compact)
	}

	m.SetSize(100, 18)
	m.updateViewport()
	wide := stripAnsi(m.viewport.View())
	if !strings.Contains(wide, "Jul 14 12:34") {
		t.Fatalf("wide file row did not restore timestamp:\n%s", wide)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || cmd().(FileBrowserActionMsg).Path != entry.Path {
		t.Fatal("display truncation changed the raw open path")
	}
}

func TestFileBrowserRowSanitizesDisplayWithoutChangingOpenPath(t *testing.T) {
	rawPath := "/tmp/🙂\x1b]52;c;clipboard\aevil\nфайл.go"
	m := NewFileBrowserModel(DefaultStyles())
	m.entries = []FileEntry{{
		Name: "🙂\x1b]0;hijacked-title\aevil\nфайл.go", Path: rawPath, Size: 42, ModTime: "Jul 14\nforged row",
	}}
	m.selectedIndex = 0
	m.SetSize(80, 18)
	m.updateViewport()

	view := m.viewport.View()
	plain := stripAnsi(view)
	if strings.Contains(view, "\x1b]") || strings.Contains(plain, "hijacked-title") {
		t.Fatalf("file row retained terminal control payload: %q", view)
	}
	for _, forgedBreak := range []string{"evil\nфайл.go", "Jul 14\nforged row"} {
		if strings.Contains(plain, forgedBreak) {
			t.Fatalf("file row allowed forged line %q:\n%s", forgedBreak, plain)
		}
	}
	if !strings.Contains(plain, "🙂evil файл.go") {
		t.Fatalf("file row lost safe Unicode content:\n%s", plain)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || cmd().(FileBrowserActionMsg).Path != rawPath {
		t.Fatal("display sanitization changed raw open path")
	}
}
