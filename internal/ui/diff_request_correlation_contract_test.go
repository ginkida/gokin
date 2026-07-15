package ui

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func updateDiffCorrelationModel(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(msg)
	got, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want ui.Model", updated)
	}
	return got, cmd
}

func TestDiffDecisionCallbackWithIDOwnsCollisionAndVisibleResponse(t *testing.T) {
	type response struct {
		id       string
		decision DiffDecision
	}
	var responses []response

	m := *NewModel()
	m.SetDiffDecisionCallbackWithID(func(id string, decision DiffDecision) {
		responses = append(responses, response{id: id, decision: decision})
	})

	visible, _ := updateDiffCorrelationModel(t, m, DiffPreviewRequestMsg{
		RequestID: "diff-A", FilePath: "a.go", OldContent: "old\n", NewContent: "new\n",
	})
	visible, _ = updateDiffCorrelationModel(t, visible, DiffPreviewRequestMsg{
		RequestID: "diff-B", FilePath: "b.go", OldContent: "old\n", NewContent: "new\n",
	})
	if !reflect.DeepEqual(responses, []response{{id: "diff-B", decision: DiffReject}}) {
		t.Fatalf("collision response=%v, want rejection owned by incoming diff-B", responses)
	}
	if visible.state != StateDiffPreview || visible.diffRequest == nil || visible.diffRequest.RequestID != "diff-A" {
		t.Fatalf("collision clobbered visible diff-A: state=%v request=%+v", visible.state, visible.diffRequest)
	}

	decided, responseCmd := updateDiffCorrelationModel(t, visible, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if responseCmd == nil {
		t.Fatal("visible diff-A did not emit a response")
	}
	settled, _ := updateDiffCorrelationModel(t, decided, responseCmd())
	want := []response{
		{id: "diff-B", decision: DiffReject},
		{id: "diff-A", decision: DiffApply},
	}
	if !reflect.DeepEqual(responses, want) {
		t.Fatalf("ID-aware normal response=%v, want %v", responses, want)
	}
	if settled.diffRequest != nil || settled.state != StateProcessing {
		t.Errorf("matching normal response did not settle diff-A: state=%v request=%+v", settled.state, settled.diffRequest)
	}
}

func TestMultiDiffDecisionCallbackWithIDOwnsCollisionAndVisibleResponse(t *testing.T) {
	type response struct {
		id        string
		decisions map[string]DiffDecision
	}
	var responses []response

	m := *NewModel()
	m.SetMultiDiffDecisionCallbackWithID(func(id string, decisions map[string]DiffDecision) {
		responses = append(responses, response{id: id, decisions: cloneDiffDecisions(decisions)})
	})

	visible, _ := updateDiffCorrelationModel(t, m, MultiDiffPreviewRequestMsg{
		RequestID: "multi-A",
		Files: []DiffFile{{
			FilePath: "a.go", OldContent: "old\n", NewContent: "new\n",
		}},
	})
	visible, _ = updateDiffCorrelationModel(t, visible, MultiDiffPreviewRequestMsg{
		RequestID: "multi-B",
		Files: []DiffFile{{
			FilePath: "b.go", OldContent: "old\n", NewContent: "new\n",
		}},
	})
	if len(responses) != 1 || responses[0].id != "multi-B" ||
		!reflect.DeepEqual(responses[0].decisions, map[string]DiffDecision{"b.go": DiffReject}) {
		t.Fatalf("multi collision response=%v, want rejection owned by incoming multi-B", responses)
	}
	if visible.state != StateMultiDiffPreview || visible.multiDiffRequest == nil || visible.multiDiffRequest.RequestID != "multi-A" {
		t.Fatalf("collision clobbered visible multi-A: state=%v request=%+v", visible.state, visible.multiDiffRequest)
	}

	decided, responseCmd := updateDiffCorrelationModel(t, visible, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if responseCmd == nil {
		t.Fatal("visible multi-A did not emit a response")
	}
	settled, _ := updateDiffCorrelationModel(t, decided, responseCmd())
	if len(responses) != 2 || responses[1].id != "multi-A" ||
		!reflect.DeepEqual(responses[1].decisions, map[string]DiffDecision{"a.go": DiffApply}) {
		t.Fatalf("ID-aware multi normal response=%v, want apply owned by multi-A", responses)
	}
	if settled.multiDiffRequest != nil || settled.state != StateProcessing {
		t.Errorf("matching normal response did not settle multi-A: state=%v request=%+v", settled.state, settled.multiDiffRequest)
	}
}

func TestDiffPromptExpiryRequiresMatchingKindAndRequestID(t *testing.T) {
	tests := []struct {
		name      string
		kind      PromptKind
		wrongKind PromptKind
		open      func(*testing.T, Model, string) Model
		active    func(Model, string) bool
	}{
		{
			name:      "single diff",
			kind:      PromptKindDiff,
			wrongKind: PromptKindMultiDiff,
			open: func(t *testing.T, m Model, id string) Model {
				got, _ := updateDiffCorrelationModel(t, m, DiffPreviewRequestMsg{
					RequestID: id, FilePath: "same.go", OldContent: "old\n", NewContent: "new\n",
				})
				return got
			},
			active: func(m Model, id string) bool {
				return m.state == StateDiffPreview && m.diffRequest != nil &&
					m.diffRequest.RequestID == id && m.diffPreview.requestID == id
			},
		},
		{
			name:      "multi diff",
			kind:      PromptKindMultiDiff,
			wrongKind: PromptKindDiff,
			open: func(t *testing.T, m Model, id string) Model {
				got, _ := updateDiffCorrelationModel(t, m, MultiDiffPreviewRequestMsg{
					RequestID: id,
					Files: []DiffFile{{
						FilePath: "same.go", OldContent: "old\n", NewContent: "new\n",
					}},
				})
				return got
			},
			active: func(m Model, id string) bool {
				return m.state == StateMultiDiffPreview && m.multiDiffRequest != nil &&
					m.multiDiffRequest.RequestID == id && m.multiDiffPreview.requestID == id
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := *NewModel()
			visibleA := tc.open(t, m, "request-A")

			wrongKind, _ := updateDiffCorrelationModel(t, visibleA, PromptExpiredMsg{
				Kind: tc.wrongKind, ID: "request-A", Message: "wrong kind",
			})
			if !tc.active(wrongKind, "request-A") {
				t.Fatalf("same ID with wrong kind closed request-A: state=%v", wrongKind.state)
			}

			wrongID, _ := updateDiffCorrelationModel(t, wrongKind, PromptExpiredMsg{
				Kind: tc.kind, ID: "request-other", Message: "wrong ID",
			})
			if !tc.active(wrongID, "request-A") {
				t.Fatalf("foreign expiry closed request-A: state=%v", wrongID.state)
			}

			expiredA, _ := updateDiffCorrelationModel(t, wrongID, PromptExpiredMsg{
				Kind: tc.kind, ID: "request-A", Message: "request A expired",
			})
			if tc.active(expiredA, "request-A") || expiredA.isModalState() {
				t.Fatalf("matching expiry did not close request-A: state=%v", expiredA.state)
			}

			visibleB := tc.open(t, expiredA, "request-B")
			lateA, _ := updateDiffCorrelationModel(t, visibleB, PromptExpiredMsg{
				Kind: tc.kind, ID: "request-A", Message: "late request A expiry",
			})
			if !tc.active(lateA, "request-B") {
				t.Fatalf("late expiry for A closed newer request-B: state=%v", lateA.state)
			}
		})
	}
}
