package ui

import (
	"errors"
	tea "github.com/charmbracelet/bubbletea"
	"reflect"
	"strings"
	"testing"
)

func TestPromptEditorArgumentsKeepQuotedPathsAndDoNotRunShellExpressions(t *testing.T) {
	for _, test := range []struct {
		input string
		want  []string
	}{
		{`code --wait`, []string{"code", "--wait"}},
		{`"/Applications/Visual Studio Code.app/bin/code" --wait`, []string{"/Applications/Visual Studio Code.app/bin/code", "--wait"}},
		{`"C:\Program Files\Editor\editor.exe" --wait`, []string{`C:\Program Files\Editor\editor.exe`, "--wait"}},
		{`editor '$(touch /tmp/never-run)'`, []string{"editor", "$(touch /tmp/never-run)"}},
	} {
		args, err := editorArguments(test.input)
		if err != nil || !reflect.DeepEqual(args, test.want) {
			t.Fatalf("%s: args=%q err=%v", test.input, args, err)
		}
	}
	if _, err := editorArguments(`editor "unterminated`); err == nil {
		t.Fatal("invalid editor command accepted")
	}
}

func TestPromptEditorFailureAndStaleCompletionPreserveCurrentDraft(t *testing.T) {
	m := claudeComposer(t)
	m = uxText(m, "newer draft")
	for _, msg := range []editorFinishedMsg{{original: "older draft", err: errors.New("editor failed")}, {original: "older draft", text: "edited older draft"}} {
		updated, _ := m.applyEditedPrompt(msg)
		m = updated.(Model)
		if m.input.Value() != "newer draft" {
			t.Fatal("editor completion overwrote a newer draft")
		}
	}
	if len(m.input.history) == 0 || m.input.history[len(m.input.history)-1] != "edited older draft" {
		t.Fatal("stale edited prompt is not recoverable")
	}
}

func TestPromptEditorRoundTripRemainsUndoableAndDoesNotSubmit(t *testing.T) {
	m := claudeComposer(t)
	m = uxText(m, "original draft")
	submitted := false
	m.SetCallbacks(func(string) { submitted = true }, nil)
	text := strings.Repeat("edited text\n", 12) + "last"
	updated, _ := m.applyEditedPrompt(editorFinishedMsg{original: "original draft", text: text})
	m = updated.(Model)
	if m.input.ExpandedValue() != text || submitted {
		t.Fatal("edited prompt was truncated or submitted without confirmation")
	}
	m = uxCtrl(m, tea.KeyCtrlUnderscore)
	if m.input.Value() != "original draft" {
		t.Fatal("external edit cannot be undone")
	}
}
