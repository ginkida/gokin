package ui

import (
	"strings"
	"testing"
)

// TestSummarizeSubAgentTask pins the contract that turns a dispatch prompt
// into a one-line activity-feed description. The UI regression we're
// guarding: sub-agents used to render as "Sub-agent: general" for every
// agent, so users couldn't tell them apart or tell what they were working on.
func TestSummarizeSubAgentTask(t *testing.T) {
	cases := []struct {
		name      string
		prompt    string
		agentType string
		want      string
	}{
		{
			name:      "empty_prompt_falls_back_to_type",
			prompt:    "",
			agentType: "general",
			want:      "Sub-agent: general",
		},
		{
			name:      "empty_prompt_and_type",
			prompt:    "",
			agentType: "",
			want:      "Sub-agent",
		},
		{
			name:      "short_single_line_prompt",
			prompt:    "Find all TODO comments in the repo",
			agentType: "explore",
			want:      "explore · Find all TODO comments in the repo",
		},
		{
			name: "multiline_skips_system_preamble",
			prompt: "You are a code search assistant.\n" +
				"Find all callers of renderContextBar",
			agentType: "explore",
			want: "explore · Find all callers of renderContextBar",
		},
		{
			name:      "truncates_very_long_prompt",
			prompt:    strings.Repeat("x", 200),
			agentType: "general",
			// 70 - 1 (ellipsis) = 69 x's, then "…", prefix "general · "
			want: "general · " + strings.Repeat("x", 69) + "…",
		},
		{
			name:      "period_after_verb_trims_to_first_sentence",
			prompt:    "Locate the stagnation fingerprint. Then list its callers.",
			agentType: "general",
			want:      "general · Locate the stagnation fingerprint",
		},
		{
			name:      "keeps_short_sentence_with_early_period",
			prompt:    "Go.", // period too early to trim
			agentType: "general",
			want:      "general · Go.",
		},
		{
			name:      "prompt_only_whitespace_falls_back",
			prompt:    "   \n\t\n  ",
			agentType: "general",
			want:      "Sub-agent: general",
		},
		{
			name: "uses_raw_first_line_when_preamble_is_only_thing",
			// All lines match the "You are " preamble skip — we still want
			// *something* visible rather than an empty row.
			prompt:    "You are an assistant",
			agentType: "general",
			want:      "general · You are an assistant",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := summarizeSubAgentTask(tc.prompt, tc.agentType)
			if got != tc.want {
				t.Errorf("summarizeSubAgentTask(%q, %q):\n  got:  %q\n  want: %q",
					tc.prompt, tc.agentType, got, tc.want)
			}
		})
	}
}

// TestSummarizeSubAgentTask_NeverExceedsMaxLen is a property-style guard:
// regardless of input, the visible width must stay under the feed row's
// budget so wrapping doesn't break the panel layout.
func TestSummarizeSubAgentTask_NeverExceedsMaxLen(t *testing.T) {
	inputs := []string{
		strings.Repeat("x", 1000),
		strings.Repeat("You are a very long preamble\n", 50) +
			strings.Repeat("a", 500),
		"a b c d e f g h i j k l m n o p q r s t u v w x y z " +
			strings.Repeat("zoom ", 100),
	}
	for i, in := range inputs {
		got := summarizeSubAgentTask(in, "t")
		// Rune count, not byte count — the helper guards runes.
		runes := []rune(got)
		// Allow room for the "t · " prefix (4 runes) + 70-char body.
		if len(runes) > 4+70 {
			t.Errorf("case %d: length %d exceeds 74 runes: %q", i, len(runes), got)
		}
	}
}
