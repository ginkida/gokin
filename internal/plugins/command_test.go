package plugins

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runPluginCLI(t *testing.T, manager *Manager, args ...string) (string, error) {
	t.Helper()
	var output bytes.Buffer
	cmd := NewCommand(func() (Options, error) { return manager.opts, nil })
	cmd.SetArgs(args)
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetContext(t.Context())
	err := cmd.Execute()
	return output.String(), err
}
func TestPluginCLIJSONWithoutProviderAndOptionConfiguration(t *testing.T) {
	manager, market, _ := fixture(t)
	output, err := runPluginCLI(t, manager, "marketplace", "add", market, "--json")
	var result Result
	if err != nil || json.Unmarshal([]byte(output), &result) != nil || result.Outcome != "ok" {
		t.Fatalf("add=%q %v", output, err)
	}
	output, err = runPluginCLI(t, manager, "install", "alpha", "--config", "value=from-cli", "--json")
	if err != nil || json.Unmarshal([]byte(output), &result) != nil || result.PluginID != "alpha@acme" {
		t.Fatalf("install=%q %v", output, err)
	}
	output, err = runPluginCLI(t, manager, "list", "--available", "--json")
	var listed struct {
		Installed []Installed
		Available []Available
	}
	if err != nil || json.Unmarshal([]byte(output), &listed) != nil || len(listed.Installed) != 1 || len(listed.Available) != 0 {
		t.Fatalf("list=%q %v", output, err)
	}
	if strings.Contains(output, "from-cli") || strings.Contains(output, "gokinSource") {
		t.Fatal("list disclosed saved configuration or installer internals", output)
	}
	output, err = runPluginCLI(t, manager, "validate", listed.Installed[0].InstallPath, "--json")
	if err != nil {
		t.Fatalf("validate=%q %v", output, err)
	}
	if _, err = runPluginCLI(t, manager, "enable", "alpha", "--scope", "bad"); err == nil {
		t.Fatal("invalid scope accepted")
	}
}

func TestNPMFetchUsesActualClientWithoutInstallScripts(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm is not installed")
	}
	manager, market, _ := fixture(t)
	marker := filepath.Join(t.TempDir(), "npm-script")
	files := map[string]string{
		"package/package.json":               `{"name":"@gokin-fixture/alpha","version":"1.0.0","scripts":{"preinstall":"touch NEVER","prepare":"touch NEVER","postinstall":"touch NEVER"}}`,
		"package/.claude-plugin/plugin.json": `{"name":"alpha","version":"1.0.0"}`,
		"package/skills/review/SKILL.md":     "---\ndescription: npm workflow\n---\nReview sources.\n",
	}
	var packed bytes.Buffer
	gzipWriter := gzip.NewWriter(&packed)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, text := range files {
		tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(text))})
		tarWriter.Write([]byte(text))
	}
	tarWriter.Close()
	gzipWriter.Close()
	sum := sha512.Sum512(packed.Bytes())
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".tgz") {
			w.Write(packed.Bytes())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"name": "@gokin-fixture/alpha", "dist-tags": map[string]string{"latest": "1.0.0"}, "versions": map[string]any{"1.0.0": map[string]any{"name": "@gokin-fixture/alpha", "version": "1.0.0", "dist": map[string]string{"tarball": server.URL + "/alpha.tgz", "integrity": "sha512-" + base64.StdEncoding.EncodeToString(sum[:])}, "scripts": map[string]string{"prepare": "touch " + marker, "postinstall": "touch " + marker}}}})
	}))
	defer server.Close()
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Acme"}, "plugins": []any{map[string]any{"name": "alpha", "source": map[string]any{"source": "npm", "package": "@gokin-fixture/alpha", "registry": server.URL, "version": "1.0.0"}}}})
	if _, err := manager.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	rows, err := manager.List()
	if err != nil || len(rows) != 1 || rows[0].Version != "1.0.0" {
		t.Fatalf("npm install=%+v %v", rows, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("npm install script ran")
	}
	if _, err := os.Stat(filepath.Join(rows[0].InstallPath, "NEVER")); !os.IsNotExist(err) {
		t.Fatal("pack/install scripts ran")
	}
}
