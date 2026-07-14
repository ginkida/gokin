package ui

import (
	"strings"
	"testing"
)

// TestErrorGuidance_PolishedTitlesAreReachable pins the dedup pass: each
// of these errors used to match an earlier shorter pattern that won by
// first-match ordering, leaving the more polished guidance at the
// bottom of errorGuidancePatterns as unreachable dead code. Merged in
// place — these are the wordings users now see.
//
// If a future refactor reintroduces a duplicate earlier in the slice
// with a less helpful title, this test catches it.
func TestErrorGuidance_PolishedTitlesAreReachable(t *testing.T) {
	cases := []struct {
		name      string
		errMsg    string
		wantTitle string
	}{
		{"401 → Authentication Failed (polished suggestions)",
			"HTTP 401 Unauthorized", "Authentication Failed"},
		{"authentication_error variant",
			"authentication_error: invalid api key", "Authentication Failed"},
		{"403 → Access Denied (polished suggestions)",
			"HTTP 403 Forbidden", "Access Denied"},
		{"402 / billing → Quota or Billing Issue",
			"HTTP 402 Payment Required", "Provider Limit Reached"},
		{"quota exceeded → Quota or Billing Issue",
			"quota exceeded for this billing cycle", "Provider Limit Reached"},
		{"insufficient credit → Quota or Billing Issue",
			"insufficient credit on account", "Provider Limit Reached"},
		{"model_not_found → Model Not Available",
			"model_not_found: glm-99", "Model Not Available"},
		{"unknown model → Model Not Available",
			"unknown model: gpt-99", "Model Not Available"},
		{"context window → Context Window Exceeded",
			"context window exceeded for kimi-for-coding", "Context Window Exceeded"},
		{"prompt is too long → Context Window Exceeded",
			"the prompt is too long for this model", "Context Window Exceeded"},
		{"max tokens → Context Window Exceeded",
			"max tokens limit reached", "Context Window Exceeded"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := GetErrorGuidance(tc.errMsg)
			if g == nil {
				t.Fatalf("no guidance for %q", tc.errMsg)
			}
			if g.Title != tc.wantTitle {
				t.Errorf("title for %q = %q, want %q", tc.errMsg, g.Title, tc.wantTitle)
			}
		})
	}
}

// TestErrorGuidance_NoDuplicateTitles is the structural invariant: no two
// patterns share the same Title. Duplicates indicate dead code (first
// pattern wins by ordering, so the second's guidance never renders).
func TestErrorGuidance_NoDuplicateTitles(t *testing.T) {
	seen := make(map[string]int)
	for _, g := range errorGuidancePatterns {
		seen[g.Title]++
	}
	for title, count := range seen {
		if count > 1 {
			t.Errorf("Title %q appears %d times in errorGuidancePatterns — later occurrences are dead code (first-match wins)", title, count)
		}
	}
}

// TestErrorGuidance_AuthFailedSuggestionsAreActionable pins the polished
// wording for the most common error path (auth). Suggestions should be
// instructions, not status reports.
func TestErrorGuidance_AuthFailedSuggestionsAreActionable(t *testing.T) {
	g := GetErrorGuidance("HTTP 401 unauthorized")
	if g == nil {
		t.Fatal("no guidance")
	}
	// First suggestion should describe the problem in user terms.
	if !strings.Contains(g.Suggestions[0], "API key") {
		t.Errorf("first suggestion should mention API key, got %q", g.Suggestions[0])
	}
	// One suggestion should point at the recovery command.
	hasLoginHint := false
	for _, s := range g.Suggestions {
		if strings.Contains(s, "/login") {
			hasLoginHint = true
			break
		}
	}
	if !hasLoginHint {
		t.Errorf("auth-failed suggestions should include a /login hint, got %v", g.Suggestions)
	}
	if g.Command != "/login" {
		t.Errorf("Command = %q, want /login", g.Command)
	}
}
