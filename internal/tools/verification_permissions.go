package tools

import (
	"context"
	"strings"

	"gokin/internal/hooks"
	"gokin/internal/permission"

	"google.golang.org/genai"
)

type headlessToolPolicyKey struct{}
type headlessToolPolicy struct {
	denied                  []string
	verificationUnavailable bool
}

func isHeadless(ctx context.Context) bool {
	state, _ := ctx.Value(headlessPermissionsKey{}).(*HeadlessPermissions)
	return state != nil
}

// Permission preflight must leave hooks to the dispatcher: they can rewrite
// inputs, supply approval, or stop the run. Discovery never executes a hook.
func hasPermissionPreHooks(ctx context.Context, registry ToolRegistry, manager *hooks.Manager, name string) bool {
	if manager != nil && (manager.HasHooksFor(hooks.PreTool, name) || len(manager.CachedClaudeHooks(hooks.PreTool)) > 0) {
		return true
	}
	if len(hooks.ScopeFromContext(ctx).ConfiguredHooks(hooks.PreTool)) > 0 {
		return true
	}
	if skill := ownedSkillTool(registry); skill != nil {
		return len(skill.HookScope().ConfiguredHooks(hooks.PreTool)) > 0
	}
	return false
}

// PreflightToolPermission shares authorization inputs and policy with both
// dispatchers. It skips only known refusals; execution still authorizes again
// after hooks, and no refusal is added to the actual permission-denial ledger.
func PreflightToolPermission(ctx context.Context, manager *permission.Manager, registry ToolRegistry, hookManager *hooks.Manager, tool Tool, args map[string]any, workDir string) bool {
	// Use the dispatcher's live refresh path, including required-hook errors.
	// Any hook handling or failure remains owned by normal dispatch.
	ctx = ContextWithSkillHooks(ctx, registry, hookManager)
	if SkillHooksError(ctx) != nil {
		return true
	}
	ctx = permission.ContextWithWorkDir(ctx, workDir)
	ctx = permission.ContextWithToolIdentity(ctx, tool.Name(), PermissionToolIdentity(tool))
	// Hooks run before authorization, including terminal hooks on hard denies.
	if hasPermissionPreHooks(ctx, registry, hookManager, tool.Name()) {
		return true
	}
	args = PermissionArgsForTool(tool, args)
	grants, denies := SkillPermissionRules(ctx, registry)
	if manager == nil {
		return !permission.TemporaryToolRulesDenyWithIdentity(ctx, denies, tool.Name(), args, workDir)
	}
	response, err := manager.Preflight(ctx, tool.Name(), ClonePermissionArgs(args), grants, denies, !isHeadless(ctx))
	return err == nil && response.Allowed
}

func (e *Executor) PreflightToolPermission(ctx context.Context, name string, args map[string]any) bool {
	tool, ok := e.registry.Get(name)
	return ok && PreflightToolPermission(ctx, e.permissions, e.registry, e.hooks, tool, args, e.workDir)
}

// ContextWithHeadlessToolPolicy freezes schema omissions once per invocation.
// It changes advertisement only; hallucinated calls still reach normal policy
// and strict-permissions handling, rather than becoming capability violations.
func (e *Executor) ContextWithHeadlessToolPolicy(ctx context.Context) context.Context {
	return ContextWithHeadlessToolPolicy(ctx, e.registry, e.permissions, e.hooks)
}

func potentialSkillPermissionGrants(registry ToolRegistry) []string {
	skill := ownedSkillTool(registry)
	if skill == nil {
		return nil
	}
	skill.ReloadCatalog()
	trusted := skill.WorkspaceTrusted()
	var grants []string
	for _, entry := range skill.catalog.List() {
		if entry.PersonalSource() || trusted {
			grants = append(grants, entry.AllowedTools...)
		}
	}
	return grants
}

// ContextWithHeadlessToolPolicy snapshots the owning registry's effective
// grants. Delegated runs must replace inherited omissions after skill seeding.
func ContextWithHeadlessToolPolicy(ctx context.Context, registry ToolRegistry, manager *permission.Manager, hookManager *hooks.Manager) context.Context {
	if !isHeadless(ctx) {
		return ctx
	}
	policy := headlessToolPolicy{}
	verificationDenied, verificationAvailable := false, false
	grants, denies := SkillPermissionRules(ctx, registry)
	potentialGrants := potentialSkillPermissionGrants(registry)
	for _, name := range registry.Names() {
		tool, ok := registry.Get(name)
		if !ok {
			continue
		}
		toolCtx := permission.ContextWithToolIdentity(ctx, name, PermissionToolIdentity(tool))
		hardDeny := manager.UnconditionallyDenied(toolCtx, name, false)
		hide := hardDeny || len(grants) == 0 && !hasPermissionPreHooks(ctx, registry, hookManager, name) && manager.UnconditionallyDenied(toolCtx, name, true)
		for _, deny := range denies {
			hide = hide || permission.ToolDenyRuleMatchesName(deny, name) || permission.ToolDenyRuleMatchesName(deny, PermissionToolIdentity(tool))
		}
		// Model-invoked skills may activate these grants later. Keep their
		// tools visible from the first request; dispatch still enforces policy.
		for _, grant := range potentialGrants {
			grantName, _, _ := strings.Cut(grant, "(")
			if permission.ToolDenyRuleMatchesName(grantName, name) || permission.ToolDenyRuleMatchesName(grantName, PermissionToolIdentity(tool)) {
				hide = false
				break
			}
		}
		if hide {
			policy.denied = append(policy.denied, name)
		}
		if name == "bash" || name == "verify_code" || name == "run_tests" {
			verificationDenied = verificationDenied || hide
			verificationAvailable = verificationAvailable || !hide
		}
	}
	policy.verificationUnavailable = verificationDenied && !verificationAvailable
	return context.WithValue(ctx, headlessToolPolicyKey{}, policy)
}

func FilterHeadlessTools(ctx context.Context, declarations []*genai.Tool) []*genai.Tool {
	policy, _ := ctx.Value(headlessToolPolicyKey{}).(headlessToolPolicy)
	return FilterGeminiToolsExcluding(declarations, policy.denied...)
}

func HeadlessVerificationUnavailable(ctx context.Context) bool {
	policy, _ := ctx.Value(headlessToolPolicyKey{}).(headlessToolPolicy)
	return policy.verificationUnavailable
}
