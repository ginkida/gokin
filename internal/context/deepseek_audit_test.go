package context

import (
	"math"
	"testing"

	"gokin/internal/client"
)

func TestDeepSeekCurrentLimitsAndCachePricing(t *testing.T) {
	for _, tc := range []struct {
		model                 string
		input, cached, output float64
	}{
		{"deepseek-flash", 0.30, 0.006, 1.20},
		{"deepseek-v4-flash", 0.30, 0.006, 1.20},
		{"deepseek-v4-pro", 1.32, 0.044, 3.96},
	} {
		t.Run(tc.model, func(t *testing.T) {
			counter := NewTokenCounter(nil, tc.model, nil)
			limits := counter.GetLimits()
			if limits.MaxInputTokens != 1000000 || limits.MaxOutputTokens != 384000 {
				t.Fatalf("limits=%+v", limits)
			}
			profile := client.GetModelProfile(tc.model)
			if profile.ContextWindow != limits.MaxInputTokens || !profile.SupportsTools {
				t.Fatalf("profile=%+v", profile)
			}
			// One million prompt tokens, half cached, and one million output tokens.
			want := (tc.input+tc.cached)/2 + tc.output
			if got := counter.CalculateCostWithCache(1000000, 1000000, 500000); math.Abs(got-want) > 1e-9 {
				t.Fatalf("cost=%g want %g", got, want)
			}
		})
	}
}
