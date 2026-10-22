package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"
	"gokin/internal/hooks"
	"gokin/internal/mcp"
	"gokin/internal/pluginsecrets"
	"gokin/internal/skills"
)

type memoryKeyring struct {
	mu   sync.Mutex
	keys map[string]string
	err  error
}

func (s *memoryKeyring) Get(service, account string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	value, found := s.keys[service+account]
	if !found {
		return "", keyring.ErrNotFound
	}
	return value, nil
}
func (s *memoryKeyring) Set(service, account, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.keys[service+account] = value
	return nil
}

func secretFixture(t *testing.T) (*Manager, string, string, *memoryKeyring) {
	t.Helper()
	m, market, plugin := fixture(t)
	store := &memoryKeyring{keys: map[string]string{}}
	m.opts.SecretKeyring = store
	writeTestJSON(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), map[string]any{"name": "alpha", "userConfig": map[string]any{"token": map[string]any{"type": "string", "sensitive": true, "required": true}, "value": map[string]any{"type": "string", "default": "ordinary"}}})
	os.WriteFile(filepath.Join(plugin, "skills", "review", "SKILL.md"), []byte("---\ndescription: Review\n---\nToken ${user_config.token}, value ${user_config.value}"), 0600)
	writeTestJSON(t, filepath.Join(plugin, ".mcp.json"), map[string]any{"db": map[string]any{"command": "UNUSED_BINARY", "env": map[string]string{"TOKEN": "${user_config.token}"}}})
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	return m, market, plugin, store
}

func secretAuthority(m *Manager) *hooks.Manager {
	authority := hooks.NewManager(false, m.opts.WorkDir)
	authority.ConfigureClaudeManagedSettings(m.opts.ManagedDir)
	authority.ConfigureClaudeSettings(m.opts.WorkDir, m.opts.UserDir, true)
	authority.ConfigureClaudeSecretOptions(func(id string) (map[string]any, error) {
		fields, err := m.readObject(pluginsecrets.Path(m.opts.UserDir))
		if err != nil {
			return nil, err
		}
		return pluginsecrets.Read(m.opts.UserDir, id, fields, m.opts.SecretKeyring)
	})
	authority.ConfigureClaudePlugins(nil)
	return authority
}

func TestSecretOptionsEncryptedAndHiddenFromContent(t *testing.T) {
	m, _, _, _ := secretFixture(t)
	secret := "opaque-private-value-123"
	result, err := m.Install(t.Context(), "alpha", InstallOptions{Config: map[string]string{"token": secret}})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(m.opts.UserDir, "settings.json"), filepath.Join(m.pluginDir, "installed_plugins.json"), pluginsecrets.Path(m.opts.UserDir)} {
		data, err := os.ReadFile(path)
		if err != nil || bytes.Contains(data, []byte(secret)) {
			t.Fatalf("plaintext file %s: %v", path, err)
		}
	}
	encoded, _ := json.Marshal(result)
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatal("result leaks secret")
	}
	authority := secretAuthority(m)
	selected, err := authority.ClaudePlugins()
	if err != nil || len(selected) != 1 {
		t.Fatalf("plugins=%+v %v", selected, err)
	}
	env, err := selected[0].ProcessEnvironment(m.opts.WorkDir)
	if err != nil || env["CLAUDE_PLUGIN_OPTION_TOKEN"] != secret {
		t.Fatalf("process secret unavailable: %v", err)
	}
	servers, err := mcp.LoadClaudePluginServers(authority, m.opts.WorkDir, true)
	if err != nil || len(servers) != 1 {
		t.Fatalf("servers=%+v %v", servers, err)
	}
	for _, server := range servers {
		if server.Env["TOKEN"] != secret {
			t.Fatal("MCP did not receive secret")
		}
	}
	catalog := skills.NewCatalog(nil)
	catalog.ConfigureClaudePlugins(authority)
	if len(catalog.List()) != 1 {
		t.Fatal("plugin skill was not loaded")
	}
	for _, skill := range catalog.List() {
		body, err := skill.ExpandPluginVariables(skill.Body)
		if err != nil || strings.Contains(body, secret) || !strings.Contains(body, "[sensitive option token]") {
			t.Fatalf("skill content=%q %v", body, err)
		}
	}
	previous := selected[0].RuntimeFingerprint()
	if _, err := m.Configure(t.Context(), "alpha", map[string]string{"token": "replacement"}); err != nil {
		t.Fatal(err)
	}
	selected, err = authority.ClaudePlugins()
	if err != nil || len(selected) != 1 || selected[0].RuntimeFingerprint() == previous {
		t.Fatal("changed secret was not reloaded")
	}
	if _, err := m.Uninstall(t.Context(), "alpha", RemoveOptions{KeepData: true}); err != nil {
		t.Fatal(err)
	}
	fields, _ := m.readObject(pluginsecrets.Path(m.opts.UserDir))
	values, err := pluginsecrets.Read(m.opts.UserDir, "alpha@acme", fields, m.opts.SecretKeyring)
	if err != nil || len(values) != 0 {
		t.Fatal("uninstall retained secrets")
	}
}

func TestSecretOptionsRetainedAcrossScopesAndRemovedByPrune(t *testing.T) {
	m, _, _, _ := secretFixture(t)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{Config: map[string]string{"token": "shared"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{Scope: "project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Uninstall(t.Context(), "alpha", RemoveOptions{Scope: "user"}); err != nil {
		t.Fatal(err)
	}
	fields, _ := m.readObject(pluginsecrets.Path(m.opts.UserDir))
	values, err := pluginsecrets.Read(m.opts.UserDir, "alpha@acme", fields, m.opts.SecretKeyring)
	if err != nil || values["token"] != "shared" {
		t.Fatal("removing one scope erased shared secret")
	}
	doc, installed, err := m.installs()
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	json.Unmarshal(installed["alpha@acme"][0], &record)
	record["autoInstalled"] = json.RawMessage("true")
	installed["alpha@acme"][0], _ = json.Marshal(record)
	doc["plugins"], _ = json.Marshal(installed)
	writeTestJSON(t, filepath.Join(m.pluginDir, "installed_plugins.json"), doc)
	result, err := m.Prune(t.Context(), PruneOptions{Scope: "project"})
	if err != nil || len(result.Pruned) != 1 {
		t.Fatalf("prune=%+v %v", result, err)
	}
	fields, _ = m.readObject(pluginsecrets.Path(m.opts.UserDir))
	values, err = pluginsecrets.Read(m.opts.UserDir, "alpha@acme", fields, m.opts.SecretKeyring)
	if err != nil || len(values) != 0 {
		t.Fatal("prune retained last scope's secret")
	}
}

func TestSensitivePlaintextSettingsRejected(t *testing.T) {
	m, _, _, _ := secretFixture(t)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{Config: map[string]string{"token": "encrypted"}}); err != nil {
		t.Fatal(err)
	}
	writeTestJSON(t, filepath.Join(m.opts.ManagedDir, "managed-settings.json"), map[string]any{"pluginConfigs": map[string]any{"alpha@acme": map[string]any{"options": map[string]string{"token": "managed-plaintext"}}}})
	if _, err := secretAuthority(m).ClaudePlugins(); err == nil || strings.Contains(err.Error(), "managed-plaintext") {
		t.Fatalf("managed plaintext accepted or echoed: %v", err)
	}
}

func TestSecretOptionsFailuresPreserveState(t *testing.T) {
	m, _, _, store := secretFixture(t)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{Config: map[string]string{"token": "initial"}}); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(m.opts.UserDir, "settings.json"), pluginsecrets.Path(m.opts.UserDir), filepath.Join(m.pluginDir, "installed_plugins.json")}
	before := map[string][]byte{}
	for _, path := range paths {
		before[path], _ = os.ReadFile(path)
	}
	for _, mode := range []string{"invalid", "locked"} {
		values := map[string]string{"token": "new", "value": "ordinary"}
		if mode == "invalid" {
			values["token"] = strings.Repeat("x", 9000)
		} else {
			store.err = errors.New("cannot unlock secret backend: private-value")
		}
		if _, err := m.Configure(t.Context(), "alpha", values); err == nil || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("%s failure not handled: %v", mode, err)
		}
		store.err = nil
		for _, path := range paths {
			after, _ := os.ReadFile(path)
			if !bytes.Equal(after, before[path]) {
				t.Fatalf("%s modified %s", mode, path)
			}
		}
	}
}

func TestSecretArgumentsRejectedAndStdinInstallAccepted(t *testing.T) {
	m, _, _, _ := secretFixture(t)
	for _, stdin := range []bool{false, true} {
		cmd := NewCommand(func() (Options, error) { return m.opts, nil })
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		args := []string{"install", "alpha", "--json"}
		if stdin {
			args = append(args, "--values-stdin")
			cmd.SetIn(strings.NewReader(`{"token":"provided-via-stdin"}`))
		} else {
			args = append(args, "--config", "token=visible-value")
		}
		cmd.SetArgs(args)
		err := cmd.Execute()
		if !stdin && (err == nil || strings.Contains(output.String(), "visible-value")) {
			t.Fatalf("argv secret accepted or echoed: %s %v", output.String(), err)
		}
		if stdin && (err != nil || strings.Contains(output.String(), "provided-via-stdin")) {
			t.Fatalf("stdin failed or echoed: %s %v", output.String(), err)
		}
	}
}
