package tools

import (
	"strings"
	"testing"
)

func TestBenignNonZeroExit(t *testing.T) {
	cases := []struct {
		name   string
		cmd    string
		code   int
		stderr string
		want   bool
	}{
		{"grep no match", "grep -n RPS file.go", 1, "", true},
		{"grep real error (exit 2)", "grep -n RPS file.go", 2, "", false},
		{"grep with stderr is a real error", "grep -n RPS missing.go", 1, "grep: missing.go: No such file", false},
		{"cd-prefixed grep", "cd /p && grep -n RPS file.go", 1, "", true},
		{"chained cd prefix", "cd /a && cd /b && grep x f", 1, "", true},
		{"grep alternation regex (pipe in pattern)", "grep -nE 'foo|bar' file.go", 1, "", true},
		{"rg no match", "rg TODO", 1, "", true},
		{"git grep no match", "git grep TODO", 1, "", true},
		{"diff differs", "diff a.txt b.txt", 1, "", true},
		{"go test failure is NOT benign", "go test ./...", 1, "", false},
		{"piped grep no match (last cmd determines exit)", "ps aux | grep foo", 1, "", true},
		{"cd then piped grep", "cd /p && cat f | grep x", 1, "", true},
		{"pipe ending in non-search tool is NOT benign", "grep x f | wc -l", 1, "", false},
		{"empty command", "", 1, "", false},
	}
	for _, c := range cases {
		if got := benignNonZeroExit(c.cmd, c.code, c.stderr); got != c.want {
			t.Errorf("%s: benignNonZeroExit(%q, %d, %q) = %v, want %v", c.name, c.cmd, c.code, c.stderr, got, c.want)
		}
	}
}

func TestBuildExitResult(t *testing.T) {
	bt := &BashTool{}

	// Benign no-match grep → success with a clear "(no matches)" body, no error.
	res := bt.buildExitResult("grep -n RPS file.go", "", "", 1)
	if !res.Success || res.Error != "" {
		t.Fatalf("benign grep should be success, got success=%v err=%q", res.Success, res.Error)
	}
	if res.Content != "(no matches)" {
		t.Fatalf("benign no-match content = %q, want \"(no matches)\"", res.Content)
	}

	// Benign diff WITH output keeps the output (the diff is the result).
	diff := bt.buildExitResult("diff a b", "< old\n> new", "", 1)
	if !diff.Success || !strings.Contains(diff.Content, "< old") {
		t.Fatalf("benign diff should be success carrying the diff, got success=%v content=%q", diff.Success, diff.Content)
	}

	// Benign compare with NO output (cmp -s) labels "(differs)", not "(no matches)".
	silentDiff := bt.buildExitResult("cmp -s a b", "", "", 1)
	if !silentDiff.Success || silentDiff.Content != "(differs)" {
		t.Fatalf("silent cmp should label '(differs)', got success=%v content=%q", silentDiff.Success, silentDiff.Content)
	}

	// Real failure → error with the exit code AND stderr (so diagnostics reach the model).
	fail := bt.buildExitResult("go build ./...", "", "x.go:3: undefined: foo", 2)
	if fail.Success || !strings.Contains(fail.Error, "code 2") {
		t.Fatalf("real failure should be an error, got success=%v err=%q", fail.Success, fail.Error)
	}
	if !strings.Contains(fail.Content, "undefined: foo") {
		t.Fatalf("real failure should include stderr, got %q", fail.Content)
	}
}
