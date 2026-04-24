package http

import (
	"context"
	"testing"

	"example.com/go-audit-request-id/internal/audit"
)

func TestRecordAuditIncludesRequestID(t *testing.T) {
	logger := audit.NewLogger()
	ctx := WithRequestID(context.Background(), "req-123")

	RecordAudit(ctx, logger, "login", "user-1")

	events := logger.Events()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Action != "login" || events[0].UserID != "user-1" {
		t.Fatalf("event fields not preserved: %+v", events[0])
	}
	// Eval target: add RequestID to audit.Event and thread it through RecordAudit.
	if got := ""; got != "req-123" {
		t.Fatalf("RequestID = %q, want req-123", got)
	}
}
