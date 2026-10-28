package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"google.golang.org/genai"
)

func writeDeltaGoModule(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":             "module example.com/m\n\ngo 1.23\n",
		"root.go":            "package m\n",
		"a/a.go":             "package a\n",
		"a/a_test.go":        "package a\n",
		"b/c/c.go":           "package c\n",
		"windowsonly/w.go":   "//go:build windows\n\npackage windowsonly\n",
		"docs/notes.md":      "notes\n",
		"tests/only_test.go": "package tests\n",
	}
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func goDeltaArgs(t *testing.T, root string, changed ...string) []string {
	t.Helper()
	commands := buildDeltaCheckCommands(root, changed, 8)
	if len(commands) == 0 {
		return nil
	}
	if len(commands) != 1 || commands[0].Command != "go" {
		t.Fatalf("commands = %+v, want one go command", commands)
	}
	return commands[0].Args
}

func TestGoDeltaCheckBuildsOnlyTouchedPackages(t *testing.T) {
	root := writeDeltaGoModule(t)
	cases := []struct {
		name    string
		changed []string
		want    []string
	}{
		{"one package", []string{"a/a.go"}, []string{"build", "./a"}},
		{"nested and root, sorted and deduped", []string{"b/c/c.go", "root.go", "b/c/c.go"}, []string{"build", ".", "./b/c"}},
		{"go.mod widens to the module", []string{"a/a.go", "go.mod"}, []string{"build", "./..."}},
		{"non-Go file keeps the full build", []string{"docs/notes.md"}, []string{"build", "./..."}},
		{"vanished directory keeps the full build", []string{"gone/x.go"}, []string{"build", "./..."}},
		{"test file next to a package change adds nothing", []string{"a/a_test.go", "b/c/c.go"}, []string{"build", "./b/c"}},
		{"build-tag-excluded package is never listed", []string{"windowsonly/w.go", "a/a.go"}, []string{"build", "./a"}},
	}
	for _, tc := range cases {
		if got := goDeltaArgs(t, root, tc.changed...); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: args = %v, want %v", tc.name, got, tc.want)
		}
	}
	// go build never compiled tests, so a test-only change has nothing to check.
	if got := goDeltaArgs(t, root, "tests/only_test.go"); got != nil {
		t.Errorf("test-only change ran %v, want no go command", got)
	}
}

func TestGoDeltaCheckBenignBuildConstraintRefusal(t *testing.T) {
	refusal := "package example.com/m/windowsonly: build constraints exclude all Go files in /tmp/x/windowsonly"
	if !deltaOutputIsBenign(refusal, goDeltaBenignOutputs) {
		t.Fatal("build-constraint refusal must count as nothing to check")
	}
	mixed := refusal + "\n./a/a.go:3:1: syntax error: unexpected }"
	if deltaOutputIsBenign(mixed, goDeltaBenignOutputs) {
		t.Fatal("a real compile error next to a refusal must still fail")
	}
	if deltaOutputIsBenign("", goDeltaBenignOutputs) {
		t.Fatal("empty output is not a benign refusal")
	}
}

func TestGoDeltaCheckRealBuildScope(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain unavailable")
	}
	root := writeDeltaGoModule(t)
	// A broken package that was NOT touched must not fail the scoped check, and
	// an excluded package listed alongside a broken one must not mask it.
	if err := os.WriteFile(filepath.Join(root, "b", "c", "c.go"), []byte("package c\nfunc broken( {\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := NewExecutor(NewRegistry(), nil, 0)
	e.SetWorkDir(root)
	e.SetDeltaCheckConfig(true, 0, false, 0)
	for _, rel := range []string{"a/a.go", "windowsonly/w.go"} {
		e.addPendingDeltaTargets(t.Context(), &genai.FunctionCall{Name: "write", Args: map[string]any{"file_path": filepath.Join(root, rel)}})
	}
	result := e.runDeltaCheck(t.Context())
	if !result.Ran || !result.Passed {
		t.Fatalf("scoped check should build ./a and treat ./windowsonly as nothing to compile: %+v", result)
	}

	e.addPendingDeltaTargets(t.Context(), &genai.FunctionCall{Name: "write", Args: map[string]any{"file_path": filepath.Join(root, "b", "c", "c.go")}})
	result = e.runDeltaCheck(t.Context())
	if result.Passed || result.Failed != 1 {
		t.Fatalf("touching the broken package must fail the check even next to an excluded package: %+v", result)
	}
}
