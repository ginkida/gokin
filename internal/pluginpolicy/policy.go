// Package pluginpolicy shares offline marketplace policy checks between
// plugin management and runtime discovery. It never fetches sources.
package pluginpolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gokin/internal/fileutil"
	"gopkg.in/yaml.v3"
)

type Source struct {
	Kind          string            `json:"source"`
	Repo          string            `json:"repo,omitempty"`
	URL           string            `json:"url,omitempty"`
	Ref           string            `json:"ref,omitempty"`
	Path          string            `json:"path,omitempty"`
	SHA           string            `json:"sha,omitempty"`
	SHA256        string            `json:"sha256,omitempty"`
	SparsePaths   []string          `json:"sparsePaths,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	HeadersHelper string            `json:"headersHelper,omitempty"`
	HostPattern   string            `json:"hostPattern,omitempty"`
	PathPattern   string            `json:"pathPattern,omitempty"`
	Name          string            `json:"name,omitempty"`
	Plugins       json.RawMessage   `json:"plugins,omitempty"`
	Owner         json.RawMessage   `json:"owner,omitempty"`
	Package       string            `json:"package,omitempty"`
	Version       string            `json:"version,omitempty"`
	Registry      string            `json:"registry,omitempty"`
	Command       string            `json:"command,omitempty"`
	Timeout       int               `json:"timeout,omitempty"`
	Mode          string            `json:"mode,omitempty"`
	SkipLFS       bool              `json:"skipLfs,omitempty"`
}

// EqualSource compares source objects semantically while keeping identifiers,
// URLs, refs and paths exact. Inline JSON formatting does not change provenance.
func EqualSource(a, b Source) bool {
	canonical := func(source Source) []byte {
		data, err := json.Marshal(source)
		if err != nil {
			return nil
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) != nil {
			return nil
		}
		data, _ = json.Marshal(value)
		return data
	}
	left, right := canonical(a), canonical(b)
	return left != nil && right != nil && bytes.Equal(left, right)
}

// ResolveLocalSource aligns a settings path with the canonical local source
// recorded by an installer. Git source paths retain their exact identity.
func ResolveLocalSource(source Source, base string) Source {
	if source.Kind != "file" && source.Kind != "directory" {
		return source
	}
	if !filepath.IsAbs(source.Path) {
		source.Path = filepath.Join(base, source.Path)
	}
	if path, err := filepath.EvalSymlinks(source.Path); err == nil {
		source.Path = path
	}
	source.Path = filepath.Clean(source.Path)
	return source
}

type Policy struct {
	Allow            []Source
	AllowSet         bool
	Block            []Source
	DisabledCommands bool
	Required         map[string]Source
	Enabled          map[string]bool
}

func OfficialMarketplace() Source {
	return Source{Kind: "github", Repo: "anthropics/claude-plugins-official"}
}

func FromDocuments(documents [][]byte) (Policy, error) {
	p := Policy{Required: map[string]Source{}, Enabled: map[string]bool{}}
	var commandSetting *bool
	managedHooksOnly := false
	for _, data := range documents {
		if len(bytes.TrimSpace(data)) == 0 {
			continue
		}
		if err := ValidateJSON(data); err != nil {
			return p, err
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil || fields == nil {
			return p, fmt.Errorf("managed plugin policy must be an object")
		}
		if _, exists := fields["strictKnownMarketplaces"]; !exists {
			if value, alias := fields["allowedMarketplaces"]; alias {
				fields["strictKnownMarketplaces"] = value
			}
		}
		if _, exists := fields["extraKnownMarketplaces"]; !exists {
			if value, alias := fields["additionalMarketplaces"]; alias {
				fields["extraKnownMarketplaces"] = value
			}
		}
		if raw, set := fields["allowManagedHooksOnly"]; set {
			var flag *bool
			if json.Unmarshal(raw, &flag) != nil {
				return p, fmt.Errorf("allowManagedHooksOnly must be boolean")
			}
			managedHooksOnly = flag != nil && *flag
		}
		if raw, set := fields["strictKnownMarketplaces"]; set {
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				p.Allow = nil
				p.AllowSet = false
			} else {
				p.AllowSet = true
				if err := json.Unmarshal(raw, &p.Allow); err != nil {
					return p, fmt.Errorf("strictKnownMarketplaces must be an array")
				}
			}
		}
		if raw, set := fields["blockedMarketplaces"]; set {
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				p.Block = nil
			} else {
				if err := json.Unmarshal(raw, &p.Block); err != nil {
					return p, fmt.Errorf("blockedMarketplaces must be an array")
				}
			}
		}
		if raw, set := fields["disableCommandPluginSources"]; set {
			if err := json.Unmarshal(raw, &commandSetting); err != nil {
				return p, fmt.Errorf("disableCommandPluginSources must be boolean")
			}
		}
		if raw, set := fields["enabledPlugins"]; set {
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				clear(p.Enabled)
			}
			var entries map[string]*bool
			if json.Unmarshal(raw, &entries) != nil {
				return p, fmt.Errorf("managed enabledPlugins must be an object")
			}
			for id, on := range entries {
				if on == nil {
					delete(p.Enabled, id)
				} else {
					p.Enabled[id] = *on
				}
			}
		}
		if raw, set := fields["extraKnownMarketplaces"]; set {
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				clear(p.Required)
			}
			var entries map[string]json.RawMessage
			if json.Unmarshal(raw, &entries) != nil {
				return p, fmt.Errorf("managed extraKnownMarketplaces must be an object")
			}
			for name, raw := range entries {
				if string(raw) == "null" {
					delete(p.Required, name)
					continue
				}
				var entry struct {
					Source Source `json:"source"`
				}
				if json.Unmarshal(raw, &entry) != nil || entry.Source.Kind == "" {
					return p, fmt.Errorf("invalid required marketplace %q", name)
				}
				p.Required[name] = entry.Source
			}
		}
	}
	for id, on := range p.Enabled {
		if on && strings.HasSuffix(id, "@claude-plugins-official") {
			if _, explicit := p.Required["claude-plugins-official"]; !explicit {
				p.Required["claude-plugins-official"] = OfficialMarketplace()
			}
		}
	}
	p.DisabledCommands = managedHooksOnly
	if commandSetting != nil {
		p.DisabledCommands = *commandSetting
	}
	if len(p.Allow) > 128 || len(p.Block) > 128 || len(p.Required) > 64 || len(p.Enabled) > 64 {
		return p, fmt.Errorf("managed plugin policy exceeds count limits")
	}
	for _, source := range append(append([]Source{}, p.Allow...), p.Block...) {
		if err := validateRule(source); err != nil {
			return p, err
		}
	}
	return p, nil
}

func validateRule(s Source) error {
	switch s.Kind {
	case "github":
		if s.Repo == "" {
			return fmt.Errorf("marketplace github policy requires repo")
		}
	case "git", "url":
		if s.URL == "" {
			return fmt.Errorf("marketplace URL policy requires url")
		}
	case "file", "directory":
		if s.Path == "" {
			return fmt.Errorf("marketplace path policy requires path")
		}
	case "settings":
		if s.Name == "" {
			return fmt.Errorf("settings marketplace policy requires name")
		}
	case "skills-dir", "npm":
	case "hostPattern":
		if _, err := regexp.Compile(s.HostPattern); err != nil || s.HostPattern == "" {
			return fmt.Errorf("invalid marketplace hostPattern")
		}
	case "pathPattern":
		if _, err := regexp.Compile(s.PathPattern); err != nil || s.PathPattern == "" {
			return fmt.Errorf("invalid marketplace pathPattern")
		}
	default:
		return fmt.Errorf("unsupported managed marketplace policy type %q", s.Kind)
	}
	return nil
}

func (p Policy) Check(source Source) error {
	for _, rule := range p.Block {
		if matches(rule, source, false) {
			return fmt.Errorf("marketplace source is blocked by managed policy")
		}
	}
	if !p.AllowSet {
		return nil
	}
	for _, rule := range p.Allow {
		if matches(rule, source, true) {
			return nil
		}
	}
	return fmt.Errorf("marketplace source is absent from managed strictKnownMarketplaces")
}
func (p Policy) RestrictsMarketplaces() bool {
	if p.AllowSet {
		return true
	}
	for _, rule := range p.Block {
		if rule.Kind != "skills-dir" {
			return true
		}
	}
	return false
}
func matches(rule, source Source, allow bool) bool {
	switch rule.Kind {
	case "hostPattern":
		host := Host(source)
		return host != "" && regexp.MustCompile(rule.HostPattern).MatchString(host)
	case "pathPattern":
		return (source.Kind == "file" || source.Kind == "directory") && regexp.MustCompile(rule.PathPattern).MatchString(source.Path)
	case "github":
		if allow {
			if source.Kind != "github" {
				return false
			}
			wildcard := strings.HasSuffix(rule.Repo, "/*")
			matched := source.Repo == rule.Repo || wildcard && strings.HasPrefix(source.Repo, strings.TrimSuffix(rule.Repo, "*"))
			return matched && rule.Ref == source.Ref && (wildcard && rule.Path == "" || rule.Path == source.Path)
		}
		repo := GitHubRepo(source)
		if repo == "" {
			return false
		}
		pattern := strings.ToLower(rule.Repo)
		repo = strings.ToLower(repo)
		matched := repo == pattern || strings.HasSuffix(pattern, "/*") && strings.HasPrefix(repo, strings.TrimSuffix(pattern, "*"))
		return matched && (rule.Ref == "" || rule.Ref == source.Ref) && (rule.Path == "" || rule.Path == source.Path)
	case "git":
		if allow {
			return source.Kind == "git" && rule.URL == source.URL && rule.Ref == source.Ref && rule.Path == source.Path
		}
		if source.Kind != "git" && source.Kind != "github" {
			return false
		}
		matched := canonicalGitURL(rule.URL) == canonicalGitURL(GitURL(source))
		return matched && (rule.Ref == "" || rule.Ref == source.Ref) && (rule.Path == "" || rule.Path == source.Path)
	case "url":
		if !allow && (source.Kind == "git" || source.Kind == "github") {
			return canonicalGitURL(rule.URL) == canonicalGitURL(GitURL(source))
		}
		return source.Kind == "url" && rule.URL == source.URL
	case "file", "directory":
		return rule.Kind == source.Kind && filepath.Clean(rule.Path) == filepath.Clean(source.Path)
	case "settings":
		if source.Kind != "settings" || rule.Name != source.Name {
			return false
		}
		if !allow {
			return true
		}
		var a, b any
		json.Unmarshal(rule.Plugins, &a)
		json.Unmarshal(source.Plugins, &b)
		da, _ := json.Marshal(a)
		db, _ := json.Marshal(b)
		return bytes.Equal(da, db)
	default:
		return rule.Kind == source.Kind
	}
}
func GitURL(s Source) string {
	if s.Kind == "github" {
		return "https://github.com/" + s.Repo + ".git"
	}
	return s.URL
}
func canonicalGitURL(raw string) string {
	if strings.HasPrefix(raw, "git@github.com:") {
		raw = "https://github.com/" + strings.TrimPrefix(raw, "git@github.com:")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if strings.EqualFold(u.Hostname(), "github.com") {
		return "github.com/" + strings.ToLower(strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git"))
	}
	return strings.TrimSuffix(raw, ".git")
}
func GitHubRepo(s Source) string {
	if s.Kind == "github" {
		return s.Repo
	}
	normalized := canonicalGitURL(s.URL)
	if strings.HasPrefix(normalized, "github.com/") {
		return strings.TrimPrefix(normalized, "github.com/")
	}
	return ""
}
func Host(s Source) string {
	if s.Kind == "github" {
		return "github.com"
	}
	if strings.HasPrefix(s.URL, "git@") {
		host, _, ok := strings.Cut(strings.TrimPrefix(s.URL, "git@"), ":")
		if ok {
			return host
		}
	}
	u, err := url.Parse(s.URL)
	if err == nil {
		return u.Hostname()
	}
	return ""
}

// ReadManaged shares the base-file/drop-in order used by runtime hooks. It
// reads only administrator-owned paths supplied by the application.
func ReadManaged(directory string) (Policy, error) {
	documents, err := ReadManagedDocuments(directory)
	if err != nil {
		return Policy{}, err
	}
	return FromDocuments(documents)
}

// ReadManagedDocuments returns bounded, validated documents in the same
// base-file/drop-in order used for runtime settings.
func ReadManagedDocuments(directory string) ([][]byte, error) {
	if directory == "" {
		return nil, nil
	}
	paths := []string{filepath.Join(directory, "managed-settings.json")}
	dir := filepath.Join(directory, "managed-settings.d")
	file, err := os.Open(dir)
	if err == nil {
		entries, readErr := file.ReadDir(4097)
		file.Close()
		if readErr != nil && readErr != io.EOF {
			return nil, readErr
		}
		if len(entries) > 4096 {
			return nil, fmt.Errorf("too many managed settings entries")
		}
		var names []string
		for _, entry := range entries {
			if !entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") && strings.HasSuffix(entry.Name(), ".json") {
				names = append(names, entry.Name())
			}
		}
		sort.Strings(names)
		if len(names) > 256 {
			return nil, fmt.Errorf("too many managed settings files")
		}
		for _, name := range names {
			paths = append(paths, filepath.Join(dir, name))
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	var documents [][]byte
	total := 0
	for _, path := range paths {
		data, err := fileutil.ReadResolvedRegularFile(path, 1<<20)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(data)) == 0 {
			data = []byte("{}")
		}
		if err := ValidateJSON(data); err != nil {
			return nil, fmt.Errorf("invalid managed settings: %w", err)
		}
		total += len(data)
		if total > 16<<20 {
			return nil, fmt.Errorf("managed policy byte limit exceeded")
		}
		documents = append(documents, data)
		if len(documents) > 256 {
			return nil, fmt.Errorf("too many managed settings files")
		}
	}
	return documents, nil
}

func ValidateJSON(data []byte) error {
	if !json.Valid(data) {
		return fmt.Errorf("invalid JSON")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	var visit func(*yaml.Node, int) error
	visit = func(n *yaml.Node, depth int) error {
		if depth > 64 {
			return fmt.Errorf("JSON nesting limit exceeded")
		}
		if n.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				key := n.Content[i].Value
				if seen[key] {
					return fmt.Errorf("duplicate JSON key %q", key)
				}
				seen[key] = true
			}
		}
		for _, child := range n.Content {
			if err := visit(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(&doc, 0)
}
