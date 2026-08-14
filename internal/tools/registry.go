package tools

import (
	"fmt"
	"sort"
	"sync"

	"gokin/internal/logging"

	"google.golang.org/genai"
)

// Registry manages the collection of available tools.
type Registry struct {
	tools                       map[string]Tool
	staticDeclarations          map[string]*genai.FunctionDeclaration
	declarationSnapshotCache    []registryDeclarationSnapshot
	declarationRevision         uint64
	declarationSnapshotRevision uint64
	mu                          sync.RWMutex
}

// NewRegistry creates a new tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools:              make(map[string]Tool),
		staticDeclarations: make(map[string]*genai.FunctionDeclaration),
	}
}

// Get retrieves a tool by name (read-optimized with RLock).
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, ok := r.tools[name]
	return tool, ok
}

// List returns all registered tools (read-optimized).
func (r *Registry) List() []Tool {
	snapshots := r.cachedDeclarationSnapshots()
	registered := make([]Tool, len(snapshots))
	for index, snapshot := range snapshots {
		registered[index] = snapshot.tool
	}
	return registered
}

// Names returns the names of all registered tools (read-optimized).
func (r *Registry) Names() []string {
	snapshots := r.cachedDeclarationSnapshots()
	names := make([]string, len(snapshots))
	for index, snapshot := range snapshots {
		names[index] = snapshot.name
	}
	return names
}

// Declarations returns all tool declarations for Gemini (read-optimized).
func (r *Registry) Declarations() []*genai.FunctionDeclaration {
	snapshots := r.cachedDeclarationSnapshots()
	declarations := make([]*genai.FunctionDeclaration, 0, len(snapshots))
	for _, snapshot := range snapshots {
		declaration := snapshot.declaration
		if declaration == nil {
			declaration = snapshot.tool.Declaration()
		}
		if declaration != nil {
			declarations = append(declarations, declaration)
		}
	}
	sortDeclarationsByName(declarations)
	return declarations
}

type registryDeclarationSnapshot struct {
	name        string
	tool        Tool
	declaration *genai.FunctionDeclaration
}

// cachedDeclarationSnapshots returns an immutable, name-ordered registry view.
// Register, Unregister, and declaration freezing replace this slice instead of
// mutating it, so callers may safely retain the returned view after the lock is
// released and invoke third-party Declaration methods without deadlocking the
// registry. The common per-request path avoids rebuilding ~60 snapshot records.
func (r *Registry) cachedDeclarationSnapshots() []registryDeclarationSnapshot {
	r.mu.RLock()
	if r.declarationSnapshotRevision == r.declarationRevision {
		cached := r.declarationSnapshotCache
		r.mu.RUnlock()
		return cached
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.declarationSnapshotRevision != r.declarationRevision {
		snapshots := make([]registryDeclarationSnapshot, 0, len(r.tools))
		for name, tool := range r.tools {
			snapshots = append(snapshots, registryDeclarationSnapshot{
				name: name, tool: tool, declaration: r.staticDeclarations[name],
			})
		}
		sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].name < snapshots[j].name })
		r.declarationSnapshotCache = snapshots
		r.declarationSnapshotRevision = r.declarationRevision
	}
	return r.declarationSnapshotCache
}

func sortDeclarationsByName(declarations []*genai.FunctionDeclaration) {
	// Snapshot-backed eager paths are already ordered by registry name, and a
	// well-formed tool uses that same name in its declaration. Avoid paying the
	// reflection allocations of sort.SliceStable on every model round. Lazy or
	// third-party declarations whose published names differ still take the exact
	// stable-sort fallback below.
	ordered := true
	for index := 1; index < len(declarations); index++ {
		if declarationNameLess(declarations[index], declarations[index-1]) {
			ordered = false
			break
		}
	}
	if ordered {
		return
	}
	sort.SliceStable(declarations, func(i, j int) bool {
		return declarationNameLess(declarations[i], declarations[j])
	})
}

func declarationNameLess(left, right *genai.FunctionDeclaration) bool {
	if left == nil {
		return false
	}
	if right == nil {
		return true
	}
	return left.Name < right.Name
}

func sortToolsByName(registered []Tool) {
	ordered := true
	for index := 1; index < len(registered); index++ {
		if toolNameLess(registered[index], registered[index-1]) {
			ordered = false
			break
		}
	}
	if ordered {
		return
	}
	sort.SliceStable(registered, func(i, j int) bool {
		return toolNameLess(registered[i], registered[j])
	})
}

func toolNameLess(left, right Tool) bool {
	if left == nil {
		return false
	}
	if right == nil {
		return true
	}
	return left.Name() < right.Name()
}

// Register adds a tool to the registry.
func (r *Registry) Register(tool Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool already registered: %s", name)
	}

	r.tools[name] = tool
	r.declarationRevision++
	return nil
}

// MustRegister adds a tool to the registry and logs a warning on error.
func (r *Registry) MustRegister(tool Tool) {
	if err := r.Register(tool); err != nil {
		logging.Warn("failed to register tool", "tool", tool.Name(), "error", err)
	}
}

// Unregister removes a tool by name. Returns true if a tool was removed.
// Used when MCP servers disconnect so their tools stop appearing in the
// registry exposed to the model.
func (r *Registry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; !exists {
		return false
	}
	delete(r.tools, name)
	delete(r.staticDeclarations, name)
	r.declarationRevision++
	return true
}

// runtimeDynamicDeclaration marks built-ins whose model-facing schema can
// change without a Register/Unregister operation. DefaultRegistry freezes every
// other built-in declaration once before publication; public Register remains
// dynamic for MCP and third-party tools regardless of this marker.
type runtimeDynamicDeclaration interface {
	runtimeDynamicDeclaration()
}

// freezeDefaultDeclarations is called only while DefaultRegistry is still
// thread-confined. It removes hundreds of nested genai.Schema allocations from
// every request while keeping explicitly dynamic schemas live.
func (r *Registry) freezeDefaultDeclarations() {
	snapshots := r.cachedDeclarationSnapshots()
	frozen := make(map[string]*genai.FunctionDeclaration, len(snapshots))
	for _, snapshot := range snapshots {
		if _, dynamic := snapshot.tool.(runtimeDynamicDeclaration); dynamic {
			continue
		}
		if declaration := snapshot.tool.Declaration(); declaration != nil {
			frozen[snapshot.name] = declaration
		}
	}
	r.mu.Lock()
	for name, declaration := range frozen {
		// No concurrent mutation is permitted before publication, but retain the
		// membership check so future constructor refactors fail closed.
		if _, exists := r.tools[name]; exists {
			r.staticDeclarations[name] = declaration
		}
	}
	// declarationSnapshots may already have populated a pre-freeze view.
	// Publish a new revision so the next reader observes the frozen pointers.
	r.declarationRevision++
	r.mu.Unlock()
}

// GeminiTools returns the tools in Gemini format.
func (r *Registry) GeminiTools() []*genai.Tool {
	return []*genai.Tool{
		{
			FunctionDeclarations: r.Declarations(),
		},
	}
}

// PlanModeControlToolNames are the interactive plan-mode tools. They are a
// SUBSET of ToolSetPlanning (which also bundles the task* background-agent
// tools), so they're listed explicitly — gating the whole set would wrongly
// drop task/task_output/task_stop. Exported so the app can fold them into a
// combined feature-gated exclusion set (plan-off + memory-off, …).
var PlanModeControlToolNames = map[string]bool{
	"enter_plan_mode":      true,
	"exit_plan_mode":       true,
	"update_plan_progress": true,
	"get_plan_status":      true,
	"undo_plan":            true,
	"redo_plan":            true,
}

// GeminiToolsExcluding returns the full tool envelope minus the named tools.
// Used to drop FEATURE-GATED tools the model must not call because the feature
// is off in config — plan-mode control tools when plan.enabled is false, the
// memory/memorize tools when memory.enabled is false. Offering a disabled tool
// makes the model call it and hit a confusing "unavailable" error.
func (r *Registry) GeminiToolsExcluding(exclude map[string]bool) []*genai.Tool {
	snapshots := r.cachedDeclarationSnapshots()
	kept := make([]*genai.FunctionDeclaration, 0, len(snapshots))
	for _, snapshot := range snapshots {
		declaration := snapshot.declaration
		if declaration == nil {
			declaration = snapshot.tool.Declaration()
		}
		// Preserve the public contract for third-party tools whose declaration
		// name differs from their registry key: exclusions apply to the name the
		// model actually sees, not to the internal lookup key.
		if declaration == nil || exclude[declaration.Name] {
			continue
		}
		kept = append(kept, declaration)
	}
	sortDeclarationsByName(kept)
	return []*genai.Tool{{FunctionDeclarations: kept}}
}

// GeminiToolsExcludingPlanMode drops the interactive plan-mode control tools
// (the model can't enter_plan_mode and strand itself read-only with no
// interactive plan approval, e.g. in headless/eval runs).
func (r *Registry) GeminiToolsExcludingPlanMode() []*genai.Tool {
	return r.GeminiToolsExcluding(PlanModeControlToolNames)
}

// ToolSet defines a named group of tools.
type ToolSet string

const (
	// ToolSetCore contains essential tools always available.
	ToolSetCore ToolSet = "core"
	// ToolSetGit contains git-related tools.
	ToolSetGit ToolSet = "git"
	// ToolSetPlanning contains plan mode tools.
	ToolSetPlanning ToolSet = "planning"
	// ToolSetAgent contains agent/coordination tools.
	ToolSetAgent ToolSet = "agent"
	// ToolSetWeb contains web fetch/search tools.
	ToolSetWeb ToolSet = "web"
	// ToolSetAdvanced contains advanced code analysis tools.
	ToolSetAdvanced ToolSet = "advanced"
	// ToolSetHybrid exposes the request-scoped read-only computation plane.
	ToolSetHybrid ToolSet = "hybrid"
	// ToolSetHarness exposes direct continual-harness administration. Auto mode
	// keeps this internal to rlm.harness; explicit hybrid mode may advertise it.
	ToolSetHarness ToolSet = "harness"
	// ToolSetMemory contains memory and context tools.
	ToolSetMemory ToolSet = "memory"
	// ToolSetFileOps contains file management tools beyond core read/write/edit.
	ToolSetFileOps ToolSet = "fileops"
	// ToolSetOllamaCore is a minimal set for Ollama models.
	ToolSetOllamaCore ToolSet = "ollama_core"
)

type toolSetMask uint16

const (
	toolSetMaskCore toolSetMask = 1 << iota
	toolSetMaskGit
	toolSetMaskPlanning
	toolSetMaskAgent
	toolSetMaskWeb
	toolSetMaskAdvanced
	toolSetMaskHybrid
	toolSetMaskHarness
	toolSetMaskMemory
	toolSetMaskFileOps
	toolSetMaskOllamaCore
)

var toolSetMasks = map[ToolSet]toolSetMask{
	ToolSetCore:       toolSetMaskCore,
	ToolSetGit:        toolSetMaskGit,
	ToolSetPlanning:   toolSetMaskPlanning,
	ToolSetAgent:      toolSetMaskAgent,
	ToolSetWeb:        toolSetMaskWeb,
	ToolSetAdvanced:   toolSetMaskAdvanced,
	ToolSetHybrid:     toolSetMaskHybrid,
	ToolSetHarness:    toolSetMaskHarness,
	ToolSetMemory:     toolSetMaskMemory,
	ToolSetFileOps:    toolSetMaskFileOps,
	ToolSetOllamaCore: toolSetMaskOllamaCore,
}

// toolSetDefinitions maps tool sets to their member tool names.
var toolSetDefinitions = map[ToolSet][]string{
	ToolSetCore: {
		"read", "write", "edit", "bash", "glob", "grep",
		"ask_user", "list_dir", "tree", "diff", "todo",
		"tools_list", "skill",
		// mcp_admin is read-only inspection of the MCP control plane.
		// Keeping it in Core means the model can always answer "is MCP
		// set up?" / "which servers do I have?" — even in plan mode —
		// without depending on an extra tool set being active.
		"mcp_admin",
		// Semantic discovery tools — fundamental for code understanding.
		"go_to_definition", "find_references", "go_search",
		// request_tool moved to ToolSetAgent — it was shipping in the
		// Core declaration list for main-agent Kimi requests, but the
		// requester dependency is only wired in the sub-agent path.
		// Result: Kimi saw it, tried it, got "tool requester not
		// initialized" error, looked like a silent failure. Sub-agents
		// that genuinely need it still get it via ToolSetAgent.
	},
	ToolSetGit: {
		"git_status", "git_diff", "git_add", "git_commit",
		"git_log", "git_blame", "git_branch", "git_pr",
		"review_changes",
	},
	ToolSetPlanning: {
		"enter_plan_mode", "update_plan_progress", "get_plan_status",
		"exit_plan_mode", "undo_plan", "redo_plan",
		"task", "task_output", "task_stop", "loop_control",
	},
	ToolSetAgent: {
		"ask_agent", "coordinate", "shared_memory", "update_scratchpad",
		"request_tool",
	},
	ToolSetWeb: {
		"web_fetch", "web_search",
	},
	ToolSetAdvanced: {
		"batch", "refactor", "check_impact",
		"verify_code", "run_tests",
	},
	ToolSetHybrid: {
		"repl_exec",
	},
	ToolSetHarness: {
		"harness",
	},
	ToolSetMemory: {
		"memory", "memorize", "pin_context", "history_search",
	},
	ToolSetFileOps: {
		"copy", "move", "delete", "mkdir",
		"env", "kill_shell", "ssh",
	},
	ToolSetOllamaCore: {
		"read", "write", "edit", "bash", "glob", "grep",
		"ask_user", "list_dir", "todo", "skill",
	},
}

var toolSetMembershipByName = buildToolSetMembership()

func buildToolSetMembership() map[string]toolSetMask {
	membership := make(map[string]toolSetMask)
	for set, names := range toolSetDefinitions {
		mask := toolSetMasks[set]
		for _, name := range names {
			membership[name] |= mask
		}
	}
	return membership
}

func selectedToolSetMask(sets []ToolSet) (toolSetMask, int) {
	var selected toolSetMask
	capacity := 0
	for _, set := range sets {
		selected |= toolSetMasks[set]
		capacity += len(toolSetDefinitions[set])
	}
	return selected, capacity
}

// FilteredDeclarations returns declarations for only the specified tool sets.
func (r *Registry) FilteredDeclarations(sets ...ToolSet) []*genai.FunctionDeclaration {
	selected, capacity := selectedToolSetMask(sets)
	snapshots := r.cachedDeclarationSnapshots()
	if capacity > len(snapshots) {
		capacity = len(snapshots)
	}
	decls := make([]*genai.FunctionDeclaration, 0, capacity)
	for _, snapshot := range snapshots {
		if toolSetMembershipByName[snapshot.name]&selected == 0 {
			continue
		}
		declaration := snapshot.declaration
		if declaration == nil {
			declaration = snapshot.tool.Declaration()
		}
		if declaration != nil {
			decls = append(decls, declaration)
		}
	}
	sortDeclarationsByName(decls)
	return decls
}

// FilteredGeminiTools returns tools in Gemini format for the specified tool sets.
func (r *Registry) FilteredGeminiTools(sets ...ToolSet) []*genai.Tool {
	return []*genai.Tool{
		{
			FunctionDeclarations: r.FilteredDeclarations(sets...),
		},
	}
}

// DefaultRegistry creates a registry with all default tools.
func DefaultRegistry(workDir string) *Registry {
	r := NewRegistry()

	// Register all tools
	r.MustRegister(NewReadTool(workDir))
	r.MustRegister(NewWriteTool(workDir))
	r.MustRegister(NewEditTool(workDir))
	r.MustRegister(NewBashTool(workDir))
	r.MustRegister(NewGlobTool(workDir))
	r.MustRegister(NewGrepTool(workDir))
	r.MustRegister(NewTodoTool())
	r.MustRegister(NewListDirTool(workDir))
	r.MustRegister(NewDiffTool(workDir))
	r.MustRegister(NewTreeTool(workDir))
	r.MustRegister(NewEnvTool())
	r.MustRegister(NewAskUserTool())
	r.MustRegister(NewTaskOutputTool())
	r.MustRegister(NewTaskStopTool())
	r.MustRegister(NewWebFetchTool())
	r.MustRegister(NewWebSearchTool())
	r.MustRegister(NewTaskTool())
	r.MustRegister(NewKillShellTool())
	r.MustRegister(NewMemoryTool())
	r.MustRegister(NewEnterPlanModeTool())
	r.MustRegister(NewUpdatePlanProgressTool())
	r.MustRegister(NewGetPlanStatusTool())
	r.MustRegister(NewExitPlanModeTool())
	r.MustRegister(NewUndoPlanTool())
	r.MustRegister(NewRedoPlanTool())
	r.MustRegister(NewBatchTool(workDir))
	r.MustRegister(NewRefactorTool())
	r.MustRegister(NewToolsListTool(r))
	r.MustRegister(NewSkillTool(workDir))
	r.MustRegister(NewRequestToolTool())
	r.MustRegister(NewAskAgentTool())

	// File operation tools
	r.MustRegister(NewCopyTool(workDir))
	r.MustRegister(NewMoveTool(workDir))
	r.MustRegister(NewDeleteTool(workDir))
	r.MustRegister(NewMkdirTool(workDir))

	// Git tools
	r.MustRegister(NewGitLogTool(workDir))
	r.MustRegister(NewGitBlameTool(workDir))
	r.MustRegister(NewGitDiffTool(workDir))
	r.MustRegister(NewGitStatusTool(workDir))
	r.MustRegister(NewGitAddTool(workDir))
	r.MustRegister(NewGitCommitTool(workDir))
	r.MustRegister(NewGitBranchTool(workDir))
	r.MustRegister(NewGitPRTool(workDir))

	// Test runner
	r.MustRegister(NewRunTestsTool(workDir))

	// Semantic discovery tools (gopls-backed)
	r.MustRegister(NewGoToDefinitionTool(workDir))
	r.MustRegister(NewFindReferencesTool(workDir))
	r.MustRegister(NewGoSearchTool(workDir))

	// Review changes tool
	r.MustRegister(NewReviewChangesTool(workDir))

	// SSH tool
	r.MustRegister(NewSSHTool())

	// Coordination tool
	r.MustRegister(NewCoordinateTool())

	// Shared memory tool (Phase 2)
	r.MustRegister(NewSharedMemoryTool())

	// Agent Scratchpad tool (Phase 7)
	r.MustRegister(NewUpdateScratchpadTool(nil))

	// v0.78.27 — these five tools were in DefaultLazyRegistry +
	// declarations + ToolSet listings but missing from this eager
	// registry. Production uses DefaultRegistry (see app/builder.go),
	// so calling any of them returned "tool not found" mid-task even
	// though the LLM had been shown their schemas. Caught by
	// TestEagerVsLazyRegistryAlignment in registry_drift_test.go.
	//
	// The nil deps mirror the lazy-registry shape — they get wired
	// later in app/builder.go via registry.Get(name).
	r.MustRegister(NewVerifyCodeTool(workDir))
	r.MustRegister(NewCheckImpactTool(workDir))
	r.MustRegister(NewMemorizeTool(nil))
	r.MustRegister(NewPinContextTool(nil))
	r.MustRegister(NewHistorySearchTool(nil))
	// The builder either attaches a successfully probed session kernel or
	// unregisters this optional capability before schemas reach the model.
	r.MustRegister(NewReplExecTool(nil))
	r.MustRegister(NewHarnessTool(nil))

	// MCP admin tool: callbacks wired later by builder.go after MCP init.
	r.MustRegister(NewMCPAdminTool())
	r.MustRegister(NewLoopControlTool())

	// Dynamic declaration types carry an explicit marker. All remaining
	// built-ins have immutable names/descriptions/parameter schemas and can be
	// retained by reference; request filters clone only the envelope slices.
	r.freezeDefaultDeclarations()

	return r
}

// BareRegistry constructs the physically minimal automation registry. Keep
// this separate from DefaultRegistry instead of filtering after construction:
// several default tools discover skills or initialize optional subsystems in
// their constructors, which violates --bare's no-auto-discovery contract even
// when their declarations are later hidden from the model.
func BareRegistry(workDir string) *Registry {
	r := NewRegistry()
	r.MustRegister(NewReadTool(workDir))
	r.MustRegister(NewEditTool(workDir))
	r.MustRegister(NewBashTool(workDir))
	return r
}

// ========== LazyRegistry - Lazy-Loading Tool Registry ==========

// ToolLister interface for listing tools without full registry access.
// Used by ToolsListTool to avoid cyclic dependency.
type ToolLister interface {
	Names() []string
	Declarations() []*genai.FunctionDeclaration
}

// LazyRegistry manages tools with lazy loading.
// Tools are only instantiated when first accessed.
type LazyRegistry struct {
	entries                   map[string]*ToolEntry
	declarations              map[string]*genai.FunctionDeclaration
	declarationProviders      map[string]func() *genai.FunctionDeclaration
	discoverySnapshotCache    []lazyRegistrySnapshot
	discoveryRevision         uint64
	discoverySnapshotRevision uint64
	mu                        sync.RWMutex
}

type lazyRegistrySnapshot struct {
	name        string
	entry       *ToolEntry
	declaration *genai.FunctionDeclaration
	provider    func() *genai.FunctionDeclaration
}

// NewLazyRegistry creates a new lazy registry.
func NewLazyRegistry() *LazyRegistry {
	return &LazyRegistry{
		entries:              make(map[string]*ToolEntry),
		declarations:         make(map[string]*genai.FunctionDeclaration),
		declarationProviders: make(map[string]func() *genai.FunctionDeclaration),
	}
}

// RegisterFactory registers a tool factory with its declaration.
// The tool will not be instantiated until Get() is called.
func (r *LazyRegistry) RegisterFactory(name string, factory ToolFactory, decl *genai.FunctionDeclaration) {
	r.registerFactory(name, factory, decl, nil)
}

// registerFactoryWithDeclarationProvider registers a lazy tool whose
// declaration can change while the process is running. The provider is called
// by Declarations without instantiating the tool entry.
func (r *LazyRegistry) registerFactoryWithDeclarationProvider(
	name string,
	factory ToolFactory,
	decl *genai.FunctionDeclaration,
	provider func() *genai.FunctionDeclaration,
) {
	r.registerFactory(name, factory, decl, provider)
}

func (r *LazyRegistry) registerFactory(
	name string,
	factory ToolFactory,
	decl *genai.FunctionDeclaration,
	provider func() *genai.FunctionDeclaration,
) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.entries[name] = NewToolEntry(factory)
	delete(r.declarations, name)
	delete(r.declarationProviders, name)
	if decl != nil {
		r.declarations[name] = decl
	}
	if provider != nil {
		r.declarationProviders[name] = provider
	}
	r.discoveryRevision++
}

// Get retrieves a tool by name, instantiating it if necessary.
func (r *LazyRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	entry, ok := r.entries[name]
	r.mu.RUnlock()

	if !ok {
		return nil, false
	}

	return entry.Get(), true
}

// Configure adds a configuration function for a tool.
// The config will be applied when the tool is instantiated.
func (r *LazyRegistry) Configure(name string, cfg func(Tool)) {
	r.mu.RLock()
	entry, ok := r.entries[name]
	r.mu.RUnlock()

	if ok {
		entry.Configure(cfg)
	}
}

// ConfigureTyped adds a typed configuration function for a specific tool type.
func ConfigureTyped[T Tool](r *LazyRegistry, name string, cfg func(T)) {
	r.Configure(name, func(t Tool) {
		if typed, ok := t.(T); ok {
			cfg(typed)
		}
	})
}

// Declarations returns all tool declarations without instantiating tools.
// Dynamic providers run outside the registry lock because refreshing a
// declaration may read the filesystem or acquire tool-local locks.
func (r *LazyRegistry) Declarations() []*genai.FunctionDeclaration {
	snapshots := r.cachedDiscoverySnapshots()
	decls := make([]*genai.FunctionDeclaration, 0, len(snapshots))
	for _, snapshot := range snapshots {
		decl := snapshot.declaration
		if snapshot.provider != nil {
			if current := snapshot.provider(); current != nil {
				decl = current
			}
		}
		if decl != nil {
			decls = append(decls, decl)
		}
	}
	sortDeclarationsByName(decls)
	return decls
}

// Names returns the names of all registered tools.
func (r *LazyRegistry) Names() []string {
	snapshots := r.cachedDiscoverySnapshots()
	names := make([]string, len(snapshots))
	for index, snapshot := range snapshots {
		names[index] = snapshot.name
	}
	return names
}

// List returns all tools, instantiating them if necessary.
func (r *LazyRegistry) List() []Tool {
	snapshots := r.cachedDiscoverySnapshots()
	tools := make([]Tool, len(snapshots))
	for i, snapshot := range snapshots {
		tools[i] = snapshot.entry.Get()
	}
	sortToolsByName(tools)
	return tools
}

// cachedDiscoverySnapshots returns an immutable, name-ordered view of the
// lazy registry. Registration replaces the cache instead of mutating it, so
// factories and dynamic declaration providers can safely run after the lock
// is released. Providers themselves are deliberately not memoized.
func (r *LazyRegistry) cachedDiscoverySnapshots() []lazyRegistrySnapshot {
	r.mu.RLock()
	if r.discoverySnapshotRevision == r.discoveryRevision {
		cached := r.discoverySnapshotCache
		r.mu.RUnlock()
		return cached
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.discoverySnapshotRevision != r.discoveryRevision {
		snapshots := make([]lazyRegistrySnapshot, 0, len(r.entries))
		for name, entry := range r.entries {
			snapshots = append(snapshots, lazyRegistrySnapshot{
				name:        name,
				entry:       entry,
				declaration: r.declarations[name],
				provider:    r.declarationProviders[name],
			})
		}
		sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].name < snapshots[j].name })
		r.discoverySnapshotCache = snapshots
		r.discoverySnapshotRevision = r.discoveryRevision
	}
	return r.discoverySnapshotCache
}

// GeminiTools returns tool declarations in Gemini format without instantiation.
func (r *LazyRegistry) GeminiTools() []*genai.Tool {
	return []*genai.Tool{
		{
			FunctionDeclarations: r.Declarations(),
		},
	}
}

// Register adds an already-instantiated tool to the registry.
// This is for backward compatibility and dynamic tools.
func (r *LazyRegistry) Register(tool Tool) error {
	name := tool.Name()
	r.mu.RLock()
	_, exists := r.entries[name]
	r.mu.RUnlock()
	if exists {
		return fmt.Errorf("tool already registered: %s", name)
	}

	// Third-party declarations are arbitrary code. Resolve them without the
	// registry lock so a declaration can safely register another tool.
	declaration := tool.Declaration()

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[name]; exists {
		return fmt.Errorf("tool already registered: %s", name)
	}

	// Create a factory that returns the existing instance
	r.entries[name] = &ToolEntry{
		factory:  func() Tool { return tool },
		instance: tool,
	}
	delete(r.declarations, name)
	delete(r.declarationProviders, name)
	if declaration != nil {
		r.declarations[name] = declaration
	}
	r.discoveryRevision++
	return nil
}

// MustRegister adds a tool and logs a warning on error.
func (r *LazyRegistry) MustRegister(tool Tool) {
	if err := r.Register(tool); err != nil {
		logging.Warn("failed to register tool", "tool", tool.Name(), "error", err)
	}
}

// IsInstantiated returns true if a tool has been instantiated.
func (r *LazyRegistry) IsInstantiated(name string) bool {
	r.mu.RLock()
	entry, ok := r.entries[name]
	r.mu.RUnlock()

	if !ok {
		return false
	}
	return entry.IsInstantiated()
}

// InstantiatedCount returns the number of instantiated tools.
func (r *LazyRegistry) InstantiatedCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, entry := range r.entries {
		if entry.IsInstantiated() {
			count++
		}
	}
	return count
}

// TotalCount returns the total number of registered tools.
func (r *LazyRegistry) TotalCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}

// DefaultLazyRegistry creates a lazy registry with all default tools.
// Tools are registered with factories but not instantiated.
func DefaultLazyRegistry(workDir string) *LazyRegistry {
	r := NewLazyRegistry()

	// Build model-facing declarations from the same live contracts as the
	// factories below, without retaining the temporary tool instances.
	declarations := getAllDeclarationsForWorkDir(workDir)

	// Core file tools
	r.RegisterFactory("read", func() Tool { return NewReadTool(workDir) }, declarations["read"])
	r.RegisterFactory("write", func() Tool { return NewWriteTool(workDir) }, declarations["write"])
	r.RegisterFactory("edit", func() Tool { return NewEditTool(workDir) }, declarations["edit"])

	// Search tools
	r.RegisterFactory("glob", func() Tool { return NewGlobTool(workDir) }, declarations["glob"])
	r.RegisterFactory("grep", func() Tool { return NewGrepTool(workDir) }, declarations["grep"])

	// Shell and execution
	r.RegisterFactory("bash", func() Tool { return NewBashTool(workDir) }, declarations["bash"])
	taskTool := NewTaskTool()
	r.registerFactoryWithDeclarationProvider(
		"task",
		func() Tool { return taskTool },
		declarations["task"],
		taskTool.Declaration,
	)
	r.RegisterFactory("task_output", func() Tool { return NewTaskOutputTool() }, declarations["task_output"])
	r.RegisterFactory("task_stop", func() Tool { return NewTaskStopTool() }, declarations["task_stop"])
	r.RegisterFactory("kill_shell", func() Tool { return NewKillShellTool() }, declarations["kill_shell"])

	// Directory tools
	r.RegisterFactory("list_dir", func() Tool { return NewListDirTool(workDir) }, declarations["list_dir"])
	r.RegisterFactory("tree", func() Tool { return NewTreeTool(workDir) }, declarations["tree"])

	// File operations
	r.RegisterFactory("copy", func() Tool { return NewCopyTool(workDir) }, declarations["copy"])
	r.RegisterFactory("move", func() Tool { return NewMoveTool(workDir) }, declarations["move"])
	r.RegisterFactory("delete", func() Tool { return NewDeleteTool(workDir) }, declarations["delete"])
	r.RegisterFactory("mkdir", func() Tool { return NewMkdirTool(workDir) }, declarations["mkdir"])

	// Utility tools
	r.RegisterFactory("diff", func() Tool { return NewDiffTool(workDir) }, declarations["diff"])
	r.RegisterFactory("env", func() Tool { return NewEnvTool() }, declarations["env"])
	r.RegisterFactory("todo", func() Tool { return NewTodoTool() }, declarations["todo"])
	// Skill discovery is lightweight and its declaration includes the compact
	// project catalog, so instantiate this one tool while keeping its body loads
	// on demand.
	skillTool := NewSkillTool(workDir)
	r.registerFactoryWithDeclarationProvider(
		"skill",
		func() Tool { return skillTool },
		skillTool.Declaration(),
		skillTool.Declaration,
	)
	// User interaction
	r.RegisterFactory("ask_user", func() Tool { return NewAskUserTool() }, declarations["ask_user"])
	r.RegisterFactory("ask_agent", func() Tool { return NewAskAgentTool() }, declarations["ask_agent"])

	// Web tools
	r.RegisterFactory("web_fetch", func() Tool { return NewWebFetchTool() }, declarations["web_fetch"])
	r.RegisterFactory("web_search", func() Tool { return NewWebSearchTool() }, declarations["web_search"])

	// Memory tools
	r.RegisterFactory("memory", func() Tool { return NewMemoryTool() }, declarations["memory"])
	r.RegisterFactory("shared_memory", func() Tool { return NewSharedMemoryTool() }, declarations["shared_memory"])

	// Plan mode tools
	r.RegisterFactory("enter_plan_mode", func() Tool { return NewEnterPlanModeTool() }, declarations["enter_plan_mode"])
	r.RegisterFactory("update_plan_progress", func() Tool { return NewUpdatePlanProgressTool() }, declarations["update_plan_progress"])
	r.RegisterFactory("get_plan_status", func() Tool { return NewGetPlanStatusTool() }, declarations["get_plan_status"])
	r.RegisterFactory("exit_plan_mode", func() Tool { return NewExitPlanModeTool() }, declarations["exit_plan_mode"])
	r.RegisterFactory("undo_plan", func() Tool { return NewUndoPlanTool() }, declarations["undo_plan"])
	r.RegisterFactory("redo_plan", func() Tool { return NewRedoPlanTool() }, declarations["redo_plan"])

	// Code analysis tools
	r.RegisterFactory("batch", func() Tool { return NewBatchTool(workDir) }, declarations["batch"])
	r.RegisterFactory("refactor", func() Tool { return NewRefactorTool() }, declarations["refactor"])
	r.RegisterFactory("repl_exec", func() Tool { return NewReplExecTool(nil) }, declarations["repl_exec"])
	r.RegisterFactory("harness", func() Tool { return NewHarnessTool(nil) }, declarations["harness"])

	// Git tools
	r.RegisterFactory("git_log", func() Tool { return NewGitLogTool(workDir) }, declarations["git_log"])
	r.RegisterFactory("git_blame", func() Tool { return NewGitBlameTool(workDir) }, declarations["git_blame"])
	r.RegisterFactory("git_diff", func() Tool { return NewGitDiffTool(workDir) }, declarations["git_diff"])
	r.RegisterFactory("git_status", func() Tool { return NewGitStatusTool(workDir) }, declarations["git_status"])
	r.RegisterFactory("git_add", func() Tool { return NewGitAddTool(workDir) }, declarations["git_add"])
	r.RegisterFactory("git_commit", func() Tool { return NewGitCommitTool(workDir) }, declarations["git_commit"])
	r.RegisterFactory("git_branch", func() Tool { return NewGitBranchTool(workDir) }, declarations["git_branch"])
	r.RegisterFactory("git_pr", func() Tool { return NewGitPRTool(workDir) }, declarations["git_pr"])

	// Test runner
	r.RegisterFactory("run_tests", func() Tool { return NewRunTestsTool(workDir) }, declarations["run_tests"])

	// Semantic discovery tools
	r.RegisterFactory("go_to_definition", func() Tool { return NewGoToDefinitionTool(workDir) }, declarations["go_to_definition"])
	r.RegisterFactory("find_references", func() Tool { return NewFindReferencesTool(workDir) }, declarations["find_references"])
	r.RegisterFactory("go_search", func() Tool { return NewGoSearchTool(workDir) }, declarations["go_search"])

	// Review changes tool
	r.RegisterFactory("review_changes", func() Tool { return NewReviewChangesTool(workDir) }, declarations["review_changes"])

	// Other tools
	r.RegisterFactory("ssh", func() Tool { return NewSSHTool() }, declarations["ssh"])
	r.RegisterFactory("coordinate", func() Tool { return NewCoordinateTool() }, declarations["coordinate"])
	r.RegisterFactory("request_tool", func() Tool { return NewRequestToolTool() }, declarations["request_tool"])
	r.RegisterFactory("update_scratchpad", func() Tool { return NewUpdateScratchpadTool(nil) }, declarations["update_scratchpad"])
	r.RegisterFactory("verify_code", func() Tool { return NewVerifyCodeTool(workDir) }, declarations["verify_code"])
	r.RegisterFactory("check_impact", func() Tool { return NewCheckImpactTool(workDir) }, declarations["check_impact"])
	r.RegisterFactory("memorize", func() Tool { return NewMemorizeTool(nil) }, declarations["memorize"])

	// Custom improvements
	r.RegisterFactory("pin_context", func() Tool { return NewPinContextTool(nil) }, declarations["pin_context"])
	r.RegisterFactory("history_search", func() Tool { return NewHistorySearchTool(nil) }, declarations["history_search"])

	// MCP admin — read-only inspection of the MCP control plane.
	r.RegisterFactory("mcp_admin", func() Tool { return NewMCPAdminTool() }, declarations["mcp_admin"])
	r.RegisterFactory("loop_control", func() Tool { return NewLoopControlTool() }, declarations["loop_control"])

	return r
}
