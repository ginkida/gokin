package tools

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/cache"
	"gokin/internal/permission"
	"gokin/internal/security"
	"gokin/internal/testkit"
)

func TestSecretPathsTraversal(t *testing.T) {
	home := testkit.ResolvedTempDir(t)
	t.Setenv("HOME", home)
	for _, rel := range []string{".ssh/leaked.txt", ".claude/.credentials.json", "project/.env.local", "project/.env.example", "project/source.txt"} {
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("credential_fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []Tool{NewGrepTool(home), NewGlobTool(home), NewTreeTool(home), NewListDirTool(filepath.Join(home, "project"))} {
		args := map[string]any{"show_hidden": true}
		switch tool.Name() {
		case "grep":
			args["pattern"] = "credential_fixture"
		case "glob":
			args["pattern"] = "**/*"
		}
		result, err := tool.Execute(context.Background(), args)
		if err != nil || !result.Success {
			t.Fatalf("%s: %+v %v", tool.Name(), result, err)
		}
		if tool.Name() != "grep" && !strings.Contains(result.Content, ".env.local") {
			t.Errorf("%s hid credential-like names: %s", tool.Name(), result.Content)
		}
		if strings.Contains(result.Content, "leaked.txt") {
			t.Errorf("%s entered secret tree: %s", tool.Name(), result.Content)
		}
	}
}

func TestCredentialGrepFooterAndExplicitFile(t *testing.T) {
	work := testkit.ResolvedTempDir(t)
	for _, name := range []string{".env.local", "private.key", "source.txt", ".env.example"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte("credential_fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tool := NewGrepTool(work)
	tool.SetCache(cache.NewSearchCache(100, 0))
	for _, args := range []map[string]any{
		{"pattern": "credential_fixture"}, {"pattern": "credential_fixture"},
		{"pattern": "absent"}, {"pattern": "absent", "count_only": true},
		{"pattern": "credential_fixture", "count_only": true}, {"pattern": "absent", "invert": true},
	} {
		r, err := tool.Execute(context.Background(), args)
		if err != nil || !r.Success {
			t.Fatalf("grep: %+v %v", r, err)
		}
		footer := "2 credential-like file(s) skipped"
		if strings.Count(r.Content, footer) != 1 || strings.Contains(r.Content, "private.key:") || strings.Contains(r.Content, ".env.local:") {
			t.Errorf("unsafe/incomplete search: %s", r.Content)
		}
	}
	r, err := tool.Execute(context.Background(), map[string]any{"pattern": "credential_fixture", "path": ".env.local"})
	if err != nil || !strings.Contains(r.Content, ".env.local:1:") {
		t.Fatalf("explicit file search: %+v %v", r, err)
	}
	impact, err := NewCheckImpactTool(work).Execute(t.Context(), map[string]any{"symbol": "credential_fixture"})
	if err != nil || !impact.Success || strings.Contains(impact.Content, "private.key:") || !strings.Contains(impact.Content, "2 credential-like file(s) skipped") {
		t.Errorf("impact: %+v %v", impact, err)
	}
	content, _, err := renderUntrackedFile(filepath.Join(work, ".env.local"))
	if err != nil || strings.TrimSpace(content) != "(content withheld: credential-like file)" {
		t.Fatalf("preview: %q %v", content, err)
	}
}

func TestSecretPathsSSHAndGrantDrift(t *testing.T) {
	home := testkit.ResolvedTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "custom-claude"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	// Include every specified location, not a copy of the old SSH subset.
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(configDir, "gokin"), filepath.Join(home, "xdg/gokin"), filepath.Join(home, "custom-claude/.credentials.json")}
	for _, rel := range []string{".ssh", ".aws", ".gnupg", ".kube", ".config/gcloud", ".docker/config.json", ".netrc", ".claude/.credentials.json", ".claude.json", ".config/gokin"} {
		paths = append(paths, filepath.Join(home, rel))
	}
	for _, path := range append(paths, security.SecretPaths()...) {
		if err := security.IsGrantableDir(path); err == nil {
			t.Errorf("grant admits %s", path)
		}
		if err := validateSSHLocalPath(path); err == nil {
			t.Errorf("SSH admits %s", path)
		}
	}
}

func TestSecretPathRegistryPropagationAndClone(t *testing.T) {
	home := testkit.ResolvedTempDir(t)
	t.Setenv("HOME", home)
	reg := DefaultRegistry(home)
	regTool, _ := reg.Get("refactor")
	regTool.(*RefactorTool).SetWorkDir(home)
	secret := filepath.Join(home, ".ssh", "fixture")
	if err := os.Mkdir(filepath.Dir(secret), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	SetAllowedDirsOnRegistry(reg, nil, []string{secret})
	count := 0
	for _, tool := range reg.List() {
		_, hasValidator := reflect.TypeOf(tool).Elem().FieldByName("pathValidator")
		scoped, ok := tool.(secretPathScoped)
		if hasValidator && !ok {
			t.Errorf("%s missed secret policy interface", tool.Name())
		}
		if !ok {
			continue
		}
		count++
		validator := scoped.secretPathValidator()
		if validator == nil {
			t.Fatalf("%s has nil validator", tool.Name())
		}
		if _, err := validator.Validate(secret); err != nil {
			t.Errorf("%s missed exception: %v", tool.Name(), err)
		}
		clone := CloneToolForWorkDir(tool, home).(secretPathScoped)
		if _, err := clone.secretPathValidator().Validate(secret); err != nil {
			t.Errorf("%s clone lost exception: %v", tool.Name(), err)
		}
		validator.SetAllowedDirs([]string{home})
		if _, err := validator.Validate(secret); err != nil {
			t.Errorf("%s directory update lost exception: %v", tool.Name(), err)
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	readTool, _ := reg.Get("read")
	validator := readTool.(secretPathScoped).secretPathValidator()
	go func() {
		defer wg.Done()
		for range 50 {
			SetAllowedDirsOnRegistry(reg, nil, []string{secret})
		}
	}()
	go func() {
		defer wg.Done()
		for range 50 {
			_, _ = validator.Validate(secret)
		}
	}()
	wg.Wait()
	if readTool.(secretPathScoped).secretPathValidator() != validator {
		t.Fatal("propagation swapped the live validator")
	}
	if count < 20 {
		t.Fatalf("only %d scoped tools inspected", count)
	}
	SetAllowedDirsOnRegistry(reg, nil, nil)
	for _, tool := range reg.List() {
		if scoped, ok := tool.(secretPathScoped); ok {
			if _, err := scoped.secretPathValidator().Validate(secret); err == nil {
				t.Errorf("%s ignored revocation", tool.Name())
			}
		}
	}
}

func TestSecretPathCacheRevocation(t *testing.T) {
	home := testkit.ResolvedTempDir(t)
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".ssh", "fixture.txt")
	if err := os.Mkdir(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("credential_fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	read := NewReadTool(home)
	reg.MustRegister(read)
	SetAllowedDirsOnRegistry(reg, nil, []string{path})
	executor := NewExecutor(reg, nil, time.Second)
	executor.SetWorkDir(home)
	executor.SetPermissions(permission.NewManager(nil, false))
	executor.SetUnrestrictedMode(true)
	executor.SetToolCache(NewToolResultCache(DefaultCacheConfig()))
	args := map[string]any{"file_path": path}
	r, err := executor.InvokeTool(t.Context(), "read", args)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Success || !strings.Contains(r.Content, "credential_fixture") {
		t.Fatalf("approved fixture read: %+v", r)
	}
	SetAllowedDirsOnRegistry(reg, nil, nil)
	r, err = executor.InvokeTool(t.Context(), "read", args)
	if err != nil {
		t.Fatal(err)
	}
	if r.Success {
		t.Fatalf("cache bypassed credential exception revocation: %+v", r)
	}
}

// Personal Claude skills live under ~/.claude/skills and their reference files
// are read by the model; only the credential files in that tree are withheld.
func TestSecretPaths_ClaudeSkillsStayReadable(t *testing.T) {
	home := testkit.ResolvedTempDir(t)
	t.Setenv("HOME", home)
	skill := filepath.Join(home, ".claude", "skills", "demo", "reference.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("ref"), 0o600); err != nil {
		t.Fatal(err)
	}
	if security.IsSecretPath(skill) {
		t.Fatal("skill reference file must not be a credential location")
	}
	if !security.IsSecretPath(filepath.Join(home, ".claude", ".credentials.json")) {
		t.Fatal("Claude credentials file must be withheld")
	}
}

// A root-level pattern reads one directory; the walker must not descend into
// subtrees that cannot match (the old glob engine never did), and a symlinked
// regular file is still listed.
func TestWalkScopedFiles_NonRecursivePatternAndSymlinkedFile(t *testing.T) {
	home := testkit.ResolvedTempDir(t)
	t.Setenv("HOME", home)
	root := filepath.Join(home, "project")
	for _, rel := range []string{"main.go", "deep/inner.go", "docs/README.md"} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "docs", "README.md"), filepath.Join(root, "README.md")); err != nil {
		t.Fatal(err)
	}
	validator := security.NewPathValidator([]string{root}, true)
	var visitedDirs, matches []string
	err := walkScopedFiles(root, "*", validator, func(path string, _ fs.DirEntry) error {
		matches = append(matches, filepath.Base(path))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = visitedDirs
	got := strings.Join(matches, ",")
	if !strings.Contains(got, "main.go") || !strings.Contains(got, "README.md") || strings.Contains(got, "inner.go") {
		t.Fatalf("root-level '*' matches = %v", matches)
	}
}
