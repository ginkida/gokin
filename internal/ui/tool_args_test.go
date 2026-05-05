package ui

import (
	"strings"
	"testing"
)

func TestFormatToolActivity_ProjectTools(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "run_tests",
			args: map[string]any{
				"path":      "internal/app",
				"framework": "go",
				"filter":    "TestDoneGate",
				"coverage":  true,
			},
			want: "Testing go internal/app filter=TestDoneGate coverage",
		},
		{
			name: "verify_code",
			args: map[string]any{"path": "internal/ui"},
			want: "Verifying internal/ui",
		},
		{
			name: "git_diff",
			args: map[string]any{"staged": true, "name_status": true},
			want: "Inspecting diff staged changes --name-status",
		},
		{
			name: "copy",
			args: map[string]any{"source": "internal/ui/source.go", "destination": "internal/ui/destination.go"},
			want: "Copying internal/ui/source.go -> internal/ui/destination.go",
		},
	}

	for _, tt := range tests {
		if got := formatToolActivity(tt.name, tt.args); got != tt.want {
			t.Fatalf("formatToolActivity(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestToolExecutingArgs_ProjectTools(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "run_tests",
			args: map[string]any{"path": "internal/app", "framework": "go", "filter": "TestDoneGate"},
			want: "RunTests go internal/app filter=TestDoneGate",
		},
		{
			name: "git_add",
			args: map[string]any{"paths": []any{"internal/ui/tui.go", "internal/ui/styles.go"}},
			want: "GitAdd 2 paths",
		},
		{
			name: "move",
			args: map[string]any{"source": "internal/ui/old.go", "destination": "internal/ui/new.go"},
			want: "Move internal/ui/old.go -> internal/ui/new.go",
		},
	}

	styles := DefaultStyles()
	for _, tt := range tests {
		got := stripAnsi(styles.FormatToolExecutingBlock(tt.name, tt.args))
		if !strings.Contains(got, tt.want) {
			t.Fatalf("FormatToolExecutingBlock(%q) = %q, want to contain %q", tt.name, got, tt.want)
		}
	}
}

func TestExtractToolInfoFromArgs_ProjectTools(t *testing.T) {
	m := NewModel()

	got := m.extractToolInfoFromArgs("copy", map[string]any{
		"source":      "internal/ui/source.go",
		"destination": "internal/ui/destination.go",
	})
	if got != "internal/ui/source.go -> internal/ui/destination.go" {
		t.Fatalf("copy info = %q", got)
	}

	got = m.extractToolInfoFromArgs("git_status", map[string]any{"short": true})
	if got != ". --short" {
		t.Fatalf("git_status info = %q", got)
	}
}

func TestToolDisplayLabels(t *testing.T) {
	if got := displayToolName("run_tests"); got != "Run Tests" {
		t.Fatalf("displayToolName(run_tests) = %q", got)
	}
	if got := statusToolLabel("run_tests"); got != "TESTS" {
		t.Fatalf("statusToolLabel(run_tests) = %q", got)
	}
	if got := statusToolLabel("git_diff"); got != "GIT DIFF" {
		t.Fatalf("statusToolLabel(git_diff) = %q", got)
	}
}
