package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
)

// checkGopls reports whether gopls is on PATH and returns its version.
func checkGopls(t *testing.T) (available bool) {
	t.Helper()
	_, err := exec.LookPath("gopls")
	return err == nil
}

// setupGoModule creates a temp Go module with one source file. The dir is
// symlink-resolved (macOS /var → /private/var) because the tools' PathValidator
// rejects symlinked roots — see CLAUDE.md "Known".
func setupGoModule(t *testing.T) (dir, srcFile string) {
	t.Helper()
	dir = testkit.ResolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/testmod\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	srcFile = filepath.Join(dir, "main.go")
	src := `package main

type MyStruct struct {
	Value int
}

func NewMyStruct() *MyStruct {
	return &MyStruct{Value: 42}
}

func (m *MyStruct) GetValue() int {
	return m.Value
}

func main() {
	s := NewMyStruct()
	_ = s.GetValue()
}
`
	if err := os.WriteFile(srcFile, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	return dir, srcFile
}

// TestGoToDefinition_ValidateRejectsMissingFile tests the Validate method directly.
func TestGoToDefinition_ValidateRejectsMissingFile(t *testing.T) {
	tool := NewGoToDefinitionTool(t.TempDir())
	err := tool.Validate(map[string]any{
		"symbol": "foo",
	})
	if err == nil {
		t.Fatal("expected error when file is missing")
	}
	if !strings.Contains(err.Error(), "file") {
		t.Fatalf("expected 'file' in error, got: %v", err)
	}
}

// TestGoToDefinition_FindsSymbol tests that the tool can find a symbol definition.
// When gopls is available it uses gopls (needs line:col); when not, it falls back to AST.
func TestGoToDefinition_FindsSymbol(t *testing.T) {
	dir, srcFile := setupGoModule(t)

	// Find the line of NewMyStruct definition
	content, _ := os.ReadFile(srcFile)
	lineNum := 0
	for i, l := range strings.Split(string(content), "\n") {
		if strings.Contains(l, "func NewMyStruct") {
			lineNum = i + 1
			break
		}
	}
	if lineNum == 0 {
		t.Fatal("could not find NewMyStruct in source")
	}

	tool := NewGoToDefinitionTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{
		"file":   srcFile,
		"symbol": "NewMyStruct",
		"line":   float64(lineNum),
		"column": float64(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "NewMyStruct") {
		t.Fatalf("expected output to mention NewMyStruct, got: %s", result.Content)
	}
}

// TestGoToDefinition_NonGoFile tests behavior with a non-Go file. With no line
// number, gopls is skipped and a non-Go file has no AST fallback, so the result
// is a deterministic error regardless of whether gopls is installed.
func TestGoToDefinition_NonGoFile(t *testing.T) {
	tool := NewGoToDefinitionTool(testkit.ResolvedTempDir(t))
	result, err := tool.Execute(context.Background(), map[string]any{
		"file":   "README.md",
		"symbol": "foo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatalf("expected failure for a non-Go file without line, got success: %s", result.Content)
	}
	if !strings.Contains(result.Content+result.Error, "non-Go") {
		t.Fatalf("expected 'non-Go' hint, got content=%q error=%q", result.Content, result.Error)
	}
}

// TestFindReferences_ValidateRejectsMissingFile tests the Validate method directly.
func TestFindReferences_ValidateRejectsMissingFile(t *testing.T) {
	tool := NewFindReferencesTool(t.TempDir())
	err := tool.Validate(map[string]any{
		"symbol": "foo",
	})
	if err == nil {
		t.Fatal("expected error when file is missing")
	}
}

// TestFindReferences_FindsUsage tests that the tool can find references.
// When gopls is available it uses gopls (needs line:col); when not, it falls back to grep.
func TestFindReferences_FindsUsage(t *testing.T) {
	dir, srcFile := setupGoModule(t)

	// Find the line of GetValue usage in main() (not the definition)
	content, _ := os.ReadFile(srcFile)
	lineNum := 0
	insideMain := false
	for i, l := range strings.Split(string(content), "\n") {
		if strings.Contains(l, "func main()") {
			insideMain = true
			continue
		}
		if insideMain && strings.Contains(l, "GetValue()") {
			lineNum = i + 1
			break
		}
	}
	if lineNum == 0 {
		t.Fatal("could not find GetValue usage in main()")
	}

	// Find column of GetValue within this line
	col := strings.Index(strings.Split(string(content), "\n")[lineNum-1], "GetValue") + 1
	if col <= 0 {
		col = 1
	}

	tool := NewFindReferencesTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{
		"file":   srcFile,
		"symbol": "GetValue",
		"line":   float64(lineNum),
		"column": float64(col),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "GetValue") {
		t.Fatalf("expected output to mention GetValue, got: %s", result.Content)
	}
}

// TestFindReferences_NonGoFile tests behavior with a non-Go file. No line +
// non-Go file => deterministic error result regardless of gopls.
func TestFindReferences_NonGoFile(t *testing.T) {
	tool := NewFindReferencesTool(testkit.ResolvedTempDir(t))
	result, err := tool.Execute(context.Background(), map[string]any{
		"file":   "README.md",
		"symbol": "foo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatalf("expected failure for a non-Go file without line, got success: %s", result.Content)
	}
	if !strings.Contains(result.Content+result.Error, "non-Go") {
		t.Fatalf("expected 'non-Go' hint, got content=%q error=%q", result.Content, result.Error)
	}
}

// TestGoToDefinition_NoLineFallsThrough verifies the no-line call falls through
// to the AST fallback instead of returning a hard error (regression: when gopls
// was installed, a no-line call short-circuited to an error and never reached
// the fallback, making a gopls machine worse than one without it).
func TestGoToDefinition_NoLineFallsThrough(t *testing.T) {
	dir, srcFile := setupGoModule(t)
	tool := NewGoToDefinitionTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{
		"file":   srcFile,
		"symbol": "NewMyStruct",
		// no line — must use the AST fallback, not error out
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("no-line call should fall through to AST fallback, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "NewMyStruct") {
		t.Fatalf("expected NewMyStruct in output, got: %s", res.Content)
	}
}

// TestFindReferences_NoLineFallsThrough is the find_references twin of the above.
func TestFindReferences_NoLineFallsThrough(t *testing.T) {
	dir, srcFile := setupGoModule(t)
	tool := NewFindReferencesTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{
		"file":   srcFile,
		"symbol": "GetValue",
		// no line — must use the text-search fallback, not error out
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("no-line call should fall through to fallback, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "GetValue") {
		t.Fatalf("expected GetValue in output, got: %s", res.Content)
	}
}

// TestGoToDefinition_ASTFallbackReturnsDeclaration verifies the AST fallback
// returns the symbol's DECLARATION (not every occurrence) and labels it as a
// definition — not "References to".
func TestGoToDefinition_ASTFallbackReturnsDeclaration(t *testing.T) {
	dir, srcFile := setupGoModule(t)
	tool := NewGoToDefinitionTool(dir)
	res, err := tool.definitionViaAST(srcFile, "NewMyStruct")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}
	// NewMyStruct is declared on line 7 of the canonical fixture.
	if !strings.Contains(res.Content, "main.go:7") {
		t.Fatalf("expected declaration at main.go:7, got: %s", res.Content)
	}
	if !strings.Contains(res.Content, "Definition") {
		t.Fatalf("expected a 'Definition' header, got: %s", res.Content)
	}
	// Must NOT emit the usage line inside main() (line 16) nor mislabel as refs.
	if strings.Contains(res.Content, "main.go:16") {
		t.Fatalf("AST fallback should return the declaration, not the usage: %s", res.Content)
	}
	if strings.Contains(res.Content, "References to") {
		t.Fatalf("AST fallback should not be labelled 'References to': %s", res.Content)
	}
}

// TestFindReferences_FileScanFallback verifies the filesystem-scan fallback
// works outside a git repo (CI has no gopls and the fixture is not a git repo).
func TestFindReferences_FileScanFallback(t *testing.T) {
	dir, _ := setupGoModule(t)
	tool := NewFindReferencesTool(dir)
	res, err := tool.referencesViaFileScan(context.Background(), "GetValue")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}
	if !strings.Contains(res.Content, "GetValue") {
		t.Fatalf("expected GetValue match, got: %s", res.Content)
	}
}

// TestFindReferences_FileScanWholeWord pins whole-word matching: searching for
// "Get" must not match the identifier "GetValue".
func TestFindReferences_FileScanWholeWord(t *testing.T) {
	dir := t.TempDir()
	src := "package p\n\nfunc Get() {}\nfunc GetValue() {}\nvar _ = Get\n"
	if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewFindReferencesTool(dir)
	res, err := tool.referencesViaFileScan(context.Background(), "Get")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "func Get()") {
		t.Fatalf("expected whole-word Get match, got: %s", res.Content)
	}
	if strings.Contains(res.Content, "GetValue() {}") {
		t.Fatalf("whole-word search must not match GetValue: %s", res.Content)
	}
}

// TestContainsWholeWord pins the matcher used by the file-scan fallback.
func TestContainsWholeWord(t *testing.T) {
	cases := []struct {
		line, word string
		want       bool
	}{
		{"x := Get()", "Get", true},
		{"x := GetValue()", "Get", false},
		{"Get", "Get", true},
		{"prefixGet", "Get", false},
		{"a.Get.b", "Get", true},
		{"", "Get", false},
		{"Get", "", false},
	}
	for _, c := range cases {
		if got := containsWholeWord(c.line, c.word); got != c.want {
			t.Errorf("containsWholeWord(%q, %q) = %v, want %v", c.line, c.word, got, c.want)
		}
	}
}

// TestFindReferences_GitGrepInRepo exercises the git-grep path (vs. the
// filesystem fallback) by running inside a real, committed git repo.
func TestFindReferences_GitGrepInRepo(t *testing.T) {
	dir := setupGitRepo(t)
	addTrackedFile(t, dir, "lib.go", "package main\n\nfunc Widget() int { return 1 }\n\nvar _ = Widget\n")
	tool := NewFindReferencesTool(dir)
	res, err := tool.referencesViaGrep(context.Background(), "Widget")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}
	if !strings.Contains(res.Content, "Widget") || !strings.Contains(res.Content, "git grep") {
		t.Fatalf("expected git-grep matches for Widget, got: %s", res.Content)
	}
}

// TestSemanticTools_RejectOutOfWorkspacePath pins the PathValidator wiring:
// both tools are RiskLow + auto-allowed, so an absolute path outside workDir
// must be rejected exactly like read/grep would — without this they were a
// prompt-free read primitive over arbitrary files (review-confirmed bypass).
func TestSemanticTools_RejectOutOfWorkspacePath(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	outside := testkit.ResolvedTempDir(t)
	outsideFile := filepath.Join(outside, "secret.go")
	if err := os.WriteFile(outsideFile, []byte("package x\n\nvar apiKey = \"s\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	def := NewGoToDefinitionTool(dir)
	res, err := def.Execute(context.Background(), map[string]any{"file": outsideFile, "symbol": "apiKey"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Success || !strings.Contains(res.Error, "security error") {
		t.Fatalf("go_to_definition must reject out-of-workspace paths, got success=%v err=%q", res.Success, res.Error)
	}

	refs := NewFindReferencesTool(dir)
	res, err = refs.Execute(context.Background(), map[string]any{"file": outsideFile, "symbol": "apiKey"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Success || !strings.Contains(res.Error, "security error") {
		t.Fatalf("find_references must reject out-of-workspace paths, got success=%v err=%q", res.Success, res.Error)
	}
}

// TestGoplsAvailable exercises the gopls availability check.
func TestGoplsAvailable(t *testing.T) {
	avail := goplsAvailable()
	if checkGopls(t) != avail {
		t.Logf("goplsAvailable()=%v, checkGopls()=%v (may differ if gopls has no version output)", avail, checkGopls(t))
	}
}

// TestGoToDefinition_ResolvePath tests path resolution.
func TestGoToDefinition_ResolvePath(t *testing.T) {
	tool := NewGoToDefinitionTool("/workspace")
	tests := []struct {
		path string
		want string
	}{
		{"/absolute/path/file.go", "/absolute/path/file.go"},
		{"relative/file.go", "/workspace/relative/file.go"},
		{"file.go", "/workspace/file.go"},
	}
	for _, tc := range tests {
		got := tool.resolvePath(tc.path)
		if got != tc.want {
			t.Errorf("resolvePath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestFindReferences_ResolvePath tests path resolution.
func TestFindReferences_ResolvePath(t *testing.T) {
	tool := NewFindReferencesTool("/workspace")
	tests := []struct {
		path string
		want string
	}{
		{"/abs/file.go", "/abs/file.go"},
		{"rel/file.go", "/workspace/rel/file.go"},
	}
	for _, tc := range tests {
		got := tool.resolvePath(tc.path)
		if got != tc.want {
			t.Errorf("resolvePath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
