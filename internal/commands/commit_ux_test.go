package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitAcceptsDocumentedPositionalMessage(t *testing.T) {
	app, dir := newLogTestApp(t, 1)
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := (&CommitCommand{}).Execute(context.Background(), []string{"fix", "positional", "message"}, app)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(got, "Committed") || !strings.Contains(got, "fix positional message") {
		t.Fatalf("commit outcome = %q", got)
	}
	cmd := exec.Command("git", "log", "-1", "--pretty=%s")
	cmd.Dir = dir
	subject, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if strings.TrimSpace(string(subject)) != "fix positional message" {
		t.Fatalf("commit subject = %q, want documented positional message", strings.TrimSpace(string(subject)))
	}
}

func TestCommitMessageParserRejectsAmbiguousOrIncompleteSyntax(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"-m"}, want: "Missing commit message"},
		{args: []string{"--message", "oops"}, want: "Unknown commit option"},
		{args: []string{"fix", "-m", "oops"}, want: "Don't mix"},
	}
	for _, tc := range tests {
		message, got := parseCommitMessage(tc.args)
		if message != "" || !strings.Contains(got, tc.want) {
			t.Errorf("parseCommitMessage(%q) = (%q, %q), want empty message and %q", tc.args, message, got, tc.want)
		}
	}
}
