package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gokin/internal/fileutil"
	"gokin/internal/hooks"
	"gokin/internal/pluginpolicy"
	"gokin/internal/pluginsecrets"
	"gokin/internal/security"
)

func (m *Manager) findEntry(input string, known map[string]json.RawMessage) (string, Marketplace, Catalog, Entry, error) {
	name, market, qualified := strings.Cut(input, "@")
	if !ValidName(name) || qualified && !ValidName(market) {
		return "", Marketplace{}, Catalog{}, Entry{}, fmt.Errorf("invalid plugin identifier")
	}
	var matches []struct {
		id      string
		market  Marketplace
		catalog Catalog
		entry   Entry
	}
	names := make([]string, 0, len(known))
	for key := range known {
		names = append(names, key)
	}
	sort.Strings(names)
	for _, key := range names {
		if qualified && key != market {
			continue
		}
		var marketplace Marketplace
		if json.Unmarshal(known[key], &marketplace) != nil {
			return "", marketplace, Catalog{}, Entry{}, fmt.Errorf("invalid marketplace record")
		}
		catalog, err := m.readCatalog(key, marketplace)
		if err != nil {
			return "", marketplace, catalog, Entry{}, err
		}
		for _, entry := range catalog.Plugins {
			if entry.Name == name {
				matches = append(matches, struct {
					id      string
					market  Marketplace
					catalog Catalog
					entry   Entry
				}{name + "@" + key, marketplace, catalog, entry})
			}
		}
	}
	if len(matches) != 1 {
		var ids []string
		for _, match := range matches {
			ids = append(ids, match.id)
		}
		return "", Marketplace{}, Catalog{}, Entry{}, fmt.Errorf("plugin is missing or ambiguous; use name@marketplace (%s)", strings.Join(ids, ", "))
	}
	match := matches[0]
	return match.id, match.market, match.catalog, match.entry, nil
}

func (m *Manager) fetchPlugin(ctx context.Context, source Source, marketplace Marketplace, entry Entry, stage string, opts InstallOptions) (string, string, error) {
	switch source.Kind {
	case "relative":
		if marketplace.Source.Kind == "url" || marketplace.Source.Kind == "settings" {
			return "", "", fmt.Errorf("relative plugin sources need a directory or git marketplace")
		}
		root := marketplace.InstallLocation
		if marketplace.Source.Kind == "directory" {
			root = marketplace.Source.Path
		} else if marketplace.Source.Kind == "file" {
			root = filepath.Dir(filepath.Dir(marketplace.Source.Path))
		}
		path := filepath.Join(root, source.Path)
		checked, err := security.NewPathValidator([]string{root}, true).Validate(path)
		return checked, "", err
	case "github", "url", "git-subdir":
		return fetchGit(ctx, source, stage)
	case "archive":
		headers, err := headersFor(ctx, marketplace.Source, entry, opts.AcceptCommands)
		if err != nil {
			return "", "", err
		}
		archivePath := filepath.Join(stage, "plugin.zip")
		file, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return "", "", err
		}
		hash := sha256.New()
		err = download(ctx, source.URL, headers, true, io.MultiWriter(file, hash), maxDownloadBytes)
		file.Close()
		if err != nil {
			return "", "", err
		}
		if source.SHA256 != "" && !strings.EqualFold(source.SHA256, hex.EncodeToString(hash.Sum(nil))) {
			return "", "", fmt.Errorf("archive SHA-256 does not match")
		}
		dest := filepath.Join(stage, "archive-tree")
		os.Mkdir(dest, 0700)
		if err := extractZIP(archivePath, dest); err != nil {
			return "", "", err
		}
		root, err := findPluginRoot(dest)
		return root, "", err
	case "npm":
		root, err := fetchNPM(ctx, source, stage)
		return root, "", err
	case "command":
		output, err := runReviewedCommand(ctx, source.Command, source.Timeout, opts.AcceptCommands)
		if err != nil {
			return "", "", err
		}
		path := strings.TrimSuffix(strings.TrimSuffix(string(output), "\n"), "\r")
		if path == "" || strings.ContainsAny(path, "\r\n\x00") || !filepath.IsAbs(path) || strings.HasPrefix(path, `\\`) {
			return "", "", fmt.Errorf("install command must print one absolute local directory path")
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return "", "", err
		}
		relative, err := filepath.Rel(path, m.opts.WorkDir)
		if err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
			return "", "", fmt.Errorf("install command may not return the workspace or one of its parents")
		}
		if !pluginContent(path) {
			return "", "", fmt.Errorf("install command returned a directory without plugin content")
		}
		return path, "", nil
	default:
		return "", "", fmt.Errorf("unsupported plugin source")
	}
}

func (m *Manager) Install(ctx context.Context, input string, opts InstallOptions) (Result, error) {
	unlock, err := m.lock(ctx)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	return m.installGraph(ctx, input, opts)
}

func (m *Manager) installOne(ctx context.Context, input string, opts InstallOptions) (Result, error) {
	policy, err := m.policy()
	if err != nil {
		return Result{}, err
	}
	known, err := m.marketplaces()
	if err != nil {
		return Result{}, err
	}
	doc, installs, err := m.installs()
	if err != nil {
		return Result{}, err
	}
	if opts.Update {
		id, err := m.resolveID(input, installs)
		if err != nil {
			return Result{}, err
		}
		input = id
		opts.Scope, err = m.resolveScope(id, opts.Scope, installs)
		if err != nil {
			return Result{}, err
		}
	} else if opts.Scope == "" {
		opts.Scope = "user"
	}
	id, marketplace, catalog, entry, err := m.findEntry(input, known)
	if err != nil {
		return Result{}, err
	}
	if on, set := policy.Enabled[id]; set && !on {
		return Result{}, fmt.Errorf("plugin is disabled by managed policy")
	}
	scopePath, scopeSettings, err := m.settings(opts.Scope)
	if err != nil {
		return Result{}, err
	}
	enabled, err := rawEnabledEntries(scopeSettings)
	if err != nil {
		return Result{}, err
	}
	oldIndex := -1
	var old InstallRecord
	for i, raw := range installs[id] {
		var record InstallRecord
		if json.Unmarshal(raw, &record) != nil {
			return Result{}, fmt.Errorf("invalid install record")
		}
		if m.scopeMatches(record, opts.Scope) {
			oldIndex = i
			old = record
			break
		}
	}
	if opts.Update && oldIndex < 0 {
		return Result{}, fmt.Errorf("plugin is not installed at this scope")
	}
	if oldIndex >= 0 && !opts.Update && len(opts.Config) == 0 {
		if info, err := os.Stat(old.InstallPath); err != nil || !info.IsDir() {
			opts.Update = true
		}
	}
	if oldIndex >= 0 && !opts.Update && len(opts.Config) == 0 {
		promoted := false
		if old.AutoInstalled && !opts.dependency && !opts.KeepEnabledSettings {
			var fields map[string]json.RawMessage
			json.Unmarshal(installs[id][oldIndex], &fields)
			delete(fields, "autoInstalled")
			installs[id][oldIndex], _ = json.Marshal(fields)
			doc["plugins"], _ = json.Marshal(installs)
			if err := m.save(map[string]any{filepath.Join(m.pluginDir, "installed_plugins.json"): doc}); err != nil {
				return Result{}, err
			}
			promoted = true
		}
		return Result{Command: "install", Outcome: "ok", PluginID: id, Scope: opts.Scope, Changed: promoted, Message: "Plugin is already installed at this scope."}, nil
	}
	if len(installs) >= 64 && oldIndex < 0 {
		if _, exists := installs[id]; !exists {
			return Result{}, fmt.Errorf("installed registry exceeds 64 plugins")
		}
	}
	source, err := entrySource(entry, catalog.Metadata.PluginRoot)
	if err != nil {
		return Result{}, err
	}
	if policy.DisabledCommands && source.Kind == "command" {
		return Result{}, fmt.Errorf("command plugin sources are disabled by managed policy")
	}
	if err := policy.Check(marketplace.Source); err != nil {
		return Result{}, err
	}
	if opts.commandRefresh != nil {
		if oldIndex < 0 || !old.Managed || !sameCommandSource(source, *opts.commandRefresh) || !sameCommandSource(source, old.Source) || old.MarketplaceSource == nil || !sameSource(*old.MarketplaceSource, marketplace.Source) || acceptedCommand(source.Command, old.ApprovedCommands) != nil {
			return Result{}, fmt.Errorf("command or mode changed, or approval/provenance is unavailable; run an explicit plugin update: gokin plugin update %s --scope %s", id, opts.Scope)
		}
		plan, err := m.startupPlan(ctx)
		if err != nil {
			return Result{}, err
		}
		current, enabled, err := m.commandCandidate(id, plan)
		if err != nil {
			return Result{}, err
		}
		if !plan.commandRefreshAllowed() || !enabled || current.Scope != opts.Scope {
			return Result{}, errCommandRefreshSkipped
		}
	}
	if (opts.Background || opts.dependency) && (source.Kind == "command" && (opts.commandRefresh == nil || opts.dependency) || entry.HeadersHelper != "" || source.Kind == "archive" && marketplace.Source.HeadersHelper != "") {
		return Result{}, fmt.Errorf("plugin requires an explicit install/update because its source uses a command")
	}
	if oldIndex >= 0 && old.Managed && (source.Kind != "command" || sameCommandSource(source, old.Source)) {
		opts.AcceptCommands = append(opts.AcceptCommands, old.ApprovedCommands...)
	}
	if source.Kind == "command" {
		if err := acceptedCommand(source.Command, opts.AcceptCommands); err != nil {
			if approval, ok := err.(*CommandApprovalError); ok {
				approval.Mode = commandMode(source)
			}
			return Result{}, err
		}
	}
	if entry.HeadersHelper != "" && (entry.Strict == nil || *entry.Strict) {
		return Result{}, fmt.Errorf("archive headersHelper requires strict: false")
	}
	stage, err := os.MkdirTemp(filepath.Join(m.pluginDir, "gokin"), "install-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(stage)
	root, commit, resolvedVersion, err := m.fetchConstrainedPlugin(ctx, id, source, marketplace, entry, stage, opts)
	if err != nil {
		return Result{}, err
	}
	candidate := filepath.Join(stage, "candidate")
	linked := source.Kind == "command" && source.Mode == "link"
	commandVersion := ""
	var producerManifest map[string]json.RawMessage
	if source.Kind == "command" {
		var manifest map[string]json.RawMessage
		if linked {
			manifest, err = pluginpolicy.ReadLinkedManifest(root)
		} else {
			manifest, err = readObject(filepath.Join(root, ".claude-plugin", "plugin.json"))
		}
		if err != nil {
			return Result{}, err
		}
		producerManifest = manifest
		_ = json.Unmarshal(manifest["version"], &commandVersion)
	}
	var installedPath, linkedRoot, digest string
	var linkEntry json.RawMessage
	if linked {
		installedPath, linkedRoot, digest, err = m.publishLinkedTree(root, id)
		if err != nil {
			return Result{}, err
		}
		linkEntry, _ = json.Marshal(entry.Raw)
		// Generate only the effective manifest used for option validation.
		manifest := producerManifest
		if err := fileutil.EnsurePrivateDir(filepath.Join(candidate, ".claude-plugin")); err != nil {
			return Result{}, err
		}
		if len(manifest) > 0 {
			data, _ := json.Marshal(manifest)
			if err := fileutil.AtomicWrite(filepath.Join(candidate, ".claude-plugin", "plugin.json"), data, 0600); err != nil {
				return Result{}, err
			}
		}
	} else if _, err := copyTree(root, candidate); err != nil {
		return Result{}, err
	}
	if err := effectiveManifest(candidate, entry); err != nil {
		return Result{}, err
	}
	if err := ensureDeclaredOptions(candidate, opts.Config); err != nil {
		return Result{}, err
	}
	if err := rejectSensitiveArguments(candidate, opts.ArgumentConfig); err != nil {
		return Result{}, err
	}
	// Option values always live in user-owned settings, even for project/local
	// installs, so repository files can never provision plugin process secrets.
	userPath, userSettings, err := m.settings("user")
	if err != nil {
		return Result{}, err
	}
	if opts.Scope == "user" {
		userSettings = scopeSettings
	}
	if err := m.setOptionValues(userSettings, id, opts.Config, candidate); err != nil {
		return Result{}, err
	}
	linkRecord := InstallRecord{InstallPath: installedPath, Source: source, LinkedRoot: linkedRoot, LinkEntry: linkEntry}
	validation, err := m.validateInstall(candidate, id, userSettings, linkRecord)
	if err != nil {
		return Result{}, err
	}
	nodeWarning := ""
	if !linked {
		nodeWarning = installNodeDependencies(ctx, candidate, stage)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if nodeWarning != "" {
		validation.Warnings = append(validation.Warnings, nodeWarning)
	}
	if !linked {
		installedPath, digest, err = m.publishTree(candidate, "plugins", hooks.ClaudePluginStorageKey(id))
		if err != nil {
			return Result{}, err
		}
	}
	currentPolicy, err := m.policy()
	if err != nil {
		return Result{}, err
	}
	if err := currentPolicy.Check(marketplace.Source); err != nil {
		return Result{}, err
	}
	if currentPolicy.DisabledCommands && source.Kind == "command" {
		return Result{}, fmt.Errorf("command plugin sources were disabled during installation")
	}
	if on, set := currentPolicy.Enabled[id]; set && !on {
		return Result{}, fmt.Errorf("plugin was disabled during installation")
	}
	stamp := time.Now().UTC().Format(time.RFC3339)
	version := validation.Version
	if source.Kind == "command" {
		version = digest[:12]
		if commandVersion != "" {
			version = commandVersion + "-" + version
		}
	}
	if resolvedVersion != "" {
		version = resolvedVersion + "-" + commit[:12]
	}
	if version == "" {
		version = entry.Version
	}
	if version == "" {
		version = digest[:12]
	}
	record := InstallRecord{Scope: opts.Scope, InstallPath: installedPath, Version: version, InstalledAt: stamp, LastUpdated: stamp, GitCommitSHA: commit, Digest: digest, Source: source, Managed: true}
	record.LinkedRoot, record.LinkEntry = linkedRoot, linkEntry
	record.ResolvedVersion = resolvedVersion
	record.NodeWarning = nodeWarning
	record.AutoInstalled = oldIndex < 0 && opts.dependency || oldIndex >= 0 && old.AutoInstalled && (opts.dependency || opts.Update && !opts.promote)
	marketplaceSource := marketplace.Source
	record.MarketplaceSource = &marketplaceSource
	var reviewed []string
	if source.Kind == "command" {
		reviewed = append(reviewed, source.Command)
	}
	if source.Kind == "archive" {
		helper := entry.HeadersHelper
		if helper == "" {
			helper = marketplace.Source.HeadersHelper
		}
		if helper != "" {
			reviewed = append(reviewed, helper)
		}
	}
	for _, command := range reviewed {
		if acceptedCommand(command, opts.AcceptCommands) == nil {
			sum := sha256.Sum256([]byte(command))
			record.ApprovedCommands = append(record.ApprovedCommands, hex.EncodeToString(sum[:]))
		}
	}
	if opts.Scope != "user" {
		record.ProjectPath = m.opts.WorkDir
	}
	if oldIndex >= 0 {
		record.InstalledAt = old.InstalledAt
	}
	recordData, _ := json.Marshal(record)
	if oldIndex >= 0 {
		var previous map[string]json.RawMessage
		json.Unmarshal(installs[id][oldIndex], &previous)
		var next map[string]json.RawMessage
		json.Unmarshal(recordData, &next)
		for key, value := range next {
			previous[key] = value
		}
		if !linked {
			delete(previous, "gokinLinkedRoot")
			delete(previous, "gokinLinkEntry")
		}
		if commit == "" {
			delete(previous, "gitCommitSha")
		}
		if len(record.ApprovedCommands) == 0 {
			delete(previous, "gokinApprovedCommands")
		}
		if !record.AutoInstalled {
			delete(previous, "autoInstalled")
		}
		if resolvedVersion == "" {
			delete(previous, "gokinResolvedVersion")
		}
		if nodeWarning == "" {
			delete(previous, "gokinNodeWarning")
		}
		recordData, _ = json.Marshal(previous)
		installs[id][oldIndex] = recordData
	} else {
		installs[id] = append(installs[id], recordData)
	}
	if !opts.Update && !opts.KeepEnabledSettings {
		enabled[id] = json.RawMessage("true")
	}
	scopeSettings["enabledPlugins"], _ = json.Marshal(enabled)
	doc["plugins"], _ = json.Marshal(installs)
	if err := forgetRemoval(doc, id, opts.Scope, m.opts.WorkDir); err != nil {
		return Result{}, err
	}
	documents := map[string]any{filepath.Join(m.pluginDir, "installed_plugins.json"): doc}
	if !opts.KeepEnabledSettings {
		documents[scopePath] = scopeSettings
	}
	if len(opts.Config) > 0 || opts.Scope == "user" && !opts.KeepEnabledSettings {
		documents[userPath] = userSettings
	}
	if opts.Background {
		if err := m.verifySnapshot(); err != nil {
			return Result{}, err
		}
	}
	if err := m.save(documents); err != nil {
		return Result{}, err
	}
	command := "install"
	if opts.Update {
		command = "update"
	}
	changed := oldIndex < 0 || old.Digest != digest || old.Version != version || len(opts.Config) > 0
	message := fmt.Sprintf("Plugin %s is ready at %s scope. Use /reload-plugins in an existing session.", id, opts.Scope)
	if !changed {
		message = "Plugin is already at the latest content."
	}
	return Result{Command: command, Outcome: "ok", PluginID: id, Scope: opts.Scope, Changed: changed, Message: message, Warnings: validation.Warnings}, nil
}

func (m *Manager) SetEnabled(ctx context.Context, input, requestedScope string, on bool) (Result, error) {
	unlock, err := m.lock(ctx)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	_, installs, err := m.installs()
	if err != nil {
		return Result{}, err
	}
	id, err := m.resolveID(input, installs)
	if err != nil {
		return Result{}, err
	}
	if err := m.requireCurrentPluginID(id); err != nil {
		return Result{}, err
	}
	scope, err := m.resolveToggleScope(id, requestedScope, installs)
	if err != nil {
		return Result{}, err
	}
	policy, err := m.policy()
	if err != nil {
		return Result{}, err
	}
	if forced, set := policy.Enabled[id]; set && forced != on {
		return Result{}, fmt.Errorf("plugin state is fixed by managed policy")
	}
	if on {
		return m.enableGraph(ctx, id, scope)
	}
	if err := m.guardDependencyChange(id, scope, false); err != nil {
		return Result{}, err
	}
	path, settings, err := m.settings(scope)
	if err != nil {
		return Result{}, err
	}
	enabled, err := rawEnabledEntries(settings)
	if err != nil {
		return Result{}, err
	}
	var current *bool
	_ = json.Unmarshal(enabled[id], &current)
	changed := current == nil || *current != on
	enabled[id], _ = json.Marshal(on)
	settings["enabledPlugins"], _ = json.Marshal(enabled)
	if err := m.save(map[string]any{path: settings}); err != nil {
		return Result{}, err
	}
	command := "disable"
	if on {
		command = "enable"
	}
	return Result{Command: command, Outcome: "ok", PluginID: id, Scope: scope, Changed: changed, Message: fmt.Sprintf("Plugin %s: %s at %s scope.", id, command, scope)}, nil
}
func (m *Manager) Uninstall(ctx context.Context, input string, opts RemoveOptions) (Result, error) {
	unlock, err := m.lock(ctx)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	doc, installs, err := m.installs()
	if err != nil {
		return Result{}, err
	}
	id, err := m.resolveID(input, installs)
	if err != nil {
		return Result{}, err
	}
	if opts.Scope == "" {
		opts.Scope = "user"
	}
	if _, err := m.settingsPath(opts.Scope); err != nil {
		return Result{}, err
	}
	policy, err := m.policy()
	if err != nil {
		return Result{}, err
	}
	if policy.Enabled[id] {
		return Result{}, fmt.Errorf("plugin is required by managed policy")
	}
	var keep []json.RawMessage
	found := false
	for _, raw := range installs[id] {
		var record InstallRecord
		if json.Unmarshal(raw, &record) != nil {
			return Result{}, fmt.Errorf("invalid plugin record")
		}
		if m.scopeMatches(record, opts.Scope) {
			found = true
		} else {
			keep = append(keep, raw)
		}
	}
	if !found {
		return Result{}, fmt.Errorf("plugin is not installed at this scope")
	}
	if err := m.guardDependencyChange(id, opts.Scope, true); err != nil {
		return Result{}, err
	}
	path, settings, err := m.settings(opts.Scope)
	if err != nil {
		return Result{}, err
	}
	enabled, err := rawEnabledEntries(settings)
	if err != nil {
		return Result{}, err
	}
	delete(enabled, id)
	settings["enabledPlugins"], _ = json.Marshal(enabled)
	if len(keep) == 0 {
		delete(installs, id)
	} else {
		installs[id] = keep
	}
	doc["plugins"], _ = json.Marshal(installs)
	documents := map[string]any{path: settings, filepath.Join(m.pluginDir, "installed_plugins.json"): doc}
	if len(keep) == 0 {
		userPath, user, err := m.settings("user")
		if err != nil {
			return Result{}, err
		}
		if opts.Scope == "user" {
			user = settings
		}
		var configs map[string]json.RawMessage
		if raw, set := user["pluginConfigs"]; set {
			if json.Unmarshal(raw, &configs) != nil {
				return Result{}, fmt.Errorf("pluginConfigs must be an object")
			}
			delete(configs, id)
			user["pluginConfigs"], _ = json.Marshal(configs)
		}
		documents[userPath] = user
		secretPath := pluginsecrets.Path(m.opts.UserDir)
		fields, err := m.readObject(secretPath)
		if err != nil {
			return Result{}, err
		}
		if len(fields) > 0 {
			fields, err = pluginsecrets.Remove(id, fields)
			if err != nil {
				return Result{}, err
			}
			documents[secretPath] = fields
		}
	}
	if err := m.save(documents); err != nil {
		return Result{}, err
	}
	var warnings []string
	if len(keep) == 0 && !opts.KeepData {
		dataRoot := filepath.Join(m.pluginDir, "data")
		if _, err := os.Lstat(dataRoot); err == nil {
			if err := fileutil.EnsurePrivateDir(dataRoot); err != nil {
				warnings = append(warnings, "Plugin uninstalled; data retained because its directory is unsafe")
			} else {
				safeID := hooks.ClaudePluginStorageKey(id)
				if err := os.RemoveAll(filepath.Join(dataRoot, safeID)); err != nil {
					warnings = append(warnings, "Plugin uninstalled; data cleanup failed")
				}
			}
		}
	}
	// Immutable versions stay cached so existing sessions can retire their
	// references safely. Removing user-owned/imported install trees is avoided.
	return Result{Command: "uninstall", Outcome: "ok", PluginID: id, Scope: opts.Scope, Changed: true, Message: "Uninstalled " + id + " from " + opts.Scope + " scope.", Warnings: warnings}, nil
}

func (m *Manager) Configure(ctx context.Context, input string, values map[string]string) (Result, error) {
	unlock, err := m.lock(ctx)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	_, installs, err := m.installs()
	if err != nil {
		return Result{}, err
	}
	id, err := m.resolveID(input, installs)
	if err != nil {
		return Result{}, err
	}
	if err := m.requireCurrentPluginID(id); err != nil {
		return Result{}, err
	}
	scope, err := m.resolveScope(id, "", installs)
	if err != nil {
		return Result{}, err
	}
	var installedRecord InstallRecord
	for _, raw := range installs[id] {
		var record InstallRecord
		json.Unmarshal(raw, &record)
		if m.scopeMatches(record, scope) {
			installedRecord = record
			break
		}
	}
	root, cleanup, err := m.installedOptionRoot(installedRecord)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	if err := ensureDeclaredOptions(root, values); err != nil {
		return Result{}, err
	}
	path, settings, err := m.settings("user")
	if err != nil {
		return Result{}, err
	}
	transaction, err := NewManager(m.opts)
	if err != nil {
		return Result{}, err
	}
	transaction.transaction = map[string]map[string]json.RawMessage{}
	if err := transaction.setOptionValues(settings, id, values, root); err != nil {
		return Result{}, err
	}
	if _, err := transaction.validateInstall(root, id, settings, installedRecord); err != nil {
		return Result{}, err
	}
	documents := map[string]any{path: settings}
	for path, fields := range transaction.transaction {
		documents[path] = fields
	}
	if err := m.save(documents); err != nil {
		return Result{}, err
	}
	return Result{Command: "configure", Outcome: "ok", PluginID: id, Changed: len(values) > 0, Message: "Plugin options saved. Use /reload-plugins in an existing session."}, nil
}
