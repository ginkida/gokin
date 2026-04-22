package tools

import "testing"

func TestStagnationFingerprint_DistinguishesFilesForWriteEditReadDelete(t *testing.T) {
	for _, tool := range []string{"write", "edit", "read", "delete"} {
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
	if len(long) > 60 {
		t.Errorf("bash fingerprint len = %d, want ≤ 60", len(long))
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

func TestStagnationFingerprint_CopyMoveUseSource(t *testing.T) {
	for _, tool := range []string{"copy", "move"} {
		got := stagnationFingerprint(tool, map[string]any{
			"source":      "/path/to/file.txt",
			"destination": "/other/file.txt",
		})
		if got != "file.txt" {
			t.Errorf("%s fingerprint = %q, want basename file.txt", tool, got)
		}
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
	got := stagnationFingerprint("write", map[string]any{})
	if got != "" {
		t.Errorf("missing file_path should produce empty fingerprint, got %q", got)
	}
}

func TestStagnationFingerprint_WrongArgTypeReturnsEmpty(t *testing.T) {
	// file_path is an int instead of a string — type assertion should fall through.
	got := stagnationFingerprint("write", map[string]any{"file_path": 42})
	if got != "" {
		t.Errorf("wrong type for file_path should produce empty fingerprint, got %q", got)
	}
}
