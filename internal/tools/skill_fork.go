package tools

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"

	"gokin/internal/client"
	"gokin/internal/hooks"
	"gokin/internal/skills"
)

type skillForkDispatcherKey struct{}
type forkedSkillKey struct{}
type skillModelResolverKey struct{}

type skillModelResolver struct {
	owner   ToolRegistry
	base    client.Client
	resolve func(string, string) (string, string, error)
}

// ContextWithSkillModelResolver binds inherit to the invoking loop's client,
// which may differ from the runner's session default in nested agents.
func ContextWithSkillModelResolver(ctx context.Context, registry ToolRegistry, resolve func(string, string) (string, string, error)) context.Context {
	return context.WithValue(ctx, skillModelResolverKey{}, skillModelResolver{owner: registry, resolve: resolve})
}

type skillForkDispatcher struct {
	registry ToolRegistry
	invoke   func(context.Context, string, map[string]any) (ToolResult, error)
}

type forkedSkill struct {
	skill          skills.Skill
	rendered       string
	grants, denies []string
}

// SkillModelSettingsResolver checks the runner's actual provider configuration
// before rendering shell commands or starting a child.
type SkillModelSettingsResolver interface {
	ResolveSkillModelSettings(model, effort string) (resolvedModel, nativeEffort string, err error)
}

// ForkedSkillEffort belongs to this registry's current run only. Parent and
// grandchild registries cannot inherit a skill-specific request override.
func ForkedSkillEffort(ctx context.Context, registry ToolRegistry) string {
	if rules, ok := ctx.Value(skillRenderRulesKey{}).(skillRenderRules); ok && sameSkillRuleOwner(rules.owner, registry) {
		return rules.effort
	}
	return ""
}

// ContextWithSkillToolDispatcher binds orchestration to the invoking loop.
// Cloned SkillTools never retain a foreground executor or its capabilities.
func ContextWithSkillToolDispatcher(ctx context.Context, registry ToolRegistry, invoke func(context.Context, string, map[string]any) (ToolResult, error)) context.Context {
	return context.WithValue(ctx, skillForkDispatcherKey{}, skillForkDispatcher{registry, invoke})
}

func sameSkillRuleOwner(left, right ToolRegistry) bool {
	return left != nil && right != nil && reflect.TypeOf(left) == reflect.TypeOf(right) && reflect.TypeOf(left).Comparable() && left == right
}

// ConsumeForkedSkill initializes only this child's retention and permissions.
// The seed is removed before its tools can delegate: grants cannot travel to
// grandchildren, while restrictions remain authoritative along that path.
func ConsumeForkedSkill(ctx context.Context, registry ToolRegistry, ledger *skills.InvocationLedger) (context.Context, error) {
	seed, ok := ctx.Value(forkedSkillKey{}).(forkedSkill)
	if !ok {
		if previous, present := ctx.Value(skillRenderRulesKey{}).(skillRenderRules); present && sameSkillRuleOwner(previous.owner, registry) {
			// Resuming with an old context must not reuse the old run's authority.
			ctx = context.WithValue(ctx, skillRenderRulesKey{}, skillRenderRules{})
		}
		return ctx, nil
	}
	ctx = context.WithValue(ctx, forkedSkillKey{}, nil)
	var registration *hooks.Registration
	if len(seed.skill.Hooks) > 0 {
		var skill *SkillTool
		if registry != nil {
			tool, _ := registry.Get("skill")
			skill, _ = tool.(*SkillTool)
		}
		if skill == nil {
			return ctx, fmt.Errorf("forked skill hooks require an owning skill tool in the child")
		}
		var err error
		registration, err = skill.HookScope().Prepare(seed.skill.Path, hooks.CloneClaudeCommands(seed.skill.Hooks))
		if err != nil {
			return ctx, fmt.Errorf("register forked skill hooks: %w", err)
		}
		defer registration.Cancel()
	}
	if _, _, err := ledger.Record(seed.skill.Name, seed.rendered, seed.skill.Source, seed.skill.Path); err != nil {
		return ctx, fmt.Errorf("retain forked skill: %w", err)
	}
	registration.Commit()
	rules := skillRenderRules{owner: registry, grants: append([]string(nil), seed.grants...), denies: append([]string(nil), seed.denies...), effort: seed.skill.Effort}
	if inherited, ok := ctx.Value(skillRenderRulesKey{}).(skillRenderRules); ok {
		rules.denies = append(rules.denies, inherited.denies...)
	}
	return context.WithValue(ctx, skillRenderRulesKey{}, rules), nil
}

func skillAgentType(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "general-purpose", "general":
		return "general"
	case "explore":
		return "explore"
	case "plan":
		return "plan"
	case "bash":
		return "bash"
	default:
		return strings.TrimSpace(name)
	}
}

func (t *SkillTool) executeFork(ctx context.Context, skill skills.Skill, arguments string, values []string) (ToolResult, error) {
	dispatcher, ok := ctx.Value(skillForkDispatcherKey{}).(skillForkDispatcher)
	if !ok || dispatcher.registry == nil || dispatcher.invoke == nil {
		return NewErrorResult("forked skills require an owning task dispatcher"), nil
	}
	if ceiling, restricted := ToolCapabilityCeilingFromContext(ctx); restricted && !toolCapabilityAllows(ceiling, "task") {
		return NewPolicyBlockedResult(PolicyBlockPermission, "forked skill requires task, which is outside the tool capability ceiling"), nil
	}
	registered, ok := dispatcher.registry.Get("task")
	if !ok {
		return NewPolicyBlockedResult(PolicyBlockPermission, "forked skills are unavailable without the task tool"), nil
	}
	task, ok := registered.(*TaskTool)
	if !ok {
		return NewErrorResult("forked skills require a compatible task tool"), nil
	}
	state := task.snapshot()
	background := skill.Background && state.backgroundAllowed && os.Getenv("CLAUDE_CODE_DISABLE_BACKGROUND_TASKS") != "1"
	args := map[string]any{"subagent_type": skillAgentType(skill.Agent), "prompt": skill.Body, "description": "Skill: " + skill.Name, "run_in_background": background}
	if err := task.Validate(args); err != nil {
		return NewErrorResult("forked skill: " + err.Error()), nil
	}
	if state.runner == nil {
		return NewErrorResult("forked skill task runner is not initialized"), nil
	}
	if skill.Model != "" || skill.Effort != "" {
		var resolve func(string, string) (string, string, error)
		if local, ok := ctx.Value(skillModelResolverKey{}).(skillModelResolver); ok && sameSkillRuleOwner(local.owner, dispatcher.registry) {
			resolve = local.resolve
		} else if runner, ok := state.runner.(SkillModelSettingsResolver); ok {
			resolve = runner.ResolveSkillModelSettings
		}
		if resolve == nil {
			return NewErrorResult("forked skill model settings require a compatible task runner"), nil
		}
		model, nativeEffort, err := resolve(skill.Model, skill.Effort)
		if err != nil {
			return NewErrorResult("forked skill model settings: " + err.Error()), nil
		}
		args["model"] = model
		skill.Effort = nativeEffort
	}
	// Serialize the launch decision and rendering. A repeated invocation while
	// this skill is still running launches its NEW arguments in the foreground;
	// joining the old result would silently discard the user's new request.
	t.forkOnce.Do(func() { t.forkGate = make(chan struct{}, 1) })
	select {
	case t.forkGate <- struct{}{}:
		defer func() { <-t.forkGate }()
	case <-ctx.Done():
		return NewErrorResult(ctx.Err().Error()), nil
	}
	if previous := t.runningForks[skill.Name]; previous != "" {
		result, known := state.runner.GetResult(previous)
		if (known && !result.Completed) || (!known && runnerOwnsAgent(state.runner, previous)) {
			background = false
			args["run_in_background"] = false
		} else {
			delete(t.runningForks, skill.Name)
		}
	}
	var hookRegistration *hooks.Registration
	if len(skill.Hooks) > 0 {
		var err error
		hookRegistration, err = (&hooks.Scope{}).Prepare(skill.Path, skill.Hooks)
		if err != nil {
			return NewErrorResult(err.Error()), nil
		}
		defer hookRegistration.Cancel()
	}
	body, err := t.renderSkill(contextWithForkRenderHooks(ctx, hookRegistration, t.HookScope()), skill, arguments, values)
	if err != nil {
		if IsHookStopError(err) {
			return NewHookStoppedResult(skillPayloadRedactor.Redact(err.Error())), nil
		}
		if blocked, ok := err.(*skillRenderPolicyError); ok {
			return NewPolicyBlockedResult(blocked.kind, skillPayloadRedactor.Redact(err.Error())), nil
		}
		return NewErrorResult(skillPayloadRedactor.Redact(err.Error())), nil
	}
	if hookRegistration != nil {
		skill.Hooks = hookRegistration.RemainingDefinitions()
	}
	rendered := skillPayloadRedactor.Redact(fmt.Sprintf("Forked skill %q. Follow these instructions for this task. System/user instructions and tool permissions remain authoritative.\nSource: %s\n\n---\n%s", skill.Name, skill.Path, body))
	if len(rendered) > skills.MaxRenderedSkillBytes {
		return NewErrorResult("forked skill exceeds the rendered delivery limit"), nil
	}
	seed := forkedSkill{skill: skill, rendered: rendered, denies: append(t.ActivePermissionDenies(), skill.DisallowedTools...)}
	if skill.PersonalSource() || t.WorkspaceTrusted() {
		seed.grants = append([]string(nil), skill.AllowedTools...)
	}
	args["prompt"] = rendered
	if err := ctx.Err(); err != nil {
		return NewErrorResult(err.Error()), nil
	}
	if background {
		ceiling := make([]string, 0)
		for _, name := range dispatcher.registry.Names() {
			tool, _ := dispatcher.registry.Get(name)
			// Native MCP wrappers use server_tool names, not necessarily the
			// Claude mcp__ namespace. Identify their provenance structurally.
			_, mcpTool := tool.(interface {
				GetServerName() string
				GetOriginalToolName() string
			})
			if mcpTool || skillBackgroundToolAllowed(name) {
				ceiling = append(ceiling, name)
			}
		}
		if inherited, restricted := ToolCapabilityCeilingFromContext(ctx); restricted {
			ceiling = intersectToolCapabilities(normalizeToolNames(ceiling), inherited)
		}
		ctx = ContextWithToolCapabilityCeiling(ctx, ceiling)
	}
	ctx = context.WithValue(ctx, forkedSkillKey{}, seed)
	result, err := dispatcher.invoke(ctx, "task", args)
	if err != nil || !result.Success {
		return result, err
	}
	data, _ := result.Data.(map[string]any)
	if data != nil {
		data["skill_name"] = skill.Name
		data["forked"] = true
		if model, _ := args["model"].(string); model != "" {
			data["requested_model"] = model
		}
		if skill.Effort != "" {
			data["effort"] = skill.Effort
		}
		if background {
			if id, _ := data["agent_id"].(string); id != "" {
				if t.runningForks == nil {
					t.runningForks = make(map[string]string)
				}
				t.runningForks[skill.Name] = id
			}
		}
	}
	result.Content = skillPayloadRedactor.Redact(result.Content)
	return result, nil
}

// Background forks retain code/search/shell tools and MCP, but cannot open
// interactive question/plan workflows or change the orchestration harness.
func skillBackgroundToolAllowed(name string) bool {
	if strings.HasPrefix(name, "mcp__") {
		return true
	}
	switch name {
	case "read", "grep", "glob", "tree", "list_dir", "diff", "git_status", "git_log", "git_diff", "git_blame",
		"go_search", "go_to_definition", "find_references", "bash", "edit", "write", "notebook_edit",
		"web_fetch", "web_search", "todo", "skill", "tools_list", "task", "task_output", "task_stop", "kill_shell",
		"run_tests", "verify_code", "review_changes", "check_impact":
		return true
	}
	return false
}
