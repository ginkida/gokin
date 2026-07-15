package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Denying one tool action (or dismissing ask_user with an empty answer) resolves
// the blocking tool callback; it does not cancel the foreground executor turn.
// These close paths must therefore restore Processing just like plan rejection,
// otherwise the composer/status bar falsely report idle until the next stream
// event arrives and any immediate submission is unexpectedly queued by the app.
func TestBlockingDecisionSoftRejectionResumesProcessing(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T) Model
	}{
		{name: "permission deny", run: func(t *testing.T) Model {
			m := NewModel()
			m.state = StatePermissionPrompt
			m.permRequest = &PermissionRequestMsg{ID: "permission-1", ToolName: "bash"}
			m.SetPermissionCallback(func(string, PermissionDecision) {})
			_ = m.handlePermissionPromptKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
			return *m
		}},
		{name: "question dismiss", run: func(t *testing.T) Model {
			m := NewModel()
			m.state = StateQuestionPrompt
			m.questionRequest = &QuestionRequestMsg{ID: "question-1", Question: "Continue?", Options: []string{"Yes"}}
			m.SetQuestionCallback(func(string) {})
			_ = m.handleQuestionPromptKeys(tea.KeyMsg{Type: tea.KeyEscape})
			return *m
		}},
		{name: "single diff reject", run: func(t *testing.T) Model {
			m := NewModel()
			req := DiffPreviewRequestMsg{RequestID: "diff-1", FilePath: "main.go", OldContent: "old\n", NewContent: "new\n"}
			m.diffRequest, m.state = &req, StateDiffPreview
			m.diffPreview.SetRequestID(req.RequestID)
			m.diffPreview.SetContent(req.FilePath, req.OldContent, req.NewContent, "edit", false)
			m.SetDiffDecisionCallback(func(DiffDecision) {})
			return runBlockingDecisionCommand(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
		}},
		{name: "multi diff reject", run: func(t *testing.T) Model {
			m := NewModel()
			files := []DiffFile{{FilePath: "main.go", OldContent: "old\n", NewContent: "new\n"}}
			req := MultiDiffPreviewRequestMsg{RequestID: "multi-diff-1", Files: files}
			m.multiDiffRequest, m.state = &req, StateMultiDiffPreview
			m.multiDiffPreview.SetRequestID(req.RequestID)
			m.multiDiffPreview.SetFiles(files)
			m.SetMultiDiffDecisionCallback(func(map[string]DiffDecision) {})
			return runBlockingDecisionCommand(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.run(t); got.state != StateProcessing {
				t.Errorf("state = %v, want StateProcessing while executor consumes the decision", got.state)
			}
		})
	}
}

func runBlockingDecisionCommand(t *testing.T, m *Model, key tea.KeyMsg) Model {
	t.Helper()
	updated, cmd := m.Update(key)
	if cmd == nil {
		t.Fatal("decision key did not emit a response")
	}
	updated, _ = updated.(Model).Update(cmd())
	return updated.(Model)
}
