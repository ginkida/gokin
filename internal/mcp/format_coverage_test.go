package mcp

import (
	"strings"
	"testing"
)

// --- FormatList ---

func TestFormatList_NilManager(t *testing.T) {
	got := FormatList(nil)
	if !strings.Contains(got, "not initialised") {
		t.Fatalf("nil manager: got %q, want 'not initialised' hint", got)
	}
}

func TestFormatList_EmptyManager(t *testing.T) {
	mgr := &Manager{}
	got := FormatList(mgr)
	if !strings.Contains(got, "No MCP servers configured") {
		t.Fatalf("empty manager: got %q, want 'No MCP servers configured' hint", got)
	}
}

func TestFormatList_WithServers(t *testing.T) {
	mgr := NewManager([]*ServerConfig{
		{Name: "zeta", Transport: "stdio"},
		{Name: "alpha", Transport: "stdio"},
	})
	// Simulate connected state by injecting a client + health
	mgr.mu.Lock()
	mgr.clients["alpha"] = &Client{serverName: "alpha", initialized: true}
	mgr.health["alpha"] = &ServerHealth{Healthy: true}
	mgr.tools = append(mgr.tools, &MCPTool{serverName: "alpha", toolName: "tool-a"})
	mgr.mu.Unlock()

	got := FormatList(mgr)
	// alpha must sort before zeta
	if idxA, idxZ := strings.Index(got, "alpha"), strings.Index(got, "zeta"); idxA < 0 || idxZ < 0 || idxA > idxZ {
		t.Fatalf("expected alpha before zeta, got:\n%s", got)
	}
	if !strings.Contains(got, "1/2 connected") {
		t.Fatalf("expected '1/2 connected' in output, got:\n%s", got)
	}
}

// --- FormatStatus ---

func TestFormatStatus_NilManager(t *testing.T) {
	got := FormatStatus(nil, "")
	if !strings.Contains(got, "not initialised") {
		t.Fatalf("nil manager: got %q", got)
	}
}

func TestFormatStatus_EmptyManager(t *testing.T) {
	got := FormatStatus(&Manager{}, "")
	if !strings.Contains(got, "No MCP servers configured") {
		t.Fatalf("empty manager: got %q", got)
	}
}

func TestFormatStatus_NotFound(t *testing.T) {
	mgr := NewManager([]*ServerConfig{{Name: "alpha"}})
	got := FormatStatus(mgr, "nonexistent")
	if !strings.Contains(got, `No MCP server named "nonexistent"`) {
		t.Fatalf("expected not-found hint, got %q", got)
	}
}

func TestFormatStatus_ByName(t *testing.T) {
	mgr := NewManager([]*ServerConfig{
		{Name: "alpha"},
		{Name: "beta"},
	})
	got := FormatStatus(mgr, "beta")
	if !strings.Contains(got, "Server: beta") {
		t.Fatalf("expected detail for beta, got %q", got)
	}
	if strings.Contains(got, "Server: alpha") {
		t.Fatalf("should not contain alpha, got %q", got)
	}
}

func TestFormatStatus_AllSorted(t *testing.T) {
	mgr := NewManager([]*ServerConfig{
		{Name: "zeta"},
		{Name: "alpha"},
	})
	got := FormatStatus(mgr, "")
	if idxA, idxZ := strings.Index(got, "Server: alpha"), strings.Index(got, "Server: zeta"); idxA < 0 || idxZ < 0 || idxA > idxZ {
		t.Fatalf("expected alpha before zeta, got:\n%s", got)
	}
}

// --- FormatServerLine ---

func TestFormatServerLine_Offline(t *testing.T) {
	s := &ServerStatus{Name: "srv", Connected: false, Healthy: false}
	got := FormatServerLine(s)
	if !strings.Contains(got, "✗ offline") {
		t.Fatalf("expected offline indicator, got %q", got)
	}
}

func TestFormatServerLine_Healthy(t *testing.T) {
	s := &ServerStatus{Name: "srv", Connected: true, Healthy: true, ToolCount: 3}
	got := FormatServerLine(s)
	if !strings.Contains(got, "✓ healthy") {
		t.Fatalf("expected healthy indicator, got %q", got)
	}
	if !strings.Contains(got, "3 tools") {
		t.Fatalf("expected tool count, got %q", got)
	}
}

func TestFormatServerLine_Unhealthy(t *testing.T) {
	s := &ServerStatus{Name: "srv", Connected: true, Healthy: false}
	got := FormatServerLine(s)
	if !strings.Contains(got, "⚠ unhealthy") {
		t.Fatalf("expected unhealthy indicator, got %q", got)
	}
}

// --- FormatServerDetail ---

func TestFormatServerDetail_NoTools(t *testing.T) {
	s := &ServerStatus{Name: "srv", Connected: true, Healthy: true, ToolCount: 0}
	got := FormatServerDetail(s)
	if !strings.Contains(got, "(none)") {
		t.Fatalf("expected '(none)' for zero tools, got %q", got)
	}
}

func TestFormatServerDetail_WithToolsAndVersion(t *testing.T) {
	s := &ServerStatus{
		Name:       "srv",
		Connected:  true,
		Healthy:    true,
		ToolCount:  2,
		ToolNames:  []string{"zebra", "alpha"},
		ServerInfo: &ServerInfo{Version: "1.5.0"},
	}
	got := FormatServerDetail(s)
	if !strings.Contains(got, "Version:    1.5.0") {
		t.Fatalf("expected version line, got %q", got)
	}
	// Tools should be sorted
	idxA, idxZ := strings.Index(got, "alpha"), strings.Index(got, "zebra")
	if idxA < 0 || idxZ < 0 || idxA > idxZ {
		t.Fatalf("expected sorted tool names (alpha before zebra), got:\n%s", got)
	}
}

func TestFormatServerDetail_NoVersionWhenAbsent(t *testing.T) {
	s := &ServerStatus{Name: "srv", Connected: true, Healthy: true, ToolCount: 1, ToolNames: []string{"t1"}}
	got := FormatServerDetail(s)
	if strings.Contains(got, "Version:") {
		t.Fatalf("should not contain version when ServerInfo is nil, got %q", got)
	}
}

// --- FormatPrompts ---

func TestFormatPrompts_NilManager(t *testing.T) {
	got := FormatPrompts(nil)
	if !strings.Contains(got, "not initialised") {
		t.Fatalf("nil manager: got %q", got)
	}
}

func TestFormatPrompts_EmptyManager(t *testing.T) {
	got := FormatPrompts(&Manager{})
	if !strings.Contains(got, "No MCP prompts available") {
		t.Fatalf("empty manager: got %q", got)
	}
}
