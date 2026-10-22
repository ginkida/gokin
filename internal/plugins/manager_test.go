package plugins

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/fileutil"
	"gokin/internal/hooks"
)

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0700)
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func fixture(t *testing.T) (*Manager, string, string) {
	t.Helper()
	user, work, managed, market := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	work, _ = filepath.EvalSymlinks(work)
	market, _ = filepath.EvalSymlinks(market)
	plugin := filepath.Join(market, "plugins", "alpha")
	writeTestJSON(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), map[string]any{"name": "alpha", "version": "1.0", "userConfig": map[string]any{"value": map[string]any{"type": "string", "default": "literal"}}, "hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Write", "hooks": []any{map[string]any{"type": "command", "command": "touch NEVER_EXECUTED"}}}}}})
	os.MkdirAll(filepath.Join(plugin, "skills", "review"), 0700)
	os.WriteFile(filepath.Join(plugin, "skills", "review", "SKILL.md"), []byte("---\ndescription: Review code\n---\nReview using ${user_config.value}.\n"), 0600)
	os.MkdirAll(filepath.Join(plugin, "agents"), 0700)
	os.WriteFile(filepath.Join(plugin, "agents", "review.md"), []byte("---\ndescription: Review sources\ntools: Read\n---\nReview carefully.\n"), 0600)
	writeTestJSON(t, filepath.Join(plugin, ".mcp.json"), map[string]any{"db": map[string]any{"command": "UNUSED_BINARY"}})
	writeTestJSON(t, filepath.Join(plugin, ".lsp.json"), map[string]any{"python": map[string]any{"command": "UNUSED_BINARY", "extensionToLanguage": map[string]string{".py": "python"}}})
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Acme"}, "plugins": []any{map[string]any{"name": "alpha", "source": "./plugins/alpha"}}})
	writeTestJSON(t, filepath.Join(user, "settings.json"), map[string]any{"theme": "existing", "pluginConfigs": map[string]any{"other@elsewhere": map[string]any{"options": map[string]string{"keep": "value"}}}})
	manager, err := NewManager(Options{UserDir: user, WorkDir: work, ManagedDir: managed, Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	return manager, market, plugin
}
func runtimePlugins(t *testing.T, m *Manager) []hooks.ClaudePlugin {
	t.Helper()
	authority := hooks.NewManager(false, m.opts.WorkDir)
	authority.ConfigureClaudeManagedSettings(m.opts.ManagedDir)
	authority.ConfigureClaudeSettings(m.opts.WorkDir, m.opts.UserDir, m.opts.Trusted)
	authority.ConfigureClaudePlugins(nil)
	selected, err := authority.ClaudePlugins()
	if err != nil {
		t.Fatal(err)
	}
	return selected
}

func TestMarketplaceInstallUpdateConfigureAndScopes(t *testing.T) {
	manager, market, plugin := fixture(t)
	if rows, err := manager.List(); err != nil || len(rows) != 0 {
		t.Fatalf("initial=%+v %v", rows, err)
	}
	if _, err := manager.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Install(t.Context(), "alpha@acme", InstallOptions{Config: map[string]string{"value": "${HOME} literal"}})
	if err != nil || !result.Changed {
		t.Fatalf("install=%+v %v", result, err)
	}
	if selected := runtimePlugins(t, manager); len(selected) != 1 || selected[0].ID() != "alpha@acme" {
		t.Fatalf("runtime=%+v", selected)
	}
	rows, err := manager.List()
	if err != nil || len(rows) != 1 || !rows[0].Enabled {
		t.Fatalf("list=%+v %v", rows, err)
	}
	oldPath := rows[0].InstallPath
	if _, err := os.Stat(filepath.Join(manager.opts.WorkDir, "NEVER_EXECUTED")); !os.IsNotExist(err) {
		t.Fatal("install ran a hook")
	}
	settings, err := readObject(filepath.Join(manager.opts.UserDir, "settings.json"))
	if err != nil || string(settings["theme"]) != "\"existing\"" {
		t.Fatal("install erased unrelated settings")
	}
	if _, err := manager.SetEnabled(t.Context(), "alpha", "", false); err != nil {
		t.Fatal(err)
	}
	if selected := runtimePlugins(t, manager); len(selected) != 0 {
		t.Fatal("disabled plugin still selected")
	}
	if _, err := manager.SetEnabled(t.Context(), "alpha", "", true); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(manager.pluginDir, "installed_plugins.json"))
	writeTestJSON(t, filepath.Join(plugin, ".mcp.json"), map[string]any{"bad": map[string]any{"command": "server", "unsupported": true}})
	if _, err := manager.Install(t.Context(), "alpha", InstallOptions{Update: true}); err == nil {
		t.Fatal("invalid update was activated")
	}
	after, _ := os.ReadFile(filepath.Join(manager.pluginDir, "installed_plugins.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("failed update changed working registry")
	}
	writeTestJSON(t, filepath.Join(plugin, ".mcp.json"), map[string]any{"db": map[string]any{"command": "UNUSED_BINARY"}})
	os.WriteFile(filepath.Join(plugin, "new.txt"), []byte("version two"), 0600)
	if result, err := manager.Install(t.Context(), "alpha", InstallOptions{Update: true}); err != nil || !result.Changed {
		t.Fatalf("update=%+v %v", result, err)
	}
	rows, _ = manager.List()
	if rows[0].InstallPath == oldPath {
		t.Fatal("changed content overwrote active immutable source")
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatal("update removed old source used by live session")
	}
	if _, err := manager.Configure(t.Context(), "alpha@acme", map[string]string{"missing": "value"}); err == nil {
		t.Fatal("undeclared option accepted")
	}
	if _, err := manager.Configure(t.Context(), "alpha@acme", map[string]string{"value": "configured"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(t.Context(), "alpha", InstallOptions{Scope: "project"}); err != nil {
		t.Fatal(err)
	}
	rows, _ = manager.List()
	if len(rows) != 2 {
		t.Fatalf("scope records=%+v", rows)
	}
	if _, err := manager.Uninstall(t.Context(), "alpha", RemoveOptions{Scope: "project"}); err != nil {
		t.Fatal(err)
	}
	rows, _ = manager.List()
	if len(rows) != 1 {
		t.Fatal("uninstall erased another scope")
	}
	if _, err := manager.RemoveMarketplace(t.Context(), "acme"); err == nil {
		t.Fatal("marketplace removed while plugin still installed")
	}
	if _, err := manager.Uninstall(t.Context(), "alpha", RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RemoveMarketplace(t.Context(), "acme"); err != nil {
		t.Fatal(err)
	}
}

func gitTest(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.email=fixture@example.test", "-c", "user.name=Fixture"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}
func TestGitMarketplaceAndPinnedPluginSource(t *testing.T) {
	manager, market, plugin := fixture(t)
	gitTest(t, market, "init", "-q")
	gitTest(t, market, "add", ".")
	gitTest(t, market, "commit", "--no-gpg-sign", "-q", "-m", "fixture")
	commit := gitTest(t, market, "rev-parse", "HEAD")
	if _, err := manager.AddMarketplace(t.Context(), "file://"+filepath.ToSlash(market), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Acme"}, "plugins": []any{map[string]any{"name": "alpha", "source": map[string]any{"source": "git-subdir", "url": "file://" + filepath.ToSlash(market), "path": "plugins/alpha", "sha": commit}}}})
	os.WriteFile(filepath.Join(plugin, "added.txt"), []byte("newer content"), 0600)
	gitTest(t, market, "add", ".")
	gitTest(t, market, "commit", "--no-gpg-sign", "-q", "-m", "change")
	if _, err := manager.UpdateMarketplace(t.Context(), "acme", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(t.Context(), "alpha", InstallOptions{Update: true}); err != nil {
		t.Fatal(err)
	}
	rows, _ := manager.List()
	if rows[0].GitCommitSHA != commit {
		t.Fatalf("pin=%+v", rows[0])
	}
	if _, err := os.Stat(filepath.Join(rows[0].InstallPath, "added.txt")); !os.IsNotExist(err) {
		t.Fatal("pinned commit loaded latest content")
	}
}

func TestCommandSourceNeedsExactApprovalAndPolicyPrecedesExecution(t *testing.T) {
	manager, market, plugin := fixture(t)
	marker := filepath.Join(t.TempDir(), "marker")
	command := "printf ran > '" + marker + "'; printf '%s\\n' '" + plugin + "'"
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Acme"}, "plugins": []any{map[string]any{"name": "alpha", "source": map[string]any{"source": "command", "command": command}}}})
	if _, err := manager.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	_, err := manager.Install(t.Context(), "alpha", InstallOptions{})
	var approval *CommandApprovalError
	if !errors.As(err, &approval) {
		t.Fatalf("approval=%v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unreviewed command executed")
	}
	if _, err := manager.Install(t.Context(), "alpha", InstallOptions{AcceptCommands: []string{approval.SHA256}}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "ran" {
		t.Fatal("accepted command not run")
	}
	os.Remove(marker)
	if _, err := manager.Install(t.Context(), "alpha", InstallOptions{Update: true}); err != nil {
		t.Fatalf("unchanged approved command asked again: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("stored command approval was not applied", err)
	}
	changedMarker := filepath.Join(t.TempDir(), "changed-command")
	changedCommand := command + "; printf changed > '" + changedMarker + "'"
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Acme"}, "plugins": []any{map[string]any{"name": "alpha", "source": map[string]any{"source": "command", "command": changedCommand}}}})
	if _, err := manager.Install(t.Context(), "alpha", InstallOptions{Update: true}); err == nil {
		t.Fatal("changed command reused old acceptance")
	}
	if _, err := os.Stat(changedMarker); !os.IsNotExist(err) {
		t.Fatal("changed command executed before review")
	}
	writeTestJSON(t, filepath.Join(manager.opts.ManagedDir, "managed-settings.json"), map[string]any{"disableCommandPluginSources": true})
	os.Remove(marker)
	if _, err := manager.Install(t.Context(), "alpha", InstallOptions{Update: true, AcceptCommands: []string{approval.SHA256}}); err == nil {
		t.Fatal("managed command-source disable bypassed")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("policy rejection happened after executing command")
	}
}

func TestArchivesRejectTraversalLinksDuplicatesAndDigest(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "a/../../escape", `C:\escape`} {
		t.Run(name, func(t *testing.T) {
			var buffer bytes.Buffer
			writer := zip.NewWriter(&buffer)
			entry, _ := writer.Create(name)
			entry.Write([]byte("bad"))
			writer.Close()
			archive := filepath.Join(t.TempDir(), "bad.zip")
			os.WriteFile(archive, buffer.Bytes(), 0600)
			if err := extractZIP(archive, t.TempDir()); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
	var buffer bytes.Buffer
	tw := tar.NewWriter(&buffer)
	tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/tmp/out"})
	tw.Close()
	if err := extractTar(bytes.NewReader(buffer.Bytes()), t.TempDir()); err == nil {
		t.Fatal("tar symlink accepted")
	}
	buffer.Reset()
	zw := zip.NewWriter(&buffer)
	for i := 0; i < 2; i++ {
		entry, _ := zw.Create("same")
		entry.Write([]byte("x"))
	}
	zw.Close()
	archive := filepath.Join(t.TempDir(), "duplicate.zip")
	os.WriteFile(archive, buffer.Bytes(), 0600)
	if err := extractZIP(archive, t.TempDir()); err == nil {
		t.Fatal("duplicate archive accepted")
	}
	if err := validateHTTPURL("https://127.0.0.1/plugin.zip", true); err == nil {
		t.Fatal("private archive endpoint accepted")
	}
	if err := validateHTTPURL("https://10.42.0.1/plugin.zip", true); err != nil {
		t.Fatal("explicit HTTPS intranet archive was rejected", err)
	}
}

func TestCatalogHTTPRedirectDoesNotForwardCredentials(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("X-Credential") != ""
		w.Write([]byte("{}"))
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer origin.Close()
	var result bytes.Buffer
	if err := download(t.Context(), origin.URL, map[string]string{"X-Credential": "synthetic"}, false, &result, 100); err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Fatal("credentials forwarded across download hosts")
	}
}

func TestManagementLeaseAndReadOnlyValidation(t *testing.T) {
	manager, _, plugin := fixture(t)
	report, err := manager.Validate(plugin)
	if err != nil || report.Components["workflows"] != 1 || report.Components["agents"] != 1 {
		t.Fatalf("validation=%+v %v", report, err)
	}
	if _, err := os.Stat(manager.pluginDir); !os.IsNotExist(err) {
		t.Fatal("read-only validate created plugin store")
	}
	unlock, err := manager.lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := manager.lock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second writer=%v", err)
	}
}

func TestNPMSourceValidation(t *testing.T) {
	for _, value := range []string{"../path", "file:/tmp/x", "npm:other", "git+ssh://host/repo", "https://github.com/org/repo/releases/a.tgz", "-malicious"} {
		if err := validateNPMSource(Source{Kind: "npm", Package: value}); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	for _, value := range []string{"@acme/alpha", "alpha@^2.0.0", "alpha"} {
		if err := validateNPMSource(Source{Kind: "npm", Package: value}); err != nil {
			t.Fatalf("%q: %v", value, err)
		}
	}
	sum := sha256.Sum256([]byte("printf path"))
	if err := acceptedCommand("printf path", []string{hex.EncodeToString(sum[:])}); err != nil {
		t.Fatal(err)
	}
}

func TestManagementDoesNotOverwriteSettingsChangedByAnotherWriter(t *testing.T) {
	manager, _, _ := fixture(t)
	unlock, err := manager.lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	path, stale, err := manager.settings("user")
	if err != nil {
		t.Fatal(err)
	}
	newer := map[string]any{"theme": "edited-while-fetching"}
	data, _ := json.Marshal(newer)
	if err := fileutil.AtomicWrite(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.save(map[string]any{path: stale}); err == nil {
		t.Fatal("stale state overwrote another writer")
	}
	current, err := readObject(path)
	if err != nil || string(current["theme"]) != "\"edited-while-fetching\"" {
		t.Fatalf("new state lost: %v %v", current, err)
	}
}
