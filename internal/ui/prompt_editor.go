package ui

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

type editorFinishedMsg struct {
	text, original string
	err            error
}

func (m *Model) editPromptExternally() tea.Cmd {
	if m.editorOpen {
		return keyConsumed
	}
	if m.state != StateInput {
		if m.toastManager != nil {
			m.toastManager.ShowInfo("Stop the current response before opening the prompt editor")
		}
		return keyConsumed
	}
	original := m.input.ExpandedValue()
	m.editorOpen = true
	return func() tea.Msg {
		editor := os.Getenv("VISUAL")
		if strings.TrimSpace(editor) == "" {
			editor = os.Getenv("EDITOR")
		}
		if strings.TrimSpace(editor) == "" {
			editor = "vi"
			if runtime.GOOS == "windows" {
				editor = "notepad"
			}
		}
		args, err := editorArguments(editor)
		if err != nil {
			return editorFinishedMsg{original: original, err: err}
		}
		file, err := os.CreateTemp("", "gokin-prompt-*.txt")
		if err != nil {
			return editorFinishedMsg{original: original, err: err}
		}
		path := file.Name()
		_, err = file.WriteString(original)
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			os.Remove(path)
			return editorFinishedMsg{original: original, err: err}
		}
		command := exec.Command(args[0], append(args[1:], path)...)
		return tea.ExecProcess(command, func(runErr error) tea.Msg {
			defer os.Remove(path)
			if runErr != nil {
				return editorFinishedMsg{original: original, err: runErr}
			}
			file, err := os.Open(path)
			if err != nil {
				return editorFinishedMsg{original: original, err: err}
			}
			defer file.Close()
			content, err := io.ReadAll(io.LimitReader(file, maxInputHistoryEntryBytes+1))
			if len(content) > maxInputHistoryEntryBytes {
				err = fmt.Errorf("edited prompt exceeds 1 MiB")
			}
			return editorFinishedMsg{text: string(content), original: original, err: err}
		})()
	}
}

func (m Model) applyEditedPrompt(msg editorFinishedMsg) (tea.Model, tea.Cmd) {
	m.editorOpen = false
	if msg.err != nil {
		if m.toastManager != nil {
			m.toastManager.ShowError("Prompt editor: " + safeKeyEntryText(msg.err.Error()) + " · draft preserved")
		}
		return m, nil
	}
	if m.input.ExpandedValue() != msg.original {
		m.input.AddToHistory(msg.text)
		if m.toastManager != nil {
			m.toastManager.ShowWarning("Draft changed while editor opened · edited prompt saved in input history")
		}
		return m, nil
	}
	before := m.input.snapshot()
	m.input.Reset()
	if isLargePaste(msg.text) {
		m.input = m.input.collapsePaste(msg.text)
	} else {
		m.input.textarea.SetValue(msg.text)
	}
	m.input.rememberUndo(before)
	m.input.syncTextareaHeight()
	m.input.refreshCompletionStateForValue()
	if m.toastManager != nil {
		m.toastManager.ShowInfo("Prompt updated · Enter sends · Ctrl+_ undoes")
	}
	return m, m.input.Focus()
}

// Parse an editor executable and arguments without invoking a shell. Quoted
// application paths work, while shell substitutions remain literal text.
func editorArguments(value string) ([]string, error) {
	var args []string
	var word strings.Builder
	var quote rune
	started := false
	runes := []rune(value)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\\' && i+1 < len(runes) && (runes[i+1] == quote || unicode.IsSpace(runes[i+1]) || runes[i+1] == '\\' || runes[i+1] == '"' || runes[i+1] == '\'') {
			i++
			word.WriteRune(runes[i])
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			started = true
			continue
		}
		if r == '"' || r == '\'' {
			quote = r
			started = true
			continue
		}
		if unicode.IsSpace(r) {
			if started {
				args = append(args, word.String())
				word.Reset()
				started = false
			}
			continue
		}
		word.WriteRune(r)
		started = true
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote in VISUAL/EDITOR")
	}
	if started {
		args = append(args, word.String())
	}
	if len(args) == 0 || args[0] == "" {
		return nil, fmt.Errorf("editor executable is empty")
	}
	return args, nil
}
