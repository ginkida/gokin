// Package lsp manages workspace-scoped plugin language servers. Discovery is
// read-only; processes start only for a matching file operation.
package lsp

import (
	"bytes"
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
	"unicode"

	"gokin/internal/hooks"
	"gokin/internal/security"
	"gopkg.in/yaml.v3"
)

const maxConfigBytes = 256 << 10
const maxCatalogBytes = 4 << 20
const maxServers = 128

type Config struct {
	Command               string            `json:"command"`
	Args                  []string          `json:"args,omitempty"`
	ExtensionToLanguage   map[string]string `json:"extensionToLanguage"`
	Transport             string            `json:"transport,omitempty"`
	Env                   map[string]string `json:"env,omitempty"`
	InitializationOptions any               `json:"initializationOptions,omitempty"`
	Settings              any               `json:"settings,omitempty"`
	WorkspaceFolder       string            `json:"workspaceFolder,omitempty"`
	StartupTimeout        int64             `json:"startupTimeout,omitempty"`
	ShutdownTimeout       int64             `json:"shutdownTimeout,omitempty"`
	RequestTimeout        int64             `json:"requestTimeout,omitempty"`
	RestartOnCrash        *bool             `json:"restartOnCrash,omitempty"`
	MaxRestarts           *int              `json:"maxRestarts,omitempty"`
	Diagnostics           *bool             `json:"diagnostics,omitempty"`
	name                  string
	fingerprint           string
	owner                 hooks.ClaudePlugin
}

func (c *Config) Name() string { return c.name }
func (c *Config) timeout(kind string) time.Duration {
	var ms int64
	var fallback time.Duration
	switch kind {
	case "startup":
		ms, fallback = c.StartupTimeout, 10*time.Second
	case "shutdown":
		ms, fallback = c.ShutdownTimeout, 2*time.Second
	default:
		ms, fallback = c.RequestTimeout, 60*time.Second
	}
	if ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return fallback
}

// LoadPluginConfigs merges the default .lsp.json with manifest sources in
// order. Runtime provenance cannot be supplied by plugin JSON.
func LoadPluginConfigs(authority *hooks.Manager, workDir string) ([]*Config, error) {
	plugins, err := authority.ClaudePlugins()
	if err != nil {
		return nil, err
	}
	var configs []*Config
	total := 0
	for _, plugin := range plugins {
		loaded, err := loadPlugin(plugin, workDir, &total)
		if err != nil {
			return nil, fmt.Errorf("plugin %s LSP: %w", plugin.Name, err)
		}
		configs = append(configs, loaded...)
		if len(configs) > maxServers {
			return nil, fmt.Errorf("LSP catalog exceeds %d servers", maxServers)
		}
	}
	return configs, nil
}

func loadPlugin(plugin hooks.ClaudePlugin, workDir string, total *int) ([]*Config, error) {
	root, err := os.OpenRoot(plugin.Root())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	definitions := map[string]json.RawMessage{}
	sources := 0
	merge := func(data []byte) error {
		if len(data) > maxConfigBytes || *total+len(data) > maxCatalogBytes {
			return fmt.Errorf("LSP config byte limit exceeded")
		}
		*total += len(data)
		if err := strictJSON(data); err != nil {
			return err
		}
		var entries map[string]json.RawMessage
		if json.Unmarshal(data, &entries) != nil || entries == nil {
			return fmt.Errorf("LSP config must be an object map")
		}
		if len(entries) > maxServers {
			return fmt.Errorf("too many LSP servers")
		}
		for name, raw := range entries {
			definitions[name] = raw
		}
		if len(definitions) > maxServers {
			return fmt.Errorf("too many merged LSP servers")
		}
		return nil
	}
	read := func(path string, required bool) error {
		sources++
		if sources > 64 {
			return fmt.Errorf("too many LSP sources")
		}
		info, err := root.Lstat(path)
		if err != nil {
			if !required && os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > maxConfigBytes {
			return fmt.Errorf("LSP source must be a bounded regular file")
		}
		file, err := root.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
		if err != nil {
			return err
		}
		return merge(data)
	}
	if err := read(".lsp.json", false); err != nil {
		return nil, err
	}
	var declared func(json.RawMessage, bool) error
	declared = func(raw json.RawMessage, array bool) error {
		data := bytes.TrimSpace(raw)
		if len(data) == 0 || bytes.Equal(data, []byte("null")) {
			return nil
		}
		switch data[0] {
		case '"':
			var path string
			if json.Unmarshal(data, &path) != nil || !strings.HasPrefix(path, "./") || filepath.Ext(path) != ".json" || strings.ContainsAny(path, "\\\x00") {
				return fmt.Errorf("LSP paths must name contained ./files.json")
			}
			for _, part := range strings.Split(path, "/") {
				if part == ".." {
					return fmt.Errorf("LSP path escapes plugin")
				}
			}
			return read(filepath.FromSlash(strings.TrimPrefix(path, "./")), true)
		case '{':
			sources++
			if sources > 64 {
				return fmt.Errorf("too many LSP sources")
			}
			return merge(data)
		case '[':
			if array {
				return fmt.Errorf("nested LSP source arrays are unsupported")
			}
			var entries []json.RawMessage
			if json.Unmarshal(data, &entries) != nil || len(entries) > 64 {
				return fmt.Errorf("invalid LSP source array")
			}
			for _, entry := range entries {
				if err := declared(entry, true); err != nil {
					return err
				}
			}
			return nil
		default:
			return fmt.Errorf("lspServers must be a path, object or array")
		}
	}
	if err := declared(plugin.LSPServers, false); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(definitions))
	for name := range definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	var configs []*Config
	for _, name := range names {
		if !validName(name) {
			return nil, fmt.Errorf("invalid LSP server name")
		}
		var cfg Config
		decoder := json.NewDecoder(bytes.NewReader(definitions[name]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg); err != nil {
			return nil, fmt.Errorf("invalid fields for LSP server %q", name)
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(definitions[name], &fields)
		for _, key := range []string{"startupTimeout", "shutdownTimeout", "requestTimeout"} {
			if raw, present := fields[key]; present {
				var ms int64
				if json.Unmarshal(raw, &ms) != nil || ms <= 0 {
					return nil, fmt.Errorf("%s must be positive milliseconds", key)
				}
			}
		}
		for _, key := range []string{"restartOnCrash", "diagnostics", "maxRestarts"} {
			if raw, present := fields[key]; present && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return nil, fmt.Errorf("%s must not be null", key)
			}
		}
		if err := cfg.normalize(plugin, workDir); err != nil {
			return nil, fmt.Errorf("server %q: %w", name, err)
		}
		cfg.name = "plugin:" + plugin.Name + ":" + name
		cfg.owner = plugin
		data, _ := json.Marshal(cfg)
		if len(data) > maxConfigBytes || *total+len(data) > maxCatalogBytes {
			return nil, fmt.Errorf("expanded LSP config exceeds byte limit")
		}
		*total += len(data)
		sum := sha256.Sum256(append(data, []byte(plugin.RuntimeFingerprint())...))
		cfg.fingerprint = hex.EncodeToString(sum[:])
		configs = append(configs, &cfg)
	}
	return configs, nil
}

func strictJSON(data []byte) error {
	if !json.Valid(data) {
		return fmt.Errorf("invalid LSP JSON")
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return err
	}
	var visit func(*yaml.Node) error
	visit = func(n *yaml.Node) error {
		if n.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				k := n.Content[i].Value
				if seen[k] {
					return fmt.Errorf("duplicate LSP JSON key")
				}
				seen[k] = true
			}
		}
		for _, child := range n.Content {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(&node)
}

func validName(name string) bool {
	if name == "" || len(name) > 128 || strings.ContainsAny(name, ":/\\@") {
		return false
	}
	for _, c := range name {
		if unicode.IsSpace(c) || unicode.IsControl(c) || unicode.In(c, unicode.Cf) {
			return false
		}
	}
	return true
}

func (c *Config) normalize(plugin hooks.ClaudePlugin, workDir string) error {
	expand := func(value string) (string, error) {
		var out strings.Builder
		for {
			i := strings.Index(value, "${")
			if i < 0 {
				out.WriteString(value)
				break
			}
			out.WriteString(value[:i])
			value = value[i:]
			end := strings.IndexByte(value, '}')
			if end < 0 {
				return "", fmt.Errorf("unterminated LSP variable")
			}
			ref := value[:end+1]
			replacement, err := plugin.Expand(ref)
			if err != nil {
				return "", err
			}
			if ref == "${CLAUDE_PROJECT_DIR}" {
				replacement = workDir
			}
			out.WriteString(replacement)
			value = value[end+1:]
		}
		if out.Len() > 32<<10 {
			return "", fmt.Errorf("expanded LSP value too large")
		}
		return out.String(), nil
	}
	var err error
	c.Command, err = expand(c.Command)
	if err != nil {
		return err
	}
	if strings.TrimSpace(c.Command) == "" || strings.ContainsRune(c.Command, 0) || (!filepath.IsAbs(c.Command) && strings.ContainsFunc(c.Command, unicode.IsSpace)) {
		return fmt.Errorf("command must be a binary name or absolute path; use args for arguments")
	}
	if c.Transport == "" {
		c.Transport = "stdio"
	}
	if c.Transport != "stdio" && c.Transport != "socket" {
		return fmt.Errorf("unsupported LSP transport")
	}
	if len(c.Args) > 256 || len(c.Env) > 128 || len(c.ExtensionToLanguage) == 0 || len(c.ExtensionToLanguage) > 128 {
		return fmt.Errorf("invalid LSP args, env or extension count")
	}
	for ext, language := range c.ExtensionToLanguage {
		if !strings.HasPrefix(ext, ".") || len(ext) < 2 || len(ext) > 64 || strings.ContainsAny(ext, "/\\\x00 \t\n") || strings.TrimSpace(language) == "" || len(language) > 128 || strings.ContainsAny(language, "\x00\r\n") {
			return fmt.Errorf("invalid extensionToLanguage entry")
		}
	}
	for i, v := range c.Args {
		c.Args[i], err = expand(v)
		if err != nil {
			return err
		}
		if strings.ContainsRune(c.Args[i], 0) {
			return fmt.Errorf("null byte in LSP argument")
		}
	}
	if c.Env == nil {
		c.Env = map[string]string{}
	}
	for k, v := range c.Env {
		if k == "" || strings.ContainsAny(k, "=\x00\r\n") {
			return fmt.Errorf("invalid LSP environment name")
		}
		c.Env[k], err = expand(v)
		if err != nil {
			return err
		}
		if strings.ContainsRune(c.Env[k], 0) {
			return fmt.Errorf("null byte in LSP environment")
		}
	}
	owned, err := plugin.ProcessEnvironment(workDir)
	if err != nil {
		return err
	}
	for k, v := range owned {
		c.Env[k] = v
	}
	var expandJSON func(any) (any, error)
	expandJSON = func(v any) (any, error) {
		switch x := v.(type) {
		case string:
			return expand(x)
		case []any:
			for i, item := range x {
				next, err := expandJSON(item)
				if err != nil {
					return nil, err
				}
				x[i] = next
			}
		case map[string]any:
			for k, item := range x {
				next, err := expandJSON(item)
				if err != nil {
					return nil, err
				}
				x[k] = next
			}
		}
		return v, nil
	}
	c.InitializationOptions, err = expandJSON(c.InitializationOptions)
	if err != nil {
		return err
	}
	c.Settings, err = expandJSON(c.Settings)
	if err != nil {
		return err
	}
	if c.WorkspaceFolder == "" {
		c.WorkspaceFolder = workDir
	} else {
		c.WorkspaceFolder, err = expand(c.WorkspaceFolder)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(c.WorkspaceFolder) {
			c.WorkspaceFolder = filepath.Join(workDir, c.WorkspaceFolder)
		}
	}
	c.WorkspaceFolder, err = security.NewPathValidator([]string{workDir}, true).Validate(c.WorkspaceFolder)
	if err != nil {
		return fmt.Errorf("LSP workspaceFolder must remain in workspace: %w", err)
	}
	info, err := os.Stat(c.WorkspaceFolder)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("LSP workspaceFolder must exist and be a directory")
	}
	for _, ms := range []int64{c.StartupTimeout, c.ShutdownTimeout, c.RequestTimeout} {
		if ms < 0 || ms > 2147483647 {
			return fmt.Errorf("LSP timeouts must be positive milliseconds")
		}
	}
	if c.MaxRestarts != nil && (*c.MaxRestarts < 0 || *c.MaxRestarts > 100) {
		return fmt.Errorf("maxRestarts must be between 0 and 100")
	}
	return nil
}
