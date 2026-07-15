package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/skills"
)

func writeToolSkill(t *testing.T, root, name, document string) {
	t.Helper()
	path := filepath.Join(root, name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSkillToolListsLoadsAndExpandsArguments(t *testing.T) {
	root := t.TempDir()
	writeToolSkill(t, root, "deploy", "---\nname: deploy\ndescription: Deploy a release safely\n---\nTarget=$0\nAll=$ARGUMENTS\nSecond=$1\nIndexed=$ARGUMENTS[0]")
	catalog := skills.NewCatalog([]skills.Root{{Path: root, Source: "project"}})
	tool := NewSkillToolWithCatalog(catalog)

	if description := tool.Declaration().Description; !strings.Contains(description, "deploy: Deploy a release safely") {
		t.Fatalf("declaration lacks compact catalog: %q", description)
	}
	listed, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil || !listed.Success || !strings.Contains(listed.Content, "deploy") {
		t.Fatalf("list result = %#v err=%v", listed, err)
	}
	filtered, err := tool.Execute(context.Background(), map[string]any{"query": "release"})
	if err != nil || !filtered.Success || !strings.Contains(filtered.Content, "deploy") {
		t.Fatalf("filtered list result = %#v err=%v", filtered, err)
	}
	missing, err := tool.Execute(context.Background(), map[string]any{"query": "database"})
	if err != nil || !missing.Success || !strings.Contains(missing.Content, "No model-invocable skills match") {
		t.Fatalf("empty filtered list result = %#v err=%v", missing, err)
	}

	loaded, err := tool.Execute(context.Background(), map[string]any{
		"name":      "DEPLOY",
		"arguments": "staging --dry-run",
	})
	if err != nil || !loaded.Success {
		t.Fatalf("load result = %#v err=%v", loaded, err)
	}
	for _, want := range []string{"Target=staging", "All=staging --dry-run", "Second=--dry-run", "Indexed=staging", "does not override"} {
		if !strings.Contains(loaded.Content, want) {
			t.Fatalf("loaded skill missing %q:\n%s", want, loaded.Content)
		}
	}
}

func TestSkillToolExpandsQuotedNamedAndLiteralArgumentsOnce(t *testing.T) {
	root := t.TempDir()
	writeToolSkill(t, root, "issue", `---
name: Issue helper
description: Work on an issue
arguments: [issue, branch]
---
First=$ARGUMENTS[0]
Second=$1
Named=$issue/$branch
Raw=$ARGUMENTS`)
	tool := NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{Path: root, Source: "project"}}))

	result, err := tool.Execute(context.Background(), map[string]any{
		"name":      "issue",
		"arguments": `"$1 literal" feature`,
	})
	if err != nil || !result.Success {
		t.Fatalf("result = %#v err=%v", result, err)
	}
	for _, want := range []string{
		"First=$1 literal",
		"Second=feature",
		"Named=$1 literal/feature",
		`Raw="$1 literal" feature`,
	} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("loaded skill missing %q:\n%s", want, result.Content)
		}
	}
}

func TestSkillArgumentRendererPreservesBoundaryWhitespaceAndShellEscapes(t *testing.T) {
	skill := skills.Skill{Name: "render", Body: "$0"}
	got, err := expandSkillArguments(skill, `" padded "`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != " padded " {
		t.Fatalf("boundary placeholder = %q, want exact padded value", got)
	}

	values, err := splitSkillArguments(`"C:\Program Files"`)
	if err != nil || len(values) != 1 || values[0] != `C:\Program Files` {
		t.Fatalf("Windows quoted argument = %#v err=%v", values, err)
	}
	values, err = splitSkillArguments("foo\\\nbar")
	if err != nil || len(values) != 1 || values[0] != "foobar" {
		t.Fatalf("line continuation = %#v err=%v", values, err)
	}
}

func TestSkillArgumentRendererLeavesOverflowIndexesLiteral(t *testing.T) {
	index := strings.Repeat("9", 64)
	for _, body := range []string{"$" + index, "$ARGUMENTS[" + index + "]"} {
		got, err := expandSkillArguments(skills.Skill{Name: "overflow", Body: body}, "value", nil, "")
		if err != nil {
			t.Fatalf("body %q: %v", body, err)
		}
		if !strings.HasPrefix(got, body) || !strings.Contains(got, "ARGUMENTS: value") {
			t.Fatalf("overflow placeholder %q rendered as %q", body, got)
		}
	}
}

func TestSkillToolEscapesPlaceholdersAndAppendsUnusedArguments(t *testing.T) {
	root := t.TempDir()
	writeToolSkill(t, root, "escape", `---
name: escape
description: Check escaping
---
One=\$0
Two=\\$0
Three=$0`)
	writeToolSkill(t, root, "fallback", "---\nname: fallback\ndescription: Preserve otherwise unused arguments\n---\nRun the workflow")
	tool := NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{Path: root, Source: "project"}}))

	escaped, err := tool.Execute(context.Background(), map[string]any{"name": "escape", "arguments": "value"})
	if err != nil || !escaped.Success {
		t.Fatalf("escape result = %#v err=%v", escaped, err)
	}
	for _, want := range []string{"One=$0", `Two=\\value`, "Three=value"} {
		if !strings.Contains(escaped.Content, want) {
			t.Fatalf("escaped skill missing %q:\n%s", want, escaped.Content)
		}
	}

	fallback, err := tool.Execute(context.Background(), map[string]any{"name": "fallback", "arguments": "staging"})
	if err != nil || !fallback.Success || !strings.Contains(fallback.Content, "ARGUMENTS: staging") {
		t.Fatalf("fallback result = %#v err=%v", fallback, err)
	}
}

func TestSkillToolExpandsDeterministicPathVariables(t *testing.T) {
	project := t.TempDir()
	root := filepath.Join(project, ".gokin", "skills")
	writeToolSkill(t, root, "paths", "---\nname: paths\ndescription: Locate bundled files\n---\nSkill=${CLAUDE_SKILL_DIR}\nProject=${CLAUDE_PROJECT_DIR}")
	tool := NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "project", Boundary: project}}), project)

	result, err := tool.Execute(context.Background(), map[string]any{"name": "paths"})
	if err != nil || !result.Success {
		t.Fatalf("result = %#v err=%v", result, err)
	}
	for _, want := range []string{filepath.Join(root, "paths"), project} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("path expansion missing %q:\n%s", want, result.Content)
		}
	}
}

func TestSkillToolRejectsUnsupportedRuntimeVariablesExplicitly(t *testing.T) {
	for _, variable := range []string{"${CLAUDE_SESSION_ID}", "${CLAUDE_EFFORT}"} {
		t.Run(variable, func(t *testing.T) {
			root := t.TempDir()
			writeToolSkill(t, root, "runtime", "---\nname: runtime\ndescription: Runtime variable\n---\nValue="+variable)
			tool := NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{Path: root, Source: "project"}}))
			result, err := tool.Execute(context.Background(), map[string]any{"name": "runtime"})
			if err != nil {
				t.Fatal(err)
			}
			if result.Success || !strings.Contains(result.Error, "recognized but unsupported") {
				t.Fatalf("runtime variable result = %#v", result)
			}
		})
	}

	escaped, err := expandSkillArguments(skills.Skill{Body: `\${CLAUDE_SESSION_ID}`}, "", nil, "")
	if err != nil || escaped != "${CLAUDE_SESSION_ID}" {
		t.Fatalf("escaped runtime variable = %q err=%v", escaped, err)
	}
	if _, err := expandSkillArguments(skills.Skill{Body: `${CLAUDE_PROJECT_DIR}`}, "", nil, ""); err == nil || !strings.Contains(err.Error(), "workDir-bound") {
		t.Fatalf("unbound project variable error = %v", err)
	}
}

func TestSkillToolFailsClosedForModelDisabledSkill(t *testing.T) {
	root := t.TempDir()
	writeToolSkill(t, root, "release", "---\nname: release\ndescription: User-only release\ndisable-model-invocation: true\n---\nPublish production")
	tool := NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{Path: root, Source: "project"}}))

	if strings.Contains(tool.Description(), "release: User-only") {
		t.Fatal("model-disabled skill leaked into the model catalog")
	}
	result, err := tool.Execute(context.Background(), map[string]any{"name": "release"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "disable-model-invocation") {
		t.Fatalf("disabled skill result = %#v", result)
	}
	userResult, err := tool.ExecuteForUser(map[string]any{"name": "release"})
	if err != nil || !userResult.Success || !strings.Contains(userResult.Content, "Publish production") {
		t.Fatalf("explicit user invocation result = %#v err=%v", userResult, err)
	}
}

func TestSkillToolRespectsUserInvocableFalse(t *testing.T) {
	root := t.TempDir()
	writeToolSkill(t, root, "internal-review", "---\nname: internal-review\ndescription: Model-only review\nuser-invocable: false\n---\nReview internal state")
	tool := NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{Path: root, Source: "project"}}))

	modelResult, err := tool.Execute(context.Background(), map[string]any{"name": "internal-review"})
	if err != nil || !modelResult.Success {
		t.Fatalf("model invocation result = %#v err=%v", modelResult, err)
	}
	userResult, err := tool.ExecuteForUser(map[string]any{"name": "internal-review"})
	if err != nil || userResult.Success || !strings.Contains(userResult.Error, "user-invocable: false") {
		t.Fatalf("user invocation result = %#v err=%v", userResult, err)
	}
}

func TestSkillToolPicksUpEditsWithoutRestart(t *testing.T) {
	root := t.TempDir()
	writeToolSkill(t, root, "verify", "---\nname: verify\ndescription: Verify code\n---\nRun old command")
	tool := NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{Path: root, Source: "project"}}))
	writeToolSkill(t, root, "verify", "---\nname: verify\ndescription: Verify code\n---\nRun new command")

	result, err := tool.Execute(context.Background(), map[string]any{"name": "verify"})
	if err != nil || !result.Success || !strings.Contains(result.Content, "Run new command") {
		t.Fatalf("reloaded skill result = %#v err=%v", result, err)
	}
}

func TestLazyRegistrySkillDeclarationTracksCatalogChanges(t *testing.T) {
	workDir := t.TempDir()
	registry := DefaultLazyRegistry(workDir)

	findSkillDescription := func() string {
		t.Helper()
		for _, declaration := range registry.Declarations() {
			if declaration != nil && declaration.Name == "skill" {
				return declaration.Description
			}
		}
		t.Fatal("lazy registry has no skill declaration")
		return ""
	}

	if got := findSkillDescription(); strings.Contains(got, "release-dynamic") {
		t.Fatalf("skill appeared before its file existed: %q", got)
	}

	skillRoot := filepath.Join(workDir, ".gokin", "skills")
	writeToolSkill(t, skillRoot, "release-dynamic", "---\nname: release-dynamic\ndescription: Release from the live catalog\n---\nRun release checks")

	if got := findSkillDescription(); !strings.Contains(got, "release-dynamic: Release from the live catalog") {
		t.Fatalf("lazy declaration did not discover added skill: %q", got)
	}
	if registry.IsInstantiated("skill") {
		t.Fatal("refreshing the skill declaration instantiated the lazy tool entry")
	}

	if err := os.RemoveAll(filepath.Join(skillRoot, "release-dynamic")); err != nil {
		t.Fatal(err)
	}
	if got := findSkillDescription(); strings.Contains(got, "release-dynamic") {
		t.Fatalf("lazy declaration retained removed skill: %q", got)
	}
}

func TestEagerRegistrySkillDeclarationTracksCatalogChanges(t *testing.T) {
	workDir := t.TempDir()
	registry := DefaultRegistry(workDir)

	findSkillDescription := func() string {
		t.Helper()
		for _, declaration := range registry.Declarations() {
			if declaration != nil && declaration.Name == "skill" {
				return declaration.Description
			}
		}
		t.Fatal("eager registry has no skill declaration")
		return ""
	}

	if got := findSkillDescription(); strings.Contains(got, "eager-live") {
		t.Fatalf("skill appeared before its file existed: %q", got)
	}
	writeToolSkill(t, filepath.Join(workDir, ".gokin", "skills"), "eager-live", "---\nname: Eager live\ndescription: Discovered by the eager registry\n---\nRun eager checks")
	if got := findSkillDescription(); !strings.Contains(got, "eager-live") {
		t.Fatalf("eager declaration did not discover added skill: %q", got)
	}
}

func TestSkillToolBoundsArgumentExpansionBeforeAllocation(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("$ARGUMENTS ", 128)
	writeToolSkill(t, root, "expand", "---\nname: expand\ndescription: Exercise bounded expansion\n---\n"+body)
	tool := NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{Path: root, Source: "project"}}))

	result, err := tool.Execute(context.Background(), map[string]any{
		"name":      "expand",
		"arguments": strings.Repeat("x", 1024),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "expanded workflow exceeds") {
		t.Fatalf("expansion result = %#v", result)
	}
}

func TestSkillToolBoundsArgumentsForDirectUserInvocation(t *testing.T) {
	root := t.TempDir()
	writeToolSkill(t, root, "echo", "---\nname: echo\ndescription: Echo arguments\n---\n$ARGUMENTS")
	tool := NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{Path: root, Source: "project"}}))

	result, err := tool.ExecuteForUser(map[string]any{
		"name":      "echo",
		"arguments": strings.Repeat("x", maxSkillArgumentsBytes+1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "arguments exceed") {
		t.Fatalf("oversized arguments result = %#v", result)
	}

	result, err = tool.ExecuteForUserArguments("echo", []string{strings.Repeat("x", maxSkillArgumentsBytes+1)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "arguments exceed") {
		t.Fatalf("oversized tokenized arguments result = %#v", result)
	}
}

func TestSkillToolRejectsRenderedWrapperBeyondDeliveryLimit(t *testing.T) {
	root := t.TempDir()
	writeToolSkill(t, root, "bounded", "---\nname: bounded\ndescription: Bound the complete payload\n---\nRun safely")
	tool := NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{
		Path:   root,
		Source: strings.Repeat("s", skills.MaxRenderedSkillBytes),
	}}))

	result, err := tool.Execute(context.Background(), map[string]any{"name": "bounded"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "delivery limit") {
		t.Fatalf("oversized rendered result = %#v", result)
	}
}

func TestSkillToolInvocationLedgerDeduplicatesAndTracksChangedRender(t *testing.T) {
	root := t.TempDir()
	writeToolSkill(t, root, "verify", "---\nname: Verify display\ndescription: Verify code\n---\nRun focused tests")
	tool := NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{Path: root, Source: "project"}}))

	first, err := tool.Execute(context.Background(), map[string]any{"name": "VERIFY"})
	if err != nil || !first.Success || !strings.Contains(first.Content, "Run focused tests") {
		t.Fatalf("first load = %#v err=%v", first, err)
	}
	firstData, ok := first.Data.(map[string]any)
	if !ok || firstData["name"] != "verify" || firstData["changed"] != true {
		t.Fatalf("first data = %#v", first.Data)
	}
	snapshot := tool.InvocationLedger().SnapshotNewestFirst()
	if len(snapshot) != 1 || snapshot[0].Name != "verify" || snapshot[0].Rendered != first.Content || snapshot[0].Sequence != 1 {
		t.Fatalf("first ledger snapshot = %#v", snapshot)
	}

	duplicate, err := tool.Execute(context.Background(), map[string]any{"name": "verify"})
	if err != nil || !duplicate.Success || !strings.Contains(duplicate.Content, "already loaded") || strings.Contains(duplicate.Content, "Run focused tests") {
		t.Fatalf("duplicate load = %#v err=%v", duplicate, err)
	}
	duplicateData, ok := duplicate.Data.(map[string]any)
	if !ok || duplicateData["changed"] != false || duplicateData["sequence"] != uint64(1) || duplicateData["render_hash"] != snapshot[0].RenderHash {
		t.Fatalf("duplicate data = %#v", duplicate.Data)
	}
	if got := tool.InvocationLedger().SnapshotNewestFirst(); len(got) != 1 || got[0] != snapshot[0] {
		t.Fatalf("duplicate changed ledger = %#v", got)
	}

	writeToolSkill(t, root, "verify", "---\nname: Verify display\ndescription: Verify code\n---\nRun the changed tests")
	changed, err := tool.Execute(context.Background(), map[string]any{"name": "verify"})
	if err != nil || !changed.Success || !strings.Contains(changed.Content, "Run the changed tests") {
		t.Fatalf("changed load = %#v err=%v", changed, err)
	}
	if got := tool.InvocationLedger().SnapshotNewestFirst(); len(got) != 1 || got[0].Rendered != changed.Content || got[0].Sequence != 2 {
		t.Fatalf("changed ledger = %#v", got)
	}
}

func TestSkillToolRedactsCanonicalPayloadBeforeDeliveryAndPersistence(t *testing.T) {
	root := t.TempDir()
	secret := "supersecretvalue123456789"
	writeToolSkill(t, root, "safe", "---\nname: safe\ndescription: Redact accidental secrets\n---\nUse api_key="+secret)
	tool := NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{Path: root, Source: "project"}}))

	result, err := tool.ExecuteForUser(map[string]any{"name": "safe"})
	if err != nil || !result.Success {
		t.Fatalf("load = %#v err=%v", result, err)
	}
	if strings.Contains(result.Content, secret) || !strings.Contains(result.Content, "[REDACTED]") {
		t.Fatalf("delivered payload was not redacted: %q", result.Content)
	}
	snapshot := tool.InvocationLedger().SnapshotNewestFirst()
	if len(snapshot) != 1 || snapshot[0].Rendered != result.Content || strings.Contains(snapshot[0].Rendered, secret) {
		t.Fatalf("persisted payload = %#v", snapshot)
	}
}

func TestSkillToolListAndFailedLoadDoNotMutateInvocationLedger(t *testing.T) {
	root := t.TempDir()
	writeToolSkill(t, root, "known", "---\nname: known\ndescription: Known workflow\n---\nDo work")
	tool := NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{Path: root, Source: "project"}}))

	for _, args := range []map[string]any{{}, {"query": "known"}, {"name": "missing"}} {
		_, _ = tool.Execute(context.Background(), args)
	}
	if got := tool.InvocationLedger().SnapshotNewestFirst(); len(got) != 0 {
		t.Fatalf("non-load operations recorded invocations: %#v", got)
	}
}
