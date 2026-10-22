package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"gokin/internal/fileutil"
	"gokin/internal/pluginpolicy"
)

// Plans contain only checked dependency data. Plugin config, scripts and
// package-manager directives never reach the install directory.
type nodeInstallPlan struct {
	Manager, Lockfile string
	Package, Lock     []byte
}

var npmName = regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)
var npmTag = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._-]*$`)
var dependencyFields = []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies"}
var npmEnvReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(\?)?\}`)

// Read only user-selected configuration. A Bun-only machine must not need an
// npm executable to authorize its own registry, and plugin .npmrc files must
// never widen the HTTPS exception.
func userNPMRegistry() string {
	for _, key := range []string{"npm_config_registry", "NPM_CONFIG_REGISTRY"} {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	path := os.Getenv("npm_config_userconfig")
	if path == "" {
		path = os.Getenv("NPM_CONFIG_USERCONFIG")
	}
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		path = filepath.Join(home, ".npmrc")
	}
	data, err := fileutil.ReadResolvedRegularFile(path, 64<<10)
	if err != nil {
		return ""
	}
	registry := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			break
		} // registry inside an INI section is not the default.
		key, value, found := strings.Cut(line, "=")
		if !found || strings.Trim(strings.TrimSpace(key), `"'`) != "registry" {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, `"`) {
			var quoted string
			if json.Unmarshal([]byte(value), &quoted) != nil {
				return ""
			}
			value = quoted
		} else if strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") {
			value = strings.Trim(value, "'")
		}
		registry = npmEnvReference.ReplaceAllStringFunc(value, func(ref string) string {
			match := npmEnvReference.FindStringSubmatch(ref)
			if value, found := os.LookupEnv(match[1]); found {
				return value
			}
			if match[2] == "?" {
				return ""
			}
			return ref
		})
	}
	return registry
}

func registrySpecs(fields map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	result := map[string]json.RawMessage{}
	for _, field := range dependencyFields {
		raw, exists := fields[field]
		if !exists {
			continue
		}
		var deps map[string]string
		if json.Unmarshal(raw, &deps) != nil || deps == nil || len(deps) > 4096 {
			return nil, fmt.Errorf("invalid %s", field)
		}
		for name, spec := range deps {
			if !npmName.MatchString(name) || len(name) > 214 || len(spec) > 512 {
				return nil, fmt.Errorf("invalid registry dependency")
			}
			selector := spec
			if strings.HasPrefix(selector, "npm:") {
				alias := strings.TrimPrefix(selector, "npm:")
				index := strings.LastIndex(alias, "@")
				if index <= 0 || !npmName.MatchString(alias[:index]) {
					return nil, fmt.Errorf("invalid npm registry alias")
				}
				selector = alias[index+1:]
			}
			if _, err := semver.NewConstraint(selector); err != nil && !npmTag.MatchString(selector) {
				return nil, fmt.Errorf("non-registry dependency in %s", field)
			}
		}
		if len(deps) > 0 {
			result[field], _ = json.Marshal(deps)
		}
	}
	return result, nil
}

func checkedDownload(value, registry string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	r, err := url.Parse(registry)
	return err == nil && u.Scheme == "http" && r.Scheme == "http" && strings.EqualFold(u.Host, r.Host) && strings.HasPrefix(u.Path, strings.TrimRight(r.Path, "/")+"/")
}

func checkedBins(raw json.RawMessage) error {
	var bins map[string]string
	var single string
	if json.Unmarshal(raw, &single) == nil {
		bins = map[string]string{"bin": single}
	} else if json.Unmarshal(raw, &bins) != nil || bins == nil {
		return fmt.Errorf("invalid package bin paths")
	}
	for name, path := range bins {
		if !npmName.MatchString(name) {
			return fmt.Errorf("invalid bin name")
		}
		if _, err := safeRelative(path, false); err != nil {
			return fmt.Errorf("package bin path escapes its package")
		}
	}
	return nil
}

func checkedPackageFields(fields map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	result, err := registrySpecs(fields)
	if err != nil {
		return nil, err
	}
	if raw, exists := fields["bin"]; exists {
		if err := checkedBins(raw); err != nil {
			return nil, err
		}
		result["bin"] = raw
	}
	// These affect platform filtering and peer installation, without executing
	// code or changing the download source.
	for _, field := range []string{"os", "cpu", "libc", "engines", "peerDependenciesMeta", "dev", "optional", "devOptional", "peer", "inBundle", "hasInstallScript"} {
		if raw, exists := fields[field]; exists {
			result[field] = raw
		}
	}
	return result, nil
}

func npmLockPlan(lock map[string]json.RawMessage, pkg map[string]json.RawMessage, registry string) ([]byte, error) {
	var version int
	if json.Unmarshal(lock["lockfileVersion"], &version) != nil || version != 2 && version != 3 {
		return nil, fmt.Errorf("npm lockfileVersion must be 2 or 3")
	}
	var packages map[string]map[string]json.RawMessage
	if json.Unmarshal(lock["packages"], &packages) != nil || packages == nil || len(packages) > 20000 {
		return nil, fmt.Errorf("invalid npm packages map")
	}
	root, exists := packages[""]
	if !exists {
		return nil, fmt.Errorf("npm lockfile has no root package")
	}
	rootSpecs, err := registrySpecs(root)
	if err != nil {
		return nil, err
	}
	pkgSpecs, err := registrySpecs(pkg)
	if err != nil || !reflect.DeepEqual(rootSpecs, pkgSpecs) {
		return nil, fmt.Errorf("package.json and lockfile dependencies differ")
	}
	for path, entry := range packages {
		if path == "" {
			packages[path] = pkg
			continue
		}
		parts := strings.Split(path, "/node_modules/")
		parts[0] = strings.TrimPrefix(parts[0], "node_modules/")
		if !strings.HasPrefix(path, "node_modules/") {
			return nil, fmt.Errorf("npm package path is outside node_modules")
		}
		for _, name := range parts {
			if !npmName.MatchString(name) {
				return nil, fmt.Errorf("invalid npm package path")
			}
		}
		var pinned, resolved string
		var linked bool
		json.Unmarshal(entry["link"], &linked)
		if linked || json.Unmarshal(entry["version"], &pinned) != nil {
			return nil, fmt.Errorf("linked or unpinned npm dependency")
		}
		if _, err := semver.StrictNewVersion(pinned); err != nil {
			return nil, fmt.Errorf("npm dependencies must pin exact versions")
		}
		json.Unmarshal(entry["resolved"], &resolved)
		var bundled bool
		json.Unmarshal(entry["inBundle"], &bundled)
		if resolved == "" && !bundled || resolved != "" && !checkedDownload(resolved, registry) {
			return nil, fmt.Errorf("npm dependency download must use HTTPS or the user's default registry")
		}
		clean, err := checkedPackageFields(entry)
		if err != nil {
			return nil, err
		}
		for _, field := range []string{"name", "version", "resolved", "integrity"} {
			if raw, exists := entry[field]; exists {
				clean[field] = raw
			}
		}
		packages[path] = clean
	}
	return json.Marshal(map[string]any{"name": "gokin-plugin-install", "version": "1.0.0", "lockfileVersion": version, "requires": true, "packages": packages})
}

// Bun text locks are JSON with comments and trailing commas. Strip these in a
// string-aware pass, then use the same duplicate-key rejection as state files.
func nodeJSON(data []byte, jsonc bool) (map[string]json.RawMessage, error) {
	if jsonc {
		var out []byte
		quoted, escaped := false, false
		for i := 0; i < len(data); i++ {
			c := data[i]
			if quoted {
				out = append(out, c)
				if escaped {
					escaped = false
				} else if c == '\\' {
					escaped = true
				} else if c == '"' {
					quoted = false
				}
				continue
			}
			if c == '"' {
				quoted = true
				out = append(out, c)
				continue
			}
			if c == '/' && i+1 < len(data) && data[i+1] == '/' {
				for i < len(data) && data[i] != '\n' {
					i++
				}
				out = append(out, '\n')
				continue
			}
			if c == '/' && i+1 < len(data) && data[i+1] == '*' {
				i += 2
				for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
					i++
				}
				if i+1 >= len(data) {
					return nil, fmt.Errorf("unterminated lockfile comment")
				}
				i++
				out = append(out, ' ')
				continue
			}
			out = append(out, c)
		}
		data = out
		out = nil
		quoted, escaped = false, false
		for i, c := range data {
			if !quoted && c == ',' {
				j := i + 1
				for j < len(data) && strings.ContainsRune(" \t\r\n", rune(data[j])) {
					j++
				}
				if j < len(data) && (data[j] == '}' || data[j] == ']') {
					continue
				}
			}
			out = append(out, c)
			if quoted {
				if escaped {
					escaped = false
				} else if c == '\\' {
					escaped = true
				} else if c == '"' {
					quoted = false
				}
			} else if c == '"' {
				quoted = true
			}
		}
		data = out
	}
	if err := pluginpolicy.ValidateJSON(data); err != nil {
		return nil, fmt.Errorf("invalid package/lockfile JSON")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return nil, fmt.Errorf("package/lockfile must be an object")
	}
	return fields, nil
}

func bunLockPlan(lock map[string]json.RawMessage, pkg map[string]json.RawMessage, registry string) ([]byte, error) {
	var version int
	if json.Unmarshal(lock["lockfileVersion"], &version) != nil || version < 0 || version > 2 {
		return nil, fmt.Errorf("unsupported Bun lockfileVersion")
	}
	for _, field := range []string{"overrides", "patchedDependencies", "catalog", "catalogs"} {
		if _, set := lock[field]; set {
			return nil, fmt.Errorf("Bun overrides, patches and catalogs are unsupported")
		}
	}
	var workspaces map[string]map[string]json.RawMessage
	if json.Unmarshal(lock["workspaces"], &workspaces) != nil || len(workspaces) != 1 || workspaces[""] == nil {
		return nil, fmt.Errorf("Bun workspace dependencies are unsupported")
	}
	rootSpecs, err := registrySpecs(workspaces[""])
	if err != nil {
		return nil, err
	}
	pkgSpecs, err := registrySpecs(pkg)
	if err != nil || !reflect.DeepEqual(rootSpecs, pkgSpecs) {
		return nil, fmt.Errorf("package.json and lockfile dependencies differ")
	}
	var packages map[string][]json.RawMessage
	if json.Unmarshal(lock["packages"], &packages) != nil || packages == nil || len(packages) > 20000 {
		return nil, fmt.Errorf("invalid Bun packages map")
	}
	for key, entry := range packages {
		if _, err := safeRelative(key, false); err != nil || len(entry) != 4 {
			return nil, fmt.Errorf("non-registry Bun dependency")
		}
		var resolution, download, integrity string
		if json.Unmarshal(entry[0], &resolution) != nil || json.Unmarshal(entry[1], &download) != nil || json.Unmarshal(entry[3], &integrity) != nil {
			return nil, fmt.Errorf("invalid Bun package resolution")
		}
		index := strings.LastIndex(resolution, "@")
		if index <= 0 || !npmName.MatchString(resolution[:index]) {
			return nil, fmt.Errorf("non-registry Bun dependency")
		}
		if _, err := semver.StrictNewVersion(resolution[index+1:]); err != nil {
			return nil, fmt.Errorf("Bun dependencies must pin exact versions")
		}
		if download != "" && !checkedDownload(download, registry) {
			return nil, fmt.Errorf("Bun registry must use HTTPS or the user's default registry")
		}
		var info map[string]json.RawMessage
		if json.Unmarshal(entry[2], &info) != nil || info == nil {
			return nil, fmt.Errorf("invalid Bun package metadata")
		}
		if _, set := info["binDir"]; set {
			return nil, fmt.Errorf("Bun binDir is unsupported")
		}
		clean, err := checkedPackageFields(info)
		if err != nil {
			return nil, err
		}
		entry[2], _ = json.Marshal(clean)
	}
	return json.Marshal(map[string]any{"lockfileVersion": version, "configVersion": 0, "workspaces": map[string]any{"": pkg}, "packages": packages})
}

func planNodeInstall(root, registry string) (*nodeInstallPlan, error) {
	data, err := fileutil.ReadRegularFile(filepath.Join(root, "package.json"), 4<<20)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	pkg, err := nodeJSON(data, false)
	if err != nil {
		return nil, err
	}
	lockName := ""
	for _, name := range []string{"bun.lock", "npm-shrinkwrap.json", "package-lock.json"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			lockName = name
			break
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	if lockName == "" {
		return nil, nil
	}
	for _, field := range []string{"overrides", "patchedDependencies", "workspaces", "resolutions", "bundleDependencies", "bundledDependencies"} {
		if _, set := pkg[field]; set {
			return nil, fmt.Errorf("package overrides, patches, workspaces and bundled dependencies are unsupported")
		}
	}
	clean, err := registrySpecs(pkg)
	if err != nil {
		return nil, err
	}
	clean["name"] = json.RawMessage(`"gokin-plugin-install"`)
	clean["version"] = json.RawMessage(`"1.0.0"`)
	if meta, set := pkg["peerDependenciesMeta"]; set {
		clean["peerDependenciesMeta"] = meta
	}
	data, err = fileutil.ReadRegularFile(filepath.Join(root, lockName), 4<<20)
	if err != nil {
		return nil, err
	}
	lock, err := nodeJSON(data, lockName == "bun.lock")
	if err != nil {
		return nil, err
	}
	plan := &nodeInstallPlan{Manager: "npm", Lockfile: lockName}
	plan.Package, _ = json.Marshal(clean)
	if lockName == "bun.lock" {
		plan.Manager = "bun"
		plan.Lock, err = bunLockPlan(lock, clean, registry)
	} else {
		plan.Lock, err = npmLockPlan(lock, clean, registry)
	}
	return plan, err
}

func nodeRuntimeWarning(root, saved string) string {
	if info, err := os.Stat(filepath.Join(root, "node_modules")); err == nil && info.IsDir() {
		return ""
	}
	data, err := fileutil.ReadRegularFile(filepath.Join(root, "package.json"), 4<<20)
	if err != nil {
		return ""
	}
	pkg, err := nodeJSON(data, false)
	if err != nil {
		return ""
	}
	runtime := false
	for _, field := range []string{"dependencies", "optionalDependencies"} {
		var deps map[string]json.RawMessage
		if json.Unmarshal(pkg[field], &deps) == nil && len(deps) > 0 {
			runtime = true
		}
	}
	if !runtime {
		return ""
	}
	locked := false
	for _, name := range []string{"bun.lock", "npm-shrinkwrap.json", "package-lock.json", "bun.lockb", "yarn.lock", "pnpm-lock.yaml"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			locked = true
			break
		}
	}
	if !locked {
		return ""
	}
	if saved == "" {
		saved = "Node.js dependencies are missing; use plugin update to retry with a supported lockfile"
	}
	return saved
}

func installNodeDependencies(ctx context.Context, candidate, stage string) string {
	if info, err := os.Stat(filepath.Join(candidate, "node_modules")); err == nil && info.IsDir() {
		return ""
	}
	// Skip absent declarations without probing or running a package manager.
	plan, err := planNodeInstall(candidate, "https://registry.npmjs.org/")
	if plan == nil && err == nil {
		return nodeRuntimeWarning(candidate, "")
	}
	installDir, mkdirErr := os.MkdirTemp(stage, "node-install-")
	if mkdirErr != nil {
		return "Node.js dependency install failed: cannot create install directory"
	}
	defer os.RemoveAll(installDir)
	// Only the user's registry config can authorize HTTP downloads. Never ask
	// npm in the plugin directory, where a plugin-controlled .npmrc could lie.
	if err != nil && strings.Contains(err.Error(), "default registry") {
		if registry := userNPMRegistry(); registry != "" {
			plan, err = planNodeInstall(candidate, registry)
		}
	}
	if err != nil && strings.Contains(err.Error(), "default registry") {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		output, probeErr := runCommand(probeCtx, installDir, "npm", "config", "get", "registry")
		cancel()
		if probeErr == nil {
			plan, err = planNodeInstall(candidate, strings.TrimSpace(string(output)))
		}
	}
	if err != nil {
		return "Node.js dependency install skipped: " + err.Error()
	}
	for name, data := range map[string][]byte{"package.json": plan.Package, plan.Lockfile: plan.Lock} {
		if err := os.WriteFile(filepath.Join(installDir, name), data, 0600); err != nil {
			return "Node.js dependency install failed: cannot stage dependency data"
		}
	}
	args := []string{"ci", "--ignore-scripts", "--no-audit", "--no-fund", "--cache", filepath.Join(installDir, ".cache")}
	if plan.Manager == "bun" {
		args = []string{"install", "--frozen-lockfile", "--ignore-scripts", "--no-progress", "--cache-dir", filepath.Join(installDir, ".cache")}
	}
	installCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	_, err = runCommand(installCtx, installDir, plan.Manager, args...)
	cancel()
	if err != nil {
		// Package manager output may echo registry credentials or arbitrary
		// downloaded metadata. Persist only a fixed, actionable diagnostic.
		return "Node.js dependency install failed (" + plan.Manager + "); check package manager availability, network and lockfile, then run plugin update"
	}
	modules := filepath.Join(installDir, "node_modules")
	if info, err := os.Lstat(modules); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "Node.js dependency install failed: package manager produced no regular node_modules directory"
	}
	// Normalize contained bin symlinks and enforce the plugin tree budget
	// before exposing any generated files in a published snapshot.
	if _, err := copyTree(modules, filepath.Join(candidate, "node_modules")); err != nil {
		os.RemoveAll(filepath.Join(candidate, "node_modules"))
		return "Node.js dependency install failed: generated files exceed limits or contain unsafe links"
	}
	budget := treeBudget{}
	if err := filepath.WalkDir(candidate, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == candidate {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(candidate, path)
		if err != nil {
			return err
		}
		size := info.Size()
		if info.IsDir() {
			size = 0
		}
		return budget.entry(filepath.ToSlash(relative), size)
	}); err != nil {
		os.RemoveAll(filepath.Join(candidate, "node_modules"))
		return "Node.js dependency install failed: plugin and dependencies together exceed file limits"
	}
	return ""
}
