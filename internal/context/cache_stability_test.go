package context

import (
	"regexp"
	"strings"
	"testing"
)

// hhmm matches a wall-clock "HH:MM" timestamp like the removed "_Updated: 14:32_".
var hhmm = regexp.MustCompile(`\b\d{1,2}:\d{2}\b`)

// TestWorkingMemoryRenderIsCacheStable pins the v0.86.4 fix: the render must
// be byte-stable across calls (no wall-clock timestamp). Post-#7 working
// memory left the MAIN cached prefix (it travels as per-turn context), but
// the render is still injected into SUB-AGENT system prompts, where stability
// keeps a long-running agent's own prefix cacheable.
func TestWorkingMemoryRenderIsCacheStable(t *testing.T) {
	turn := WorkingMemoryTurn{
		Response:     "Updated the executor guard for repeated reads.",
		TouchedPaths: []string{"internal/tools/executor.go"},
	}

	a := renderWorkingMemory(turn)
	b := renderWorkingMemory(turn)
	if a != b {
		t.Errorf("renderWorkingMemory not byte-stable across calls:\nA: %q\nB: %q", a, b)
	}
	if a == "" {
		t.Fatal("expected non-empty working memory render for a turn with content")
	}
	if hhmm.MatchString(a) {
		t.Errorf("working memory render contains a wall-clock timestamp (cache-buster): %q", a)
	}
}

// fakeWM satisfies WorkingMemoryProvider for prompt-structure tests.
type fakeWM struct{ content string }

func (f *fakeWM) GetContent() string { return f.content }

// TestBuildExcludesWorkingMemory pins roadmap #7: the MAIN system prompt (the
// cached prefix) must NOT contain working memory — it mutates every turn and
// re-billed the whole system+tools prefix on caching providers. WM is
// delivered via client.SetTurnContext instead. The sub-agent builders still
// inject it (point-in-time snapshot, no cache cost there).
func TestBuildExcludesWorkingMemory(t *testing.T) {
	pb := NewPromptBuilder("/tmp/fake", &ProjectInfo{Type: ProjectTypeGo, Name: "fake"})
	pb.SetWorkingMemory(&fakeWM{content: "## Working Memory\nWM-SENTINEL-CONTENT"})
	prompt := pb.Build()
	if strings.Contains(prompt, "WM-SENTINEL-CONTENT") {
		t.Fatal("working memory leaked into the cached system prefix — roadmap #7 regression")
	}
}

// TestBuildIsStableAcrossWorkingMemoryChanges — the whole point of #7: two
// Builds around a working-memory change must be byte-identical.
func TestBuildIsStableAcrossWorkingMemoryChanges(t *testing.T) {
	wm := &fakeWM{content: "state A"}
	pb := NewPromptBuilder("/tmp/fake", &ProjectInfo{Type: ProjectTypeGo, Name: "fake"})
	pb.SetWorkingMemory(wm)
	before := pb.Build()
	wm.content = "state B — totally different"
	pb.Invalidate() // even a forced rebuild must not differ
	after := pb.Build()
	if before != after {
		t.Fatal("system prompt changed when only working memory changed — cached prefix busted")
	}
}

// TestBuildExcludesSessionMemory — session memory mutates every ~5K tokens and
// busted the cached prefix on each update; it travels as per-turn context now
// (same contract as working memory, roadmap #7).
func TestBuildExcludesSessionMemory(t *testing.T) {
	dir := t.TempDir()
	sm := NewSessionMemoryManager(dir, SessionMemoryConfig{})
	pb := NewPromptBuilder("/tmp/fake", &ProjectInfo{Type: ProjectTypeGo, Name: "fake"})
	pb.SetSessionMemory(sm)
	// Even with a manager wired, the MAIN prompt must not embed its content.
	prompt := pb.Build()
	if strings.Contains(prompt, "Session Memory") {
		t.Fatalf("session memory leaked into the cached system prefix:\n%s", prompt)
	}
}

// TestQuestionTrimGatedOnPrefixCaching pins the trim/caching trade-off: with
// prefix caching enabled the prompt must be byte-identical across
// question↔action message flips (each flip used to re-bill the whole cached
// prefix); without caching the trim still applies and saves tokens.
func TestQuestionTrimGatedOnPrefixCaching(t *testing.T) {
	mk := func(caching bool) *PromptBuilder {
		pb := NewPromptBuilder("/tmp/fake", &ProjectInfo{Type: ProjectTypeGo, Name: "fake"})
		pb.SetPlanAutoDetect(true)
		pb.SetDetectedContext("DETECTED-CTX-SENTINEL")
		pb.SetPrefixCachingEnabled(caching)
		return pb
	}

	// Caching provider: stable across flips, heavy sections always present.
	pb := mk(true)
	pb.SetLastMessage("what does the executor do?") // question
	q := pb.Build()
	if !strings.Contains(q, "DETECTED-CTX-SENTINEL") {
		t.Fatal("caching provider: heavy sections must be present even for questions")
	}
	pb.SetLastMessage("fix the executor bug") // action — flip
	if a := pb.Build(); a != q {
		t.Fatal("caching provider: prompt changed across question↔action flip — cached prefix busted")
	}

	// Non-caching provider: trim still applies (questions get the light prompt).
	pb = mk(false)
	pb.SetLastMessage("what does the executor do?")
	q = pb.Build()
	if strings.Contains(q, "DETECTED-CTX-SENTINEL") {
		t.Fatal("non-caching provider: question prompt should be trimmed")
	}
	pb.SetLastMessage("fix the executor bug")
	a := pb.Build()
	if !strings.Contains(a, "DETECTED-CTX-SENTINEL") {
		t.Fatal("non-caching provider: action prompt should carry the full sections")
	}
	if a == q {
		t.Fatal("non-caching provider: trim must differentiate question vs action prompts")
	}
}

// TestProviderSupportsPrefixCaching pins the prefix-caching family list. NOTE:
// this is intentionally BROADER than client/anthropic.go supportsPromptCaching —
// glm ignores explicit cache_control (false there) but caches the prefix
// IMPLICITLY (verified live: cache_read_input_tokens on a stable prefix), so it
// is true here to keep the question-trim from busting that cache.
func TestProviderSupportsPrefixCaching(t *testing.T) {
	for provider, want := range map[string]bool{
		"kimi": true, "deepseek": true, "minimax": true, "anthropic": true,
		"glm": true, "ollama": false, "": false,
	} {
		if got := ProviderSupportsPrefixCaching(provider); got != want {
			t.Errorf("ProviderSupportsPrefixCaching(%q) = %v, want %v", provider, got, want)
		}
	}
}
