package skills

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"gokin/internal/hooks"
)

// HookActivation stores references, never executable commands. Restoring an
// activation requires its exact definition from the current skill catalog.
// Consumed retains successful once handlers, including those used by rendering.
type HookActivation struct {
	Name           string `json:"name"`
	Source         string `json:"source"`
	Path           string `json:"path"`
	DefinitionHash string `json:"definition_hash"`
	Generation     string `json:"generation"`
	Consumed       []bool `json:"consumed"`
}

func NewHookActivation(name, source, path string, definitions []hooks.ClaudeCommand) (HookActivation, error) {
	var generation [16]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return HookActivation{}, err
	}
	activation := HookActivation{Name: name, Source: source, Path: path,
		DefinitionHash: hooks.CommandFingerprint(definitions), Generation: hex.EncodeToString(generation[:]), Consumed: make([]bool, len(definitions))}
	return activation, ValidateHookActivations([]HookActivation{activation})
}

func CloneHookActivations(activations []HookActivation) []HookActivation {
	if activations == nil {
		return nil
	}
	cloned := append([]HookActivation{}, activations...)
	for i := range cloned {
		cloned[i].Consumed = append([]bool(nil), activations[i].Consumed...)
	}
	return cloned
}

// All-or-nothing validation: dropping a corrupt activation would silently
// remove an enforced check from an otherwise retained conversation.
func ValidateHookActivations(activations []HookActivation) error {
	if len(activations) > 256 {
		return fmt.Errorf("skill hook activations exceed 256 groups")
	}
	seen := make(map[string]bool, len(activations))
	count := 0
	for i, activation := range activations {
		if !ValidInvocationName(activation.Name) || activation.Source == "" ||
			!utf8.ValidString(activation.Source) || len(activation.Source) > MaxInvocationSourceBytes ||
			activation.Path == "" || !utf8.ValidString(activation.Path) || len(activation.Path) > MaxInvocationPathBytes ||
			strings.ContainsRune(activation.Path, 0) || strings.ContainsRune(activation.Source, 0) {
			return fmt.Errorf("skill hook activation[%d] has invalid provenance", i)
		}
		if seen[activation.Path] {
			return fmt.Errorf("duplicate skill hook activation for %q", activation.Name)
		}
		seen[activation.Path] = true
		for _, digest := range []struct {
			value string
			bytes int
		}{{activation.DefinitionHash, 32}, {activation.Generation, 16}} {
			if len(digest.value) != digest.bytes*2 {
				return fmt.Errorf("skill hook activation[%d] has an invalid fingerprint or generation", i)
			}
			decoded, err := hex.DecodeString(digest.value)
			if err != nil || len(decoded) != digest.bytes || digest.value != strings.ToLower(digest.value) {
				return fmt.Errorf("skill hook activation[%d] has an invalid fingerprint or generation", i)
			}
		}
		if len(activation.Consumed) == 0 || len(activation.Consumed) > 32 {
			return fmt.Errorf("skill hook activation[%d] must reference 1 to 32 handlers", i)
		}
		count += len(activation.Consumed)
		if count > 256 {
			return fmt.Errorf("session skill hooks exceed 256 handlers")
		}
	}
	return nil
}

// HookActivations returns a defensive snapshot and the restoration epoch.
// The epoch changes only at ownership/rollback boundaries, not on once use.
func (l *InvocationLedger) HookActivations() ([]HookActivation, uint64) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	activations := make([]HookActivation, 0, len(l.hookActivations))
	for _, activation := range l.hookActivations {
		activations = append(activations, activation)
	}
	sort.Slice(activations, func(i, j int) bool { return activations[i].Path < activations[j].Path })
	return CloneHookActivations(activations), l.hookEpoch
}

func (l *InvocationLedger) SnapshotWithHooks() ([]Invocation, []HookActivation) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	invocations := invocationValuesNewestFirst(l.latest)
	for i := range invocations {
		invocations[i] = cloneInvocation(invocations[i])
	}
	activations := make([]HookActivation, 0, len(l.hookActivations))
	for _, activation := range l.hookActivations {
		activations = append(activations, activation)
	}
	sort.Slice(activations, func(i, j int) bool { return activations[i].Path < activations[j].Path })
	return invocations, CloneHookActivations(activations)
}

func (l *InvocationLedger) RestoreHookActivations(activations []HookActivation) error {
	if err := ValidateHookActivations(activations); err != nil {
		return err
	}
	next := make(map[string]HookActivation, len(activations))
	for _, activation := range CloneHookActivations(activations) {
		next[activation.Path] = activation
	}
	l.mu.Lock()
	l.hookActivations = next
	l.hookEpoch++
	l.mu.Unlock()
	return nil
}

func (l *InvocationLedger) registerHookActivationLocked(activation HookActivation) (HookActivation, error) {
	if l.hookActivations == nil {
		l.hookActivations = make(map[string]HookActivation)
	}
	if previous, ok := l.hookActivations[activation.Path]; ok && previous.DefinitionHash == activation.DefinitionHash {
		if len(previous.Consumed) != len(activation.Consumed) {
			return HookActivation{}, fmt.Errorf("persisted skill hook handler count changed")
		}
		activation.Generation = previous.Generation
		for i, consumed := range previous.Consumed {
			activation.Consumed[i] = activation.Consumed[i] || consumed
		}
	}
	count := len(activation.Consumed)
	for path, existing := range l.hookActivations {
		if path != activation.Path {
			count += len(existing.Consumed)
		}
	}
	if count > 256 {
		return HookActivation{}, fmt.Errorf("session skill hooks exceed 256 handlers")
	}
	activation = CloneHookActivations([]HookActivation{activation})[0]
	l.hookActivations[activation.Path] = activation
	return CloneHookActivations([]HookActivation{activation})[0], nil
}

// RecordSkillActivation publishes rendered instructions and their hook
// references in the same transaction. A failed hook registration must not
// leave newly retained instructions running without their required checks.
func (l *InvocationLedger) RecordSkillActivation(name, rendered, source, path string, activation *HookActivation, epoch uint64) (Invocation, bool, HookActivation, error) {
	normalized, err := validateInvocationFields(name, rendered, source, path)
	if err != nil {
		return Invocation{}, false, HookActivation{}, err
	}
	if activation != nil {
		if err := ValidateHookActivations([]HookActivation{*activation}); err != nil {
			return Invocation{}, false, HookActivation{}, err
		}
		if activation.Name != normalized || activation.Source != source || activation.Path != path {
			return Invocation{}, false, HookActivation{}, fmt.Errorf("skill hook provenance does not match its invocation")
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if epoch != l.hookEpoch {
		return Invocation{}, false, HookActivation{}, fmt.Errorf("skill hook owner changed during invocation")
	}
	var registered HookActivation
	if activation != nil {
		registered, err = l.registerHookActivationLocked(CloneHookActivations([]HookActivation{*activation})[0])
		if err != nil {
			return Invocation{}, false, HookActivation{}, err
		}
	} else {
		delete(l.hookActivations, path)
	}
	l.ensureInitializedLocked()
	invocation, changed := l.recordInvocationLocked(normalized, rendered, source, path, renderHash(rendered))
	return invocation, changed, registered, nil
}

func (l *InvocationLedger) ConsumeHook(path, generation string, epoch uint64, index int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	activation, present := l.hookActivations[path]
	if epoch != l.hookEpoch || !present || activation.Generation != generation || index < 0 || index >= len(activation.Consumed) {
		return
	}
	activation.Consumed[index] = true
}

func (l *InvocationLedger) ClearHookActivations() {
	l.mu.Lock()
	l.hookActivations = nil
	l.hookEpoch++
	l.mu.Unlock()
}
