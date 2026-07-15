package client

import (
	"errors"
	"strings"
	"testing"
)

// TestClassifyGLMErrorCode_OfficialTaxonomy pins the current Z.AI error table.
// These codes are deliberately not inferred from their HTTP status: a gateway
// can wrap a hard business rejection in 429/5xx.
func TestClassifyGLMErrorCode_OfficialTaxonomy(t *testing.T) {
	cases := []struct {
		code      string
		retryable bool
		mustHave  []string // substrings the description should contain
	}{
		{"-500", true, []string{"service", "retrying"}},
		{"1200", true, []string{"service", "retrying"}},
		{"1230", true, []string{"processing", "retrying"}},
		{"1234", true, []string{"network", "retrying"}},
		{"1302", true, []string{"rate limit", "retrying"}},
		{"1305", true, []string{"overload"}},
		{"1113", false, []string{"balance", "/provider"}},
		{"1210", false, []string{"invalid", "parameter"}},
		{"1301", false, []string{"sensitive"}},
		{"1308", false, []string{"quota", "/provider"}},
		{"1309", false, []string{"subscription", "/provider"}},
		{"1310", false, []string{"limit", "/provider"}},
		{"1311", false, []string{"plan", "/provider"}},
		{"1313", false, []string{"fair usage", "/provider"}},
		{"1314", false, []string{"enterprise", "/provider"}},
		{"1315", false, []string{"api key", "/provider"}},
		{"1316", false, []string{"limit", "/provider"}},
		{"1317", false, []string{"limit", "/provider"}},
		{"1318", false, []string{"limit", "/provider"}},
		{"1319", false, []string{"limit", "/provider"}},
		{"1320", false, []string{"limit", "/provider"}},
		{"1321", false, []string{"limit", "/provider"}},
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

func TestClassifyGLMErrorCode_OfficialRetryBoundary(t *testing.T) {
	tests := []struct {
		code      string
		keyword   string
		overload  bool
		transient bool
	}{
		{"1234", "temporarily", false, true},
		{"1302", "rate limit", true, true},
		{"1305", "overloaded", true, true},
	}
	for _, tc := range tests {
		retryable, keyword, description := classifyGLMErrorCode(tc.code, "provider detail")
		if !retryable || keyword != tc.keyword {
			t.Fatalf("code %s classification = retryable %v keyword %q", tc.code, retryable, keyword)
		}
		err := errors.New(formatGLMError(description, keyword, tc.code, "provider detail"))
		if got := IsOverloadError(err); got != tc.overload {
			t.Errorf("code %s IsOverloadError = %v, want %v", tc.code, got, tc.overload)
		}
		if got := IsTransientProviderError(err); got != tc.transient {
			t.Errorf("code %s IsTransientProviderError = %v, want %v", tc.code, got, tc.transient)
		}
		if !IsRetryableError(err) {
			t.Errorf("code %s should enter the generic retry path", tc.code)
		}
	}

	// These were the stale local mappings: official Z.AI now defines 1210 as
	// invalid parameters and 1301 as sensitive content. Neither may retry.
	for _, code := range []string{"1210", "1301"} {
		err, retryable := newGLMStreamError(code, "hard rejection")
		if retryable || IsRetryableError(err) || IsOverloadError(err) || IsTransientProviderError(err) {
			t.Errorf("code %s was classified as retryable/transient: %v", code, err)
		}
		if !IsTerminalProviderError(err) {
			t.Errorf("code %s must be a terminal provider error", code)
		}
	}
}
