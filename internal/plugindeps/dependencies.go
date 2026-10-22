// Package plugindeps shares dependency parsing and offline runtime checks.
// It never fetches sources or changes enable settings.
package plugindeps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"gokin/internal/pluginpolicy"
)

type Dependency struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Marketplace string `json:"marketplace,omitempty"`
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var prereleaseComparator = regexp.MustCompile(`[vV]?[0-9]+\.[0-9]+\.[0-9]+-[0-9A-Za-z.-]+(?:\+[0-9A-Za-z.-]+)?`)

func validName(name string) bool {
	return namePattern.MatchString(name) && !strings.Contains(name, "..")
}

func Parse(raw json.RawMessage) ([]Dependency, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var entries []json.RawMessage
	if err := pluginpolicy.ValidateJSON(raw); err != nil {
		return nil, err
	}
	if json.Unmarshal(raw, &entries) != nil || len(entries) > 64 {
		return nil, fmt.Errorf("plugin dependencies must be an array of at most 64 entries")
	}
	result := make([]Dependency, 0, len(entries))
	seen := map[string]bool{}
	for _, entry := range entries {
		var dependency Dependency
		var value string
		if json.Unmarshal(entry, &value) == nil {
			var qualified bool
			dependency.Name, dependency.Marketplace, qualified = strings.Cut(value, "@")
			if qualified && dependency.Marketplace == "" {
				return nil, fmt.Errorf("dependency marketplace name is empty")
			}
		} else {
			decoder := json.NewDecoder(bytes.NewReader(entry))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&dependency) != nil {
				return nil, fmt.Errorf("dependency must be a name or an object with name, version and marketplace strings")
			}
			var fields map[string]json.RawMessage
			json.Unmarshal(entry, &fields)
			for _, raw := range fields {
				var field string
				if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &field) != nil {
					return nil, fmt.Errorf("dependency object fields must be strings")
				}
			}
		}
		if !validName(dependency.Name) || dependency.Marketplace != "" && !validName(dependency.Marketplace) {
			return nil, fmt.Errorf("invalid dependency identifier")
		}
		if err := ValidateRange(dependency.Version); err != nil {
			return nil, err
		}
		key := dependency.Name + "@" + dependency.Marketplace
		if seen[key] {
			return nil, fmt.Errorf("duplicate dependency %s", key)
		}
		seen[key] = true
		result = append(result, dependency)
	}
	return result, nil
}

func ValidateRange(value string) error {
	if len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") || strings.Count(value, "||") > 15 {
		return fmt.Errorf("dependency version range exceeds limits")
	}
	if value == "" {
		return nil
	}
	if _, err := semver.NewConstraint(value); err != nil {
		return fmt.Errorf("invalid dependency version range %q: %w", value, err)
	}
	return nil
}

func Satisfies(version, constraint string) bool {
	if constraint == "" {
		return true
	}
	v, err := semver.StrictNewVersion(strings.TrimPrefix(version, "v"))
	if err != nil || ValidateRange(constraint) != nil {
		return false
	}
	for _, branch := range strings.Split(constraint, "||") {
		c, err := semver.NewConstraint(branch)
		if err != nil || !c.Check(v) {
			continue
		}
		if v.Prerelease() == "" {
			return true
		}
		// npm admits a prerelease only when this AND branch explicitly names
		// a prerelease with the same major/minor/patch tuple.
		for _, comparator := range prereleaseComparator.FindAllString(branch, -1) {
			allowed, err := semver.StrictNewVersion(strings.TrimPrefix(strings.TrimPrefix(comparator, "v"), "V"))
			if err == nil && allowed.Major() == v.Major() && allowed.Minor() == v.Minor() && allowed.Patch() == v.Patch() {
				return true
			}
		}
	}
	return false
}

func (d Dependency) ID(parent string) string {
	market := d.Marketplace
	if market == "" {
		_, market, _ = strings.Cut(parent, "@")
	}
	return d.Name + "@" + market
}

type Node struct {
	ID           string
	Name         string
	Version      string
	Local        bool
	Enabled      bool
	Dependencies []Dependency
}

// Check returns errors for each unavailable dependent, propagating failures
// transitively while preserving unrelated plugins. Local copies satisfy by name
// and deliberately bypass version constraints.
func Check(nodes map[string]Node) map[string]string {
	errors := map[string]string{}
	state := map[string]int{}
	var visit func(string, int) bool
	visit = func(id string, depth int) bool {
		node, exists := nodes[id]
		if !exists || !node.Enabled {
			return false
		}
		if state[id] == 2 {
			return errors[id] == ""
		}
		if state[id] == 1 || depth > 64 {
			errors[id] = "circular or excessively deep plugin dependencies"
			return false
		}
		state[id] = 1
		for _, dependency := range node.Dependencies {
			target := dependency.ID(id)
			dep, found := nodes[target]
			for _, localID := range []string{dependency.Name + "@inline", dependency.Name + "@skills-dir"} {
				if local, exists := nodes[localID]; exists && local.Local {
					dep, target, found = local, localID, true
					break
				}
			}
			if !found || !dep.Enabled {
				errors[id] = fmt.Sprintf("Requires %q: not installed or disabled — enable it or remove the dependency", target)
				break
			}
			if !dep.Local && !Satisfies(dep.Version, dependency.Version) {
				errors[id] = fmt.Sprintf("Requires %q %s, installed %s", target, dependency.Version, dep.Version)
				break
			}
			if !visit(target, depth+1) {
				errors[id] = fmt.Sprintf("Requires %q: %s", target, errors[target])
				break
			}
		}
		state[id] = 2
		return errors[id] == ""
	}
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		visit(id, 0)
	}
	return errors
}
