package ui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func updateWorkspaceOwnershipModel(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(msg)
	got, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want ui.Model", updated)
	}
	return got, cmd
}

// A workspace action is produced asynchronously by a child Bubble Tea model.
// Once its owning overlay has closed, replaying that action must be a no-op --
// even when another instance of the same overlay type is now visible. Merely
// checking the current State is insufficient for this same-surface ABA case;
// implementations need an opening generation/request owner stamped onto the
// emitted action.
func TestStaleWorkspaceActionCannotMutateReopenedOverlay(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{
			name: "search action A cannot close search B",
			run: func(t *testing.T) {
				m := *NewModel()
				m.input.textarea.SetValue("draft stays")
				callbackCalls := 0
				m.SetSearchResultActionCallback(func(SearchAction, string, int) {
					callbackCalls++
				})

				openedA, _ := updateWorkspaceOwnershipModel(t, m, SearchResultsRequestMsg{
					Query: "request A",
					Tool:  "grep",
					Results: []SearchResult{{
						FilePath: "a.go", LineNumber: 10, Content: "A",
					}},
				})
				afterActionKey, actionA := updateWorkspaceOwnershipModel(t, openedA, tea.KeyMsg{Type: tea.KeyEnter})
				if actionA == nil {
					t.Fatal("setup: search A did not emit an action")
				}
				closedA, _ := updateWorkspaceOwnershipModel(t, afterActionKey, actionA())
				if callbackCalls != 1 || closedA.state != StateInput {
					t.Fatalf("setup: search A calls=%d state=%v", callbackCalls, closedA.state)
				}

				openedB, _ := updateWorkspaceOwnershipModel(t, closedA, SearchResultsRequestMsg{
					Query: "request B",
					Tool:  "grep",
					Results: []SearchResult{{
						FilePath: "b.go", LineNumber: 20, Content: "B",
					}},
				})
				beforeDraft := openedB.input.Value()
				got, _ := updateWorkspaceOwnershipModel(t, openedB, actionA())

				if got.state != StateSearchResults || got.searchRequest == nil || got.searchRequest.Query != "request B" {
					t.Errorf("stale search A action replaced/closed B: state=%v request=%+v", got.state, got.searchRequest)
				}
				if callbackCalls != 1 {
					t.Errorf("stale search A action reached callback again: total=%d", callbackCalls)
				}
				if draft := got.input.Value(); draft != beforeDraft {
					t.Errorf("stale search A action changed draft: before=%q after=%q", beforeDraft, draft)
				}
			},
		},
		{
			name: "git action A cannot close git B",
			run: func(t *testing.T) {
				m := *NewModel()
				m.input.textarea.SetValue("draft stays")
				callbackCalls := 0
				m.SetGitStatusActionCallback(func(GitAction, []string, string) {
					callbackCalls++
				})

				openedA, _ := updateWorkspaceOwnershipModel(t, m, GitStatusRequestMsg{
					Branch: "branch-A",
					Entries: []GitFileEntry{{
						FilePath: "a.go", Status: GitFileStaged, IsStaged: true,
					}},
				})
				afterActionKey, actionA := updateWorkspaceOwnershipModel(t, openedA, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
				if actionA == nil {
					t.Fatal("setup: git A did not emit a commit action")
				}
				closedA, _ := updateWorkspaceOwnershipModel(t, afterActionKey, actionA())
				if callbackCalls != 1 || closedA.state != StateInput {
					t.Fatalf("setup: git A calls=%d state=%v", callbackCalls, closedA.state)
				}

				openedB, _ := updateWorkspaceOwnershipModel(t, closedA, GitStatusRequestMsg{
					Branch: "branch-B",
					Entries: []GitFileEntry{{
						FilePath: "b.go", Status: GitFileStaged, IsStaged: true,
					}},
				})
				beforeDraft := openedB.input.Value()
				got, _ := updateWorkspaceOwnershipModel(t, openedB, actionA())

				if got.state != StateGitStatus || got.gitStatusRequest == nil || got.gitStatusRequest.Branch != "branch-B" {
					t.Errorf("stale git A action replaced/closed B: state=%v request=%+v", got.state, got.gitStatusRequest)
				}
				if callbackCalls != 1 {
					t.Errorf("stale git A action reached callback again: total=%d", callbackCalls)
				}
				if draft := got.input.Value(); draft != beforeDraft {
					t.Errorf("stale git A action changed draft: before=%q after=%q", beforeDraft, draft)
				}
			},
		},
		{
			name: "file action A cannot close file browser B or edit draft",
			run: func(t *testing.T) {
				dirA := t.TempDir()
				dirB := t.TempDir()
				fileA := filepath.Join(dirA, "a.go")
				if err := os.WriteFile(fileA, []byte("package a\n"), 0o600); err != nil {
					t.Fatal(err)
				}

				m := *NewModel()
				m.workDir = dirB
				m.input.textarea.SetValue("draft stays")
				callbackCalls := 0
				m.SetFileSelectCallback(func(string) { callbackCalls++ })

				openedA, _ := updateWorkspaceOwnershipModel(t, m, FileBrowserRequestMsg{StartPath: dirA})
				for i, entry := range openedA.fileBrowser.entries {
					if entry.Path == fileA {
						openedA.fileBrowser.selectedIndex = i
						break
					}
				}
				afterActionKey, actionA := updateWorkspaceOwnershipModel(t, openedA, tea.KeyMsg{Type: tea.KeyEnter})
				if actionA == nil {
					t.Fatal("setup: file browser A did not emit an open action")
				}
				closedA, _ := updateWorkspaceOwnershipModel(t, afterActionKey, actionA())
				if callbackCalls != 1 || closedA.state != StateInput {
					t.Fatalf("setup: file browser A calls=%d state=%v", callbackCalls, closedA.state)
				}

				openedB, _ := updateWorkspaceOwnershipModel(t, closedA, FileBrowserRequestMsg{StartPath: dirB})
				beforeDraft := openedB.input.Value()
				beforeDir := openedB.fileBrowser.currentDir
				got, _ := updateWorkspaceOwnershipModel(t, openedB, actionA())

				if got.state != StateFileBrowser || !got.fileBrowserActive || got.fileBrowser.currentDir != beforeDir {
					t.Errorf("stale file-browser A action replaced/closed B: state=%v active=%v dir=%q want=%q", got.state, got.fileBrowserActive, got.fileBrowser.currentDir, beforeDir)
				}
				if callbackCalls != 1 {
					t.Errorf("stale file-browser A action reached callback again: total=%d", callbackCalls)
				}
				if draft := got.input.Value(); draft != beforeDraft {
					t.Errorf("stale file-browser A action changed draft: before=%q after=%q", beforeDraft, draft)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, tc.run)
	}
}

func TestGitStatusCorrelatedActionDispatchesExactlyOnce(t *testing.T) {
	m := *NewModel()
	calls := 0
	var gotAction GitAction
	var gotFiles []string
	m.SetGitStatusActionCallbackWithID(func(action GitAction, files []string, _ string, _ string) {
		calls++
		gotAction = action
		gotFiles = append([]string(nil), files...)
	})

	opened, _ := updateWorkspaceOwnershipModel(t, m, GitStatusRequestMsg{
		Branch: "main",
		Entries: []GitFileEntry{{
			FilePath: "main.go",
			Status:   GitFileModified,
		}},
	})
	staged, actionCmd := updateWorkspaceOwnershipModel(t, opened, tea.KeyMsg{Type: tea.KeySpace})
	if actionCmd == nil {
		t.Fatal("stage key did not emit a Git action")
	}
	_, _ = updateWorkspaceOwnershipModel(t, staged, actionCmd())

	if calls != 1 || gotAction != GitActionStage || len(gotFiles) != 1 || gotFiles[0] != "main.go" {
		t.Fatalf("correlated Git dispatch calls=%d action=%v files=%v, want one Stage for main.go", calls, gotAction, gotFiles)
	}
}
