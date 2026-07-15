package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestModelWorkDirPowersMainComposerFileAutocomplete(t *testing.T) {
	for _, accept := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{name: "enter", key: tea.KeyMsg{Type: tea.KeyEnter}},
		{name: "tab", key: tea.KeyMsg{Type: tea.KeyTab}},
	} {
		t.Run(accept.name, func(t *testing.T) {
			workDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			m := NewModel()
			m.SetWorkDir(workDir)
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("@ma")})
			model := updated.(Model)

			if got := model.input.workDir; got != workDir {
				t.Fatalf("main composer workDir=%q, want %q", got, workDir)
			}
			if !model.input.ShowingSuggestions() || len(model.input.fileSuggestions) == 0 {
				t.Fatalf("@file autocomplete unavailable after SetWorkDir: notice=%q suggestions=%v", model.input.suggestionNotice, model.input.fileSuggestions)
			}
			if strings.Contains(strings.ToLower(model.input.suggestionNotice), "no working directory") {
				t.Fatalf("main composer still reports missing workdir: %q", model.input.suggestionNotice)
			}

			updated, _ = model.Update(accept.key)
			model = updated.(Model)
			if got := model.input.Value(); got != "@main.go" {
				t.Fatalf("%s accepted @file value=%q, want %q", accept.name, got, "@main.go")
			}
		})
	}
}
