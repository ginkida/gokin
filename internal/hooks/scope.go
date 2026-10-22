package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Scope owns registrations for one conversation. It is never shared by a
// cloned tool registry or added to the manager's global configuration.
type Scope struct {
	mu               sync.Mutex
	groups           map[string][]*scopedHook
	order            []string
	preparing        map[string]int
	count, reserved  int
	epoch            uint64
	inherited        *Scope
	restoreError     error
	validationParent *Scope
}

type scopedHook struct {
	hook              *Hook
	owner             *Scope
	claimed, consumed bool
	index             int
	onceObserver      func(int)
}

type scopeKey struct{}

func ContextWithScope(ctx context.Context, scope *Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope)
}

func ScopeFromContext(ctx context.Context) *Scope {
	scope, _ := ctx.Value(scopeKey{}).(*Scope)
	return scope
}

// Prepare reserves capacity without activating anything. Rendering and ledger
// retention must succeed before Commit; Cancel releases failed preparations.
func (s *Scope) Prepare(source string, definitions []ClaudeCommand) (*Registration, error) {
	return s.prepare(source, "skill:"+source, false, definitions)
}

func (s *Scope) prepare(source, provenance string, settings bool, definitions []ClaudeCommand) (*Registration, error) {
	entries := make([]*scopedHook, 0, len(definitions))
	for i, definition := range CloneClaudeCommands(definitions) {
		kind := claudeEventTypes[definition.Event]
		if kind == "" {
			return nil, fmt.Errorf("unsupported hook event %q", definition.Event)
		}
		hook := &Hook{Name: fmt.Sprintf("%s:%d", source, i+1), Source: provenance,
			Type: kind, Enabled: true, Command: definition.Command, Claude: &definition, claudeSettings: settings}
		hook.claudePlugin = definition.plugin
		hook.claudeManaged = settings && strings.HasPrefix(provenance, "claude:managed:")
		if definition.Matcher != "" && definition.Matcher != "*" {
			matcher, err := compileClaudeMatcher(definition.Matcher)
			if err != nil {
				return nil, err
			}
			hook.claudeMatcher = matcher
		}
		entries = append(entries, &scopedHook{hook: hook, owner: s, index: i})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.groups == nil {
		s.groups = make(map[string][]*scopedHook)
	}
	if s.preparing == nil {
		s.preparing = make(map[string]int)
	}
	if _, busy := s.preparing[source]; busy {
		return nil, fmt.Errorf("hooks for this skill are already being prepared")
	}
	// An identical re-invocation must not rearm a successfully consumed once
	// handler or steal an in-flight claim from another tool call.
	if existing := s.groups[source]; len(existing) == len(definitions) && len(existing) > 0 {
		previous := make([]ClaudeCommand, len(existing))
		for i, entry := range existing {
			previous[i] = *entry.hook.Claude
		}
		if CommandFingerprint(previous) == CommandFingerprint(definitions) {
			entries = append([]*scopedHook(nil), existing...)
		}
	}
	delta := max(0, len(entries)-len(s.groups[source]))
	if s.count+s.reserved+delta > 256 {
		if settings {
			return nil, fmt.Errorf("Claude settings hooks exceed 256 handlers")
		}
		return nil, fmt.Errorf("session skill hooks exceed 256 handlers")
	}
	s.reserved += delta
	s.preparing[source] = delta
	return &Registration{scope: s, source: source, entries: entries, epoch: s.epoch}, nil
}

type Registration struct {
	scope    *Scope
	source   string
	entries  []*scopedHook
	finished bool // protected by scope.mu
	epoch    uint64
}

func (r *Registration) Consumed() []bool {
	if r == nil {
		return nil
	}
	r.scope.mu.Lock()
	defer r.scope.mu.Unlock()
	consumed := make([]bool, len(r.entries))
	for i, entry := range r.entries {
		consumed[i] = entry.consumed
	}
	return consumed
}

func (r *Registration) RestoreConsumed(consumed []bool) error {
	r.scope.mu.Lock()
	defer r.scope.mu.Unlock()
	if len(consumed) != len(r.entries) {
		return fmt.Errorf("persisted skill hook handler count changed")
	}
	for i, used := range consumed {
		if used && !r.entries[i].hook.Claude.Once {
			return fmt.Errorf("persisted state cannot consume a non-once hook")
		}
	}
	for i, used := range consumed {
		r.entries[i].consumed = used
	}
	return nil
}

func (r *Registration) ObserveOnce(observer func(int)) {
	r.scope.mu.Lock()
	defer r.scope.mu.Unlock()
	for _, entry := range r.entries {
		entry.onceObserver = observer
	}
}

// RenderScope overlays the pending definition for this invocation's shell
// injection without publishing it to concurrent calls in the conversation.
// Existing registrations keep their original once claims.
func (r *Registration) RenderScope() *Scope {
	return r.RenderScopeOver(r.scope)
}

func (r *Registration) RenderScopeOver(s *Scope) *Scope {
	s.mu.Lock()
	defer s.mu.Unlock()
	render := &Scope{groups: make(map[string][]*scopedHook), inherited: s.inherited, validationParent: s}
	for _, group := range s.order {
		if group != r.source {
			render.order = append(render.order, group)
			render.groups[group] = append([]*scopedHook(nil), s.groups[group]...)
			render.count += len(render.groups[group])
		}
	}
	render.order = append(render.order, r.source)
	render.groups[r.source] = r.entries
	render.count += len(r.entries)
	return render
}

func (r *Registration) RemainingDefinitions() []ClaudeCommand {
	r.scope.mu.Lock()
	defer r.scope.mu.Unlock()
	var definitions []ClaudeCommand
	for _, entry := range r.entries {
		if !entry.consumed {
			definitions = append(definitions, *entry.hook.Claude)
		}
	}
	return CloneClaudeCommands(definitions)
}

func (r *Registration) Commit() {
	if r == nil {
		return
	}
	s := r.scope
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.finished {
		return
	}
	if r.epoch != s.epoch {
		r.finishLocked()
		return
	}
	if _, existing := s.groups[r.source]; !existing {
		s.order = append(s.order, r.source)
	}
	s.count += len(r.entries) - len(s.groups[r.source])
	s.groups[r.source] = r.entries
	r.finishLocked()
}

func (r *Registration) Cancel() {
	if r == nil {
		return
	}
	r.scope.mu.Lock()
	defer r.scope.mu.Unlock()
	if !r.finished {
		r.finishLocked()
	}
}

func (r *Registration) finishLocked() {
	r.scope.reserved -= r.scope.preparing[r.source]
	delete(r.scope.preparing, r.source)
	r.finished = true
}

// Run executes independent Claude handlers in parallel. Once handlers have a
// single claim across concurrent tool calls, and only a successful invocation
// consumes that claim permanently.
func (s *Scope) run(ctx context.Context, manager *Manager, kind Type, hctx *Context, handler Handler) []Result {
	if s == nil || ctx.Err() != nil {
		return nil
	}
	if err := s.RestoreError(); err != nil {
		return []Result{{Hook: &Hook{Name: "required skill hooks", Source: "session", Type: kind}, Error: err, StopReason: "Cannot restore required skill hooks: " + err.Error()}}
	}
	toolName := hctx.ToolName
	if override := RuntimeInfoFromContext(ctx).ToolNameOverride; override != "" {
		toolName = override
	}
	var entries []*scopedHook
	seenSettings := make(map[string]bool)
	info := RuntimeInfoFromContext(ctx)
	for _, entry := range s.snapshotEntries() {
		if AgentHookActive(ctx) && entry.hook.Claude.Handler == "agent" {
			continue
		}
		if !manager.HookEnabled(entry.hook) {
			continue
		}
		entry.owner.mu.Lock()
		matchName := toolName
		if kind == SubagentStart {
			matchName = ClaudeAgentType(info.AgentType)
		}
		if entry.hook.claudeSettings && kind == Stop {
			matchName = ClaudeAgentType(info.AgentType)
		}
		if entry.consumed || entry.claimed || !entry.hook.Matches(kind, matchName) ||
			entry.hook.claudeSettings && kind == Stop && ((entry.hook.Claude.Event == "SubagentStop") != (info.AgentID != "")) {
			entry.owner.mu.Unlock()
			continue
		}
		if entry.hook.claudeSettings {
			definition := *entry.hook.Claude
			definition.Matcher = ""
			fingerprint := CommandFingerprint([]ClaudeCommand{definition})
			if entry.hook.claudePlugin != nil {
				data, _ := json.Marshal(entry.hook.claudePlugin)
				fingerprint += string(data)
			}
			if seenSettings[fingerprint] {
				entry.owner.mu.Unlock()
				continue
			}
			seenSettings[fingerprint] = true
		}
		if entry.hook.Claude.Once {
			entry.claimed = true
		}
		entries = append(entries, entry)
		entry.owner.mu.Unlock()
	}
	if kind != Stop {
		s.mu.Lock()
		parent := s.inherited
		s.mu.Unlock()
		for _, entry := range inheritedEntries(parent) {
			if AgentHookActive(ctx) && entry.hook.Claude.Handler == "agent" {
				continue
			}
			if !manager.HookEnabled(entry.hook) {
				continue
			}
			entry.owner.mu.Lock()
			matchName := toolName
			if kind == SubagentStart {
				matchName = ClaudeAgentType(info.AgentType)
			}
			if !entry.consumed && !entry.claimed && entry.hook.Matches(kind, matchName) {
				if entry.hook.Claude.Once {
					entry.claimed = true
				}
				entries = append(entries, entry)
			}
			entry.owner.mu.Unlock()
		}
	}
	results := make([]Result, len(entries))
	var work sync.WaitGroup
	for i, entry := range entries {
		work.Add(1)
		go func() {
			defer work.Done()
			result := executeScopedHook(ctx, manager, entry.hook, hctx)
			if entry.hook.Claude.Once && result.asyncJob != nil {
				result.asyncJob.observe(func(completed Result) {
					entry.owner.mu.Lock()
					entry.claimed = false
					entry.consumed = completed.Error == nil
					observer, consumed, index := entry.onceObserver, entry.consumed, entry.index
					entry.owner.mu.Unlock()
					if consumed && observer != nil {
						observer(index)
					}
				})
				results[i] = result
				return
			}
			entry.owner.mu.Lock()
			entry.claimed = false
			entry.consumed = entry.hook.Claude.Once && result.Error == nil && !result.skipped
			observer, consumed, index := entry.onceObserver, entry.consumed, entry.index
			entry.owner.mu.Unlock()
			if consumed && observer != nil {
				observer(index)
			}
			results[i] = result
		}()
	}
	work.Wait()
	if handler != nil {
		for _, result := range results {
			handler(result.Hook, result.Output, result.Error)
		}
	}
	return results
}

func executeScopedHook(ctx context.Context, manager *Manager, hook *Hook, hctx *Context) (result Result) {
	defer func() {
		if value := recover(); value != nil {
			result = Result{Hook: hook, Error: fmt.Errorf("hook panicked: %v", value)}
		}
	}()
	return manager.executeHook(ctx, hook, hctx, effectiveClaudeTimeout(ctx, hook))
}

func (s *Scope) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.groups = make(map[string][]*scopedHook)
	s.order = nil
	s.count = 0
	s.inherited = nil
	s.restoreError = nil
	s.validationParent = nil
	s.epoch++
	s.mu.Unlock()
}

// Adopt atomically replaces local registrations with a prepared, unused scope.
// Existing child scopes keep the parent's stable pointer. Old preparations are
// invalidated, and their eventual cancellation still releases reservations.
func (s *Scope) Adopt(prepared *Scope) {
	if s == prepared {
		return
	}
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groups, s.order, s.count = prepared.groups, prepared.order, prepared.count
	for _, group := range s.groups {
		for _, entry := range group {
			entry.owner = s
		}
	}
	s.restoreError = nil
	s.epoch++
}

func (s *Scope) SetRestoreError(err error) {
	s.mu.Lock()
	s.restoreError = err
	s.mu.Unlock()
}

func (s *Scope) HasSource(source string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.groups[source]) > 0
}

func (s *Scope) RestoreError() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	err, parent, validation := s.restoreError, s.inherited, s.validationParent
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := validation.RestoreError(); err != nil {
		return err
	}
	return parent.RestoreError()
}

func (s *Scope) HasRegistrations() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	count, parent := s.count, s.inherited
	s.mu.Unlock()
	return count > 0 || parent.HasRegistrations()
}

// Inherit keeps the spawning conversation's tool checks authoritative without
// granting registration authority or applying its Stop hooks to the child.
func (s *Scope) Inherit(parent *Scope) {
	if parent == s {
		return
	}
	s.mu.Lock()
	s.inherited = parent
	s.mu.Unlock()
}

func inheritedEntries(scope *Scope) []*scopedHook {
	if scope == nil {
		return nil
	}
	entries := scope.snapshotEntries()
	scope.mu.Lock()
	parent := scope.inherited
	scope.mu.Unlock()
	return append(entries, inheritedEntries(parent)...)
}

func (s *Scope) Hooks() []*Hook {
	if s == nil {
		return nil
	}
	var result []*Hook
	for _, entry := range s.snapshotEntries() {
		entry.owner.mu.Lock()
		if !entry.consumed {
			copy := *entry.hook
			definition := CloneClaudeCommands([]ClaudeCommand{*copy.Claude})[0]
			copy.Claude = &definition
			result = append(result, &copy)
		}
		entry.owner.mu.Unlock()
	}
	return result
}

// ConfiguredHooks retains consumed once declarations for passive discovery:
// a filename remains a watch seed even after its own once handler completed.
func (s *Scope) ConfiguredHooks(kind Type) []*Hook {
	if s == nil {
		return nil
	}
	var result []*Hook
	for _, entry := range s.snapshotEntries() {
		entry.owner.mu.Lock()
		if entry.hook.Type == kind {
			copy := *entry.hook
			definition := CloneClaudeCommands([]ClaudeCommand{*copy.Claude})[0]
			copy.Claude = &definition
			result = append(result, &copy)
		}
		entry.owner.mu.Unlock()
	}
	return result
}

func (s *Scope) snapshotEntries() []*scopedHook {
	s.mu.Lock()
	defer s.mu.Unlock()
	var entries []*scopedHook
	for _, group := range s.order {
		entries = append(entries, s.groups[group]...)
	}
	return entries
}

func AdditionalContext(results []Result) string {
	var contexts []string
	for _, result := range results {
		if result.AdditionalContext != "" {
			contexts = append(contexts, result.AdditionalContext)
		}
	}
	return strings.Join(contexts, "\n")
}

// PolicyError includes consumed handlers because registrations remain required
// until their conversation owner explicitly clears them.
func (s *Scope) PolicyError(manager *Manager) error {
	if s == nil {
		return nil
	}
	var definitions []ClaudeCommand
	for _, entry := range inheritedEntries(s) {
		if entry.hook.Claude != nil {
			definitions = append(definitions, *entry.hook.Claude)
		}
	}
	return manager.CommandsPolicyError(definitions)
}
