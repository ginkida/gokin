package repl

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
)

func TestCredentialFileIndex(t *testing.T) {
	home := testkit.ResolvedTempDir(t)
	t.Setenv("HOME", home)
	for _, rel := range []string{".ssh/leaked.txt", ".claude/.credentials.json", ".claude/skills/demo/ref.md", ".env.local", ".env.example", "source.txt"} {
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("credential_fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// Exercise both enumeration backends and the explicit ignored-scope walker.
	for _, mode := range []string{"matcher", "explicit", "git"} {
		t.Run(mode, func(t *testing.T) {
			m := &Manager{opts: Options{WorkDir: home, GitPath: gitPath}, runtimeDir: testkit.ResolvedTempDir(t)}
			var result fileIndexResult
			switch mode {
			case "matcher":
				result, err = m.writeMatcherFileIndex(home)
			case "explicit":
				result, err = m.writeExplicitFileIndex(home, "test")
			case "git":
				cmd := exec.Command(gitPath, "init", home)
				if out, e := cmd.CombinedOutput(); e != nil {
					t.Fatalf("git init: %s %v", out, e)
				}
				result, err = m.writeGitFileIndex(t.Context(), ".")
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(result.Path)
			if err != nil {
				t.Fatal(err)
			}
			index := string(data)
			if strings.Contains(index, "leaked.txt") || strings.Contains(index, ".credentials.json") || !strings.Contains(index, "ref.md") || strings.Contains(index, ".env.local") || !strings.Contains(index, "source.txt") || !strings.Contains(index, ".env.example") {
				t.Fatalf("unsafe index: %q", index)
			}
		})
	}
	m := &Manager{opts: Options{WorkDir: home}, runtimeDir: testkit.ResolvedTempDir(t)}
	if _, err := m.buildFileIndex(t.Context(), map[string]any{"path": ".ssh"}); err == nil {
		t.Error("explicit secret index root accepted")
	}
	m.SetAllowSecretPaths([]string{filepath.Join(home, ".ssh")})
	allowed, err := m.buildFileIndex(t.Context(), map[string]any{"path": ".ssh"})
	if err != nil || allowed.Entries != 1 {
		t.Fatalf("explicit index exception: %+v %v", allowed, err)
	}
	m.SetAllowSecretPaths(nil)
	if _, err := m.buildFileIndex(t.Context(), map[string]any{"path": ".ssh"}); err == nil {
		t.Error("revoked index exception remained active")
	}

}

func TestCredentialSearchCode(t *testing.T) {
	work := testkit.ResolvedTempDir(t)
	for _, name := range []string{".env.local", ".env.example", "source.txt"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte("credential_fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	manager := testManager(t, work, nil)
	for _, code := range []string{
		`context.search_code("credential_fixture")`,
		`context.search_code("credential_fixture", path=".env.local")`,
		`context.count_code("credential_fixture")`,
		`context.list_files()`,
	} {
		result, err := manager.Execute(t.Context(), code)
		if err != nil || result.Error != nil {
			t.Fatalf("%s: %+v %v", code, result, err)
		}
		if strings.Contains(result.Value, ".env.local") {
			t.Errorf("%s exposed credential file: %s", code, result.Value)
		}
		if strings.Contains(code, "search_code") && !strings.Contains(code, "path=") && !strings.Contains(result.Value, "source.txt") {
			t.Errorf("source missing: %s", result.Value)
		}
	}
}
