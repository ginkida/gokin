package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Model/provider names describe the requested operation, but they are not its
// identity. Both can repeat after request A settles, so every async attempt
// needs an opaque RequestID that is echoed by its result.
func TestModelSelectorLateDuplicateCannotSettleNewerSameTargetSwitch(t *testing.T) {
	type selection struct {
		requestID string
		modelID   string
	}
	var selections []selection

	m := NewModel()
	m.width = 90
	m.SetAvailableModels([]ModelInfo{
		{ID: "fast", Name: "Fast"},
		{ID: "reasoning", Name: "Reasoning"},
	})
	m.SetCurrentModel("fast")
	m.SetModelSelectCallbackWithID(func(requestID, modelID string) {
		selections = append(selections, selection{requestID: requestID, modelID: modelID})
	})

	// A closes the selector and fails, making the same target selectable again.
	m.openModelSelector()
	m.modelSelectedIndex = 1
	_ = m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if len(selections) != 1 || selections[0].requestID == "" || selections[0].modelID != "reasoning" {
		t.Fatalf("switch A callback = %+v, want one owned reasoning request", selections)
	}
	requestA := selections[0].requestID
	_ = m.handleMessageTypes(ModelSelectResultMsg{
		RequestID:   requestA,
		RequestedID: "reasoning",
		ModelID:     "fast",
		Message:     "A failed; previous model restored",
	})
	if m.modelSwitchPending != "" || m.currentModel != "fast" {
		t.Fatalf("setup: switch A did not settle as failure: pending=%q current=%q", m.modelSwitchPending, m.currentModel)
	}

	// B requests the identical target after a close/reopen cycle.
	m.openModelSelector()
	m.modelSelectedIndex = 1
	_ = m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if len(selections) != 2 || selections[1].requestID == "" || selections[1].requestID == requestA || selections[1].modelID != "reasoning" {
		t.Fatalf("switch B lacks a unique owner: A=%q callbacks=%+v", requestA, selections)
	}
	requestB := selections[1].requestID
	m.openModelSelector() // expose B's pending status while its worker runs
	noticeBefore := m.modelSelectorNotice

	// Empty is not a backwards-compatibility wildcard once the active callback
	// is ID-aware. An unowned result must be ignored too.
	_ = m.handleMessageTypes(ModelSelectResultMsg{
		RequestedID: "reasoning",
		ModelID:     "fast",
		Message:     "unowned result without an ID",
	})
	if m.modelSwitchPending != "reasoning" || m.currentModel != "fast" || m.modelSelectorNotice != noticeBefore {
		t.Fatalf("empty RequestID settled owned switch B: pending=%q current=%q notice=%q", m.modelSwitchPending, m.currentModel, m.modelSelectorNotice)
	}

	// A duplicated result has the same semantic target as B. It must be a full
	// no-op: in particular it cannot clear B or replace B's visible status.
	_ = m.handleMessageTypes(ModelSelectResultMsg{
		RequestID:   requestA,
		RequestedID: "reasoning",
		ModelID:     "fast",
		Message:     "late duplicate failure from A",
	})
	if m.modelSwitchPending != "reasoning" || m.currentModel != "fast" {
		t.Errorf("late A settled switch B: pending=%q current=%q", m.modelSwitchPending, m.currentModel)
	}
	if m.modelSelectorNotice != noticeBefore || strings.Contains(m.modelSelectorNotice, "late duplicate") {
		t.Errorf("late A replaced B's selector feedback: before=%q after=%q", noticeBefore, m.modelSelectorNotice)
	}
	if len(selections) != 2 {
		t.Errorf("one user action invoked the selection callback more than once: %+v", selections)
	}

	_ = m.handleMessageTypes(ModelSelectResultMsg{
		RequestID:   requestB,
		RequestedID: "reasoning",
		ModelID:     "reasoning",
		Success:     true,
		Message:     "B succeeded",
	})
	if m.modelSwitchPending != "" || m.currentModel != "reasoning" {
		t.Errorf("matching result B did not settle B: pending=%q current=%q", m.modelSwitchPending, m.currentModel)
	}
}

func TestKeyEntryLateFailureCannotConsumeNewerSameProviderSubmission(t *testing.T) {
	type submission struct {
		requestID string
		provider  string
		key       string
	}
	var submissions []submission

	m := NewModel()
	m.width = 90
	m.SetKeyEntrySubmitCallbackWithID(func(requestID, provider, key string) {
		submissions = append(submissions, submission{requestID: requestID, provider: provider, key: key})
	})

	// A fails and reopens an intentionally empty retry field.
	m.openKeyEntry(OpenKeyEntryMsg{Provider: "glm", DisplayName: "GLM", SetupURL: "https://z.ai/keys"})
	m.keyEntryInput.SetValue("secret-A")
	_ = m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if len(submissions) != 1 || submissions[0].requestID == "" || submissions[0].provider != "glm" || submissions[0].key != "secret-A" {
		t.Fatalf("submission A callback = %+v, want one owned GLM submission", submissions)
	}
	requestA := submissions[0].requestID
	_ = m.handleMessageTypes(KeyEntryResultMsg{
		RequestID: requestA,
		Provider:  "glm",
		Message:   "A was rejected",
	})
	if m.state != StateAPIKeyEntry || m.keyEntryInput.Value() != "" || m.keyEntryPendingProvider != "" {
		t.Fatalf("setup: failure A did not open an empty retry: state=%v value=%q pending=%q", m.state, m.keyEntryInput.Value(), m.keyEntryPendingProvider)
	}

	// Submit B for the same provider, with a newer composer draft underneath.
	m.input.textarea.SetValue("newer composer draft")
	m.keyEntryInput.SetValue("secret-B")
	_ = m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if len(submissions) != 2 || submissions[1].requestID == "" || submissions[1].requestID == requestA || submissions[1].provider != "glm" || submissions[1].key != "secret-B" {
		t.Fatalf("submission B lacks a unique owner: A=%q callbacks=%+v", requestA, submissions)
	}
	requestB := submissions[1].requestID
	outputBefore := m.output.Content()

	_ = m.handleMessageTypes(KeyEntryResultMsg{
		Provider: "glm",
		Message:  "unowned result without an ID",
	})
	if m.state != StateInput || m.keyEntryPendingProvider != "glm" || m.output.Content() != outputBefore {
		t.Fatalf("empty RequestID consumed owned submission B: state=%v pending=%q", m.state, m.keyEntryPendingProvider)
	}

	// Provider equality is insufficient here: this is a duplicate of A, not B.
	// It must not reopen a stale retry, consume B, or disturb the newer draft.
	_ = m.handleMessageTypes(KeyEntryResultMsg{
		RequestID: requestA,
		Provider:  "glm",
		Message:   "late duplicate failure from A",
		Output:    "stale A command output",
	})
	if m.state != StateInput || m.keyEntryPendingProvider != "glm" {
		t.Errorf("late A consumed submission B: state=%v pending=%q", m.state, m.keyEntryPendingProvider)
	}
	if got := m.input.Value(); got != "newer composer draft" {
		t.Errorf("late A changed the newer composer draft: %q", got)
	}
	if got := m.output.Content(); got != outputBefore {
		t.Errorf("late A added stale durable feedback: before=%q after=%q", outputBefore, got)
	}
	if len(submissions) != 2 {
		t.Errorf("one Enter invoked the key callback more than once: %+v", submissions)
	}

	_ = m.handleMessageTypes(KeyEntryResultMsg{
		RequestID: requestB,
		Provider:  "glm",
		Success:   true,
		Message:   "B saved",
	})
	if m.keyEntryPendingProvider != "" || m.state != StateInput {
		t.Errorf("matching result B did not settle B: state=%v pending=%q", m.state, m.keyEntryPendingProvider)
	}
}
