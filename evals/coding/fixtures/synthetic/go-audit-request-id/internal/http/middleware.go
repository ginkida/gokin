package http

import (
	"context"

	"example.com/go-audit-request-id/internal/audit"
)

type contextKey string

const requestIDKey contextKey = "request_id"

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

func RecordAudit(ctx context.Context, logger *audit.Logger, action, userID string) {
	logger.Record(action, userID)
}
