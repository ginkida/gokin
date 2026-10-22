package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/hooks"
)

func writePluginFile(t *testing.T, root, path, content string) {
	t.Helper()
	file := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func pluginCatalog(t *testing.T, root string, roots []Root) (*Catalog, *hooks.Manager, string) {
	t.Helper()
	user := t.TempDir()
	manager := hooks.NewManager(false, t.TempDir())
	manager.ConfigureClaudeManagedSettings(t.TempDir())
	manager.ConfigureClaudeSettings("", user, false)
	manager.ConfigureClaudePlugins([]string{root})
	catalog := NewCatalog(roots)
	catalog.ConfigureClaudePlugins(manager)
	return catalog, manager, user
}

func TestPluginCatalogLoadsAllWorkflowFormsAndUsesNamespace(t *testing.T) {
	plugin, native := t.TempDir(), t.TempDir()
	writeSkill(t, native, "review", "---\ndescription: Native review\n---\nNative review instructions.")
	writePluginFile(t, plugin, ".claude-plugin/plugin.json", `{"name":"suite","skills":["./extra/","./single/","."],"commands":{"check":{"source":"./docs/check.md","description":"Override description","argumentHint":"target","allowedTools":["Read"],"model":"glm-5.3"},"inline":{"content":"Explain $ARGUMENTS."}}}`)
	writePluginFile(t, plugin, "skills/directory/SKILL.md", "---\nname: suite:review\ndescription: Plugin review\n---\nReview from plugin.")
	writePluginFile(t, plugin, "extra/extra/SKILL.md", "Extra workflow.")
	writePluginFile(t, plugin, "single/SKILL.md", "Single workflow.")
	writePluginFile(t, plugin, "SKILL.md", "Root workflow.")
	writePluginFile(t, plugin, "docs/check.md", "---\nname: ignored-by-object-key\nallowed-tools: Bash\n---\nCheck the project.")
	writePluginFile(t, plugin, "commands/replaced.md", "Must not load through a replaced default.")
	catalog, _, _ := pluginCatalog(t, plugin, []Root{{Path: native, Source: "global"}})
	if warnings := catalog.Warnings(); len(warnings) != 0 {
		t.Fatal(warnings)
	}
	for _, name := range []string{"review", "suite:review", "suite:extra", "suite:single", "suite:" + filepath.Base(plugin), "suite:check", "suite:inline"} {
		// t.TempDir's base is numeric and is a valid fallback skill segment.
		if _, ok := catalog.Get(name); !ok {
			t.Fatalf("workflow %q missing: %+v", name, catalog.List())
		}
	}
	if _, ok := catalog.Get("suite:replaced"); ok {
		t.Fatal("manifest commands did not replace default scan")
	}
	nativeSkill, _ := catalog.Get("review")
	if nativeSkill.Body != "Native review instructions." {
		t.Fatal("plugin alias shadowed native skill")
	}
	check, _ := catalog.Get("check")
	if check.Name != "suite:check" || check.Description != "Override description" || check.ArgumentHint != "target" || check.Model != "glm-5.3" || len(check.AllowedTools) != 1 || check.AllowedTools[0] != "read" || !check.PersonalSource() {
		t.Fatalf("command overrides/provenance: %+v", check)
	}
}

func TestPluginCatalogRejectsEscapesAndUnresolvedExecutableOptions(t *testing.T) {
	for _, tc := range []struct{ name, manifest, path, body string }{
		{"parent", `{"name":"suite","skills":"./../outside"}`, "skills/safe/SKILL.md", "Safe workflow."},
		{"absolute", `{"name":"suite","commands":"/tmp/external.md"}`, "skills/safe/SKILL.md", "Safe workflow."},
		{"missing-option", `{"name":"suite"}`, "skills/unsafe/SKILL.md", "Use ${user_config.missing}."},
		{"shell-option", `{"name":"suite","userConfig":{"value":{"type":"string","default":"literal"}}}`, "skills/unsafe/SKILL.md", "State: !`printf '${user_config.value}'`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plugin := t.TempDir()
			writePluginFile(t, plugin, ".claude-plugin/plugin.json", tc.manifest)
			writePluginFile(t, plugin, tc.path, tc.body)
			catalog, _, _ := pluginCatalog(t, plugin, nil)
			if len(catalog.Warnings()) == 0 {
				t.Fatal("invalid declaration did not report an error")
			}
			if _, ok := catalog.Get("suite:unsafe"); ok {
				t.Fatal("unsafe workflow loaded")
			}
		})
	}
	plugin, external := t.TempDir(), t.TempDir()
	writePluginFile(t, plugin, ".claude-plugin/plugin.json", `{"name":"suite","skills":"./escape"}`)
	writePluginFile(t, external, "outside/SKILL.md", "External workflow.")
	if err := os.Symlink(external, filepath.Join(plugin, "escape")); err != nil {
		t.Fatal(err)
	}
	catalog, _, _ := pluginCatalog(t, plugin, nil)
	if len(catalog.List()) != 0 || len(catalog.Warnings()) == 0 {
		t.Fatalf("symlink escape loaded: %+v", catalog.List())
	}
}

func TestPluginCatalogSelectsTrustedProjectAndRefreshesDisabledPolicy(t *testing.T) {
	project, user := t.TempDir(), t.TempDir()
	plugin := filepath.Join(project, ".claude", "skills", "suite")
	writePluginFile(t, plugin, ".claude-plugin/plugin.json", `{"name":"suite"}`)
	writePluginFile(t, plugin, "SKILL.md", "Project plugin workflow.")
	manager := hooks.NewManager(false, project)
	manager.ConfigureClaudeManagedSettings(t.TempDir())
	manager.ConfigureClaudeSettings(project, user, false)
	manager.ConfigureClaudePlugins(nil)
	catalog := NewCatalog([]Root{{Path: filepath.Dir(plugin), Source: "claude-project", Boundary: project}})
	catalog.ConfigureClaudePlugins(manager)
	if len(catalog.List()) != 0 {
		t.Fatal("untrusted project plugin became an ordinary skill")
	}
	manager.ConfigureClaudeSettings(project, user, true)
	catalog.ReloadIfChanged()
	skill, ok := catalog.Get("suite:suite")
	if !ok || skill.PersonalSource() || skill.Source != "claude-plugin:suite@skills-dir" {
		t.Fatalf("trusted project source=%+v warnings=%v", skill, catalog.Warnings())
	}
	writePluginFile(t, user, "settings.json", `{"enabledPlugins":{"suite@skills-dir":false}}`)
	catalog.ReloadIfChanged()
	if len(catalog.List()) != 0 {
		t.Fatal("disabled project plugin retained skills")
	}
	writePluginFile(t, user, "settings.json", `{"enabledPlugins":{"suite@skills-dir":true}}`)
	catalog.ReloadIfChanged()
	if _, ok := catalog.Get("suite:suite"); !ok {
		t.Fatal("policy repair did not restore catalog")
	}
}

func TestPluginCatalogCommandPathsAmbiguousAliasesAndLiteralOptions(t *testing.T) {
	plugin, other := t.TempDir(), t.TempDir()
	writePluginFile(t, plugin, ".claude-plugin/plugin.json", `{"name":"suite","commands":["./extra/a.md","./more"],"userConfig":{"text":{"type":"string","default":"$ARGUMENTS ${CLAUDE_PLUGIN_ROOT} !`+"`touch injected`"+`"}}}`)
	writePluginFile(t, plugin, "extra/a.md", "A workflow ${user_config.text}.")
	writePluginFile(t, plugin, "more/b.md", "B workflow.")
	writePluginFile(t, other, ".claude-plugin/plugin.json", `{"name":"other"}`)
	writePluginFile(t, other, "commands/a.md", "Other A workflow.")
	catalog, manager, _ := pluginCatalog(t, plugin, nil)
	manager.ConfigureClaudePlugins([]string{plugin, other})
	catalog.ReloadIfChanged()
	if _, ok := catalog.Get("a"); ok {
		t.Fatal("ambiguous bare alias picked an arbitrary plugin")
	}
	if _, ok := catalog.Get("b"); !ok {
		t.Fatal("unique bare alias missing")
	}
	a, _ := catalog.Get("suite:a")
	expanded, err := a.ExpandPluginVariables(a.Body)
	if err != nil || !strings.Contains(expanded, "$ARGUMENTS ${CLAUDE_PLUGIN_ROOT} !`touch injected`") {
		t.Fatalf("options were recursively expanded: %q %v", expanded, err)
	}
	invocation, _, err := NewInvocationLedger().Record(a.Name, expanded, a.Source, a.Path)
	if err != nil || invocation.Name != "suite:a" {
		t.Fatalf("namespaced invocation not retained: %+v %v", invocation, err)
	}
	encoded, err := json.Marshal(invocation)
	if err != nil || !strings.Contains(string(encoded), "suite:a") {
		t.Fatal("canonical name missing from session state")
	}
}
