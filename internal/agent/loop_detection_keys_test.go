package agent

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

// normalizeCallKey is the foundation of exact-loop AND plan-fingerprint
// detection: two calls are "the same" iff their keys match. The key MUST be
// stable regardless of Go's randomized map iteration order — sort.Strings is
// what guarantees that. Calling it many times on a multi-key map exercises many
// iteration orders; if the sort were ever dropped, some calls would diverge and
// loop detection would silently go flaky. This is the load-bearing test.
func TestNormalizeCallKey_StableAcrossMapOrder(t *testing.T) {
	args := map[string]any{
		"file_path": "internal/agent/agent.go",
		"offset":    float64(58),
		"limit":     float64(20),
		"pattern":   "func",
		"recursive": true,
	}
	want := normalizeCallKey("grep", args)
	for i := range 200 {
		if got := normalizeCallKey("grep", args); got != want {
			t.Fatalf("call %d produced a different key — map-order instability: %q != %q", i, got, want)
		}
	}

	// Two maps built with different insertion order must yield the same key.
	a := map[string]any{"b": "2", "a": "1", "c": "3"}
	b := map[string]any{"c": "3", "a": "1", "b": "2"}
	if normalizeCallKey("x", a) != normalizeCallKey("x", b) {
		t.Errorf("insertion order changed the key: %q vs %q", normalizeCallKey("x", a), normalizeCallKey("x", b))
	}
}

// Zero-valued args are filtered so an explicit default (offset:0, limit:0,
// empty string, false, nil) is equivalent to omitting it — otherwise the model
// passing offset:0 vs omitting offset would look like two distinct calls.
func TestNormalizeCallKey_FiltersZeroValues(t *testing.T) {
	bare := normalizeCallKey("read", map[string]any{"file_path": "x"})

	cases := []map[string]any{
		{"file_path": "x", "offset": float64(0)},
		{"file_path": "x", "limit": float64(0)},
		{"file_path": "x", "note": ""},
		{"file_path": "x", "recursive": false},
		{"file_path": "x", "extra": nil},
	}
	for _, c := range cases {
		if got := normalizeCallKey("read", c); got != bare {
			t.Errorf("zero-value not filtered: %v → %q, want %q", c, got, bare)
		}
	}

	// Empty / nil args collapse to the canonical "{}" form.
	if k := normalizeCallKey("read", nil); k != "read:{}" {
		t.Errorf("nil args = %q, want read:{}", k)
	}
	if k := normalizeCallKey("read", map[string]any{}); k != "read:{}" {
		t.Errorf("empty args = %q, want read:{}", k)
	}
}

// Non-zero perturbed args ARE distinct keys. This pins the documented limitation
// (see broadLoopThreshold's caveat): a drifting read offset produces a distinct
// key each call, so the exact-args loop does NOT catch an arg-perturbing loop.
// Locking it here makes the behavior intentional, not accidental.
func TestNormalizeCallKey_DistinctNonZeroArgsAreDistinctKeys(t *testing.T) {
	k58 := normalizeCallKey("read", map[string]any{"file_path": "x", "offset": float64(58)})
	k59 := normalizeCallKey("read", map[string]any{"file_path": "x", "offset": float64(59)})
	if k58 == k59 {
		t.Errorf("drifting offset collapsed to one key (%q) — would falsely look like an exact loop", k58)
	}
	// Different tool names never collide.
	if normalizeCallKey("read", nil) == normalizeCallKey("grep", nil) {
		t.Error("different tool names produced the same key")
	}
}

func TestNormalizeToolPlanFingerprint(t *testing.T) {
	calls := []*genai.FunctionCall{
		{Name: "read", Args: map[string]any{"file_path": "a"}},
		nil, // nil calls are skipped, not panicked on
		{Name: "grep", Args: map[string]any{"pattern": "foo"}},
	}
	fp := normalizeToolPlanFingerprint(calls)
	if !strings.Contains(fp, "read:") || !strings.Contains(fp, "grep:") {
		t.Errorf("fingerprint missing a call: %q", fp)
	}
	if strings.Count(fp, "|") != 1 {
		t.Errorf("expected 2 joined parts (one separator), got %q", fp)
	}
	// Order-sensitive: same calls reversed is a different plan.
	rev := normalizeToolPlanFingerprint([]*genai.FunctionCall{calls[2], calls[0]})
	if rev == fp {
		t.Error("plan fingerprint should be order-sensitive")
	}
	if normalizeToolPlanFingerprint(nil) != "" {
		t.Error("empty plan should fingerprint to empty string")
	}
}

func TestNormalizeProgressFingerprint(t *testing.T) {
	// Lowercased, trimmed, internal whitespace collapsed.
	if got := normalizeProgressFingerprint("  Build   FAILED\n\there  "); got != "build failed here" {
		t.Errorf("normalize = %q", got)
	}
	if normalizeProgressFingerprint("   ") != "" {
		t.Error("all-whitespace should normalize to empty")
	}
	// Capped at 240 runes, rune-safe on multibyte input (no panic, valid UTF-8).
	long := strings.Repeat("я", 500) // Cyrillic — 2 bytes each
	got := normalizeProgressFingerprint(long)
	if n := len([]rune(got)); n != 240 {
		t.Errorf("rune cap = %d, want 240", n)
	}
	if !strings.HasPrefix(long, got) {
		t.Error("cap must be a clean rune-boundary prefix")
	}
}

func TestSummarizeToolProgress(t *testing.T) {
	fr := func(name string, m map[string]any) *genai.FunctionResponse {
		return &genai.FunctionResponse{Name: name, Response: m}
	}
	results := []toolCallResult{
		{Response: fr("read", map[string]any{"success": true})},
		{Response: nil}, // skipped, no panic
		{Response: fr("read", map[string]any{"success": true})},
		{Response: fr("bash", map[string]any{"success": false, "error": "compile failed"})},
		{Response: fr("bash", map[string]any{"success": false, "error": "compile failed"})}, // repeated
		{Response: fr("grep", map[string]any{"success": false, "error": "no such file"})},   // new
	}
	seen := map[string]struct{}{}
	sc, nf, rf := summarizeToolProgress(nil, results, seen)
	if sc != 2 {
		t.Errorf("successCount = %d, want 2", sc)
	}
	if nf != 2 {
		t.Errorf("newFailureSignals = %d, want 2 (bash, grep)", nf)
	}
	if rf != 1 {
		t.Errorf("repeatedFailureSignals = %d, want 1 (second bash)", rf)
	}

	// seenFailureFingerprints persists across calls: re-running the same results
	// counts every failing result as repeated (all 3 fingerprints are now seen),
	// none new.
	sc2, nf2, rf2 := summarizeToolProgress(nil, results, seen)
	if sc2 != 2 || nf2 != 0 || rf2 != 3 {
		t.Errorf("second pass = (%d,%d,%d), want (2,0,3)", sc2, nf2, rf2)
	}

	// Empty results short-circuits to zeroes.
	if a, b, c := summarizeToolProgress(nil, nil, seen); a != 0 || b != 0 || c != 0 {
		t.Errorf("empty results = (%d,%d,%d), want zeroes", a, b, c)
	}
}
