package app

import (
	"strings"
	"testing"
)

func TestBuildResponseEvidenceFooter_EmptyTouchedPathsSkips(t *testing.T) {
	got := buildResponseEvidenceFooter("Done.", nil, []string{"read"}, []string{"go test ./..."})
	if got != "" {
		t.Errorf("expected empty footer with no touched paths, got %q", got)
	}
}

func TestBuildResponseEvidenceFooter_FilesOnlyNoVerification(t *testing.T) {
	got := buildResponseEvidenceFooter(
		"Applied the change.",
		[]string{"internal/app/foo.go"},
		nil, nil,
	)
	// Expect: "📁 Changed: foo.go" and no "Verified" half.
	if !strings.Contains(got, "📁 Changed: foo.go") {
		t.Errorf("missing Changed block: %q", got)
	}
	if strings.Contains(got, "Verified") {
		t.Errorf("should not show Verified half when no commands run: %q", got)
	}
}

func TestBuildResponseEvidenceFooter_FilesAndVerifiedCommand(t *testing.T) {
	got := buildResponseEvidenceFooter(
		"Wrote the refactor.",
		[]string{"internal/app/foo.go", "internal/app/bar.go"},
		[]string{"edit", "bash"},
		[]string{"go test ./internal/app/"},
	)
	if !strings.Contains(got, "📁 Changed:") || !strings.Contains(got, "foo.go") || !strings.Contains(got, "bar.go") {
		t.Errorf("expected both filenames: %q", got)
	}
	if !strings.Contains(got, "✓ Verified: go test") {
		t.Errorf("expected verified clause with go test: %q", got)
	}
	if !strings.HasPrefix(got, "\n\n_") || !strings.HasSuffix(got, "_") {
		t.Errorf("expected markdown italics wrapping with leading blank line: %q", got)
	}
}

func TestBuildResponseEvidenceFooter_VerifyToolFallback(t *testing.T) {
	// No bash command ran, but run_tests tool was used — still gets Verified.
	got := buildResponseEvidenceFooter(
		"OK.",
		[]string{"foo.go"},
		[]string{"run_tests"},
		nil,
	)
	if !strings.Contains(got, "✓ Verified: run_tests") {
		t.Errorf("expected run_tests fallback: %q", got)
	}
}

func TestBuildResponseEvidenceFooter_FileCap(t *testing.T) {
	files := []string{
		"a.go", "b.go", "c.go", "d.go", "e.go", "f.go",
	}
	got := buildResponseEvidenceFooter("x", files, nil, nil)
	// evidenceFooterMaxFiles = 4 → expect 4 names + "(+2 more)"
	if !strings.Contains(got, "a.go") || !strings.Contains(got, "d.go") {
		t.Errorf("expected first 4: %q", got)
	}
	if strings.Contains(got, "e.go") || strings.Contains(got, "f.go") {
		t.Errorf("should not list overflow: %q", got)
	}
	if !strings.Contains(got, "(+2 more)") {
		t.Errorf("expected overflow hint: %q", got)
	}
}

func TestBuildResponseEvidenceFooter_DedupExactFilenames(t *testing.T) {
	// Response already lists both files + mentions verification → skip.
	// Use "passed" (past tense) to match outputContainsVerificationSignals
	// — it deliberately requires past-tense to avoid false positives from
	// forward-looking language like "pass this as an arg".
	response := "Updated internal/app/foo.go and bar.go. All tests passed."
	got := buildResponseEvidenceFooter(
		response,
		[]string{"internal/app/foo.go", "internal/app/bar.go"},
		[]string{"edit"},
		[]string{"go test ./internal/app/"},
	)
	if got != "" {
		t.Errorf("expected skip when response already covers everything, got %q", got)
	}
}

func TestBuildResponseEvidenceFooter_ResponseNamesFilesButNotVerification(t *testing.T) {
	// Files mentioned, but no verification signal → still show footer.
	// Use a response that neither includes positive verification words
	// (pass/passed/ok) nor the literal command prefix.
	response := "Wrote foo.go and bar.go. Let me know if you want anything else."
	got := buildResponseEvidenceFooter(
		response,
		[]string{"foo.go", "bar.go"},
		[]string{"edit", "bash"},
		[]string{"npm run lint"},
	)
	if got == "" {
		t.Fatalf("expected footer (response misses verification): got empty")
	}
	if !strings.Contains(got, "✓ Verified: npm run lint") {
		t.Errorf("expected Verified with lint command: %q", got)
	}
}

func TestBuildResponseEvidenceFooter_ResponseMentionsVerificationCommand(t *testing.T) {
	// Response quotes the literal command — treated as verification signal.
	response := "Updated foo.go. Ran `go test ./internal/app` successfully."
	got := buildResponseEvidenceFooter(
		response,
		[]string{"foo.go"},
		[]string{"edit", "bash"},
		[]string{"go test ./internal/app"},
	)
	if got != "" {
		t.Errorf("expected skip when response quotes the command, got %q", got)
	}
}

func TestPickVerificationCommands_FiltersNonVerification(t *testing.T) {
	got := pickVerificationCommands(
		[]string{"ls -la", "go test ./...", "cat file"},
		nil,
	)
	if len(got) != 1 || got[0] != "go test ./..." {
		t.Errorf("expected only go test command, got %v", got)
	}
}

func TestPickVerificationCommands_FallsBackToTools(t *testing.T) {
	got := pickVerificationCommands(nil, []string{"read", "verify_code"})
	if len(got) != 1 || got[0] != "verify_code" {
		t.Errorf("expected verify_code fallback, got %v", got)
	}
}

func TestFormatEvidenceCommands_TruncatesLongCommand(t *testing.T) {
	long := "go test ./... -run TestSomeExtremelyLongTestName -v -count=1 -race"
	got := formatEvidenceCommands([]string{long}, 2)
	if !strings.HasSuffix(strings.TrimSpace(got), "...") {
		t.Errorf("expected truncation ellipsis: %q", got)
	}
	if len(got) > 55 {
		t.Errorf("expected ≤55 chars after truncation, got %d: %q", len(got), got)
	}
}

func TestUniquePaths_PreservesOrder(t *testing.T) {
	got := uniquePaths([]string{"a", "b", "a", "c", "b"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("idx %d: got %q, want %q", i, g, want[i])
		}
	}
}
