package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
)

// The following pin the existing fuzzyStrategies chain in edit.go. They
// guard against a common future regression: someone "simplifies" the
// chain and silently drops the whitespace-tolerant matching that makes
// Kimi's Edit calls land on the first attempt instead of the third.

func TestTryFuzzyReplace_TrailingWhitespaceTolerant(t *testing.T) {
	// File has trailing spaces on the line; model's old_string doesn't.
	content := "package x\n\nfunc Foo() {}  \n"
	old := "func Foo() {}"
	newS := "func Foo2() {}"

	got, strategy, err := tryFuzzyReplace(content, old, newS, false)
	if err != nil {
		t.Fatalf("tryFuzzyReplace: %v", err)
	}
	if strategy != "TrailingWhitespace" {
		t.Errorf("expected TrailingWhitespace strategy to win, got %q", strategy)
	}
	if !strings.Contains(got, "Foo2") {
		t.Errorf("replacement not applied: %s", got)
	}
}

func TestTryFuzzyReplace_LeadingWhitespaceTolerant(t *testing.T) {
	// File indented with tabs, model's old_string uses none.
	content := "package x\n\nfunc outer() {\n\tfoo()\n}\n"
	old := "foo()"
	newS := "bar()"

	got, _, err := tryFuzzyReplace(content, old, newS, false)
	if err != nil {
		t.Fatalf("tryFuzzyReplace: %v", err)
	}
	if !strings.Contains(got, "bar()") {
		t.Errorf("expected leading-whitespace normalization to match: %s", got)
	}
}

func TestTryFuzzyReplace_WhitespaceCollapse(t *testing.T) {
	// File has extra spaces between tokens, model's old_string has one.
	content := "a  =   b\n"
	old := "a = b"
	newS := "a = c"

	got, strategy, err := tryFuzzyReplace(content, old, newS, false)
	if err != nil {
		t.Fatalf("tryFuzzyReplace: %v", err)
	}
	if strategy == "" {
		t.Error("expected a strategy name")
	}
	if !strings.Contains(got, "a = c") {
		t.Errorf("expected collapsed-whitespace match: %s", got)
	}
}

func TestTryFuzzyReplace_AmbiguousMatchReturnsError(t *testing.T) {
	content := "x\nfoo\nbar\nx\nfoo\nbaz\n"
	old := "foo "
	newS := "FOO"

	_, _, err := tryFuzzyReplace(content, old, newS, false)
	if err == nil {
		t.Fatal("ambiguous fuzzy match must return error (safety — don't guess)")
	}
	if !strings.Contains(err.Error(), "ambiguous") && !strings.Contains(err.Error(), "occurrences") {
		t.Errorf("error should name the ambiguity: %v", err)
	}
}

func TestTryFuzzyReplace_NoMatchReturnsError(t *testing.T) {
	content := "hello world\n"
	_, _, err := tryFuzzyReplace(content, "nothingLikeThis", "replaced", false)
	if err == nil {
		t.Fatal("no-match fuzzy must return error")
	}
}

// End-to-end: Edit tool auto-invokes fuzzy matching when literal match
// fails. The model should see a success, not an error, when only
// whitespace differs.
func TestEditTool_FuzzyMatchAutoApplies(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	target := filepath.Join(dir, "code.go")
	// File uses tabs; old_string uses spaces.
	if err := os.WriteFile(target, []byte("package x\n\nfunc Foo() {\n\treturn 1\n}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tracker := NewFileReadTracker()
	tracker.CheckAndRecord(target, 1, 100, 50)

	et := NewEditTool(dir)
	et.SetReadTracker(tracker)
	et.SetRequireReadBeforeEdit(true)

	result, err := et.Execute(context.Background(), map[string]any{
		"file_path":  target,
		"old_string": "    return 1", // spaces instead of tab — fuzzy path should still land
		"new_string": "    return 2",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Success {
		t.Fatalf("fuzzy should auto-apply on whitespace mismatch, got error: %s", result.Error)
	}
	data, _ := os.ReadFile(target)
	if !strings.Contains(string(data), "return 2") {
		t.Errorf("file not updated: %s", data)
	}
	// Success message should mention fuzzy so the caller knows matching
	// was tolerant (audit-trail).
	if !strings.Contains(result.Content, "fuzzy") {
		t.Errorf("success message should signal fuzzy match: %s", result.Content)
	}
}
