package tools

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"gokin/internal/hooks"
	"gokin/internal/skills"
)

func ownedSkillTool(registry ToolRegistry) *SkillTool {
	if registry == nil {
		return nil
	}
	value := reflect.ValueOf(registry)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return nil
	}
	registered, _ := registry.Get("skill")
	skill, _ := registered.(*SkillTool)
	return skill
}

// ValidateSkillHookRestore checks a candidate before session publication.
// A saved session is never a source of executable shell commands.
func ValidateSkillHookRestore(registry ToolRegistry, manager *hooks.Manager, activations []skills.HookActivation) error {
	if err := manager.CheckReady(); err != nil {
		return err
	}
	if err := skills.ValidateHookActivations(activations); err != nil {
		return err
	}
	if len(activations) == 0 {
		return nil
	}
	skill := ownedSkillTool(registry)
	if skill == nil {
		return fmt.Errorf("saved skill hooks require an owning skill tool")
	}
	_, err := skill.resolveHookActivations(manager, activations)
	return err
}

func (t *SkillTool) resolveHookActivations(manager *hooks.Manager, activations []skills.HookActivation) ([]skills.Skill, error) {
	if err := skills.ValidateHookActivations(activations); err != nil {
		return nil, err
	}
	if len(activations) == 0 {
		return nil, nil
	}
	if t.catalog == nil {
		return nil, fmt.Errorf("saved skill hooks require an owning skill catalog")
	}
	t.catalog.Reload()
	definitions := make([]skills.Skill, len(activations))
	for i, activation := range activations {
		definition, present := t.catalog.Get(activation.Name)
		if !present || definition.Path != activation.Path || definition.Source != activation.Source {
			return nil, fmt.Errorf("cannot restore hooks for skill %q: its original catalog source is unavailable or replaced", activation.Name)
		}
		if !definition.PersonalSource() && !t.WorkspaceTrusted() {
			return nil, fmt.Errorf("cannot restore hooks for skill %q: workspace is not trusted", activation.Name)
		}
		if err := manager.CommandsPolicyError(definition.Hooks); err != nil {
			return nil, fmt.Errorf("saved %w", err)
		}
		if hooks.CommandFingerprint(definition.Hooks) != activation.DefinitionHash || len(definition.Hooks) != len(activation.Consumed) {
			return nil, fmt.Errorf("cannot restore hooks for skill %q: hook definitions changed; restore the definition before resuming", activation.Name)
		}
		for index, consumed := range activation.Consumed {
			if consumed && !definition.Hooks[index].Once {
				return nil, fmt.Errorf("cannot restore hooks for skill %q: saved state consumed a non-once handler", activation.Name)
			}
		}
		definitions[i] = definition
	}
	return definitions, nil
}

func (t *SkillTool) synchronizeHooks(manager *hooks.Manager) error {
	t.hookSyncMu.Lock()
	defer t.hookSyncMu.Unlock()
	ledger := t.InvocationLedger()
	activations, epoch := ledger.HookActivations()
	trusted, enabled := t.WorkspaceTrusted(), manager.SkillHooksAllowed()
	policyRevision := manager.ExecutionRevision()
	hasPlugin := false
	for _, activation := range activations {
		hasPlugin = hasPlugin || strings.HasPrefix(activation.Source, "claude-plugin:")
	}
	if t.hookLedger == ledger && t.hookEpoch == epoch && t.hookTrusted == trusted && t.hookEnabled == enabled && t.hookPolicyRevision == policyRevision && t.hookSyncError == nil && !hasPlugin {
		return nil
	}
	if t.hookLedger == nil && epoch == 0 && len(activations) == 0 {
		// Embedders may seed this fresh runtime scope directly. It has no
		// restored state to reconcile; ownership boundaries still clear it.
		t.hookLedger, t.hookEpoch = ledger, epoch
		t.hookTrusted, t.hookEnabled, t.hookSyncError = trusted, enabled, nil
		t.hookPolicyRevision = policyRevision
		return nil
	}
	scope := t.HookScope()
	definitions, err := t.resolveHookActivations(manager, activations)
	if err != nil {
		t.hookSyncError = err
		scope.SetRestoreError(err)
		return err
	}
	if t.hookLedger == ledger && t.hookEpoch == epoch {
		// A policy toggle changes authority, not the owner or definitions.
		// Preserve active once claims and in-progress render reservations.
		current := make(map[string][]hooks.ClaudeCommand, len(activations))
		for i, activation := range activations {
			current[activation.Path] = definitions[i].Hooks
		}
		if err := scope.RefreshPluginProvenance(current); err != nil {
			t.hookSyncError = err
			scope.SetRestoreError(err)
			return err
		}
		scope.SetRestoreError(nil)
		t.hookTrusted, t.hookEnabled, t.hookSyncError = trusted, enabled, nil
		t.hookPolicyRevision = policyRevision
		return nil
	}
	prepared := &hooks.Scope{}
	for i, activation := range activations {
		registration, prepareErr := prepared.Prepare(activation.Path, definitions[i].Hooks)
		if prepareErr != nil {
			err = prepareErr
			break
		}
		if err = registration.RestoreConsumed(activation.Consumed); err != nil {
			registration.Cancel()
			break
		}
		observeHookConsumption(registration, ledger, activation, epoch)
		registration.Commit()
	}
	if _, current := ledger.HookActivations(); current != epoch && err == nil {
		err = fmt.Errorf("skill hook owner changed during restoration")
	}
	if err != nil {
		t.hookSyncError = err
		scope.SetRestoreError(err)
		return err
	}
	scope.Adopt(prepared)
	t.hookLedger, t.hookEpoch = ledger, epoch
	t.hookTrusted, t.hookEnabled, t.hookSyncError = trusted, enabled, nil
	t.hookPolicyRevision = policyRevision
	return nil
}

func observeHookConsumption(registration *hooks.Registration, ledger *skills.InvocationLedger, activation skills.HookActivation, epoch uint64) {
	registration.ObserveOnce(func(index int) { ledger.ConsumeHook(activation.Path, activation.Generation, epoch, index) })
	// Async render hooks can finish after the activation snapshot but before
	// the observer is installed. Replay those completions; consumption is
	// idempotent and still guarded by the activation generation/owner epoch.
	for index, consumed := range registration.Consumed() {
		if consumed {
			ledger.ConsumeHook(activation.Path, activation.Generation, epoch, index)
		}
	}
}

func RestoreSkillHooks(registry ToolRegistry, manager *hooks.Manager) error {
	if skill := ownedSkillTool(registry); skill != nil {
		return skill.synchronizeHooks(manager)
	}
	return nil
}

func BindSkillInvocationLedger(registry ToolRegistry, ledger *skills.InvocationLedger) {
	if skill := ownedSkillTool(registry); skill != nil && skill.InvocationLedger() != ledger {
		skill.SetInvocationLedger(ledger)
	}
}

func SkillHooksError(ctx context.Context) error {
	local, _ := ctx.Value(skillHookManagerKey{}).(skillHookManager)
	if err := local.manager.CheckReady(); err != nil {
		return &HookExecutionStoppedError{Reason: err.Error()}
	}
	scope := hooks.ScopeFromContext(ctx)
	if err := scope.RestoreError(); err != nil {
		return &HookExecutionStoppedError{Reason: "Cannot restore required skill hooks: " + err.Error()}
	}
	if scope.HasRegistrations() {
		if err := scope.PolicyError(local.manager); err != nil {
			return &HookExecutionStoppedError{Reason: "Required skill hooks cannot run: " + err.Error()}
		}
	}
	return nil
}

func (t *SkillTool) revokeUntrustedHooks() {
	t.hookSyncMu.Lock()
	defer t.hookSyncMu.Unlock()
	activations, _ := t.InvocationLedger().HookActivations()
	for _, activation := range activations {
		definition, ok := t.catalog.Get(activation.Name)
		if !ok || !definition.PersonalSource() {
			err := fmt.Errorf("required skill hook workspace trust was revoked")
			t.hookSyncError = err
			t.HookScope().SetRestoreError(err)
			return
		}
	}
}

func (t *SkillTool) clearHooks() {
	t.hookSyncMu.Lock()
	defer t.hookSyncMu.Unlock()
	t.InvocationLedger().ClearHookActivations()
	t.HookScope().Clear()
	t.hookLedger = nil
	t.hookSyncError = nil
}
