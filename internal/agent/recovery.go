package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"gokin/internal/logging"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

// RecoveryExecutor attempts automatic recovery from tool errors using AutoFixAction.
type RecoveryExecutor struct {
	maxAttempts int
	policy      *ToolRecoveryPolicy
	mu          sync.Mutex
	attempts    map[string]*recoveryAttemptState
	lastPrune   time.Time
}

type recoveryPhase string

const (
	recoveryPhaseClassify    recoveryPhase = "classify"
	recoveryPhaseFixChain    recoveryPhase = "fix_chain"
	recoveryPhaseAlternative recoveryPhase = "alternative_path"
	recoveryPhaseStop        recoveryPhase = "stop"

	recoveryStateTTL           = 45 * time.Minute
	recoveryStatePruneInterval = 3 * time.Minute
	recoveryStateMaxEntries    = 800
)

type recoveryAttemptState struct {
	Phase           recoveryPhase
	Category        string
	AttemptedFixes  map[string]bool
	FixChain        []*AutoFixAction
	FixCursor       int
	LastError       string
	RepeatedErrors  int
	RemainingBudget int
	AlternativeUsed bool
	LastTouched     time.Time
}

// NewRecoveryExecutor creates a new RecoveryExecutor with the given max attempts per call key.
func NewRecoveryExecutor(maxAttempts int) *RecoveryExecutor {
	if maxAttempts <= 0 {
		maxAttempts = 2
	}
	return &RecoveryExecutor{
		maxAttempts: maxAttempts,
		policy:      NewToolRecoveryPolicy(),
		attempts:    make(map[string]*recoveryAttemptState),
	}
}

// AttemptAutoFix tries to automatically recover from a tool error.
// Returns (result, true) if recovery succeeded, (nil, false) otherwise.
func (r *RecoveryExecutor) AttemptAutoFix(
	ctx context.Context,
	agent *Agent,
	originalCall *genai.FunctionCall,
	reflection *Reflection,
	attempt int,
) (tools.ToolResult, bool) {
	if reflection == nil || originalCall == nil {
		return tools.ToolResult{}, false
	}
	r.pruneStaleStates()
	if r.policy == nil {
		r.policy = NewToolRecoveryPolicy()
	}

	category := r.policy.Classify(reflection)
	stateKey := recoveryStateKey(originalCall, category)
	repeatCount := r.updateErrorRepeat(stateKey, category, reflection.Error)

	if attempt >= r.maxAttempts || repeatCount >= 2 {
		r.setPhase(stateKey, category, recoveryPhaseAlternative)
	}

	// Explicit FSM:
	// classify -> fix_chain -> alternative_path -> stop.
	for fsmSteps := 0; fsmSteps < 8; fsmSteps++ {
		switch r.getPhase(stateKey, category) {
		case recoveryPhaseClassify:
			r.prepareFixChain(stateKey, category, reflection, originalCall, repeatCount)
			continue

		case recoveryPhaseFixChain:
			fix, ok := r.nextFixAction(stateKey, category, repeatCount)
			if !ok {
				r.setPhase(stateKey, category, recoveryPhaseAlternative)
				continue
			}

			result, handled := r.executeFix(ctx, agent, originalCall, fix)
			if result.Success {
				r.clearAttemptState(stateKey)
				return result, true
			}
			if handled {
				if r.getRemainingBudget(stateKey, category) <= 0 {
					r.setPhase(stateKey, category, recoveryPhaseAlternative)
				}
				return result, true
			}
			if r.getRemainingBudget(stateKey, category) <= 0 {
				r.setPhase(stateKey, category, recoveryPhaseAlternative)
			}
			continue

		case recoveryPhaseAlternative:
			if !r.claimAlternativeBudget(stateKey, category) {
				r.setPhase(stateKey, category, recoveryPhaseStop)
				continue
			}
			result, handled := r.tryAlternativePath(ctx, agent, originalCall, reflection, category)
			if result.Success {
				r.clearAttemptState(stateKey)
				return result, true
			}
			if handled {
				r.setPhase(stateKey, category, recoveryPhaseStop)
				return result, true
			}
			r.setPhase(stateKey, category, recoveryPhaseStop)
			continue

		case recoveryPhaseStop:
			return tools.ToolResult{}, false

		default:
			r.setPhase(stateKey, category, recoveryPhaseStop)
		}
	}
	return tools.ToolResult{}, false
}

func (r *RecoveryExecutor) executeFix(
	ctx context.Context,
	agent *Agent,
	originalCall *genai.FunctionCall,
	fix *AutoFixAction,
) (tools.ToolResult, bool) {
	if fix == nil {
		return tools.ToolResult{}, false
	}

	switch fix.FixType {
	case "retry_with_args":
		return r.retryWithArgs(ctx, agent, originalCall, fix)
	case "run_tool_first":
		return r.runToolFirst(ctx, agent, originalCall, fix)
	case "modify_and_retry":
		return r.modifyAndRetry(ctx, agent, originalCall, fix)
	default:
		logging.Debug("unknown auto-fix type", "type", fix.FixType)
		return tools.ToolResult{}, false
	}
}

func (r *RecoveryExecutor) tryAlternativePath(
	ctx context.Context,
	agent *Agent,
	originalCall *genai.FunctionCall,
	reflection *Reflection,
	category string,
) (tools.ToolResult, bool) {
	if reflection == nil || r.policy == nil {
		return tools.ToolResult{}, false
	}
	alt := r.policy.AlternativeTool(category, reflection)
	if alt == "" {
		return tools.ToolResult{}, false
	}
	if alt != "read" && alt != "glob" && alt != "bash" {
		return tools.ToolResult{}, false
	}

	logging.Info("recovery budget exhausted, switching to alternative path",
		"tool", originalCall.Name,
		"alternative", alt,
		"category", reflection.Category)

	return r.executeFix(ctx, agent, originalCall, &AutoFixAction{
		FixType:  "run_tool_first",
		ToolName: alt,
	})
}

func recoveryStateKey(call *genai.FunctionCall, category string) string {
	if call == nil {
		return strings.TrimSpace(strings.ToLower(category))
	}
	category = strings.TrimSpace(strings.ToLower(category))
	args := ""
	if call.Args != nil {
		if encoded, err := json.Marshal(call.Args); err == nil {
			args = strings.Join(strings.Fields(strings.ToLower(string(encoded))), " ")
		}
	}
	if len(args) > 180 {
		args = args[:180]
	}
	return call.Name + "|" + category + "|" + args
}

func (r *RecoveryExecutor) clearAttemptState(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.attempts, key)
}

func (r *RecoveryExecutor) updateErrorRepeat(key, category, errorMsg string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.ensureStateLocked(key, category)

	fingerprint := strings.TrimSpace(strings.ToLower(errorMsg))
	fingerprint = strings.Join(strings.Fields(fingerprint), " ")
	if len(fingerprint) > 240 {
		fingerprint = fingerprint[:240]
	}
	if fingerprint == "" {
		return state.RepeatedErrors
	}
	if state.LastError == fingerprint {
		state.RepeatedErrors++
	} else {
		state.LastError = fingerprint
		state.RepeatedErrors = 0
	}
	return state.RepeatedErrors
}

func (r *RecoveryExecutor) ensureStateLocked(key, category string) *recoveryAttemptState {
	now := time.Now()
	state, ok := r.attempts[key]
	if !ok || state == nil {
		state = newRecoveryAttemptState(category, r.maxAttempts)
		r.attempts[key] = state
		state.LastTouched = now
		return state
	}

	normalizedCategory := strings.TrimSpace(strings.ToLower(category))
	if strings.TrimSpace(strings.ToLower(state.Category)) != normalizedCategory {
		state = newRecoveryAttemptState(category, r.maxAttempts)
		r.attempts[key] = state
		state.LastTouched = now
		return state
	}

	if state.AttemptedFixes == nil {
		state.AttemptedFixes = make(map[string]bool)
	}
	if state.RemainingBudget <= 0 && state.Phase == recoveryPhaseClassify {
		state.RemainingBudget = maxInt(1, r.maxAttempts)
	}
	state.LastTouched = now
	return state
}

func newRecoveryAttemptState(category string, budget int) *recoveryAttemptState {
	if budget <= 0 {
		budget = 1
	}
	return &recoveryAttemptState{
		Phase:           recoveryPhaseClassify,
		Category:        strings.TrimSpace(strings.ToLower(category)),
		AttemptedFixes:  make(map[string]bool),
		FixChain:        nil,
		FixCursor:       0,
		LastError:       "",
		RepeatedErrors:  0,
		RemainingBudget: budget,
		AlternativeUsed: false,
		LastTouched:     time.Now(),
	}
}

func (r *RecoveryExecutor) pruneStaleStates() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	if !r.lastPrune.IsZero() &&
		now.Sub(r.lastPrune) < recoveryStatePruneInterval &&
		len(r.attempts) < recoveryStateMaxEntries {
		return
	}
	r.lastPrune = now

	for key, state := range r.attempts {
		if state == nil {
			delete(r.attempts, key)
			continue
		}
		if state.LastTouched.IsZero() || now.Sub(state.LastTouched) > recoveryStateTTL {
			delete(r.attempts, key)
		}
	}

	if len(r.attempts) <= recoveryStateMaxEntries {
		return
	}

	type stateAge struct {
		Key  string
		Time time.Time
	}
	ages := make([]stateAge, 0, len(r.attempts))
	for key, state := range r.attempts {
		ts := state.LastTouched
		if ts.IsZero() {
			ts = time.Unix(0, 0)
		}
		ages = append(ages, stateAge{Key: key, Time: ts})
	}
	sort.Slice(ages, func(i, j int) bool {
		return ages[i].Time.Before(ages[j].Time)
	})

	excess := len(r.attempts) - recoveryStateMaxEntries
	for i := 0; i < excess && i < len(ages); i++ {
		delete(r.attempts, ages[i].Key)
	}
}

func (r *RecoveryExecutor) setPhase(key, category string, phase recoveryPhase) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.ensureStateLocked(key, category)
	state.Phase = phase
}

func (r *RecoveryExecutor) getPhase(key, category string) recoveryPhase {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.ensureStateLocked(key, category)
	return state.Phase
}

func (r *RecoveryExecutor) getRemainingBudget(key, category string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.ensureStateLocked(key, category)
	return state.RemainingBudget
}

func (r *RecoveryExecutor) claimAlternativeBudget(key, category string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.ensureStateLocked(key, category)
	if state.AlternativeUsed || state.RemainingBudget <= 0 {
		return false
	}
	state.AlternativeUsed = true
	state.RemainingBudget--
	return true
}

func (r *RecoveryExecutor) prepareFixChain(
	key, category string,
	reflection *Reflection,
	originalCall *genai.FunctionCall,
	repeatCount int,
) {
	if r.policy == nil || originalCall == nil {
		r.setPhase(key, category, recoveryPhaseAlternative)
		return
	}

	chain := make([]*AutoFixAction, 0, 4)
	if reflection != nil && reflection.AutoFix != nil {
		chain = append(chain, reflection.AutoFix)
	}
	chain = append(chain, r.policy.DeterministicFixChain(category, reflection, originalCall)...)
	if repeatCount >= 1 {
		chain = stripRetryWithArgsFixes(chain)
	}
	chain = dedupeFixChain(chain)
	if len(chain) == 0 {
		r.setPhase(key, category, recoveryPhaseAlternative)
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.ensureStateLocked(key, category)
	state.FixChain = chain
	state.FixCursor = 0
	state.Phase = recoveryPhaseFixChain
}

func dedupeFixChain(chain []*AutoFixAction) []*AutoFixAction {
	if len(chain) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(chain))
	out := make([]*AutoFixAction, 0, len(chain))
	for _, fix := range chain {
		if fix == nil {
			continue
		}
		key := recoveryFixKey(fix)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, fix)
	}
	return out
}

func (r *RecoveryExecutor) nextFixAction(key, category string, repeatCount int) (*AutoFixAction, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.ensureStateLocked(key, category)
	if state.Phase != recoveryPhaseFixChain || len(state.FixChain) == 0 {
		return nil, false
	}

	for state.FixCursor < len(state.FixChain) {
		fix := state.FixChain[state.FixCursor]
		state.FixCursor++
		if fix == nil {
			continue
		}
		if repeatCount >= 1 && strings.EqualFold(strings.TrimSpace(fix.FixType), "retry_with_args") {
			continue
		}
		fixKey := recoveryFixKey(fix)
		if fixKey == "" || state.AttemptedFixes[fixKey] {
			continue
		}
		if state.RemainingBudget <= 0 {
			state.Phase = recoveryPhaseAlternative
			return nil, false
		}
		state.AttemptedFixes[fixKey] = true
		state.RemainingBudget--
		return cloneAutoFix(fix), true
	}

	state.Phase = recoveryPhaseAlternative
	return nil, false
}

func cloneAutoFix(fix *AutoFixAction) *AutoFixAction {
	if fix == nil {
		return nil
	}
	cloned := &AutoFixAction{
		FixType:     fix.FixType,
		ToolName:    fix.ToolName,
		ArgModifier: fix.ArgModifier,
	}
	if len(fix.ToolArgs) > 0 {
		cloned.ToolArgs = make(map[string]any, len(fix.ToolArgs))
		for k, v := range fix.ToolArgs {
			cloned.ToolArgs[k] = v
		}
	}
	if len(fix.ModifiedArgs) > 0 {
		cloned.ModifiedArgs = make(map[string]any, len(fix.ModifiedArgs))
		for k, v := range fix.ModifiedArgs {
			cloned.ModifiedArgs[k] = v
		}
	}
	return cloned
}

func recoveryFixKey(fix *AutoFixAction) string {
	if fix == nil {
		return ""
	}
	key := fix.FixType + "|" + fix.ToolName
	if len(fix.ModifiedArgs) > 0 {
		if encoded, err := json.Marshal(fix.ModifiedArgs); err == nil {
			key += "|" + string(encoded)
		}
	}
	if len(fix.ToolArgs) > 0 {
		if encoded, err := json.Marshal(fix.ToolArgs); err == nil {
			key += "|" + string(encoded)
		}
	}
	return key
}

func stripRetryWithArgsFixes(chain []*AutoFixAction) []*AutoFixAction {
	if len(chain) == 0 {
		return nil
	}
	out := make([]*AutoFixAction, 0, len(chain))
	for _, fix := range chain {
		if fix == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(fix.FixType), "retry_with_args") {
			continue
		}
		out = append(out, fix)
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// retryWithArgs retries the original tool with modified static args.
func (r *RecoveryExecutor) retryWithArgs(
	ctx context.Context,
	agent *Agent,
	originalCall *genai.FunctionCall,
	fix *AutoFixAction,
) (tools.ToolResult, bool) {
	if fix.ModifiedArgs == nil {
		return tools.ToolResult{}, false
	}

	retryCall := &genai.FunctionCall{
		ID:   originalCall.ID,
		Name: originalCall.Name,
		Args: fix.ModifiedArgs,
	}

	result := agent.executeTool(ctx, retryCall)
	if result.Success {
		return result, true
	}
	return tools.ToolResult{}, false
}

// runToolFirst runs a prerequisite tool, then retries the original if it succeeds.
// For "read" fixes, we don't retry — we return the file content as enriched context.
func (r *RecoveryExecutor) runToolFirst(
	ctx context.Context,
	agent *Agent,
	originalCall *genai.FunctionCall,
	fix *AutoFixAction,
) (tools.ToolResult, bool) {
	// Build args for the fix tool
	var fixArgs map[string]any
	switch fix.ToolName {
	case "read":
		fixArgs = buildReadArgs(originalCall.Args)
	case "glob":
		fixArgs = buildGlobArgs(originalCall.Args)
	case "bash":
		fixArgs = buildWhichArgs(originalCall.Args)
	default:
		fixArgs = fix.ToolArgs
	}

	if fixArgs == nil {
		return tools.ToolResult{}, false
	}

	// Execute the fix tool
	fixCall := &genai.FunctionCall{
		Name: fix.ToolName,
		Args: fixArgs,
	}

	fixResult := agent.executeTool(ctx, fixCall)
	if !fixResult.Success {
		logging.Debug("auto-fix prerequisite tool failed", "tool", fix.ToolName, "error", fixResult.Error)
		return tools.ToolResult{}, false
	}

	// For read/bash prerequisite tools, return enriched context rather than blindly retrying.
	// The model needs to see the file content to formulate a better edit.
	// We mark Success=false so the model knows the original call failed,
	// but provide the prerequisite output as context for a smarter retry.
	enrichedContent := fmt.Sprintf(
		"**Auto-recovery:** The original `%s` call failed. Ran `%s` to gather context for retry.\n\n---\n%s",
		originalCall.Name, fix.ToolName, fixResult.Content,
	)
	return tools.ToolResult{
		Content: enrichedContent,
		Success: false,
		Data: map[string]any{
			"auto_fix":      true,
			"fix_type":      "enriched_context",
			"original_tool": originalCall.Name,
			"prerequisite":  fix.ToolName,
		},
	}, true
}

// modifyAndRetry runs a fix tool (e.g., glob), then uses ArgModifier to build new args and retries.
func (r *RecoveryExecutor) modifyAndRetry(
	ctx context.Context,
	agent *Agent,
	originalCall *genai.FunctionCall,
	fix *AutoFixAction,
) (tools.ToolResult, bool) {
	if fix.ArgModifier == nil {
		return tools.ToolResult{}, false
	}

	// Build args for the fix tool (e.g., glob)
	var fixArgs map[string]any
	switch fix.ToolName {
	case "glob":
		fixArgs = buildGlobArgs(originalCall.Args)
	default:
		fixArgs = fix.ToolArgs
	}

	if fixArgs == nil {
		return tools.ToolResult{}, false
	}

	// Execute the fix tool
	fixCall := &genai.FunctionCall{
		Name: fix.ToolName,
		Args: fixArgs,
	}

	fixResult := agent.executeTool(ctx, fixCall)
	if !fixResult.Success || fixResult.Content == "" {
		logging.Debug("auto-fix tool returned no results", "tool", fix.ToolName)
		return tools.ToolResult{}, false
	}

	// Use ArgModifier to build modified args
	modifiedArgs := fix.ArgModifier(originalCall.Args, fixResult.Content)
	if modifiedArgs == nil {
		return tools.ToolResult{}, false
	}

	// Retry the original tool with modified args
	retryCall := &genai.FunctionCall{
		ID:   originalCall.ID,
		Name: originalCall.Name,
		Args: modifiedArgs,
	}

	result := agent.executeTool(ctx, retryCall)
	if result.Success {
		logging.Info("auto-fix modify_and_retry succeeded",
			"tool", originalCall.Name,
			"fix_tool", fix.ToolName)
		return result, true
	}

	return tools.ToolResult{}, false
}
