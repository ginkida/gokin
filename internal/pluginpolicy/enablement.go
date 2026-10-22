package pluginpolicy

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const MaxEnabledPlugins = 64

// MergePluginEnableJSON applies one settings layer using discovery's merge
// rules: null clears the map or deletes a key, restoring manifest defaults
// (or ordinary settings when a managed-tier key is removed).
func MergePluginEnableJSON(target map[string]bool, raw json.RawMessage, aliases ...PluginAliases) error {
	if len(raw) == 0 {
		return nil
	}
	if !json.Valid(raw) {
		return fmt.Errorf("enabledPlugins must be an object")
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return err
	}
	return MergePluginEnable(target, document.Content[0], aliases...)
}

// MergePluginEnable merges the parsed settings node without executing plugins.
func MergePluginEnable(target map[string]bool, node *yaml.Node, aliasMaps ...PluginAliases) error {
	if node == nil {
		return nil
	}
	if node.Tag == "!!null" {
		clear(target)
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("enabledPlugins must be an object")
	}
	indices := make([]int, 0, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		indices = append(indices, i)
	}
	aliases := PluginAliases{}
	if len(aliasMaps) > 0 {
		aliases = aliasMaps[0]
	}
	sort.SliceStable(indices, func(i, j int) bool {
		_, left, _ := aliases.Resolve(node.Content[indices[i]].Value)
		_, right, _ := aliases.Resolve(node.Content[indices[j]].Value)
		if left != right {
			return left && !right
		}
		return aliases.Depth(node.Content[indices[i]].Value) > aliases.Depth(node.Content[indices[j]].Value)
	})
	for _, i := range indices {
		id, value := node.Content[i].Value, node.Content[i+1]
		if len(id) > 256 || !ValidPluginID(id) {
			return fmt.Errorf("invalid plugin identifier in enabledPlugins")
		}
		resolved, _, err := aliases.Resolve(id)
		if err != nil {
			return err
		}
		if resolved == "" {
			continue
		}
		id = resolved
		if value.Tag == "!!null" {
			delete(target, id)
			continue
		}
		if value.Tag != "!!bool" {
			return fmt.Errorf("enabledPlugins[%q] must be a boolean", id)
		}
		target[id] = value.Value == "true"
	}
	if len(target) > MaxEnabledPlugins {
		return fmt.Errorf("enabledPlugins exceed %d entries", MaxEnabledPlugins)
	}
	return nil
}

func ValidPluginName(name string) bool {
	if name == "" || len(name) > 128 || !utf8.ValidString(name) || strings.ContainsAny(name, "@:/\\") {
		return false
	}
	for _, c := range name {
		if unicode.IsSpace(c) || unicode.IsControl(c) || c == 0x061c || c == 0x200e || c == 0x200f || c >= 0x202a && c <= 0x202e || c >= 0x2066 && c <= 0x2069 {
			return false
		}
	}
	return true
}

func ValidPluginID(id string) bool {
	parts := strings.Split(id, "@")
	return len(parts) == 2 && ValidPluginName(parts[0]) && ValidPluginName(parts[1])
}
