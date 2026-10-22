package skills

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gokin/internal/hooks"
	"gopkg.in/yaml.v3"
)

func ValidInvocationName(name string) bool {
	parts := strings.Split(name, ":")
	if len(parts) == 1 {
		return validSkillName.MatchString(name)
	}
	if len(parts) > 17 || parts[0] == "" || len(parts[0]) > 128 || !utf8.ValidString(parts[0]) || strings.ContainsAny(parts[0], "@/\\") {
		return false
	}
	for _, part := range parts[1:] {
		if !validSkillName.MatchString(part) {
			return false
		}
	}
	for _, c := range parts[0] {
		if unicode.IsSpace(c) || unicode.IsControl(c) || c == 0x061c || c == 0x200e || c == 0x200f || c >= 0x202a && c <= 0x202e || c >= 0x2066 && c <= 0x2069 {
			return false
		}
	}
	return true
}

// ConfigureClaudePlugins reuses the hook manager's trust and policy selection.
// Delegated catalogs share this authority but retain their own invocation state.
func (c *Catalog) ConfigureClaudePlugins(manager *hooks.Manager) {
	c.reloadMu.Lock()
	defer c.reloadMu.Unlock()
	c.plugins = manager
	c.reloadLocked()
}

func (s Skill) PersonalSource() bool {
	return s.Source == "global" || s.Source == "claude-global" || s.plugin != nil && s.plugin.Personal()
}

func (s Skill) ExpandPluginVariables(value string) (string, error) {
	if s.plugin == nil {
		return value, nil
	}
	return s.plugin.ExpandContent(value)
}

func (s Skill) PluginVariable(ref string) (string, bool) {
	if s.plugin == nil || !(ref == "${CLAUDE_PLUGIN_ROOT}" || ref == "${CLAUDE_PLUGIN_DATA}" || strings.HasPrefix(ref, "${user_config.")) {
		return "", false
	}
	value, err := s.plugin.ExpandContent(ref)
	return value, err == nil
}

func pluginComponentPath(path string, skills bool) (string, error) {
	if skills && (path == "." || path == "./") {
		return ".", nil
	}
	if !strings.HasPrefix(path, "./") || strings.Contains(path, "\\") {
		return "", fmt.Errorf("plugin component paths must start with ./")
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return "", fmt.Errorf("plugin component path escapes root")
		}
	}
	return filepath.FromSlash(strings.TrimPrefix(path, "./")), nil
}

func componentPaths(raw json.RawMessage) ([]string, error) {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return []string{single}, nil
	}
	var paths []string
	if err := json.Unmarshal(raw, &paths); err != nil || len(paths) > MaxSkillScanEntries {
		return nil, fmt.Errorf("plugin paths must be a string or bounded list of strings")
	}
	return paths, nil
}

func pluginEntries(root *os.Root, path string) ([]os.DirEntry, error) {
	dir, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(MaxSkillScanEntries + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > MaxSkillScanEntries {
		return nil, fmt.Errorf("plugin directory exceeds %d entries", MaxSkillScanEntries)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

type pluginCommand struct {
	Source       *string   `json:"source"`
	Content      *string   `json:"content"`
	Description  *string   `json:"description"`
	ArgumentHint *string   `json:"argumentHint"`
	Model        *string   `json:"model"`
	AllowedTools *[]string `json:"allowedTools"`
}

func (c *Catalog) loadPluginWorkflows(plugins []hooks.ClaudePlugin, next map[string]Skill, totalBytes *int, warnings *[]string) {
	for _, plugin := range plugins {
		root, err := os.OpenRoot(plugin.Root())
		if err != nil {
			*warnings = append(*warnings, fmt.Sprintf("plugin %s: %v", plugin.Name, err))
			continue
		}
		func() {
			defer root.Close()
			seenFiles, claimed := map[string]bool{}, map[string]bool{}
			warn := func(err error) { *warnings = append(*warnings, fmt.Sprintf("plugin %s: %v", plugin.Name, err)) }
			add := func(skill Skill, fallback string, err error) {
				if err != nil {
					warn(err)
					return
				}
				name := strings.TrimSpace(skill.DisplayName)
				if name == "" {
					name = fallback
				}
				prefix := strings.ToLower(plugin.Name) + ":"
				name = strings.TrimPrefix(strings.ToLower(name), prefix)
				key := prefix + name
				if !ValidInvocationName(key) {
					warn(fmt.Errorf("invalid workflow name %q", key))
					return
				}
				if claimed[key] {
					warn(fmt.Errorf("duplicate workflow %q", key))
					return
				}
				claimed[key] = true
				skill.Name, skill.DisplayName = key, key
				skill.Source = "claude-plugin:" + plugin.ID()
				p := plugin
				skill.plugin = &p
				skill.Hooks = p.BindHooks(skill.Hooks)
				if err := p.ValidateHooks(skill.Hooks); err != nil {
					warn(err)
					return
				}
				// Validate references without expanding the template: inserted option
				// values must never become dynamic syntax or argument placeholders.
				if _, err := p.ExpandContent(skill.Body); err != nil {
					warn(err)
					return
				}
				commands, _ := ParseDynamicCommands(skill.Body)
				for _, command := range commands {
					if strings.Contains(command.Command, "${user_config.") {
						warn(fmt.Errorf("user_config interpolation inside dynamic shell commands is unsupported"))
						return
					}
				}
				for _, rule := range skill.AllowedTools {
					if _, err := p.ExpandContent(rule); err != nil {
						warn(err)
						return
					}
				}
				bytes := catalogSkillBytes(skill)
				if len(next) >= MaxSkillCount || *totalBytes+bytes > MaxCatalogBytes {
					warn(fmt.Errorf("plugin workflow catalog limit reached"))
					return
				}
				if _, exists := next[key]; exists {
					warn(fmt.Errorf("workflow name %q is already claimed", key))
					return
				}
				next[key] = skill
				*totalBytes += bytes
			}
			load := func(path, name string, override *pluginCommand) {
				identity := path
				if override != nil {
					identity += "#" + name
				}
				if seenFiles[identity] {
					return
				}
				seenFiles[identity] = true
				var skill Skill
				var err error
				if override != nil && override.Content != nil {
					skill, err = parseSkillData([]byte(*override.Content), filepath.Join(plugin.Root(), ".claude-plugin", "plugin.json")+"#commands:"+name, name, "claude-plugin:"+plugin.ID())
				} else {
					skill, err = loadSkill(root, path, filepath.Join(plugin.Root(), path), name, "claude-plugin:"+plugin.ID())
				}
				if err == nil && override != nil {
					skill.DisplayName = name
					err = override.apply(&skill)
				}
				add(skill, name, err)
			}
			loadSkills := func(path string, required bool) {
				if _, err := root.Stat(filepath.Join(path, "SKILL.md")); err == nil {
					load(filepath.Join(path, "SKILL.md"), filepath.Base(filepath.Join(plugin.Root(), path)), nil)
					return
				}
				entries, err := pluginEntries(root, path)
				if err != nil {
					if required || !os.IsNotExist(err) {
						warn(err)
					}
					return
				}
				for _, entry := range entries {
					if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
						load(filepath.Join(path, entry.Name(), "SKILL.md"), entry.Name(), nil)
					}
				}
			}
			loadSkills("skills", false)
			if len(plugin.Skills) > 0 && string(plugin.Skills) != "null" {
				paths, err := componentPaths(plugin.Skills)
				if err != nil {
					warn(err)
				} else {
					for _, path := range paths {
						rel, err := pluginComponentPath(path, true)
						if err != nil {
							warn(err)
						} else {
							loadSkills(rel, true)
						}
					}
				}
			} else if _, err := root.Stat("skills"); os.IsNotExist(err) {
				if _, err := root.Stat("SKILL.md"); err == nil {
					load("SKILL.md", filepath.Base(plugin.Root()), nil)
				}
			}
			loadCommands := func(path string, required bool) {
				if strings.HasSuffix(path, ".md") {
					load(path, strings.TrimSuffix(filepath.Base(path), ".md"), nil)
					return
				}
				entries, err := pluginEntries(root, path)
				if err != nil {
					if required || !os.IsNotExist(err) {
						warn(err)
					}
					return
				}
				for _, entry := range entries {
					if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".md") {
						load(filepath.Join(path, entry.Name()), strings.TrimSuffix(entry.Name(), ".md"), nil)
					}
				}
			}
			if len(plugin.Commands) == 0 || string(plugin.Commands) == "null" {
				loadCommands("commands", false)
			} else if plugin.Commands[0] != '{' {
				paths, err := componentPaths(plugin.Commands)
				if err != nil {
					warn(err)
				} else {
					for _, path := range paths {
						rel, err := pluginComponentPath(path, false)
						if err != nil {
							warn(err)
						} else {
							loadCommands(rel, true)
						}
					}
				}
			} else {
				var commands map[string]pluginCommand
				if err := json.Unmarshal(plugin.Commands, &commands); err != nil || len(commands) > MaxSkillScanEntries {
					warn(fmt.Errorf("invalid plugin commands object"))
					return
				}
				keys := make([]string, 0, len(commands))
				for key := range commands {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					command := commands[key]
					if !validSkillName.MatchString(key) || (command.Source == nil) == (command.Content == nil) {
						warn(fmt.Errorf("command %q requires exactly one source or content", key))
						continue
					}
					path := "#commands:" + key
					if command.Source != nil {
						var err error
						path, err = pluginComponentPath(*command.Source, false)
						if err != nil || !strings.HasSuffix(path, ".md") {
							warn(fmt.Errorf("command %q requires a contained .md source", key))
							continue
						}
					}
					load(path, key, &command)
				}
			}
		}()
	}
}

func (c pluginCommand) apply(skill *Skill) error {
	for _, field := range []struct {
		value  *string
		target *string
		max    int
	}{{c.Description, &skill.Description, maxDescriptionBytes}, {c.ArgumentHint, &skill.ArgumentHint, maxArgumentHintBytes}, {c.Model, &skill.Model, 128}} {
		if field.value != nil {
			if len(*field.value) > field.max || !utf8.ValidString(*field.value) || strings.ContainsAny(*field.value, "\r\n\x00") {
				return fmt.Errorf("invalid command metadata override")
			}
			*field.target = strings.TrimSpace(*field.value)
		}
	}
	if c.AllowedTools != nil {
		data, _ := yaml.Marshal(*c.AllowedTools)
		var node yaml.Node
		if err := yaml.Unmarshal(data, &node); err != nil {
			return err
		}
		var err error
		skill.AllowedTools, err = parseSkillAllowedTools(*node.Content[0])
		if err != nil {
			return err
		}
	}
	return nil
}
