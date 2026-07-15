package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func writeSkill(t *testing.T, root, dir, document string) string {
	t.Helper()
	path := filepath.Join(root, dir, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCatalogProjectPrecedenceAndAtomicReload(t *testing.T) {
	project := t.TempDir()
	global := t.TempDir()
	writeSkill(t, global, "verify", "---\nname: verify\ndescription: global workflow\n---\nRun global checks")
	projectPath := writeSkill(t, project, "verify", "---\nname: verify\ndescription: project workflow\n---\nRun project checks")
	writeSkill(t, global, "review", "---\nname: review\ndescription: review changes\n---\nReview the diff")

	catalog := NewCatalog([]Root{
		{Path: project, Source: "project"},
		{Path: global, Source: "global"},
	})
	skill, ok := catalog.Get("VERIFY")
	if !ok {
		t.Fatal("project skill not loaded")
	}
	if skill.Source != "project" || skill.Path != projectPath || skill.Body != "Run project checks" {
		t.Fatalf("precedence result = %#v", skill)
	}
	if got := catalog.List(); len(got) != 2 || got[0].Name != "review" || got[1].Name != "verify" {
		t.Fatalf("sorted catalog = %#v", got)
	}

	// Concurrent reads during reload exercise the snapshot boundary under race.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = catalog.Get("verify")
				_ = catalog.List()
			}
		}()
	}
	for i := 0; i < 20; i++ {
		catalog.Reload()
	}
	wg.Wait()
}

func TestDefaultRootsPersonalSkillsOverrideProject(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	configHome := filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	writeSkill(t, filepath.Join(project, ".claude", "skills"), "review", "---\nname: Project review\ndescription: project workflow\n---\nProject")
	personalPath := writeSkill(t, filepath.Join(home, ".claude", "skills"), "review", "---\nname: Personal review\ndescription: personal workflow\n---\nPersonal")

	catalog := NewCatalog(DefaultRoots(project))
	skill, ok := catalog.Get("review")
	if !ok {
		t.Fatalf("review skill missing: %q", strings.Join(catalog.Warnings(), "\n"))
	}
	if skill.Source != "claude-global" || skill.Path != personalPath || skill.Body != "Personal" {
		t.Fatalf("default precedence = %#v", skill)
	}
}

func TestCatalogRejectsSymlinkAndMalformedSkills(t *testing.T) {
	root := t.TempDir()
	escape := t.TempDir()
	target := writeSkill(t, escape, "outside", "---\nname: escaped\ndescription: must not load\n---\nUnsafe")
	if err := os.Symlink(filepath.Dir(target), filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writeSkill(t, root, "broken", "---\nname: broken\ndescription: [not, a, scalar]\n---\nMalformed description")
	writeSkill(t, root, "valid", "---\nname: valid\ndescription: safe workflow\n---\nDo the safe thing")

	catalog := NewCatalog([]Root{{Path: root, Source: "project"}})
	if _, ok := catalog.Get("escaped"); ok {
		t.Fatal("symlinked skill escaped its discovery root")
	}
	if _, ok := catalog.Get("broken"); ok {
		t.Fatal("malformed skill was loaded")
	}
	if _, ok := catalog.Get("valid"); !ok {
		t.Fatal("valid sibling skill was lost because another skill was malformed")
	}
	warnings := strings.Join(catalog.Warnings(), "\n")
	if !strings.Contains(warnings, "description must be a string scalar") {
		t.Fatalf("warnings = %q", warnings)
	}
}

func TestCatalogRejectsInvalidUTF8Body(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "invalid-utf8", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	document := append([]byte("---\nname: Invalid UTF-8\ndescription: invalid body\n---\n"), 0xff)
	if err := os.WriteFile(path, document, 0o600); err != nil {
		t.Fatal(err)
	}

	catalog := NewCatalog([]Root{{Path: root, Source: "project"}})
	if _, ok := catalog.Get("invalid-utf8"); ok {
		t.Fatal("skill with invalid UTF-8 body was loaded")
	}
	if warnings := strings.Join(catalog.Warnings(), "\n"); !strings.Contains(warnings, "valid UTF-8") {
		t.Fatalf("warnings = %q", warnings)
	}
}

func TestCatalogRejectsRootAndAncestorSymlinkEscapes(t *testing.T) {
	outside := t.TempDir()
	writeSkill(t, filepath.Join(outside, "skills"), "escaped", "---\nname: escaped\ndescription: must remain outside\n---\nUnsafe")

	tests := []struct {
		name     string
		rootPath func(boundary string) string
		linkPath func(boundary string) string
		linkDest string
	}{
		{
			name:     "root",
			rootPath: func(boundary string) string { return filepath.Join(boundary, "skills") },
			linkPath: func(boundary string) string { return filepath.Join(boundary, "skills") },
			linkDest: filepath.Join(outside, "skills"),
		},
		{
			name:     "ancestor",
			rootPath: func(boundary string) string { return filepath.Join(boundary, ".gokin", "skills") },
			linkPath: func(boundary string) string { return filepath.Join(boundary, ".gokin") },
			linkDest: outside,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			boundary := t.TempDir()
			if err := os.Symlink(tc.linkDest, tc.linkPath(boundary)); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			catalog := NewCatalog([]Root{{
				Path:     tc.rootPath(boundary),
				Source:   "project",
				Boundary: boundary,
			}})
			if _, ok := catalog.Get("escaped"); ok {
				t.Fatal("skill escaped its configured boundary through a symlink")
			}
			if len(catalog.Warnings()) == 0 {
				t.Fatal("boundary escape was rejected without a diagnostic")
			}
		})
	}
}

func TestCatalogRejectsUnknownFrontmatterFields(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "restricted", "---\nname: restricted\ndescription: must remain model-disabled\ndisable-model-invocatoin: true\n---\nSensitive workflow")

	catalog := NewCatalog([]Root{{Path: root, Source: "project"}})
	if _, ok := catalog.Get("restricted"); ok {
		t.Fatal("skill with a misspelled security field was loaded fail-open")
	}
	warnings := strings.Join(catalog.Warnings(), "\n")
	if !strings.Contains(warnings, "field disable-model-invocatoin not found") {
		t.Fatalf("warnings = %q", warnings)
	}
}

func TestCatalogAcceptsRecognizedMetadataFrontmatter(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "release", `---
name: release
description: release workflow
when_to_use: Use when preparing a release
argument-hint: "[target]"
arguments:
  - target
  - dry-run
---
Release the requested target`)

	catalog := NewCatalog([]Root{{Path: root, Source: "project"}})
	skill, ok := catalog.Get("release")
	if !ok {
		t.Fatalf("recognized metadata rejected: %q", strings.Join(catalog.Warnings(), "\n"))
	}
	if skill.DisplayName != "release" || skill.WhenToUse != "Use when preparing a release" ||
		skill.ArgumentHint != "[target]" || strings.Join(skill.Arguments, ",") != "target,dry-run" {
		t.Fatalf("metadata = %#v", skill)
	}
}

func TestCatalogDirectoryNameIsInvocationKey(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "alpha", "---\nname: beta\ndescription: alpha workflow\n---\nRun alpha")
	writeSkill(t, root, "beta", "---\nname: Actual Beta\ndescription: beta workflow\n---\nRun beta")

	catalog := NewCatalog([]Root{{Path: root, Source: "project"}})
	alpha, alphaOK := catalog.Get("alpha")
	beta, betaOK := catalog.Get("beta")
	if !alphaOK || !betaOK {
		t.Fatalf("directory keys missing: alpha=%v beta=%v warnings=%q", alphaOK, betaOK, strings.Join(catalog.Warnings(), "\n"))
	}
	if alpha.Name != "alpha" || alpha.DisplayName != "beta" {
		t.Fatalf("alpha semantics = %#v", alpha)
	}
	if beta.Name != "beta" || beta.DisplayName != "Actual Beta" {
		t.Fatalf("beta semantics = %#v", beta)
	}
	if _, ok := catalog.Get("Actual Beta"); ok {
		t.Fatal("display name unexpectedly became an invocation alias")
	}
}

func TestCatalogParsesScalarArgumentsAndReturnsImmutableCopies(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "---\nname: Deploy\ndescription: deploy workflow\narguments: target dry-run _internal\n---\nDeploy safely")

	catalog := NewCatalog([]Root{{Path: root, Source: "project"}})
	skill, ok := catalog.Get("deploy")
	if !ok {
		t.Fatalf("skill missing: %q", strings.Join(catalog.Warnings(), "\n"))
	}
	if got := strings.Join(skill.Arguments, ","); got != "target,dry-run,_internal" {
		t.Fatalf("arguments = %q", got)
	}

	// []string metadata must not expose the immutable catalog snapshot to
	// callers through a shallow copy.
	skill.Arguments[0] = "mutated"
	again, _ := catalog.Get("deploy")
	if again.Arguments[0] != "target" {
		t.Fatalf("Get exposed mutable catalog arguments: %#v", again.Arguments)
	}
	listed := catalog.List()
	listed[0].Arguments[0] = "mutated-list"
	again, _ = catalog.Get("deploy")
	if again.Arguments[0] != "target" {
		t.Fatalf("List exposed mutable catalog arguments: %#v", again.Arguments)
	}
}

func TestCatalogRejectsInvalidArgumentsShapesAndIdentifiers(t *testing.T) {
	tests := []struct {
		name      string
		arguments string
		want      string
	}{
		{name: "mapping", arguments: "{target: required}", want: "space-separated string or a list of strings"},
		{name: "non-string list item", arguments: "[target, 42]", want: "only string identifiers"},
		{name: "invalid identifier", arguments: "[target, bad/value]", want: "is not a valid identifier"},
		{name: "duplicate", arguments: "[first, first, third]", want: "is duplicated"},
		{name: "reserved", arguments: "[ARGUMENTS]", want: "is reserved"},
		{name: "too many", arguments: strings.Join(func() []string {
			values := make([]string, maxSkillArgumentCount+1)
			for i := range values {
				values[i] = fmt.Sprintf("arg%d", i)
			}
			return values
		}(), " "), want: "exceed 32 unique identifiers"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			document := fmt.Sprintf("---\nname: Controlled\ndescription: controlled workflow\narguments: %s\n---\nRun controlled workflow", tc.arguments)
			writeSkill(t, root, "controlled", document)
			catalog := NewCatalog([]Root{{Path: root, Source: "project"}})
			if _, ok := catalog.Get("controlled"); ok {
				t.Fatal("invalid arguments metadata was loaded")
			}
			if warnings := strings.Join(catalog.Warnings(), "\n"); !strings.Contains(warnings, tc.want) {
				t.Fatalf("warnings = %q, want %q", warnings, tc.want)
			}
		})
	}
}

func TestCatalogRejectsAliasingDirectoryNames(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "release", "---\nname: Release\ndescription: canonical workflow\n---\nCanonical")
	writeSkill(t, root, " release", "---\nname: Spaced\ndescription: must not shadow\n---\nSpaced")
	writeSkill(t, root, "Upper", "---\nname: Upper\ndescription: must not normalize\n---\nUpper")

	catalog := NewCatalog([]Root{{Path: root, Source: "project"}})
	release, ok := catalog.Get("release")
	if !ok || release.Body != "Canonical" {
		t.Fatalf("canonical directory was shadowed: %#v warnings=%q", release, strings.Join(catalog.Warnings(), "\n"))
	}
	if _, ok := catalog.Get("upper"); ok {
		t.Fatal("uppercase directory was silently normalized into an invocation key")
	}
	if warnings := strings.Join(catalog.Warnings(), "\n"); strings.Count(warnings, "invalid skill directory name") < 2 {
		t.Fatalf("aliasing directories lacked diagnostics: %q", warnings)
	}
}

func TestCatalogValidatesBoundedScalarMetadata(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value string
		want  string
	}{
		{name: "when mapping", field: "when_to_use", value: "{task: release}", want: "when_to_use must be a string scalar"},
		{name: "hint list", field: "argument-hint", value: "[target]", want: "argument-hint must be a string scalar"},
		{name: "when too long", field: "when_to_use", value: fmt.Sprintf("%q", strings.Repeat("w", maxWhenToUseBytes+1)), want: "when_to_use exceeds"},
		{name: "hint too long", field: "argument-hint", value: fmt.Sprintf("%q", strings.Repeat("h", maxArgumentHintBytes+1)), want: "argument-hint exceeds"},
		{name: "display too long", field: "name", value: fmt.Sprintf("%q", strings.Repeat("n", maxDisplayNameBytes+1)), want: "name exceeds"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			document := fmt.Sprintf("---\ndescription: bounded metadata\n%s: %s\n---\nRun bounded workflow", tc.field, tc.value)
			writeSkill(t, root, "bounded", document)
			catalog := NewCatalog([]Root{{Path: root, Source: "project"}})
			if _, ok := catalog.Get("bounded"); ok {
				t.Fatal("invalid bounded metadata was loaded")
			}
			if warnings := strings.Join(catalog.Warnings(), "\n"); !strings.Contains(warnings, tc.want) {
				t.Fatalf("warnings = %q, want %q", warnings, tc.want)
			}
		})
	}
}

func TestCatalogDescriptionFallsBackToFirstMarkdownParagraph(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "release", `---
name: Release workflow
---
# Release

Build **and verify** artifacts.
Continue with a clean working tree.

This later paragraph is not catalog metadata.`)
	longParagraph := strings.Repeat("длинное описание ", 80)
	writeSkill(t, root, "long", "---\nname: Long workflow\n---\n# Long\n\n"+longParagraph)

	catalog := NewCatalog([]Root{{Path: root, Source: "project"}})
	release, ok := catalog.Get("release")
	if !ok {
		t.Fatalf("fallback skill missing: %q", strings.Join(catalog.Warnings(), "\n"))
	}
	want := "Build **and verify** artifacts. Continue with a clean working tree."
	if release.Description != want {
		t.Fatalf("fallback description = %q, want %q", release.Description, want)
	}
	long, ok := catalog.Get("long")
	if !ok {
		t.Fatalf("long fallback skill missing: %q", strings.Join(catalog.Warnings(), "\n"))
	}
	if len(long.Description) > maxDescriptionBytes || !strings.HasSuffix(long.Description, "…") {
		t.Fatalf("long fallback was not safely bounded: bytes=%d value=%q", len(long.Description), long.Description)
	}
}

func TestCatalogRejectsUnsupportedExecutionFrontmatterExplicitly(t *testing.T) {
	fields := map[string]string{
		"allowed-tools":    "[Read]",
		"disallowed-tools": "[Bash]",
		"model":            "sonnet",
		"effort":           "high",
		"context":          "fork",
		"agent":            "Explore",
		"hooks":            "{}",
		"paths":            "['src/**']",
		"shell":            "bash",
	}
	for field, value := range fields {
		t.Run(field, func(t *testing.T) {
			root := t.TempDir()
			document := fmt.Sprintf("---\nname: controlled\ndescription: controlled workflow\n%s: %s\n---\nRun controlled workflow", field, value)
			writeSkill(t, root, "controlled", document)

			catalog := NewCatalog([]Root{{Path: root, Source: "project"}})
			if _, ok := catalog.Get("controlled"); ok {
				t.Fatalf("unsupported execution field %q was silently ignored", field)
			}
			warnings := strings.Join(catalog.Warnings(), "\n")
			want := fmt.Sprintf("field %q is recognized but unsupported", field)
			if !strings.Contains(warnings, want) {
				t.Fatalf("warnings = %q, want %q", warnings, want)
			}
		})
	}
}

func TestCatalogRejectsUnsupportedDynamicContextInjection(t *testing.T) {
	tests := map[string]string{
		"inline": "State:\n- Diff: !`git diff`",
		"fenced": "State:\n```!\ngit status\n```",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeSkill(t, root, "dynamic", "---\nname: Dynamic\ndescription: dynamic context\n---\n"+body)
			catalog := NewCatalog([]Root{{Path: root, Source: "project"}})
			if _, ok := catalog.Get("dynamic"); ok {
				t.Fatal("unsupported dynamic context skill was loaded")
			}
			if warnings := strings.Join(catalog.Warnings(), "\n"); !strings.Contains(warnings, "dynamic context injection") {
				t.Fatalf("warnings = %q", warnings)
			}
		})
	}

	root := t.TempDir()
	writeSkill(t, root, "literal", "---\nname: Literal\ndescription: literal syntax\n---\nKEY=!`not-injected`\nEscaped: \\!`also-literal`")
	if _, ok := NewCatalog([]Root{{Path: root, Source: "project"}}).Get("literal"); !ok {
		t.Fatal("literal non-injection syntax was rejected")
	}
}

func TestCatalogMalformedProjectSkillBlocksGlobalFallback(t *testing.T) {
	project := t.TempDir()
	global := t.TempDir()
	// The invocation key comes from the directory. A malformed project skill
	// reserves that key regardless of unrelated display metadata.
	writeSkill(t, project, "release", "---\nname: Different Display Name\ndescription: project release\ndisable-model-invocatoin: true\n---\nProject workflow")
	writeSkill(t, global, "release", "---\nname: release\ndescription: global release\n---\nGlobal workflow")

	catalog := NewCatalog([]Root{
		{Path: project, Source: "project"},
		{Path: global, Source: "global"},
	})
	if skill, ok := catalog.Get("release"); ok {
		t.Fatalf("malformed project skill fell through to lower-priority workflow: %#v", skill)
	}
}

func TestCatalogMissingProjectSkillFileBlocksGlobalWithDiagnostic(t *testing.T) {
	project := t.TempDir()
	global := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "release"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, global, "release", "---\nname: Global Release\ndescription: global release\n---\nGlobal workflow")

	catalog := NewCatalog([]Root{
		{Path: project, Source: "project"},
		{Path: global, Source: "global"},
	})
	if skill, ok := catalog.Get("release"); ok {
		t.Fatalf("missing project SKILL.md fell through to global workflow: %#v", skill)
	}
	if warnings := strings.Join(catalog.Warnings(), "\n"); !strings.Contains(warnings, "SKILL.md is missing") {
		t.Fatalf("missing fail-closed skill had no diagnostic: %q", warnings)
	}
}

func TestCatalogEnforcesSkillCountLimit(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < MaxSkillCount+8; i++ {
		name := fmt.Sprintf("skill-%03d", i)
		writeSkill(t, root, name, fmt.Sprintf("---\nname: %s\ndescription: bounded skill\n---\nRun bounded workflow", name))
	}

	catalog := NewCatalog([]Root{{Path: root, Source: "project"}})
	if got := len(catalog.List()); got != MaxSkillCount {
		t.Fatalf("loaded skill count = %d, want %d", got, MaxSkillCount)
	}
	if warnings := strings.Join(catalog.Warnings(), "\n"); !strings.Contains(warnings, "skill count limit") {
		t.Fatalf("warnings = %q", warnings)
	}
}

func TestCatalogEnforcesAggregateByteBudget(t *testing.T) {
	root := t.TempDir()
	// Stay below the per-file and count limits while exceeding the aggregate
	// catalog budget, so this test exercises only MaxCatalogBytes.
	body := strings.Repeat("x", 27*1024)
	for i := 0; i < 80; i++ {
		name := fmt.Sprintf("large-%03d", i)
		writeSkill(t, root, name, fmt.Sprintf("---\nname: %s\ndescription: aggregate budget skill\n---\n%s", name, body))
	}

	catalog := NewCatalog([]Root{{Path: root, Source: "project"}})
	total := 0
	for _, skill := range catalog.List() {
		total += catalogSkillBytes(skill)
	}
	if total > MaxCatalogBytes {
		t.Fatalf("loaded catalog bytes = %d, limit = %d", total, MaxCatalogBytes)
	}
	if got := len(catalog.List()); got >= 80 {
		t.Fatalf("aggregate budget loaded all %d oversized skills", got)
	}
	if warnings := strings.Join(catalog.Warnings(), "\n"); !strings.Contains(warnings, "catalog byte limit") {
		t.Fatalf("warnings = %q", warnings)
	}
}
