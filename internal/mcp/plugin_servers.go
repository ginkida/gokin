package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"gokin/internal/hooks"
	"gokin/internal/tools"
	"gopkg.in/yaml.v3"
)

const (
	maxPluginMCPServers   = 128
	maxPluginMCPFileBytes = 256 << 10
	maxPluginMCPBytes     = 4 << 20
	maxPluginMCPSources   = 64
)

type pluginMCPServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// LoadClaudePluginServers performs bounded, read-only component discovery.
// Process starts and network requests belong to Manager's connection phase.
func LoadClaudePluginServers(manager *hooks.Manager, workDir string, allowDefault ...bool) ([]*ServerConfig, error) {
	selected, err := manager.ClaudePlugins()
	if err != nil {
		return nil, err
	}
	var result []*ServerConfig
	totalBytes := 0
	for _, plugin := range selected {
		if len(allowDefault) > 0 && !allowDefault[0] && !plugin.ExplicitlyEnabled() {
			continue
		}
		servers, err := loadPluginMCP(plugin, workDir, &totalBytes)
		if err != nil {
			return nil, fmt.Errorf("plugin %s MCP: %w", plugin.Name, err)
		}
		for _, cfg := range servers {
			if len(result) >= maxPluginMCPServers {
				return nil, fmt.Errorf("plugin MCP catalog exceeds %d servers", maxPluginMCPServers)
			}
			fingerprint := cfg.pluginFingerprint
			name := cfg.Name
			owner := plugin
			cfg.guard = func(ctx context.Context) error {
				if err := owner.CheckSelected(manager); err != nil {
					return &tools.HookExecutionStoppedError{Reason: "plugin MCP unavailable: " + err.Error()}
				}
				current, err := LoadClaudePluginServersUnguarded(manager, workDir, allowDefault...)
				if err != nil {
					return &tools.HookExecutionStoppedError{Reason: "plugin MCP configuration unavailable: " + err.Error()}
				}
				for _, candidate := range current {
					if candidate.Name == name && candidate.pluginFingerprint == fingerprint {
						return nil
					}
				}
				return &tools.HookExecutionStoppedError{Reason: fmt.Sprintf("plugin MCP server %q changed or was removed; reload plugins before using it", name)}
			}
			result = append(result, cfg)
		}
	}
	return result, nil
}

// The guard must re-read current definitions without invoking or installing a
// guard recursively. Runtime fingerprints bind options and trust sources too.
func LoadClaudePluginServersUnguarded(manager *hooks.Manager, workDir string, allowDefault ...bool) ([]*ServerConfig, error) {
	selected, err := manager.ClaudePlugins()
	if err != nil {
		return nil, err
	}
	var result []*ServerConfig
	total := 0
	for _, plugin := range selected {
		if len(allowDefault) > 0 && !allowDefault[0] && !plugin.ExplicitlyEnabled() {
			continue
		}
		configs, err := loadPluginMCP(plugin, workDir, &total)
		if err != nil {
			return nil, err
		}
		result = append(result, configs...)
		if len(result) > maxPluginMCPServers {
			return nil, fmt.Errorf("plugin MCP server limit exceeded")
		}
	}
	return result, nil
}

func validatePluginMCPJSON(data []byte) error {
	if !json.Valid(data) {
		return fmt.Errorf("invalid MCP JSON")
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return err
	}
	var check func(*yaml.Node) error
	check = func(n *yaml.Node) error {
		if n.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				key := n.Content[i].Value
				if seen[key] {
					return fmt.Errorf("duplicate MCP JSON key")
				}
				seen[key] = true
			}
		}
		for _, child := range n.Content {
			if err := check(child); err != nil {
				return err
			}
		}
		return nil
	}
	return check(&node)
}

func loadPluginMCP(plugin hooks.ClaudePlugin, workDir string, totalBytes *int) ([]*ServerConfig, error) {
	root, err := os.OpenRoot(plugin.Root())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	definitions := map[string]json.RawMessage{}
	sources := 0
	seen := map[string]bool{}
	merge := func(data []byte) error {
		if len(data) > maxPluginMCPFileBytes || *totalBytes+len(data) > maxPluginMCPBytes {
			return fmt.Errorf("plugin MCP configuration byte limit exceeded")
		}
		*totalBytes += len(data)
		if err := validatePluginMCPJSON(data); err != nil {
			return err
		}
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(data, &entries); err != nil || entries == nil {
			return fmt.Errorf("MCP configuration must be an object")
		}
		if wrapper, ok := entries["mcpServers"]; ok {
			var wrapped map[string]json.RawMessage
			if len(entries) != 1 || json.Unmarshal(wrapper, &wrapped) != nil || wrapped == nil {
				return fmt.Errorf("invalid mcpServers wrapper")
			}
			entries = wrapped
		}
		if len(entries) > maxPluginMCPServers {
			return fmt.Errorf("too many plugin MCP servers")
		}
		for name, raw := range entries {
			definitions[name] = raw
		}
		return nil
	}
	read := func(relative string, required bool) error {
		if seen[relative] {
			return nil
		}
		seen[relative] = true
		sources++
		if sources > maxPluginMCPSources {
			return fmt.Errorf("too many plugin MCP sources")
		}
		info, err := root.Lstat(relative)
		if err != nil {
			if !required && os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > maxPluginMCPFileBytes {
			return fmt.Errorf("MCP source must be a bounded regular file")
		}
		file, err := root.Open(relative)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(file, maxPluginMCPFileBytes+1))
		file.Close()
		if err != nil {
			return err
		}
		return merge(data)
	}
	if err := read(".mcp.json", false); err != nil {
		return nil, err
	}
	var declared func(json.RawMessage) error
	declared = func(raw json.RawMessage) error {
		data := bytes.TrimSpace(raw)
		if len(data) == 0 || bytes.Equal(data, []byte("null")) {
			return nil
		}
		switch data[0] {
		case '"':
			var value string
			if json.Unmarshal(data, &value) != nil {
				return fmt.Errorf("invalid MCP path")
			}
			if !strings.HasPrefix(value, "./") || strings.Contains(value, "\\") || filepath.Ext(value) != ".json" {
				return fmt.Errorf("MCP paths must start with ./ and name a .json file; bundles and URLs are not yet supported")
			}
			for _, part := range strings.Split(value, "/") {
				if part == ".." {
					return fmt.Errorf("MCP path escapes plugin root")
				}
			}
			return read(filepath.FromSlash(strings.TrimPrefix(value, "./")), true)
		case '{':
			sources++
			if sources > maxPluginMCPSources {
				return fmt.Errorf("too many plugin MCP sources")
			}
			return merge(data)
		case '[':
			var entries []json.RawMessage
			if json.Unmarshal(data, &entries) != nil || len(entries) > maxPluginMCPSources {
				return fmt.Errorf("invalid MCP source array")
			}
			for _, entry := range entries {
				if len(bytes.TrimSpace(entry)) > 0 && bytes.TrimSpace(entry)[0] == '[' {
					return fmt.Errorf("nested MCP source arrays are unsupported")
				}
				if err := declared(entry); err != nil {
					return err
				}
			}
			return nil
		default:
			return fmt.Errorf("mcpServers must be a path, object or array")
		}
	}
	if err := declared(plugin.MCPServers); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(definitions))
	for name := range definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	var result []*ServerConfig
	for _, name := range names {
		if !validPluginMCPName(name) {
			return nil, fmt.Errorf("invalid plugin MCP server name")
		}
		var definition pluginMCPServer
		decoder := json.NewDecoder(bytes.NewReader(definitions[name]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&definition); err != nil {
			return nil, fmt.Errorf("invalid MCP server fields for %q", name)
		}
		cfg, err := makePluginMCPConfig(plugin, name, definition, workDir)
		if err != nil {
			return nil, fmt.Errorf("server %q: %w", name, err)
		}
		data, _ := json.Marshal(cfg)
		if len(data) > maxPluginMCPFileBytes || *totalBytes+len(data) > maxPluginMCPBytes {
			return nil, fmt.Errorf("expanded plugin MCP configuration byte limit exceeded")
		}
		*totalBytes += len(data)
		sum := sha256.Sum256(append(data, []byte(fmt.Sprintf("%s:%t", plugin.RuntimeFingerprint(), plugin.ExplicitlyEnabled()))...))
		cfg.pluginFingerprint = hex.EncodeToString(sum[:])
		cfg.pluginExplicit = plugin.ExplicitlyEnabled()
		result = append(result, cfg)
	}
	return result, nil
}

func validPluginMCPName(name string) bool {
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

func expandPluginMCPValue(plugin hooks.ClaudePlugin, value, workDir string) (string, error) {
	// Expand the original template once. Option values inserted by Expand
	// cannot acquire new shell, environment or plugin-variable semantics.
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
			out.WriteString(value)
			break
		}
		reference := value[:end+1]
		replacement, err := plugin.Expand(reference)
		if err != nil {
			return "", err
		}
		if reference == "${CLAUDE_PROJECT_DIR}" {
			replacement = workDir
		}
		if replacement == reference {
			key := reference[2 : len(reference)-1]
			for _, safe := range SafeEnvVars {
				if key == safe {
					replacement = os.Getenv(key)
					break
				}
			}
		}
		out.WriteString(replacement)
		value = value[end+1:]
	}
	if out.Len() > 32<<10 {
		return "", fmt.Errorf("expanded MCP value exceeds limit")
	}
	return out.String(), nil
}

func makePluginMCPConfig(plugin hooks.ClaudePlugin, name string, definition pluginMCPServer, workDir string) (*ServerConfig, error) {
	if len(definition.Args) > 128 || len(definition.Env) > 128 || len(definition.Headers) > 64 {
		return nil, fmt.Errorf("MCP server argument or environment limit exceeded")
	}
	expand := func(value string) (string, error) { return expandPluginMCPValue(plugin, value, workDir) }
	command, err := expand(definition.Command)
	if err != nil {
		return nil, err
	}
	address, err := expand(definition.URL)
	if err != nil {
		return nil, err
	}
	transport := definition.Type
	if transport == "" {
		if command != "" {
			transport = "stdio"
		} else if address != "" {
			transport = "http"
		}
	}
	if transport != "stdio" && transport != "http" {
		return nil, fmt.Errorf("supported plugin MCP transports are stdio and http")
	}
	if transport == "stdio" && (command == "" || address != "" || len(definition.Headers) > 0) || transport == "http" && (address == "" || command != "" || len(definition.Args) > 0 || len(definition.Env) > 0) {
		return nil, fmt.Errorf("MCP fields do not match transport")
	}
	if strings.ContainsAny(command, "\x00\r\n") {
		return nil, fmt.Errorf("invalid MCP command")
	}
	if transport == "http" {
		parsed, err := url.Parse(address)
		if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Scheme != "https" && parsed.Scheme != "http" {
			return nil, fmt.Errorf("invalid absolute MCP HTTP URL")
		}
		if parsed.Scheme == "http" && parsed.Hostname() != "localhost" && !net.ParseIP(parsed.Hostname()).IsLoopback() {
			return nil, fmt.Errorf("non-loopback MCP URLs must use https")
		}
	}
	args := make([]string, len(definition.Args))
	for i, value := range definition.Args {
		args[i], err = expand(value)
		if err != nil || strings.ContainsRune(args[i], 0) {
			return nil, fmt.Errorf("invalid MCP argument")
		}
	}
	env := make(map[string]string, len(definition.Env)+4)
	for key, value := range definition.Env {
		if !validPluginEnvName(key) {
			return nil, fmt.Errorf("invalid MCP environment name")
		}
		env[key], err = expand(value)
		if err != nil || strings.ContainsRune(env[key], 0) {
			return nil, fmt.Errorf("invalid MCP environment value")
		}
	}
	ownedEnv, err := plugin.ProcessEnvironment(workDir)
	if err != nil {
		return nil, err
	}
	for key, value := range ownedEnv {
		env[key] = value
	}
	headers := make(map[string]string, len(definition.Headers))
	for key, value := range definition.Headers {
		if key == "" || strings.ContainsAny(key, "\r\n\x00:") {
			return nil, fmt.Errorf("invalid MCP header name")
		}
		headers[key], err = expand(value)
		if err != nil || strings.ContainsAny(headers[key], "\r\n\x00") {
			return nil, fmt.Errorf("invalid MCP header value")
		}
	}
	dataDir := env["CLAUDE_PLUGIN_DATA"]
	return &ServerConfig{Name: "plugin:" + plugin.Name + ":" + name, Transport: transport, Command: command, Args: args, Env: env, WorkDir: workDir, URL: address, Headers: headers, AutoConnect: true, ToolPrefix: "mcp__plugin_" + plugin.Name + "_" + name + "_", PermissionLevel: "medium", pluginToolPrefix: "mcp__plugin_" + plugin.Name + "_" + name + "__", literalEnv: true, prepare: func() error { return os.MkdirAll(dataDir, 0o700) }}, nil
}

func validPluginEnvName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i, c := range value {
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
