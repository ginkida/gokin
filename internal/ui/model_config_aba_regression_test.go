package ui

import "testing"

func TestNewerConfigReturningToBaseSupersedesLateModelResult(t *testing.T) {
	m := NewModel()
	m.SetAvailableModels([]ModelInfo{{ID: "base"}, {ID: "selector-target"}})
	_ = m.handleMessageTypes(ConfigUpdateMsg{Revision: 10, ModelName: "base"})

	var requestID string
	m.SetModelSelectCallbackWithID(func(id, modelID string) {
		requestID = id
	})
	m.state = StateModelSelector
	m.modelSelectedIndex = 1
	_ = m.selectModelAt(1)
	if requestID == "" {
		t.Fatal("selector request did not get an ID")
	}

	// A's ApplyConfig publishes its snapshot before its result. A later config
	// operation then returns the model to the original value while A's result is
	// still queued. Numeric revision, not model-value inequality, owns ordering.
	_ = m.handleMessageTypes(ConfigUpdateMsg{Revision: 11, ModelName: "selector-target"})
	_ = m.handleMessageTypes(ConfigUpdateMsg{Revision: 12, ModelName: "base"})
	_ = m.handleMessageTypes(ModelSelectResultMsg{
		RequestID:   requestID,
		Revision:    11,
		RequestedID: "selector-target",
		ModelID:     "selector-target",
		Success:     true,
		Message:     "late selector success",
	})

	if m.currentModel != "base" {
		t.Fatalf("late selector result overwrote newer ABA config: current=%q", m.currentModel)
	}
	if m.modelSwitchPending != "" || m.modelSwitchPendingID != "" {
		t.Fatalf("newer ABA config retained stale selector ownership: pending=%q id=%q",
			m.modelSwitchPending, m.modelSwitchPendingID)
	}
}
