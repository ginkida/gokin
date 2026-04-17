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
		{"gemini-2.0-flash", 1048576, 8192},
		{"gemini-3-flash", 1048576, 65536},
		{"gemini-3-pro", 1048576, 65536},
		{"claude-opus", 200000, 32000},
		{"claude-sonnet", 200000, 16384},
		{"claude-haiku", 200000, 8192},
		{"glm-5", 128000, 131072},
		{"deepseek-chat", 64000, 8192},
		{"minimax", 204800, 16384},
		{"kimi", 256000, 32768},
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
		// "-preview" suffix should fuzzy match
		{"gemini-3-flash-preview", 1048576, 65536},
		{"gemini-3-pro-preview", 1048576, 65536},
		// Full versioned model IDs
		{"claude-sonnet-4-5-20250929", 200000, 16384},
		{"claude-opus-4-0-20250929", 200000, 32000},
		// Case insensitive
		{"GEMINI-3-FLASH", 1048576, 65536},
		{"Claude-Sonnet", 200000, 16384},
		// GLM variants — longest key must win deterministically. A name like
		// "glm-4.5-preview" is a substring of both "glm-4.5" (131K out) and
		// "glm-4" (32K out); the longer, more-specific key must be selected.
		{"glm-5.1-preview", 128000, 131072},
		{"glm-5-turbo-v2", 128000, 131072},
		{"glm-4.5-preview", 128000, 131072}, // must not fall back to glm-4 (32K)
		{"glm-4.6-beta", 128000, 131072},    // must not fall back to glm-4 (32K)
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
	lim := GetModelLimits("claude-sonnet")
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
		{"gemini-2.0-flash", 0.10, 0.40},
		{"claude-opus", 15.00, 75.00},
		{"claude-sonnet", 3.00, 15.00},
		{"deepseek-chat", 0.27, 1.10},
		{"glm-5", 1.00, 4.00},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			p := getPricing(tt.model)
			if p.InputCostPer1M != tt.wantInput {
				t.Errorf("InputCostPer1M = %v, want %v", p.InputCostPer1M, tt.wantInput)
			}
			if p.OutputCostPer1M != tt.wantOutput {
				t.Errorf("OutputCostPer1M = %v, want %v", p.OutputCostPer1M, tt.wantOutput)
			}
		})
	}
}

func TestGetPricing_FuzzyMatch(t *testing.T) {
	// Model with suffix should fuzzy match
	p := getPricing("gemini-3-flash-preview")
	if p.InputCostPer1M != 0.50 {
		t.Errorf("gemini-3-flash-preview InputCostPer1M = %v, want 0.50", p.InputCostPer1M)
	}
}

func TestGetPricing_Unknown(t *testing.T) {
	// Unknown model defaults to gemini-1.5-flash pricing
	p := getPricing("unknown-model")
	expected := DefaultPricing["gemini-1.5-flash"]
	if p.InputCostPer1M != expected.InputCostPer1M {
		t.Errorf("unknown InputCostPer1M = %v, want %v", p.InputCostPer1M, expected.InputCostPer1M)
	}
}

func TestFormatCost(t *testing.T) {
	tests := []struct {
		cost float64
		want string
	}{
		{0, "$0.00"},
		{0.00001, "< $0.0001"},
		{0.00005, "< $0.0001"},
		{0.0001, "$0.0001"},
		{0.1234, "$0.1234"},
		{1.5, "$1.5000"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := FormatCost(tt.cost)
			if got != tt.want {
				t.Errorf("FormatCost(%v) = %q, want %q", tt.cost, got, tt.want)
			}
		})
	}
}

func TestEstimateTokens(t *testing.T) {
	// Empty
	if got := EstimateTokens(""); got != 0 {
		t.Errorf("EstimateTokens(\"\") = %d, want 0", got)
	}

	// Non-empty should return > 0
	if got := EstimateTokens("hello world"); got <= 0 {
		t.Error("EstimateTokens(\"hello world\") should be > 0")
	}

	// Code should produce more tokens than prose of same length
	prose := "The quick brown fox jumps over the lazy dog repeatedly in the field"
	code := "func main() { fmt.Println(\"hello\"); if err != nil { return err } }"
	proseTokens := EstimateTokens(prose)
	codeTokens := EstimateTokens(code)
	if codeTokens <= 0 {
		t.Error("code tokens should be > 0")
	}
	if proseTokens <= 0 {
		t.Error("prose tokens should be > 0")
	}
}

func TestEstimateTokensWithType(t *testing.T) {
	if got := EstimateTokensWithType("", ContentTypeCode); got != 0 {
		t.Errorf("empty with ContentTypeCode = %d, want 0", got)
	}

	text := "func main() { return nil }"
	// Code type should use chars/3.2
	code := EstimateTokensWithType(text, ContentTypeCode)
	// JSON type should use chars/3.0
	json := EstimateTokensWithType(text, ContentTypeJSON)
	if json <= 0 || code <= 0 {
		t.Error("both should be > 0")
	}
	// JSON uses smaller divisor, so more tokens
	if json < code {
		t.Errorf("JSON tokens (%d) should be >= code tokens (%d)", json, code)
	}
}

func TestDetectContentType(t *testing.T) {
	tests := []struct {
		name string
		text string
		want ContentType
	}{
		{"empty", "", ContentTypeProse},
		{"json object", `{"key": "value", "num": 42}`, ContentTypeJSON},
		{"json array", `[1, 2, 3]`, ContentTypeJSON},
		{"prose", "The quick brown fox jumps over the lazy dog.", ContentTypeProse},
		{"go code", "func main() {\n\tfmt.Println(\"hello\")\n\tif err != nil {\n\t\treturn err\n\t}\n}", ContentTypeCode},
		{"python code", "def main():\n    print('hello')\n    return 0\n\nclass Foo:\n    pass", ContentTypeCode},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectContentType(tt.text)
			if got != tt.want {
				t.Errorf("detectContentType() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestContainsCamelCase(t *testing.T) {
	tests := []struct {
		word string
		want bool
	}{
		{"camelCase", true},
		{"myFunc", true},
		{"ALLCAPS", false},
		{"lowercase", false},
		{"CamelCase", true}, // has both lower and upper chars
		{"getHTTPClient", true},
	}

	for _, tt := range tests {
		t.Run(tt.word, func(t *testing.T) {
			got := containsCamelCase(tt.word)
			if got != tt.want {
				t.Errorf("containsCamelCase(%q) = %v, want %v", tt.word, got, tt.want)
			}
		})
	}
}

func TestDefaultModelLimitsCount(t *testing.T) {
	if len(DefaultModelLimits) < 15 {
		t.Errorf("DefaultModelLimits has %d entries, want >= 15", len(DefaultModelLimits))
	}
}

func TestDefaultPricingCount(t *testing.T) {
	if len(DefaultPricing) < 15 {
		t.Errorf("DefaultPricing has %d entries, want >= 15", len(DefaultPricing))
	}
}
