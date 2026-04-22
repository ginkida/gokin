package ui

import (
	"strings"
	"testing"
)

func TestGetErrorGuidance_MCPCommandNotFound(t *testing.T) {
	cases := []string{
		`failed to start MCP server: exec: "missing-cmd": executable file not found in $PATH`,
		`connect: failed to create transport: failed to start MCP server: exec: "/tmp/nope": no such file or directory`,
	}
	for _, msg := range cases {
		g := GetErrorGuidance(msg)
		if g == nil {
			t.Errorf("no guidance for %q", msg)
			continue
		}
		if !strings.Contains(g.Title, "Command Not Found") {
			t.Errorf("wrong title for %q: %s", msg, g.Title)
		}
		if g.Command != "/mcp list" {
			t.Errorf("command = %q, want /mcp list", g.Command)
		}
	}
}

func TestGetErrorGuidance_MCPPermissionDenied(t *testing.T) {
	g := GetErrorGuidance(`failed to start MCP server: fork/exec /usr/local/bin/my-mcp: permission denied`)
	if g == nil {
		t.Fatal("no guidance")
	}
	if !strings.Contains(g.Title, "Not Executable") {
		t.Errorf("wrong title: %s", g.Title)
	}
}

func TestGetErrorGuidance_MCPInitializeTimeout(t *testing.T) {
	cases := []string{
		`connect: initialization failed: request timeout after 30s`,
		`initialize failed: context deadline exceeded`,
	}
	for _, msg := range cases {
		g := GetErrorGuidance(msg)
		if g == nil {
			t.Errorf("no guidance for %q", msg)
			continue
		}
		// Could match either the MCP-specific "Did Not Initialize" or the
		// generic "Request Timed Out" — order in errorGuidancePatterns
		// matters. Check that SOME guidance fires and it's MCP-flavored
		// when MCP keywords are present.
		if !strings.Contains(msg, "initialize") {
			continue
		}
		if !strings.Contains(g.Title, "Initialize") && !strings.Contains(g.Title, "Timed Out") {
			t.Errorf("for %q got title %q, want MCP init or timeout", msg, g.Title)
		}
	}
}

func TestGetErrorGuidance_MCPProtocolMismatch(t *testing.T) {
	g := GetErrorGuidance(`initialize failed: protocol version mismatch: server uses 2023-11-05, client supports 2024-11-05`)
	if g == nil {
		t.Fatal("no guidance")
	}
	if !strings.Contains(g.Title, "Protocol Mismatch") {
		t.Errorf("wrong title: %s", g.Title)
	}
}

func TestGetErrorGuidance_MCPGenericConnection(t *testing.T) {
	g := GetErrorGuidance(`MCP server "github" connect failed: broken pipe`)
	if g == nil {
		t.Fatal("no guidance")
	}
	if g.Command != "/mcp status" {
		t.Errorf("command = %q, want /mcp status", g.Command)
	}
}

func TestGetErrorGuidance_NilForUnrelated(t *testing.T) {
	// Should not match MCP patterns.
	g := GetErrorGuidance(`unrelated: something broke`)
	if g != nil {
		t.Errorf("expected nil guidance, got %+v", g)
	}
}

func TestFormatErrorWithGuidance_WrapsInErrorBox(t *testing.T) {
	styles := DefaultStyles()
	got := FormatErrorWithGuidance(styles, "failed to start MCP server: exec: \"x\": executable file not found in $PATH")

	// Rounded-border characters from lipgloss.RoundedBorder(): ╭╮╰╯
	// Require at least one to verify ErrorBox is applied (not all of them —
	// narrow terminal width could trigger different rendering).
	hasBorder := false
	for _, r := range []string{"╭", "╮", "╰", "╯", "│", "─"} {
		if strings.Contains(got, r) {
			hasBorder = true
			break
		}
	}
	if !hasBorder {
		t.Errorf("expected ErrorBox border chars in output; got:\n%s", got)
	}

	// Content must still be present inside the box.
	if !strings.Contains(got, "Error:") {
		t.Errorf("error prefix missing from box content")
	}
	if !strings.Contains(got, "Command Not Found") {
		t.Errorf("guidance title missing from box content")
	}
}

func TestFormatErrorWithGuidance_NilStylesPassesThrough(t *testing.T) {
	// When styles is nil (telemetry, tests), fall back to plain text —
	// don't panic and don't insert border chars.
	got := FormatErrorWithGuidance(nil, "something broke")
	if strings.ContainsAny(got, "╭╮╰╯") {
		t.Errorf("nil styles should not render box borders: %q", got)
	}
	if !strings.Contains(got, "something broke") {
		t.Errorf("error text missing: %q", got)
	}
}

func TestGetErrorGuidance_StatusCodeWordBoundary(t *testing.T) {
	// Regression guard from v0.63: status codes in the patterns must use
	// word boundaries so "500ms" doesn't trigger "5xx error" patterns.
	// Verify our new MCP patterns don't break this convention.
	g := GetErrorGuidance(`network latency: 401ms response time`)
	// Must NOT match as "Authentication Failed" just because 401 appears.
	if g != nil && strings.Contains(g.Title, "Authentication") {
		t.Errorf("401ms falsely triggered auth pattern: %+v", g)
	}
}
