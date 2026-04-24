package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newInitApp(t *testing.T) *fakeAppForAuth {
	t.Helper()
	// Init writes GOKIN.md into workDir — use a throwaway tempdir per test.
	base := newAuthApp(nil)
	base.fakeAppForMCP.workDir = t.TempDir()
	return base
}

// Fresh workdir: /init must create GOKIN.md with a project-matching template
// and report success.
func TestInit_CreatesGokinMdOnFreshProject(t *testing.T) {
	app := newInitApp(t)
	// Simulate a Go project — detectTemplate branches on go.mod presence.
	if err := os.WriteFile(filepath.Join(app.workDir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatalf("seed go.mod: %v", err)
	}
	cmd := &InitCommand{}

	out, err := cmd.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "Created GOKIN.md") {
		t.Errorf("output should confirm creation: %q", out)
	}

	data, err := os.ReadFile(filepath.Join(app.workDir, "GOKIN.md"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), "go build") {
		t.Errorf("go project template should contain go build section, got:\n%s", data)
	}
}

// Regression: /init was vulnerable to a TOCTOU race where two concurrent
// invocations both saw "file absent" in os.Stat and both proceeded to
// WriteFile, the second silently clobbering the first. Fix swapped to
// O_CREATE|O_EXCL so second caller gets "already exists".
func TestInit_SecondCallReturnsAlreadyExists(t *testing.T) {
	app := newInitApp(t)
	cmd := &InitCommand{}

	if _, err := cmd.Execute(context.Background(), nil, app); err != nil {
		t.Fatalf("first exec: %v", err)
	}

	// Capture the first-write contents so we can prove the second didn't clobber.
	original, err := os.ReadFile(filepath.Join(app.workDir, "GOKIN.md"))
	if err != nil {
		t.Fatalf("read after first: %v", err)
	}

	out, err := cmd.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("second exec: %v", err)
	}
	if !strings.Contains(out, "already exists") {
		t.Errorf("second call should return already-exists notice: %q", out)
	}

	after, err := os.ReadFile(filepath.Join(app.workDir, "GOKIN.md"))
	if err != nil {
		t.Fatalf("read after second: %v", err)
	}
	if string(after) != string(original) {
		t.Errorf("second /init clobbered first write — TOCTOU regression")
	}
}

// Edge: pre-existing GOKIN.md with user content must NOT be overwritten.
// Same invariant as above but tested with content we'd notice if corrupted.
func TestInit_PreservesUserContent(t *testing.T) {
	app := newInitApp(t)
	gokinPath := filepath.Join(app.workDir, "GOKIN.md")
	userContent := "# My Project\n\nI wrote this by hand. Do not overwrite.\n"
	if err := os.WriteFile(gokinPath, []byte(userContent), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cmd := &InitCommand{}
	out, err := cmd.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(out, "already exists") {
		t.Errorf("should detect existing file: %q", out)
	}

	after, err := os.ReadFile(gokinPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(after) != userContent {
		t.Errorf("user content lost:\nbefore: %q\nafter:  %q", userContent, after)
	}
}
