package ui

import (
	"strings"
	"testing"
)

func TestNewerConfigModelSupersedesLateSelectorResult(t *testing.T) {
	m := NewModel()
	m.SetAvailableModels([]ModelInfo{{ID: "base"}, {ID: "selector-target"}})
	_ = m.handleMessageTypes(ConfigUpdateMsg{Revision: 10, ModelName: "base"})

	var requestID string
	m.SetModelSelectCallbackWithID(func(id, modelID string) {
		requestID = id
		if modelID != "selector-target" {
			t.Fatalf("selected model=%q", modelID)
		}
	})
	m.state = StateModelSelector
	m.modelSelectedIndex = 1
	_ = m.selectModelAt(1)
	if requestID == "" || m.modelSwitchPending != "selector-target" {
		t.Fatalf("selector request not staged: id=%q pending=%q", requestID, m.modelSwitchPending)
	}

	_ = m.handleMessageTypes(ConfigUpdateMsg{Revision: 12, ModelName: "external-newer"})
	if m.modelSwitchPending != "" || m.modelSwitchPendingID != "" {
		t.Fatalf("newer model config did not release stale selector ownership: pending=%q id=%q",
			m.modelSwitchPending, m.modelSwitchPendingID)
	}
	outputBefore := m.output.Content()
	noticeBefore := m.modelSelectorNotice
	_ = m.handleMessageTypes(ModelSelectResultMsg{
		RequestID:   requestID,
		RequestedID: "selector-target",
		ModelID:     "selector-target",
		Success:     true,
		Message:     "stale selector success",
	})

	if m.currentModel != "external-newer" || m.modelSwitchPending != "" || m.modelSwitchPendingID != "" {
		t.Fatalf("late selector result replaced newer config: current=%q pending=%q id=%q",
			m.currentModel, m.modelSwitchPending, m.modelSwitchPendingID)
	}
	if m.output.Content() != outputBefore || m.modelSelectorNotice != noticeBefore ||
		strings.Contains(m.output.Content(), "stale selector") || strings.Contains(m.modelSelectorNotice, "stale selector") {
		t.Fatalf("late selector result leaked feedback: notice=%q output=%q", m.modelSelectorNotice, m.output.Content())
	}
}
