package permission

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseClaudeCompatibleScopedToolRules(t *testing.T) {
	rules, err := ParseTemporaryToolGrantList(
		"Read(/src/**) Edit(/src/**) WebFetch(domain:example.com) " +
			"Agent(Explore) mcp__github__* Bash(ls:*)",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"read(/src/**)",
		"edit(/src/**)",
		"web_fetch(domain:example.com)",
		"task(Explore)",
		"mcp__github__*",
		"bash(ls:*)",
	}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules = %#v, want %#v", rules, want)
	}
	allBash, err := ParseTemporaryToolDenyList("Bash(*)")
	if err != nil || !reflect.DeepEqual(allBash, []string{"bash"}) {
		t.Fatalf("Bash(*) canonicalization = %#v, %v", allBash, err)
	}
	for _, malformed := range []string{
		"Read(src/[)",
		"WebFetch(domain:example.com:8443)",
		"WebFetch(domain:*.example.com)",
	} {
		if _, err := ParseTemporaryToolGrantList(malformed); err == nil {
			t.Fatalf("malformed scoped rule %q was accepted", malformed)
		}
	}
}

func TestScopedPathRulesUseAgentWorkDirAndSymlinkTargets(t *testing.T) {
	workDir := t.TempDir()
	outside := t.TempDir()
	insideFile := filepath.Join(workDir, "src", "main.go")
	outsideFile := filepath.Join(outside, "secret.go")
	if err := os.MkdirAll(filepath.Dir(insideFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(insideFile, []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workDir, "src", "linked-secret.go")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Fatal(err)
	}

	if !temporaryToolGrantMatches(
		"read(/src/**)", "read",
		map[string]any{"file_path": insideFile}, workDir,
	) {
		t.Fatal("project-root Read rule did not match an in-scope file")
	}
	if temporaryToolGrantMatches(
		"read(/src/**)", "read",
		map[string]any{"file_path": outsideFile}, workDir,
	) {
		t.Fatal("project-root Read rule matched an outside file")
	}
	if temporaryToolGrantMatches(
		"read(/src/**)", "read",
		map[string]any{"file_path": link}, workDir,
	) {
		t.Fatal("allow rule ignored an out-of-scope symlink target")
	}

	outsidePattern := "//" + filepath.ToSlash(stringsTrimRoot(outside)) + "/**"
	if !temporaryToolDenyMatchesAny(
		[]string{"read(" + outsidePattern + ")"},
		"read",
		map[string]any{"file_path": link},
		workDir,
	) {
		t.Fatal("deny rule did not block a symlink resolving into denied scope")
	}
}

func TestScopedDenyBlocksBroadSearchAndParentMutation(t *testing.T) {
	workDir := t.TempDir()
	denies := []string{"read(/src/**)"}
	if !temporaryToolDenyMatchesAny(
		denies, "grep",
		map[string]any{"pattern": "secret", "path": workDir}, workDir,
	) {
		t.Fatal("broad Grep bypassed a nested Read deny")
	}
	if !temporaryToolDenyMatchesAny(
		denies, "glob",
		map[string]any{"pattern": "src/**/*.go"}, workDir,
	) {
		t.Fatal("scoped Glob bypassed a nested Read deny")
	}
	if temporaryToolDenyMatchesAny(
		denies, "glob",
		map[string]any{"pattern": "docs/**/*.md"}, workDir,
	) {
		t.Fatal("disjoint Glob was overblocked by Read deny")
	}

	if !temporaryToolDenyMatchesAny(
		[]string{"edit(/src/protected.go)"}, "delete",
		map[string]any{"path": filepath.Join(workDir, "src")}, workDir,
	) {
		t.Fatal("deleting a parent directory bypassed a nested Edit deny")
	}
}

func TestEditGroupRulesCoverAllDeclaredMutationPaths(t *testing.T) {
	workDir := t.TempDir()
	insideA := filepath.Join(workDir, "src", "a.go")
	insideB := filepath.Join(workDir, "src", "b.go")
	outside := filepath.Join(t.TempDir(), "escaped.go")

	if !temporaryToolGrantMatches(
		"edit(/src/**)", "write",
		map[string]any{"file_path": insideA}, workDir,
	) {
		t.Fatal("Edit rule did not cover Write")
	}
	if !temporaryToolGrantMatches(
		"edit(/src/**)", "copy",
		map[string]any{"source": insideA, "destination": insideB}, workDir,
	) {
		t.Fatal("Edit rule did not cover an in-scope Copy")
	}
	if temporaryToolGrantMatches(
		"edit(/src/**)", "copy",
		map[string]any{"source": insideA, "destination": outside}, workDir,
	) {
		t.Fatal("Edit allow matched Copy with an outside destination")
	}
	if !temporaryToolDenyMatchesAny(
		[]string{"edit(/src/**)"}, "move",
		map[string]any{"source": insideA, "destination": outside}, workDir,
	) {
		t.Fatal("Edit deny did not match one affected in-scope Move path")
	}
	if temporaryToolGrantMatches(
		"edit(/src/**)", "refactor",
		map[string]any{"operation": "rename", "target": "Thing"}, workDir,
	) {
		t.Fatal("scoped Edit allow granted an indeterminate refactor")
	}
	if !temporaryToolDenyMatchesAny(
		[]string{"edit(/src/**)"}, "refactor",
		map[string]any{"operation": "rename", "target": "Thing"}, workDir,
	) {
		t.Fatal("scoped Edit deny did not conservatively block an indeterminate refactor")
	}
}

func TestScopedWebFetchAgentBashAndMCPRules(t *testing.T) {
	if !temporaryToolGrantMatches(
		"web_fetch(domain:example.com)", "web_fetch",
		map[string]any{"url": "https://EXAMPLE.com:8443/docs"}, "",
	) {
		t.Fatal("WebFetch domain rule did not ignore host case/port")
	}
	if temporaryToolGrantMatches(
		"web_fetch(domain:example.com)", "web_fetch",
		map[string]any{"url": "https://sub.example.com/docs"}, "",
	) {
		t.Fatal("WebFetch domain rule unexpectedly matched a subdomain")
	}
	if !temporaryToolGrantMatches(
		"task(Explore)", "task",
		map[string]any{"subagent_type": "explore"}, "",
	) {
		t.Fatal("Agent rule did not match subagent_type case-insensitively")
	}
	if temporaryToolGrantMatches(
		"task(Explore)", "task",
		map[string]any{"resume": "agent-1"}, "",
	) {
		t.Fatal("Agent allow granted an untyped resume")
	}
	if !temporaryToolDenyMatchesAny(
		[]string{"task(Explore)"}, "task",
		map[string]any{"resume": "agent-1"}, "",
	) {
		t.Fatal("Agent deny did not fail closed for an untyped resume")
	}
	for _, command := range []string{"ls", "ls -la"} {
		if !temporaryToolGrantMatches(
			"bash(ls:*)", "bash", map[string]any{"command": command}, "",
		) {
			t.Fatalf("Bash trailing wildcard did not match %q", command)
		}
	}
	if !temporaryToolGrantMatches(
		"mcp__github__*", "mcp__github__create_pr", nil, "",
	) {
		t.Fatal("MCP wildcard grant did not match the server tool")
	}
}

func TestDontAskScopedRunRulesAreBoundToExecutionWorkDir(t *testing.T) {
	workDir := t.TempDir()
	manager := NewManager(DefaultRules(), true)
	manager.SetDontAsk(true)
	manager.SetRunToolRules([]string{"edit(/generated/**)"}, nil)
	ctx := ContextWithWorkDir(context.Background(), workDir)

	inside, err := manager.Check(
		ctx, "write",
		map[string]any{"file_path": filepath.Join(workDir, "generated", "a.go")},
	)
	if err != nil || inside == nil || !inside.Allowed {
		t.Fatalf("in-scope write = %+v, %v", inside, err)
	}
	outside, err := manager.Check(
		ctx, "write",
		map[string]any{"file_path": filepath.Join(t.TempDir(), "escaped.go")},
	)
	if err != nil || outside == nil || outside.Allowed {
		t.Fatalf("outside write = %+v, %v", outside, err)
	}
}

func TestRelativePathSessionApprovalDoesNotCrossAgentWorkDirs(t *testing.T) {
	manager := NewManager(DefaultRules(), true)
	prompts := 0
	manager.SetPromptHandler(func(context.Context, *Request) (Decision, error) {
		prompts++
		return DecisionAllowSession, nil
	})
	args := map[string]any{"file_path": "generated.go"}
	ctxA := ContextWithWorkDir(context.Background(), t.TempDir())
	ctxB := ContextWithWorkDir(context.Background(), t.TempDir())

	for _, ctx := range []context.Context{ctxA, ctxA, ctxB} {
		response, err := manager.Check(ctx, "write", args)
		if err != nil || response == nil || !response.Allowed {
			t.Fatalf("relative write response = %+v, %v", response, err)
		}
	}
	if prompts != 2 {
		t.Fatalf("permission prompts = %d, want one per distinct workdir", prompts)
	}
}

func TestBareReadAndEditDeniesExpandToToolGroups(t *testing.T) {
	tests := []struct {
		rule string
		tool string
		want bool
	}{
		{rule: "read", tool: "grep", want: true},
		{rule: "read", tool: "glob", want: true},
		{rule: "read", tool: "write", want: false},
		{rule: "edit", tool: "write", want: true},
		{rule: "edit", tool: "delete", want: true},
		{rule: "edit", tool: "read", want: false},
	}
	for _, test := range tests {
		if got := ToolDenyRuleMatchesName(test.rule, test.tool); got != test.want {
			t.Errorf("ToolDenyRuleMatchesName(%q, %q) = %v, want %v",
				test.rule, test.tool, got, test.want)
		}
	}
}

func TestPermissionSlashPathNormalizesWindowsDrive(t *testing.T) {
	if got := permissionSlashPath(`C:\Users\Alice\src\main.go`); got != "/c/Users/Alice/src/main.go" {
		t.Fatalf("normalized Windows path = %q", got)
	}
}

func stringsTrimRoot(path string) string {
	return strings.TrimPrefix(filepath.ToSlash(path), "/")
}
