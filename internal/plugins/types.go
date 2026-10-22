// Package plugins implements explicit plugin lifecycle operations. Reading
// catalogs and runtime discovery never downloads code or executes installers.
package plugins

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"gokin/internal/hooks"
	"gokin/internal/plugindeps"
	"gokin/internal/pluginpolicy"
)

type Source = pluginpolicy.Source

// Compare structured sources without treating JSON whitespace/key order in an
// inline catalog as a new executable source. URL, repo, ref and path stay exact.
func sameSource(a, b Source) bool {
	return pluginpolicy.EqualSource(a, b)
}

type Entry struct {
	Name          string                     `json:"name"`
	Description   string                     `json:"description,omitempty"`
	Version       string                     `json:"version,omitempty"`
	Source        json.RawMessage            `json:"source"`
	Strict        *bool                      `json:"strict,omitempty"`
	Headers       map[string]string          `json:"headers,omitempty"`
	HeadersHelper string                     `json:"headersHelper,omitempty"`
	Raw           map[string]json.RawMessage `json:"-"`
}
type Catalog struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Owner       struct {
		Name string `json:"name"`
	} `json:"owner"`
	Plugins  []Entry `json:"plugins"`
	Metadata struct {
		PluginRoot string `json:"pluginRoot"`
	} `json:"metadata,omitempty"`
	ForceRemoveDeletedPlugins           bool               `json:"forceRemoveDeletedPlugins,omitempty"`
	AllowCrossMarketplaceDependenciesOn []string           `json:"allowCrossMarketplaceDependenciesOn,omitempty"`
	Renames                             map[string]*string `json:"renames,omitempty"`
}
type Marketplace struct {
	Source          Source `json:"source"`
	InstallLocation string `json:"installLocation"`
	LastUpdated     string `json:"lastUpdated,omitempty"`
	AutoUpdate      *bool  `json:"autoUpdate,omitempty"`
}
type InstallRecord struct {
	Scope             string          `json:"scope"`
	InstallPath       string          `json:"installPath"`
	Version           string          `json:"version,omitempty"`
	InstalledAt       string          `json:"installedAt,omitempty"`
	LastUpdated       string          `json:"lastUpdated,omitempty"`
	ProjectPath       string          `json:"projectPath,omitempty"`
	GitCommitSHA      string          `json:"gitCommitSha,omitempty"`
	Digest            string          `json:"gokinDigest,omitempty"`
	Source            Source          `json:"gokinSource,omitempty"`
	Managed           bool            `json:"gokinManaged,omitempty"`
	ApprovedCommands  []string        `json:"gokinApprovedCommands,omitempty"`
	MarketplaceSource *Source         `json:"gokinMarketplaceSource,omitempty"`
	AutoInstalled     bool            `json:"autoInstalled,omitempty"`
	ResolvedVersion   string          `json:"gokinResolvedVersion,omitempty"`
	NodeWarning       string          `json:"gokinNodeWarning,omitempty"`
	LinkedRoot        string          `json:"gokinLinkedRoot,omitempty"`
	LinkEntry         json.RawMessage `json:"gokinLinkEntry,omitempty"`
}
type Installed struct {
	ID        string `json:"id"`
	Status    string `json:"status,omitempty"`
	RenamedTo string `json:"renamedTo,omitempty"`
	InstallRecord
	Enabled                bool             `json:"enabled"`
	Errors                 []string         `json:"errors,omitempty"`
	Warnings               []string         `json:"warnings,omitempty"`
	GokinSource            *Source          `json:"gokinSource,omitempty"`
	GokinMarketplaceSource *Source          `json:"gokinMarketplaceSource,omitempty"`
	GokinLinkEntry         *json.RawMessage `json:"gokinLinkEntry,omitempty"`
}
type Available struct {
	PluginID        string `json:"pluginId"`
	MarketplaceName string `json:"marketplaceName"`
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	Version         string `json:"version,omitempty"`
}
type Result struct {
	Command      string        `json:"command"`
	Outcome      string        `json:"outcome"`
	Message      string        `json:"message"`
	PluginID     string        `json:"pluginId,omitempty"`
	Marketplace  string        `json:"marketplace,omitempty"`
	Scope        string        `json:"scope,omitempty"`
	Changed      bool          `json:"changed,omitempty"`
	Warnings     []string      `json:"warnings,omitempty"`
	ShownCommand *ShownCommand `json:"shownCommand,omitempty"`
	Dependencies []string      `json:"dependencies,omitempty"`
	Pruned       []string      `json:"pruned,omitempty"`
	DryRun       bool          `json:"dryRun,omitempty"`
}
type ShownCommand struct {
	Command string `json:"command"`
	SHA256  string `json:"sha256"`
	Mode    string `json:"mode,omitempty"`
}
type CommandApprovalError struct{ ShownCommand }

func (e *CommandApprovalError) Error() string {
	mode := ""
	if e.Mode != "" {
		mode = " (" + e.Mode + " mode)"
	}
	return fmt.Sprintf("command was not run%s: %s\nReview it, then repeat with --accept-command %s", mode, e.Command, e.SHA256)
}

type InstallOptions struct {
	Scope          string
	Config         map[string]string
	ArgumentConfig []string // CLI keys supplied visibly in argv; never allow secrets here.
	AcceptCommands []string
	Update         bool
	// Startup operations preserve the settings which declared the plugin and
	// skip install/helper commands except a checked command-source lifecycle
	// refresh of the exact previously accepted source and mode.
	KeepEnabledSettings bool
	Background          bool
	dependency          bool
	versionRanges       []string
	promote             bool
	repairOnly          bool
	commandRefresh      *Source // Internal lifecycle authority; never supplied by the CLI/model.
}
type RemoveOptions struct {
	Scope    string
	KeepData bool
}
type Validation struct {
	Name       string         `json:"name"`
	Version    string         `json:"version,omitempty"`
	Warnings   []string       `json:"warnings,omitempty"`
	Components map[string]int `json:"components"`
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var hexSHA = regexp.MustCompile(`^(?:[a-fA-F0-9]{40}|[a-fA-F0-9]{64})$`)
var githubRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func ValidName(name string) bool {
	return namePattern.MatchString(name) && !strings.Contains(name, "..")
}
func ValidID(id string) bool {
	return len(id) <= 256 && hooks.ValidClaudePluginID(id)
}
func safeRelative(value string, allowDot bool) (string, error) {
	if value == "." && allowDot {
		return ".", nil
	}
	value = strings.TrimPrefix(value, "./")
	if value == "" || filepath.IsAbs(value) || strings.ContainsAny(value, "\\\x00:") {
		return "", fmt.Errorf("source path must stay inside its root")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == "" {
			return "", fmt.Errorf("source path contains traversal or empty segments")
		}
	}
	return filepath.FromSlash(value), nil
}

func ParseCatalog(data []byte) (Catalog, error) {
	var catalog Catalog
	if len(data) > 4<<20 {
		return catalog, fmt.Errorf("marketplace exceeds 4 MiB")
	}
	if err := pluginpolicy.ValidateJSON(data); err != nil {
		return catalog, err
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		return catalog, err
	}
	if !ValidName(catalog.Name) || strings.TrimSpace(catalog.Owner.Name) == "" || catalog.Plugins == nil || len(catalog.Plugins) > 4096 {
		return catalog, fmt.Errorf("marketplace requires a valid name, owner and bounded plugins array")
	}
	seen := map[string]bool{}
	for i := range catalog.Plugins {
		entry := &catalog.Plugins[i]
		if !ValidName(entry.Name) || seen[entry.Name] || len(entry.Source) == 0 {
			return catalog, fmt.Errorf("invalid or duplicate marketplace plugin name")
		}
		seen[entry.Name] = true
	}
	var raw struct {
		Plugins []map[string]json.RawMessage `json:"plugins"`
	}
	json.Unmarshal(data, &raw)
	for i := range catalog.Plugins {
		catalog.Plugins[i].Raw = raw.Plugins[i]
	}
	if catalog.Metadata.PluginRoot != "" {
		if _, err := safeRelative(catalog.Metadata.PluginRoot, true); err != nil {
			return catalog, err
		}
	}
	if len(catalog.AllowCrossMarketplaceDependenciesOn) > 64 {
		return catalog, fmt.Errorf("dependency marketplace allowlist exceeds 64 entries")
	}
	for _, name := range catalog.AllowCrossMarketplaceDependenciesOn {
		if !ValidName(name) {
			return catalog, fmt.Errorf("invalid dependency marketplace name")
		}
	}
	for _, entry := range catalog.Plugins {
		if _, err := plugindeps.Parse(entry.Raw["dependencies"]); err != nil {
			return catalog, fmt.Errorf("plugin %s: %w", entry.Name, err)
		}
	}
	if len(catalog.Renames) > 4096 {
		return catalog, fmt.Errorf("marketplace renames exceed count limit")
	}
	for name := range catalog.Renames {
		if !ValidName(name) || seen[name] {
			return catalog, fmt.Errorf("invalid marketplace rename %q", name)
		}
		visited := map[string]bool{}
		current := name
		for {
			if visited[current] {
				return catalog, fmt.Errorf("renames.%s: chain does not resolve", name)
			}
			visited[current] = true
			next, exists := catalog.Renames[current]
			if !exists {
				if !seen[current] {
					return catalog, fmt.Errorf("renames.%s: chain does not resolve", name)
				}
				break
			}
			if next == nil {
				break
			}
			if !ValidName(*next) {
				return catalog, fmt.Errorf("renames.%s: chain does not resolve", name)
			}
			current = *next
		}
	}
	return catalog, nil
}

func ParseMarketplaceSource(value, workDir string) (Source, error) {
	if strings.HasPrefix(strings.TrimSpace(value), "{") {
		var source Source
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&source); err != nil {
			return source, err
		}
		return normalizeMarketplaceSource(source, workDir)
	}
	if strings.HasPrefix(value, "git@") || strings.HasPrefix(value, "ssh://") || strings.HasPrefix(value, "file://") {
		raw, ref, _ := strings.Cut(value, "#")
		return normalizeMarketplaceSource(Source{Kind: "git", URL: raw, Ref: ref}, workDir)
	}
	if strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://") {
		raw, ref, _ := strings.Cut(value, "#")
		source := Source{Kind: "url", URL: raw}
		if strings.HasSuffix(raw, ".git") || strings.Contains(raw, "/_git/") || strings.HasPrefix(raw, "https://github.com/") || strings.HasPrefix(raw, "https://gitlab.com/") {
			source.Kind = "git"
			source.Ref = ref
		} else if ref != "" {
			return source, fmt.Errorf("marketplace JSON URL cannot have a fragment")
		}
		return normalizeMarketplaceSource(source, workDir)
	}
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}
	if info, err := os.Stat(path); err == nil {
		kind := "file"
		if info.IsDir() {
			kind = "directory"
		}
		return normalizeMarketplaceSource(Source{Kind: kind, Path: path}, workDir)
	}
	repo, ref, _ := strings.Cut(value, "#")
	if strings.Contains(repo, "@") {
		repo, ref, _ = strings.Cut(repo, "@")
	}
	if githubRepo.MatchString(repo) {
		return normalizeMarketplaceSource(Source{Kind: "github", Repo: repo, Ref: ref}, workDir)
	}
	return Source{}, fmt.Errorf("marketplace source must be a directory, JSON file, repository or JSON URL")
}
func normalizeMarketplaceSource(s Source, workDir string) (Source, error) {
	switch s.Kind {
	case "directory", "file":
		if !filepath.IsAbs(s.Path) {
			s.Path = filepath.Join(workDir, s.Path)
		}
		path, err := filepath.EvalSymlinks(s.Path)
		if err != nil {
			return s, err
		}
		s.Path = path
	case "github":
		if !githubRepo.MatchString(s.Repo) {
			return s, fmt.Errorf("GitHub source must be owner/repo")
		}
	case "git":
		if err := validateGitURL(s.URL); err != nil {
			return s, err
		}
	case "url":
		if err := validateHTTPURL(s.URL, false); err != nil {
			return s, err
		}
	case "settings":
		if !ValidName(s.Name) || len(s.Plugins) == 0 {
			return s, fmt.Errorf("invalid inline marketplace source")
		}
	default:
		return s, fmt.Errorf("unsupported marketplace source type %q", s.Kind)
	}
	if s.Ref != "" && (strings.HasPrefix(s.Ref, "-") || strings.ContainsAny(s.Ref, "\x00\r\n ")) {
		return s, fmt.Errorf("invalid git ref")
	}
	if s.Path != "" && (s.Kind == "git" || s.Kind == "github") {
		path, err := safeRelative(s.Path, false)
		if err != nil {
			return s, err
		}
		s.Path = filepath.ToSlash(path)
	}
	if len(s.SparsePaths) > 128 {
		return s, fmt.Errorf("too many sparse paths")
	}
	for _, path := range s.SparsePaths {
		if _, err := safeRelative(path, false); err != nil {
			return s, err
		}
	}
	return s, nil
}

func entrySource(entry Entry, root string) (Source, error) {
	data := bytes.TrimSpace(entry.Source)
	if len(data) == 0 {
		return Source{}, fmt.Errorf("missing plugin source")
	}
	if data[0] == '"' {
		var path string
		json.Unmarshal(data, &path)
		if !strings.HasPrefix(path, "./") && path != "." {
			if root == "" || strings.Contains(path, "/") {
				return Source{}, fmt.Errorf("relative plugin source must start with ./")
			}
			path = filepath.ToSlash(filepath.Join(root, path))
		}
		path, err := safeRelative(path, true)
		return Source{Kind: "relative", Path: path}, err
	}
	var s Source
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&s); err != nil {
		return s, err
	}
	switch s.Kind {
	case "github":
		if !githubRepo.MatchString(s.Repo) {
			return s, fmt.Errorf("plugin GitHub source must be owner/repo")
		}
	case "url", "git-subdir":
		if s.Kind == "git-subdir" && githubRepo.MatchString(s.URL) {
			s.URL = "https://github.com/" + s.URL + ".git"
		}
		if err := validateGitURL(s.URL); err != nil {
			return s, err
		}
		if s.Kind == "git-subdir" {
			path, err := safeRelative(s.Path, false)
			if err != nil {
				return s, err
			}
			s.Path = path
		}
	case "archive":
		if err := validateHTTPURL(s.URL, true); err != nil {
			return s, err
		}
		if s.SHA256 != "" {
			if len(s.SHA256) != 64 {
				return s, fmt.Errorf("archive sha256 must be 64 hex digits")
			}
			for _, c := range s.SHA256 {
				if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
					return s, fmt.Errorf("invalid archive sha256")
				}
			}
		}
	case "npm":
		if err := validateNPMSource(s); err != nil {
			return s, err
		}
	case "command":
		if s.Command == "" || len(s.Command) > 500 || strings.Contains(s.Command, "    ") {
			return s, fmt.Errorf("invalid install command")
		}
		for _, c := range s.Command {
			if c < 32 || c > 126 {
				return s, fmt.Errorf("install command must be printable ASCII")
			}
		}
		if s.Timeout < 0 || s.Timeout > 600 {
			return s, fmt.Errorf("install timeout must be 1..600 seconds")
		}
		if s.Mode != "" && s.Mode != "copy" && s.Mode != "link" {
			return s, fmt.Errorf("command mode must be copy or link")
		}
		if s.Mode == "link" && runtime.GOOS == "windows" {
			return s, fmt.Errorf("command link mode is unavailable on Windows; use copy mode")
		}
	default:
		return s, fmt.Errorf("unsupported plugin source type %q", s.Kind)
	}
	if s.SHA != "" && !hexSHA.MatchString(s.SHA) {
		return s, fmt.Errorf("git sha must be 40 or 64 hex digits")
	}
	if s.Ref != "" && (strings.HasPrefix(s.Ref, "-") || strings.ContainsAny(s.Ref, "\x00\r\n ")) {
		return s, fmt.Errorf("invalid git ref")
	}
	return s, nil
}
