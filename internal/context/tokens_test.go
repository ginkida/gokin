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
		{"glm-5", 128000, 131072},
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
		{"glm-5.1-preview", 128000, 131072},
		{"glm-5-turbo-v2", 128000, 131072},
		{"glm-4.5-preview", 128000, 131072}, // must not fall back to glm-4 (32K)
		{"glm-4.6-beta", 128000, 131072},    // must not fall back to glm-4 (32K)
		// Case insensitive
		{"GLM-5.1-PREVIEW", 128000, 131072},
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
	if lim.MaxInputTokens != 128000 {
		t.Errorf("GetModelLimits MaxInputTokens = %d, want 128000", lim.MaxInputTokens)
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
		{"kimi-for-coding", 1.12, 4.48},
		{"MiniMax-M2.7", 1.40, 5.60},
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
