package context

import (
	"math"
	"testing"

	"gokin/internal/client"
)

func TestGLM53LimitsAndCost(t *testing.T) {
	for _, tc := range []struct {
		model                 string
		input, cached, output float64
	}{
		{"glm-5.3", 1.4, 0.26, 4.4},
		{"glm-5.3-flash", 0.15, 0.03, 0.5},
		{"glm-5.3-flashx", 0.37, 0.075, 1.25},
		{"GLM-5.3-FLASH[1m]", 0.15, 0.03, 0.5},
	} {
		t.Run(tc.model, func(t *testing.T) {
			limits := GetModelLimits(tc.model)
			if limits.MaxInputTokens != 1000000 || limits.MaxOutputTokens != 131072 {
				t.Fatalf("limits = %+v", limits)
			}
			profile := client.GetModelProfile(tc.model)
			if profile.ContextWindow != limits.MaxInputTokens || !profile.SupportsTools || !profile.IsCoding {
				t.Fatalf("client profile disagrees with context limits: %+v", profile)
			}
			counter := NewTokenCounter(nil, tc.model, nil)
			cost := counter.CalculateCostWithCache(1000000, 1000000, 500000)
			want := 0.5*tc.input + 0.5*tc.cached + tc.output
			if math.Abs(cost-want) > 1e-9 {
				t.Fatalf("cost = %v, want %v", cost, want)
			}
			if !HasKnownPricing(tc.model) {
				t.Fatal("budget enforcement cannot resolve GLM pricing")
			}
		})
	}
}
