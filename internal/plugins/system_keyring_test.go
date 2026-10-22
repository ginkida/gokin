package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
	"gokin/internal/hooks"
	"gokin/internal/pluginsecrets"
)

// Opt-in because this creates and deletes one temporary key in the logged-in
// user's real credential store. No existing credential or OS setting changes.
func TestSystemKeyringPluginLifecycle(t *testing.T) {
	if os.Getenv("GOKIN_TEST_SYSTEM_KEYRING") != "1" {
		t.Skip("set GOKIN_TEST_SYSTEM_KEYRING=1 to test the native credential store")
	}
	m, _, _, _ := secretFixture(t)
	m.opts.SecretKeyring = nil
	account := pluginsecrets.Account(m.opts.UserDir)
	t.Cleanup(func() {
		if err := keyring.Delete(pluginsecrets.Service, account); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			t.Error("temporary credential cleanup failed")
		}
	})
	command := NewCommand(func() (Options, error) { return m.opts, nil })
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetIn(bytes.NewBufferString(`{"token":"system-keyring-test-value"}`))
	command.SetArgs([]string{"install", "alpha", "--values-stdin", "--json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output.Bytes(), []byte("system-keyring-test-value")) {
		t.Fatal("CLI echoed a secret")
	}
	// A separate process uses the normal runtime reader, with neither a test
	// keyring nor an injected secret-options callback.
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), binary, "-test.run=^TestSystemKeyringRuntimeProcess$")
	child.Env = append(os.Environ(), "GOKIN_KEYRING_RUNTIME_USER="+m.opts.UserDir, "GOKIN_KEYRING_RUNTIME_WORK="+m.opts.WorkDir, "GOKIN_KEYRING_RUNTIME_MANAGED="+m.opts.ManagedDir)
	if data, err := child.CombinedOutput(); err != nil {
		t.Fatalf("fresh runtime failed: %v %s", err, data)
	}
	fields, err := m.readObject(pluginsecrets.Path(m.opts.UserDir))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(fields)
	if bytes.Contains(data, []byte("system-keyring-test-value")) {
		t.Fatal("plaintext in encrypted file")
	}
	if _, err := m.Uninstall(t.Context(), "alpha", RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(m.opts.UserDir, "settings.json"), pluginsecrets.Path(m.opts.UserDir)} {
		data, err := os.ReadFile(path)
		if err != nil || bytes.Contains(data, []byte("system-keyring-test-value")) {
			t.Fatal("secret cleanup failed")
		}
	}
}

func TestSystemKeyringRuntimeProcess(t *testing.T) {
	user := os.Getenv("GOKIN_KEYRING_RUNTIME_USER")
	if os.Getenv("GOKIN_TEST_SYSTEM_KEYRING") != "1" || user == "" {
		t.Skip("native credential-store helper process")
	}
	work := os.Getenv("GOKIN_KEYRING_RUNTIME_WORK")
	authority := hooks.NewManager(false, work)
	authority.ConfigureClaudeManagedSettings(os.Getenv("GOKIN_KEYRING_RUNTIME_MANAGED"))
	authority.ConfigureClaudeSettings(work, user, true)
	authority.ConfigureClaudePlugins(nil)
	selected, err := authority.ClaudePlugins()
	if err != nil || len(selected) != 1 {
		t.Fatalf("native runtime discovery: %v", err)
	}
	env, err := selected[0].ProcessEnvironment(work)
	if err != nil || env["CLAUDE_PLUGIN_OPTION_TOKEN"] != "system-keyring-test-value" {
		t.Fatal("native runtime did not decrypt the saved option")
	}
	content, err := selected[0].ExpandContent("${user_config.token}")
	if err != nil || content != "[sensitive option token]" {
		t.Fatal("native runtime exposed a secret to content")
	}
}
