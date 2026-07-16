package agent

import (
	"strings"
	"testing"

	"gokin/internal/tools"

	"google.golang.org/genai"
)

// --- recoveryAttemptKey (agent.go:3798) ---

func TestRecoveryAttemptKey_CategoryOnly(t *testing.T) {
	got := recoveryAttemptKey("read", map[string]any{"path": "/x"}, "loop", "")
	if !strings.Contains(got, "read") || !strings.Contains(got, "|loop") {
		t.Fatalf("got %q, want read|loop segment", got)
	}
	if strings.Contains(got, "alt=") {
		t.Fatalf("got %q, should not contain alt= when alternative empty", got)
	}
}

func TestRecoveryAttemptKey_WithAlternative(t *testing.T) {
	got := recoveryAttemptKey("grep", map[string]any{"pattern": "foo"}, "stagnation", "use-glob")
	if !strings.Contains(got, "alt=use-glob") {
		t.Fatalf("got %q, want alt=use-glob", got)
	}
}

func TestRecoveryAttemptKey_EmptyCategoryDefaultsUnknown(t *testing.T) {
	got := recoveryAttemptKey("bash", map[string]any{"command": "ls"}, "", "")
	if !strings.Contains(got, "|unknown") {
		t.Fatalf("got %q, want |unknown default", got)
	}
}

func TestRecoveryAttemptKey_TrimsWhitespace(t *testing.T) {
	got := recoveryAttemptKey("edit", map[string]any{}, "  LOOP  ", "  Grep  ")
	if !strings.Contains(got, "|loop") {
		t.Fatalf("got %q, category not lowercased/trimmed", got)
	}
	if !strings.Contains(got, "alt=grep") {
		t.Fatalf("got %q, alternative not lowercased/trimmed", got)
	}
}

func TestRecoveryAttemptKey_EmptyArgs(t *testing.T) {
	got := recoveryAttemptKey("read", map[string]any{}, "loop", "")
	// normalizeCallKey on empty args yields "read:{}"
	if !strings.Contains(got, "read:{}") {
		t.Fatalf("got %q, want read:{} base", got)
	}
}

// --- normalizeCallKey (agent.go:3670) ---

func TestNormalizeCallKey_EmptyArgs(t *testing.T) {
	got := normalizeCallKey("read", map[string]any{})
	if got != "read:{}" {
		t.Fatalf("got %q, want read:{}", got)
	}
}

func TestNormalizeCallKey_NilArgs(t *testing.T) {
	got := normalizeCallKey("read", nil)
	if got != "read:{}" {
		t.Fatalf("got %q, want read:{}", got)
	}
}

func TestNormalizeCallKey_FiltersZeroValuesExplicit(t *testing.T) {
	got := normalizeCallKey("grep", map[string]any{
		"pattern": "foo",
		"path":    "",    // empty string filtered
		"count":   0.0,   // zero filtered
		"active":  false, // false filtered
		"missing": nil,   // nil filtered
	})
	if !strings.Contains(got, "foo") {
		t.Fatalf("got %q, want to keep pattern=foo", got)
	}
	if strings.Contains(got, "path") || strings.Contains(got, "count") || strings.Contains(got, "active") || strings.Contains(got, "missing") {
		t.Fatalf("got %q, zero-value keys should be filtered", got)
	}
}

func TestNormalizeCallKey_SortsKeys(t *testing.T) {
	got := normalizeCallKey("x", map[string]any{
		"zeta":  "z",
		"alpha": "a",
		"mid":   "m",
	})
	alphaIdx := strings.Index(got, "alpha")
	midIdx := strings.Index(got, "mid")
	zetaIdx := strings.Index(got, "zeta")
	if !(alphaIdx < midIdx && midIdx < zetaIdx) {
		t.Fatalf("got %q, keys not sorted alpha<mid<zeta", got)
	}
}

func TestNormalizeCallKey_DeterministicAcrossMaps(t *testing.T) {
	a := normalizeCallKey("read", map[string]any{"path": "/a", "line": 10.0})
	b := normalizeCallKey("read", map[string]any{"line": 10.0, "path": "/a"})
	if a != b {
		t.Fatalf("normalizeCallKey not deterministic: %q != %q", a, b)
	}
}

// --- normalizeToolPlanFingerprint (agent.go:3703) ---

func TestNormalizeToolPlanFingerprint_Empty(t *testing.T) {
	if got := normalizeToolPlanFingerprint(nil); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if got := normalizeToolPlanFingerprint([]*genai.FunctionCall{}); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestNormalizeToolPlanFingerprint_SkipsNilCalls(t *testing.T) {
	calls := []*genai.FunctionCall{
		nil,
		{Name: "read", Args: map[string]any{"path": "/x"}},
		nil,
	}
	got := normalizeToolPlanFingerprint(calls)
	if !strings.Contains(got, "read") {
		t.Fatalf("got %q, want read segment", got)
	}
}

func TestNormalizeToolPlanFingerprint_JoinsMultiple(t *testing.T) {
	calls := []*genai.FunctionCall{
		{Name: "read", Args: map[string]any{"path": "/a"}},
		{Name: "grep", Args: map[string]any{"pattern": "foo"}},
	}
	got := normalizeToolPlanFingerprint(calls)
	if strings.Count(got, "|") != 1 {
		t.Fatalf("got %q, want exactly one separator for 2 calls", got)
	}
	if !strings.Contains(got, "read") || !strings.Contains(got, "grep") {
		t.Fatalf("got %q, want both read and grep", got)
	}
}

// --- normalizeProgressFingerprint (agent.go:3717) ---

func TestNormalizeProgressFingerprint_Empty(t *testing.T) {
	if got := normalizeProgressFingerprint(""); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if got := normalizeProgressFingerprint("   "); got != "" {
		t.Fatalf("got %q, want empty for whitespace", got)
	}
}

func TestNormalizeProgressFingerprint_TrimsAndLowercases(t *testing.T) {
	got := normalizeProgressFingerprint("  Hello WORLD  ")
	if got != "hello world" {
		t.Fatalf("got %q, want 'hello world'", got)
	}
}

func TestNormalizeProgressFingerprint_CollapsesWhitespace(t *testing.T) {
	got := normalizeProgressFingerprint("a\t\n b  \t c")
	if got != "a b c" {
		t.Fatalf("got %q, want 'a b c'", got)
	}
}

func TestNormalizeProgressFingerprint_TruncatesLongText(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := normalizeProgressFingerprint(long)
	if len([]rune(got)) > 240 {
		t.Fatalf("got len %d, want <= 240", len([]rune(got)))
	}
}

// --- mapModelName (agent.go:1284) ---

func TestMapModelName_FlashShorthand(t *testing.T) {
	if got := mapModelName("flash"); got != "glm-5-turbo" {
		t.Fatalf("got %q, want glm-5-turbo", got)
	}
}

func TestMapModelName_FastShorthand(t *testing.T) {
	if got := mapModelName("fast"); got != "glm-5-turbo" {
		t.Fatalf("got %q, want glm-5-turbo", got)
	}
}

func TestMapModelName_ProShorthand(t *testing.T) {
	if got := mapModelName("pro"); got != "glm-5.2" {
		t.Fatalf("got %q, want glm-5.2", got)
	}
}

func TestMapModelName_StrongShorthand(t *testing.T) {
	if got := mapModelName("strong"); got != "glm-5.2" {
		t.Fatalf("got %q, want glm-5.2", got)
	}
}

func TestMapModelName_Passthrough(t *testing.T) {
	if got := mapModelName("kimi-k2"); got != "kimi-k2" {
		t.Fatalf("got %q, want passthrough", got)
	}
}

func TestMapModelName_CaseInsensitive(t *testing.T) {
	if got := mapModelName("FLASH"); got != "glm-5-turbo" {
		t.Fatalf("got %q, want glm-5-turbo (case-insensitive)", got)
	}
}

func TestMapModelName_EmptyString(t *testing.T) {
	if got := mapModelName(""); got != "" {
		t.Fatalf("got %q, want empty passthrough", got)
	}
}

// --- generateAgentID (agent.go:1296) ---

func TestGenerateAgentID_Unique(t *testing.T) {
	ids := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		id := generateAgentID()
		if id == "" {
			t.Fatal("generated empty id")
		}
		if len(id) != 16 {
			t.Fatalf("id %q len %d, want 16 hex chars", id, len(id))
		}
		if ids[id] {
			t.Fatalf("duplicate id generated: %s", id)
		}
		ids[id] = true
	}
}

// --- Agent simple methods ---

func newTestAgent(t *testing.T) *Agent {
	t.Helper()
	return NewAgent(AgentTypeGeneral, nil, tools.NewRegistry(), t.TempDir(), 3, "", nil, nil)
}

func TestAgent_EnableAutoCheckpoint_SetsFlagAndInterval(t *testing.T) {
	a := newTestAgent(t)
	a.EnableAutoCheckpoint(7)
	if !a.autoCheckpoint {
		t.Fatal("autoCheckpoint not set")
	}
	if a.checkpointInterval != 7 {
		t.Fatalf("interval = %d, want 7", a.checkpointInterval)
	}
}

func TestAgent_EnableAutoCheckpoint_DefaultsTo5WhenZeroOrNegative(t *testing.T) {
	a := newTestAgent(t)
	a.EnableAutoCheckpoint(0)
	if a.checkpointInterval != 5 {
		t.Fatalf("interval = %d, want 5 default", a.checkpointInterval)
	}
	a.EnableAutoCheckpoint(-3)
	if a.checkpointInterval != 5 {
		t.Fatalf("interval = %d, want 5 default", a.checkpointInterval)
	}
}

func TestAgent_EnableAutoCheckpoint_PreservesExplicitInterval(t *testing.T) {
	a := newTestAgent(t)
	a.EnableAutoCheckpoint(3)
	a.EnableAutoCheckpoint(0) // interval<=0 should NOT overwrite existing 3
	if a.checkpointInterval != 3 {
		t.Fatalf("interval = %d, want preserved 3", a.checkpointInterval)
	}
}

func TestAgent_DisableAutoCheckpoint(t *testing.T) {
	a := newTestAgent(t)
	a.EnableAutoCheckpoint(5)
	a.DisableAutoCheckpoint()
	if a.autoCheckpoint {
		t.Fatal("autoCheckpoint should be false after disable")
	}
}

func TestAgent_Close_NoLearningReturnsNil(t *testing.T) {
	a := newTestAgent(t)
	if err := a.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil (no learning)", err)
	}
}

func TestAgent_SetOnInput(t *testing.T) {
	a := newTestAgent(t)
	called := false
	a.SetOnInput(func(s string) (string, error) {
		called = true
		return "resp", nil
	})
	a.stateMu.RLock()
	fn := a.onInput
	a.stateMu.RUnlock()
	if fn == nil {
		t.Fatal("onInput not set")
	}
	if _, err := fn("test"); err != nil || !called {
		t.Fatal("onInput callback not invokable")
	}
}

func TestAgent_AddToolUsed_AppendsAndDedupsByGetToolsUsed(t *testing.T) {
	a := newTestAgent(t)
	a.AddToolUsed("read")
	a.AddToolUsed("grep")
	a.AddToolUsed("read")
	used := a.GetToolsUsed()
	if len(used) != 3 {
		t.Fatalf("len = %d, want 3 (append does not dedup)", len(used))
	}
}

func TestAgent_GetToolsUsed_ReturnsCopy(t *testing.T) {
	a := newTestAgent(t)
	a.AddToolUsed("read")
	first := a.GetToolsUsed()
	first[0] = "mutated"
	second := a.GetToolsUsed()
	if second[0] != "read" {
		t.Fatal("GetToolsUsed should return a defensive copy")
	}
}

func TestAgent_ToolsUsedCount(t *testing.T) {
	a := newTestAgent(t)
	if a.ToolsUsedCount() != 0 {
		t.Fatal("initial count should be 0")
	}
	a.AddToolUsed("read")
	a.AddToolUsed("grep")
	if a.ToolsUsedCount() != 2 {
		t.Fatalf("count = %d, want 2", a.ToolsUsedCount())
	}
}

func TestAgent_SetPlanGoal(t *testing.T) {
	a := newTestAgent(t)
	goal := &PlanGoal{Description: "do X"}
	a.SetPlanGoal(goal)
	a.stateMu.RLock()
	g := a.planGoal
	a.stateMu.RUnlock()
	if g == nil || g.Description != "do X" {
		t.Fatalf("planGoal not set: %+v", g)
	}
}

func TestAgent_SetRequireApproval(t *testing.T) {
	a := newTestAgent(t)
	a.SetRequireApproval(true)
	a.stateMu.RLock()
	v := a.requireApproval
	a.stateMu.RUnlock()
	if !v {
		t.Fatal("requireApproval not set")
	}
}

func TestAgent_EnablePlanningMode(t *testing.T) {
	a := newTestAgent(t)
	goal := &PlanGoal{Description: "plan X"}
	a.EnablePlanningMode(goal)
	if !a.IsPlanningMode() {
		t.Fatal("planningMode should be true")
	}
}

func TestAgent_DisablePlanningMode_ClearsPlan(t *testing.T) {
	a := newTestAgent(t)
	goal := &PlanGoal{Description: "plan X"}
	a.EnablePlanningMode(goal)
	a.DisablePlanningMode()
	if a.IsPlanningMode() {
		t.Fatal("planningMode should be false after disable")
	}
	a.stateMu.RLock()
	g := a.planGoal
	a.stateMu.RUnlock()
	if g != nil {
		t.Fatal("planGoal should be nil after disable")
	}
}

func TestAgent_GetActivePlan_InitiallyNil(t *testing.T) {
	a := newTestAgent(t)
	if a.GetActivePlan() != nil {
		t.Fatal("active plan should be nil initially")
	}
}

func TestAgent_IsPlanningMode_DefaultFalse(t *testing.T) {
	a := newTestAgent(t)
	if a.IsPlanningMode() {
		t.Fatal("planningMode should be false by default")
	}
}

func TestAgent_GetTimeout_DefaultSet(t *testing.T) {
	a := newTestAgent(t)
	if a.GetTimeout() == 0 {
		t.Fatal("timeout should be non-zero by default")
	}
}

// --- QueueSteer / drainSteers ---

func TestAgent_QueueSteer_EmptyIgnored(t *testing.T) {
	a := newTestAgent(t)
	a.QueueSteer("")
	a.QueueSteer("   ")
	a.stateMu.RLock()
	steers := len(a.pendingSteers)
	a.stateMu.RUnlock()
	if steers != 0 {
		t.Fatalf("empty steers should be ignored, got %d", steers)
	}
}

func TestAgent_QueueSteer_DeduplicatesIdentical(t *testing.T) {
	a := newTestAgent(t)
	a.QueueSteer("change approach")
	a.QueueSteer("change approach")
	a.stateMu.RLock()
	steers := len(a.pendingSteers)
	a.stateMu.RUnlock()
	if steers != 1 {
		t.Fatalf("dedup failed, got %d", steers)
	}
}

func TestAgent_QueueSteer_AppendsDistinct(t *testing.T) {
	a := newTestAgent(t)
	a.QueueSteer("change approach")
	a.QueueSteer("verify first")
	a.stateMu.RLock()
	steers := len(a.pendingSteers)
	a.stateMu.RUnlock()
	if steers != 2 {
		t.Fatalf("distinct steers should append, got %d", steers)
	}
}

func TestAgent_DrainSteers_AppendsToHistory(t *testing.T) {
	a := newTestAgent(t)
	a.QueueSteer("nudge one")
	a.QueueSteer("nudge two")
	a.drainSteers()
	a.stateMu.RLock()
	histLen := len(a.history)
	a.stateMu.RUnlock()
	if histLen != 2 {
		t.Fatalf("history len = %d, want 2 steers injected", histLen)
	}
	// second drain should be no-op
	a.drainSteers()
	a.stateMu.RLock()
	histLen2 := len(a.history)
	a.stateMu.RUnlock()
	if histLen2 != 2 {
		t.Fatalf("history len = %d, steers already drained", histLen2)
	}
}

// --- buildWeakModelGuidance (agent.go:1898) ---

func TestAgent_BuildWeakModelGuidance_NonGLM(t *testing.T) {
	a := newTestAgent(t)
	a.Model = "kimi-k2"
	got := a.buildWeakModelGuidance()
	if !strings.Contains(got, "IMPORTANT RULES") {
		t.Fatalf("missing base rules section: %q", got)
	}
	if strings.Contains(got, "GLM-specific") {
		t.Fatalf("non-GLM model should not get GLM section: %q", got)
	}
}

func TestAgent_BuildWeakModelGuidance_GLM(t *testing.T) {
	a := newTestAgent(t)
	a.Model = "glm-5.2"
	got := a.buildWeakModelGuidance()
	if !strings.Contains(got, "GLM-specific") {
		t.Fatalf("GLM model should get GLM section: %q", got)
	}
	if !strings.Contains(got, "tool_use blocks need unique ids") {
		t.Fatalf("missing GLM-specific guidance: %q", got)
	}
}

// --- buildLoopRecoveryIntervention (agent.go:3848) ---

func TestAgent_BuildLoopRecoveryIntervention_ReadTool(t *testing.T) {
	a := newTestAgent(t)
	got := a.buildLoopRecoveryIntervention("read", map[string]any{"path": "/etc/hosts"}, 4)
	if !strings.Contains(got, "read") {
		t.Fatalf("missing tool name: %q", got)
	}
	if !strings.Contains(got, "4 times") {
		t.Fatalf("missing count: %q", got)
	}
	if !strings.Contains(got, "/etc/hosts") {
		t.Fatalf("missing path arg: %q", got)
	}
	if !strings.Contains(got, "glob") {
		t.Fatalf("read tool should suggest glob: %q", got)
	}
}

func TestAgent_BuildLoopRecoveryIntervention_GrepTool(t *testing.T) {
	a := newTestAgent(t)
	got := a.buildLoopRecoveryIntervention("grep", map[string]any{"pattern": "foo"}, 5)
	if !strings.Contains(got, "Simplify my search pattern") {
		t.Fatalf("grep should suggest simplifying pattern: %q", got)
	}
}

func TestAgent_BuildLoopRecoveryIntervention_BashTool(t *testing.T) {
	a := newTestAgent(t)
	got := a.buildLoopRecoveryIntervention("bash", map[string]any{"command": "ls"}, 3)
	if !strings.Contains(got, "which") {
		t.Fatalf("bash should suggest which: %q", got)
	}
}

func TestAgent_BuildLoopRecoveryIntervention_EditTool(t *testing.T) {
	a := newTestAgent(t)
	got := a.buildLoopRecoveryIntervention("edit", map[string]any{"file_path": "/x"}, 3)
	if !strings.Contains(got, "old_string actually exists") {
		t.Fatalf("edit should suggest checking old_string: %q", got)
	}
}

func TestAgent_BuildLoopRecoveryIntervention_WriteTool(t *testing.T) {
	a := newTestAgent(t)
	got := a.buildLoopRecoveryIntervention("write", map[string]any{"file_path": "/x"}, 3)
	if !strings.Contains(got, "directory permissions") {
		t.Fatalf("write should suggest checking permissions: %q", got)
	}
}

func TestAgent_BuildLoopRecoveryIntervention_GlobTool(t *testing.T) {
	a := newTestAgent(t)
	got := a.buildLoopRecoveryIntervention("glob", map[string]any{"pattern": "**/*.go"}, 3)
	if !strings.Contains(got, "tree") {
		t.Fatalf("glob should suggest tree: %q", got)
	}
}

func TestAgent_BuildLoopRecoveryIntervention_UnknownTool(t *testing.T) {
	a := newTestAgent(t)
	got := a.buildLoopRecoveryIntervention("custom_tool", map[string]any{}, 3)
	if !strings.Contains(got, "reconsider my overall approach") {
		t.Fatalf("unknown tool should get default guidance: %q", got)
	}
}

func TestAgent_BuildLoopRecoveryIntervention_NilArgs(t *testing.T) {
	a := newTestAgent(t)
	got := a.buildLoopRecoveryIntervention("read", nil, 3)
	if !strings.Contains(got, "DIFFERENT approach") {
		t.Fatalf("missing closing guidance: %q", got)
	}
}

// --- buildStagnationRecoveryIntervention (agent.go:3911) ---

func TestAgent_BuildStagnationRecoveryIntervention_WithReason(t *testing.T) {
	a := newTestAgent(t)
	got := a.buildStagnationRecoveryIntervention(1, "stuck on read")
	if !strings.Contains(got, "EXECUTION WATCHDOG") {
		t.Fatalf("missing header: %q", got)
	}
	if !strings.Contains(got, "1/2") {
		t.Fatalf("missing attempt count: %q", got)
	}
	if !strings.Contains(got, "stuck on read") {
		t.Fatalf("missing reason: %q", got)
	}
}

func TestAgent_BuildStagnationRecoveryIntervention_EmptyReason(t *testing.T) {
	a := newTestAgent(t)
	got := a.buildStagnationRecoveryIntervention(2, "   ")
	if strings.Contains(got, "Observed issue:") {
		t.Fatalf("empty reason should skip Observed issue: %q", got)
	}
	if !strings.Contains(got, "2/2") {
		t.Fatalf("missing attempt count: %q", got)
	}
}

// --- updateTokenGrowthAndProjectOverflow (agent.go:3940) ---

func TestAgent_UpdateTokenGrowth_MaxZeroReturnsFalse(t *testing.T) {
	a := newTestAgent(t)
	if a.updateTokenGrowthAndProjectOverflow(100, 0, 0.8) {
		t.Fatal("maxTokens<=0 should return false")
	}
}

func TestAgent_UpdateTokenGrowth_FirstSampleReturnsFalse(t *testing.T) {
	a := newTestAgent(t)
	// first call sets lastTokenCount, no delta yet
	if a.updateTokenGrowthAndProjectOverflow(100, 10000, 0.8) {
		t.Fatal("first sample should return false (no prior baseline)")
	}
}

func TestAgent_UpdateTokenGrowth_FlatGrowthReturnsFalse(t *testing.T) {
	a := newTestAgent(t)
	a.updateTokenGrowthAndProjectOverflow(100, 10000, 0.8)
	// same count → delta 0 → returns false
	if a.updateTokenGrowthAndProjectOverflow(100, 10000, 0.8) {
		t.Fatal("flat growth (delta<=0) should return false")
	}
}

func TestAgent_UpdateTokenGrowth_ProjectsOverflow(t *testing.T) {
	a := newTestAgent(t)
	// EMA needs ≥2 positive-delta samples before it projects. Feed three
	// rising samples so the 3rd call clears the sample floor and projects.
	// seed baseline (delta=0, returns false)
	a.updateTokenGrowthAndProjectOverflow(100, 1000, 0.5)
	// sample 1: +100, EMA=100, sample<2 returns false
	a.updateTokenGrowthAndProjectOverflow(200, 1000, 0.5)
	// sample 2: +800, EMA=0.3*800+0.7*100=310, projected=1000+310*3=1930 > 500 (50% of 1000)
	if !a.updateTokenGrowthAndProjectOverflow(1000, 1000, 0.5) {
		t.Fatal("should project overflow with sustained growth")
	}
}

func TestAgent_UpdateTokenGrowth_NoOverflowUnderThreshold(t *testing.T) {
	a := newTestAgent(t)
	maxTokens := 100000
	a.updateTokenGrowthAndProjectOverflow(100, maxTokens, 0.8)
	// tiny growth, far from threshold
	if a.updateTokenGrowthAndProjectOverflow(105, maxTokens, 0.8) {
		t.Fatal("should not project overflow well under threshold")
	}
}

// --- needsVerificationNudge (agent.go:1078) ---

func TestAgent_NeedsVerificationNudge_NoMutations(t *testing.T) {
	a := newTestAgent(t)
	a.AddToolUsed("read")
	a.AddToolUsed("grep")
	if a.needsVerificationNudge() {
		t.Fatal("read-only tools should not trigger nudge")
	}
}

func TestAgent_NeedsVerificationNudge_MutatedVerified(t *testing.T) {
	a := newTestAgent(t)
	a.AddToolUsed("edit")
	a.AddToolUsed("bash") // verification tool
	if a.needsVerificationNudge() {
		t.Fatal("mutated+verified should not trigger nudge")
	}
}

func TestAgent_NeedsVerificationNudge_MutatedNotVerified(t *testing.T) {
	a := newTestAgent(t)
	a.AddToolUsed("edit")
	a.AddToolUsed("write")
	if !a.needsVerificationNudge() {
		t.Fatal("mutated without verification should trigger nudge")
	}
}
