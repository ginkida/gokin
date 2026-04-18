package app

import (
	"errors"
	"testing"
)

func TestIsOverloadError_Words(t *testing.T) {
	cases := map[string]bool{
		"server overloaded":              true,
		"API rate limit exceeded":        true,
		"too many requests (HTTP 429)":   true,
		"random network timeout":         false,
		"connection reset by peer":       false,
		"":                               false,
	}
	for msg, want := range cases {
		got := isOverloadError(errors.New(msg))
		if got != want {
			t.Errorf("isOverloadError(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestIsOverloadError_CodesWordBoundary(t *testing.T) {
	// Real z.ai errors — should match.
	for _, msg := range []string{
		"z.ai API error (1305): service unavailable",
		"[GLM 1305] overloaded",
		"HTTP 529: Site is overloaded",
		"status 529 returned",
		"code=1305",
	} {
		if !isOverloadError(errors.New(msg)) {
			t.Errorf("expected overload match for %q", msg)
		}
	}

	// Numeric substrings embedded inside timings — must NOT match.
	for _, msg := range []string{
		"timeout after 1305ms",
		"elapsed 529ms",
		"retry delay 11305 attempts",
		"dial error 5291",
	} {
		if isOverloadError(errors.New(msg)) {
			t.Errorf("unexpected overload match for %q", msg)
		}
	}
}

func TestIsOverloadError_Nil(t *testing.T) {
	if isOverloadError(nil) {
		t.Error("nil error should not match overload")
	}
}
