package tools

import (
	"testing"

	"google.golang.org/genai"
)

// The field-report loop: `git status --short && echo "---DIFF---" && git
// diff --stat` repeated 5x hard-aborted the whole turn — an INSPECTION loop
// deserves the same graceful recovery read/grep earned in v0.86.7.
func TestReadOnlyBashCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		// The exact field-report command.
		{`git status --short && echo "---DIFF---" && git diff --stat`, true},
		{"git log --oneline -5", true},
		{"git -C /repo diff --stat", true},
		{"ls -la | grep foo", true},
		{"go test -race ./...", true},
		{"go env", true},
		{"cd /repo && go build ./...", true},
		{"ps aux | grep gokin | wc -l", true},

		// Mutating / unknown / risky — keep the immediate abort.
		{"git push origin main", false},
		{"git branch new-branch", false},
		{"rm -rf ./build", false},
		{"echo hi > file.txt", false},     // redirection writes
		{"cat a.txt; make deploy", false}, // one mutating segment poisons all
		{"go env -w GOFLAGS=-mod=mod", false},
		{"echo $(rm -rf /tmp/x)", false}, // command substitution hides programs
		{"git stash pop", false},
		{"", false},
	}
	for _, c := range cases {
		if got := readOnlyBashCommand(c.cmd); got != c.want {
			t.Errorf("readOnlyBashCommand(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

// Read-only bash earns a bounded recovery budget (2 hints + 2 finalize);
// mutating bash keeps 0. Edit keeps its 1-hint budget with NO finalize phase —
// forcing a "final answer" after a failed mutation invites a dishonest
// success claim.
func TestShouldAttemptStagnationRecovery_ReadOnlyBash(t *testing.T) {
	roCall := []*genai.FunctionCall{{Name: "bash", Args: map[string]any{"command": "git status --short && git diff --stat"}}}
	mutCall := []*genai.FunctionCall{{Name: "bash", Args: map[string]any{"command": "git push origin main"}}}
	editCall := []*genai.FunctionCall{{Name: "edit", Args: map[string]any{"file_path": "a.go", "old_string": "x"}}}

	for attempt := 0; attempt < 4; attempt++ {
		if !shouldAttemptStagnationRecovery(roCall, attempt) {
			t.Fatalf("read-only bash loop must earn recovery at attempt %d (2 hints + 2 finalize)", attempt)
		}
	}
	if shouldAttemptStagnationRecovery(roCall, 4) {
		t.Fatal("budget must be bounded — attempt 4 aborts")
	}
	if shouldAttemptStagnationRecovery(mutCall, 0) {
		t.Fatal("mutating bash must keep the immediate abort")
	}
	if !shouldAttemptStagnationRecovery(editCall, 0) {
		t.Fatal("edit keeps its single hint")
	}
	if shouldAttemptStagnationRecovery(editCall, 1) {
		t.Fatal("edit must NOT get a finalize phase — attempt 1 aborts")
	}
}
