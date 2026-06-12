package context

import (
	"testing"
)

func TestGetModelLimits_ExactMatch(t *testing.T) {
	tests := []struct {
		model   string
		wantIn  int
		wantOut int
	}{
		{"glm-5", 200000, 131072},
		{"glm-4.7", 128000, 131072},
		{"glm-4.5-air", 128000, 32768},
		{"minimax", 204800, 16384},
		{"kimi", 262144, 32768},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			lim := getModelLimits(tt.model)
			if lim.MaxInputTokens != tt.wantIn {
				t.Errorf("MaxInputTokens = %d, want %d", lim.MaxInputTokens, tt.wantIn)
			}
			if lim.MaxOutputTokens != tt.wantOut {
				t.Errorf("MaxOutputTokens = %d, want %d", lim.MaxOutputTokens, tt.wantOut)
			}
		})
	}
}

func TestGetModelLimits_FuzzyMatch(t *testing.T) {
	tests := []struct {
		model   string
		wantIn  int
		wantOut int
	}{
		// GLM variants — longest key must win deterministically. A name like
		// "glm-4.5-preview" is a substring of both "glm-4.5" (131K out) and
		// "glm-4" (32K out); the longer, more-specific key must be selected.
		{"glm-5.1-preview", 200000, 131072},
		{"glm-5-turbo-v2", 200000, 131072},
		{"glm-4.5-preview", 128000, 131072}, // must not fall back to glm-4 (32K)
		{"glm-4.6-beta", 128000, 131072},    // must not fall back to glm-4 (32K)
		// Case insensitive
		{"GLM-5.1-PREVIEW", 200000, 131072},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			lim := getModelLimits(tt.model)
			if lim.MaxInputTokens != tt.wantIn {
				t.Errorf("MaxInputTokens = %d, want %d", lim.MaxInputTokens, tt.wantIn)
			}
			if lim.MaxOutputTokens != tt.wantOut {
				t.Errorf("MaxOutputTokens = %d, want %d", lim.MaxOutputTokens, tt.wantOut)
			}
		})
	}
}

func TestGetModelLimits_Unknown(t *testing.T) {
	lim := getModelLimits("totally-unknown-model")
	if lim.MaxInputTokens != 128000 {
		t.Errorf("unknown MaxInputTokens = %d, want 128000", lim.MaxInputTokens)
	}
	if lim.MaxOutputTokens != 8192 {
		t.Errorf("unknown MaxOutputTokens = %d, want 8192", lim.MaxOutputTokens)
	}
	if lim.WarningThreshold != 0.8 {
		t.Errorf("unknown WarningThreshold = %v, want 0.8", lim.WarningThreshold)
	}
}

func TestGetModelLimits_Exported(t *testing.T) {
	// GetModelLimits (exported) wraps getModelLimits
	lim := GetModelLimits("glm-5")
	if lim.MaxInputTokens != 200000 {
		t.Errorf("GetModelLimits MaxInputTokens = %d, want 200000", lim.MaxInputTokens)
	}
}

func TestGetPricing_KnownModels(t *testing.T) {
	tests := []struct {
		model      string
		wantInput  float64
		wantOutput float64
	}{
		{"glm-5.1", 4.00, 16.00},
		{"glm-5", 1.00, 4.00},
		{"kimi-for-coding", 0.95, 4.00},
		{"MiniMax-M2.7", 0.30, 1.20},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			p, ok := DefaultPricing[tt.model]
			if !ok {
				t.Errorf("no pricing for %q", tt.model)
				return
			}
			if p.InputCostPer1M != tt.wantInput {
				t.Errorf("InputCostPer1M = %v, want %v", p.InputCostPer1M, tt.wantInput)
			}
			if p.OutputCostPer1M != tt.wantOutput {
				t.Errorf("OutputCostPer1M = %v, want %v", p.OutputCostPer1M, tt.wantOutput)
			}
		})
	}
}

// getPricing()'s resolution logic — case-insensitive exact, longest-key fuzzy,
// and the unknown→flash fallback — was previously untested (TestGetPricing_
// KnownModels only checks raw map entries, never the function). The highspeed
// variants are the load-bearing case: case-insensitive matching must let them
// hit their specific entry rather than silently falling back to the cheaper
// bare "minimax" price, which would understate /cost.
func TestGetPricing_Resolution(t *testing.T) {
	cases := []struct {
		model      string
		wantInput  float64
		wantOutput float64
	}{
		{"MiniMax-M2.7-highspeed", 0.60, 2.40}, // specific, NOT the 0.30 "minimax" fallback
		{"minimax-m2.7-highspeed", 0.60, 2.40}, // case-insensitive resolves the same
		{"MiniMax-M2.7", 0.30, 1.20},           // base variant
		{"deepseek-v4-pro-2026", 0.435, 0.87},  // suffix variant → longest key, not bare "deepseek"
		{"GLM-5.1", 4.00, 16.00},               // case-insensitive exact
		{"glm-5-turbo", 0.70, 2.80},            // not the shorter "glm-5"
		{"totally-unknown-model", 0.14, 0.28},  // flash default, NOT zero (silent under-report)
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			p := getPricing(c.model)
			if p.InputCostPer1M != c.wantInput || p.OutputCostPer1M != c.wantOutput {
				t.Errorf("getPricing(%q) = %v/%v, want %v/%v",
					c.model, p.InputCostPer1M, p.OutputCostPer1M, c.wantInput, c.wantOutput)
			}
		})
	}
}

func TestDefaultModelLimitsCount(t *testing.T) {
	if len(DefaultModelLimits) < 8 {
		t.Errorf("DefaultModelLimits has %d entries, want >= 8", len(DefaultModelLimits))
	}
}

func TestDefaultPricingCount(t *testing.T) {
	if len(DefaultPricing) < 8 {
		t.Errorf("DefaultPricing has %d entries, want >= 8", len(DefaultPricing))
	}
}

// TestGetPricingFunction guards against two historical bugs:
//  1. Short-key wins: map iteration order caused "deepseek" (fallback) to
//     match before "deepseek-v4-pro", returning wrong price. Fix: longest
//     matching key wins.
//  2. Case mismatch: keys like "MiniMax-M2.7" were compared without
//     lowercasing, so the specific entry was never reached and the generic
//     "minimax" fallback was returned instead. Fix: case-insensitive compare.
func TestGetPricingFunction(t *testing.T) {
	cases := []struct {
		model      string
		wantInput  float64
		wantOutput float64
		desc       string
	}{
		// Specific DeepSeek models must not match the shorter "deepseek" fallback.
		{"deepseek-v4-pro", 0.435, 0.87, "deepseek-v4-pro beats deepseek fallback"},
		{"deepseek-v4-flash", 0.14, 0.28, "deepseek-v4-flash beats deepseek fallback"},
		// MiniMax mixed-case key must match case-insensitively.
		{"MiniMax-M2.7", 0.30, 1.20, "MiniMax-M2.7 case-insensitive match"},
		// "glm-5.1" must not match "glm-5" (a shorter substring).
		{"glm-5.1", 4.00, 16.00, "glm-5.1 beats shorter glm-5 key"},
		// Generic fallback should match for unknown model.
		{"unknown-model-xyz", 0.14, 0.28, "unknown model gets flash-tier fallback"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			p := getPricing(tc.model)
			if p.InputCostPer1M != tc.wantInput || p.OutputCostPer1M != tc.wantOutput {
				t.Errorf("getPricing(%q) = {%.2f, %.2f}, want {%.2f, %.2f}",
					tc.model, p.InputCostPer1M, p.OutputCostPer1M,
					tc.wantInput, tc.wantOutput)
			}
		})
	}
}
