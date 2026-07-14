package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestWorkspaceOverlayNarrowFootersKeepCloseFirst(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, width := range []int{12, 16, 24, 32} {
		t.Run("file-browser-"+itoa(width), func(t *testing.T) {
			m := NewFileBrowserModel(DefaultStyles())
			m.SetSize(width, 16)
			if err := m.SetPath(work); err != nil {
				t.Fatal(err)
			}
			var b strings.Builder
			m.renderActions(&b)
			assertRecoveryFirstAndFits(t, b.String(), width, "Esc/q")
		})

		t.Run("search-results-"+itoa(width), func(t *testing.T) {
			m := NewSearchResultsModel(DefaultStyles())
			m.SetSize(width, 16)
			m.SetResults("main", "grep", []SearchResult{{FilePath: "main.go", LineNumber: 1}})
			var b strings.Builder
			m.renderActions(&b)
			assertRecoveryFirstAndFits(t, b.String(), width, "Esc/q")
		})

		t.Run("git-status-"+itoa(width), func(t *testing.T) {
			m := NewGitStatusModel(DefaultStyles())
			m.SetSize(width, 16)
			m.SetStatus([]GitFileEntry{{FilePath: "main.go", Status: GitFileModified}}, "main", "", "")
			var b strings.Builder
			m.renderActions(&b)
			assertRecoveryFirstAndFits(t, b.String(), width, "Esc/q")
		})
	}
}

func TestWorkspaceOverlaySafetyPromptsKeepCancelBeforeConfirmation(t *testing.T) {
	m := NewGitStatusModel(DefaultStyles())
	m.SetSize(16, 16)
	m.SetStatus([]GitFileEntry{{FilePath: "main.go", Status: GitFileModified}}, "main", "", "")
	m.confirmReset = true
	m.pendingResetFiles = []string{"main.go"}
	var b strings.Builder
	m.renderActions(&b)
	plain := stripAnsi(b.String())
	if !strings.Contains(plain, "Esc/q Cancel") {
		t.Fatalf("narrow destructive confirmation lost its cancel action:\n%s", plain)
	}
	if confirm := strings.Index(plain, "Enter"); confirm >= 0 && strings.Index(plain, "Esc/q") > confirm {
		t.Fatalf("destructive confirmation appears before cancellation:\n%s", plain)
	}
	for _, line := range strings.Split(b.String(), "\n") {
		if got := lipgloss.Width(line); got > 16 {
			t.Fatalf("confirmation footer width=%d, want <=16: %q", got, stripAnsi(line))
		}
	}

	diff := NewMultiDiffPreviewModel(DefaultStyles())
	diff.SetSize(16, 18)
	diff.SetFiles([]DiffFile{{FilePath: "main.go", OldContent: "old\n", NewContent: "new\n"}})
	diff.confirmFinish = true
	b.Reset()
	diff.renderMultiActions(&b)
	plain = stripAnsi(b.String())
	if !strings.Contains(plain, "Esc/q") {
		t.Fatalf("narrow pending-file confirmation lost its back action:\n%s", plain)
	}
	if confirm := strings.Index(plain, "Enter"); confirm >= 0 && strings.Index(plain, "Esc/q") > confirm {
		t.Fatalf("pending-file confirmation appears before back action:\n%s", plain)
	}
	for _, line := range strings.Split(b.String(), "\n") {
		if got := lipgloss.Width(line); got > 16 {
			t.Fatalf("multi-diff footer width=%d, want <=16: %q", got, stripAnsi(line))
		}
	}
}

func TestFileBrowserFilterFooterKeepsExitFromEditingFirst(t *testing.T) {
	m := NewFileBrowserModel(DefaultStyles())
	m.SetSize(16, 16)
	m.filterActive = true
	var b strings.Builder
	m.renderActions(&b)
	assertRecoveryFirstAndFits(t, b.String(), 16, "Enter/Esc")
}

func assertRecoveryFirstAndFits(t *testing.T, rendered string, width int, recovery string) {
	t.Helper()
	lines := strings.Split(rendered, "\n")
	if len(lines) == 0 || !strings.Contains(stripAnsi(lines[0]), recovery) {
		t.Fatalf("first footer line lost recovery %q at width %d:\n%s", recovery, width, stripAnsi(rendered))
	}
	for _, line := range lines {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("footer width=%d, want <=%d: %q", got, width, stripAnsi(line))
		}
	}
}
