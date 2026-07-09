package context

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// --- truncateUTF8Safe ---

func TestTruncateUTF8Safe_ShortString(t *testing.T) {
	got := truncateUTF8Safe("hello", 100)
	if got != "hello" {
		t.Errorf("short string = %q, want 'hello'", got)
	}
}

func TestTruncateUTF8Safe_ExactLen(t *testing.T) {
	got := truncateUTF8Safe("hello", 5)
	if got != "hello" {
		t.Errorf("exact len = %q, want 'hello'", got)
	}
}

func TestTruncateUTF8Safe_LongerString(t *testing.T) {
	got := truncateUTF8Safe("hello world", 5)
	if got != "hello" {
		t.Errorf("truncated = %q, want 'hello'", got)
	}
}

func TestTruncateUTF8Safe_Multibyte(t *testing.T) {
	// "привет" = 12 bytes (6 runes × 2 bytes). Truncate at 5 bytes → must
	// backtrack to 4 bytes (2 full runes) to avoid splitting a UTF-8 char.
	got := truncateUTF8Safe("привет", 5)
	if !isValidUTF8(got) {
		t.Errorf("truncated multibyte string is not valid UTF-8: %q", got)
	}
}

func TestTruncateUTF8Safe_MultibyteBoundary(t *testing.T) {
	// "é" is 2 bytes (0xC3 0xA9). Truncate at 1 byte → must backtrack to 0.
	got := truncateUTF8Safe("é", 1)
	if got != "" {
		t.Errorf("truncated at multibyte boundary = %q, want ''", got)
	}
}

func TestTruncateUTF8Safe_ZeroMaxBytes(t *testing.T) {
	got := truncateUTF8Safe("hello", 0)
	if got != "" {
		t.Errorf("zero maxBytes = %q, want ''", got)
	}
}

func TestTruncateUTF8Safe_ASCII(t *testing.T) {
	got := truncateUTF8Safe("abcdef", 3)
	if got != "abc" {
		t.Errorf("ASCII truncate = %q, want 'abc'", got)
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}

// --- extractContractBlock ---

func TestExtractContractBlock_NoBlock(t *testing.T) {
	got := extractContractBlock("no contract here")
	if got != "" {
		t.Errorf("no contract = %q, want ''", got)
	}
}

func TestExtractContractBlock_StartOnly(t *testing.T) {
	got := extractContractBlock("=== ACTIVE CONTRACT ===\nsome content")
	if got != "" {
		t.Errorf("start-only = %q, want ''", got)
	}
}

func TestExtractContractBlock_EndOnly(t *testing.T) {
	got := extractContractBlock("some content\n=== END CONTRACT ===")
	if got != "" {
		t.Errorf("end-only = %q, want ''", got)
	}
}

func TestExtractContractBlock_Complete(t *testing.T) {
	prompt := `Before contract
=== ACTIVE CONTRACT ===
contract body here
=== END CONTRACT ===
Operate strictly within this contract's boundaries.
After contract`
	got := extractContractBlock(prompt)
	if !strings.Contains(got, "ACTIVE CONTRACT") {
		t.Errorf("expected ACTIVE CONTRACT in: %q", got)
	}
	if !strings.Contains(got, "END CONTRACT") {
		t.Errorf("expected END CONTRACT in: %q", got)
	}
	if !strings.Contains(got, "Operate strictly") {
		t.Errorf("expected 'Operate strictly' in: %q", got)
	}
}

func TestExtractContractBlock_NoTailSentence(t *testing.T) {
	prompt := `=== ACTIVE CONTRACT ===
contract body
=== END CONTRACT ===`
	got := extractContractBlock(prompt)
	if !strings.Contains(got, "ACTIVE CONTRACT") {
		t.Errorf("expected ACTIVE CONTRACT in: %q", got)
	}
}

// --- removeHeadingSection ---

func TestRemoveHeadingSection_NotFound(t *testing.T) {
	prompt := "some text without heading"
	got := removeHeadingSection(prompt, "## Missing")
	if got != prompt {
		t.Errorf("missing heading should return prompt as-is")
	}
}

func TestRemoveHeadingSection_LastSection(t *testing.T) {
	prompt := "## Heading\ncontent here"
	got := removeHeadingSection(prompt, "## Heading")
	if strings.Contains(got, "content here") {
		t.Errorf("last section content should be removed: %q", got)
	}
}

func TestRemoveHeadingSection_MiddleSection(t *testing.T) {
	prompt := `# First
content1
## Heading
content2
# Last
content3`
	got := removeHeadingSection(prompt, "## Heading")
	if strings.Contains(got, "content2") {
		t.Errorf("section content should be removed: %q", got)
	}
	if !strings.Contains(got, "content1") {
		t.Errorf("first section should remain: %q", got)
	}
	if !strings.Contains(got, "content3") {
		t.Errorf("last section should remain: %q", got)
	}
}

// --- truncateHeadingSection ---

func TestTruncateHeadingSection_NotFound(t *testing.T) {
	prompt := "some text"
	got := truncateHeadingSection(prompt, "## Missing", 100)
	if got != prompt {
		t.Errorf("missing heading should return prompt as-is")
	}
}

func TestTruncateHeadingSection_ZeroMaxChars(t *testing.T) {
	prompt := "## Heading\ncontent"
	got := truncateHeadingSection(prompt, "## Heading", 0)
	// maxSectionChars <= 0 → delegates to removeHeadingSection
	if strings.Contains(got, "content") {
		t.Errorf("zero maxChars should remove section: %q", got)
	}
}

func TestTruncateHeadingSection_SectionUnderLimit(t *testing.T) {
	prompt := "## Heading\nshort"
	got := truncateHeadingSection(prompt, "## Heading", 1000)
	if got != prompt {
		t.Errorf("section under limit should return prompt as-is")
	}
}

func TestTruncateHeadingSection_SectionOverLimit(t *testing.T) {
	longContent := strings.Repeat("x", 2000)
	prompt := "## Heading\n" + longContent + "\n## Next"
	got := truncateHeadingSection(prompt, "## Heading", 100)
	if !strings.Contains(got, "[section compacted]") {
		t.Errorf("expected [section compacted]: %q", got)
	}
}

func TestTruncateHeadingSection_LastSectionOverLimit(t *testing.T) {
	longContent := strings.Repeat("x", 2000)
	prompt := "## Heading\n" + longContent
	got := truncateHeadingSection(prompt, "## Heading", 100)
	if !strings.Contains(got, "[section compacted]") {
		t.Errorf("expected [section compacted]: %q", got)
	}
}

// --- truncatePlanningProtocolSection ---

func TestTruncatePlanningProtocolSection_NotFound(t *testing.T) {
	prompt := "no planning protocol here"
	got := truncatePlanningProtocolSection(prompt, 1000)
	if got != prompt {
		t.Errorf("missing protocol should return prompt as-is")
	}
}

func TestTruncatePlanningProtocolSection_UnderLimit(t *testing.T) {
	prompt := "AUTOMATIC PLANNING PROTOCOL\nshort content\n## Common Task Patterns\nmore"
	got := truncatePlanningProtocolSection(prompt, 1000)
	if got != prompt {
		t.Errorf("section under limit should return prompt as-is")
	}
}

func TestTruncatePlanningProtocolSection_OverLimit(t *testing.T) {
	longContent := strings.Repeat("x", 3000)
	prompt := "intro\nAUTOMATIC PLANNING PROTOCOL\n" + longContent + "\n## Common Task Patterns\nafter"
	got := truncatePlanningProtocolSection(prompt, 100)
	if !strings.Contains(got, "[planning protocol compacted]") {
		t.Errorf("expected [planning protocol compacted]: %q", got)
	}
}

func TestTruncatePlanningProtocolSection_OverLimitNoEndMarker(t *testing.T) {
	longContent := strings.Repeat("x", 3000)
	prompt := "AUTOMATIC PLANNING PROTOCOL\n" + longContent
	got := truncatePlanningProtocolSection(prompt, 100)
	if !strings.Contains(got, "[planning protocol compacted]") {
		t.Errorf("expected [planning protocol compacted]: %q", got)
	}
}

// --- findNextHeading ---

func TestFindNextHeading_OutOfRange(t *testing.T) {
	if got := findNextHeading("hello", -1); got != -1 {
		t.Errorf("negative from = %d, want -1", got)
	}
	if got := findNextHeading("hello", 100); got != -1 {
		t.Errorf("from >= len = %d, want -1", got)
	}
}

func TestFindNextHeading_NoHeading(t *testing.T) {
	if got := findNextHeading("just text no heading", 0); got != -1 {
		t.Errorf("no heading = %d, want -1", got)
	}
}

func TestFindNextHeading_H1(t *testing.T) {
	prompt := "text\n# Heading"
	got := findNextHeading(prompt, 0)
	if got != 4 {
		t.Errorf("H1 heading at = %d, want 4", got)
	}
}

func TestFindNextHeading_H2(t *testing.T) {
	prompt := "text\n## Heading"
	got := findNextHeading(prompt, 0)
	if got != 4 {
		t.Errorf("H2 heading at = %d, want 4", got)
	}
}

func TestFindNextHeading_Contract(t *testing.T) {
	prompt := "text\n=== ACTIVE CONTRACT ==="
	got := findNextHeading(prompt, 0)
	if got != 4 {
		t.Errorf("contract heading at = %d, want 4", got)
	}
}

func TestFindNextHeading_EarliestWins(t *testing.T) {
	prompt := "text\n## Second\n# First"
	got := findNextHeading(prompt, 0)
	if got != 4 {
		t.Errorf("earliest heading at = %d, want 4", got)
	}
}

func TestFindNextHeading_FromOffset(t *testing.T) {
	prompt := "text\n## First\nmore text\n## Second"
	// Start searching from after first heading
	got := findNextHeading(prompt, 10)
	if got < 0 {
		t.Errorf("expected to find heading from offset, got %d", got)
	}
}

// --- applyPromptBudget ---

func TestApplyPromptBudget_NoBudget(t *testing.T) {
	prompt := "some prompt text"
	got := applyPromptBudget(prompt, 0)
	if got != "some prompt text" {
		t.Errorf("zero budget = %q, want 'some prompt text'", got)
	}
}

func TestApplyPromptBudget_UnderLimit(t *testing.T) {
	prompt := "short prompt"
	got := applyPromptBudget(prompt, 1000)
	if got != "short prompt" {
		t.Errorf("under limit = %q, want 'short prompt'", got)
	}
}

func TestApplyPromptBudget_OverLimit(t *testing.T) {
	long := strings.Repeat("a", 500)
	got := applyPromptBudget(long, 100)
	if len(got) > 200 {
		t.Errorf("over limit should be truncated, got len=%d", len(got))
	}
	if !strings.Contains(got, "[Prompt compacted") {
		t.Errorf("expected compaction marker: %q", got)
	}
}

func TestApplyPromptBudget_TinyBudget(t *testing.T) {
	got := applyPromptBudget("hello world this is long", 10)
	if len(got) > 50 {
		t.Errorf("tiny budget should produce short output, got len=%d", len(got))
	}
}

func TestApplyPromptBudget_PreservesContract(t *testing.T) {
	contract := "=== ACTIVE CONTRACT ===\nbody\n=== END CONTRACT ===\nOperate strictly within this contract's boundaries."
	// The contract is ~100 chars. Budget must be large enough for prefixBudget
	// (= budget - len(contract) - 2) to exceed 200 so the contract path triggers.
	long := strings.Repeat("a", 500)
	prompt := long + "\n" + contract
	got := applyPromptBudget(prompt, 500)
	if !strings.Contains(got, "ACTIVE CONTRACT") {
		t.Errorf("contract block should be preserved: %q", got)
	}
}

// --- PromptBuilder setters (dirty flag + caching) ---

func TestPromptBuilder_BuildCaches(t *testing.T) {
	b := NewPromptBuilder("/tmp", &ProjectInfo{Type: ProjectTypeGo})
	p1 := b.Build()
	p2 := b.Build()
	if p1 != p2 {
		t.Error("Build() should return cached result on second call")
	}
}

func TestPromptBuilder_Invalidate(t *testing.T) {
	b := NewPromptBuilder("/tmp", &ProjectInfo{Type: ProjectTypeGo})
	p1 := b.Build()
	b.Invalidate()
	p2 := b.Build()
	// Content should be the same but cache was busted
	if p1 != p2 {
		t.Error("Build() after Invalidate should produce same content")
	}
}

func TestPromptBuilder_SetProjectMemory(t *testing.T) {
	b := NewPromptBuilder("/tmp", &ProjectInfo{Type: ProjectTypeGo})
	b.Build()
	b.SetProjectMemory(&ProjectMemory{instructions: "test"})
	if !b.promptDirty {
		t.Error("SetProjectMemory should mark dirty")
	}
}

func TestPromptBuilder_SetMemoryStore(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.Build()
	b.SetMemoryStore(nil) // should not panic
	if !b.promptDirty {
		t.Error("SetMemoryStore should mark dirty")
	}
}

func TestPromptBuilder_SetPlanManager(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.Build()
	b.SetPlanManager(nil)
	if !b.promptDirty {
		t.Error("SetPlanManager should mark dirty")
	}
}

func TestPromptBuilder_SetPinnedContent(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.Build()
	b.SetPinnedContent("important focus")
	if !b.promptDirty {
		t.Error("SetPinnedContent should mark dirty")
	}
}

func TestPromptBuilder_SetPinnedContent_Idempotent(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.SetPinnedContent("focus")
	b.Build()
	b.SetPinnedContent("focus") // same value
	if b.promptDirty {
		t.Error("SetPinnedContent with same value should NOT mark dirty")
	}
}

func TestPromptBuilder_SetPlanMode(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.Build()
	b.SetPlanMode(true)
	if !b.promptDirty {
		t.Error("SetPlanMode should mark dirty")
	}
	if !b.planMode {
		t.Error("planMode should be true")
	}
}

func TestPromptBuilder_SetPlanMode_Idempotent(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.SetPlanMode(true)
	b.Build()
	b.SetPlanMode(true) // same value
	if b.promptDirty {
		t.Error("SetPlanMode with same value should NOT mark dirty")
	}
}

func TestPromptBuilder_SetProvider(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.Build()
	b.SetProvider("  GLM  ")
	if !b.promptDirty {
		t.Error("SetProvider should mark dirty")
	}
	if b.provider != "glm" {
		t.Errorf("provider = %q, want 'glm'", b.provider)
	}
}

func TestPromptBuilder_SetProvider_Idempotent(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.SetProvider("kimi")
	b.Build()
	b.SetProvider("Kimi") // same after lowercasing
	if b.promptDirty {
		t.Error("SetProvider with same value should NOT mark dirty")
	}
}

func TestPromptBuilder_SetPrefixCachingEnabled(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.Build()
	b.SetPrefixCachingEnabled(true)
	if !b.promptDirty {
		t.Error("SetPrefixCachingEnabled should mark dirty")
	}
	if !b.prefixCachingEnabled {
		t.Error("prefixCachingEnabled should be true")
	}
}

func TestPromptBuilder_SetPrefixCachingEnabled_Idempotent(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.SetPrefixCachingEnabled(true)
	b.Build()
	b.SetPrefixCachingEnabled(true)
	if b.promptDirty {
		t.Error("SetPrefixCachingEnabled with same value should NOT mark dirty")
	}
}

func TestPromptBuilder_SetDetectedContext(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.Build()
	b.SetDetectedContext("Go 1.25 project")
	if !b.promptDirty {
		t.Error("SetDetectedContext should mark dirty")
	}
}

func TestPromptBuilder_SetDetectedContext_Idempotent(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.SetDetectedContext("context")
	b.Build()
	b.SetDetectedContext("context")
	if b.promptDirty {
		t.Error("SetDetectedContext with same value should NOT mark dirty")
	}
}

func TestPromptBuilder_SetToolHints(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.Build()
	b.SetToolHints("hints")
	if !b.promptDirty {
		t.Error("SetToolHints should mark dirty")
	}
}

func TestPromptBuilder_SetToolHints_Idempotent(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.SetToolHints("hints")
	b.Build()
	b.SetToolHints("hints")
	if b.promptDirty {
		t.Error("SetToolHints with same value should NOT mark dirty")
	}
}

func TestPromptBuilder_SetPlanAutoDetect(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.Build()
	b.SetPlanAutoDetect(true)
	if !b.promptDirty {
		t.Error("SetPlanAutoDetect should mark dirty")
	}
}

func TestPromptBuilder_SetPlanAutoDetect_Idempotent(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.SetPlanAutoDetect(true)
	b.Build()
	b.SetPlanAutoDetect(true)
	if b.promptDirty {
		t.Error("SetPlanAutoDetect with same value should NOT mark dirty")
	}
}

func TestPromptBuilder_SetLastMessage_QuestionFlipsDirty(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.Build()
	// Empty → not question. Setting a question flips classification → dirty.
	b.SetLastMessage("what is this?")
	if !b.promptDirty {
		t.Error("SetLastMessage flipping from non-question to question should mark dirty")
	}
}

func TestPromptBuilder_SetLastMessage_SameClassNoDirty(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.SetLastMessage("what is this?")
	b.Build()
	// Both are questions → classification doesn't flip → NOT dirty
	b.SetLastMessage("how does this work?")
	if b.promptDirty {
		t.Error("SetLastMessage with same question classification should NOT mark dirty")
	}
}

func TestPromptBuilder_SetLastMessage_PrefixCachingNoDirty(t *testing.T) {
	b := NewPromptBuilder("/tmp", nil)
	b.SetPrefixCachingEnabled(true)
	b.Build()
	b.SetLastMessage("what is this?")
	if b.promptDirty {
		t.Error("SetLastMessage with prefix caching should NOT mark dirty")
	}
}

// --- isQuestionOnly ---

func TestIsQuestionOnly_Empty(t *testing.T) {
	b := &PromptBuilder{lastMessage: ""}
	if b.isQuestionOnly() {
		t.Error("empty message should not be question-only")
	}
}

func TestIsQuestionOnly_Question(t *testing.T) {
	b := &PromptBuilder{lastMessage: "what is the architecture?"}
	if !b.isQuestionOnly() {
		t.Error("question should be question-only")
	}
}

func TestIsQuestionOnly_QuestionMark(t *testing.T) {
	b := &PromptBuilder{lastMessage: "really?"}
	if !b.isQuestionOnly() {
		t.Error("text ending with ? should be question-only")
	}
}

func TestIsQuestionOnly_Action(t *testing.T) {
	b := &PromptBuilder{lastMessage: "add a new function"}
	if b.isQuestionOnly() {
		t.Error("action message should not be question-only")
	}
}

func TestIsQuestionOnly_RussianQuestion(t *testing.T) {
	b := &PromptBuilder{lastMessage: "как работает этот код?"}
	if !b.isQuestionOnly() {
		t.Error("Russian question should be question-only")
	}
}

func TestIsQuestionOnly_RussianAction(t *testing.T) {
	b := &PromptBuilder{lastMessage: "создай новый файл"}
	if b.isQuestionOnly() {
		t.Error("Russian action should not be question-only")
	}
}

func TestIsQuestionOnly_Neither(t *testing.T) {
	b := &PromptBuilder{lastMessage: "some random text"}
	if b.isQuestionOnly() {
		t.Error("ambiguous text should not be question-only (safe default)")
	}
}

// --- ProviderSupportsPrefixCaching ---
// (already tested in cache_stability_test.go — TestProviderSupportsPrefixCaching)

// TestTruncateHeadingSection_MultibyteNoInvalidUTF8 pins the v0.100.73 #12 fix:
// the section was byte-sliced with a raw section[:maxSectionChars], which splits
// a multi-byte rune and injects invalid UTF-8 into the system prompt for
// Cyrillic/CJK instruction files.
func TestTruncateHeadingSection_MultibyteNoInvalidUTF8(t *testing.T) {
	// 2-byte Cyrillic runes: a byte cut at an odd offset lands mid-rune.
	body := strings.Repeat("тест ", 600) // ~3000 runes, ~5400 bytes
	prompt := "## Project Instructions\n" + body + "\n## Next\ntail"
	got := truncateHeadingSection(prompt, "## Project Instructions", 2201)
	if !utf8.ValidString(got) {
		t.Fatal("truncated section contains invalid UTF-8 (byte-sliced mid-rune)")
	}
}

// TestTruncatePlanningProtocolSection_MultibyteNoInvalidUTF8: same for the
// planning-protocol section truncator.
func TestTruncatePlanningProtocolSection_MultibyteNoInvalidUTF8(t *testing.T) {
	body := strings.Repeat("шаг ", 800)
	prompt := "## Planning Protocol\n" + body + "\n## Next\ntail"
	got := truncatePlanningProtocolSection(prompt, 2201)
	if !utf8.ValidString(got) {
		t.Fatal("truncated planning-protocol section contains invalid UTF-8")
	}
}

// mutablePlanManager returns a contract that the test can change to simulate a
// step transition.
type mutablePlanManager struct{ contract string }

func (m *mutablePlanManager) GetActiveContractContext() string { return m.contract }

// TestBuild_SelfHealsOnContractChange pins the v0.100.73 #10 fix: plan-step
// transitions change the active contract but do NOT dirty the PromptBuilder, so
// Build() served a stale "ACTIVE CONTRACT" for every step after the first. The
// cache now re-checks the contract and rebuilds when it changes — no explicit
// Invalidate() call needed.
func TestBuild_SelfHealsOnContractChange(t *testing.T) {
	b := NewPromptBuilder(t.TempDir(), nil)
	pm := &mutablePlanManager{contract: "Current Step Contract:\n- Step 1: Do the first thing\n"}
	b.SetPlanManager(pm)

	first := b.Build()
	if !strings.Contains(first, "Step 1: Do the first thing") {
		t.Fatalf("build 1 missing step-1 contract:\n%s", first)
	}

	// Step transition: the contract changes, but nothing dirties the builder.
	pm.contract = "Current Step Contract:\n- Step 2: Do the second thing\n"

	second := b.Build()
	if strings.Contains(second, "Step 1: Do the first thing") {
		t.Fatalf("build 2 served the STALE step-1 contract:\n%s", second)
	}
	if !strings.Contains(second, "Step 2: Do the second thing") {
		t.Fatalf("build 2 missing the new step-2 contract:\n%s", second)
	}

	// Plan completion: contract reverts to empty — the block must disappear.
	pm.contract = ""
	third := b.Build()
	if strings.Contains(third, "ACTIVE CONTRACT") {
		t.Fatalf("build 3 still carries the ACTIVE CONTRACT block after plan completion:\n%s", third)
	}
}
