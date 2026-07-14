package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestWorkspaceOverlaysRestoreLiveUnderlyingState(t *testing.T) {
	tests := []struct {
		name      string
		open      func(Model) Model
		close     func(Model) Model
		wantState State
	}{
		{
			name: "search results",
			open: func(m Model) Model {
				updated, _ := m.Update(SearchResultsRequestMsg{Query: "needle", Tool: "grep"})
				return updated.(Model)
			},
			close: func(m Model) Model {
				updated, _ := m.Update(SearchResultsActionMsg{Action: SearchActionClose})
				return updated.(Model)
			},
			wantState: StateSearchResults,
		},
		{
			name: "git status",
			open: func(m Model) Model {
				updated, _ := m.Update(GitStatusRequestMsg{Branch: "main"})
				return updated.(Model)
			},
			close: func(m Model) Model {
				updated, _ := m.Update(GitStatusActionMsg{Action: GitActionClose})
				return updated.(Model)
			},
			wantState: StateGitStatus,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name+" follows stream", func(t *testing.T) {
			m := *NewModel()
			m.state = StateProcessing
			overlay := tc.open(m)
			if overlay.state != tc.wantState || overlay.workspaceOverlayReturnState != StateProcessing {
				t.Fatalf("open state=%v return=%v", overlay.state, overlay.workspaceOverlayReturnState)
			}
			updated, _ := overlay.Update(StreamTextMsg("still working"))
			overlay = updated.(Model)
			if overlay.state != tc.wantState || overlay.workspaceOverlayReturnState != StateStreaming {
				t.Fatalf("stream state=%v return=%v", overlay.state, overlay.workspaceOverlayReturnState)
			}
			closed := tc.close(overlay)
			if closed.state != StateStreaming || closed.workspaceOverlayReturnState != StateInput {
				t.Fatalf("close state=%v retained return=%v", closed.state, closed.workspaceOverlayReturnState)
			}
		})

		t.Run(tc.name+" follows completion", func(t *testing.T) {
			m := *NewModel()
			m.state = StateProcessing
			overlay := tc.open(m)
			updated, _ := overlay.Update(ResponseDoneMsg{})
			overlay = updated.(Model)
			if overlay.state != tc.wantState || overlay.workspaceOverlayReturnState != StateInput {
				t.Fatalf("completion state=%v return=%v", overlay.state, overlay.workspaceOverlayReturnState)
			}
			if closed := tc.close(overlay); closed.state != StateInput {
				t.Fatalf("close after completion state=%v, want input", closed.state)
			}
		})
	}
}

func TestWorkspaceOverlayActionRestoresStateAndStillDispatches(t *testing.T) {
	m := NewModel()
	m.state = StateProcessing
	called := SearchActionNone
	m.SetSearchActionCallback(func(action SearchAction) { called = action })
	updated, _ := m.Update(SearchResultsRequestMsg{Query: "needle", Tool: "grep"})
	overlay := updated.(Model)
	updated, _ = overlay.Update(StreamTextMsg("working"))
	overlay = updated.(Model)
	updated, _ = overlay.Update(SearchResultsActionMsg{Action: SearchActionOpen, FilePath: "README.md", LineNumber: 12})
	closed := updated.(Model)
	if closed.state != StateStreaming || called != SearchActionOpen {
		t.Fatalf("action did not restore/dispatch: state=%v action=%v", closed.state, called)
	}
}

func TestSearchResultCopyStaysOpenAndConfirms(t *testing.T) {
	m := NewModel()
	updated, _ := m.Update(SearchResultsRequestMsg{Query: "needle", Tool: "grep"})
	overlay := updated.(Model)

	var gotAction SearchAction
	var gotPath string
	var gotLine int
	overlay.SetSearchResultActionCallback(func(action SearchAction, path string, line int) {
		gotAction, gotPath, gotLine = action, path, line
	})
	updated, _ = overlay.Update(SearchResultsActionMsg{
		Action: SearchActionCopyPath, FilePath: "internal/ui/tui.go", LineNumber: 2951,
	})
	got := updated.(Model)

	if got.state != StateSearchResults || got.searchRequest == nil {
		t.Fatalf("copy closed search results: state=%v request=%v", got.state, got.searchRequest)
	}
	if gotAction != SearchActionCopyPath || gotPath != "internal/ui/tui.go" || gotLine != 2951 {
		t.Fatalf("payload callback got action=%v path=%q line=%d", gotAction, gotPath, gotLine)
	}
	if !activeToastContains(&got, "Copied result path") &&
		!activeToastContains(&got, "Copy request sent via terminal") {
		t.Fatal("copy did not distinguish confirmed success from terminal fallback")
	}
}

func TestSearchResultUnavailableActionStaysOpen(t *testing.T) {
	m := NewModel()
	updated, _ := m.Update(SearchResultsRequestMsg{Query: "needle", Tool: "grep"})
	overlay := updated.(Model)
	updated, _ = overlay.Update(SearchResultsActionMsg{
		Action: SearchActionEdit, FilePath: "internal/ui/tui.go", LineNumber: 2951,
	})
	got := updated.(Model)

	if got.state != StateSearchResults || got.searchRequest == nil {
		t.Fatalf("unavailable action closed search results: state=%v request=%v", got.state, got.searchRequest)
	}
	if !activeToastContains(&got, "Search result actions are unavailable") {
		t.Fatal("unavailable search action did not explain itself")
	}
}

func TestSearchResultOpenPreservesPayloadAndRestores(t *testing.T) {
	m := NewModel()
	m.state = StateProcessing
	updated, _ := m.Update(SearchResultsRequestMsg{Query: "needle", Tool: "grep"})
	overlay := updated.(Model)
	updated, _ = overlay.Update(StreamTextMsg("working"))
	overlay = updated.(Model)

	var gotAction SearchAction
	var gotPath string
	var gotLine int
	overlay.SetSearchResultActionCallback(func(action SearchAction, path string, line int) {
		gotAction, gotPath, gotLine = action, path, line
	})
	updated, _ = overlay.Update(SearchResultsActionMsg{
		Action: SearchActionOpen, FilePath: "internal/ui/tui.go", LineNumber: 2951,
	})
	closed := updated.(Model)

	if closed.state != StateStreaming || closed.searchRequest != nil {
		t.Fatalf("open did not restore overlay: state=%v request=%v", closed.state, closed.searchRequest)
	}
	if gotAction != SearchActionOpen || gotPath != "internal/ui/tui.go" || gotLine != 2951 {
		t.Fatalf("payload callback got action=%v path=%q line=%d", gotAction, gotPath, gotLine)
	}
}

func TestSearchResultCopyWithoutPathStaysOpenAndExplains(t *testing.T) {
	m := NewModel()
	updated, _ := m.Update(SearchResultsRequestMsg{Query: "needle", Tool: "grep"})
	overlay := updated.(Model)
	updated, _ = overlay.Update(SearchResultsActionMsg{Action: SearchActionCopyPath})
	got := updated.(Model)

	if got.state != StateSearchResults || got.searchRequest == nil {
		t.Fatalf("empty copy closed search results: state=%v request=%v", got.state, got.searchRequest)
	}
	if !activeToastContains(&got, "No path is available to copy") {
		t.Fatal("empty copy did not explain why it could not run")
	}
}

func TestGitStatusActionPreservesPayloadAndRestores(t *testing.T) {
	m := NewModel()
	m.state = StateProcessing
	updated, _ := m.Update(GitStatusRequestMsg{Branch: "main"})
	overlay := updated.(Model)
	updated, _ = overlay.Update(StreamTextMsg("working"))
	overlay = updated.(Model)

	var gotAction GitAction
	var gotFiles []string
	var gotMessage string
	overlay.SetGitStatusActionCallback(func(action GitAction, files []string, message string) {
		gotAction, gotFiles, gotMessage = action, files, message
		files[0] = "mutated-by-callback"
	})
	msg := GitStatusActionMsg{Action: GitActionCommit, Files: []string{"a.go", "b.go"}, Message: "Improve UX"}
	updated, _ = overlay.Update(msg)
	closed := updated.(Model)

	if closed.state != StateStreaming || closed.gitStatusRequest != nil {
		t.Fatalf("git action did not restore overlay: state=%v request=%v", closed.state, closed.gitStatusRequest)
	}
	if gotAction != GitActionCommit || gotMessage != "Improve UX" || len(gotFiles) != 2 {
		t.Fatalf("payload callback got action=%v files=%v message=%q", gotAction, gotFiles, gotMessage)
	}
	if msg.Files[0] != "a.go" {
		t.Fatalf("callback mutated action message payload: %v", msg.Files)
	}
}

func TestGitStatusUnavailableActionStaysOpen(t *testing.T) {
	m := NewModel()
	updated, _ := m.Update(GitStatusRequestMsg{Branch: "main"})
	overlay := updated.(Model)
	updated, _ = overlay.Update(GitStatusActionMsg{Action: GitActionStage, Files: []string{"a.go"}})
	got := updated.(Model)

	if got.state != StateGitStatus || got.gitStatusRequest == nil {
		t.Fatalf("unavailable action closed git status: state=%v request=%v", got.state, got.gitStatusRequest)
	}
	if !activeToastContains(&got, "Git status actions are unavailable") {
		t.Fatal("unavailable git action did not explain itself")
	}
}

func TestGitStatusDiffRequestStaysOpenAndLoadsMatchingResponse(t *testing.T) {
	m := NewModel()
	m.width, m.height = 90, 24
	updated, _ := m.Update(GitStatusRequestMsg{
		Branch:  "main",
		Entries: []GitFileEntry{{FilePath: "one.go", Status: GitFileModified}},
	})
	overlay := updated.(Model)

	var requested []string
	overlay.SetGitStatusActionCallback(func(action GitAction, files []string, _ string) {
		if action == GitActionDiff {
			requested = files
		}
	})
	updated, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if cmd == nil {
		t.Fatal("d did not issue a diff request")
	}
	overlay = updated.(Model)
	updated, _ = overlay.Update(cmd())
	loading := updated.(Model)

	if loading.state != StateGitStatus || loading.gitStatusRequest == nil {
		t.Fatalf("diff request closed git status: state=%v request=%v", loading.state, loading.gitStatusRequest)
	}
	if len(requested) != 1 || requested[0] != "one.go" {
		t.Fatalf("diff callback files=%v, want one.go", requested)
	}

	updated, _ = loading.Update(GitStatusDiffMsg{FilePath: "one.go", Content: "-old\n+new"})
	loaded := updated.(Model)
	if got := stripAnsi(loaded.gitStatusModel.diffViewport.View()); !strings.Contains(got, "+new") {
		t.Fatalf("matching response did not populate diff: %q", got)
	}
}

func TestGitStatusDiffIgnoresLateResponseForPreviousSelection(t *testing.T) {
	m := NewModel()
	m.width, m.height = 90, 24
	updated, _ := m.Update(GitStatusRequestMsg{
		Branch: "main",
		Entries: []GitFileEntry{
			{FilePath: "one.go", Status: GitFileModified},
			{FilePath: "two.go", Status: GitFileModified},
		},
	})
	overlay := updated.(Model)
	overlay.gitStatusModel.showDiff = true
	overlay.gitStatusModel.pendingDiffPath = "two.go"
	overlay.gitStatusModel.SetDiff("Loading diff…")

	updated, _ = overlay.Update(GitStatusDiffMsg{FilePath: "one.go", Content: "stale content"})
	got := updated.(Model)
	if preview := stripAnsi(got.gitStatusModel.diffViewport.View()); strings.Contains(preview, "stale content") || !strings.Contains(preview, "Loading diff") {
		t.Fatalf("late response replaced current loading state: %q", preview)
	}
	if got.gitStatusModel.pendingDiffPath != "two.go" {
		t.Fatalf("late response cleared pending path: %q", got.gitStatusModel.pendingDiffPath)
	}
}

func TestGitStatusUnavailableDiffStaysOpenWithInlineExplanation(t *testing.T) {
	m := NewModel()
	m.width, m.height = 90, 24
	updated, _ := m.Update(GitStatusRequestMsg{
		Branch:  "main",
		Entries: []GitFileEntry{{FilePath: "one.go", Status: GitFileModified}},
	})
	overlay := updated.(Model)
	overlay.gitStatusModel.showDiff = true
	overlay.gitStatusModel.pendingDiffPath = "one.go"
	overlay.gitStatusModel.SetDiff("Loading diff…")

	updated, _ = overlay.Update(GitStatusActionMsg{Action: GitActionDiff, Files: []string{"one.go"}})
	got := updated.(Model)
	if got.state != StateGitStatus || got.gitStatusRequest == nil {
		t.Fatalf("unavailable diff closed git status: state=%v request=%v", got.state, got.gitStatusRequest)
	}
	if preview := stripAnsi(got.gitStatusModel.diffViewport.View()); !strings.Contains(preview, "No diff provider is connected") {
		t.Fatalf("unavailable diff lacks inline explanation: %q", preview)
	}
	if !activeToastContains(&got, "Diff preview is unavailable") {
		t.Fatal("unavailable diff lacks toast feedback")
	}
}
