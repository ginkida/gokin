package tools

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/client"
	"gokin/internal/hooks"
	"gokin/internal/permission"
	"gokin/internal/skills"
)

type skillShellInvokerKey struct{}
type skillRenderRulesKey struct{}
type skillRenderRules struct {
	owner          ToolRegistry
	grants, denies []string
	effort         string
}

type skillRenderPolicyError struct {
	kind        PolicyBlockKind
	reason      string
	terminal    bool
	recoverable bool
}

func (e *skillRenderPolicyError) Error() string { return e.reason }

// SkillShellInvoker dispatches a shell command through its owning executor or
// agent. No SkillTool retains another loop's dispatcher across cloning.
type SkillShellInvoker func(context.Context, string) (ToolResult, error)

func ContextWithSkillShellInvoker(ctx context.Context, invoke SkillShellInvoker) context.Context {
	return context.WithValue(ctx, skillShellInvokerKey{}, invoke)
}

func (e *Executor) SkillInvocationContext(ctx context.Context) context.Context {
	if e == nil {
		return ctx
	}
	ctx = ContextWithSkillHooks(ctx, e.registry, e.hooks)
	ctx = ContextWithSkillToolDispatcher(ctx, e.registry, e.InvokeTool)
	ctx = ContextWithAgentHookDispatcher(ctx, e.registry, e.InvokeTool)
	local, bound := ctx.Value(skillModelResolverKey{}).(skillModelResolver)
	if !bound || !sameSkillRuleOwner(local.owner, e.registry) {
		if c := e.getClient(); c != nil {
			ctx = ContextWithSkillModelClient(ctx, e.registry, c)
		}
	}
	info := hooks.RuntimeInfoFromContext(ctx)
	e.sessionIDMu.RLock()
	info.SessionID = e.sessionID
	e.sessionIDMu.RUnlock()
	if info.ProjectDir == "" {
		info.ProjectDir = e.workDir
	}
	base := e.getClient()
	if bound && sameSkillRuleOwner(local.owner, e.registry) && local.base != nil {
		base = local.base
	}
	if base != nil {
		info.Effort = client.EffectiveReasoningEffort(ctx, SkillScopedClient(base, e.registry))
	}
	ctx = hooks.ContextWithRuntimeInfo(ctx, info)
	if base != nil {
		hookCtx := ctx
		ctx = hooks.ContextWithPromptClient(ctx, SkillScopedClient(base, e.registry), func() error {
			if err := PermissionStopError(hookCtx); err != nil {
				return err
			}
			return SkillHooksError(ContextWithSkillHooks(hookCtx, e.registry, e.hooks))
		})
	}
	return ContextWithSkillShellInvoker(ctx, func(ctx context.Context, command string) (ToolResult, error) {
		return e.InvokeTool(ctx, "bash", map[string]any{"command": command})
	})
}

// SkillPermissionRules combines the current turn's rules with render-local
// rules. Rendering failures must not publish authority into a later turn.
func SkillPermissionRules(ctx context.Context, registry ToolRegistry) (grants, denies []string) {
	grants, denies = SkillPermissionGrants(registry), SkillPermissionDenies(registry)
	if rules, ok := ctx.Value(skillRenderRulesKey{}).(skillRenderRules); ok {
		if rules.owner == nil || sameSkillRuleOwner(rules.owner, registry) {
			grants = append(grants, rules.grants...)
		}
		denies = append(denies, rules.denies...)
	}
	return grants, denies
}

func (t *SkillTool) renderSkill(ctx context.Context, skill skills.Skill, arguments string, values []string) (string, error) {
	commands, err := skills.ParseDynamicCommands(skill.Body)
	if err != nil {
		return "", err
	}
	if len(commands) == 0 {
		return expandSkillArguments(skill, arguments, values, t.workDir)
	}
	if t.ShellExecutionDisabled() {
		var body strings.Builder
		previous := 0
		for _, command := range commands {
			body.WriteString(skill.Body[previous:command.Start])
			body.WriteString("[shell command execution disabled by policy]")
			previous = command.End
		}
		body.WriteString(skill.Body[previous:])
		skill.Body = body.String()
		return expandSkillArguments(skill, arguments, values, t.workDir)
	}
	invoke, ok := ctx.Value(skillShellInvokerKey{}).(SkillShellInvoker)
	if !ok || invoke == nil {
		return "", fmt.Errorf("dynamic context requires an owning tool executor")
	}
	if ceiling, restricted := ToolCapabilityCeilingFromContext(ctx); restricted && !toolCapabilityAllows(ceiling, "bash") {
		return "", &skillRenderPolicyError{kind: PolicyBlockPermission, reason: "dynamic context requires bash, which is outside the tool capability ceiling", terminal: true}
	}
	// Parse and expand every source fragment before starting the first command.
	// Inserted arguments and command output are never parsed as shell blocks.
	fragments := make([]string, 0, len(commands)*2+1)
	usedArguments := false
	previous := 0
	for _, command := range commands {
		for _, source := range []string{skill.Body[previous:command.Start], command.Command} {
			fragment := skill
			fragment.Body = source
			expanded, used, err := expandSkillArgumentFragment(fragment, arguments, values, t.workDir)
			if err != nil {
				return "", err
			}
			fragments = append(fragments, expanded)
			usedArguments = usedArguments || used
		}
		previous = command.End
	}
	fragment := skill
	fragment.Body = skill.Body[previous:]
	expanded, used, err := expandSkillArgumentFragment(fragment, arguments, values, t.workDir)
	if err != nil {
		return "", err
	}
	fragments = append(fragments, expanded)
	usedArguments = usedArguments || used
	expandedBytes := 0
	for _, fragment := range fragments {
		expandedBytes += len(fragment)
	}
	if expandedBytes > skills.MaxSkillBytes {
		return "", fmt.Errorf("expanded workflow exceeds the %d-byte limit", skills.MaxSkillBytes)
	}
	for i := 1; i < len(fragments); i += 2 {
		if strings.TrimSpace(fragments[i]) == "" {
			return "", fmt.Errorf("dynamic context command %d expanded to an empty command", i/2+1)
		}
	}

	rules := skillRenderRules{denies: append([]string(nil), skill.DisallowedTools...)}
	if inherited, ok := ctx.Value(skillRenderRulesKey{}).(skillRenderRules); ok {
		rules.denies = append(rules.denies, inherited.denies...)
		dispatcher, _ := ctx.Value(skillForkDispatcherKey{}).(skillForkDispatcher)
		if inherited.owner == nil || sameSkillRuleOwner(inherited.owner, dispatcher.registry) {
			rules.grants = append(rules.grants, inherited.grants...)
		}
	}
	if skill.PersonalSource() || t.WorkspaceTrusted() {
		rules.grants = append(rules.grants, skill.AllowedTools...)
	}
	ctx = permission.ContextWithoutPermissionPrompt(context.WithValue(ctx, skillRenderRulesKey{}, rules))
	var body strings.Builder
	for i, fragment := range fragments {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if i%2 == 1 {
			denies := append(t.ActivePermissionDenies(), rules.denies...)
			if permission.TemporaryToolRulesDeny(denies, "bash", map[string]any{"command": fragment}, t.workDir) {
				return "", &skillRenderPolicyError{kind: PolicyBlockPermission, reason: fmt.Sprintf("dynamic context command %d is denied by the skill", i/2+1)}
			}
			result, err := invoke(ctx, fragment)
			if err != nil {
				return "", fmt.Errorf("dynamic context command %d: %w", i/2+1, err)
			}
			if result.HookStopReason != "" {
				return "", fmt.Errorf("dynamic context command %d: %w", i/2+1, &HookExecutionStoppedError{Reason: result.HookStopReason})
			}
			if !result.Success {
				if result.PolicyBlock != nil {
					return "", &skillRenderPolicyError{kind: result.PolicyBlock.Kind, reason: fmt.Sprintf("dynamic context command %d blocked: %s", i/2+1, result.Error), terminal: result.PolicyBlock.Terminal, recoverable: result.PolicyBlock.Recoverable}
				}
				return "", fmt.Errorf("dynamic context command %d failed: %s", i/2+1, skillPayloadRedactor.Redact(result.Error))
			}
			fragment = result.Content
		}
		if err := appendSkillExpansion(&body, fragment); err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(arguments) != "" && !usedArguments {
		if err := appendSkillExpansion(&body, "\n\nARGUMENTS: "+arguments); err != nil {
			return "", err
		}
	}
	return body.String(), nil
}

func (e *skillRenderPolicyError) result(reason string) ToolResult {
	r := NewPolicyBlockedResult(e.kind, reason)
	r.PolicyBlock.Terminal = e.terminal
	r.PolicyBlock.Recoverable = e.recoverable
	return r
}
