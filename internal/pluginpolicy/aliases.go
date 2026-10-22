package pluginpolicy

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const PluginAliasesField = "gokinPluginRenames"

type PluginAliases map[string]*string

var aliasName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func aliasID(id string) bool {
	parts := strings.Split(id, "@")
	return len(parts) == 2 && len(id) <= 256 && aliasName.MatchString(parts[0]) && aliasName.MatchString(parts[1]) && !strings.Contains(id, "..")
}

func ReadPluginAliases(doc map[string]json.RawMessage) (PluginAliases, error) {
	aliases := PluginAliases{}
	if raw, set := doc[PluginAliasesField]; set {
		if err := ValidateJSON(raw); err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &aliases) != nil || aliases == nil || len(aliases) > 4096 {
			return nil, fmt.Errorf("invalid plugin rename registry")
		}
	}
	for from, to := range aliases {
		if !aliasID(from) || to != nil && (!aliasID(*to) || strings.SplitN(from, "@", 2)[1] != strings.SplitN(*to, "@", 2)[1]) {
			return nil, fmt.Errorf("invalid plugin rename identifier")
		}
		if _, _, err := aliases.Resolve(from); err != nil {
			return nil, err
		}
	}
	return aliases, nil
}

// Resolve returns an empty target for a catalog removal. Unknown identifiers
// retain their original name. Chains are bounded even for user-edited state.
func (a PluginAliases) Resolve(id string) (string, bool, error) {
	current := id
	seen := map[string]bool{}
	for {
		if seen[current] || len(seen) > 4096 {
			return "", false, fmt.Errorf("plugin rename chain does not resolve")
		}
		seen[current] = true
		next, set := a[current]
		if !set {
			return current, current != id, nil
		}
		if next == nil {
			return "", true, nil
		}
		current = *next
	}
}

func (a PluginAliases) Depth(id string) int {
	for depth := 0; depth <= len(a); depth++ {
		next, set := a[id]
		if !set {
			return depth
		}
		if next == nil {
			return depth + 1
		}
		id = *next
	}
	return len(a) + 1
}

// RenameFlags gives an explicit current-name value precedence over aliases,
// while preserving a false value from managed settings.
func (a PluginAliases) RenameFlags(flags map[string]bool) map[string]bool {
	out := map[string]bool{}
	var keys []string
	for id := range flags {
		keys = append(keys, id)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := a.Depth(keys[i]), a.Depth(keys[j])
		if left != right {
			return left < right
		}
		return keys[i] < keys[j]
	})
	for _, id := range keys {
		if _, set := a[id]; !set {
			out[id] = flags[id]
		}
	}
	for _, id := range keys {
		target, changed, err := a.Resolve(id)
		if err == nil && changed && target != "" {
			if _, set := out[target]; !set {
				out[target] = flags[id]
			}
		}
	}
	return out
}

func (a PluginAliases) IsTarget(id string) bool {
	for from := range a {
		target, _, err := a.Resolve(from)
		if err == nil && target == id {
			return true
		}
	}
	return false
}

// Apply aliases inside each settings document before scope/document
// precedence. An explicit destination in that same document wins conflicts.
func (a PluginAliases) RenameRawKeys(values map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	var ids []string
	for id := range values {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := a.Depth(ids[i]), a.Depth(ids[j])
		if left != right {
			return left < right
		}
		return ids[i] < ids[j]
	})
	for _, id := range ids {
		if _, set := a[id]; !set {
			out[id] = values[id]
		}
	}
	for _, id := range ids {
		target, changed, err := a.Resolve(id)
		if err == nil && changed && target != "" {
			if _, set := out[target]; !set {
				out[target] = values[id]
			}
		}
	}
	return out
}

func (a PluginAliases) EnabledDocuments(documents [][]byte) ([][]byte, error) {
	result := make([][]byte, 0, len(documents))
	for _, data := range documents {
		if err := ValidateJSON(data); err != nil {
			return nil, err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return nil, err
		}
		if raw, set := fields["enabledPlugins"]; set && string(raw) != "null" {
			var values map[string]json.RawMessage
			if json.Unmarshal(raw, &values) != nil || values == nil {
				return nil, fmt.Errorf("enabledPlugins must be an object")
			}
			fields["enabledPlugins"], _ = json.Marshal(a.RenameRawKeys(values))
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		result = append(result, encoded)
	}
	return result, nil
}
