package tools

import (
	"context"
	"fmt"

	"gokin/internal/client"
	"gokin/internal/skills"
)

func SkillModelSettings(registry ToolRegistry) client.RequestSettings {
	if registry != nil {
		if registered, ok := registry.Get("skill"); ok {
			if skill, ok := registered.(*SkillTool); ok {
				skill.grantMu.RLock()
				defer skill.grantMu.RUnlock()
				return skill.activeModelSettings
			}
		}
	}
	return client.RequestSettings{}
}

func HasPendingSkillModelSettings(registry ToolRegistry) bool {
	if registry != nil {
		if registered, ok := registry.Get("skill"); ok {
			if skill, ok := registered.(*SkillTool); ok {
				skill.grantMu.RLock()
				defer skill.grantMu.RUnlock()
				return skill.pendingModelSettings != (client.RequestSettings{})
			}
		}
	}
	return false
}

func SkillScopedClient(base client.Client, registry ToolRegistry) client.Client {
	if registry == nil {
		return base
	}
	registered, ok := registry.Get("skill")
	if !ok {
		return base
	}
	if _, ok := registered.(*SkillTool); !ok {
		return base
	}
	return client.WithRequestSettings(base, func() client.RequestSettings { return SkillModelSettings(registry) })
}

// ContextWithSkillModelClient binds both the current loop selection and the
// session default. A /skill handoff belongs to its next prompt, so old active
// overrides must not influence its inherit or omitted settings.
func ContextWithSkillModelClient(ctx context.Context, registry ToolRegistry, base client.Client) context.Context {
	base = client.RequestSettingsBase(base)
	resolver := skillModelResolver{owner: registry, base: base}
	resolver.resolve = func(model, effort string) (string, string, error) {
		return client.ResolveSkillModelSettings(SkillScopedClient(base, registry), model, effort)
	}
	return context.WithValue(ctx, skillModelResolverKey{}, resolver)
}

func (t *SkillTool) resolveInlineModelSettings(ctx context.Context, skill skills.Skill, forUser bool) (client.RequestSettings, error) {
	if skill.Model == "" && skill.Effort == "" {
		return client.RequestSettings{}, nil
	}
	local, ok := ctx.Value(skillModelResolverKey{}).(skillModelResolver)
	if !ok || local.resolve == nil || local.owner == nil {
		return client.RequestSettings{}, fmt.Errorf("requires an owning model executor")
	}
	if registered, ok := local.owner.Get("skill"); !ok || registered != t {
		return client.RequestSettings{}, fmt.Errorf("model executor does not own this skill")
	}
	t.grantMu.RLock()
	effective := t.activeModelSettings
	if forUser {
		effective = t.pendingModelSettings
	}
	t.grantMu.RUnlock()
	model, effort := skill.Model, skill.Effort
	if model == "" || model == "inherit" {
		if effective.Model != "" {
			model = effective.Model
		}
	}
	if effort == "" {
		effort = effective.Effort
	}
	resolve := local.resolve
	if forUser && local.base != nil {
		resolve = func(model, effort string) (string, string, error) {
			return client.ResolveSkillModelSettings(local.base, model, effort)
		}
	}
	resolved, nativeEffort, err := resolve(model, effort)
	if err != nil {
		return client.RequestSettings{}, err
	}
	if skill.Model != "" {
		effective.Model = resolved
	}
	if skill.Effort != "" {
		effective.Effort = nativeEffort
	}
	return effective, nil
}

func (t *SkillTool) activateModelSettings(settings client.RequestSettings, forUser bool) {
	t.grantMu.Lock()
	defer t.grantMu.Unlock()
	if forUser {
		t.pendingModelSettings = settings
	} else {
		t.activeModelSettings = settings
	}
}
