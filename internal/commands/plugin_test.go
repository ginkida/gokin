package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/hooks"
)

func TestPluginCommandSharesSessionStoreAndManagedPolicy(t *testing.T) {
	work, user, managed, otherUser, market := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", otherUser)
	catalog := filepath.Join(market, ".claude-plugin", "marketplace.json")
	if err := os.MkdirAll(filepath.Dir(catalog), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog, []byte(`{"name":"session-market","owner":{"name":"test"},"plugins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := hooks.NewManager(false, work)
	manager.ConfigureClaudeSettings(work, user, false)
	manager.ConfigureClaudeManagedSettings(managed)
	app := &fakeAppForHooks{fakeAppForMCP: &fakeAppForMCP{workDir: work}, mgr: manager}
	command := &PluginCommand{}
	out, err := command.Execute(t.Context(), []string{"marketplace", "add", market}, app)
	if err != nil {
		t.Fatalf("add to session store: %q %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(user, "plugins", "known_marketplaces.json")); err != nil {
		t.Fatalf("session registry missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(otherUser, "plugins")); !os.IsNotExist(err) {
		t.Fatalf("environment's replacement store was touched: %v", err)
	}
	if err := os.WriteFile(filepath.Join(managed, "managed-settings.json"), []byte(`{"strictKnownMarketplaces":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = command.Execute(t.Context(), []string{"marketplace", "add", market}, app)
	if err == nil || !strings.Contains(err.Error(), "managed") {
		t.Fatalf("session managed policy was ignored: %q %v", out, err)
	}
}
