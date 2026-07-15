package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func updatePromptCorrelationModel(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	updated, _ := m.Update(msg)
	got, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want ui.Model", updated)
	}
	return got
}

func newPromptCorrelationModel() Model {
	m := NewModel()
	m.width = 100
	m.height = 40
	m.state = StateInput
	return *m
}

func TestQuestionCollisionAndSubmitCallbacksCarryTheirOwnRequestIDs(t *testing.T) {
	type response struct {
		id     string
		answer string
	}
	var responses []response

	m := newPromptCorrelationModel()
	m.SetQuestionCallbackWithID(func(id, answer string) {
		responses = append(responses, response{id: id, answer: answer})
	})

	m = updatePromptCorrelationModel(t, m, QuestionRequestMsg{
		ID:       "question-A",
		Question: "Choose for A",
		Options:  []string{"answer-A"},
	})
	m = updatePromptCorrelationModel(t, m, QuestionRequestMsg{
		ID:       "question-B",
		Question: "Choose for B",
		Options:  []string{"answer-B"},
	})

	if len(responses) != 1 || responses[0] != (response{id: "question-B", answer: ""}) {
		t.Fatalf("collision response = %+v, want cancellation for incoming B only", responses)
	}
	if m.questionRequest == nil || m.questionRequest.ID != "question-A" || m.state != StateQuestionPrompt {
		t.Fatalf("incoming B clobbered visible A: state=%v request=%+v", m.state, m.questionRequest)
	}

	m = updatePromptCorrelationModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	if len(responses) != 2 || responses[1] != (response{id: "question-A", answer: "answer-A"}) {
		t.Fatalf("visible A submit response = %+v, want answer owned by A", responses)
	}
}

func TestPlanCollisionAndDecisionCallbacksCarryTheirOwnRequestIDs(t *testing.T) {
	type response struct {
		id       string
		decision PlanApprovalDecision
	}
	var responses []response

	m := newPromptCorrelationModel()
	m.SetPlanApprovalCallbackWithID(func(id string, decision PlanApprovalDecision) {
		responses = append(responses, response{id: id, decision: decision})
	})

	m = updatePromptCorrelationModel(t, m, PlanApprovalRequestMsg{
		ID:    "plan-A",
		Title: "Plan A",
		Steps: []PlanStepInfo{{ID: 1, Title: "A"}},
	})
	m = updatePromptCorrelationModel(t, m, PlanApprovalRequestMsg{
		ID:    "plan-B",
		Title: "Plan B",
		Steps: []PlanStepInfo{{ID: 1, Title: "B"}},
	})

	if len(responses) != 1 || responses[0] != (response{id: "plan-B", decision: PlanRejected}) {
		t.Fatalf("collision response = %+v, want rejection for incoming B only", responses)
	}
	if m.planRequest == nil || m.planRequest.ID != "plan-A" || m.state != StatePlanApproval {
		t.Fatalf("incoming B clobbered visible A: state=%v request=%+v", m.state, m.planRequest)
	}

	m = updatePromptCorrelationModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if len(responses) != 2 || responses[1] != (response{id: "plan-A", decision: PlanRejected}) {
		t.Fatalf("visible A decision response = %+v, want decision owned by A", responses)
	}
}

func TestPlanFeedbackCallbackCarriesRequestID(t *testing.T) {
	var gotID string
	var gotDecision PlanApprovalDecision
	var gotFeedback string
	var calls int

	m := newPromptCorrelationModel()
	m.SetPlanApprovalWithFeedbackCallbackWithID(func(id string, decision PlanApprovalDecision, feedback string) {
		calls++
		gotID, gotDecision, gotFeedback = id, decision, feedback
	})
	m = updatePromptCorrelationModel(t, m, PlanApprovalRequestMsg{
		ID:    "plan-feedback",
		Title: "Migration",
		Steps: []PlanStepInfo{{ID: 1, Title: "Preserve compatibility"}},
	})
	m = updatePromptCorrelationModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if !m.planFeedbackMode {
		t.Fatal("request-changes action did not enter feedback mode with ID-aware callback")
	}
	m.planFeedbackInput.textarea.SetValue("Keep the public API stable")
	m = updatePromptCorrelationModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if calls != 1 || gotID != "plan-feedback" || gotDecision != PlanModifyRequested || gotFeedback != "Keep the public API stable" {
		t.Fatalf("feedback callback: calls=%d id=%q decision=%v feedback=%q", calls, gotID, gotDecision, gotFeedback)
	}
}

func TestQuestionPromptExpiryRequiresMatchingKindAndRequestID(t *testing.T) {
	m := newPromptCorrelationModel()
	m = updatePromptCorrelationModel(t, m, QuestionRequestMsg{ID: "question-A", Question: "A?"})

	// Same ID but wrong prompt kind must not close the question.
	m = updatePromptCorrelationModel(t, m, PromptExpiredMsg{Kind: "plan", ID: "question-A", Message: "wrong kind"})
	if m.questionRequest == nil || m.questionRequest.ID != "question-A" || m.state != StateQuestionPrompt {
		t.Fatalf("wrong-kind expiry closed question A: state=%v request=%+v", m.state, m.questionRequest)
	}

	// Same kind but another request ID must not close the question either.
	m = updatePromptCorrelationModel(t, m, PromptExpiredMsg{Kind: "question", ID: "question-other", Message: "wrong ID"})
	if m.questionRequest == nil || m.questionRequest.ID != "question-A" || m.state != StateQuestionPrompt {
		t.Fatalf("foreign expiry closed question A: state=%v request=%+v", m.state, m.questionRequest)
	}

	m = updatePromptCorrelationModel(t, m, PromptExpiredMsg{Kind: "question", ID: "question-A", Message: "Question timed out"})
	if m.questionRequest != nil || m.state == StateQuestionPrompt {
		t.Fatalf("matching expiry did not close question A: state=%v request=%+v", m.state, m.questionRequest)
	}

	// A delayed duplicate expiry for A must not close the newer prompt B.
	m = updatePromptCorrelationModel(t, m, QuestionRequestMsg{ID: "question-B", Question: "B?"})
	m = updatePromptCorrelationModel(t, m, PromptExpiredMsg{Kind: "question", ID: "question-A", Message: "late duplicate"})
	if m.questionRequest == nil || m.questionRequest.ID != "question-B" || m.state != StateQuestionPrompt {
		t.Fatalf("late expiry for A closed newer question B: state=%v request=%+v", m.state, m.questionRequest)
	}
}

func TestPlanPromptExpiryRequiresMatchingKindAndRequestID(t *testing.T) {
	m := newPromptCorrelationModel()
	m = updatePromptCorrelationModel(t, m, PlanApprovalRequestMsg{
		ID: "plan-A", Title: "Plan A", Steps: []PlanStepInfo{{ID: 1, Title: "A"}},
	})

	m = updatePromptCorrelationModel(t, m, PromptExpiredMsg{Kind: "question", ID: "plan-A", Message: "wrong kind"})
	if m.planRequest == nil || m.planRequest.ID != "plan-A" || m.state != StatePlanApproval {
		t.Fatalf("wrong-kind expiry closed plan A: state=%v request=%+v", m.state, m.planRequest)
	}

	m = updatePromptCorrelationModel(t, m, PromptExpiredMsg{Kind: "plan", ID: "plan-other", Message: "wrong ID"})
	if m.planRequest == nil || m.planRequest.ID != "plan-A" || m.state != StatePlanApproval {
		t.Fatalf("foreign expiry closed plan A: state=%v request=%+v", m.state, m.planRequest)
	}

	m = updatePromptCorrelationModel(t, m, PromptExpiredMsg{Kind: "plan", ID: "plan-A", Message: "Plan approval timed out"})
	if m.planRequest != nil || m.state == StatePlanApproval {
		t.Fatalf("matching expiry did not close plan A: state=%v request=%+v", m.state, m.planRequest)
	}

	m = updatePromptCorrelationModel(t, m, PlanApprovalRequestMsg{
		ID: "plan-B", Title: "Plan B", Steps: []PlanStepInfo{{ID: 1, Title: "B"}},
	})
	m = updatePromptCorrelationModel(t, m, PromptExpiredMsg{Kind: "plan", ID: "plan-A", Message: "late duplicate"})
	if m.planRequest == nil || m.planRequest.ID != "plan-B" || m.state != StatePlanApproval {
		t.Fatalf("late expiry for A closed newer plan B: state=%v request=%+v", m.state, m.planRequest)
	}
}

func TestPromptExpiryTreatsRequestIDAsAnOpaqueToken(t *testing.T) {
	// IDs are identity, not display copy. Normalizing whitespace, stripping ANSI,
	// or otherwise sanitizing an ID before comparison makes the exact same token
	// fail to match the request that owns it. Only Message should be sanitized
	// before rendering.
	const opaqueID = "question-opaque  token"
	m := newPromptCorrelationModel()
	m = updatePromptCorrelationModel(t, m, QuestionRequestMsg{
		ID:       opaqueID,
		Question: "Does exact identity survive?",
	})

	m = updatePromptCorrelationModel(t, m, PromptExpiredMsg{
		Kind:    PromptKindQuestion,
		ID:      opaqueID,
		Message: "Question expired",
	})

	if m.questionRequest != nil || m.state == StateQuestionPrompt {
		t.Fatalf("expiry normalized an opaque request ID instead of matching it exactly: state=%v request=%+v", m.state, m.questionRequest)
	}
}
