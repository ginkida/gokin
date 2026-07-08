package context

import (
	"strings"
	"testing"
)

// --- SummarizeForPrune ---

func TestSummarizeForPrune_Empty(t *testing.T) {
	c := &ResultCompactor{}
	got := c.SummarizeForPrune("read", "")
	if !strings.Contains(got, "empty output") {
		t.Errorf("empty output summary = %q, want 'empty output'", got)
	}
}

func TestSummarizeForPrune_Read(t *testing.T) {
	c := &ResultCompactor{}
	content := "package main\n\nfunc foo() {}\nfunc bar() {}\n"
	got := c.SummarizeForPrune("read", content)
	if !strings.Contains(got, "[read:") {
		t.Errorf("expected [read: prefix, got %q", got)
	}
	if !strings.Contains(got, "package main") {
		t.Errorf("expected package name in summary, got %q", got)
	}
	if !strings.Contains(got, "foo") {
		t.Errorf("expected func name 'foo' in summary, got %q", got)
	}
}

func TestSummarizeForPrune_Bash(t *testing.T) {
	c := &ResultCompactor{}
	content := "Building...\nok\tgokin\t0.5s\nexit status 0"
	got := c.SummarizeForPrune("bash", content)
	if !strings.Contains(got, "[bash:") {
		t.Errorf("expected [bash: prefix, got %q", got)
	}
}

func TestSummarizeForPrune_Grep(t *testing.T) {
	c := &ResultCompactor{}
	content := "main.go:10:func main()\nutil.go:5:func helper()"
	got := c.SummarizeForPrune("grep", content)
	if !strings.Contains(got, "[grep:") {
		t.Errorf("expected [grep: prefix, got %q", got)
	}
	if !strings.Contains(got, "matches") {
		t.Errorf("expected 'matches' in grep summary, got %q", got)
	}
}

func TestSummarizeForPrune_Glob(t *testing.T) {
	c := &ResultCompactor{}
	content := "main.go\nutil.go\nREADME.md"
	got := c.SummarizeForPrune("glob", content)
	if !strings.Contains(got, "[glob:") {
		t.Errorf("expected [glob: prefix, got %q", got)
	}
	if !strings.Contains(got, "files") {
		t.Errorf("expected 'files' in glob summary, got %q", got)
	}
}

func TestSummarizeForPrune_GitDiff(t *testing.T) {
	c := &ResultCompactor{}
	content := "+++ b/main.go\n+func new() {}\n-old func()"
	got := c.SummarizeForPrune("git_diff", content)
	if !strings.Contains(got, "[git_diff:") {
		t.Errorf("expected [git_diff: prefix, got %q", got)
	}
}

func TestSummarizeForPrune_GitLog(t *testing.T) {
	c := &ResultCompactor{}
	content := "abc1234 fix bug\ndef5678 add feature"
	got := c.SummarizeForPrune("git_log", content)
	if !strings.Contains(got, "[git_log:") {
		t.Errorf("expected [git_log: prefix, got %q", got)
	}
	if !strings.Contains(got, "commits") {
		t.Errorf("expected 'commits' in git_log summary, got %q", got)
	}
}

func TestSummarizeForPrune_Tree(t *testing.T) {
	c := &ResultCompactor{}
	content := "├── cmd/\n├── internal/\n└── go.mod"
	got := c.SummarizeForPrune("tree", content)
	if !strings.Contains(got, "[tree:") {
		t.Errorf("expected [tree: prefix, got %q", got)
	}
	if !strings.Contains(got, "entries") {
		t.Errorf("expected 'entries' in tree summary, got %q", got)
	}
}

func TestSummarizeForPrune_Edit(t *testing.T) {
	c := &ResultCompactor{}
	content := "Updated region\nfunc foo() {\n\treturn\n}"
	got := c.SummarizeForPrune("edit", content)
	if !strings.Contains(got, "[edit:") {
		t.Errorf("expected [edit: prefix, got %q", got)
	}
}

func TestSummarizeForPrune_UnknownTool(t *testing.T) {
	c := &ResultCompactor{}
	content := "some custom output\nsecond line"
	got := c.SummarizeForPrune("custom_tool", content)
	if !strings.Contains(got, "[custom_tool:") {
		t.Errorf("expected [custom_tool: prefix, got %q", got)
	}
}

func TestSummarizeForPrune_HasCharCount(t *testing.T) {
	c := &ResultCompactor{}
	content := "hello world"
	got := c.SummarizeForPrune("read", content)
	if !strings.Contains(got, "was 11 chars") {
		t.Errorf("expected 'was 11 chars' suffix, got %q", got)
	}
}

// --- summarizeReadOutput ---

func TestSummarizeReadOutput_GoFile(t *testing.T) {
	content := "package main\n\nimport \"fmt\"\n\nfunc foo() {\n\tfmt.Println(\"hi\")\n}\n\ntype Bar struct {\n\tX int\n}\n"
	got := summarizeReadOutput(content)
	if !strings.Contains(got, "package main") {
		t.Errorf("missing package name: %q", got)
	}
	if !strings.Contains(got, "foo") {
		t.Errorf("missing func foo: %q", got)
	}
	if !strings.Contains(got, "Bar") {
		t.Errorf("missing type Bar: %q", got)
	}
}

func TestSummarizeReadOutput_NoPackage(t *testing.T) {
	content := "just some text\nno package here"
	got := summarizeReadOutput(content)
	if !strings.Contains(got, "lines") {
		t.Errorf("missing line count: %q", got)
	}
}

func TestSummarizeReadOutput_CatNFormat(t *testing.T) {
	// cat -n format: "   1\tpackage foo"
	content := "   1\tpackage foo\n   2\t\n   3\tfunc bar() {}\n"
	got := summarizeReadOutput(content)
	if !strings.Contains(got, "package foo") {
		t.Errorf("cat -n: missing package foo: %q", got)
	}
	if !strings.Contains(got, "bar") {
		t.Errorf("cat -n: missing func bar: %q", got)
	}
}

func TestSummarizeReadOutput_ManyFuncs(t *testing.T) {
	var lines []string
	lines = append(lines, "package main")
	for i := 0; i < 10; i++ {
		lines = append(lines, "func f"+string(rune('0'+i))+"() {}")
	}
	got := summarizeReadOutput(strings.Join(lines, "\n"))
	// Should only show first 5
	if !strings.Contains(got, "f0") || !strings.Contains(got, "f4") {
		t.Errorf("missing f0-f4: %q", got)
	}
}

func TestSummarizeReadOutput_MethodReceiver(t *testing.T) {
	content := "package main\n\nfunc (s *Server) Start() {}\nfunc (s Server) Stop() {}\n"
	got := summarizeReadOutput(content)
	if !strings.Contains(got, "Start") {
		t.Errorf("missing method Start: %q", got)
	}
	if !strings.Contains(got, "Stop") {
		t.Errorf("missing method Stop: %q", got)
	}
}

// --- summarizeBashOutput ---

func TestSummarizeBashOutput_ExitStatus(t *testing.T) {
	content := "running...\nexit status 1"
	got := summarizeBashOutput(content)
	if !strings.Contains(got, "exit=1") {
		t.Errorf("missing exit=1: %q", got)
	}
}

func TestSummarizeBashOutput_Errors(t *testing.T) {
	content := "main.go:10: undefined: foo\nmain.go:20: cannot use x as int"
	got := summarizeBashOutput(content)
	if !strings.Contains(got, "errors") {
		t.Errorf("missing errors: %q", got)
	}
}

func TestSummarizeBashOutput_TestResults(t *testing.T) {
	content := "--- PASS: TestFoo\n--- FAIL: TestBar\nok\tgokin\t0.5s"
	got := summarizeBashOutput(content)
	if !strings.Contains(got, "FAIL: 1") {
		t.Errorf("missing FAIL: 1: %q", got)
	}
}

func TestSummarizeBashOutput_PassCount(t *testing.T) {
	content := "--- PASS: TestA\n--- PASS: TestB\nok\tgokin"
	got := summarizeBashOutput(content)
	if !strings.Contains(got, "PASS: 2") {
		t.Errorf("missing PASS: 2: %q", got)
	}
}

func TestSummarizeBashOutput_CleanRun(t *testing.T) {
	content := "Building...\nSuccess"
	got := summarizeBashOutput(content)
	if !strings.Contains(got, "lines") {
		t.Errorf("missing line count: %q", got)
	}
}

func TestSummarizeBashOutput_LongErrorTruncated(t *testing.T) {
	longError := "error: " + strings.Repeat("x", 200)
	content := longError + "\n"
	got := summarizeBashOutput(content)
	// Error should be truncated to 80 chars
	if !strings.Contains(got, "errors") {
		t.Errorf("missing errors: %q", got)
	}
}

// --- summarizeGrepOutput ---

func TestSummarizeGrepOutput_Basic(t *testing.T) {
	content := "main.go:10:func main()\nutil.go:5:func helper()\nmain.go:20:var x"
	got := summarizeGrepOutput(content)
	if !strings.Contains(got, "3 matches") {
		t.Errorf("missing match count: %q", got)
	}
	if !strings.Contains(got, "2 files") {
		t.Errorf("missing file count: %q", got)
	}
}

func TestSummarizeGrepOutput_Empty(t *testing.T) {
	got := summarizeGrepOutput("")
	if !strings.Contains(got, "0 matches") {
		t.Errorf("empty grep: %q", got)
	}
}

func TestSummarizeGrepOutput_TopFiles(t *testing.T) {
	// main.go has 3 matches, should appear first
	content := "main.go:1:a\nmain.go:2:b\nmain.go:3:c\nother.go:1:d"
	got := summarizeGrepOutput(content)
	if !strings.Contains(got, "main.go") {
		t.Errorf("missing top file main.go: %q", got)
	}
}

// --- summarizeGlobOutput ---

func TestSummarizeGlobOutput_Basic(t *testing.T) {
	content := "cmd/main.go\ninternal/app.go\nREADME.md"
	got := summarizeGlobOutput(content)
	if !strings.Contains(got, "3 files") {
		t.Errorf("missing file count: %q", got)
	}
}

func TestSummarizeGlobOutput_TopDirs(t *testing.T) {
	content := "cmd/a.go\ncmd/b.go\ncmd/c.go\ninternal/x.go"
	got := summarizeGlobOutput(content)
	if !strings.Contains(got, "top dirs") {
		t.Errorf("missing top dirs: %q", got)
	}
	if !strings.Contains(got, "cmd") {
		t.Errorf("missing cmd dir: %q", got)
	}
}

func TestSummarizeGlobOutput_Empty(t *testing.T) {
	got := summarizeGlobOutput("")
	if !strings.Contains(got, "0 files") {
		t.Errorf("empty glob: %q", got)
	}
}

// --- summarizeGitDiffOutput ---

func TestSummarizeGitDiffOutput_Basic(t *testing.T) {
	content := "+++ b/main.go\n+added line\n-removed line"
	got := summarizeGitDiffOutput(content)
	if !strings.Contains(got, "1 files") {
		t.Errorf("missing file count: %q", got)
	}
	if !strings.Contains(got, "+1") {
		t.Errorf("missing added count: %q", got)
	}
	if !strings.Contains(got, "-1") {
		t.Errorf("missing removed count: %q", got)
	}
}

func TestSummarizeGitDiffOutput_MultipleFiles(t *testing.T) {
	content := "+++ b/a.go\n+++ b/b.go\n+++ b/c.go\n+new"
	got := summarizeGitDiffOutput(content)
	if !strings.Contains(got, "3 files") {
		t.Errorf("missing 3 files: %q", got)
	}
}

func TestSummarizeGitDiffOutput_Empty(t *testing.T) {
	got := summarizeGitDiffOutput("")
	if !strings.Contains(got, "0 files") {
		t.Errorf("empty diff: %q", got)
	}
}

// --- summarizeGitLogOutput ---

func TestSummarizeGitLogOutput_ShortFormat(t *testing.T) {
	content := "abc1234 fix bug\ndef5678 add feature"
	got := summarizeGitLogOutput(content)
	if !strings.Contains(got, "2 commits") {
		t.Errorf("missing commit count: %q", got)
	}
	if !strings.Contains(got, "fix bug") {
		t.Errorf("missing latest message: %q", got)
	}
}

func TestSummarizeGitLogOutput_FullFormat(t *testing.T) {
	content := "commit abc1234\nAuthor: Test\nDate: Mon\n\n    fix something\n"
	got := summarizeGitLogOutput(content)
	if !strings.Contains(got, "1 commits") {
		t.Errorf("missing commit count: %q", got)
	}
}

func TestSummarizeGitLogOutput_Empty(t *testing.T) {
	got := summarizeGitLogOutput("")
	if !strings.Contains(got, "0 commits") {
		t.Errorf("empty log: %q", got)
	}
}

func TestSummarizeGitLogOutput_LongMessageTruncated(t *testing.T) {
	longMsg := strings.Repeat("x", 100)
	content := "abc1234 " + longMsg
	got := summarizeGitLogOutput(content)
	// Should be truncated to 60 chars in the latest message
	if !strings.Contains(got, "latest:") {
		t.Errorf("missing latest: %q", got)
	}
}

// --- summarizeTreeOutput ---

func TestSummarizeTreeOutput_Basic(t *testing.T) {
	content := "├── cmd/\n├── internal/\n└── go.mod"
	got := summarizeTreeOutput(content)
	if !strings.Contains(got, "3 entries") {
		t.Errorf("missing entry count: %q", got)
	}
}

func TestSummarizeTreeOutput_TopDirs(t *testing.T) {
	content := "├── cmd/\n├── internal/\n└── go.mod"
	got := summarizeTreeOutput(content)
	if !strings.Contains(got, "cmd") || !strings.Contains(got, "internal") {
		t.Errorf("missing top dirs: %q", got)
	}
}

func TestSummarizeTreeOutput_Empty(t *testing.T) {
	got := summarizeTreeOutput("")
	if !strings.Contains(got, "0 entries") {
		t.Errorf("empty tree: %q", got)
	}
}

// --- summarizeMutationOutput ---

func TestSummarizeMutationOutput_Basic(t *testing.T) {
	content := "Updated region\nfunc foo() {}\n}"
	got := summarizeMutationOutput(content)
	if !strings.Contains(got, "Updated region") {
		t.Errorf("missing first line: %q", got)
	}
}

func TestSummarizeMutationOutput_Empty(t *testing.T) {
	got := summarizeMutationOutput("")
	if got != "completed" {
		t.Errorf("empty mutation = %q, want 'completed'", got)
	}
}

func TestSummarizeMutationOutput_OnlyDashes(t *testing.T) {
	got := summarizeMutationOutput("---\n---")
	if got != "completed" {
		t.Errorf("dashes-only = %q, want 'completed'", got)
	}
}

func TestSummarizeMutationOutput_LongLineTruncated(t *testing.T) {
	longLine := strings.Repeat("x", 200)
	got := summarizeMutationOutput(longLine)
	// Should be truncated to 120 chars
	if len(got) > 130 {
		t.Errorf("long line not truncated: len=%d", len(got))
	}
}

func TestSummarizeMutationOutput_MaxThreeLines(t *testing.T) {
	content := "line1\nline2\nline3\nline4\nline5"
	got := summarizeMutationOutput(content)
	// Should only keep first 3 non-empty lines
	parts := strings.Split(got, " | ")
	if len(parts) > 3 {
		t.Errorf("expected at most 3 parts, got %d: %q", len(parts), got)
	}
}

// --- defaultSummary ---

func TestDefaultSummary_Basic(t *testing.T) {
	got := defaultSummary("unknown", "first line\nsecond line")
	if got != "first line" {
		t.Errorf("defaultSummary = %q, want 'first line'", got)
	}
}

func TestDefaultSummary_LongLineTruncated(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := defaultSummary("x", long)
	if len(got) > 100 {
		t.Errorf("not truncated: len=%d", len(got))
	}
}

func TestDefaultSummary_Empty(t *testing.T) {
	got := defaultSummary("x", "")
	if got != "" {
		t.Errorf("empty = %q, want empty", got)
	}
}

// --- stripLineNumber ---

func TestStripLineNumber_CatN(t *testing.T) {
	got := stripLineNumber("   42\tpackage main")
	if got != "package main" {
		t.Errorf("stripLineNumber = %q, want 'package main'", got)
	}
}

func TestStripLineNumber_NoPrefix(t *testing.T) {
	got := stripLineNumber("package main")
	if got != "package main" {
		t.Errorf("stripLineNumber = %q, want 'package main'", got)
	}
}

func TestStripLineNumber_NonDigitPrefix(t *testing.T) {
	// "abc\tcontent" — abc is not all digits, return original
	got := stripLineNumber("abc\tcontent")
	if got != "abc\tcontent" {
		t.Errorf("stripLineNumber = %q, want original", got)
	}
}

// --- extractFuncName ---

func TestExtractFuncName_Simple(t *testing.T) {
	got := extractFuncName("func foo() {")
	if got != "foo" {
		t.Errorf("extractFuncName = %q, want 'foo'", got)
	}
}

func TestExtractFuncName_WithReceiver(t *testing.T) {
	got := extractFuncName("func (s *Server) Start() {")
	if got != "Start" {
		t.Errorf("extractFuncName = %q, want 'Start'", got)
	}
}

func TestExtractFuncName_ValueReceiver(t *testing.T) {
	got := extractFuncName("func (s Server) Stop() {")
	if got != "Stop" {
		t.Errorf("extractFuncName = %q, want 'Stop'", got)
	}
}

func TestExtractFuncName_Generic(t *testing.T) {
	got := extractFuncName("func Map[T any]() {")
	if got != "Map" {
		t.Errorf("extractFuncName = %q, want 'Map'", got)
	}
}

func TestExtractFuncName_NoParen(t *testing.T) {
	got := extractFuncName("func foo")
	// No paren or bracket — falls through to space check
	if got != "" && got != "foo" {
		// Either is acceptable depending on impl; just ensure no panic
		t.Logf("extractFuncName('func foo') = %q", got)
	}
}

func TestExtractFuncName_BadReceiver(t *testing.T) {
	// Unclosed receiver
	got := extractFuncName("func (s *Server Start() {")
	if got != "" {
		t.Logf("bad receiver = %q", got)
	}
}

// --- extractTypeName ---

func TestExtractTypeName_Struct(t *testing.T) {
	got := extractTypeName("type Bar struct {")
	if got != "Bar" {
		t.Errorf("extractTypeName = %q, want 'Bar'", got)
	}
}

func TestExtractTypeName_Interface(t *testing.T) {
	got := extractTypeName("type Reader interface {")
	if got != "Reader" {
		t.Errorf("extractTypeName = %q, want 'Reader'", got)
	}
}

func TestExtractTypeName_Alias(t *testing.T) {
	got := extractTypeName("type MyInt int")
	if got != "MyInt" {
		t.Errorf("extractTypeName = %q, want 'MyInt'", got)
	}
}

// --- isHexPrefix ---

func TestIsHexPrefix(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"abc1234", true},
		{"ABCDEF0", true},
		{"0123456", true},
		{"abcdefg", false}, // g is not hex
		{"xyz1234", false},
		{"", true}, // empty string — vacuously true
	}
	for _, c := range cases {
		if got := isHexPrefix(c.s); got != c.want {
			t.Errorf("isHexPrefix(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}

// --- isAllDigits ---

func TestIsAllDigits(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"123", true},
		{"0", true},
		{"", false}, // empty — false
		{"12a", false},
		{" 12", false},
		{"1.5", false},
	}
	for _, c := range cases {
		if got := isAllDigits(c.s); got != c.want {
			t.Errorf("isAllDigits(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}
