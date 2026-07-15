package ui

import (
	"strings"
	"testing"
)

// The field-report shape: a provider-limit error rendered as a bare line,
// tail-amputated at 100 runes — exactly where the actionable half lives
// («…switch p...│»). Errors must WRAP, keep their tail, drop the machine
// wrapper prefix, and match the provider-limit guidance.

const glmLimitRaw = "model response error (other): GLM weekly/monthly limit exhausted — wait for the reset or switch provider with /provider (resets Monday 00:00 UTC)"

func TestDisplayErrorLines_StripsMachinePrefixAndKeepsTail(t *testing.T) {
	lines := displayErrorLines(glmLimitRaw, 60)
	joined := strings.Join(lines, " ")

	if strings.Contains(joined, "model response error") || strings.Contains(joined, "(other)") {
		t.Fatalf("machine wrapper prefix must be stripped for display: %q", joined)
	}
	if !strings.HasPrefix(lines[0], "GLM weekly/monthly limit exhausted") {
		t.Fatalf("cause must lead the display text: %q", lines[0])
	}
	if !strings.Contains(joined, "switch provider with /provider") {
		t.Fatalf("the actionable TAIL must survive display preparation: %q", joined)
	}
}

func TestDisplayErrorLines_NestedWrappersAndPlainText(t *testing.T) {
	lines := displayErrorLines("model response error: request failed: connection refused", 60)
	if lines[0] != "connection refused" {
		t.Fatalf("nested wrappers should strip iteratively: %q", lines[0])
	}
	// Plain text without a wrapper is untouched.
	lines = displayErrorLines("something broke", 60)
	if lines[0] != "something broke" {
		t.Fatalf("plain error must pass through verbatim: %q", lines[0])
	}
	// Empty stays renderable.
	if lines := displayErrorLines("", 60); lines[0] == "" {
		t.Fatal("empty error must render a placeholder")
	}
}

func TestDisplayErrorLines_CapKeepsHeadAndTail(t *testing.T) {
	long := strings.Repeat("word ", 200) + "final-action /provider"
	lines := displayErrorLines(long, 40)
	if len(lines) > 5 {
		t.Fatalf("display must cap at 5 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[len(lines)-1], "final-action /provider") {
		t.Fatalf("the TAIL must survive the line cap (middle elision): %v", lines)
	}
	if !strings.HasPrefix(lines[0], "word") {
		t.Fatalf("the head must survive too: %v", lines)
	}
}

// End-to-end: the exact field-report error renders with its full actionable
// tail AND the provider-limit guidance card underneath.
func TestFormatErrorWithGuidance_GLMLimitFullCard(t *testing.T) {
	got := stripAnsi(FormatErrorWithGuidanceWidth(DefaultStyles(), glmLimitRaw, 100))

	// The tail may wrap across lines — normalize to one line before asserting.
	flat := strings.Join(strings.Fields(got), " ")
	if !strings.Contains(flat, "switch provider with") || !strings.Contains(flat, "(resets Monday 00:00 UTC)") {
		t.Fatalf("actionable tail amputated:\n%s", got)
	}
	if strings.Contains(got, "(other)") {
		t.Fatalf("machine taxonomy leaked into the card:\n%s", got)
	}
	if !strings.Contains(got, "Provider Limit Reached") {
		t.Fatalf("provider-limit guidance must match the GLM cap wording:\n%s", got)
	}
	if !strings.Contains(got, "Try: /provider") {
		t.Fatalf("command hint missing:\n%s", got)
	}
}

func TestGetErrorGuidance_GLMLimitWordings(t *testing.T) {
	for _, msg := range []string{
		"GLM weekly/monthly limit exhausted — wait for the reset",
		"Usage limit reached for 5 hour. Your limit will reset at 12:00",
	} {
		g := GetErrorGuidance(msg)
		if g == nil || g.Title != "Provider Limit Reached" {
			t.Errorf("GetErrorGuidance(%q) = %+v, want Provider Limit Reached", msg, g)
		}
	}
}

// Toasts are one line by design — but their TAIL is where this app puts the
// action ("… — check /hooks", "/loop resume <id>", the "→ hint" suffix). The
// old tail-cut amputated it on narrow terminals; toasts now middle-elide and
// strip machine wrapper taxonomy.
func TestRenderToast_KeepsActionableTailAndStripsWrapper(t *testing.T) {
	tm := NewToastManager(DefaultStyles())
	tm.ShowError("Loop loop-x #3: Iteration error: model response error (other): GLM weekly limit exhausted — retry later or /loop resume loop-x")

	got := stripAnsi(tm.View(60)) // narrow terminal
	if strings.Contains(got, "model response error") || strings.Contains(got, "(other)") {
		t.Fatalf("machine wrapper leaked into the toast: %q", got)
	}
	if !strings.Contains(got, "/loop resume loop-x") {
		t.Fatalf("the actionable tail must survive toast truncation: %q", got)
	}
	if !strings.Contains(got, "Loop loop-x #3") {
		t.Fatalf("the head (what happened) must survive too: %q", got)
	}

	// Short toasts render whole, untouched.
	tm2 := NewToastManager(DefaultStyles())
	tm2.ShowInfo("Saved")
	if got := stripAnsi(tm2.View(80)); !strings.Contains(got, "Saved") {
		t.Fatalf("short toast must render verbatim: %q", got)
	}
}

// The loop-guard abort error is machine-shaped ("tool pattern %q repeated N
// times") — the guidance card must translate it into the human story + the
// practical recovery.
func TestGetErrorGuidance_LoopGuardAborts(t *testing.T) {
	for _, msg := range []string{
		`executor stagnation: tool pattern "bash:git status --short" repeated 5 times consecutively`,
		"executor re-coverage loop: read internal/x.go",
		"agent reached maximum turn limit (25 turns)",
	} {
		g := GetErrorGuidance(msg)
		if g == nil || g.Title != "Agent Got Stuck in a Loop" {
			t.Errorf("GetErrorGuidance(%q) = %+v, want Agent Got Stuck in a Loop", msg, g)
		}
	}
}
