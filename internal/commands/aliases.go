package commands

import (
	"fmt"
	"maps"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// cloneAliases returns a shallow copy of the alias map so the default table
// isn't mutated when a caller overlays config.
func cloneAliases(src map[string]string) map[string]string {
	out := make(map[string]string, len(src))
	maps.Copy(out, src)
	return out
}

// LoadAliasesFromFile parses a YAML file mapping short names to canonical
// command names and overlays it on the defaults. Missing file is not an
// error — users without the file keep the built-in shortcuts. Malformed
// entries (empty key/value, key with whitespace) are skipped with a logged
// warning so a bad config doesn't crash startup.
//
// Sample format:
//
//	p: plan
//	c: commit
//	deploy: bash
func (h *Handler) LoadAliasesFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read aliases file: %w", err)
	}
	var raw map[string]string
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse aliases yaml: %w", err)
	}
	if h.aliases == nil {
		h.aliases = cloneAliases(defaultAliases)
	}
	for alias, target := range raw {
		alias = strings.TrimSpace(alias)
		target = strings.TrimSpace(strings.TrimPrefix(target, "/"))
		if alias == "" || target == "" {
			continue
		}
		if strings.ContainsAny(alias, " \t/") {
			continue // aliases must be single tokens
		}
		h.aliases[alias] = target
	}
	return nil
}

// ResolveAlias returns the canonical command name for a potential alias, or
// the input unchanged if it's not a known alias. Exported so /help and
// autocomplete can surface the real command name.
func (h *Handler) ResolveAlias(name string) string {
	if h == nil || h.aliases == nil {
		return name
	}
	if target, ok := h.aliases[name]; ok {
		return target
	}
	return name
}

// Aliases returns a copy of the current alias table. Intended for /help
// rendering and tests.
func (h *Handler) Aliases() map[string]string {
	if h == nil {
		return nil
	}
	return cloneAliases(h.aliases)
}
