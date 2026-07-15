package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func runSurfaceActionCmd(t *testing.T, m Model, key tea.KeyMsg) Model {
	t.Helper()
	afterKey, cmd := updateWorkspaceOwnershipModel(t, m, key)
	if cmd == nil {
		t.Fatal("surface action did not emit a command")
	}
	closed, _ := updateWorkspaceOwnershipModel(t, afterKey, cmd())
	return closed
}

func assertDraftWasNotSubmitted(t *testing.T, m Model, wantDraft string, submissions int) {
	t.Helper()
	if submissions != 0 {
		t.Fatalf("surface key repeat submitted the underlying draft %d time(s)", submissions)
	}
	if got := m.input.Value(); got != wantDraft {
		t.Fatalf("surface key repeat changed the underlying draft: got %q, want %q", got, wantDraft)
	}
}

// A terminal action belongs to the surface that rendered it. Bubble Tea
// delivers the resulting action message asynchronously, so the next physical
// auto-repeat may arrive after the parent has already revealed the composer.
// That repeat must not become an unrelated submit or edit of the preserved
// draft.
func TestRapidFullscreenTerminalActionDoesNotLeakIntoComposer(t *testing.T) {
	for _, decisionKey := range []rune{'y', 'n'} {
		t.Run("single diff decision key "+string(decisionKey)+" cannot pollute typeahead", func(t *testing.T) {
			m := *NewModel()
			m.width, m.height = 80, 24
			m.state = StateProcessing
			m.input.textarea.SetValue("draft stays exact")
			m.SetDiffDecisionCallback(func(DiffDecision) {})
			opened, _ := updateWorkspaceOwnershipModel(t, m, DiffPreviewRequestMsg{
				RequestID: "diff-repeat-" + string(decisionKey), FilePath: "main.go", OldContent: "old\n", NewContent: "new\n",
			})

			key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{decisionKey}}
			closed := runSurfaceActionCmd(t, opened, key)
			if closed.state != StateProcessing {
				t.Fatalf("diff decision returned to %v, want Processing", closed.state)
			}
			repeated, _ := updateWorkspaceOwnershipModel(t, closed, key)
			if got := repeated.input.Value(); got != "draft stays exact" {
				t.Fatalf("repeated diff decision key polluted typeahead: %q", got)
			}
		})
	}

	t.Run("multi diff finish Enter cannot submit typeahead", func(t *testing.T) {
		m := *NewModel()
		m.width, m.height = 80, 24
		m.state = StateProcessing
		m.input.textarea.SetValue("draft must remain unsent")
		submissions := 0
		m.SetCallbacks(func(string) { submissions++ }, nil)
		m.SetMultiDiffDecisionCallback(func(map[string]DiffDecision) {})
		opened, _ := updateWorkspaceOwnershipModel(t, m, MultiDiffPreviewRequestMsg{
			RequestID: "multi-repeat",
			Files:     []DiffFile{{FilePath: "main.go", OldContent: "old\n", NewContent: "new\n"}},
		})
		confirming, cmd := updateWorkspaceOwnershipModel(t, opened, tea.KeyMsg{Type: tea.KeyEnter})
		if cmd != nil || !confirming.multiDiffPreview.confirmFinish {
			t.Fatal("setup: first multi-diff Enter did not open finish confirmation")
		}
		closed := runSurfaceActionCmd(t, confirming, tea.KeyMsg{Type: tea.KeyEnter})
		if closed.state != StateProcessing {
			t.Fatalf("multi-diff decision returned to %v, want Processing", closed.state)
		}
		repeated, _ := updateWorkspaceOwnershipModel(t, closed, tea.KeyMsg{Type: tea.KeyEnter})
		assertDraftWasNotSubmitted(t, repeated, "draft must remain unsent", submissions)
	})

	t.Run("search Enter cannot submit revealed draft", func(t *testing.T) {
		m := *NewModel()
		m.width, m.height = 80, 24
		m.input.textarea.SetValue("draft must remain unsent")
		submissions, opens := 0, 0
		m.SetCallbacks(func(string) { submissions++ }, nil)
		m.SetSearchResultActionCallback(func(action SearchAction, _ string, _ int) {
			if action == SearchActionOpen {
				opens++
			}
		})
		opened, _ := updateWorkspaceOwnershipModel(t, m, SearchResultsRequestMsg{
			Query: "needle", Tool: "grep",
			Results: []SearchResult{{FilePath: "main.go", LineNumber: 7, Content: "needle"}},
		})
		closed := runSurfaceActionCmd(t, opened, tea.KeyMsg{Type: tea.KeyEnter})
		if opens != 1 || closed.state != StateInput {
			t.Fatalf("setup: opens=%d state=%v, want one open and Input", opens, closed.state)
		}
		repeated, _ := updateWorkspaceOwnershipModel(t, closed, tea.KeyMsg{Type: tea.KeyEnter})
		assertDraftWasNotSubmitted(t, repeated, "draft must remain unsent", submissions)
	})

	t.Run("git Space cannot edit revealed draft", func(t *testing.T) {
		m := *NewModel()
		m.width, m.height = 80, 24
		m.input.textarea.SetValue("draft stays exact")
		actions := 0
		m.SetGitStatusActionCallback(func(GitAction, []string, string) { actions++ })
		opened, _ := updateWorkspaceOwnershipModel(t, m, GitStatusRequestMsg{
			Branch:  "main",
			Entries: []GitFileEntry{{FilePath: "main.go", Status: GitFileModified}},
		})
		closed := runSurfaceActionCmd(t, opened, tea.KeyMsg{Type: tea.KeySpace})
		if actions != 1 || closed.state != StateInput {
			t.Fatalf("setup: actions=%d state=%v, want one action and Input", actions, closed.state)
		}
		if closed.modalExitGuardKey != (tea.KeyMsg{Type: tea.KeySpace}).String() || closed.modalEnterGuardUntil.IsZero() {
			t.Fatalf("Git transition did not retain exact Space ownership: key=%q until=%v", closed.modalExitGuardKey, closed.modalEnterGuardUntil)
		}
		repeated, _ := updateWorkspaceOwnershipModel(t, closed, tea.KeyMsg{Type: tea.KeySpace})
		if got := repeated.input.Value(); got != "draft stays exact" {
			t.Fatalf("repeated Git action Space edited the composer: %q", got)
		}
	})

	t.Run("file Enter cannot submit inserted reference", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "selected.go")
		if err := os.WriteFile(file, []byte("package selected\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		m := *NewModel()
		m.width, m.height = 80, 24
		m.workDir = dir
		m.input.textarea.SetValue("review")
		submissions := 0
		m.SetCallbacks(func(string) { submissions++ }, nil)
		opened, _ := updateWorkspaceOwnershipModel(t, m, FileBrowserRequestMsg{StartPath: dir})
		selectFileBrowserPath(t, &opened.fileBrowser, file)
		closed := runSurfaceActionCmd(t, opened, tea.KeyMsg{Type: tea.KeyEnter})
		if closed.state != StateInput {
			t.Fatalf("file selection returned to %v, want Input", closed.state)
		}
		wantDraft := closed.input.Value()
		if wantDraft == "" || wantDraft == "review" {
			t.Fatalf("setup: file selection did not add a reference: %q", wantDraft)
		}
		repeated, _ := updateWorkspaceOwnershipModel(t, closed, tea.KeyMsg{Type: tea.KeyEnter})
		assertDraftWasNotSubmitted(t, repeated, wantDraft, submissions)
	})

	t.Run("completed batch Enter cannot submit revealed draft", func(t *testing.T) {
		m := *NewModel()
		m.width, m.height = 80, 24
		m.input.textarea.SetValue("draft must remain unsent")
		submissions := 0
		m.SetCallbacks(func(string) { submissions++ }, nil)
		m.progressModel.Start("Batch", 1)
		m.progressModel.Complete()
		m.progressActive = true
		m.progressReturnState = StateInput
		m.state = StateBatchProgress

		closed := runSurfaceActionCmd(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		if closed.state != StateInput || closed.progressActive {
			t.Fatalf("setup: completed batch state=%v active=%v", closed.state, closed.progressActive)
		}
		repeated, _ := updateWorkspaceOwnershipModel(t, closed, tea.KeyMsg{Type: tea.KeyEnter})
		assertDraftWasNotSubmitted(t, repeated, "draft must remain unsent", submissions)
	})
}

func TestSurfaceExitKeyGuardReleasesForNewIntentAndExpires(t *testing.T) {
	t.Run("distinct key releases exact transition ownership", func(t *testing.T) {
		m := *NewModel()
		m.width, m.height = 80, 24
		m.input.textarea.SetValue("draft")
		submissions := 0
		m.SetCallbacks(func(string) { submissions++ }, nil)
		m.SetSearchResultActionCallback(func(SearchAction, string, int) {})
		opened, _ := updateWorkspaceOwnershipModel(t, m, SearchResultsRequestMsg{
			Query: "needle", Tool: "grep", Results: []SearchResult{{FilePath: "main.go", Content: "needle"}},
		})
		closed := runSurfaceActionCmd(t, opened, tea.KeyMsg{Type: tea.KeyEnter})
		if closed.modalExitGuardKey != "enter" {
			t.Fatalf("search transition key=%q, want enter", closed.modalExitGuardKey)
		}

		typed, _ := updateWorkspaceOwnershipModel(t, closed, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'!'}})
		if !typed.modalEnterGuardUntil.IsZero() || typed.modalExitGuardKey != "" {
			t.Fatalf("distinct key did not release guard: key=%q until=%v", typed.modalExitGuardKey, typed.modalEnterGuardUntil)
		}
		if got := typed.input.Value(); got != "draft!" {
			t.Fatalf("distinct key was not delivered to composer: %q", got)
		}
		_, _ = updateWorkspaceOwnershipModel(t, typed, tea.KeyMsg{Type: tea.KeyEnter})
		if submissions != 1 {
			t.Fatalf("intentional Enter after distinct input submitted %d times, want 1", submissions)
		}
	})

	t.Run("expired ownership permits same key", func(t *testing.T) {
		m := *NewModel()
		m.width, m.height = 80, 24
		m.state = StateProcessing
		m.input.textarea.SetValue("draft")
		m.SetDiffDecisionCallback(func(DiffDecision) {})
		opened, _ := updateWorkspaceOwnershipModel(t, m, DiffPreviewRequestMsg{
			RequestID: "diff-expiry", FilePath: "main.go", OldContent: "old\n", NewContent: "new\n",
		})
		key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
		closed := runSurfaceActionCmd(t, opened, key)
		closed.modalEnterGuardUntil = time.Now().Add(-time.Millisecond)
		afterExpiry, _ := updateWorkspaceOwnershipModel(t, closed, key)
		if got := afterExpiry.input.Value(); got != "drafty" {
			t.Fatalf("expired exact-key ownership still suppressed intentional input: %q", got)
		}
		if afterExpiry.modalExitGuardKey != "" || !afterExpiry.modalEnterGuardUntil.IsZero() {
			t.Fatalf("expired guard was not cleared: key=%q until=%v", afterExpiry.modalExitGuardKey, afterExpiry.modalEnterGuardUntil)
		}
	})
}

// Esc is both a surface-close key and the active-turn interrupt key. A single
// auto-repeat burst must not reject/close a review and then immediately cancel
// the executor that resumes underneath it.
func TestRapidFullscreenEscapeDoesNotInterruptResumedTurn(t *testing.T) {
	tests := []struct {
		name string
		open func(*testing.T, Model) Model
	}{
		{
			name: "single diff",
			open: func(t *testing.T, m Model) Model {
				m.SetDiffDecisionCallback(func(DiffDecision) {})
				opened, _ := updateWorkspaceOwnershipModel(t, m, DiffPreviewRequestMsg{
					RequestID: "diff-escape", FilePath: "main.go", OldContent: "old\n", NewContent: "new\n",
				})
				return opened
			},
		},
		{
			name: "multi diff",
			open: func(t *testing.T, m Model) Model {
				m.SetMultiDiffDecisionCallback(func(map[string]DiffDecision) {})
				opened, _ := updateWorkspaceOwnershipModel(t, m, MultiDiffPreviewRequestMsg{
					RequestID: "multi-escape",
					Files:     []DiffFile{{FilePath: "main.go", OldContent: "old\n", NewContent: "new\n"}},
				})
				return opened
			},
		},
		{
			name: "search",
			open: func(t *testing.T, m Model) Model {
				opened, _ := updateWorkspaceOwnershipModel(t, m, SearchResultsRequestMsg{
					Query: "needle", Tool: "grep",
					Results: []SearchResult{{FilePath: "main.go", LineNumber: 1, Content: "needle"}},
				})
				return opened
			},
		},
		{
			name: "git",
			open: func(t *testing.T, m Model) Model {
				opened, _ := updateWorkspaceOwnershipModel(t, m, GitStatusRequestMsg{
					Branch: "main", Entries: []GitFileEntry{{FilePath: "main.go", Status: GitFileModified}},
				})
				return opened
			},
		},
		{
			name: "file browser",
			open: func(t *testing.T, m Model) Model {
				opened, _ := updateWorkspaceOwnershipModel(t, m, FileBrowserRequestMsg{StartPath: t.TempDir()})
				return opened
			},
		},
		{
			name: "batch progress",
			open: func(_ *testing.T, m Model) Model {
				m.progressModel.Start("Batch", 1)
				m.progressModel.Complete()
				m.progressActive = true
				m.progressReturnState = StateProcessing
				m.state = StateBatchProgress
				return m
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := *NewModel()
			m.width, m.height = 80, 24
			m.state = StateProcessing
			m.input.textarea.SetValue("typeahead stays exact")
			cancels := 0
			m.SetCancelCallback(func() { cancels++ })
			opened := tc.open(t, m)
			closed := runSurfaceActionCmd(t, opened, tea.KeyMsg{Type: tea.KeyEsc})
			if closed.state != StateProcessing {
				t.Fatalf("surface close returned to %v, want Processing", closed.state)
			}

			repeated, _ := updateWorkspaceOwnershipModel(t, closed, tea.KeyMsg{Type: tea.KeyEsc})
			if cancels != 0 {
				t.Fatalf("repeated surface Esc interrupted resumed turn %d time(s)", cancels)
			}
			if repeated.state != StateProcessing {
				t.Fatalf("repeated surface Esc changed resumed state to %v", repeated.state)
			}
			if got := repeated.input.Value(); got != "typeahead stays exact" {
				t.Fatalf("repeated surface Esc changed typeahead: %q", got)
			}
		})
	}
}

func selectFileBrowserPath(t *testing.T, m *FileBrowserModel, want string) {
	t.Helper()
	for i, entry := range m.entries {
		if entry.Path == want {
			m.selectEntry(i)
			return
		}
	}
	t.Fatalf("file browser does not contain %q; entries=%v", want, m.entries)
}
