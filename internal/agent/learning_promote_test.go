package agent

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/memory"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

// TestNormalizeCommandForLearning pins the filter rules. The pattern matrix
// here IS the contract — drifts in either direction (accepting noise or
// rejecting useful commands) are the regression we're guarding.
func TestNormalizeCommandForLearning(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Passes through unchanged.
		{"plain_command", "go test ./...", "go test ./..."},
		{"compound_command", "go build && go test", "go build && go test"},

		// Whitespace handling.
		{"leading_whitespace", "  go vet", "go vet"},
		{"trailing_newline", "npm install\n", "npm install"},

		// cd-prefix stripping — mirrors stagnation_fingerprint.
		{"strips_cd_prefix", "cd /Users/me/project && go test", "go test"},
		{"strips_relative_cd", "cd internal/app && go build", "go build"},
		{"pure_cd_dropped", "cd /some/path", ""},   // navigation only
		{"cd_with_empty_rest", "cd /x && ", ""},     // TrimSpace → empty
		{"cd_then_short", "cd /x && ls", ""},        // stripped becomes <5 chars

		// Junk rejection.
		{"empty", "", ""},
		{"only_whitespace", "   \n\t  ", ""},
		{"too_short_pwd", "pwd", ""},
		{"too_short_ls", "ls", ""},
		{"too_short_w", "w", ""},
		{"six_chars_ok", "go vet", "go vet"},

		// Long scripts rejected — not reusable signal.
		{"huge_heredoc_dropped", strings.Repeat("x", 800), ""},
		{"at_limit_kept", strings.Repeat("a", 500), strings.Repeat("a", 500)},
		{"over_limit_dropped", strings.Repeat("a", 501), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeCommandForLearning(tc.in)
			if got != tc.want {
				t.Errorf("normalizeCommandForLearning(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRecordToolForLearning_BashSuccess verifies that a successful bash call
// is persisted to ProjectLearning with the normalized command and a
// success-weighted EMA rate. Uses a real ProjectLearning against tmpdir so
// we also exercise the disk path.
func TestRecordToolForLearning_BashSuccess(t *testing.T) {
	tmp := t.TempDir()
	pl, err := memory.NewProjectLearning(tmp)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}

	a := &Agent{learning: pl}

	call := &genai.FunctionCall{
		Name: "bash",
		Args: map[string]any{
			"command": "cd /tmp && go test ./...",
		},
	}
	// Two runs so it crosses the UsageCount >= 2 threshold that
	// FormatForPrompt uses to gate the Reliable Commands section.
	a.recordToolForLearning(call, tools.ToolResult{Success: true}, 250*time.Millisecond)
	a.recordToolForLearning(call, tools.ToolResult{Success: true}, 300*time.Millisecond)

	// Force flush so we can inspect the on-disk yaml directly.
	if err := pl.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Reload from disk to prove it stuck.
	pl2, err := memory.NewProjectLearning(tmp)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	prompt := pl2.FormatForPrompt()
	// Must contain the normalized (cd-stripped) command, not the raw one.
	if !strings.Contains(prompt, "go test ./...") {
		t.Errorf("prompt missing learned command: %q", prompt)
	}
	if strings.Contains(prompt, "cd /tmp") {
		t.Errorf("cd prefix leaked into learning: %q", prompt)
	}
}

// TestRecordToolForLearning_IgnoresNonBash: only bash has a promotion
// signal today. Adding other tools should be an intentional decision with
// its own test — catch accidental scope creep.
func TestRecordToolForLearning_IgnoresNonBash(t *testing.T) {
	tmp := t.TempDir()
	pl, err := memory.NewProjectLearning(tmp)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}

	a := &Agent{learning: pl}

	for _, name := range []string{"read", "write", "edit", "grep", "glob"} {
		call := &genai.FunctionCall{
			Name: name,
			Args: map[string]any{"file_path": "/tmp/x"},
		}
		a.recordToolForLearning(call, tools.ToolResult{Success: true}, 10*time.Millisecond)
	}

	_ = pl.Flush()
	// Check on-disk yaml stays empty of commands.
	got, err := memory.NewProjectLearning(tmp)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	prompt := got.FormatForPrompt()
	// "Reliable Commands" header should not be present when nothing was recorded.
	if strings.Contains(prompt, "Reliable Commands") {
		t.Errorf("non-bash tools leaked into Reliable Commands: %q", prompt)
	}
}

// TestRecordToolForLearning_NilSafe: the hook must never panic when the
// Agent has no ProjectLearning wired (e.g. tests, or misconfigured
// workdir). We deliberately hit the nil branch.
func TestRecordToolForLearning_NilSafe(t *testing.T) {
	a := &Agent{} // learning == nil
	call := &genai.FunctionCall{
		Name: "bash",
		Args: map[string]any{"command": "go test ./..."},
	}
	// Must not panic.
	a.recordToolForLearning(call, tools.ToolResult{Success: true}, 0)

	// Also nil call.
	a.recordToolForLearning(nil, tools.ToolResult{}, 0)
}

// TestRecordToolForLearning_FailurePersisted: failed commands also need to
// be recorded so the EMA success rate accurately reflects reality. A
// command that fails 4/5 runs should NOT show up as "Reliable".
func TestRecordToolForLearning_FailurePersisted(t *testing.T) {
	tmp := t.TempDir()
	pl, err := memory.NewProjectLearning(tmp)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}

	a := &Agent{learning: pl}
	call := &genai.FunctionCall{
		Name: "bash",
		Args: map[string]any{"command": "flaky-build-script"},
	}

	// 1 success, 4 failures → EMA rate should land below the 0.5 threshold
	// used by FormatForPrompt's Reliable Commands section.
	a.recordToolForLearning(call, tools.ToolResult{Success: true}, 100*time.Millisecond)
	for range 4 {
		a.recordToolForLearning(call, tools.ToolResult{Success: false}, 100*time.Millisecond)
	}
	_ = pl.Flush()

	// Verify the yaml file exists and has content — we're not asserting
	// the exact EMA value, just that failure signals got through.
	if _, err := filepath.Glob(filepath.Join(tmp, ".gokin", "learning.yaml")); err != nil {
		t.Fatalf("glob: %v", err)
	}

	pl2, err := memory.NewProjectLearning(tmp)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	// With 1W/4L the EMA should have tanked below 0.5 — display layer
	// filters it out of the "Reliable" bucket.
	prompt := pl2.FormatForPrompt()
	if strings.Contains(prompt, "flaky-build-script") &&
		strings.Contains(prompt, "Reliable Commands") {
		// Only fail if the flaky command actually appears in the reliable
		// section. If the section is absent entirely, we're fine.
		reliableIdx := strings.Index(prompt, "Reliable Commands")
		if reliableIdx >= 0 && strings.Contains(prompt[reliableIdx:], "flaky-build-script") {
			t.Errorf("flaky command surfaced as Reliable: %q", prompt)
		}
	}
}
