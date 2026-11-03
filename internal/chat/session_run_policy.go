package chat

import "slices"

// SessionRunPolicy records limits, never invocation pre-approvals. A nil
// ToolCeiling is unrestricted; an empty non-nil ceiling permits no tools.
type SessionRunPolicy struct {
	ToolCeiling    []string `json:"tool_ceiling"`
	DeniedRules    []string `json:"denied_rules,omitempty"`
	PermissionMode string   `json:"permission_mode"`
}

func CloneRunPolicy(policy *SessionRunPolicy) *SessionRunPolicy {
	if policy == nil {
		return nil
	}
	copy := *policy
	copy.ToolCeiling = slices.Clone(policy.ToolCeiling)
	copy.DeniedRules = slices.Clone(policy.DeniedRules)
	return &copy
}

func (s *Session) SetRunPolicy(policy *SessionRunPolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runPolicy = CloneRunPolicy(policy)
}

func (s *Session) RunPolicy() *SessionRunPolicy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return CloneRunPolicy(s.runPolicy)
}
