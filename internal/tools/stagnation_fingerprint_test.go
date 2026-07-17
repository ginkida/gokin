package tools

import "testing"

func TestStagnationFingerprint_DistinguishesFilesForWriteEditDelete(t *testing.T) {
	for _, tool := range []string{"write", "edit", "delete"} {
		t.Run(tool, func(t *testing.T) {
			a := stagnationFingerprint(tool, map[string]any{"file_path": "/tmp/a.go"})
			b := stagnationFingerprint(tool, map[string]any{"file_path": "/tmp/b.go"})
			if a == b {
				t.Errorf("%s: different files produced same fingerprint (%q)", tool, a)
			}
			if a != "a.go" {
				t.Errorf("%s: fingerprint = %q, want basename a.go", tool, a)
			}
		})
	}
}

// TestStagnationFingerprint_ReadIncludesOffsetLimit pins the fix for the
// false-positive that Kimi users hit: paging through a ~3000-line file in
// 2000-line chunks produced the same fingerprint every time and tripped the
// 5-repeat stagnation abort. The fix encodes offset+limit into the
// fingerprint so forward progress through a file is visible.
func TestStagnationFingerprint_ReadIncludesOffsetLimit(t *testing.T) {
	// Paged reads of the same file must fingerprint differently.
	p0 := stagnationFingerprint("read", map[string]any{"file_path": "/tmp/tui.go", "offset": 0, "limit": 2000})
	p1 := stagnationFingerprint("read", map[string]any{"file_path": "/tmp/tui.go", "offset": 2000, "limit": 2000})
	p2 := stagnationFingerprint("read", map[string]any{"file_path": "/tmp/tui.go", "offset": 4000, "limit": 2000})
	if p0 == p1 || p1 == p2 || p0 == p2 {
		t.Errorf("paged reads collapsed to same fingerprint: %q %q %q", p0, p1, p2)
	}

	// JSON unmarshal typically produces float64 for integer fields — the
	// fingerprint helper must accept that shape transparently.
	pInt := stagnationFingerprint("read", map[string]any{"file_path": "/tmp/x.go", "offset": 100, "limit": 50})
	pFloat := stagnationFingerprint("read", map[string]any{"file_path": "/tmp/x.go", "offset": 100.0, "limit": 50.0})
	if pInt != pFloat {
		t.Errorf("int vs float64 produced different fingerprints: %q vs %q", pInt, pFloat)
	}

	// Missing offset/limit still needs to fingerprint sensibly (two reads
	// without paging *are* stagnation if the file_path is identical).
	noPaging1 := stagnationFingerprint("read", map[string]any{"file_path": "/tmp/a.go"})
	noPaging2 := stagnationFingerprint("read", map[string]any{"file_path": "/tmp/a.go"})
	if noPaging1 != noPaging2 {
		t.Errorf("identical bare reads must share a fingerprint, got %q vs %q", noPaging1, noPaging2)
	}

	// Different files must still be distinguishable regardless of paging.
	a := stagnationFingerprint("read", map[string]any{"file_path": "/tmp/a.go", "offset": 0})
	b := stagnationFingerprint("read", map[string]any{"file_path": "/tmp/b.go", "offset": 0})
	if a == b {
		t.Errorf("different files produced same fingerprint: %q", a)
	}
}

// Edits that target DIFFERENT parts of the same file must fingerprint
// differently so legitimate multi-region editing isn't mistaken for a stuck
// retry loop — while a genuinely-repeated identical edit still collapses to one
// fingerprint and is still caught.
func TestStagnationFingerprint_EditDistinguishesTargets(t *testing.T) {
	// old_string mode: different old_strings differ, identical ones collapse.
	o1 := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "old_string": "func a()"})
	o2 := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "old_string": "func b()"})
	o1b := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "old_string": "func a()"})
	if o1 == o2 {
		t.Errorf("different old_strings collapsed: %q", o1)
	}
	if o1 != o1b {
		t.Errorf("identical old_string edits must share a fingerprint: %q vs %q", o1, o1b)
	}

	// line-range mode: different ranges differ, identical ranges collapse.
	l1 := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "line_start": 10, "line_end": 15})
	l2 := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "line_start": 50, "line_end": 55})
	l1b := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "line_start": 10.0, "line_end": 15.0})
	if l1 == l2 {
		t.Errorf("different line ranges collapsed: %q", l1)
	}
	if l1 != l1b {
		t.Errorf("int vs float line ranges must match: %q vs %q", l1, l1b)
	}

	// insert mode: different insert points differ; insert-at-0 is distinct from
	// a bare edit (so it isn't swallowed by the basename fallback).
	i0 := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "insert_after_line": 0})
	i5 := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "insert_after_line": 5})
	bare := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go"})
	if i0 == i5 || i0 == bare {
		t.Errorf("insert points not distinguished: i0=%q i5=%q bare=%q", i0, i5, bare)
	}
	if bare != "f.go" {
		t.Errorf("bare edit fingerprint = %q, want basename f.go", bare)
	}

	// multi-edit mode: different edit batches to the same file differ.
	m1 := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "edits": []any{map[string]any{"old_string": "a", "new_string": "b"}}})
	m2 := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "edits": []any{map[string]any{"old_string": "c", "new_string": "d"}}})
	if m1 == m2 {
		t.Errorf("different multi-edit batches collapsed: %q", m1)
	}

	// Different files always differ regardless of mode.
	a := stagnationFingerprint("edit", map[string]any{"file_path": "/x/a.go", "line_start": 1, "line_end": 2})
	b := stagnationFingerprint("edit", map[string]any{"file_path": "/x/b.go", "line_start": 1, "line_end": 2})
	if a == b {
		t.Errorf("different files collapsed: %q", a)
	}
}

func TestStagnationFingerprint_BashStripsCdPrefix(t *testing.T) {
	withCd := stagnationFingerprint("bash", map[string]any{"command": "cd /some/long/path && go test ./..."})
	without := stagnationFingerprint("bash", map[string]any{"command": "go test ./..."})
	if withCd != without {
		t.Errorf("cd-prefixed (%q) and plain (%q) should fingerprint the same", withCd, without)
	}
}

func TestStagnationFingerprint_BashTruncatesLongCommands(t *testing.T) {
	long := stagnationFingerprint("bash", map[string]any{
		"command": "echo this command is considerably longer than sixty characters so it should be truncated by the fingerprint helper",
	})
	// 60-rune display prefix + "#" + 6 hex chars of the full-command hash.
	if len(long) > 70 {
		t.Errorf("bash fingerprint len = %d, want ≤ 70", len(long))
	}
}

// The v0.100.91 field report: two DIFFERENT commands sharing a >60-rune
// prefix (`… && git status --short` vs `… && git status --porcelain`)
// collapsed into one fingerprint, so five distinct inspection variants
// tripped the "repeated 5 times consecutively" abort as if the model had
// repeated one identical call. Distinct args must be distinct keys; truly
// identical commands must still collapse.
func TestStagnationFingerprint_BashLongCommandsKeyOnFullCommand(t *testing.T) {
	prefix := `git log --oneline -5 && echo "===STATUS===" && git status --`
	a := stagnationFingerprint("bash", map[string]any{"command": prefix + "short"})
	b := stagnationFingerprint("bash", map[string]any{"command": prefix + "porcelain"})
	if a == b {
		t.Fatalf("different long commands collapsed into one fingerprint: %q", a)
	}
	a2 := stagnationFingerprint("bash", map[string]any{"command": prefix + "short"})
	if a != a2 {
		t.Fatalf("identical commands produced different fingerprints: %q vs %q", a, a2)
	}
	// cd-prefix stripping still normalizes before hashing, so a cd-wrapped
	// repeat of the same command keeps the same key.
	withCd := stagnationFingerprint("bash", map[string]any{"command": "cd /repo && " + prefix + "short"})
	if withCd != a {
		t.Fatalf("cd-prefixed variant diverged: %q vs %q", withCd, a)
	}
}

func TestStagnationFingerprint_BashOnlyStripsCdWhenPrefix(t *testing.T) {
	// "&& cd" in the middle must NOT be stripped.
	cmd := "go test ./... && cd /tmp && ls"
	got := stagnationFingerprint("bash", map[string]any{"command": cmd})
	// First &&-split: " && ". strings.HasPrefix check guards against stripping
	// unless the command starts with "cd ". Since cmd starts with "go", no strip.
	if got != cmd {
		t.Errorf("fingerprint = %q, want unchanged command", got)
	}
}

func TestStagnationFingerprint_GrepAndGlobUsePattern(t *testing.T) {
	g1 := stagnationFingerprint("grep", map[string]any{"pattern": "foo.*bar"})
	g2 := stagnationFingerprint("grep", map[string]any{"pattern": "baz"})
	if g1 == g2 || g1 != "foo.*bar" {
		t.Errorf("grep: g1=%q g2=%q", g1, g2)
	}
	gb := stagnationFingerprint("glob", map[string]any{"pattern": "**/*.go"})
	if gb != "**/*.go" {
		t.Errorf("glob fingerprint = %q", gb)
	}
}

// Regression test for v0.79.6: grep/glob fingerprints used to ignore the
// `path` argument, so searching the same pattern in different directories
// (legitimate exploration) tripped the 5-repeat stagnation abort. Both now
// include the path when present.
func TestStagnationFingerprint_GrepAndGlobIncludePath(t *testing.T) {
	for _, tool := range []string{"grep", "glob"} {
		t.Run(tool, func(t *testing.T) {
			a := stagnationFingerprint(tool, map[string]any{"pattern": "TODO", "path": "internal/app"})
			b := stagnationFingerprint(tool, map[string]any{"pattern": "TODO", "path": "internal/ui"})
			if a == b {
				t.Errorf("same pattern in different paths must not collide: %q", a)
			}
			// Bare pattern still works (no path arg).
			bare := stagnationFingerprint(tool, map[string]any{"pattern": "TODO"})
			if bare != "TODO" {
				t.Errorf("bare pattern fingerprint = %q, want TODO", bare)
			}
			// Same pattern + same path still collide (real stagnation).
			same1 := stagnationFingerprint(tool, map[string]any{"pattern": "TODO", "path": "internal/app"})
			same2 := stagnationFingerprint(tool, map[string]any{"pattern": "TODO", "path": "internal/app"})
			if same1 != same2 {
				t.Errorf("identical args must share fingerprint: %q vs %q", same1, same2)
			}
		})
	}
}

func TestStagnationFingerprint_CopyMoveUseSource(t *testing.T) {
	for _, tool := range []string{"copy", "move"} {
		got := stagnationFingerprint(tool, map[string]any{
			"source":      "/path/to/file.txt",
			"destination": "/other/file.txt",
		})
		// Fingerprint must include both source and destination so that copying
		// the same file to different destinations is not mistaken for stagnation.
		if got != "file.txt->file.txt" {
			t.Errorf("%s fingerprint = %q, want file.txt->file.txt", tool, got)
		}
	}
}

func TestStagnationFingerprint_CopyMoveDifferentDest(t *testing.T) {
	fp1 := stagnationFingerprint("copy", map[string]any{
		"source":      "/src/file.txt",
		"destination": "/dst1/file.txt",
	})
	fp2 := stagnationFingerprint("copy", map[string]any{
		"source":      "/src/file.txt",
		"destination": "/dst2/other.txt",
	})
	if fp1 == fp2 {
		t.Errorf("copy to different destinations must not share a fingerprint, got %q for both", fp1)
	}
}

func TestStagnationFingerprint_WebFetchTruncatesURL(t *testing.T) {
	url := "https://example.com/very-long-path/that-exceeds-fifty-characters-eventually/nested/deep"
	got := stagnationFingerprint("web_fetch", map[string]any{"url": url})
	if len(got) > 50 {
		t.Errorf("web_fetch fingerprint len = %d, want ≤ 50", len(got))
	}
}

func TestStagnationFingerprint_WebSearchUsesQuery(t *testing.T) {
	got := stagnationFingerprint("web_search", map[string]any{"query": "golang test patterns"})
	if got != "golang test patterns" {
		t.Errorf("web_search fingerprint = %q", got)
	}
}

func TestStagnationFingerprint_UnknownToolReturnsEmpty(t *testing.T) {
	got := stagnationFingerprint("mystery_tool", map[string]any{"file_path": "/x"})
	if got != "" {
		t.Errorf("unknown tool should produce empty fingerprint, got %q", got)
	}
}

func TestStagnationFingerprint_MissingArgReturnsEmpty(t *testing.T) {
	// Write: no file_path → empty fingerprint.
	if got := stagnationFingerprint("write", map[string]any{}); got != "" {
		t.Errorf("write: missing file_path should produce empty fingerprint, got %q", got)
	}
	// Read: same contract. Without this, a nil-arg call would fingerprint
	// as ".@0+0" (filepath.Base("") on Unix) and stagnate malformed calls
	// against unrelated real reads.
	if got := stagnationFingerprint("read", map[string]any{}); got != "" {
		t.Errorf("read: missing file_path should produce empty fingerprint, got %q", got)
	}
	if got := stagnationFingerprint("read", map[string]any{"file_path": ""}); got != "" {
		t.Errorf("read: empty file_path should produce empty fingerprint, got %q", got)
	}
	// Five malformed reads in a row must still trigger stagnation via the
	// consecutive-identical-pattern path (they all produce "").
	if got1, got2 := stagnationFingerprint("read", map[string]any{"offset": 0}),
		stagnationFingerprint("read", map[string]any{"offset": 100}); got1 != got2 || got1 != "" {
		t.Errorf("reads without file_path must share the empty fingerprint: got1=%q got2=%q", got1, got2)
	}
}

// Regression test for v0.79.6: run_tests / verify_code had no fingerprint,
// so 5 consecutive iterations against the same package (each with a
// progressively narrower filter — a normal debugging workflow) tripped the
// stagnation abort. Now the fingerprint includes path+filter so narrowing
// a filter is forward progress.
func TestStagnationFingerprint_RunTestsIncludesFilter(t *testing.T) {
	for _, tool := range []string{"run_tests", "verify_code"} {
		t.Run(tool, func(t *testing.T) {
			a := stagnationFingerprint(tool, map[string]any{"path": "internal/app", "filter": "TestFoo"})
			b := stagnationFingerprint(tool, map[string]any{"path": "internal/app", "filter": "TestBar"})
			if a == b {
				t.Errorf("same path with different filter must not collide: %q", a)
			}
			c := stagnationFingerprint(tool, map[string]any{"path": "internal/app", "filter": "TestFoo"})
			if a != c {
				t.Errorf("identical args must share fingerprint: %q vs %q", a, c)
			}
			// Empty args fall through to "" — bare retries should still
			// trigger stagnation.
			if got := stagnationFingerprint(tool, map[string]any{}); got != "" {
				t.Errorf("empty args should produce empty fingerprint, got %q", got)
			}
		})
	}
}

func TestStagnationFingerprint_WrongArgTypeReturnsEmpty(t *testing.T) {
	// file_path is an int instead of a string — type assertion should fall through.
	got := stagnationFingerprint("write", map[string]any{"file_path": 42})
	if got != "" {
		t.Errorf("wrong type for file_path should produce empty fingerprint, got %q", got)
	}
}
