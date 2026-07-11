package client

import (
	"fmt"
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
		{"1312", true, []string{"high traffic", "retrying"}},
		// The 429 subscription/quota/plan/FUP codes are all hard failures — the
		// only recovery is user action, so each must carry a switch-provider hint.
		{"1309", false, []string{"subscription", "/provider"}},
		{"1310", false, []string{"limit", "/provider"}},
		{"1311", false, []string{"plan", "/provider"}},
		{"1313", false, []string{"fair usage", "/provider"}},
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
		// Classified codes must NEVER pass the raw provider message through.
		if desc == "raw provider message" {
			t.Errorf("code %s should be classified, not pass through the raw message", tc.code)
		}
	}
}

func TestClassifyGLMErrorCode_1312EntersOverloadRetryPath(t *testing.T) {
	retryable, keyword, description := classifyGLMErrorCode("1312", "This model is currently experiencing high traffic")
	if !retryable || keyword != "overloaded" {
		t.Fatalf("1312 classification = retryable %v keyword %q", retryable, keyword)
	}
	err := fmt.Errorf("%s [%s] (1312)", description, keyword)
	if !IsOverloadError(err) {
		t.Fatalf("1312 error %q did not enter patient overload retry path", err)
	}
	if IsTerminalProviderError(err) {
		t.Fatalf("1312 error %q incorrectly classified as terminal", err)
	}
}
