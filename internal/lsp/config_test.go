package lsp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/hooks"
	"gokin/internal/lsp"
)

func configFixture(t *testing.T, manifest string) (string, string, *hooks.Manager) {
	t.Helper()
	plugin, work, user := t.TempDir(), t.TempDir(), t.TempDir()
	plugin, _ = filepath.EvalSymlinks(plugin)
	work, _ = filepath.EvalSymlinks(work)
	os.MkdirAll(filepath.Join(plugin, ".claude-plugin"), 0700)
	os.WriteFile(filepath.Join(plugin, ".claude-plugin", "plugin.json"), []byte(manifest), 0600)
	authority := hooks.NewManager(false, work)
	authority.ConfigureClaudeManagedSettings(t.TempDir())
	authority.ConfigureClaudeSettings(work, user, false)
	authority.ConfigureClaudePlugins([]string{plugin})
	return plugin, work, authority
}
func TestLSPLoaderMergeExpansionAndNoSpawn(t *testing.T) {
	plugin, work, authority := configFixture(t, `{"name":"suite","userConfig":{"literal":{"type":"string","default":"${HOME} !touch never"}},"lspServers":["./more.json",{"python":{"command":"${CLAUDE_PLUGIN_ROOT}/bin/server","args":["${user_config.literal}"],"env":{"LITERAL":"${user_config.literal}","CLAUDE_PLUGIN_ROOT":"spoof"},"extensionToLanguage":{".py":"python"},"initializationOptions":{"literal":"${user_config.literal}"},"settings":{"directory":"${CLAUDE_PROJECT_DIR}"},"transport":"socket"}}]}`)
	os.WriteFile(filepath.Join(plugin, ".lsp.json"), []byte(`{"python":{"command":"old","extensionToLanguage":{".py":"old"}},"go":{"command":"gopls","extensionToLanguage":{".go":"go"}}}`), 0600)
	os.WriteFile(filepath.Join(plugin, "more.json"), []byte(`{"python":{"command":"middle","extensionToLanguage":{".py":"middle"}}}`), 0600)
	configs, err := lsp.LoadPluginConfigs(authority, work)
	if err != nil || len(configs) != 2 {
		t.Fatalf("configs=%+v %v", configs, err)
	}
	python := configs[1]
	if python.Name() != "plugin:suite:python" || python.Command != filepath.Join(plugin, "bin", "server") || python.Args[0] != "${HOME} !touch never" || python.Env["LITERAL"] != "${HOME} !touch never" || python.Env["CLAUDE_PLUGIN_ROOT"] != plugin {
		t.Fatalf("expansion=%+v", python)
	}
	if _, err := os.Stat(python.Env["CLAUDE_PLUGIN_DATA"]); !os.IsNotExist(err) {
		t.Fatal("discovery created plugin data")
	}
	data, _ := json.Marshal(python.Settings)
	if !strings.Contains(string(data), work) {
		t.Fatalf("settings=%s", data)
	}
}
func TestLSPLoaderRejectsInvalidSourcesAndFields(t *testing.T) {
	for _, tc := range []struct{ name, source string }{
		{"parent", `"./../outside.json"`}, {"absolute", `"/tmp/out.json"`}, {"nonjson", `"./config.txt"`}, {"nested", `[[{}]]`},
		{"missing-command", `{"x":{"extensionToLanguage":{".go":"go"}}}`},
		{"command-with-args", `{"x":{"command":"server --stdio","extensionToLanguage":{".go":"go"}}}`},
		{"empty-extensions", `{"x":{"command":"server","extensionToLanguage":{}}}`},
		{"invalid-extension", `{"x":{"command":"server","extensionToLanguage":{"go":"go"}}}`},
		{"unknown", `{"x":{"command":"server","extensionToLanguage":{".go":"go"},"unsafe":true}}`},
		{"zero-timeout", `{"x":{"command":"server","extensionToLanguage":{".go":"go"},"requestTimeout":0}}`},
		{"negative-restarts", `{"x":{"command":"server","extensionToLanguage":{".go":"go"},"maxRestarts":-1}}`},
		{"null-bool", `{"x":{"command":"server","extensionToLanguage":{".go":"go"},"diagnostics":null}}`},
		{"escape-workspace", `{"x":{"command":"server","extensionToLanguage":{".go":"go"},"workspaceFolder":".."}}`},
		{"duplicate", `{"x":{"command":"one","command":"two","extensionToLanguage":{".go":"go"}}}`},
		{"wrapper", `{"lspServers":{"x":{"command":"server","extensionToLanguage":{".go":"go"}}}}`},
		{"unavailable-option", `{"x":{"command":"${user_config.missing}","extensionToLanguage":{".go":"go"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, work, authority := configFixture(t, `{"name":"suite","lspServers":`+tc.source+`}`)
			if _, err := lsp.LoadPluginConfigs(authority, work); err == nil {
				t.Fatal("invalid LSP config accepted")
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		plugin, work, authority := configFixture(t, `{"name":"suite","lspServers":"./escaped.json"}`)
		target := filepath.Join(t.TempDir(), "escaped.json")
		os.WriteFile(target, []byte(`{}`), 0600)
		if err := os.Symlink(target, filepath.Join(plugin, "escaped.json")); err != nil {
			t.Skip(err)
		}
		if _, err := lsp.LoadPluginConfigs(authority, work); err == nil {
			t.Fatal("symlink source accepted")
		}
	})
}
