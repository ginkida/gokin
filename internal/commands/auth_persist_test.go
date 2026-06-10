package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/config"
)

// TestMain isolates all config-file I/O for this package's tests into a
// temporary XDG_CONFIG_HOME. /login now re-saves the config to disk to verify
// persistence; without this isolation those tests would read/write the
// developer's real ~/.config/gokin/config.yaml (or fail in CI sandboxes with
// no writable HOME).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gokin-commands-test-cfg-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// TestLogin_PersistenceFailure_WarnsHonestly reproduces the reported bug class:
// the key applies for the session but cannot be written to disk, so the user
// has to re-enter it next launch. /login must say so honestly (with the path)
// instead of printing a success message, since ApplyConfig swallows the Save
// error internally.
func TestLogin_PersistenceFailure_WarnsHonestly(t *testing.T) {
	// Point the config dir at a regular FILE so MkdirAll/WriteFile fail —
	// simulates an unwritable/ephemeral config location.
	brokenAsDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(brokenAsDir, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", brokenAsDir)

	app := newAuthApp(&config.Config{})
	cmd := &LoginCommand{}
	key := "sk-deepseek-" + strings.Repeat("x", 40)

	out, err := cmd.Execute(context.Background(), []string{"deepseek", key}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "could NOT be saved") {
		t.Fatalf("expected an honest persistence-failure warning, got:\n%s", out)
	}
	if !strings.Contains(out, "DeepSeek") {
		t.Fatalf("warning should name the provider, got:\n%s", out)
	}
	// The key should still be applied for the current session.
	if app.applyCalls != 1 {
		t.Fatalf("ApplyConfig should still run (session apply); calls=%d", app.applyCalls)
	}
}

// TestLogin_PersistenceSuccess_NoWarning confirms the happy path: a writable
// config dir yields the normal success message, not the warning.
func TestLogin_PersistenceSuccess_NoWarning(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	app := newAuthApp(&config.Config{})
	cmd := &LoginCommand{}
	key := "sk-deepseek-" + strings.Repeat("x", 40)

	out, err := cmd.Execute(context.Background(), []string{"deepseek", key}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if strings.Contains(out, "could NOT be saved") {
		t.Fatalf("writable dir should not warn, got:\n%s", out)
	}
	// The key really is on disk and reloads.
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if reloaded.API.DeepSeekKey != key {
		t.Fatalf("DeepSeekKey did not persist: %q", reloaded.API.DeepSeekKey)
	}
}
