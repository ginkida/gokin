package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gokin/internal/fileutil"
	"gokin/internal/hooks"
	"gokin/internal/plugindeps"
	"gokin/internal/pluginpolicy"
	"gokin/internal/pluginsecrets"
)

var errBusy = errors.New("plugin management is busy")

type Options struct {
	UserDir       string
	WorkDir       string
	ManagedDir    string
	Trusted       bool
	SecretKeyring pluginsecrets.Keyring
}
type Manager struct {
	opts             Options
	pluginDir        string
	cacheDir         string
	expectedMu       sync.RWMutex
	expected         map[string]string
	transaction      map[string]map[string]json.RawMessage
	commandMu        sync.Mutex
	commandRefreshed map[string]bool
}

func NewManager(opts Options) (*Manager, error) {
	var err error
	if opts.UserDir == "" {
		opts.UserDir, err = hooks.ClaudeUserSettingsDir()
		if err != nil {
			return nil, err
		}
	}
	opts.UserDir, err = filepath.Abs(opts.UserDir)
	if err != nil {
		return nil, err
	}
	if path, err := filepath.EvalSymlinks(opts.UserDir); err == nil {
		opts.UserDir = path
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if opts.WorkDir == "" {
		opts.WorkDir, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	opts.WorkDir, err = filepath.Abs(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	opts.WorkDir, err = filepath.EvalSymlinks(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	if opts.ManagedDir == "" {
		opts.ManagedDir = hooks.ClaudeManagedSettingsDir()
	}
	root := filepath.Join(opts.UserDir, "plugins")
	return &Manager{opts: opts, pluginDir: root, cacheDir: filepath.Join(root, "gokin", "cache")}, nil
}
func (m *Manager) policy() (pluginpolicy.Policy, error) {
	aliases, err := m.pluginAliases()
	if err != nil {
		return pluginpolicy.Policy{}, err
	}
	documents, err := pluginpolicy.ReadManagedDocuments(m.opts.ManagedDir)
	if err != nil {
		return pluginpolicy.Policy{}, err
	}
	documents, err = aliases.EnabledDocuments(documents)
	if err != nil {
		return pluginpolicy.Policy{}, err
	}
	return pluginpolicy.FromDocuments(documents)
}

func (m *Manager) lock(ctx context.Context) (func(), error) {
	for _, dir := range []string{m.opts.UserDir, m.pluginDir, filepath.Join(m.pluginDir, "gokin"), m.cacheDir} {
		if err := fileutil.EnsurePrivateDir(dir); err != nil {
			return nil, err
		}
	}
	file, err := fileutil.OpenPrivateReadWrite(filepath.Join(m.pluginDir, "gokin", "management.lock"))
	if err != nil {
		return nil, err
	}
	for {
		err = lockFile(file)
		if err == nil {
			if err := m.snapshotState(); err != nil {
				unlockFile(file)
				file.Close()
				return nil, err
			}
			return func() { unlockFile(file); file.Close() }, nil
		}
		if !errors.Is(err, errBusy) {
			file.Close()
			return nil, err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func stateToken(path string) (string, error) {
	data, err := fileutil.ReadResolvedRegularFile(path, 4<<20)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return target + ":" + hex.EncodeToString(sum[:]), nil
}
func (m *Manager) snapshotState() error {
	paths := []string{filepath.Join(m.pluginDir, "installed_plugins.json"), filepath.Join(m.pluginDir, "known_marketplaces.json")}
	paths = append(paths, pluginsecrets.Path(m.opts.UserDir))
	for _, scope := range []string{"user", "project", "local"} {
		path, _ := m.settingsPath(scope)
		paths = append(paths, path)
	}
	next := map[string]string{}
	for _, path := range paths {
		token, err := stateToken(path)
		if err != nil {
			return err
		}
		next[path] = token
	}
	m.expectedMu.Lock()
	m.expected = next
	m.expectedMu.Unlock()
	return nil
}
func readObject(path string) (map[string]json.RawMessage, error) {
	data, err := fileutil.ReadResolvedRegularFile(path, 4<<20)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	if err := pluginpolicy.ValidateJSON(data); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return nil, fmt.Errorf("%s must be a JSON object", filepath.Base(path))
	}
	return object, nil
}
func (m *Manager) marketplaces() (map[string]json.RawMessage, error) {
	return m.readObject(filepath.Join(m.pluginDir, "known_marketplaces.json"))
}
func (m *Manager) readObject(path string) (map[string]json.RawMessage, error) {
	if fields, exists := m.transaction[path]; exists {
		data, _ := json.Marshal(fields)
		var cloned map[string]json.RawMessage
		json.Unmarshal(data, &cloned)
		return cloned, nil
	}
	return readObject(path)
}
func (m *Manager) installs() (map[string]json.RawMessage, map[string][]json.RawMessage, error) {
	doc, err := m.readObject(filepath.Join(m.pluginDir, "installed_plugins.json"))
	if err != nil {
		return nil, nil, err
	}
	if len(doc) == 0 {
		doc["version"] = json.RawMessage("2")
		doc["plugins"] = json.RawMessage("{}")
	}
	var version int
	if json.Unmarshal(doc["version"], &version) != nil || version != 2 {
		return nil, nil, fmt.Errorf("installed plugin registry must have version 2")
	}
	var entries map[string][]json.RawMessage
	if json.Unmarshal(doc["plugins"], &entries) != nil || entries == nil || len(entries) > 64 {
		return nil, nil, fmt.Errorf("invalid installed plugin registry")
	}
	for id, records := range entries {
		if !ValidID(id) || len(records) > 256 {
			return nil, nil, fmt.Errorf("invalid installed plugin record")
		}
	}
	return doc, entries, nil
}
func (m *Manager) settingsPath(scope string) (string, error) {
	switch scope {
	case "user":
		return filepath.Join(m.opts.UserDir, "settings.json"), nil
	case "project":
		return filepath.Join(m.opts.WorkDir, ".claude", "settings.json"), nil
	case "local":
		return filepath.Join(m.opts.WorkDir, ".claude", "settings.local.json"), nil
	default:
		return "", fmt.Errorf("scope must be user, project or local")
	}
}
func enabledEntries(doc map[string]json.RawMessage) (map[string]bool, error) {
	var entries map[string]bool
	if raw, set := doc["enabledPlugins"]; set {
		if json.Unmarshal(raw, &entries) != nil {
			return nil, fmt.Errorf("enabledPlugins must be a boolean map")
		}
	}
	if entries == nil {
		entries = map[string]bool{}
	}
	return entries, nil
}
func (m *Manager) settings(scope string) (string, map[string]json.RawMessage, error) {
	path, err := m.settingsPath(scope)
	if err != nil {
		return "", nil, err
	}
	doc, err := m.readObject(path)
	return path, doc, err
}

func (m *Manager) verifySnapshot() error {
	m.expectedMu.RLock()
	defer m.expectedMu.RUnlock()
	for path, original := range m.expected {
		current, err := stateToken(path)
		if err != nil {
			return err
		}
		if current != original {
			return fmt.Errorf("plugin settings changed during operation; retry")
		}
	}
	return nil
}
func (m *Manager) save(docs map[string]any) error {
	if m.transaction != nil {
		for path, value := range docs {
			data, err := json.Marshal(value)
			if err != nil {
				return err
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(data, &fields) != nil || fields == nil {
				return fmt.Errorf("transaction state must be an object")
			}
			m.transaction[path] = fields
		}
		return nil
	}
	type change struct {
		path           string
		data, previous []byte
		existed        bool
		mode           os.FileMode
	}
	var changes []change
	m.expectedMu.RLock()
	expected := make(map[string]string, len(m.expected))
	for path, token := range m.expected {
		expected[path] = token
	}
	m.expectedMu.RUnlock()
	for path := range docs {
		if original, tracked := expected[path]; tracked {
			current, err := stateToken(path)
			if err != nil {
				return err
			}
			if current != original {
				return fmt.Errorf("plugin state changed during management; retry without overwriting %s", filepath.Base(path))
			}
		}
	}
	paths := make([]string, 0, len(docs))
	for path := range docs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	seen := map[string]bool{}
	for _, original := range paths {
		path := original
		if err := fileutil.EnsurePrivateDir(filepath.Dir(path)); err != nil {
			return err
		}
		if _, err := os.Lstat(path); err == nil {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			path = resolved
		}
		if seen[path] {
			return fmt.Errorf("plugin transaction targets the same file twice")
		}
		seen[path] = true
		data, err := json.MarshalIndent(docs[original], "", "  ")
		if err != nil {
			return err
		}
		if len(data) > 4<<20 {
			return fmt.Errorf("plugin management state exceeds byte limit")
		}
		item := change{path: path, data: append(data, '\n'), mode: 0600}
		if info, err := os.Stat(path); err == nil {
			item.existed = true
			item.mode = info.Mode().Perm()
			item.previous, err = fileutil.ReadRegularFile(path, 4<<20)
			if err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		changes = append(changes, item)
	}
	var applied []change
	for _, item := range changes {
		err := fileutil.AtomicWrite(item.path, item.data, 0600)
		if err == nil {
			applied = append(applied, item)
			continue
		}
		if errors.Is(err, fileutil.ErrAtomicWriteCommitUncertain) {
			applied = append(applied, item)
		}
		var rollbackErrors []error
		for i := len(applied) - 1; i >= 0; i-- {
			old := applied[i]
			current, readErr := fileutil.ReadRegularFile(old.path, 4<<20)
			if readErr != nil || !bytes.Equal(current, old.data) {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("state changed during rollback; retained %s", old.path))
				continue
			}
			var restoreErr error
			if old.existed {
				restoreErr = fileutil.AtomicWrite(old.path, old.previous, old.mode)
			} else {
				restoreErr = os.Remove(old.path)
			}
			if restoreErr != nil {
				rollbackErrors = append(rollbackErrors, restoreErr)
			}
		}
		return errors.Join(append([]error{err}, rollbackErrors...)...)
	}
	return nil
}

func (m *Manager) scopeMatches(record InstallRecord, scope string) bool {
	return record.Scope == scope && (scope == "user" || samePath(record.ProjectPath, m.opts.WorkDir))
}
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if path, err := filepath.EvalSymlinks(a); err == nil {
		a = path
	}
	if path, err := filepath.EvalSymlinks(b); err == nil {
		b = path
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
func (m *Manager) resolveID(input string, entries map[string][]json.RawMessage) (string, error) {
	if ValidID(input) {
		if _, ok := entries[input]; !ok {
			return "", fmt.Errorf("plugin %s is not installed", input)
		}
		return input, nil
	}
	if !ValidName(input) {
		return "", fmt.Errorf("invalid plugin name")
	}
	var matches []string
	for id, records := range entries {
		if strings.SplitN(id, "@", 2)[0] != input {
			continue
		}
		for _, raw := range records {
			var record InstallRecord
			if json.Unmarshal(raw, &record) == nil && (record.Scope == "user" || samePath(record.ProjectPath, m.opts.WorkDir)) {
				matches = append(matches, id)
				break
			}
		}
	}
	sort.Strings(matches)
	if len(matches) != 1 {
		return "", fmt.Errorf("plugin name is missing or ambiguous; use name@marketplace (%s)", strings.Join(matches, ", "))
	}
	return matches[0], nil
}
func (m *Manager) resolveScope(id, requested string, entries map[string][]json.RawMessage) (string, error) {
	if requested != "" {
		if _, err := m.settingsPath(requested); err != nil {
			return "", err
		}
		return requested, nil
	}
	for _, scope := range []string{"local", "project", "user"} {
		for _, raw := range entries[id] {
			var record InstallRecord
			if json.Unmarshal(raw, &record) == nil && m.scopeMatches(record, scope) {
				return scope, nil
			}
		}
	}
	return "", fmt.Errorf("plugin is not installed for this project")
}

func (m *Manager) resolveToggleScope(id, requested string, entries map[string][]json.RawMessage) (string, error) {
	declared, err := m.resolveScope(id, "", entries)
	if err != nil {
		return "", err
	}
	for _, scope := range []string{"local", "project", "user"} {
		_, doc, err := m.settings(scope)
		if err != nil {
			return "", err
		}
		enabled, err := enabledEntries(doc)
		if err != nil {
			return "", err
		}
		if _, exists := enabled[id]; exists {
			declared = scope
			break
		}
	}
	if requested == "" {
		return declared, nil
	}
	if _, err := m.settingsPath(requested); err != nil {
		return "", err
	}
	rank := map[string]int{"user": 1, "project": 2, "local": 3}
	if rank[requested] < rank[declared] {
		return "", fmt.Errorf("plugin is declared at %s scope; use that scope or a more specific one", declared)
	}
	return requested, nil
}

func (m *Manager) List() ([]Installed, error) {
	doc, entries, err := m.installs()
	if err != nil {
		return nil, err
	}
	aliases, err := pluginpolicy.ReadPluginAliases(doc)
	if err != nil {
		return nil, err
	}
	policy, err := m.policy()
	if err != nil {
		return nil, err
	}
	known, err := m.marketplaces()
	if err != nil {
		return nil, err
	}
	merged := map[string]bool{}
	for _, scope := range []string{"user", "project", "local"} {
		if scope != "user" && !m.opts.Trusted {
			continue
		}
		_, doc, err := m.settings(scope)
		if err != nil {
			return nil, err
		}
		enabled, err := enabledEntries(doc)
		if err != nil {
			return nil, err
		}
		for id, on := range enabled {
			merged[id] = on
		}
	}
	merged = aliases.RenameFlags(merged)
	for id, on := range policy.Enabled {
		merged[id] = on
	}
	rows := []Installed{}
	for id, records := range entries {
		for _, raw := range records {
			var record InstallRecord
			if json.Unmarshal(raw, &record) != nil {
				return nil, fmt.Errorf("invalid plugin install record")
			}
			if record.Scope != "user" && !samePath(record.ProjectPath, m.opts.WorkDir) {
				continue
			}
			on, set := merged[id]
			if !set {
				on = true
				if manifest, err := m.installedManifest(record); err == nil {
					if raw, exists := manifest["defaultEnabled"]; exists {
						if json.Unmarshal(raw, &on) != nil {
							on = false
						}
					}
				}
			}
			row := Installed{ID: id, InstallRecord: record, Enabled: on}
			if target, changed, _ := aliases.Resolve(id); changed {
				row.Enabled = false
				if target == "" {
					row.Status = "removed-from-marketplace"
					row.Warnings = append(row.Warnings, "Removed from marketplace")
				} else {
					row.Status = "renamed"
					row.RenamedTo = target
					row.Warnings = append(row.Warnings, "Renamed to "+target)
				}
				rows = append(rows, row)
				continue
			}
			if on && !(record.Source.Kind == "command" && record.Source.Mode == "link") {
				if warning := nodeRuntimeWarning(record.InstallPath, record.NodeWarning); warning != "" {
					row.Warnings = append(row.Warnings, warning)
				}
			}
			name := strings.SplitN(id, "@", 2)[1]
			required, requiredSet := policy.Required[name]
			required = pluginpolicy.ResolveLocalSource(required, m.opts.WorkDir)
			if requiredSet && policy.Enabled[id] && (record.MarketplaceSource == nil || !sameSource(*record.MarketplaceSource, required)) {
				row.Enabled = false
				row.Errors = append(row.Errors, "managed marketplace source requires plugin reinstallation")
			}
			if policy.RestrictsMarketplaces() {
				name := strings.SplitN(id, "@", 2)[1]
				var market Marketplace
				json.Unmarshal(known[name], &market)
				source := market.Source
				if source.Kind == "" {
					source = policy.Required[name]
				}
				if source.Kind == "" {
					row.Enabled = false
					row.Errors = append(row.Errors, "marketplace provenance is unavailable under managed policy")
				} else if err := policy.Check(source); err != nil {
					row.Enabled = false
					row.Errors = append(row.Errors, err.Error())
				}
				if record.MarketplaceSource != nil {
					if err := policy.Check(*record.MarketplaceSource); err != nil {
						row.Enabled = false
						row.Errors = append(row.Errors, "installed marketplace source: "+err.Error())
					}
				}
			}
			if policy.DisabledCommands && record.Source.Kind == "command" {
				row.Enabled = false
				row.Errors = append(row.Errors, "command plugin sources are disabled by managed policy")
			}
			row.Source.Headers = nil
			row.Source.HeadersHelper = ""
			if record.Source.Kind == "command" && record.Source.Mode == "link" && pluginpolicy.PathWithin(record.LinkedRoot, m.opts.WorkDir) {
				row.Enabled = false
				row.Errors = append(row.Errors, "linked plugin cannot load from within its own directory")
			}
			if record.Scope != "user" && !m.opts.Trusted {
				row.Enabled = false
				row.Errors = append(row.Errors, "workspace trust is required")
			}
			if _, err := os.Stat(record.InstallPath); err != nil {
				row.Enabled = false
				row.Errors = append(row.Errors, "plugin install directory is unavailable")
			}
			rows = append(rows, row)
		}
	}
	graph := map[string]plugindeps.Node{}
	ranks := map[string]int{}
	for i := range rows {
		row := &rows[i]
		if row.Status != "" {
			continue
		}
		rank := map[string]int{"user": 1, "project": 2, "local": 3}[row.Scope]
		if rank > 1 && !m.opts.Trusted {
			continue
		}
		manifest, err := m.installedManifest(row.InstallRecord)
		if err != nil {
			row.Enabled = false
			row.Errors = append(row.Errors, err.Error())
			continue
		}
		deps, err := plugindeps.Parse(manifest["dependencies"])
		if err != nil {
			row.Enabled = false
			row.Errors = append(row.Errors, err.Error())
			continue
		}
		for i := range deps {
			deps[i].Name, deps[i].Marketplace = renamedDependency(aliases, row.ID, deps[i].Name, deps[i].Marketplace)
		}
		var version, name string
		json.Unmarshal(manifest["version"], &version)
		json.Unmarshal(manifest["name"], &name)
		if row.ResolvedVersion != "" {
			version = row.ResolvedVersion
		}
		if rank > ranks[row.ID] {
			graph[row.ID] = plugindeps.Node{ID: row.ID, Name: name, Version: version, Enabled: row.Enabled, Dependencies: deps}
			ranks[row.ID] = rank
		}
	}
	issues := plugindeps.Check(graph)
	for i := range rows {
		if issue := issues[rows[i].ID]; issue != "" {
			rows[i].Enabled = false
			rows[i].Errors = append(rows[i].Errors, issue)
		}
	}
	notices, err := removalNotices(doc)
	if err != nil {
		return nil, err
	}
	for _, notice := range notices {
		if notice.Scope != "user" && !samePath(notice.ProjectPath, m.opts.WorkDir) {
			continue
		}
		rows = append(rows, Installed{ID: notice.ID, InstallRecord: InstallRecord{Scope: notice.Scope, ProjectPath: notice.ProjectPath}, Status: "removed-from-marketplace", Warnings: []string{"Removed from marketplace"}})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ID == rows[j].ID {
			return rows[i].Scope < rows[j].Scope
		}
		return rows[i].ID < rows[j].ID
	})
	return rows, nil
}
func (m *Manager) Available() ([]Available, error) {
	known, err := m.marketplaces()
	if err != nil {
		return nil, err
	}
	installed, err := m.List()
	if err != nil {
		return nil, err
	}
	present := map[string]bool{}
	for _, row := range installed {
		if row.Status == "" {
			present[row.ID] = true
		}
	}
	policy, err := m.policy()
	if err != nil {
		return nil, err
	}
	rows := []Available{}
	for name, raw := range known {
		var marketplace Marketplace
		if json.Unmarshal(raw, &marketplace) != nil {
			return nil, fmt.Errorf("invalid marketplace record")
		}
		if policy.Check(marketplace.Source) != nil {
			continue
		}
		catalog, err := m.readCatalog(name, marketplace)
		if err != nil {
			return nil, err
		}
		for _, entry := range catalog.Plugins {
			id := entry.Name + "@" + name
			if present[id] {
				continue
			}
			if on, set := policy.Enabled[id]; set && !on {
				continue
			}
			rows = append(rows, Available{PluginID: entry.Name + "@" + name, MarketplaceName: name, Name: entry.Name, Description: entry.Description, Version: entry.Version})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].PluginID < rows[j].PluginID })
	return rows, nil
}
