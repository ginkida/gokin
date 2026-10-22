package pluginpolicy

import (
	"encoding/json"
	"fmt"
)

// MergePluginManifest combines a plugin manifest with its marketplace entry
// without editing the producer's directory. Callers supply parsed JSON objects.
func MergePluginManifest(manifest, entry map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	var entryName string
	var strict *bool
	if err := json.Unmarshal(entry["name"], &entryName); err != nil {
		return nil, fmt.Errorf("entry requires a name")
	}
	if raw, exists := entry["strict"]; exists {
		if err := json.Unmarshal(raw, &strict); err != nil {
			return nil, err
		}
	}
	if manifest == nil {
		manifest = map[string]json.RawMessage{}
	}
	present := len(manifest) > 0
	fields := []string{"commands", "agents", "skills", "hooks", "outputStyles", "themes"}
	if !present {
		manifest = map[string]json.RawMessage{}
		for key, raw := range entry {
			switch key {
			case "source", "strict", "headers", "headersHelper", "relevance", "category", "tags":
				continue
			}
			manifest[key] = raw
		}
		manifest["name"], _ = json.Marshal(entryName)
	} else {
		for _, key := range fields {
			additional, exists := entry[key]
			if !exists {
				continue
			}
			if strict != nil && !*strict {
				return nil, fmt.Errorf("marketplace component %s conflicts with plugin.json when strict is false", key)
			}
			if key == "hooks" {
				var incoming map[string]json.RawMessage
				if json.Unmarshal(additional, &incoming) != nil {
					return nil, fmt.Errorf("marketplace entry hooks must be inline")
				}
				var existing map[string]json.RawMessage
				current := manifest[key]
				if len(current) > 0 && json.Unmarshal(current, &existing) != nil {
					return nil, fmt.Errorf("marketplace hooks cannot override non-inline manifest hooks")
				}
				if existing == nil {
					existing = map[string]json.RawMessage{}
				}
				for event, handlers := range incoming {
					existing[event] = handlers
				}
				manifest[key], _ = json.Marshal(existing)
			} else if current, exists := manifest[key]; exists {
				var left, right []json.RawMessage
				if json.Unmarshal(current, &left) != nil {
					left = []json.RawMessage{current}
				}
				if json.Unmarshal(additional, &right) != nil {
					right = []json.RawMessage{additional}
				}
				manifest[key], _ = json.Marshal(append(left, right...))
			} else {
				manifest[key] = additional
			}
		}
	}
	if raw, exists := entry["defaultEnabled"]; exists {
		manifest["defaultEnabled"] = raw
	}
	if raw, exists := entry["dependencies"]; exists {
		if _, declared := manifest["dependencies"]; !declared {
			manifest["dependencies"] = raw
		}
	}
	return manifest, nil
}
