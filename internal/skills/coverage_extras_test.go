package skills

import (
	"strings"
	"testing"
)

// --- trimLastRune (0% → 100%) ---

func TestTrimLastRune_ASCII(t *testing.T) {
	if got := trimLastRune("hello"); got != "hell" {
		t.Fatalf("trimLastRune(hello) = %q, want %q", got, "hell")
	}
}

func TestTrimLastRune_Multibyte(t *testing.T) {
	if got := trimLastRune("世界"); got != "世" {
		t.Fatalf("trimLastRune(世界) = %q, want %q", got, "世")
	}
}

func TestTrimLastRune_Empty(t *testing.T) {
	if got := trimLastRune(""); got != "" {
		t.Fatalf("trimLastRune('') = %q, want %q", got, "")
	}
}

func TestTrimLastRune_SingleRune(t *testing.T) {
	if got := trimLastRune("x"); got != "" {
		t.Fatalf("trimLastRune(x) = %q, want %q", got, "")
	}
}

// --- carryInvocationComesBefore (40% → 100%) ---

func TestCarryInvocationComesBefore_SequencePriority(t *testing.T) {
	a := carryInvocation{name: "alpha", sequence: 2, order: 0}
	b := carryInvocation{name: "beta", sequence: 1, order: 0}
	if !carryInvocationComesBefore(a, b) {
		t.Fatal("higher sequence should come first")
	}
}

func TestCarryInvocationComesBefore_SameSequence_OrderTiebreak(t *testing.T) {
	a := carryInvocation{name: "alpha", sequence: 1, order: 0}
	b := carryInvocation{name: "beta", sequence: 1, order: 1}
	if !carryInvocationComesBefore(a, b) {
		t.Fatal("lower order should come first when sequence is equal")
	}
}

func TestCarryInvocationComesBefore_SameSequenceSameOrder_NameTiebreak(t *testing.T) {
	a := carryInvocation{name: "alpha", sequence: 1, order: 0}
	b := carryInvocation{name: "beta", sequence: 1, order: 0}
	if !carryInvocationComesBefore(a, b) {
		t.Fatal("lower name should come first when sequence and order are equal")
	}
}

// --- isLowerSHA256 (66.7% → 100%) ---

func TestIsLowerSHA256_ValidLowerHex(t *testing.T) {
	if !isLowerSHA256("a" + strings.Repeat("b", 63)) {
		t.Fatal("64-char lowercase hex should be valid SHA256")
	}
}

func TestIsLowerSHA256_UpperHex(t *testing.T) {
	if isLowerSHA256("A" + strings.Repeat("b", 63)) {
		t.Fatal("uppercase hex should not be valid lower SHA256")
	}
}

func TestIsLowerSHA256_WrongLength(t *testing.T) {
	if isLowerSHA256("abcdef") {
		t.Fatal("short string should not be valid SHA256")
	}
}

func TestIsLowerSHA256_NonHex(t *testing.T) {
	if isLowerSHA256("g" + strings.Repeat("b", 63)) {
		t.Fatal("non-hex chars should not be valid SHA256")
	}
}

// --- estimatedCarryTokens + carryRuneTokenUnits (83.3% → 100%) ---

func TestEstimatedCarryTokens_Empty(t *testing.T) {
	if got := estimatedCarryTokens(""); got != 0 {
		t.Fatalf("estimatedCarryTokens('') = %d, want 0", got)
	}
}

func TestEstimatedCarryTokens_CJK(t *testing.T) {
	// CJK runes cost carryTokenUnitsPerToken (6) each
	// 6 runes × 6 units = 36 units → ceil(36/6) = 6 tokens
	got := estimatedCarryTokens("世界世界世界")
	if got != 6 {
		t.Fatalf("estimatedCarryTokens(6 CJK runes) = %d, want 6", got)
	}
}

func TestEstimatedCarryTokens_Punctuation(t *testing.T) {
	// Punctuation costs more than letters
	asciiTokens := estimatedCarryTokens("hello")
	punctTokens := estimatedCarryTokens("!!!!!")
	if punctTokens <= asciiTokens {
		t.Fatalf("punctuation should cost more: punct=%d ascii=%d", punctTokens, asciiTokens)
	}
}

func TestCarryRuneTokenUnits_Space(t *testing.T) {
	if got := carryRuneTokenUnits(' '); got != 1 {
		t.Fatalf("space should cost 1 unit, got %d", got)
	}
}

// --- invocationIsNewer (28.6% → 100%) ---

func TestInvocationIsNewer_SequenceDiff(t *testing.T) {
	newer := Invocation{Sequence: 2, RenderHash: "a", Source: "x", Path: "/x"}
	older := Invocation{Sequence: 1, RenderHash: "a", Source: "x", Path: "/x"}
	if !invocationIsNewer(newer, older) {
		t.Fatal("higher sequence should be newer")
	}
}

func TestInvocationIsNewer_SameSequence_HashDiff(t *testing.T) {
	newer := Invocation{Sequence: 1, RenderHash: "b", Source: "x", Path: "/x"}
	older := Invocation{Sequence: 1, RenderHash: "a", Source: "x", Path: "/x"}
	if !invocationIsNewer(newer, older) {
		t.Fatal("higher hash should be newer when sequence is equal")
	}
}

func TestInvocationIsNewer_SameSequenceSameHash_SourceDiff(t *testing.T) {
	newer := Invocation{Sequence: 1, RenderHash: "a", Source: "z", Path: "/x"}
	older := Invocation{Sequence: 1, RenderHash: "a", Source: "a", Path: "/x"}
	if !invocationIsNewer(newer, older) {
		t.Fatal("higher source should be newer")
	}
}

func TestInvocationIsNewer_AllEqual_PathDiff(t *testing.T) {
	newer := Invocation{Sequence: 1, RenderHash: "a", Source: "x", Path: "/z"}
	older := Invocation{Sequence: 1, RenderHash: "a", Source: "x", Path: "/a"}
	if !invocationIsNewer(newer, older) {
		t.Fatal("higher path should be newer")
	}
}

func TestInvocationIsNewer_NotNewer(t *testing.T) {
	newer := Invocation{Sequence: 1, RenderHash: "a", Source: "x", Path: "/x"}
	older := Invocation{Sequence: 2, RenderHash: "a", Source: "x", Path: "/x"}
	if invocationIsNewer(newer, older) {
		t.Fatal("lower sequence should not be newer")
	}
}

// --- isMarkdownThematicBreak (50% → 100%) ---

func TestIsMarkdownThematicBreak_Dashes(t *testing.T) {
	if !isMarkdownThematicBreak("---") {
		t.Fatal("'---' should be a thematic break")
	}
}

func TestIsMarkdownThematicBreak_Asterisks(t *testing.T) {
	if !isMarkdownThematicBreak("***") {
		t.Fatal("'***' should be a thematic break")
	}
}

func TestIsMarkdownThematicBreak_Underscores(t *testing.T) {
	if !isMarkdownThematicBreak("___") {
		t.Fatal("'___' should be a thematic break")
	}
}

func TestIsMarkdownThematicBreak_WithSpaces(t *testing.T) {
	if !isMarkdownThematicBreak("- - -") {
		t.Fatal("'- - -' should be a thematic break")
	}
}

func TestIsMarkdownThematicBreak_TooShort(t *testing.T) {
	if isMarkdownThematicBreak("--") {
		t.Fatal("'--' should not be a thematic break")
	}
}

func TestIsMarkdownThematicBreak_NotABreak(t *testing.T) {
	if isMarkdownThematicBreak("Some text") {
		t.Fatal("'Some text' should not be a thematic break")
	}
}

// --- firstMarkdownParagraph (75% → 100%) ---

func TestFirstMarkdownParagraph_EmptyBody(t *testing.T) {
	if got := firstMarkdownParagraph(""); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestFirstMarkdownParagraph_WithThematicBreak(t *testing.T) {
	// Thematic break at the start should be skipped, paragraph after it returned
	body := "---\n\nThis is the real description."
	got := firstMarkdownParagraph(body)
	if !strings.Contains(got, "real description") {
		t.Fatalf("expected paragraph after thematic break, got %q", got)
	}
}

func TestFirstMarkdownParagraph_SimpleParagraph(t *testing.T) {
	body := "This is a description.\n\nSecond paragraph."
	got := firstMarkdownParagraph(body)
	if !strings.Contains(got, "This is a description") {
		t.Fatalf("expected first paragraph, got %q", got)
	}
}

// --- DefaultRoots (75% → 100%) ---

func TestDefaultRoots(t *testing.T) {
	roots := DefaultRoots("/tmp/test-workdir")
	// Should return at least project + global roots
	if len(roots) == 0 {
		t.Fatal("DefaultRoots should return at least one root")
	}
	for _, root := range roots {
		if root.Path == "" {
			t.Fatal("root path should not be empty")
		}
	}
}
