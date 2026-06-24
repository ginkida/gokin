package state

import "testing"

func TestArchivedString(t *testing.T) {
	if got := StatusArchived.String(); got != "archived" {
		t.Fatalf("StatusArchived.String() = %q, want %q", got, "archived")
	}
}

func TestParseArchived(t *testing.T) {
	got, err := Parse("archived")
	if err != nil {
		t.Fatalf("Parse(\"archived\") returned error: %v", err)
	}
	if got != StatusArchived {
		t.Fatalf("Parse(\"archived\") = %v, want StatusArchived", got)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, s := range []Status{StatusDraft, StatusActive, StatusClosed, StatusArchived} {
		name := s.String()
		got, err := Parse(name)
		if err != nil {
			t.Fatalf("Parse(%q) returned error: %v", name, err)
		}
		if got != s {
			t.Fatalf("round-trip Parse(%q) = %v, want %v", name, got, s)
		}
	}
}

func TestTransitionsToArchived(t *testing.T) {
	if !CanTransition(StatusActive, StatusArchived) {
		t.Fatalf("CanTransition(Active, Archived) = false, want true")
	}
	if !CanTransition(StatusClosed, StatusArchived) {
		t.Fatalf("CanTransition(Closed, Archived) = false, want true")
	}
}

func TestArchivedIsTerminal(t *testing.T) {
	if CanTransition(StatusArchived, StatusActive) {
		t.Fatalf("CanTransition(Archived, Active) = true, want false (terminal)")
	}
}
