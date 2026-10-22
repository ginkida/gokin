package tools

import (
	"context"
	"fmt"

	"gokin/internal/hooks"
	"gokin/internal/skills"
)

type skillHookManagerKey struct{}
type skillRenderHooksKey struct{}
type skillRenderHooks struct {
	owner ToolRegistry
	scope *hooks.Scope
}
type skillHookManager struct {
	owner   ToolRegistry
	manager *hooks.Manager
	scope   *hooks.Scope
}

func (t *SkillTool) HookScope() *hooks.Scope {
	t.hooksOnce.Do(func() { t.hookScope = &hooks.Scope{} })
	return t.hookScope
}

// ContextWithSkillHooks replaces inherited registration authority with the
// current tool registry's conversation. Global configured hooks stay shared.
func ContextWithSkillHooks(ctx context.Context, registry ToolRegistry, manager *hooks.Manager) context.Context {
	_ = manager.RefreshClaudeSettings()
	var scope *hooks.Scope
	if skill := ownedSkillTool(registry); skill != nil {
		_ = skill.synchronizeHooks(manager)
		scope = skill.HookScope()
	}
	if scope == nil {
		if local, ok := ctx.Value(skillHookManagerKey{}).(skillHookManager); ok && sameSkillRuleOwner(local.owner, registry) {
			scope = local.scope
		}
	}
	ctx = hooks.ContextWithScope(ctx, scope)
	if render, ok := ctx.Value(skillRenderHooksKey{}).(skillRenderHooks); ok && sameSkillRuleOwner(render.owner, registry) {
		ctx = hooks.ContextWithScope(ctx, render.scope)
	}
	return context.WithValue(ctx, skillHookManagerKey{}, skillHookManager{owner: registry, manager: manager, scope: scope})
}

func contextWithPreparedSkillHooks(ctx context.Context, registration *hooks.Registration) context.Context {
	if registration == nil {
		return ctx
	}
	local, _ := ctx.Value(skillHookManagerKey{}).(skillHookManager)
	render := skillRenderHooks{owner: local.owner, scope: registration.RenderScope()}
	ctx = hooks.ContextWithScope(ctx, render.scope)
	return context.WithValue(ctx, skillRenderHooksKey{}, render)
}

func contextWithForkRenderHooks(ctx context.Context, registration *hooks.Registration, parent *hooks.Scope) context.Context {
	if registration == nil {
		return ctx
	}
	local, _ := ctx.Value(skillHookManagerKey{}).(skillHookManager)
	render := skillRenderHooks{owner: local.owner, scope: registration.RenderScopeOver(parent)}
	return context.WithValue(hooks.ContextWithScope(ctx, render.scope), skillRenderHooksKey{}, render)
}

func (t *SkillTool) validateHooks(ctx context.Context, skill skills.Skill) error {
	if len(skill.Hooks) == 0 {
		return nil
	}
	local, ok := ctx.Value(skillHookManagerKey{}).(skillHookManager)
	if !ok || local.owner == nil {
		return fmt.Errorf("skill hooks require an owning executor")
	}
	if ownedSkillTool(local.owner) != t {
		return fmt.Errorf("hook executor does not own this skill")
	}
	if err := local.manager.CommandsPolicyError(skill.Hooks); err != nil {
		return err
	}
	if !skill.PersonalSource() && !t.WorkspaceTrusted() {
		return fmt.Errorf("skill hooks require a trusted workspace (hooks.trusted_workspaces)")
	}
	return nil
}

func SkillHooks(registry ToolRegistry) []*hooks.Hook {
	if skill := ownedSkillTool(registry); skill != nil {
		return skill.HookScope().Hooks()
	}
	return nil
}

// ContextWithDelegatedSkillHooks creates a child's registration owner while
// keeping the spawning conversation's tool checks. A restricted child may
// lack Skill entirely; that must not let its remaining tools bypass checks.
func ContextWithDelegatedSkillHooks(ctx context.Context, registry, parent ToolRegistry, manager *hooks.Manager) context.Context {
	inherited := hooks.ScopeFromContext(ctx)
	if inherited == nil && parent != nil {
		if skill := ownedSkillTool(parent); skill != nil {
			inherited = skill.HookScope()
		}
	}
	ctx = ContextWithSkillHooks(ctx, registry, manager)
	scope := hooks.ScopeFromContext(ctx)
	if scope == nil {
		scope = &hooks.Scope{}
	}
	scope.Inherit(inherited)
	ctx = hooks.ContextWithScope(ctx, scope)
	return context.WithValue(ctx, skillHookManagerKey{}, skillHookManager{owner: registry, manager: manager, scope: scope})
}

func ClearSkillHooks(registry ToolRegistry) {
	if skill := ownedSkillTool(registry); skill != nil {
		skill.clearHooks()
	}
}

// ClaudeHookToolName derives MCP identity from the registered tool, rather
// than guessing which native names happen to contain an underscore.
func ClaudeHookToolName(tool Tool) string {
	if named, ok := tool.(interface{ ClaudeToolName() string }); ok && named.ClaudeToolName() != "" {
		return named.ClaudeToolName()
	}
	if remote, ok := tool.(interface {
		GetServerName() string
		GetOriginalToolName() string
	}); ok && remote.GetServerName() != "" && remote.GetOriginalToolName() != "" {
		return "mcp__" + remote.GetServerName() + "__" + remote.GetOriginalToolName()
	}
	return hooks.ClaudeToolName(tool.Name())
}

// PermissionToolIdentity supplies full MCP source names even when a provider
// needs a shortened function alias. Native tool policy names stay unchanged.
func PermissionToolIdentity(tool Tool) string {
	if remote, ok := tool.(interface {
		GetServerName() string
		GetOriginalToolName() string
	}); ok && remote.GetServerName() != "" && remote.GetOriginalToolName() != "" {
		return ClaudeHookToolName(tool)
	}
	return tool.Name()
}
