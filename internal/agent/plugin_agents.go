package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gokin/internal/hooks"
	"gokin/internal/permission"
	"gokin/internal/tools"
	"gopkg.in/yaml.v3"
)

const (
	maxPluginAgentCount        = 128
	maxPluginAgentCatalogBytes = 4 << 20
	maxPluginAgentScanEntries  = 4096
	maxPluginAgentDepth        = 16
)

// ConfigureClaudePlugins shares component selection with skills/hooks, while
// keeping file definitions separate from native and runtime registrations.
func (r *AgentTypeRegistry) ConfigureClaudePlugins(manager *hooks.Manager) {
	r.pluginMu.Lock()
	r.pluginManager = manager
	r.pluginMu.Unlock()
	r.refreshPluginAgents()
}

func (r *AgentTypeRegistry) PluginWarnings() []string {
	r.refreshPluginAgents()
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.pluginWarnings...)
}

func (r *AgentTypeRegistry) refreshPluginAgents() {
	r.pluginMu.Lock()
	defer r.pluginMu.Unlock()
	if r.pluginManager == nil {
		r.mu.Lock()
		if len(r.pluginTypes) > 0 {
			r.pluginTypes = nil
			r.pluginWarnings = nil
			r.pluginFingerprint = ""
			r.revision++
		}
		r.mu.Unlock()
		return
	}
	selected, err := r.pluginManager.ClaudePlugins()
	next := make(map[string]*DynamicAgentType)
	var warnings []string
	if err != nil {
		warnings = append(warnings, "Claude plugin agents unavailable: "+err.Error())
	} else {
		totalBytes := 0
		for _, plugin := range selected {
			loadPluginAgents(plugin, r.pluginManager, next, &totalBytes, &warnings)
		}
	}
	names := make([]string, 0, len(next))
	for name := range next {
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.New()
	for _, name := range names {
		dt := next[name]
		data, _ := json.Marshal(dt)
		_, _ = hash.Write(data)
		_, _ = io.WriteString(hash, dt.plugin.RuntimeFingerprint())
	}
	data, _ := json.Marshal(warnings)
	_, _ = hash.Write(data)
	fingerprint := hex.EncodeToString(hash.Sum(nil))
	r.mu.Lock()
	defer r.mu.Unlock()
	if fingerprint != r.pluginFingerprint {
		r.pluginTypes = next
		r.pluginWarnings = warnings
		r.pluginFingerprint = fingerprint
		r.revision++
	}
}

func loadPluginAgents(plugin hooks.ClaudePlugin, manager *hooks.Manager, next map[string]*DynamicAgentType, totalBytes *int, warnings *[]string) {
	warn := func(err error) { *warnings = append(*warnings, fmt.Sprintf("plugin %s agents: %v", plugin.Name, err)) }
	root, err := os.OpenRoot(plugin.Root())
	if err != nil {
		warn(err)
		return
	}
	defer root.Close()
	seen := map[string]bool{}
	limitWarned := false
	load := func(relative, prefix string) {
		if len(next) >= maxPluginAgentCount {
			if !limitWarned {
				warn(fmt.Errorf("plugin agent catalog count limit reached"))
				limitWarned = true
			}
			return
		}
		if seen[relative] {
			return
		}
		seen[relative] = true
		info, err := root.Lstat(relative)
		if err != nil {
			warn(err)
			return
		}
		if !info.Mode().IsRegular() || info.Size() > maxAgentTypeBytes {
			warn(fmt.Errorf("%s must be a regular file of at most %d bytes", relative, maxAgentTypeBytes))
			return
		}
		if info.Size() > int64(maxPluginAgentCatalogBytes-*totalBytes) {
			if !limitWarned {
				warn(fmt.Errorf("plugin agent catalog byte limit reached"))
				limitWarned = true
			}
			return
		}
		file, err := root.Open(relative)
		if err != nil {
			warn(err)
			return
		}
		data, err := io.ReadAll(io.LimitReader(file, maxAgentTypeBytes+1))
		file.Close()
		if err != nil {
			warn(err)
			return
		}
		if len(data) > maxAgentTypeBytes {
			warn(fmt.Errorf("%s exceeds agent file limit", relative))
			return
		}
		definition, err := parsePluginAgent(data, strings.TrimSuffix(filepath.Base(relative), ".md"), prefix, plugin)
		if err != nil {
			warn(fmt.Errorf("%s: %w", relative, err))
			return
		}
		if next[definition.Name] != nil {
			warn(fmt.Errorf("duplicate agent name %q", definition.Name))
			return
		}
		if len(next) >= maxPluginAgentCount || *totalBytes+len(data) > maxPluginAgentCatalogBytes {
			warn(fmt.Errorf("plugin agent catalog limit reached"))
			return
		}
		definition.pluginManager = manager
		next[definition.Name] = definition
		*totalBytes += len(data)
	}
	declared := strings.TrimSpace(string(plugin.Agents))
	if declared != "" && declared != "null" {
		var paths []string
		var single string
		if json.Unmarshal(plugin.Agents, &single) == nil {
			paths = []string{single}
		} else if json.Unmarshal(plugin.Agents, &paths) != nil {
			warn(fmt.Errorf("agents must be a path or list of paths"))
			return
		}
		if len(paths) > maxPluginAgentCount {
			warn(fmt.Errorf("too many declared agents"))
			return
		}
		for _, relative := range paths {
			if !strings.HasPrefix(relative, "./") || strings.Contains(relative, "\\") || !strings.HasSuffix(relative, ".md") {
				warn(fmt.Errorf("agent paths must start with ./ and name a .md file"))
				continue
			}
			escapes := false
			for _, part := range strings.Split(relative, "/") {
				escapes = escapes || part == ".."
			}
			if escapes {
				warn(fmt.Errorf("agent path escapes plugin root"))
				continue
			}
			load(filepath.FromSlash(strings.TrimPrefix(relative, "./")), "")
		}
		return
	}
	entriesRead := 0
	var walk func(string, []string)
	walk = func(relative string, prefix []string) {
		if len(prefix) > maxPluginAgentDepth {
			warn(fmt.Errorf("agent directory depth exceeds %d", maxPluginAgentDepth))
			return
		}
		dir, err := root.Open(relative)
		if err != nil {
			if relative != "agents" || !os.IsNotExist(err) {
				warn(err)
			}
			return
		}
		entries, err := dir.ReadDir(maxPluginAgentScanEntries - entriesRead + 1)
		dir.Close()
		if err != nil && err != io.EOF {
			warn(err)
			return
		}
		entriesRead += len(entries)
		if entriesRead > maxPluginAgentScanEntries {
			warn(fmt.Errorf("agent scan exceeds %d entries", maxPluginAgentScanEntries))
			return
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			child := filepath.Join(relative, entry.Name())
			if entry.IsDir() {
				if !validPluginAgentSegment(entry.Name()) {
					warn(fmt.Errorf("invalid agent directory name"))
					continue
				}
				walk(child, append(append([]string(nil), prefix...), entry.Name()))
			} else if strings.HasSuffix(entry.Name(), ".md") {
				load(child, strings.Join(prefix, ":"))
			}
			if entriesRead > maxPluginAgentScanEntries {
				return
			}
		}
	}
	walk("agents", nil)
}

func validPluginAgentSegment(name string) bool {
	if name == "" || len(name) > 256 || strings.HasPrefix(name, "-") || !utf8.ValidString(name) || strings.ContainsAny(name, ":/\\@") {
		return false
	}
	for _, c := range name {
		if unicode.IsSpace(c) || unicode.IsControl(c) || c == 0x061c || c == 0x200e || c == 0x200f || c >= 0x202a && c <= 0x202e || c >= 0x2066 && c <= 0x2069 {
			return false
		}
	}
	return true
}

func parsePluginAgent(data []byte, fallback, prefix string, plugin hooks.ClaudePlugin) (*DynamicAgentType, error) {
	if !utf8.Valid(data) || len(data) > maxAgentTypeBytes {
		return nil, fmt.Errorf("agent must be bounded UTF-8")
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	body := text
	fields := make(map[string]yaml.Node)
	if rest, hasFrontmatter := strings.CutPrefix(text, "---\n"); hasFrontmatter {
		lines := strings.Split(rest, "\n")
		end := -1
		for i, line := range lines {
			if line == "---" {
				end = i
				break
			}
		}
		if end < 0 {
			return nil, fmt.Errorf("unterminated agent frontmatter")
		}
		if err := yaml.Unmarshal([]byte(strings.Join(lines[:end], "\n")), &fields); err != nil {
			return nil, fmt.Errorf("agent frontmatter: %w", err)
		}
		body = strings.Join(lines[end+1:], "\n")
	}
	metadata := func(key string, limit int) (string, error) {
		node, present := fields[key]
		if !present {
			return "", nil
		}
		if node.Kind != yaml.ScalarNode || node.Tag != "!!str" || len(node.Value) > limit || strings.ContainsAny(node.Value, "\x00\r\n") {
			return "", fmt.Errorf("invalid agent %s", key)
		}
		return strings.TrimSpace(node.Value), nil
	}
	name, err := metadata("name", 256)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = fallback
	}
	if !validPluginAgentSegment(name) {
		return nil, fmt.Errorf("invalid agent name")
	}
	description, err := metadata("description", 2048)
	if err != nil {
		return nil, err
	}
	if description == "" {
		description = "Agent from " + plugin.Name + " plugin"
	}
	model, err := metadata("model", 128)
	if err != nil {
		return nil, err
	}
	effort, err := metadata("effort", 32)
	if err != nil {
		return nil, err
	}
	if effort != "" && effort != "low" && effort != "medium" && effort != "high" && effort != "xhigh" && effort != "max" {
		return nil, fmt.Errorf("invalid agent effort")
	}
	maxTurns := 0
	if node, present := fields["maxTurns"]; present {
		if node.Tag != "!!int" || node.Decode(&maxTurns) != nil || maxTurns <= 0 || maxTurns > MaxTurnLimit {
			return nil, fmt.Errorf("maxTurns must be between 1 and %d", MaxTurnLimit)
		}
	}
	// Plugin files cannot supply execution authority via these fields. Claude
	// also ignores them; global plugin hooks remain managed by hooks.Manager.
	for _, key := range []string{"skills", "memory", "background", "omitClaudeMd", "isolation", "experimental"} {
		if node, present := fields[key]; present && node.Kind != 0 && node.Tag != "!!null" && !(node.Tag == "!!bool" && node.Value == "false") {
			return nil, fmt.Errorf("plugin agent field %s is not yet supported", key)
		}
	}
	allowed, err := parsePluginAgentTools(fields["tools"])
	if err != nil {
		return nil, fmt.Errorf("agent tools: %w", err)
	}
	denied, err := parsePluginAgentTools(fields["disallowedTools"])
	if err != nil {
		return nil, fmt.Errorf("agent disallowedTools: %w", err)
	}
	_, toolsSpecified := fields["tools"]
	body, err = plugin.ExpandContent(strings.TrimSpace(body))
	if err != nil {
		return nil, err
	}
	if len(body) > maxAgentTypeBytes {
		return nil, fmt.Errorf("expanded agent prompt exceeds limit")
	}
	qualified := plugin.Name + ":"
	if prefix != "" {
		qualified += prefix + ":"
	}
	qualified += name
	return &DynamicAgentType{Name: qualified, Description: description, SystemPrompt: body, AllowedTools: allowed, DisallowedTools: denied, ToolsSpecified: toolsSpecified, Model: model, Effort: effort, MaxTurns: maxTurns, Source: "claude-plugin:" + plugin.ID(), plugin: &plugin}, nil
}

func parsePluginAgentTools(node yaml.Node) ([]string, error) {
	if node.Kind == 0 {
		return nil, nil
	}
	var values []string
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
		values = []string{node.Value}
	} else if node.Kind == yaml.SequenceNode {
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
				return nil, fmt.Errorf("tool lists must contain strings")
			}
			values = append(values, item.Value)
		}
	} else {
		return nil, fmt.Errorf("expected comma-separated tools or a YAML list")
	}
	var names []string
	for _, value := range values {
		// Agent tool specifiers remove/select the whole tool. Do not reuse
		// skill pre-approval semantics, which match individual command args.
		var b strings.Builder
		depth := 0
		for _, c := range value {
			switch c {
			case '(':
				depth++
				if depth > 1 {
					return nil, fmt.Errorf("nested tool specifier")
				}
			case ')':
				depth--
				if depth < 0 {
					return nil, fmt.Errorf("unmatched tool specifier")
				}
			default:
				if depth == 0 {
					b.WriteRune(c)
				}
			}
		}
		if depth != 0 {
			return nil, fmt.Errorf("unterminated tool specifier")
		}
		parsed, err := permission.ParseTemporaryToolDenyList(b.String())
		if err != nil {
			return nil, err
		}
		names = append(names, parsed...)
	}
	return permission.CanonicalizeTemporaryToolDenies(names)
}

func pluginAgentToolMatches(pattern string, tool tools.Tool) bool {
	for _, name := range []string{tool.Name(), tools.ClaudeHookToolName(tool)} {
		parsed, err := permission.ParseTemporaryToolDenyList(name)
		if err != nil || len(parsed) != 1 {
			continue
		}
		canonical := parsed[0]
		if canonical == "todo_write" {
			canonical = "todo"
		}
		if pattern == "todo_write" {
			pattern = "todo"
		}
		if match, _ := path.Match(pattern, canonical); match {
			return true
		}
		if strings.HasPrefix(pattern, "mcp__") && !strings.Contains(strings.TrimPrefix(pattern, "mcp__"), "__") && strings.HasPrefix(canonical, pattern+"__") {
			return true
		}
	}
	return false
}

func pluginAgentToolNames(dt *DynamicAgentType, registry tools.ToolRegistry) []string {
	names := make([]string, 0)
	for _, tool := range registry.List() {
		allowed := !dt.ToolsSpecified
		for _, rule := range dt.AllowedTools {
			allowed = allowed || pluginAgentToolMatches(rule, tool)
		}
		for _, rule := range dt.DisallowedTools {
			if pluginAgentToolMatches(rule, tool) {
				allowed = false
				break
			}
		}
		if allowed {
			names = append(names, tool.Name())
		}
	}
	sort.Strings(names)
	return names
}

type pluginAgentAuthorityKey struct{}

func contextWithPluginAgentAuthority(ctx context.Context, dt *DynamicAgentType) context.Context {
	if dt == nil || dt.plugin == nil {
		return ctx
	}
	previous, _ := ctx.Value(pluginAgentAuthorityKey{}).([]*DynamicAgentType)
	for _, definition := range previous {
		if definition == dt {
			return ctx
		}
	}
	definitions := append(append([]*DynamicAgentType(nil), previous...), dt)
	return context.WithValue(ctx, pluginAgentAuthorityKey{}, definitions)
}

func checkPluginAgentExecution(ctx context.Context) error {
	definitions, _ := ctx.Value(pluginAgentAuthorityKey{}).([]*DynamicAgentType)
	for _, definition := range definitions {
		if err := definition.plugin.CheckSelected(definition.pluginManager); err != nil {
			return &tools.HookExecutionStoppedError{Reason: fmt.Sprintf("plugin agent %q: %v", definition.Name, err)}
		}
	}
	return nil
}
