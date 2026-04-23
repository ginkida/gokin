package app

import (
	"path/filepath"
	"testing"
)

func TestModuleHasTouchedFile_EmptyTouchedReturnsTrue(t *testing.T) {
	// Safety fallback: no touched-paths info = run everything.
	if !moduleHasTouchedFile("/repo/mod-a", "/repo", nil) {
		t.Error("empty touched list should return true (full-sweep fallback)")
	}
}

func TestModuleHasTouchedFile_AbsolutePathMatch(t *testing.T) {
	if !moduleHasTouchedFile("/repo/mod-a", "/repo", []string{"/repo/mod-a/foo.go"}) {
		t.Error("abs path inside module should match")
	}
	if moduleHasTouchedFile("/repo/mod-a", "/repo", []string{"/repo/mod-b/foo.go"}) {
		t.Error("abs path in other module should not match")
	}
}

func TestModuleHasTouchedFile_RelativePathResolved(t *testing.T) {
	// Relative paths get resolved against workDir.
	if !moduleHasTouchedFile("/repo/mod-a", "/repo", []string{"mod-a/foo.go"}) {
		t.Error("relative path resolvable to module should match")
	}
	if moduleHasTouchedFile("/repo/mod-a", "/repo", []string{"mod-b/foo.go"}) {
		t.Error("relative path to other module should not match")
	}
}

func TestModuleHasTouchedFile_DeepPath(t *testing.T) {
	// Files deep within the module hierarchy still count as "inside".
	if !moduleHasTouchedFile("/repo/mod-a", "/repo", []string{"/repo/mod-a/pkg/sub/handler.go"}) {
		t.Error("deep file should count as touched")
	}
}

func TestModuleHasTouchedFile_SiblingPrefixDoesntMatch(t *testing.T) {
	// Prefix overlap trap: "mod-a" and "mod-a2" share a prefix but are
	// distinct modules. pathWithinDir must use proper path semantics.
	if moduleHasTouchedFile("/repo/mod-a", "/repo", []string{"/repo/mod-a2/foo.go"}) {
		t.Error("sibling with shared prefix must not match")
	}
}

func TestDoneGateGoVetTargets_FromTouchedGoFiles(t *testing.T) {
	profile := doneGateProfile{
		GoModules: []string{"/repo"},
		WorkDir:   "/repo",
		TouchedPaths: []string{
			"/repo/internal/handler/user.go",
			"/repo/internal/handler/user_test.go", // same dir
			"/repo/pkg/validator/rules.go",
		},
	}
	targets := doneGateGoVetTargets(profile, 5)
	if len(targets) != 2 {
		t.Fatalf("expected 2 unique dirs (handler + validator), got %d: %+v", len(targets), targets)
	}
	// Each should be a directory, framework=go.
	for _, tgt := range targets {
		if tgt.Framework != "go" {
			t.Errorf("expected framework=go, got %q", tgt.Framework)
		}
	}
}

func TestDoneGateGoVetTargets_GoModTriggersWholeModule(t *testing.T) {
	// go.mod edits should vet the whole module, not its directory.
	profile := doneGateProfile{
		GoModules:    []string{"/repo"},
		WorkDir:      "/repo",
		TouchedPaths: []string{"/repo/go.mod"},
	}
	targets := doneGateGoVetTargets(profile, 5)
	if len(targets) != 1 || filepath.Clean(targets[0].Path) != "/repo" {
		t.Errorf("go.mod edit should target the module root (/repo), got %+v", targets)
	}
}

func TestDoneGateGoVetTargets_CapEnforced(t *testing.T) {
	profile := doneGateProfile{
		GoModules: []string{"/repo"},
		WorkDir:   "/repo",
		TouchedPaths: []string{
			"/repo/a/foo.go", "/repo/b/foo.go", "/repo/c/foo.go",
			"/repo/d/foo.go", "/repo/e/foo.go",
		},
	}
	targets := doneGateGoVetTargets(profile, 2)
	if len(targets) != 2 {
		t.Errorf("cap=2 not enforced; got %d", len(targets))
	}
}

func TestDoneGateGoVetTargets_NoTouchedGoYieldsEmpty(t *testing.T) {
	// If touched files don't include .go/go.mod/go.sum, we don't
	// produce vet targets (caller falls back to module-wide vet).
	profile := doneGateProfile{
		GoModules:    []string{"/repo"},
		WorkDir:      "/repo",
		TouchedPaths: []string{"/repo/README.md", "/repo/config.yaml"},
	}
	if got := doneGateGoVetTargets(profile, 5); got != nil {
		t.Errorf("non-Go touches should yield no targets, got %+v", got)
	}
}

func TestDoneGateGoVetTargets_NoModulesYieldsEmpty(t *testing.T) {
	profile := doneGateProfile{
		GoModules:    nil,
		WorkDir:      "/repo",
		TouchedPaths: []string{"/repo/foo.go"},
	}
	if got := doneGateGoVetTargets(profile, 5); got != nil {
		t.Errorf("no Go modules should yield no targets, got %+v", got)
	}
}
