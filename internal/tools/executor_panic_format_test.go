package tools

import (
	"strings"
	"testing"
)

// formatToolPanic must keep the "panic:" prefix so error-classifier matchers
// in context/compactor.go and context/tool_summarizer.go still recognise the
// recovered panic as an error during result summarisation. If you change the
// format, update those matchers in lockstep.
func TestFormatToolPanic_PreservesPanicPrefixAndToolName(t *testing.T) {
	got := formatToolPanic("read", "nil pointer dereference")

	if !strings.HasPrefix(got, "panic:") {
		t.Errorf("missing panic: prefix, got %q", got)
	}
	if !strings.Contains(got, `"read"`) {
		t.Errorf("missing quoted tool name, got %q", got)
	}
	if !strings.Contains(got, "nil pointer dereference") {
		t.Errorf("missing recovered value, got %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "log") {
		t.Errorf("expected hint pointing to log, got %q", got)
	}
}

// The "panic:" substring must survive lowercasing so the case-insensitive
// containers in context/compactor.go (lowerContent) and tool_summarizer.go
// (lower) keep classifying the result as an error.
func TestFormatToolPanic_LowercaseContainsPanicColon(t *testing.T) {
	got := formatToolPanic("bash", "boom")
	if !strings.Contains(strings.ToLower(got), "panic:") {
		t.Errorf("lowercased message must contain %q, got %q", "panic:", got)
	}
}
