package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installFakeGH writes an executable "gh" script to a temp dir and prepends
// it to PATH for the duration of the test (t.Setenv auto-restores). script
// is the shell body executed in place of the real gh CLI.
func installFakeGH(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-gh-via-shell-script test is Unix-only")
	}
	dir := t.TempDir()
	ghPath := filepath.Join(dir, "gh")
	content := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(ghPath, []byte(content), 0755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestGitPRTool_ChecksPR_BenignFailingChecksStillSucceeds (round 5) pins the
// fix's happy path: `gh pr checks` legitimately exits non-zero when some
// checks are failing/pending — that must still be Success:true (the QUERY
// worked; it's reporting real check results).
func TestGitPRTool_ChecksPR_BenignFailingChecksStillSucceeds(t *testing.T) {
	installFakeGH(t, `echo "build	fail	1m	https://example.com/build"
echo "lint	pass	5s	https://example.com/lint"
exit 1`)

	tool := NewGitPRTool(t.TempDir())
	result, err := tool.checksPR(context.Background(), map[string]any{"pr_number": "42"})
	if err != nil {
		t.Fatalf("checksPR: unexpected Go error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected Success=true for a benign failing-checks report, got: %+v", result)
	}
	if !strings.Contains(result.Content, "build") || !strings.Contains(result.Content, "fail") {
		t.Fatalf("result content = %q, want it to include the checks report", result.Content)
	}
}

// TestGitPRTool_ChecksPR_RealFailureReportsError (round 5) pins the fix: a
// genuine gh failure (auth expired, network error, unknown PR — anything
// that writes to stderr and/or produces no real report) must NOT be
// reported as Success:true, unlike before the fix, which returned
// NewSuccessResult unconditionally regardless of what went wrong.
func TestGitPRTool_ChecksPR_RealFailureReportsError(t *testing.T) {
	installFakeGH(t, `echo "error: authentication required, run 'gh auth login'" >&2
exit 4`)

	tool := NewGitPRTool(t.TempDir())
	result, err := tool.checksPR(context.Background(), map[string]any{"pr_number": "42"})
	if err != nil {
		t.Fatalf("checksPR: unexpected Go error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected Success=false for a real gh failure (auth error on stderr), got: %+v", result)
	}
	if !strings.Contains(result.Error, "authentication required") {
		t.Fatalf("result.Error = %q, want it to surface the real gh error text", result.Error)
	}
}

// TestGitPRTool_ChecksPR_AllChecksPassing is the happy-path regression check
// — exit 0 must still succeed exactly as before.
func TestGitPRTool_ChecksPR_AllChecksPassing(t *testing.T) {
	installFakeGH(t, `echo "build	pass	1m	https://example.com/build"
exit 0`)

	tool := NewGitPRTool(t.TempDir())
	result, err := tool.checksPR(context.Background(), map[string]any{"pr_number": "42"})
	if err != nil {
		t.Fatalf("checksPR: unexpected Go error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected Success=true when all checks pass, got: %+v", result)
	}
}
