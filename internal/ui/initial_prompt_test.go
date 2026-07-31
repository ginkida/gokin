package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestInitialPromptMessageUsesNormalSubmissionLifecycle(t *testing.T) {
	model := NewModel()
	var submitted string
	model.SetCallbacks(func(message string) {
		submitted = message
	}, nil)

	updated, cmd := model.Update(InitialPromptMsg("inspect repository"))
	got := updated.(Model)
	if submitted != "inspect repository" {
		t.Fatalf("initial prompt submitted %q", submitted)
	}
	if got.state != StateProcessing {
		t.Fatalf("initial prompt state = %v, want processing", got.state)
	}
	if cmd == nil {
		t.Fatal("initial prompt did not consume its synthetic event")
	}
}

func TestModelInitSchedulesConfiguredInitialPrompt(t *testing.T) {
	model := NewModel()
	model.SetInitialPrompt("first task")
	msg := model.Init()()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("Init message = %T, want tea.BatchMsg", msg)
	}
	for _, cmd := range batch {
		if cmd == nil {
			continue
		}
		if prompt, ok := cmd().(InitialPromptMsg); ok {
			if prompt != "first task" {
				t.Fatalf("initial prompt = %q", prompt)
			}
			return
		}
	}
	t.Fatal("Init batch did not contain InitialPromptMsg")
}
