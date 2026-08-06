package ui

import (
	"errors"
	"strings"
	"testing"
)

func TestModelRoundTimeoutPromotesPartialResponseToCopyPayload(t *testing.T) {
	m := *NewModel()
	m.output.SetSize(100, 20)
	m.lastResponseText = "older completed answer"

	updated, _ := m.Update(StreamTextMsg("useful timeout partial"))
	m = updated.(Model)
	updated, _ = m.Update(ErrorMsg(errors.New(
		"model response error (model_round_timeout): model_round_timeout (14m0s): model round timeout")))
	m = updated.(Model)

	if got := m.lastResponseText; got != "useful timeout partial" {
		t.Fatalf("copy payload = %q, want preserved timeout partial", got)
	}
	if !m.lastResponseWasPartial {
		t.Fatal("preserved timeout response was not marked partial")
	}
	if got := m.currentResponseBuf.String(); got != "" {
		t.Fatalf("terminal timeout retained active response buffer: %q", got)
	}
	output := stripAnsi(m.output.Content())
	for _, want := range []string{"Partial response preserved", "Alt+C"} {
		if !strings.Contains(output, want) {
			t.Fatalf("timeout UX missing %q:\n%s", want, output)
		}
	}

	updated, _ = m.Update(StreamTextMsg("fresh complete answer"))
	m = updated.(Model)
	updated, _ = m.Update(ResponseDoneMsg{})
	m = updated.(Model)
	if m.lastResponseText != "fresh complete answer" || m.lastResponseWasPartial {
		t.Fatalf("fresh completion did not replace partial copy payload: text=%q partial=%v",
			m.lastResponseText, m.lastResponseWasPartial)
	}
}

func TestUnrelatedErrorDoesNotReplaceLastCompletedCopyPayload(t *testing.T) {
	m := *NewModel()
	m.output.SetSize(100, 20)
	m.lastResponseText = "older completed answer"

	updated, _ := m.Update(StreamTextMsg("unowned concurrent stream text"))
	m = updated.(Model)
	updated, _ = m.Update(ErrorMsg(errors.New("background integration failed")))
	m = updated.(Model)

	if got := m.lastResponseText; got != "older completed answer" {
		t.Fatalf("unrelated error replaced copy payload with %q", got)
	}
	if m.lastResponseWasPartial {
		t.Fatal("unrelated error marked the prior completed response partial")
	}
	if strings.Contains(stripAnsi(m.output.Content()), "Partial response preserved") {
		t.Fatal("unrelated error claimed partial-response preservation")
	}
}

func TestShouldPreservePartialResponseForErrorClassification(t *testing.T) {
	tests := []struct {
		message string
		want    bool
	}{
		{"model response error (model_round_timeout): model round timeout", true},
		{"function response error: stream idle timeout after partial response", true},
		{"model response error (http_timeout): context deadline exceeded", true},
		{"context canceled", false},
		{"generic provider error", false},
	}
	for _, test := range tests {
		if got := shouldPreservePartialResponseForError(test.message); got != test.want {
			t.Errorf("shouldPreservePartialResponseForError(%q) = %v, want %v", test.message, got, test.want)
		}
	}
}
