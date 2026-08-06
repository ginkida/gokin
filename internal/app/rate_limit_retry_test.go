package app

import "testing"

func TestScheduleRateLimitAutoRetry_ZeroValueMapIsSafe(t *testing.T) {
	a := &App{}
	attempt, _, ok := a.scheduleRateLimitAutoRetry("headless rate limit")
	if !ok || attempt != 1 {
		t.Fatalf("zero-value rate-limit scheduler = attempt %d ok=%v, want 1/true", attempt, ok)
	}
	if a.rateLimitRetryCount == nil {
		t.Fatal("zero-value rate-limit scheduler did not initialize its retry ledger")
	}
}
