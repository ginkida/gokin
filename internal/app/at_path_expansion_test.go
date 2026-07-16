package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/config"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func newAtRefTestApp(t *testing.T) (*App, string) {
	t.Helper()
	workDir := testkit.ResolvedTempDir(t)
	a := &App{
		workDir:  workDir,
		config:   config.DefaultConfig(),
		registry: tools.DefaultRegistry(workDir),
	}
	return a, workDir
}

func TestExpandAtReferences_InWorkspaceFile(t *testing.T) {
	a, work := newAtRefTestApp(t)
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out := a.expandAtReferences("look at @main.go please")
	if !strings.Contains(out, "look at @main.go please") {
		t.Errorf("original message must be preserved verbatim:\n%s", out)
	}
	if !strings.Contains(out, "--- Referenced files ---") || !strings.Contains(out, "[file: main.go]") {
		t.Errorf("expected a referenced-files section for main.go:\n%s", out)
	}
	if !strings.Contains(out, "package main") {
		t.Errorf("file content not inlined:\n%s", out)
	}
}

func TestRecoverySubmitDoesNotExpandAtReferenceTwice(t *testing.T) {
	a, work := newAtRefTestApp(t)
	if err := os.WriteFile(filepath.Join(work, "snapshot.txt"), []byte("NEW CONTENT\n"), 0644); err != nil {
		t.Fatal(err)
	}
	persisted := "inspect @snapshot.txt\n\n--- Referenced files ---\n[file: snapshot.txt]\nOLD CONTENT\n[/file]"

	agentMessage, memoryQuery := a.prepareAgentMessageForSubmit(
		persisted, "inspect @snapshot.txt", true)
	if agentMessage != persisted || memoryQuery != "inspect @snapshot.txt" {
		t.Fatalf("recovery payload changed:\nagent=%q\nmemory=%q", agentMessage, memoryQuery)
	}
	if strings.Contains(agentMessage, "NEW CONTENT") {
		t.Fatal("recovery re-read the changed @file")
	}

	fresh, _ := a.prepareAgentMessageForSubmit("inspect @snapshot.txt", "", false)
	if !strings.Contains(fresh, "NEW CONTENT") {
		t.Fatal("fresh submission did not expand @file")
	}
}

func TestExpandAtReferences_QuotedPathWithSpaces(t *testing.T) {
	a, work := newAtRefTestApp(t)
	if err := os.WriteFile(filepath.Join(work, "space file.go"), []byte("package spaced\n"), 0644); err != nil {
		t.Fatal(err)
	}

	out := a.expandAtReferences(`look at @"space file.go" please`)
	if !strings.Contains(out, `look at @"space file.go" please`) {
		t.Errorf("original quoted message must be preserved verbatim:\n%s", out)
	}
	if !strings.Contains(out, "[file: space file.go]") || !strings.Contains(out, "package spaced") {
		t.Errorf("quoted @ref with spaces should inline file content:\n%s", out)
	}
}

func TestExpandAtReferences_NoRefsUnchanged(t *testing.T) {
	a, _ := newAtRefTestApp(t)
	for _, msg := range []string{
		"just a normal message",
		"email me at user@host.com",       // @ not at index 0 — not a ref
		"ping @alice about this",          // resolves to nothing -> left as-is
		"weird @@double and a@b tokens",   // multi-@ excluded
		"@nonexistent-file.go is missing", // resolves to nothing
	} {
		if got := a.expandAtReferences(msg); got != msg {
			t.Errorf("message with no resolvable @ref must be unchanged.\n in:  %q\n out: %q", msg, got)
		}
	}
}

func TestExpandAtReferences_QuotedPathLineRange(t *testing.T) {
	a, work := newAtRefTestApp(t)
	content := "line1\nline2\nline3\nline4\n"
	if err := os.WriteFile(filepath.Join(work, "space file.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	out := a.expandAtReferences(`see @"space file.txt":2-3`)
	if !strings.Contains(out, "[file: space file.txt:2-3]") {
		t.Errorf("expected quoted range label:\n%s", out)
	}
	if !strings.Contains(out, "line2") || !strings.Contains(out, "line3") {
		t.Errorf("quoted range should include requested lines:\n%s", out)
	}
	if strings.Contains(out, "line1") || strings.Contains(out, "line4") {
		t.Errorf("quoted range must exclude lines outside 2-3:\n%s", out)
	}
}

func TestExpandAtReferences_OutOfWorkspaceSkipped(t *testing.T) {
	a, _ := newAtRefTestApp(t)
	// An absolute path outside the workspace must be skipped (message unchanged).
	msg := "read @/etc/hosts now"
	if got := a.expandAtReferences(msg); got != msg {
		t.Errorf("out-of-workspace @ref must be skipped:\n%s", got)
	}
}

func TestExpandAtReferences_Dedup(t *testing.T) {
	a, work := newAtRefTestApp(t)
	if err := os.WriteFile(filepath.Join(work, "x.go"), []byte("X\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out := a.expandAtReferences("@x.go and @./x.go")
	if n := strings.Count(out, "[file: x.go]"); n != 1 {
		t.Errorf("@x.go and @./x.go should dedup to ONE block, got %d:\n%s", n, out)
	}
}

func TestExpandAtReferences_BinarySkipped(t *testing.T) {
	a, work := newAtRefTestApp(t)
	if err := os.WriteFile(filepath.Join(work, "blob.bin"), []byte{0x00, 0x01, 0x02, 0x00}, 0644); err != nil {
		t.Fatal(err)
	}
	msg := "inspect @blob.bin"
	if got := a.expandAtReferences(msg); got != msg {
		t.Errorf("binary @ref (NUL byte) must be skipped:\n%s", got)
	}
}

func TestExpandAtReferences_PerFileTruncation(t *testing.T) {
	a, work := newAtRefTestApp(t)
	big := strings.Repeat("a", atRefMaxPerFile+5000)
	if err := os.WriteFile(filepath.Join(work, "big.txt"), []byte(big), 0644); err != nil {
		t.Fatal(err)
	}
	out := a.expandAtReferences("@big.txt")
	if !strings.Contains(out, "truncated") {
		t.Errorf("oversized file should be truncated with a marker:\n%s", out[:min(len(out), 400)])
	}
	// The inlined content must be capped at the per-file limit (plus chrome).
	if len(out) > atRefMaxPerFile+1000 {
		t.Errorf("expanded output not capped: len=%d", len(out))
	}
}

func TestExpandAtReferences_DirectorySkipped(t *testing.T) {
	a, work := newAtRefTestApp(t)
	if err := os.MkdirAll(filepath.Join(work, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	msg := "look in @src"
	if got := a.expandAtReferences(msg); got != msg {
		t.Errorf("a directory @ref must be skipped (v1):\n%s", got)
	}
}

func TestExpandAtReferences_LineRange(t *testing.T) {
	a, work := newAtRefTestApp(t)
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// @f.txt:2-4 -> only lines 2..4, labeled with the range.
	out := a.expandAtReferences("see @f.txt:2-4")
	if !strings.Contains(out, "[file: f.txt:2-4]") {
		t.Errorf("expected range label [file: f.txt:2-4]:\n%s", out)
	}
	if !strings.Contains(out, "line2") || !strings.Contains(out, "line4") {
		t.Errorf("range should include lines 2-4:\n%s", out)
	}
	if strings.Contains(out, "line1") || strings.Contains(out, "line5") {
		t.Errorf("range must EXCLUDE lines outside 2-4:\n%s", out)
	}

	// Single line @f.txt:3 -> just line3.
	out = a.expandAtReferences("@f.txt:3")
	if !strings.Contains(out, "[file: f.txt:3-3]") || !strings.Contains(out, "line3") || strings.Contains(out, "line2") {
		t.Errorf("single-line range wrong:\n%s", out)
	}

	// Same file, two different ranges -> NOT deduped.
	out = a.expandAtReferences("@f.txt:1-2 and @f.txt:4-5")
	if c := strings.Count(out, "[file: f.txt:"); c != 2 {
		t.Errorf("different ranges of one file must not dedup, got %d blocks:\n%s", c, out)
	}
}

func TestExpandAtReferences_BadRangeFallsBackOrSkips(t *testing.T) {
	a, work := newAtRefTestApp(t)
	if err := os.WriteFile(filepath.Join(work, "g.txt"), []byte("a\nb\nc\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Reversed range (:4-2) -> treated as plain path "g.txt:4-2" which doesn't
	// exist -> token left in place, message unchanged.
	msg := "@g.txt:4-2 hmm"
	if got := a.expandAtReferences(msg); got != msg {
		t.Errorf("reversed range should not expand:\n%s", got)
	}
	// Range past EOF (:50-60) -> skipped silently (message unchanged).
	msg2 := "@g.txt:50-60"
	if got := a.expandAtReferences(msg2); got != msg2 {
		t.Errorf("out-of-bounds range should be skipped:\n%s", got)
	}
}
