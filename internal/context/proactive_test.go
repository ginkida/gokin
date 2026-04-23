package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helper: drop a file with some bytes into dir; fails the test on error.
func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestProactiveReader_GoFileFindsPairAndSibling(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "foo.go", "package x\n\nfunc Foo() {}\n")
	writeFile(t, dir, "foo_test.go", "package x\n\nfunc TestFoo(t *testing.T) {}\n")
	writeFile(t, dir, "bar.go", "package x\n\nfunc Bar() {}\n")

	r := NewProactiveReader(dir, 3, 40)
	got := r.RelatedFor(src, nil)

	if len(got) == 0 {
		t.Fatalf("expected related files, got none")
	}
	// paired test must be first
	if !strings.HasSuffix(got[0].Path, "foo_test.go") || got[0].Reason != "paired test" {
		t.Errorf("expected foo_test.go paired test first, got %+v", got[0])
	}
	// bar.go should appear as a sibling
	foundSib := false
	for _, f := range got {
		if strings.HasSuffix(f.Path, "bar.go") && f.Reason == "package sibling" {
			foundSib = true
		}
	}
	if !foundSib {
		t.Errorf("expected bar.go as package sibling, got %+v", got)
	}
}

func TestProactiveReader_PythonPairPrefix(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "service.py", "def f(): pass\n")
	writeFile(t, dir, "test_service.py", "def test_f(): pass\n")

	r := NewProactiveReader(dir, 3, 40)
	got := r.RelatedFor(src, nil)

	if len(got) == 0 || !strings.HasSuffix(got[0].Path, "test_service.py") {
		t.Fatalf("expected test_service.py as paired test, got %+v", got)
	}
	if got[0].Reason != "paired test" {
		t.Errorf("expected reason 'paired test', got %q", got[0].Reason)
	}
}

func TestProactiveReader_JSDotTestSuffix(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "api.ts", "export const f = () => 1;\n")
	writeFile(t, dir, "api.test.ts", "it('works', () => {});\n")

	r := NewProactiveReader(dir, 3, 40)
	got := r.RelatedFor(src, nil)

	if len(got) == 0 || !strings.HasSuffix(got[0].Path, "api.test.ts") {
		t.Fatalf("expected api.test.ts as paired test, got %+v", got)
	}
}

func TestProactiveReader_NonSourceExtensionReturnsNil(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "NOTES.md", "# hi\n")
	writeFile(t, dir, "OTHER.md", "# hi2\n")

	r := NewProactiveReader(dir, 3, 40)
	if got := r.RelatedFor(src, nil); got != nil {
		t.Fatalf("expected nil for .md, got %+v", got)
	}
}

func TestProactiveReader_NoExtensionReturnsNil(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "Makefile", "all:\n\techo hi\n")

	r := NewProactiveReader(dir, 3, 40)
	if got := r.RelatedFor(src, nil); got != nil {
		t.Fatalf("expected nil for extensionless file, got %+v", got)
	}
}

func TestProactiveReader_AlreadyReadFilterSkips(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "foo.go", "package x\n")
	paired := writeFile(t, dir, "foo_test.go", "package x\n")
	sib := writeFile(t, dir, "bar.go", "package x\n")

	r := NewProactiveReader(dir, 3, 40)
	already := map[string]bool{paired: true, sib: true}
	if got := r.RelatedFor(src, already); got != nil {
		t.Fatalf("expected nil when everything already read, got %+v", got)
	}
}

func TestProactiveReader_MaxFilesCapHonored(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "foo.go", "package x\n")
	// Intentionally no paired test — forces siblings-only path so the cap
	// is visibly applied to the unrestricted candidate list.
	for _, n := range []string{"a.go", "b.go", "c.go", "d.go", "e.go"} {
		writeFile(t, dir, n, "package x\n")
	}

	r := NewProactiveReader(dir, 2, 40)
	got := r.RelatedFor(src, nil)
	if len(got) != 2 {
		t.Fatalf("expected cap=2, got %d: %+v", len(got), got)
	}
}

func TestProactiveReader_AlreadyPairedTestSrcSkipsPair(t *testing.T) {
	dir := t.TempDir()
	// feed it a _test.go file — no further pair expected
	src := writeFile(t, dir, "foo_test.go", "package x\n")
	writeFile(t, dir, "foo.go", "package x\n")

	r := NewProactiveReader(dir, 3, 40)
	got := r.RelatedFor(src, nil)
	// Should still return foo.go as a package sibling; but not recurse
	// back into a "paired test" classification for it.
	for _, f := range got {
		if f.Reason == "paired test" {
			t.Errorf("should not classify anything as paired test when source is a test, got %+v", f)
		}
	}
}

func TestFormatBlock_EmptyReturnsEmpty(t *testing.T) {
	if got := FormatBlock(nil, "/tmp"); got != "" {
		t.Errorf("expected empty for nil, got %q", got)
	}
}

func TestFormatBlock_RendersRelativePaths(t *testing.T) {
	dir := t.TempDir()
	files := []RelatedFile{
		{Path: filepath.Join(dir, "pkg", "foo.go"), Preview: "package pkg\n", Reason: "package sibling"},
	}
	out := FormatBlock(files, dir)
	if !strings.Contains(out, "--- Related files in this package ---") {
		t.Errorf("missing header: %q", out)
	}
	if !strings.Contains(out, "### pkg/foo.go  (package sibling)") {
		t.Errorf("missing relative-path heading: %q", out)
	}
	if !strings.Contains(out, "```\npackage pkg") {
		t.Errorf("missing code fence with preview: %q", out)
	}
}

func TestFormatBlock_FallsBackToAbsWhenOutsideWorkdir(t *testing.T) {
	// A path that cannot be expressed as relative to workdir without ".."
	abs := "/some/elsewhere/file.go"
	files := []RelatedFile{
		{Path: abs, Preview: "pkg\n", Reason: "paired test"},
	}
	out := FormatBlock(files, "/unrelated/workdir")
	if !strings.Contains(out, abs) {
		t.Errorf("expected absolute path fallback, got %q", out)
	}
}

func TestProactiveReader_DefensiveDefaults(t *testing.T) {
	r := NewProactiveReader("/tmp", 0, 0)
	if r.maxFiles <= 0 || r.maxLinesPerFile <= 0 {
		t.Fatalf("defaults not applied: %+v", r)
	}
}

func TestProactiveReader_PreviewLineCap(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "foo.go", "package x\n")
	// 50 lines of real content
	var body strings.Builder
	body.WriteString("package x\n")
	for range 50 {
		body.WriteString("// line\n")
	}
	writeFile(t, dir, "bar.go", body.String())

	r := NewProactiveReader(dir, 3, 10)
	got := r.RelatedFor(src, nil)
	if len(got) == 0 {
		t.Fatalf("expected candidates, got none")
	}
	var bar *RelatedFile
	for i := range got {
		if strings.HasSuffix(got[i].Path, "bar.go") {
			bar = &got[i]
			break
		}
	}
	if bar == nil {
		t.Fatalf("bar.go not found in candidates")
	}
	lines := strings.Count(bar.Preview, "\n")
	// With maxLines=10 the preview holds at most 10 lines joined by 9 "\n".
	if lines > 10 {
		t.Errorf("expected ≤10 newlines in preview, got %d", lines)
	}
}

func TestIsSourceExtension(t *testing.T) {
	cases := map[string]bool{
		".go":    true,
		".py":    true,
		".ts":    true,
		".rs":    true,
		".md":    false,
		".json":  false,
		".yaml":  false,
		".":      false,
		".GO":    true, // case-insensitive
	}
	for ext, want := range cases {
		if got := isSourceExtension(ext); got != want {
			t.Errorf("isSourceExtension(%q) = %v, want %v", ext, got, want)
		}
	}
}

func TestRelatedBlock_IntegrationWithFormat(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "foo.go", "package x\n")
	writeFile(t, dir, "foo_test.go", "package x\nfunc Test() {}\n")

	r := NewProactiveReader(dir, 3, 40)
	got := r.RelatedBlock(src, nil)
	if got == "" {
		t.Fatalf("expected non-empty block")
	}
	if !strings.Contains(got, "foo_test.go") {
		t.Errorf("block missing foo_test.go: %q", got)
	}
	if !strings.Contains(got, "paired test") {
		t.Errorf("block missing paired-test reason: %q", got)
	}
}
