package ui

import (
	"strings"
	"testing"
)

func TestReviewedRevisionHasBoundaryAndOwnCopyPayload(t *testing.T) {
	m := *NewModel()
	m.output.SetSize(100, 24)
	m.input.textarea.SetValue("next draft")
	updated, _ := m.Update(StreamTextMsg("Original draft with an unfinished `inline"))
	m = updated.(Model)
	updated, _ = m.Update(ResponseRevisedMsg{Text: "Corrected result\n\nVerification failed; more work is needed."})
	m = updated.(Model)
	updated, _ = m.Update(ResponseDoneMsg{})
	m = updated.(Model)
	plain := stripAnsi(m.output.Content())
	if !strings.Contains(plain, "Updated result") || !strings.Contains(plain, "Corrected result") || strings.Contains(plain, "inlineCorrected") {
		t.Fatalf("revision glued answers together: %s", plain)
	}
	if m.lastResponseText != "Corrected result\n\nVerification failed; more work is needed." || m.input.Value() != "next draft" {
		t.Fatalf("revision copy/draft is wrong: %q %q", m.lastResponseText, m.input.Value())
	}
}

func TestReviewedRevisionPreservesOpenTranscriptAndDraft(t *testing.T) {
	m := claudeComposer(t)
	m.state = StateStreaming
	m.input.textarea.SetValue("preserved draft")
	m.openTranscript()
	updated, _ := m.Update(ResponseRevisedMsg{Text: "Corrected answer"})
	m = updated.(Model)
	if m.state != StateTranscript || m.input.Value() != "preserved draft" {
		t.Fatal("revision dismissed the reader or lost its draft")
	}
}
