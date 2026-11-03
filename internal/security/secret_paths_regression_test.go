package security_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/security"
	"gokin/internal/testkit"
)

func TestSecretPathsHomeGrantAndValidation(t *testing.T) {
	home := testkit.ResolvedTempDir(t)
	t.Setenv("HOME", home)
	for _, dir := range []string{home, filepath.Dir(home)} {
		if err := security.IsGrantableDir(dir); err == nil || !strings.Contains(err.Error(), "refusing to grant your home directory") {
			t.Errorf("grant %s: %v", dir, err)
		}
	}
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(filepath.Join(project, ".docker"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := security.IsGrantableDir(project); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".ssh/key", ".claude/.credentials.json", ".claude.json", ".docker/config.json", ".config/gcloud/token", ".config/gokin/config.yaml", ".netrc"} {
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		for _, dirs := range [][]string{{home}, {filepath.Dir(path)}, nil} {
			if _, err := security.NewPathValidator(dirs, true).Validate(path); err == nil {
				t.Errorf("validated secret %s with %v", rel, dirs)
			}
			if ok, err := security.IsPathWithinAny(path, dirs); ok || err == nil {
				t.Errorf("gate admitted %s with %v: %v", rel, dirs, err)
			}
		}
	}
}

func TestSecretPathsAliasesAndExceptions(t *testing.T) {
	home := testkit.ResolvedTempDir(t)
	t.Setenv("HOME", home)
	target := testkit.ResolvedTempDir(t)
	if err := os.Symlink(target, filepath.Join(home, ".ssh")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(target, "key")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	v := security.NewPathValidator([]string{home, target}, true)
	if !security.IsSecretPath(path) {
		t.Fatal("symlink target is not secret")
	}
	if _, err := v.Validate(path); err == nil {
		t.Fatal("symlink target admitted")
	}
	v.SetAllowSecretPaths([]string{path})
	if _, err := v.Validate(path); err != nil {
		t.Fatalf("explicit exception: %v", err)
	}
	if ok, err := security.IsPathWithinAny(path, []string{home, target}, []string{path}); err != nil || !ok {
		t.Fatalf("gate disagrees with exception: %v %v", ok, err)
	}
	if _, err := v.Validate(filepath.Join(target, "other")); err == nil {
		t.Fatal("file exception exposed sibling")
	}
	v.SetAllowedDirs([]string{home})
	if _, err := v.Validate(path); err == nil {
		t.Fatal("exception also granted workspace access")
	}
	v.SetAllowedDirs([]string{home, target})
	v.SetAllowSecretPaths(nil)
	if _, err := v.Validate(path); err == nil {
		t.Fatal("revoked exception retained")
	}
	if security.IsSecretPath(filepath.Join(home, "project", ".ssh", "key")) {
		t.Fatal("basename alone blocked a project directory")
	}
}
