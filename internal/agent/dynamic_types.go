package agent

import (
	"fmt"
	"sort"
	"sync"

	"gokin/internal/tools"
)

// DynamicAgentType represents a user-defined agent type.
type DynamicAgentType struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	AllowedTools []string `json:"allowed_tools"`
	SystemPrompt string   `json:"system_prompt"`
	Priority     int      `json:"priority"` // Higher = evaluated first

	// Source records where the type was defined: "runtime"
	// (/register-agent-type), "project" (<workDir>/.gokin/agents/*.md) or
	// "global" (<configDir>/agents/*.md). Surfaced by /list-agent-types.
	Source string `json:"source,omitempty"`
}

// AgentTypeRegistry manages both built-in and dynamic agent types.
type AgentTypeRegistry struct {
	builtin map[AgentType]bool
	dynamic map[string]*DynamicAgentType
	mu      sync.RWMutex
}

// NewAgentTypeRegistry creates a new registry with built-in types.
func NewAgentTypeRegistry() *AgentTypeRegistry {
	return &AgentTypeRegistry{
		builtin: map[AgentType]bool{
			AgentTypeExplore: true,
			AgentTypeBash:    true,
			AgentTypeGeneral: true,
			AgentTypePlan:    true,
			AgentTypeGuide:   true,
		},
		dynamic: make(map[string]*DynamicAgentType),
	}
}

// RegisterDynamic registers a new dynamic agent type.
func (r *AgentTypeRegistry) RegisterDynamic(name, description string, tools []string, prompt string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check for conflict with built-in types
	if r.builtin[AgentType(name)] {
		return fmt.Errorf("cannot override built-in agent type: %s", name)
	}

	r.dynamic[name] = &DynamicAgentType{
		Name:         name,
		Description:  description,
		AllowedTools: append([]string(nil), tools...),
		SystemPrompt: prompt,
		Source:       "runtime",
	}

	return nil
}

// RegisterDynamicType registers a fully-specified dynamic type (file-based
// loading path — carries Source/Priority). Built-in names still win.
func (r *AgentTypeRegistry) RegisterDynamicType(dt *DynamicAgentType) error {
	if dt == nil || dt.Name == "" {
		return fmt.Errorf("agent type requires a name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.builtin[AgentType(dt.Name)] {
		return fmt.Errorf("cannot override built-in agent type: %s", dt.Name)
	}
	r.dynamic[dt.Name] = cloneDynamicAgentType(dt)
	return nil
}

// UnregisterDynamic removes a dynamic agent type.
func (r *AgentTypeRegistry) UnregisterDynamic(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.dynamic[name]; !ok {
		return fmt.Errorf("dynamic type not found: %s", name)
	}

	delete(r.dynamic, name)
	return nil
}

// GetDynamic returns a dynamic agent type by name.
func (r *AgentTypeRegistry) GetDynamic(name string) (*DynamicAgentType, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	dt, ok := r.dynamic[name]
	return cloneDynamicAgentType(dt), ok
}

// IsBuiltin checks if a type is a built-in type.
func (r *AgentTypeRegistry) IsBuiltin(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.builtin[AgentType(name)]
}

// IsDynamic checks if a type is a dynamic type.
func (r *AgentTypeRegistry) IsDynamic(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.dynamic[name]
	return ok
}

// Exists checks if a type (built-in or dynamic) exists.
func (r *AgentTypeRegistry) Exists(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.builtin[AgentType(name)] || r.dynamic[name] != nil
}

// ListDynamic returns all dynamic agent types.
func (r *AgentTypeRegistry) ListDynamic() []*DynamicAgentType {
	r.mu.RLock()
	defer r.mu.RUnlock()

	types := make([]*DynamicAgentType, 0, len(r.dynamic))
	for _, dt := range r.dynamic {
		types = append(types, cloneDynamicAgentType(dt))
	}
	sort.Slice(types, func(i, j int) bool { return types[i].Name < types[j].Name })
	return types
}

// ListAll returns all agent type names (both built-in and dynamic).
func (r *AgentTypeRegistry) ListAll() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.builtin)+len(r.dynamic))

	for t := range r.builtin {
		names = append(names, string(t))
	}
	for name := range r.dynamic {
		names = append(names, name)
	}

	sort.Strings(names)
	return names
}

// SnapshotAgentTypes returns one deterministic, immutable-by-copy view of the
// complete catalog for model-facing tools. Holding one read lock across names
// and descriptions prevents a concurrent registration from producing a mixed
// declaration (for example, an enum entry with an empty description).
func (r *AgentTypeRegistry) SnapshotAgentTypes() []tools.AgentTypeDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	types := make([]tools.AgentTypeDefinition, 0, len(r.builtin)+len(r.dynamic))
	for agentType := range r.builtin {
		types = append(types, tools.AgentTypeDefinition{
			Name:        string(agentType),
			Description: builtinAgentTypeDescription(agentType),
		})
	}
	for _, dynamicType := range r.dynamic {
		types = append(types, tools.AgentTypeDefinition{
			Name:        dynamicType.Name,
			Description: dynamicType.Description,
		})
	}
	sort.Slice(types, func(i, j int) bool { return types[i].Name < types[j].Name })
	return types
}

// GetToolsForType returns the allowed tools for a type.
func (r *AgentTypeRegistry) GetToolsForType(name string) []string {
	// Check dynamic first
	if dt, ok := r.GetDynamic(name); ok {
		return dt.AllowedTools
	}

	// Fall back to built-in
	return AgentType(name).AllowedTools()
}

// GetPromptForType returns the system prompt for a type.
func (r *AgentTypeRegistry) GetPromptForType(name string) string {
	if dt, ok := r.GetDynamic(name); ok {
		return dt.SystemPrompt
	}
	return "" // Built-in types use their own prompt builders
}

// GetDescriptionForType returns the description for a type.
func (r *AgentTypeRegistry) GetDescriptionForType(name string) string {
	if dt, ok := r.GetDynamic(name); ok {
		return dt.Description
	}

	return builtinAgentTypeDescription(AgentType(name))
}

func builtinAgentTypeDescription(agentType AgentType) string {
	switch agentType {
	case AgentTypeExplore:
		return "Explore and analyze codebases"
	case AgentTypeBash:
		return "Execute shell commands"
	case AgentTypeGeneral:
		return "General-purpose agent with full tool access"
	case AgentTypePlan:
		return "Design implementation strategies"
	case AgentTypeGuide:
		return "Answer questions about the CLI"
	default:
		return ""
	}
}

func cloneDynamicAgentType(dt *DynamicAgentType) *DynamicAgentType {
	if dt == nil {
		return nil
	}
	clone := *dt
	clone.AllowedTools = append([]string(nil), dt.AllowedTools...)
	return &clone
}
