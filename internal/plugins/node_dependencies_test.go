package plugins

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func nodeFixture(t *testing.T, root string) (map[string]any, map[string]any) {
	t.Helper()
	pkg := map[string]any{"name": "test-plugin", "version": "1.0.0", "dependencies": map[string]string{"example": "^1.0.0"}, "scripts": map[string]string{"preinstall": "touch MUST_NOT_RUN"}}
	lock := map[string]any{"lockfileVersion": 3, "packages": map[string]any{"": map[string]any{"dependencies": pkg["dependencies"]}, "node_modules/example": map[string]any{"version": "1.0.0", "resolved": "https://registry.npmjs.org/example/-/example-1.0.0.tgz", "integrity": "sha512-test", "bin": map[string]string{"example": "cli.js"}}}}
	writeTestJSON(t, filepath.Join(root, "package.json"), pkg)
	writeTestJSON(t, filepath.Join(root, "package-lock.json"), lock)
	return pkg, lock
}

func TestNodePlanRejectsUnsafeInputs(t *testing.T) {
	for _, kind := range []string{"git", "folder", "workspace", "tarball", "mismatch", "unpinned", "link", "http", "credentials", "path", "bin", "overrides", "patch", "old", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			pkg, lock := nodeFixture(t, root)
			entry := lock["packages"].(map[string]any)["node_modules/example"].(map[string]any)
			switch kind {
			case "git":
				pkg["dependencies"] = map[string]string{"example": "github:acme/example"}
			case "folder":
				pkg["dependencies"] = map[string]string{"example": "file:../example"}
			case "workspace":
				pkg["dependencies"] = map[string]string{"example": "workspace:*"}
			case "tarball":
				pkg["dependencies"] = map[string]string{"example": "https://example.com/pkg.tgz"}
			case "mismatch":
				pkg["dependencies"] = map[string]string{"example": "^2.0.0"}
			case "unpinned":
				entry["version"] = "^1.0.0"
			case "link":
				entry["link"] = true
			case "http":
				entry["resolved"] = "http://evil.example/pkg.tgz"
			case "credentials":
				entry["resolved"] = "https://user:secret@example.com/pkg.tgz"
			case "path":
				lock["packages"].(map[string]any)["../escape"] = entry
			case "bin":
				entry["bin"] = map[string]string{"example": "../../outside"}
			case "overrides":
				pkg["overrides"] = map[string]string{"example": "2.0.0"}
			case "patch":
				pkg["patchedDependencies"] = map[string]string{"example": "patches/file.patch"}
			case "old":
				lock["lockfileVersion"] = 1
			}
			writeTestJSON(t, filepath.Join(root, "package.json"), pkg)
			writeTestJSON(t, filepath.Join(root, "package-lock.json"), lock)
			if kind == "duplicate" {
				os.WriteFile(filepath.Join(root, "package-lock.json"), []byte(`{"lockfileVersion":3,"lockfileVersion":1,"packages":{}}`), 0600)
			}
			if _, err := planNodeInstall(root, "https://registry.npmjs.org/"); err == nil {
				t.Fatal("unsafe install was accepted")
			}
		})
	}
}

func TestNodePlanSanitizesNPMAndHonorsLockPriority(t *testing.T) {
	root := t.TempDir()
	pkg, lock := nodeFixture(t, root)
	pkg["config"] = map[string]string{"secret": "value"}
	writeTestJSON(t, filepath.Join(root, "package.json"), pkg)
	plan, err := planNodeInstall(root, "https://registry.npmjs.org/")
	if err != nil || plan.Manager != "npm" || bytes.Contains(plan.Package, []byte("scripts")) || bytes.Contains(plan.Package, []byte("config")) {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	lock["lockfileVersion"] = 2
	writeTestJSON(t, filepath.Join(root, "npm-shrinkwrap.json"), lock)
	plan, err = planNodeInstall(root, "https://registry.npmjs.org/")
	if err != nil || plan.Lockfile != "npm-shrinkwrap.json" {
		t.Fatalf("shrinkwrap=%+v %v", plan, err)
	}
	os.WriteFile(filepath.Join(root, "bun.lock"), []byte(`{"lockfileVersion":3}`), 0600)
	if _, err := planNodeInstall(root, "https://registry.npmjs.org/"); err == nil {
		t.Fatal("fell back to npm after invalid Bun lock")
	}
}

func TestNodePlanBunJSONCAndRegistryOnly(t *testing.T) {
	root := t.TempDir()
	nodeFixture(t, root)
	valid := `{
	// a text Bun lockfile
	"lockfileVersion": 2,
	"workspaces": {"": {"dependencies": {"example": "^1.0.0",},},},
	"packages": {"example": ["example@1.0.0", "", {"bin":{"example":"cli.js"}}, "sha512-test",],},
}`
	for _, resolution := range []string{"example@1.0.0", "example@git+https://example.com/repo", "example@workspace:../x", "example@file:../x", "example@^1.0.0"} {
		os.WriteFile(filepath.Join(root, "bun.lock"), []byte(strings.ReplaceAll(valid, "example@1.0.0", resolution)), 0600)
		plan, err := planNodeInstall(root, "https://registry.npmjs.org/")
		if resolution == "example@1.0.0" {
			if err != nil || plan.Manager != "bun" || !json.Valid(plan.Lock) || bytes.Contains(plan.Lock, []byte("//")) {
				t.Fatalf("bun plan=%+v %v", plan, err)
			}
		} else if err == nil {
			t.Fatalf("accepted %s", resolution)
		}
	}
	if _, err := nodeJSON([]byte(`{"a":1,"a":2,}`), true); err == nil {
		t.Fatal("duplicate JSONC key accepted")
	}
	for _, value := range []string{`{"a":"// text /* test */,}"}`, `{"a":"https://example.com", /* a */ "b": [1,2,],}`} {
		if _, err := nodeJSON([]byte(value), true); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNodeHTTPRegistryExceptionIsExact(t *testing.T) {
	registry := "http://localhost:8080/registry/"
	for value, want := range map[string]bool{"http://localhost:8080/registry/pkg.tgz": true, "http://localhost:8080/other/pkg.tgz": false, "http://localhost:8081/registry/pkg.tgz": false, "http://localhost:8080.evil/registry/pkg.tgz": false, "https://example.com/pkg.tgz": true, "file:///pkg.tgz": false} {
		if got := checkedDownload(value, registry); got != want {
			t.Fatalf("%s: %v", value, got)
		}
	}
}

func TestUserNPMRegistryIgnoresProjectConfig(t *testing.T) {
	t.Setenv("NPM_CONFIG_REGISTRY", "")
	t.Setenv("npm_config_registry", "")
	userConfig := filepath.Join(t.TempDir(), "npmrc")
	t.Setenv("NPM_CONFIG_USERCONFIG", userConfig)
	t.Setenv("npm_config_userconfig", "")
	t.Setenv("GOKIN_TEST_REGISTRY_HOST", "localhost:8080")
	t.Setenv("GOKIN_TEST_UNSET_REGISTRY_PART", "")
	for _, content := range []string{
		"# registry=http://wrong.example/\nregistry = http://localhost:8080/registry/\n",
		"@scope:registry=http://wrong.example/\nregistry = \"http://${GOKIN_TEST_REGISTRY_HOST}/registry/\"\n",
		"registry='http://localhost:8080/registry/'\n[other]\nregistry=http://wrong.example/\n",
	} {
		os.WriteFile(userConfig, []byte(content), 0600)
		if got := userNPMRegistry(); got != "http://localhost:8080/registry/" {
			t.Fatalf("registry=%q", got)
		}
	}
	pluginDir := t.TempDir()
	os.WriteFile(filepath.Join(pluginDir, ".npmrc"), []byte("registry=http://plugin.example/"), 0600)
	t.Chdir(pluginDir)
	os.Remove(userConfig)
	if got := userNPMRegistry(); got != "" {
		t.Fatal("plugin .npmrc authorized a registry")
	}
	t.Setenv("npm_config_registry", "http://user-env.example/")
	if got := userNPMRegistry(); got != "http://user-env.example/" {
		t.Fatalf("user registry environment ignored: %q", got)
	}
}

func TestBunRunnerIsIsolatedAndFrozen(t *testing.T) {
	root := t.TempDir()
	nodeFixture(t, root)
	writeTestJSON(t, filepath.Join(root, "bun.lock"), map[string]any{"lockfileVersion": 1, "workspaces": map[string]any{"": map[string]any{"dependencies": map[string]string{"example": "^1.0.0"}}}, "packages": map[string]any{"example": []any{"example@1.0.0", "", map[string]any{}, "sha512-test"}}})
	os.WriteFile(filepath.Join(root, "bunfig.toml"), []byte("preload = './malicious.js'"), 0600)
	fakeNodeManager(t, "bun", `test "$1" = install || exit 2
test ! -e bunfig.toml || exit 3
test ! -e .env || exit 4
case " $* " in *" --ignore-scripts "*) ;; *) exit 5;; esac
case " $* " in *" --frozen-lockfile "*) ;; *) exit 6;; esac
mkdir -p node_modules/example
printf 'module.exports = 42' > node_modules/example/index.js
`)
	if warning := installNodeDependencies(t.Context(), root, t.TempDir()); warning != "" {
		t.Fatal(warning)
	}
	if _, err := os.Stat(filepath.Join(root, "node_modules", "example", "index.js")); err != nil {
		t.Fatal(err)
	}
}

func fakeNodeManager(t *testing.T, name, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestNodeLifecycleFailureAndRetry(t *testing.T) {
	manager, market, plugin := fixture(t)
	nodeFixture(t, plugin)
	fakeNodeManager(t, "npm", `test "$1" = ci || exit 2
test ! -e .npmrc || exit 3
test ! -e .env || exit 4
test ! -e bunfig.toml || exit 5
case " $* " in *" --ignore-scripts "*) ;; *) exit 6;; esac
echo 'credential=DO_NOT_PERSIST' >&2
exit 1
`)
	os.WriteFile(filepath.Join(plugin, ".npmrc"), []byte("registry=http://evil.example/"), 0600)
	os.WriteFile(filepath.Join(plugin, ".env"), []byte("TOKEN=secret"), 0600)
	if _, err := manager.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Install(t.Context(), "alpha", InstallOptions{})
	if err != nil || len(result.Warnings) == 0 {
		t.Fatalf("install=%+v %v", result, err)
	}
	rows, err := manager.List()
	if err != nil || len(rows) != 1 || !rows[0].Enabled || len(rows[0].Warnings) == 0 {
		t.Fatalf("list=%+v %v", rows, err)
	}
	old := rows[0].InstallPath
	if selected := runtimePlugins(t, manager); len(selected) != 1 {
		t.Fatal("package install failure disabled plugin")
	}
	state, _ := os.ReadFile(filepath.Join(manager.pluginDir, "installed_plugins.json"))
	if bytes.Contains(state, []byte("DO_NOT_PERSIST")) {
		t.Fatal("package manager stderr persisted")
	}
	// A manual update retries into a new immutable snapshot.
	fakeNodeManager(t, "npm", "mkdir -p node_modules/example\nprintf 'module.exports = 42' > node_modules/example/index.js\n")
	result, err = manager.Install(t.Context(), "alpha", InstallOptions{Update: true})
	if err != nil || !result.Changed {
		t.Fatalf("retry=%+v %v", result, err)
	}
	rows, err = manager.List()
	if err != nil || len(rows[0].Warnings) != 0 || rows[0].NodeWarning != "" || rows[0].InstallPath == old {
		t.Fatalf("retry list=%+v %v", rows, err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("old live snapshot removed")
	}
}

func TestNodeValidationAndDiscoveryNeverRunInstaller(t *testing.T) {
	manager, market, plugin := fixture(t)
	nodeFixture(t, plugin)
	marker := filepath.Join(t.TempDir(), "ran")
	fakeNodeManager(t, "npm", "touch '"+marker+"'\nexit 1\n")
	if _, err := manager.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Validate(plugin); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.List(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("read-only operation ran package manager")
	}
}

func TestNodeStartupRepairsMissingSnapshot(t *testing.T) {
	m, market, plugin := fixture(t)
	nodeFixture(t, plugin)
	fakeNodeManager(t, "npm", "mkdir -p node_modules/example\nprintf 'module.exports = 42' > node_modules/example/index.js\n")
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	rows, err := m.List()
	if err != nil || len(rows) != 1 {
		t.Fatalf("initial installation: %v", err)
	}
	if err := os.RemoveAll(rows[0].InstallPath); err != nil {
		t.Fatal(err)
	}
	report, err := m.SyncStartup(t.Context())
	if err != nil || len(report.Errors) != 0 {
		t.Fatalf("startup=%+v %v", report, err)
	}
	rows, err = m.List()
	if err != nil || len(rows) != 1 || !rows[0].Enabled || len(rows[0].Warnings) != 0 {
		t.Fatalf("repaired=%+v %v", rows, err)
	}
	if _, err := os.Stat(filepath.Join(rows[0].InstallPath, "node_modules", "example", "index.js")); err != nil {
		t.Fatal("startup did not restore Node dependencies")
	}
}

func TestNodeCancellationDoesNotPublish(t *testing.T) {
	manager, market, plugin := fixture(t)
	nodeFixture(t, plugin)
	fakeNodeManager(t, "npm", "sleep 30\n")
	if _, err := manager.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, err := manager.Install(ctx, "alpha", InstallOptions{}); err == nil {
		t.Fatal("canceled installation published")
	}
	if rows, err := manager.List(); err != nil || len(rows) != 0 {
		t.Fatalf("rows=%+v %v", rows, err)
	}
}

func TestNodeRealNPMFrozenInstallWithoutScripts(t *testing.T) {
	for _, lockfile := range []string{"package-lock.json", "npm-shrinkwrap.json"} {
		for _, version := range []int{2, 3} {
			t.Run(fmt.Sprintf("%s-v%d", lockfile, version), func(t *testing.T) { testRealNodeInstall(t, "npm", false, lockfile, version) })
		}
	}
}

func TestNodeRealBunFrozenInstallWithoutScripts(t *testing.T) {
	for _, version := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) { testRealNodeInstall(t, "bun", false, "bun.lock", version) })
	}
}

func TestNodeRealBunWithoutNPM(t *testing.T) {
	testRealNodeInstall(t, "bun", true, "bun.lock", 2)
}

func testRealNodeInstall(t *testing.T, manager string, withoutNPM bool, lockfile string, lockVersion int) {
	t.Helper()
	if _, err := exec.LookPath(manager); err != nil {
		t.Skip(manager + " unavailable")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Unix script and bin fixture")
	}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	files := map[string]string{"package/package.json": `{"name":"example","version":"1.0.0","bin":{"example":"cli.js"},"scripts":{"preinstall":"touch MUST_NOT_RUN"}}`, "package/cli.js": "#!/usr/bin/env node\nconsole.log(require('./value.js'))\n", "package/value.js": "module.exports = 42\n"}
	for name, content := range files {
		mode := int64(0644)
		if strings.HasSuffix(name, "cli.js") {
			mode = 0755
		}
		tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content))})
		tw.Write([]byte(content))
	}
	tw.Close()
	gz.Close()
	sum := sha512.Sum512(archive.Bytes())
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/example" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"name": "example", "dist-tags": map[string]string{"latest": "1.0.0"}, "versions": map[string]any{"1.0.0": map[string]any{"name": "example", "version": "1.0.0", "bin": map[string]string{"example": "cli.js"}, "dist": map[string]string{"tarball": "http://" + r.Host + "/example/-/example-1.0.0.tgz", "integrity": integrity}}}})
			return
		}
		if r.URL.Path != "/example/-/example-1.0.0.tgz" {
			http.NotFound(w, r)
			return
		}
		w.Write(archive.Bytes())
	}))
	defer server.Close()
	userConfig := filepath.Join(t.TempDir(), "npmrc")
	os.WriteFile(userConfig, []byte("registry="+server.URL+"/\n"), 0600)
	t.Setenv("NPM_CONFIG_USERCONFIG", userConfig)
	t.Setenv("npm_config_userconfig", userConfig)
	t.Setenv("NPM_CONFIG_REGISTRY", "")
	t.Setenv("npm_config_registry", "")
	root, stage := t.TempDir(), t.TempDir()
	pkg, lock := nodeFixture(t, root)
	lock["lockfileVersion"] = lockVersion
	entry := lock["packages"].(map[string]any)["node_modules/example"].(map[string]any)
	entry["resolved"] = server.URL + "/example/-/example-1.0.0.tgz"
	entry["integrity"] = integrity
	writeTestJSON(t, filepath.Join(root, "package.json"), pkg)
	writeTestJSON(t, filepath.Join(root, "package-lock.json"), lock)
	if lockfile == "npm-shrinkwrap.json" {
		if err := os.Rename(filepath.Join(root, "package-lock.json"), filepath.Join(root, lockfile)); err != nil {
			t.Fatal(err)
		}
	}
	if manager == "bun" {
		// Generate a real lock rather than teaching the fixture an assumed Bun
		// format. Only this trusted test setup sees the local registry config.
		os.Remove(filepath.Join(root, "package-lock.json"))
		os.WriteFile(filepath.Join(root, ".npmrc"), []byte("registry="+server.URL+"/\n"), 0600)
		generateCtx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		_, err := runCommand(generateCtx, root, "bun", "install", "--lockfile-only", "--ignore-scripts", "--cache-dir", filepath.Join(stage, "generate-cache"))
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(root, "bun.lock"))
		if err != nil {
			t.Fatal(err)
		}
		fields, err := nodeJSON(data, true)
		if err != nil {
			t.Fatal(err)
		}
		fields["lockfileVersion"], _ = json.Marshal(lockVersion)
		writeTestJSON(t, filepath.Join(root, "bun.lock"), fields)
	}
	os.WriteFile(filepath.Join(root, ".npmrc"), []byte("registry=http://invalid.example/\n"), 0600)
	if withoutNPM {
		bin := t.TempDir()
		for _, name := range []string{"bun", "node"} {
			path, err := exec.LookPath(name)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path, filepath.Join(bin, name)); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
		if _, err := exec.LookPath("npm"); err == nil {
			t.Skip("system npm is present on the isolated PATH")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if warning := installNodeDependencies(ctx, root, stage); warning != "" {
		t.Fatal(warning)
	}
	copy := filepath.Join(t.TempDir(), "published")
	if _, err := copyTree(root, copy); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(filepath.Join(copy, "node_modules", ".bin", "example")).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "42" {
		t.Fatalf("bin=%s %v", output, err)
	}
	for _, marker := range []string{filepath.Join(root, "MUST_NOT_RUN"), filepath.Join(root, "node_modules", "example", "MUST_NOT_RUN")} {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("lifecycle script ran")
		}
	}
}

func TestNodePlanWithoutPackageJSONIsSilent(t *testing.T) {
	root := t.TempDir()
	plan, err := planNodeInstall(root, "https://registry.npmjs.org/")
	if err != nil || plan != nil {
		t.Fatalf("absent package.json=%+v %v", plan, err)
	}
	if warning := installNodeDependencies(t.Context(), root, t.TempDir()); warning != "" {
		t.Fatalf("non-Node plugin warning=%q", warning)
	}
}
