package app

import (
	"strings"
	"testing"
)

func TestJournalReportShowsStructuredTimeoutWithoutPromptPreview(t *testing.T) {
	journal, err := NewExecutionJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Append("request_failed", map[string]any{
		"message_preview": "private user prompt that should not be repeated",
		"failure_reason":  "model_round_timeout",
		"provider":        "kimi",
		"timeout":         "14m0s",
		"partial":         true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Append("auto_resume_scheduled", map[string]any{
		"message_preview": "another private prompt",
		"failure_reason":  "model_round_timeout",
		"provider":        "kimi",
		"attempt":         1,
		"max_attempts":    2,
	}); err != nil {
		t.Fatal(err)
	}

	report := (&App{journal: journal}).GetJournalReport()
	for _, want := range []string{
		"request_failed | model_round_timeout · kimi · 14m0s · partial response",
		"auto_resume_scheduled | model_round_timeout · kimi · attempt 1/2",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("journal report missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "private user prompt") || strings.Contains(report, "another private prompt") {
		t.Fatalf("structured failure report repeated prompt text:\n%s", report)
	}
}

func TestJournalEntryDisplayDetailFallsBackForLegacyEvents(t *testing.T) {
	got := journalEntryDisplayDetail(JournalEntry{
		Event:   "auto_resume_scheduled",
		Details: map[string]any{"reason": "model round timeout", "attempt": float64(2)},
	})
	if got != "model round timeout · attempt 2" {
		t.Fatalf("legacy recovery detail = %q", got)
	}
	got = journalEntryDisplayDetail(JournalEntry{
		Event:   "request_started",
		Details: map[string]any{"message_preview": "inspect files"},
	})
	if got != "inspect files" {
		t.Fatalf("ordinary journal preview = %q", got)
	}
}
