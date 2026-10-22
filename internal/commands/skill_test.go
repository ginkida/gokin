package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/hooks"
	"gokin/internal/permission"
	"gokin/internal/skills"
	"gokin/internal/tools"
)

func TestSkillCommandListsAndSubmitsExplicitWorkflow(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "release", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nname: release\ndescription: Release safely\ndisable-model-invocation: true\n---\nFirst=$1\nSecond=$2\nAll=$ARGUMENTS"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := skills.NewCatalog([]skills.Root{{Path: root, Source: "project"}})
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewSkillToolWithCatalog(catalog))
	app := &fakeAppForMCP{registry: registry}
	command := &SkillCommand{}

	listed, err := command.Execute(context.Background(), nil, app)
	if err != nil || !strings.Contains(listed, "release") {
		t.Fatalf("list = %q err=%v", listed, err)
	}
	loaded, err := command.Execute(context.Background(), []string{"release", "hello world", "second"}, app)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{PromptMarker, "First=hello world", "Second=second", `All="hello world" second`} {
		if !strings.Contains(loaded, want) {
			t.Fatalf("loaded command result missing %q: %q", want, loaded)
		}
	}
	if !strings.HasPrefix(loaded, PromptMarker) {
		t.Fatalf("loaded command result = %q", loaded)
	}
}

func TestPluginSlashCommandParserPreservesRawArgumentsAndBuiltinPrecedence(t *testing.T) {
	plugin, work, user := t.TempDir(), t.TempDir(), t.TempDir()
	for path, body := range map[string]string{
		".claude-plugin/plugin.json": `{"name":"suite"}`,
		"commands/release.md":        "---\ndescription: Plugin release\ndisable-model-invocation: true\n---\nFirst=$0\nAll=$ARGUMENTS",
		"commands/help.md":           "Plugin help instructions.",
		"commands/internal.md":       "---\nuser-invocable: false\n---\nInternal instructions.",
	} {
		file := filepath.Join(plugin, path)
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager := hooks.NewManager(false, work)
	manager.ConfigureClaudeManagedSettings(t.TempDir())
	manager.ConfigureClaudeSettings(work, user, false)
	manager.ConfigureClaudePlugins([]string{plugin})
	skill := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog(nil), work)
	skill.ConfigureClaudePlugins(manager)
	registry := tools.NewRegistry()
	registry.MustRegister(skill)
	app := &fakeAppForMCP{registry: registry}
	handler := NewHandler()
	handler.ConfigureSkillResolver(skill.HasUserSkill)
	handler.SealBootPhase()
	input := "/suite:release 'hello world'  $HOME  "
	name, args, ok := handler.Parse(input)
	if !ok || name != "suite:release" {
		t.Fatalf("plugin slash was not recognized: %q %v", name, ok)
	}
	result, err := handler.Execute(WithRawInvocation(t.Context(), input), name, args, app)
	if err != nil || !strings.HasPrefix(result, PromptMarker) || !strings.Contains(result, "First=hello world") || !strings.Contains(result, "All='hello world'  $HOME  ") {
		t.Fatalf("raw plugin arguments=%q %v", result, err)
	}
	if _, _, ok := handler.Parse("/release hello"); !ok {
		t.Fatal("unique plugin alias was not recognized")
	}
	if _, _, ok := handler.Parse("/suite:internal"); ok {
		t.Fatal("hidden workflow exposed as user command")
	}
	if _, _, ok := handler.Parse("/tmp/file"); ok {
		t.Fatal("path became slash workflow")
	}
	result, err = handler.Execute(t.Context(), "help", nil, app)
	if err != nil || strings.HasPrefix(result, PromptMarker) {
		t.Fatalf("plugin alias shadowed built-in: %q %v", result, err)
	}
}

type dynamicSkillCommandApp struct {
	*fakeAppForMCP
	executor *tools.Executor
}

func (a *dynamicSkillCommandApp) SkillInvocationContext(ctx context.Context) context.Context {
	return a.executor.SkillInvocationContext(ctx)
}

func TestSkillCommandRendersDynamicContextWithOwningExecutor(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "dynamic", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\ndescription: Slash command context\nallowed-tools: Bash\n---\nState: !`printf '%s' '$0'`"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	skill := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}), root)
	registry.MustRegister(skill)
	registry.MustRegister(tools.NewBashTool(root))
	executor := tools.NewExecutor(registry, nil, time.Second)
	executor.SetPermissions(permission.NewManager(permission.DefaultRules(), true))
	app := &dynamicSkillCommandApp{fakeAppForMCP: &fakeAppForMCP{registry: registry}, executor: executor}
	result, err := (&SkillCommand{}).Execute(t.Context(), []string{"dynamic", "slash-result"}, app)
	if err != nil || !strings.Contains(result, "State: slash-result") || !strings.HasPrefix(result, PromptMarker) {
		t.Fatalf("load=%q err=%v", result, err)
	}
	if len(skill.ActivePermissionGrants()) != 0 {
		t.Fatal("slash render published grants before the model turn")
	}
	skill.BeginPermissionTurn()
	if grants := skill.ActivePermissionGrants(); len(grants) != 1 || grants[0] != "bash" {
		t.Fatalf("queued prompt lost grants: %v", grants)
	}
}

func TestSkillCommandMissingRuntimeIsHonest(t *testing.T) {
	_, err := (&SkillCommand{}).Execute(context.Background(), nil, &fakeAppForMCP{})
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("error = %v", err)
	}
}

func TestSkillCommandPreservesRawQuotedArguments(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "raw", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	document := `---
name: Raw arguments
description: Verify lossless command handoff
---
Raw=$ARGUMENTS
First=$1
Second=$2
Third=$3`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{Path: root, Source: "project"}})))
	app := &fakeAppForMCP{registry: registry}
	input := `/skill raw '$HOME' pre"hello world"post "C:\Program Files"`
	handler := NewHandler()
	name, args, ok := handler.Parse(input)
	if !ok || name != "skill" {
		t.Fatalf("Parse(%q) = %q %#v %v", input, name, args, ok)
	}

	ctx := WithRawInvocation(context.Background(), input)
	loaded, err := (&SkillCommand{}).Execute(ctx, args, app)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`Raw='$HOME' pre"hello world"post "C:\Program Files"`,
		"First=$HOME",
		"Second=prehello worldpost",
		`Third=C:\Program Files`,
	} {
		if !strings.Contains(loaded, want) {
			t.Fatalf("lossless command result missing %q:\n%s", want, loaded)
		}
	}
}
