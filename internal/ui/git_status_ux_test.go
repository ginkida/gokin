package ui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestGitStatusCleanWorkingTreeHasHonestEmptyState(t *testing.T) {
	m := NewGitStatusModel(DefaultStyles())
	m.SetSize(80, 24)
	m.SetStatus(nil, "main", "origin/main", "")

	view := stripAnsi(m.View())
	for _, want := range []string{"Working tree clean", "No staged or unstaged changes", "Esc/q Close"} {
		if !strings.Contains(view, want) {
			t.Fatalf("clean git status missing %q:\n%s", want, view)
		}
	}
	for _, unavailable := range []string{"Stage/Unstage", "Stage all", "Diff", "Commit", "Reset", "Navigate"} {
		if strings.Contains(view, unavailable) {
			t.Fatalf("clean git status advertises unavailable action %q:\n%s", unavailable, view)
		}
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if updated.showDiff {
		t.Fatal("d opened a meaningless diff for a clean working tree")
	}
}

func TestGitStatusNavigationFollowsRenderedSectionOrder(t *testing.T) {
	m := NewGitStatusModel(DefaultStyles())
	m.SetSize(80, 14)
	entries := []GitFileEntry{
		{FilePath: "unstaged-first.go", Status: GitFileModified},
		{FilePath: "staged-first.go", Status: GitFileStaged, IsStaged: true},
		{FilePath: "unstaged-second.go", Status: GitFileModified},
		{FilePath: "staged-second.go", Status: GitFileStaged, IsStaged: true},
	}
	m.SetStatus(entries, "main", "", "")

	if m.selectedIndex != 1 {
		t.Fatalf("initial selection index=%d, want first rendered entry index 1", m.selectedIndex)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.selectedIndex != 3 {
		t.Fatalf("down selected index=%d, want second staged entry index 3", m.selectedIndex)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.selectedIndex != 0 {
		t.Fatalf("down across section selected index=%d, want first unstaged entry index 0", m.selectedIndex)
	}
	row := m.entryRows[m.selectedIndex]
	if row < m.viewport.YOffset || row >= m.viewport.YOffset+m.viewport.Height {
		t.Fatalf("selected row %d is outside viewport [%d,%d)", row, m.viewport.YOffset, m.viewport.YOffset+m.viewport.Height)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if m.selectedIndex != 2 {
		t.Fatalf("End selected index=%d, want last rendered entry index 2", m.selectedIndex)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	if m.selectedIndex != 1 {
		t.Fatalf("Home selected index=%d, want first rendered entry index 1", m.selectedIndex)
	}
}

func TestGitStatusLongRowsKeepSelectedMarkerVisible(t *testing.T) {
	m := NewGitStatusModel(DefaultStyles())
	m.SetSize(40, 14)
	entries := make([]GitFileEntry, 10)
	for i := range entries {
		entries[i] = GitFileEntry{
			FilePath:  fmt.Sprintf("очень-длинный-каталог/%02d-%s.go", i, strings.Repeat("🙂имя", 12)),
			Status:    GitFileModified,
			DiffStats: "+123 -45",
		}
	}
	m.SetStatus(entries, "main", "", "")

	for step := 0; step < 8; step++ {
		plain := stripAnsi(m.viewport.View())
		if !strings.Contains(plain, "> ") {
			t.Fatalf("selected marker disappeared at index=%d offset=%d:\n%s", m.selectedIndex, m.viewport.YOffset, plain)
		}
		if !strings.Contains(plain, "+123 -45") {
			t.Fatalf("selected row lost diff stats at index=%d:\n%s", m.selectedIndex, plain)
		}
		if !strings.Contains(plain, ".go +123 -45") {
			t.Fatalf("selected row lost filename tail at index=%d:\n%s", m.selectedIndex, plain)
		}
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
}

func TestGitStatusWideRowKeepsRenameContext(t *testing.T) {
	m := NewGitStatusModel(DefaultStyles())
	m.SetSize(100, 18)
	m.SetStatus([]GitFileEntry{{
		FilePath: "internal/ui/new_name.go", OldPath: "internal/ui/old_name.go",
		Status: GitFileRenamed, DiffStats: "+12 -3",
	}}, "main", "", "")
	plain := stripAnsi(m.viewport.View())
	for _, want := range []string{"new_name.go", "+12 -3", "(from old_name.go)"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("wide git row lost %q:\n%s", want, plain)
		}
	}
}

func TestGitStatusMultiSelectionDrivesGroupedActions(t *testing.T) {
	m := NewGitStatusModel(DefaultStyles())
	m.SetSize(90, 24)
	m.SetStatus([]GitFileEntry{
		{FilePath: "staged.go", Status: GitFileStaged, IsStaged: true},
		{FilePath: "one.go", Status: GitFileModified},
		{FilePath: "two.go", Status: GitFileModified},
	}, "main", "", "")
	m.selectedIndices = map[int]bool{1: true, 2: true}
	m.selectedIndex = 1
	m.updateViewport()

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if cmd == nil {
		t.Fatal("Space did not emit a grouped stage action")
	}
	msg, ok := cmd().(GitStatusActionMsg)
	if !ok {
		t.Fatalf("Space emitted %T, want GitStatusActionMsg", cmd())
	}
	if msg.Action != GitActionStage || !reflect.DeepEqual(msg.Files, []string{"one.go", "two.go"}) {
		t.Fatalf("grouped stage = action %v files %v", msg.Action, msg.Files)
	}

	updated.selectedIndex = 2
	updated.selectedIndices = map[int]bool{0: true, 2: true}
	_, cmd = updated.Update(tea.KeyMsg{Type: tea.KeySpace})
	msg = cmd().(GitStatusActionMsg)
	if !reflect.DeepEqual(msg.Files, []string{"two.go"}) {
		t.Fatalf("mixed selection should stage only unstaged files, got %v", msg.Files)
	}

	files := updated.GetSelectedFiles()
	if !reflect.DeepEqual(files, []string{"staged.go", "two.go"}) {
		t.Fatalf("GetSelectedFiles()=%v, want rendered-order selection", files)
	}
}

func TestGitStatusResetRequiresExplicitConfirmation(t *testing.T) {
	m := NewGitStatusModel(DefaultStyles())
	m.SetSize(90, 24)
	m.SetStatus([]GitFileEntry{
		{FilePath: "one.go", Status: GitFileModified},
		{FilePath: "two.go", Status: GitFileModified},
	}, "main", "", "")
	m.selectedIndices = map[int]bool{0: true, 1: true}
	m.updateViewport()
	callbackCalls := 0
	m.SetActionCallback(func(action GitAction, files []string, _ string) {
		callbackCalls++
		if action != GitActionReset || !reflect.DeepEqual(files, []string{"one.go", "two.go"}) {
			t.Fatalf("reset callback=%v files=%v", action, files)
		}
	})

	armed, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd != nil || !armed.confirmReset || callbackCalls != 0 {
		t.Fatalf("first r emitted reset or failed to arm confirmation: cmd=%v armed=%v calls=%d", cmd, armed.confirmReset, callbackCalls)
	}
	if !reflect.DeepEqual(armed.pendingResetFiles, []string{"one.go", "two.go"}) {
		t.Fatalf("confirmation snapshot=%v", armed.pendingResetFiles)
	}
	plain := stripAnsi(armed.View())
	for _, want := range []string{"Local changes may be lost", "Enter Confirm reset", "Esc/q Cancel"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("reset confirmation missing %q:\n%s", want, plain)
		}
	}
	armed.SetSize(40, 18)
	compactView := armed.View()
	for _, line := range strings.Split(compactView, "\n") {
		if got := lipgloss.Width(line); got > 40 {
			t.Fatalf("narrow reset confirmation line width=%d: %q", got, stripAnsi(line))
		}
	}
	if got := renderedLineCount(compactView); got > 18 {
		t.Fatalf("narrow reset confirmation height=%d, want <=18", got)
	}

	blocked, cmd := armed.Update(tea.KeyMsg{Type: tea.KeySpace})
	if cmd != nil || !blocked.confirmReset {
		t.Fatalf("unrelated action escaped confirmation: cmd=%v armed=%v", cmd, blocked.confirmReset)
	}

	// The confirmation owns an immutable snapshot even if selection state is
	// mutated before the second step completes.
	blocked.selectedIndices = map[int]bool{1: true}
	confirmed, cmd := blocked.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || confirmed.confirmReset || confirmed.pendingResetFiles != nil {
		t.Fatalf("Enter did not finish confirmation: cmd=%v armed=%v files=%v", cmd, confirmed.confirmReset, confirmed.pendingResetFiles)
	}
	msg := cmd().(GitStatusActionMsg)
	if msg.Action != GitActionReset || !reflect.DeepEqual(msg.Files, []string{"one.go", "two.go"}) {
		t.Fatalf("confirmed reset=%v files=%v", msg.Action, msg.Files)
	}
	if callbackCalls != 1 {
		t.Fatalf("confirmed reset callback calls=%d, want 1", callbackCalls)
	}
}

func TestGitStatusDiffErrorRetriesInPlace(t *testing.T) {
	m := NewGitStatusModel(DefaultStyles())
	m.SetSize(90, 24)
	m.SetStatus([]GitFileEntry{{FilePath: "main.go", Status: GitFileModified}}, "main", "", "")
	m.showDiff = true
	m.SetDiffError("Unable to load diff · temporary failure")

	plain := stripAnsi(m.View())
	for _, want := range []string{"Unable to load diff", "Press d to retry", "d Retry diff"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("diff failure missing %q:\n%s", want, plain)
		}
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if cmd == nil || !updated.showDiff || updated.diffLoadError || updated.pendingDiffPath != "main.go" {
		t.Fatalf("retry did not preserve/reload preview: cmd=%v show=%v error=%v pending=%q",
			cmd, updated.showDiff, updated.diffLoadError, updated.pendingDiffPath)
	}
	msg := cmd().(GitStatusActionMsg)
	if msg.Action != GitActionDiff || !reflect.DeepEqual(msg.Files, []string{"main.go"}) {
		t.Fatalf("retry request=%v files=%v", msg.Action, msg.Files)
	}
	if loading := stripAnsi(updated.diffViewport.View()); !strings.Contains(loading, "Loading diff") {
		t.Fatalf("retry lacks loading feedback:\n%s", loading)
	}

	updated.pendingDiffPath = ""
	updated.SetDiff("@@ -1 +1 @@\n-old\n+new")
	if updated.diffLoadError || strings.Contains(stripAnsi(updated.diffViewport.View()), "retry") {
		t.Fatal("successful retry retained error guidance")
	}
}

func TestGitStatusNarrowDiffKeepsSelectedFileContext(t *testing.T) {
	m := NewGitStatusModel(DefaultStyles())
	m.SetSize(50, 18)
	m.SetStatus([]GitFileEntry{
		{FilePath: "internal/one.go", Status: GitFileModified},
		{FilePath: "internal/two.go", Status: GitFileModified},
	}, "main", "", "")
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if cmd == nil {
		t.Fatal("opening narrow diff did not request content")
	}
	m.SetDiff("@@ -1 +1 @@\n-old\n+new")
	plain := stripAnsi(m.View())
	for _, want := range []string{"Diff · 1/2 · internal/one.go", "+new"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("narrow diff missing %q:\n%s", want, plain)
		}
	}

	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if cmd == nil {
		t.Fatal("navigating narrow diff did not request the next file")
	}
	plain = stripAnsi(m.View())
	if !strings.Contains(plain, "Diff · 2/2 · internal/two.go") || !strings.Contains(plain, "Loading diff") {
		t.Fatalf("narrow diff navigation lacks visible context/loading state:\n%s", plain)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if got := lipgloss.Width(line); got > 50 {
			t.Fatalf("narrow diff row width=%d: %q", got, stripAnsi(line))
		}
	}
	if got := renderedLineCount(m.View()); got > 18 {
		t.Fatalf("narrow diff height=%d, want <=18", got)
	}
}

func TestGitStatusResetConfirmationCancelsWithoutClosing(t *testing.T) {
	m := NewGitStatusModel(DefaultStyles())
	m.SetSize(80, 24)
	m.SetStatus([]GitFileEntry{{FilePath: "one.go", Status: GitFileModified}}, "main", "", "")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	cancelled, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil || cancelled.confirmReset || cancelled.pendingResetFiles != nil {
		t.Fatalf("Esc did not locally cancel reset: cmd=%v armed=%v files=%v", cmd, cancelled.confirmReset, cancelled.pendingResetFiles)
	}

	m = NewGitStatusModel(DefaultStyles())
	m.SetSize(80, 24)
	m.SetStatus([]GitFileEntry{{FilePath: "one.go", Status: GitFileModified}}, "main", "", "")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	cancelled, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd != nil || cancelled.confirmReset {
		t.Fatalf("q closed the overlay instead of cancelling confirmation: cmd=%v armed=%v", cmd, cancelled.confirmReset)
	}
}

func TestGitStatusRefreshClearsResetConfirmation(t *testing.T) {
	m := NewGitStatusModel(DefaultStyles())
	m.SetStatus([]GitFileEntry{{FilePath: "old.go", Status: GitFileModified}}, "main", "", "")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m.SetStatus([]GitFileEntry{{FilePath: "new.go", Status: GitFileModified}}, "main", "", "")
	if m.confirmReset || m.pendingResetFiles != nil {
		t.Fatalf("status refresh retained stale reset confirmation: armed=%v files=%v", m.confirmReset, m.pendingResetFiles)
	}
}

func TestGitStatusLongListAndRefreshClearTransientState(t *testing.T) {
	m := NewGitStatusModel(DefaultStyles())
	m.SetSize(80, 14)
	entries := make([]GitFileEntry, 20)
	for i := range entries {
		entries[i] = GitFileEntry{FilePath: fmt.Sprintf("file-%02d.go", i), Status: GitFileModified}
	}
	m.SetStatus(entries, "main", "", "")
	for range 12 {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	row := m.entryRows[m.selectedIndex]
	if row < m.viewport.YOffset || row >= m.viewport.YOffset+m.viewport.Height {
		t.Fatalf("long-list selection row %d is outside viewport [%d,%d)", row, m.viewport.YOffset, m.viewport.YOffset+m.viewport.Height)
	}

	m.showDiff = true
	m.SetDiff("stale diff")
	m.selectedIndices[12] = true
	m.SetStatus(nil, "main", "", "")
	if m.showDiff || len(m.selectedIndices) != 0 || m.viewport.YOffset != 0 {
		t.Fatalf("refresh retained transient state: diff=%v selected=%v offset=%d", m.showDiff, m.selectedIndices, m.viewport.YOffset)
	}
	if strings.Contains(stripAnsi(m.diffViewport.View()), "stale diff") {
		t.Fatal("refresh retained stale diff content")
	}
}

func TestGitStatusDiffRequestsUseActionMessageWithoutCallback(t *testing.T) {
	m := NewGitStatusModel(DefaultStyles())
	m.SetSize(90, 24)
	m.SetStatus([]GitFileEntry{
		{FilePath: "one.go", Status: GitFileModified},
		{FilePath: "two.go", Status: GitFileModified},
	}, "main", "", "")

	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if cmd == nil {
		t.Fatal("opening diff did not emit a request without a local callback")
	}
	msg, ok := cmd().(GitStatusActionMsg)
	if !ok || msg.Action != GitActionDiff || !reflect.DeepEqual(msg.Files, []string{"one.go"}) {
		t.Fatalf("opening diff emitted %#v, want diff request for one.go", msg)
	}
	if got := stripAnsi(m.diffViewport.View()); !strings.Contains(got, "Loading diff") {
		t.Fatalf("diff request lacks loading feedback: %q", got)
	}

	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if cmd == nil {
		t.Fatal("moving in an open diff did not request the newly selected file")
	}
	msg = cmd().(GitStatusActionMsg)
	if !reflect.DeepEqual(msg.Files, []string{"two.go"}) {
		t.Fatalf("diff navigation requested %v, want two.go", msg.Files)
	}
}

func TestGitStatusCopiesStatusSnapshot(t *testing.T) {
	entries := []GitFileEntry{{FilePath: "original.go", Status: GitFileModified}}
	m := NewGitStatusModel(DefaultStyles())
	m.SetStatus(entries, "main", "", "")

	entries[0].FilePath = "mutated.go"
	entries[0].IsStaged = true
	if got := m.entries[0]; got.FilePath != "original.go" || got.IsStaged {
		t.Fatalf("status changed through caller-owned slice: %+v", got)
	}
}

func TestGitStatusSanitizeDisplayWithoutChangingActionPath(t *testing.T) {
	rawPath := "путь/🙂\x1b]0;hijacked-title\aevil\nфайл.go"
	m := NewGitStatusModel(DefaultStyles())
	m.SetSize(90, 24)
	m.SetStatus([]GitFileEntry{{
		FilePath:  rawPath,
		Status:    GitFileModified,
		DiffStats: "\x1b]52;c;clipboard\a+2\nforged stat",
		OldPath:   "old/\x1b]0;old-title\alegacy\nname.go",
	}}, "\x1b]0;branch-title\amain\nforged branch", "origin/main\nforged upstream", "1 ahead\nforged count")

	view := m.View()
	plain := stripAnsi(view)
	if strings.Contains(view, "\x1b]") || strings.Contains(plain, "hijacked-title") || strings.Contains(plain, "clipboard") {
		t.Fatalf("git status retained terminal control payload: %q", view)
	}
	if !strings.Contains(plain, "🙂evil файл.go") {
		t.Fatalf("git status lost safe Unicode path content:\n%s", plain)
	}
	for _, forgedBreak := range []string{"evil\nфайл.go", "+2\nforged stat", "main\nforged branch"} {
		if strings.Contains(plain, forgedBreak) {
			t.Fatalf("git status allowed a forged row via %q:\n%s", forgedBreak, plain)
		}
	}
	if m.branch != "main forged branch" || m.upstream != "origin/main forged upstream" {
		t.Fatalf("git metadata was not normalized: branch=%q upstream=%q", m.branch, m.upstream)
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if cmd == nil {
		t.Fatal("Space did not emit a stage action")
	}
	msg := cmd().(GitStatusActionMsg)
	if !reflect.DeepEqual(msg.Files, []string{rawPath}) {
		t.Fatalf("display sanitization changed action payload: %q", msg.Files)
	}

	m.showDiff = true
	m.SetSize(90, 24)
	m.SetDiff("\x1b]0;diff-title\a-old\n+new")
	diff := m.diffViewport.View()
	if strings.Contains(diff, "\x1b]") || strings.Contains(diff, "diff-title") || !strings.Contains(diff, "+new") {
		t.Fatalf("diff content was not safely preserved: %q", diff)
	}
}

func TestGitStatusDiffUsesSinglePanelOnNarrowTerminal(t *testing.T) {
	m := NewGitStatusModel(DefaultStyles())
	m.SetSize(40, 18)
	m.SetStatus([]GitFileEntry{{
		FilePath: "deeply/nested/path/with-a-long-name.go",
		Status:   GitFileModified,
	}}, "feature/a-very-long-branch-name", "origin/feature/a-very-long-branch-name", "12 ahead, 4 behind")

	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if cmd == nil || !m.diffVisible() {
		t.Fatal("narrow terminal did not open the diff panel")
	}
	view := m.View()
	for _, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > 40 {
			t.Fatalf("narrow git status line width = %d, want <= 40: %q", got, stripAnsi(line))
		}
	}
	if got := renderedLineCount(view); got > 18 {
		t.Fatalf("narrow git status height = %d, want <= 18", got)
	}
	if boxes := strings.Count(stripAnsi(view), "╭"); boxes != 1 {
		t.Fatalf("narrow diff rendered %d panels, want one:\n%s", boxes, stripAnsi(view))
	}
}

func TestGitStatusSplitPanelsFitWideTerminal(t *testing.T) {
	m := NewGitStatusModel(DefaultStyles())
	m.SetSize(80, 24)
	m.SetStatus([]GitFileEntry{{FilePath: "main.go", Status: GitFileModified}}, "main", "", "")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})

	for _, line := range strings.Split(m.View(), "\n") {
		if got := lipgloss.Width(line); got > 80 {
			t.Fatalf("split git status line width = %d, want <= 80: %q", got, stripAnsi(line))
		}
	}
}
