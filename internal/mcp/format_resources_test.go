package mcp

import (
	"strings"
	"testing"
)

func TestFormatResources_NilManager(t *testing.T) {
	if got := FormatResources(nil); !strings.Contains(got, "not initialised") {
		t.Fatalf("nil manager: got %q, want 'not initialised' hint", got)
	}
}

func TestFormatResources_NoResources(t *testing.T) {
	// Zero-value manager has no connected clients ⇒ no resources ⇒ stable hint
	// rather than an empty string the model would misread as a tool failure.
	mgr := &Manager{}
	got := FormatResources(mgr)
	if !strings.Contains(got, "No MCP resources available") {
		t.Fatalf("empty manager: got %q, want 'No MCP resources available' hint", got)
	}
}
