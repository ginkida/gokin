package codeintel

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// --- sameRPCID (37.5% → 100%) ---

func TestSameRPCID(t *testing.T) {
	tests := []struct {
		name string
		raw  any
		want int64
		ok   bool
	}{
		{"int match", int(5), 5, true},
		{"int mismatch", int(5), 6, false},
		{"int32 match", int32(3), 3, true},
		{"int32 mismatch", int32(3), 4, false},
		{"int64 match", int64(7), 7, true},
		{"int64 mismatch", int64(7), 8, false},
		{"float64 match", float64(9), 9, true},
		{"float64 mismatch", float64(9), 10, false},
		{"float64 non-integer", float64(9.5), 9, false},
		{"json.Number match", json.Number("11"), 11, true},
		{"json.Number mismatch", json.Number("11"), 12, false},
		{"json.Number invalid", json.Number("abc"), 1, false},
		{"unsupported type", "hello", 1, false},
		{"nil", nil, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameRPCID(tt.raw, tt.want); got != tt.ok {
				t.Fatalf("sameRPCID(%v, %d) = %v, want %v", tt.raw, tt.want, got, tt.ok)
			}
		})
	}
}

func TestSameRPCID_Float64Overflow(t *testing.T) {
	// Values outside int64 range should return false
	if sameRPCID(math.MaxFloat64, 0) {
		t.Fatal("expected false for float64 overflow")
	}
}

// --- nonEmpty (0% → 100%) ---

func TestNonEmpty(t *testing.T) {
	if got := nonEmpty("hello", "fallback"); got != "hello" {
		t.Fatalf("nonEmpty(hello) = %q, want %q", got, "hello")
	}
	if got := nonEmpty("", "fallback"); got != "fallback" {
		t.Fatalf("nonEmpty(empty) = %q, want %q", got, "fallback")
	}
	if got := nonEmpty("  ", "fallback"); got != "fallback" {
		t.Fatalf("nonEmpty(whitespace) = %q, want %q", got, "fallback")
	}
}

// --- retryableFailure.Error (0% → 100%) ---

func TestRetryableFailure_Error(t *testing.T) {
	inner := errors.New("connection lost")
	wrapped := retryable(inner)

	var marked *retryableFailure
	if !errors.As(wrapped, &marked) {
		t.Fatal("expected retryable to wrap in retryableFailure")
	}
	if marked.Error() != "connection lost" {
		t.Fatalf("Error() = %q, want %q", marked.Error(), "connection lost")
	}
	if !errors.Is(marked.Unwrap(), inner) {
		t.Fatal("Unwrap should return inner error")
	}
}

func TestRetryable_NilIsNil(t *testing.T) {
	if err := retryable(nil); err != nil {
		t.Fatalf("retryable(nil) = %v, want nil", err)
	}
}

func TestRetryable_AlreadyMarked(t *testing.T) {
	inner := errors.New("fail")
	once := retryable(inner)
	twice := retryable(once)
	// Should not double-wrap
	if once != twice {
		t.Fatal("retryable on already-marked error should return same error")
	}
}

func TestIsRetryable(t *testing.T) {
	if isRetryable(nil) {
		t.Fatal("isRetryable(nil) should be false")
	}
	if isRetryable(errors.New("plain")) {
		t.Fatal("isRetryable(plain error) should be false")
	}
	if !isRetryable(retryable(errors.New("marked"))) {
		t.Fatal("isRetryable(retryable error) should be true")
	}
}

// --- boundedCapture Write + String (0% → 100%) ---

func TestBoundedCapture_WriteAndString(t *testing.T) {
	b := boundedCapture{limit: 10}

	n, err := b.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write(hello) = (%d, %v), want (5, nil)", n, err)
	}
	if got := b.String(); got != "hello" {
		t.Fatalf("String() = %q, want %q", got, "hello")
	}
}

func TestBoundedCapture_Truncation(t *testing.T) {
	b := boundedCapture{limit: 5}

	// Write more than the limit
	b.Write([]byte("hello world"))
	if got := b.String(); !strings.Contains(got, "[truncated]") {
		t.Fatalf("expected [truncated] suffix, got %q", got)
	}
}

func TestBoundedCapture_EmptyString(t *testing.T) {
	b := boundedCapture{limit: 10}
	if got := b.String(); got != "" {
		t.Fatalf("String() on empty = %q, want %q", got, "")
	}
}

func TestBoundedCapture_PartialWrite(t *testing.T) {
	b := boundedCapture{limit: 5}
	// Write in two chunks: first within limit, second exceeds
	b.Write([]byte("abc"))
	b.Write([]byte("defghij"))
	if got := b.String(); !strings.Contains(got, "[truncated]") {
		t.Fatalf("expected [truncated] after partial overflow, got %q", got)
	}
}

// --- processConnection.Diagnostic (0% → 100%) ---
// Diagnostic just delegates to boundedCapture.String(), which we test above.
// We can test it via a processConnection with a populated diagnostic field.

func TestProcessConnection_Diagnostic(t *testing.T) {
	c := &processConnection{}
	c.diagnostic = boundedCapture{limit: 100, data: []byte("some error")}
	if got := c.Diagnostic(); got != "some error" {
		t.Fatalf("Diagnostic() = %q, want %q", got, "some error")
	}
}

// --- rpcSession.close nil safety (66.7% → 100%) ---

func TestRPCSession_CloseNil(t *testing.T) {
	var s *rpcSession
	if err := s.close(nil); err != nil {
		t.Fatalf("close(nil session) = %v, want nil", err)
	}

	s2 := &rpcSession{conn: nil}
	if err := s2.close(nil); err != nil {
		t.Fatalf("close(nil conn) = %v, want nil", err)
	}
}

// --- rpcSession.notify (60% → 100%) ---

func TestRPCSession_Notify_CancelledContext(t *testing.T) {
	conn := newFakeMCPConnection(nil, nil)
	s := &rpcSession{conn: conn}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := s.notify(ctx, "test", nil); err == nil {
		t.Fatal("expected error for notify on cancelled context")
	}
}

// --- diagnosticSuffix edge cases (75% → 100%) ---

func TestDiagnosticSuffix_Empty(t *testing.T) {
	if got := diagnosticSuffix(""); got != "" {
		t.Fatalf("diagnosticSuffix('') = %q, want %q", got, "")
	}
	if got := diagnosticSuffix("   "); got != "" {
		t.Fatalf("diagnosticSuffix('   ') = %q, want %q", got, "")
	}
}

func TestDiagnosticSuffix_WithContent(t *testing.T) {
	got := diagnosticSuffix("gopls crashed")
	if !strings.Contains(got, "stderr: gopls crashed") {
		t.Fatalf("expected 'stderr:' prefix, got %q", got)
	}
}

// --- withSessionDiagnostic (66.7% → 100%) ---

func TestWithSessionDiagnostic_NilError(t *testing.T) {
	if err := withSessionDiagnostic(nil, &rpcSession{}); err != nil {
		t.Fatalf("withSessionDiagnostic(nil err) = %v, want nil", err)
	}
}

func TestWithSessionDiagnostic_NilSession(t *testing.T) {
	original := errors.New("fail")
	if err := withSessionDiagnostic(original, nil); err != original {
		t.Fatalf("withSessionDiagnostic(nil session) should return original error")
	}
}

// --- nonNilContext (66.7% → 100%) ---

func TestNonNilContext_Nil(t *testing.T) {
	ctx := nonNilContext(nil)
	if ctx == nil {
		t.Fatal("nonNilContext(nil) should return context.Background()")
	}
}

func TestNonNilContext_NonNil(t *testing.T) {
	original, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if got := nonNilContext(original); got != original {
		t.Fatal("nonNilContext(non-nil) should return same context")
	}
}

// --- callResultText edge case (85.7% → 100%) ---

func TestCallResultText_NilResult(t *testing.T) {
	if got := callResultText(nil); got != "" {
		t.Fatalf("callResultText(nil) = %q, want %q", got, "")
	}
}
