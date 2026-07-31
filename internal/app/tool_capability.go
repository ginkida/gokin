package app

import (
	"fmt"
	"sort"
	"strings"

	"gokin/internal/permission"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

// ConfigureRunPermissionRules installs process-scoped CLI pre-approval and
// deny rules on the shared manager used by foreground and delegated agents.
// The parser is shared with SKILL.md allowed-tools so pattern behavior cannot
// drift between the two entry points.
func (a *App) ConfigureRunPermissionRules(allowed, denied []string) error {
	if a == nil || a.permManager == nil {
		return fmt.Errorf("configure run permission rules: permission manager is not initialized")
	}
	canonicalAllowed, err := permission.CanonicalizeTemporaryToolGrants(allowed)
	if err != nil {
		return fmt.Errorf("configure allowedTools: %w", err)
	}
	canonicalDenied, err := permission.CanonicalizeTemporaryToolDenies(denied)
	if err != nil {
		return fmt.Errorf("configure disallowedTools: %w", err)
	}
	a.permManager.SetRunToolRules(canonicalAllowed, canonicalDenied)
	return nil
}

// ConfigureToolCapability applies a process-scoped tool ceiling. A nil allow
// and deny list means unrestricted. A non-nil empty allow list means no tools.
// Denies always win over allows. Unknown names fail closed so a typo cannot
// silently broaden an unattended run.
func (a *App) ConfigureToolCapability(allowed, denied []string) error {
	if a == nil {
		return fmt.Errorf("configure tool capability: app is nil")
	}
	restricted := allowed != nil || denied != nil
	if restricted && a.registry == nil {
		return fmt.Errorf("configure tool capability: tool registry is not initialized")
	}

	var ceiling []string
	if restricted {
		var err error
		ceiling, err = resolveToolCapabilityCeiling(a.registry.Names(), allowed, denied)
		if err != nil {
			return err
		}
	}

	a.mu.Lock()
	a.toolCapabilityRestricted = restricted
	if restricted {
		a.toolCapabilityCeiling = make([]string, len(ceiling))
		copy(a.toolCapabilityCeiling, ceiling)
		a.toolCapabilityAllowInput = append([]string(nil), allowed...)
		a.toolCapabilityDenyInput = append([]string(nil), denied...)
		if allowed == nil {
			a.toolCapabilityAllowInput = nil
		}
	} else {
		a.toolCapabilityCeiling = nil
		a.toolCapabilityAllowInput = nil
		a.toolCapabilityDenyInput = nil
	}
	a.mu.Unlock()

	if a.client != nil {
		a.client.SetTools(a.toolsForCurrentMode())
	}
	return nil
}

// refreshToolCapabilityCeiling recomputes the ceiling against the CURRENT
// registry. It is a no-op unless a ceiling is installed, and it deliberately
// ignores the unknown-name error: startup already rejected typos, and a tool
// that disappeared from the registry at runtime (an MCP server going away) must
// not fail the run.
func (a *App) refreshToolCapabilityCeiling() {
	if a == nil || a.registry == nil {
		return
	}
	a.mu.Lock()
	restricted := a.toolCapabilityRestricted
	allowed := append([]string(nil), a.toolCapabilityAllowInput...)
	if a.toolCapabilityAllowInput == nil {
		allowed = nil
	}
	denied := append([]string(nil), a.toolCapabilityDenyInput...)
	a.mu.Unlock()
	if !restricted {
		return
	}

	available := a.registry.Names()
	ceiling, err := resolveToolCapabilityCeiling(available, allowed, denied)
	if err != nil {
		// Recompute ignoring names the registry no longer offers. A nil allow
		// input must STAY nil — intersecting it would turn "everything minus
		// the denies" into an empty allowlist and disable every tool.
		knownAllowed := allowed
		if allowed != nil {
			knownAllowed = intersectCapabilityNames(allowed, available)
		}
		ceiling, err = resolveToolCapabilityCeiling(
			available, knownAllowed, intersectCapabilityNames(denied, available))
		if err != nil {
			return
		}
	}
	a.mu.Lock()
	a.toolCapabilityCeiling = ceiling
	a.mu.Unlock()
}

func (a *App) toolCapabilitySnapshot() ([]string, bool) {
	if a == nil {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.toolCapabilityRestricted {
		return nil, false
	}
	ceiling := make([]string, len(a.toolCapabilityCeiling))
	copy(ceiling, a.toolCapabilityCeiling)
	return ceiling, true
}

func resolveToolCapabilityCeiling(available, allowed, denied []string) ([]string, error) {
	availableSet := make(map[string]struct{}, len(available))
	for _, name := range available {
		name = strings.TrimSpace(name)
		if name != "" {
			availableSet[name] = struct{}{}
		}
	}

	unknownSet := map[string]struct{}{}
	normalizeAndValidate := func(values []string) map[string]struct{} {
		result := map[string]struct{}{}
		for _, raw := range values {
			name := strings.TrimSpace(raw)
			if name == "" {
				continue
			}
			if _, ok := availableSet[name]; !ok {
				unknownSet[name] = struct{}{}
				continue
			}
			result[name] = struct{}{}
		}
		return result
	}
	allowedSet := normalizeAndValidate(allowed)
	deniedSet := normalizeAndValidate(denied)

	if len(unknownSet) > 0 {
		unknown := make([]string, 0, len(unknownSet))
		for name := range unknownSet {
			unknown = append(unknown, name)
		}
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown tool name(s): %s", strings.Join(unknown, ", "))
	}

	effective := map[string]struct{}{}
	if allowed == nil {
		for name := range availableSet {
			effective[name] = struct{}{}
		}
	} else {
		for name := range allowedSet {
			effective[name] = struct{}{}
		}
	}
	for name := range deniedSet {
		delete(effective, name)
	}

	ceiling := make([]string, 0, len(effective))
	for name := range effective {
		ceiling = append(ceiling, name)
	}
	sort.Strings(ceiling)
	return ceiling, nil
}

// filterToolSchemaByCeiling delegates to the shared implementation so the
// startup schema, the router's per-request schema, and any future SetTools call
// site cannot drift apart in what "outside the ceiling" means.
func filterToolSchemaByCeiling(base []*genai.Tool, ceiling []string) []*genai.Tool {
	return tools.FilterGeminiToolsByCapability(base, ceiling)
}

func intersectCapabilityNames(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, name := range right {
		rightSet[name] = struct{}{}
	}
	result := make([]string, 0, min(len(left), len(right)))
	for _, name := range left {
		if _, ok := rightSet[name]; ok {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}
