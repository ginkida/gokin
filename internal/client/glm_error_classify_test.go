package client

import (
	"strings"
	"testing"
)

// TestClassifyGLMErrorCode_QuotaAndBalance pins that quota/balance codes —
// including 1308 (the Coding-Plan cap) — are non-retryable AND carry an
// actionable description, so a quota dead-end never shows a cryptic raw code.
func TestClassifyGLMErrorCode_QuotaAndBalance(t *testing.T) {
	cases := []struct {
		code      string
		retryable bool
		mustHave  []string // substrings the description should contain
	}{
		{"1308", false, []string{"quota", "/provider"}},
		{"1212", false, []string{"quota"}},
		{"1211", false, []string{"balance"}},
		{"1305", true, []string{"overload"}}, // overload stays retryable
	}
	for _, tc := range cases {
		retryable, _, desc := classifyGLMErrorCode(tc.code, "raw provider message")
		if retryable != tc.retryable {
			t.Errorf("code %s retryable = %v, want %v", tc.code, retryable, tc.retryable)
		}
		for _, sub := range tc.mustHave {
			if !strings.Contains(strings.ToLower(desc), strings.ToLower(sub)) {
				t.Errorf("code %s description %q missing %q", tc.code, desc, sub)
			}
		}
		if tc.code == "1308" && desc == "raw provider message" {
			t.Errorf("1308 should be classified, not pass through the raw message")
		}
	}
}
