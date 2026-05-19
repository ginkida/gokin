package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunTestsPathCanBeGoTestFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/run-tests-file\n\ngo 1.25\n"), 0644); err != nil {
		t.Fatal(err)
	}
	pkgDir := filepath.Join(root, "pkg")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatal(err)
	}
	testFile := filepath.Join(pkgDir, "foo_test.go")
	if err := os.WriteFile(testFile, []byte(`package pkg

import "testing"

func TestFoo(t *testing.T) {}
`), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := NewRunTestsTool(root).Execute(context.Background(), map[string]any{
		"path":      filepath.Join("pkg", "foo_test.go"),
		"framework": "go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("run_tests returned failure: %s\n%s", result.Error, result.Content)
	}
	if !strings.Contains(result.Content, "PASS") {
		t.Fatalf("expected PASS output, got: %s", result.Content)
	}
}
