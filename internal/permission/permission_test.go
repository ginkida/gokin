package permission

import (
	"context"
	"testing"
)

func TestRiskLevelString(t *testing.T) {
	tests := []struct {
		level RiskLevel
		want  string
	}{
		{RiskLow, "low"},
		{RiskMedium, "medium"},
		{RiskHigh, "high"},
		{RiskLevel(99), "unknown"},
	}

	for _, tt := range tests {
		got := tt.level.String()
		if got != tt.want {
			t.Errorf("RiskLevel(%d).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}

func TestGetToolRiskLevel(t *testing.T) {
	lowTools := []string{
		"read", "glob", "grep", "tree", "diff", "env", "list_dir",
		"git_status", "git_log", "git_diff", "git_blame",
		"history_search",
		"web_search", "web_fetch", "todo",
		"task_output", "task_stop",
	}
	for _, tool := range lowTools {
		if got := GetToolRiskLevel(tool); got != RiskLow {
			t.Errorf("GetToolRiskLevel(%q) = %v, want RiskLow", tool, got)
		}
	}

	mediumTools := []string{"write", "edit", "git_add", "copy", "move", "mkdir", "atomicwrite", "task", "batch"}
	for _, tool := range mediumTools {
		if got := GetToolRiskLevel(tool); got != RiskMedium {
			t.Errorf("GetToolRiskLevel(%q) = %v, want RiskMedium", tool, got)
		}
	}

	highTools := []string{"bash", "delete", "git_commit", "ssh"}
	for _, tool := range highTools {
		if got := GetToolRiskLevel(tool); got != RiskHigh {
			t.Errorf("GetToolRiskLevel(%q) = %v, want RiskHigh", tool, got)
		}
	}

	// Unknown defaults to RiskMedium
	if got := GetToolRiskLevel("unknown_tool"); got != RiskMedium {
		t.Errorf("GetToolRiskLevel(unknown) = %v, want RiskMedium", got)
	}
}

func TestBuildReason(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		args     map[string]any
		contains string
	}{
		{"write with path", "write", map[string]any{"file_path": "/tmp/test.go"}, "/tmp/test.go"},
		{"write no path", "write", nil, "Write to file"},
		{"edit with path", "edit", map[string]any{"file_path": "/tmp/edit.go"}, "/tmp/edit.go"},
		{"bash with cmd", "bash", map[string]any{"command": "ls -la"}, "ls -la"},
		{"read with path", "read", map[string]any{"file_path": "/tmp/r.go"}, "/tmp/r.go"},
		{"glob with pattern", "glob", map[string]any{"pattern": "*.go"}, "*.go"},
		{"grep with pattern", "grep", map[string]any{"pattern": "func main"}, "func main"},
		{"unknown tool", "foobar", nil, "foobar"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := buildReason(tt.tool, tt.args)
			if reason == "" {
				t.Error("reason should not be empty")
			}
			if tt.contains != "" && !containsStr(reason, tt.contains) {
				t.Errorf("reason %q should contain %q", reason, tt.contains)
			}
		})
	}
}

func TestBuildReasonBashTruncation(t *testing.T) {
	longCmd := ""
	for i := 0; i < 200; i++ {
		longCmd += "x"
	}
	reason := buildReason("bash", map[string]any{"command": longCmd})
	if len(reason) > 200 {
		t.Errorf("long bash command should be truncated, reason len = %d", len(reason))
	}
}

func TestNewRequest(t *testing.T) {
	req := NewRequest("bash", map[string]any{"command": "echo hi"})
	if req.ToolName != "bash" {
		t.Errorf("ToolName = %q, want bash", req.ToolName)
	}
	if req.RiskLevel != RiskHigh {
		t.Errorf("RiskLevel = %v, want RiskHigh", req.RiskLevel)
	}
	if req.Reason == "" {
		t.Error("Reason should not be empty")
	}

	req = NewRequest("read", map[string]any{"file_path": "/tmp/test"})
	if req.RiskLevel != RiskLow {
		t.Errorf("read RiskLevel = %v, want RiskLow", req.RiskLevel)
	}
}

// --- Rules tests ---

func TestDefaultRules(t *testing.T) {
	rules := DefaultRules()
	if rules.DefaultPolicy != LevelAsk {
		t.Errorf("DefaultPolicy = %q, want %q", rules.DefaultPolicy, LevelAsk)
	}

	// Check a few representative tools
	if rules.GetPolicy("read") != LevelAllow {
		t.Error("read should be LevelAllow")
	}
	if rules.GetPolicy("bash") != LevelAsk {
		t.Error("bash should be LevelAsk")
	}
	if rules.GetPolicy("write") != LevelAsk {
		t.Error("write should be LevelAsk")
	}

	// Unknown tool uses default
	if rules.GetPolicy("unknown") != LevelAsk {
		t.Error("unknown tool should use DefaultPolicy (LevelAsk)")
	}
}

func TestRulesSetPolicy(t *testing.T) {
	rules := DefaultRules()
	rules.SetPolicy("bash", LevelDeny)
	if rules.GetPolicy("bash") != LevelDeny {
		t.Error("bash should be LevelDeny after SetPolicy")
	}
}

func TestNewRulesFromConfig(t *testing.T) {
	rules := NewRulesFromConfig("deny", map[string]string{
		"read": "allow",
		"bash": "ask",
	})
	if rules.DefaultPolicy != LevelDeny {
		t.Errorf("DefaultPolicy = %q, want deny", rules.DefaultPolicy)
	}
	if rules.GetPolicy("read") != LevelAllow {
		t.Error("read should be allow")
	}
	if rules.GetPolicy("bash") != LevelAsk {
		t.Error("bash should be ask")
	}
	if rules.GetPolicy("write") != LevelDeny {
		t.Error("write should fall back to deny")
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		want  Level
	}{
		{"allow", LevelAllow},
		{"deny", LevelDeny},
		{"ask", LevelAsk},
		{"", LevelAsk},
		{"invalid", LevelAsk},
	}

	for _, tt := range tests {
		got := parseLevel(tt.input)
		if got != tt.want {
			t.Errorf("parseLevel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- Manager tests ---

func TestManagerDisabled(t *testing.T) {
	m := NewManager(nil, false)
	resp, err := m.Check(context.Background(), "bash", map[string]any{"command": "rm -rf /"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Allowed {
		t.Error("disabled manager should allow everything")
	}
}

func TestManagerAllowPolicy(t *testing.T) {
	m := NewManager(nil, true)
	// "read" is LevelAllow in DefaultRules
	resp, err := m.Check(context.Background(), "read", map[string]any{"file_path": "/tmp/test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Allowed {
		t.Error("read should be allowed by policy")
	}
}

func TestManagerDenyPolicy(t *testing.T) {
	rules := DefaultRules()
	rules.SetPolicy("dangerous_tool", LevelDeny)
	m := NewManager(rules, true)

	resp, err := m.Check(context.Background(), "dangerous_tool", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Allowed {
		t.Error("denied tool should not be allowed")
	}
	if resp.Decision != DecisionDeny {
		t.Errorf("Decision = %v, want DecisionDeny", resp.Decision)
	}
}

func TestManagerAskNoHandler(t *testing.T) {
	m := NewManager(nil, true)
	// "bash" is LevelAsk and RiskHigh
	resp, err := m.Check(context.Background(), "bash", map[string]any{"command": "ls"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No handler set -> allow (backwards compat)
	if !resp.Allowed {
		t.Error("no handler should default to allow")
	}
}

func TestManagerAskWithHandler(t *testing.T) {
	m := NewManager(nil, true)
	m.SetPromptHandler(func(ctx context.Context, req *Request) (Decision, error) {
		return DecisionAllow, nil
	})

	resp, err := m.Check(context.Background(), "bash", map[string]any{"command": "ls"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Allowed {
		t.Error("handler returned Allow, should be allowed")
	}
}

func TestManagerAskDenyHandler(t *testing.T) {
	m := NewManager(nil, true)
	m.SetPromptHandler(func(ctx context.Context, req *Request) (Decision, error) {
		return DecisionDeny, nil
	})

	resp, err := m.Check(context.Background(), "bash", map[string]any{"command": "rm -rf"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Allowed {
		t.Error("handler returned Deny, should not be allowed")
	}
}

func TestManagerAutoApprove(t *testing.T) {
	callCount := 0
	m := NewManager(nil, true)
	m.SetPromptHandler(func(ctx context.Context, req *Request) (Decision, error) {
		callCount++
		return DecisionAllow, nil
	})

	// "write" is RiskMedium, should auto-approve after first call
	ctx := context.Background()
	args := map[string]any{"file_path": "/tmp/test"}

	resp, _ := m.Check(ctx, "write", args)
	if !resp.Allowed {
		t.Error("first call should be allowed")
	}
	if callCount != 1 {
		t.Errorf("handler should be called once, got %d", callCount)
	}

	// Second call should NOT prompt
	resp, _ = m.Check(ctx, "write", args)
	if !resp.Allowed {
		t.Error("second call should be auto-approved")
	}
	if callCount != 1 {
		t.Errorf("handler should NOT be called again, got %d", callCount)
	}
}

func TestManagerAutoApproveNotForHigh(t *testing.T) {
	callCount := 0
	m := NewManager(nil, true)
	m.SetPromptHandler(func(ctx context.Context, req *Request) (Decision, error) {
		callCount++
		return DecisionAllow, nil
	})

	ctx := context.Background()
	// "bash" is RiskHigh, should NOT auto-approve
	m.Check(ctx, "bash", map[string]any{"command": "ls"})
	m.Check(ctx, "bash", map[string]any{"command": "ls"})
	if callCount != 2 {
		t.Errorf("RiskHigh should prompt every time, handler called %d times", callCount)
	}
}

func TestManagerSessionCache(t *testing.T) {
	m := NewManager(nil, true)
	m.SetPromptHandler(func(ctx context.Context, req *Request) (Decision, error) {
		return DecisionAllowSession, nil
	})

	ctx := context.Background()
	resp, _ := m.Check(ctx, "bash", map[string]any{"command": "echo hi"})
	if !resp.Allowed {
		t.Error("should be allowed")
	}

	// Same command should be cached
	m.SetPromptHandler(func(ctx context.Context, req *Request) (Decision, error) {
		t.Error("should not be called - session cached")
		return DecisionDeny, nil
	})
	resp, _ = m.Check(ctx, "bash", map[string]any{"command": "echo hi"})
	if !resp.Allowed {
		t.Error("cached session allow should work")
	}
}

func TestManagerClearSession(t *testing.T) {
	m := NewManager(nil, true)
	m.SetPromptHandler(func(ctx context.Context, req *Request) (Decision, error) {
		return DecisionAllowSession, nil
	})

	ctx := context.Background()
	m.Check(ctx, "bash", map[string]any{"command": "ls"})

	m.ClearSession()

	callCount := 0
	m.SetPromptHandler(func(ctx context.Context, req *Request) (Decision, error) {
		callCount++
		return DecisionAllow, nil
	})

	m.Check(ctx, "bash", map[string]any{"command": "ls"})
	if callCount != 1 {
		t.Error("after ClearSession, handler should be called again")
	}
}

func TestManagerEnabledToggle(t *testing.T) {
	m := NewManager(nil, true)
	if !m.IsEnabled() {
		t.Error("should be enabled")
	}
	m.SetEnabled(false)
	if m.IsEnabled() {
		t.Error("should be disabled")
	}
}

func TestManagerSetRules(t *testing.T) {
	m := NewManager(nil, true)
	rules := &Rules{
		DefaultPolicy: LevelDeny,
		ToolPolicies:  map[string]Level{"read": LevelAllow},
	}
	m.SetRules(rules)

	got := m.GetRules()
	if got.DefaultPolicy != LevelDeny {
		t.Error("rules should be updated")
	}
}

func TestDefaultRulesToolCount(t *testing.T) {
	rules := DefaultRules()
	// read-only (18) + file mod (7) + system (4) = 29
	if len(rules.ToolPolicies) < 25 {
		t.Errorf("ToolPolicies has %d entries, want >= 25", len(rules.ToolPolicies))
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
