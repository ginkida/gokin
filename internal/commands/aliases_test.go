package commands

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseResolvesDefaultAlias(t *testing.T) {
	h := NewHandler()
	name, _, ok := h.Parse("/p")
	if !ok {
		t.Fatal("/p should resolve (default alias for plan)")
	}
	if name != "plan" {
		t.Errorf("name = %q, want plan", name)
	}
}

func TestParseUnknownAliasRejected(t *testing.T) {
	h := NewHandler()
	_, _, ok := h.Parse("/nonexistent-xyz")
	if ok {
		t.Error("unknown command should not resolve via alias")
	}
}

func TestLoadAliasesFromFile_Missing(t *testing.T) {
	h := NewHandler()
	err := h.LoadAliasesFromFile(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Errorf("missing file should not error: %v", err)
	}
}

func TestLoadAliasesFromFile_OverridesDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aliases.yaml")
	// Override default "p: plan" with "p: status"
	content := "p: status\nx: undo\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler()
	if err := h.LoadAliasesFromFile(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := h.ResolveAlias("p"); got != "status" {
		t.Errorf("p → %q, want status (user override)", got)
	}
	if got := h.ResolveAlias("x"); got != "undo" {
		t.Errorf("x → %q, want undo (new alias)", got)
	}
	if got := h.ResolveAlias("c"); got != "commit" {
		t.Errorf("c → %q, want commit (default preserved)", got)
	}
}

// TestLoadAliasesFromFile_StripsLeadingSlashFromTarget verifies that a user
// writing "p: /plan" gets the same result as "p: plan". Commands are stored
// without the leading slash, so targets with it would silently never resolve.
func TestLoadAliasesFromFile_StripsLeadingSlashFromTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aliases.yaml")
	if err := os.WriteFile(path, []byte("p: /plan\n"), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler()
	if err := h.LoadAliasesFromFile(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	// ResolveAlias should return "plan" (without slash).
	if got := h.ResolveAlias("p"); got != "plan" {
		t.Errorf("p → %q, want plan (leading / must be stripped)", got)
	}
	// Parse should also resolve correctly.
	if name, _, ok := h.Parse("/p"); !ok || name != "plan" {
		t.Errorf("Parse(/p) = %q, %v; want plan, true", name, ok)
	}
}

func TestLoadAliasesFromFile_RejectsMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aliases.yaml")
	// Empty values and whitespace-in-alias should be ignored.
	content := "good: plan\n\"\": status\nbad alias: commit\n"
	os.WriteFile(path, []byte(content), 0644)

	h := NewHandler()
	if err := h.LoadAliasesFromFile(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := h.ResolveAlias("good"); got != "plan" {
		t.Errorf("good → %q, want plan", got)
	}
	if got := h.ResolveAlias("bad alias"); got == "commit" {
		t.Error("alias with whitespace should be rejected")
	}
}
