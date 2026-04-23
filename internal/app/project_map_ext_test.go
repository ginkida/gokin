package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
)

// Extension tests for the project map — package discovery and
// CODEOWNERS detection. Covers the user's 5 stacks: Go cmd/,
// JS workspaces, Rust workspace members, Laravel/PHP apps.

func TestDetectProjectMapPackages_GoCmdSubdirs(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "gokin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "helper"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	app := &App{workDir: dir}
	packages := app.detectProjectMapPackages()

	expected := map[string]bool{"cmd/gokin": true, "cmd/helper": true}
	for _, pkg := range packages {
		delete(expected, pkg)
	}
	if len(expected) > 0 {
		t.Errorf("missing expected cmd subdirs %v; got %v", expected, packages)
	}
}

func TestDetectProjectMapPackages_JSWorkspaces(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	pkgJSON := `{"name":"root","workspaces":["packages/*","apps/web"]}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := &App{workDir: dir}
	packages := app.detectProjectMapPackages()

	found := map[string]bool{}
	for _, p := range packages {
		found[p] = true
	}
	for _, want := range []string{"packages/*", "apps/web"} {
		if !found[want] {
			t.Errorf("expected JS workspace %q in result, got %v", want, packages)
		}
	}
}

func TestDetectProjectMapPackages_JSObjectWorkspaces(t *testing.T) {
	// `workspaces: { packages: [...] }` form used by older yarn setups.
	dir := testkit.ResolvedTempDir(t)
	pkgJSON := `{"name":"root","workspaces":{"packages":["libs/*"]}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := &App{workDir: dir}
	packages := app.detectProjectMapPackages()

	found := false
	for _, p := range packages {
		if p == "libs/*" {
			found = true
		}
	}
	if !found {
		t.Errorf("object-form workspaces not parsed; got %v", packages)
	}
}

func TestDetectProjectMapPackages_RustMembers(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	toml := `[workspace]
members = [
    "crate-a",
    "crate-b",
]
`
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := &App{workDir: dir}
	packages := app.detectProjectMapPackages()

	found := map[string]bool{}
	for _, p := range packages {
		found[p] = true
	}
	for _, want := range []string{"crate-a", "crate-b"} {
		if !found[want] {
			t.Errorf("missing Rust member %q; got %v", want, packages)
		}
	}
}

func TestDetectProjectMapPackages_LaravelApps(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	for _, app := range []string{"apps/api", "apps/admin", "packages/shared"} {
		full := filepath.Join(dir, app)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(filepath.Join(full, "composer.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
			t.Fatalf("write composer: %v", err)
		}
	}
	// Directory without composer.json — should NOT be included.
	if err := os.MkdirAll(filepath.Join(dir, "apps/docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}

	appInstance := &App{workDir: dir}
	packages := appInstance.detectProjectMapPackages()

	found := map[string]bool{}
	for _, p := range packages {
		found[p] = true
	}
	for _, want := range []string{"apps/api", "apps/admin", "packages/shared"} {
		if !found[want] {
			t.Errorf("missing Laravel/PHP package %q; got %v", want, packages)
		}
	}
	if found["apps/docs"] {
		t.Error("apps/docs has no composer.json — should not be listed")
	}
}

func TestDetectProjectMapPackages_EmptyWorkDir(t *testing.T) {
	app := &App{workDir: ""}
	if got := app.detectProjectMapPackages(); got != nil {
		t.Errorf("empty workDir should yield nil, got %v", got)
	}
}

func TestDetectProjectMapOwners_Present(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	owners := `# comment
/internal/app @ginkida
*.md @docs-team @ginkida
/cmd @ginkida
`
	if err := os.WriteFile(filepath.Join(dir, "CODEOWNERS"), []byte(owners), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := &App{workDir: dir}
	got := app.detectProjectMapOwners()
	if got == "" {
		t.Fatal("expected non-empty CODEOWNERS summary")
	}
	for _, needle := range []string{"CODEOWNERS", "/internal/app", "3 pattern"} {
		if !strings.Contains(got, needle) {
			t.Errorf("summary missing %q: %s", needle, got)
		}
	}
}

func TestDetectProjectMapOwners_NestedLocation(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	if err := os.MkdirAll(filepath.Join(dir, ".github"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".github", "CODEOWNERS"), []byte("* @team\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := &App{workDir: dir}
	got := app.detectProjectMapOwners()
	if got == "" {
		t.Fatal("should find .github/CODEOWNERS")
	}
	if !strings.Contains(got, ".github/CODEOWNERS") {
		t.Errorf("should surface nested path; got %q", got)
	}
}

func TestDetectProjectMapOwners_Absent(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	app := &App{workDir: dir}
	if got := app.detectProjectMapOwners(); got != "" {
		t.Errorf("no CODEOWNERS should yield empty; got %q", got)
	}
}

func TestDetectProjectMapOwners_OnlyComments(t *testing.T) {
	// File exists but has only comments/blank lines → no ownership rules
	// to show → empty string (skip the section in the map).
	dir := testkit.ResolvedTempDir(t)
	body := "# all comments\n# nothing real here\n"
	if err := os.WriteFile(filepath.Join(dir, "CODEOWNERS"), []byte(body), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := &App{workDir: dir}
	if got := app.detectProjectMapOwners(); got != "" {
		t.Errorf("comment-only CODEOWNERS should yield empty; got %q", got)
	}
}

func TestExtractQuotedStrings_HandlesMultipleAndEmpty(t *testing.T) {
	cases := map[string][]string{
		`"foo", "bar"`:    {"foo", "bar"},
		`no quotes here`:  nil,
		`"only-one"`:      {"only-one"},
		`"a" "b" "c"`:     {"a", "b", "c"},
		`"unterminated`:   nil, // missing closing quote → treated as no strings
	}
	for input, want := range cases {
		got := extractQuotedStrings(input)
		if len(got) != len(want) {
			t.Errorf("%q → len %d, want %d", input, len(got), len(want))
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%q → %v, want %v", input, got, want)
				break
			}
		}
	}
}

func TestLooksLikeCrateMember_Patterns(t *testing.T) {
	cases := map[string]bool{
		`"crate-a",`:       true,
		`"solo"`:           true,
		`members = [`:      false,
		`]`:                false,
		`# comment`:        false,
		`foo = "bar"`:      false, // starts with foo, not quote
	}
	for input, want := range cases {
		if got := looksLikeCrateMember(input); got != want {
			t.Errorf("looksLikeCrateMember(%q) = %v, want %v", input, got, want)
		}
	}
}
