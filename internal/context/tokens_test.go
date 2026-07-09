package context

import (
	"context"
	"testing"

	"gokin/internal/client"
	"gokin/internal/config"
	"gokin/internal/testkit"

	"google.golang.org/genai"
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
		{"glm-5.2-preview", 1000000, 131072}, // glm-5.2 fuzzy-matches the 1M base
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
		{"glm-5.2", 4.00, 16.00},
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

// --- FormatCost ---

func TestFormatCost_Zero(t *testing.T) {
	if got := FormatCost(0); got != "$0.00" {
		t.Errorf("FormatCost(0) = %q, want '$0.00'", got)
	}
}

func TestFormatCost_TinyCost(t *testing.T) {
	if got := FormatCost(0.00001); got != "< $0.0001" {
		t.Errorf("FormatCost(0.00001) = %q, want '< $0.0001'", got)
	}
}

func TestFormatCost_SmallCost(t *testing.T) {
	got := FormatCost(0.005)
	// Should be formatted as $0.0050
	if got != "$0.0050" {
		t.Errorf("FormatCost(0.005) = %q, want '$0.0050'", got)
	}
}

func TestFormatCost_LargeCost(t *testing.T) {
	got := FormatCost(1.5)
	if got != "$1.5000" {
		t.Errorf("FormatCost(1.5) = %q, want '$1.5000'", got)
	}
}

// --- EstimateTokens ---

func TestEstimateTokens_Empty(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Errorf("EstimateTokens('') = %d, want 0", got)
	}
}

func TestEstimateTokens_Prose(t *testing.T) {
	got := EstimateTokens("hello world this is a test")
	if got <= 0 {
		t.Errorf("EstimateTokens(prose) = %d, want > 0", got)
	}
}

func TestEstimateTokens_Code(t *testing.T) {
	code := `func main() { x := 10; fmt.Println(x) }`
	got := EstimateTokens(code)
	if got <= 0 {
		t.Errorf("EstimateTokens(code) = %d, want > 0", got)
	}
}

func TestEstimateTokens_JSON(t *testing.T) {
	json := `{"key": "value", "num": 42}`
	got := EstimateTokens(json)
	if got <= 0 {
		t.Errorf("EstimateTokens(json) = %d, want > 0", got)
	}
}

// --- EstimateTokensWithType ---

func TestEstimateTokensWithType_Empty(t *testing.T) {
	if got := EstimateTokensWithType("", ContentTypeProse); got != 0 {
		t.Errorf("EstimateTokensWithType('') = %d, want 0", got)
	}
}

func TestEstimateTokensWithType_Prose(t *testing.T) {
	got := EstimateTokensWithType("hello world", ContentTypeProse)
	if got <= 0 {
		t.Errorf("EstimateTokensWithType(prose) = %d, want > 0", got)
	}
}

func TestEstimateTokensWithType_Code(t *testing.T) {
	got := EstimateTokensWithType("func main() {}", ContentTypeCode)
	if got <= 0 {
		t.Errorf("EstimateTokensWithType(code) = %d, want > 0", got)
	}
}

func TestEstimateTokensWithType_JSON(t *testing.T) {
	got := EstimateTokensWithType(`{"a": 1}`, ContentTypeJSON)
	if got <= 0 {
		t.Errorf("EstimateTokensWithType(json) = %d, want > 0", got)
	}
}

func TestEstimateTokensWithType_Mixed(t *testing.T) {
	got := EstimateTokensWithType("hello world func main", ContentTypeMixed)
	if got <= 0 {
		t.Errorf("EstimateTokensWithType(mixed) = %d, want > 0", got)
	}
}

// --- EstimateContentsTokens ---

func TestEstimateContentsTokens_Text(t *testing.T) {
	contents := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hello world"}}},
	}
	got := EstimateContentsTokens(contents)
	if got <= 0 {
		t.Errorf("EstimateContentsTokens = %d, want > 0", got)
	}
}

func TestEstimateContentsTokens_FunctionCall(t *testing.T) {
	contents := []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "read", Args: map[string]any{"file_path": "/x.go"}}},
		}},
	}
	got := EstimateContentsTokens(contents)
	if got <= 0 {
		t.Errorf("EstimateContentsTokens(FunctionCall) = %d, want > 0", got)
	}
}

func TestEstimateContentsTokens_FunctionResponse(t *testing.T) {
	contents := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{Name: "read", Response: map[string]any{"content": "file data"}}},
		}},
	}
	got := EstimateContentsTokens(contents)
	if got <= 0 {
		t.Errorf("EstimateContentsTokens(FunctionResponse) = %d, want > 0", got)
	}
}

func TestEstimateContentsTokens_NonStringArg(t *testing.T) {
	contents := []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "bash", Args: map[string]any{"timeout": 30}}},
		}},
	}
	got := EstimateContentsTokens(contents)
	if got <= 0 {
		t.Errorf("EstimateContentsTokens(non-string arg) = %d, want > 0", got)
	}
}

func TestEstimateContentsTokens_NonStringResponse(t *testing.T) {
	contents := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{Name: "bash", Response: map[string]any{"exit_code": 0}}},
		}},
	}
	got := EstimateContentsTokens(contents)
	if got <= 0 {
		t.Errorf("EstimateContentsTokens(non-string resp) = %d, want > 0", got)
	}
}

// --- TokenCounter (NewTokenCounter, GetLimits, GetUsage, CalculateCost, cache) ---

func TestNewTokenCounter_Defaults(t *testing.T) {
	tc := NewTokenCounter(nil, "glm-5", nil)
	if tc == nil {
		t.Fatal("NewTokenCounter returned nil")
	}
	lim := tc.GetLimits()
	if lim.MaxInputTokens != 200000 {
		t.Errorf("MaxInputTokens = %d, want 200000", lim.MaxInputTokens)
	}
	if lim.WarningThreshold != 0.8 {
		t.Errorf("WarningThreshold = %v, want 0.8", lim.WarningThreshold)
	}
}

func TestNewTokenCounter_ConfigOverrides(t *testing.T) {
	cfg := &config.ContextConfig{
		MaxInputTokens:   50000,
		WarningThreshold: 0.9,
	}
	tc := NewTokenCounter(nil, "glm-5", cfg)
	lim := tc.GetLimits()
	if lim.MaxInputTokens != 50000 {
		t.Errorf("MaxInputTokens = %d, want 50000 (config override)", lim.MaxInputTokens)
	}
	if lim.WarningThreshold != 0.9 {
		t.Errorf("WarningThreshold = %v, want 0.9 (config override)", lim.WarningThreshold)
	}
}

func TestNewTokenCounter_UnknownModel(t *testing.T) {
	tc := NewTokenCounter(nil, "unknown-model", nil)
	lim := tc.GetLimits()
	if lim.MaxInputTokens != 128000 {
		t.Errorf("MaxInputTokens = %d, want 128000 (default)", lim.MaxInputTokens)
	}
}

func TestGetUsage_Normal(t *testing.T) {
	tc := NewTokenCounter(nil, "glm-5", nil)
	usage := tc.GetUsage(50000) // 25% of 200000
	if usage.InputTokens != 50000 {
		t.Errorf("InputTokens = %d, want 50000", usage.InputTokens)
	}
	if usage.MaxTokens != 200000 {
		t.Errorf("MaxTokens = %d, want 200000", usage.MaxTokens)
	}
	if usage.PercentUsed != 0.25 {
		t.Errorf("PercentUsed = %v, want 0.25", usage.PercentUsed)
	}
	if usage.NearLimit {
		t.Error("NearLimit should be false at 25%")
	}
	if usage.ExceedsLimit {
		t.Error("ExceedsLimit should be false at 25%")
	}
}

func TestGetUsage_NearLimit(t *testing.T) {
	tc := NewTokenCounter(nil, "glm-5", nil)
	usage := tc.GetUsage(170000) // 85% of 200000, > 80% threshold
	if !usage.NearLimit {
		t.Error("NearLimit should be true at 85%")
	}
}

func TestGetUsage_ExceedsLimit(t *testing.T) {
	tc := NewTokenCounter(nil, "glm-5", nil)
	usage := tc.GetUsage(200000) // 100%
	if !usage.ExceedsLimit {
		t.Error("ExceedsLimit should be true at 100%")
	}
}

func TestGetUsage_ZeroMaxInputTokens(t *testing.T) {
	tc := NewTokenCounter(nil, "glm-5", nil)
	// Manually zero out to test the guard
	tc.limits.MaxInputTokens = 0
	usage := tc.GetUsage(100)
	if usage.MaxTokens != 0 {
		t.Errorf("MaxTokens = %d, want 0", usage.MaxTokens)
	}
	if usage.PercentUsed != 0 {
		t.Errorf("PercentUsed = %v, want 0 (guarded)", usage.PercentUsed)
	}
}

func TestCalculateCost(t *testing.T) {
	tc := NewTokenCounter(nil, "glm-5", nil)
	cost := tc.CalculateCost(1000000, 0) // 1M input tokens
	// glm-5 pricing: $1.00/1M input
	if cost != 1.00 {
		t.Errorf("CalculateCost = %v, want 1.00", cost)
	}
}

func TestCalculateCost_WithOutput(t *testing.T) {
	tc := NewTokenCounter(nil, "glm-5", nil)
	cost := tc.CalculateCost(1000000, 1000000)
	// glm-5: $1.00/1M input + $4.00/1M output = $5.00
	if cost != 5.00 {
		t.Errorf("CalculateCost = %v, want 5.00", cost)
	}
}

func TestTokenCounter_InvalidateCache(t *testing.T) {
	tc := NewTokenCounter(nil, "glm-5", nil)
	// Add something to cache manually
	tc.addToCache("hash1", 100)
	if _, ok := tc.getFromCache("hash1"); !ok {
		t.Fatal("cache should have entry before invalidate")
	}
	tc.InvalidateCache()
	if _, ok := tc.getFromCache("hash1"); ok {
		t.Error("cache should be empty after InvalidateCache")
	}
}

func TestTokenCounter_PreservesAccuracyOnCacheHit(t *testing.T) {
	c, err := client.NewAnthropicClient(client.AnthropicConfig{
		Model:   "glm-5.2",
		BaseURL: "https://api.z.ai",
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("NewAnthropicClient: %v", err)
	}
	tc := NewTokenCounter(c, c.GetModel(), nil)
	contents := []*genai.Content{
		genai.NewContentFromText("hello", genai.RoleUser),
	}

	_, firstEstimated, err := tc.CountContentsWithAccuracy(context.Background(), contents)
	if err != nil {
		t.Fatalf("first count: %v", err)
	}
	_, cachedEstimated, err := tc.CountContentsWithAccuracy(context.Background(), contents)
	if err != nil {
		t.Fatalf("cached count: %v", err)
	}
	if !firstEstimated || !cachedEstimated {
		t.Fatalf("estimate flag lost across cache: first=%v cached=%v", firstEstimated, cachedEstimated)
	}
}

func TestTokenCounter_SetClientPreservesConfiguredLimit(t *testing.T) {
	first := testkit.NewMockClient()
	first.SetModel("glm-5.2")
	tc := NewTokenCounter(first, first.GetModel(), &config.ContextConfig{
		MaxInputTokens:   50_000,
		WarningThreshold: 0.7,
	})

	second := testkit.NewMockClient()
	second.SetModel("kimi-k2.6")
	tc.SetClient(second)
	limits := tc.GetLimits()
	if limits.MaxInputTokens != 50_000 {
		t.Fatalf("MaxInputTokens = %d after client swap, want configured 50000", limits.MaxInputTokens)
	}
	if limits.WarningThreshold != 0.7 {
		t.Fatalf("WarningThreshold = %v after client swap, want 0.7", limits.WarningThreshold)
	}
}

func TestTokenCounter_AddToCacheExistingKey(t *testing.T) {
	tc := NewTokenCounter(nil, "glm-5", nil)
	tc.addToCache("hash1", 100)
	tc.addToCache("hash1", 200) // update existing
	count, ok := tc.getFromCache("hash1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if count != 200 {
		t.Errorf("count = %d, want 200 (updated)", count)
	}
}

func TestTokenCounter_CacheLRUEviction(t *testing.T) {
	tc := NewTokenCounter(nil, "glm-5", nil)
	tc.maxCache = 2
	tc.addToCache("h1", 10)
	tc.addToCache("h2", 20)
	// Access h1 to make it more recent
	_, _ = tc.getFromCache("h1")
	// Add h3 → should evict h2 (LRU)
	tc.addToCache("h3", 30)
	if _, ok := tc.getFromCache("h2"); ok {
		t.Error("h2 should have been evicted")
	}
	if _, ok := tc.getFromCache("h1"); !ok {
		t.Error("h1 should still be present")
	}
}

func TestTokenCounter_CountContents_CacheHit(t *testing.T) {
	// With nil client, CountContents would panic on API call,
	// but a cache hit should return before reaching the client.
	tc := NewTokenCounter(nil, "glm-5", nil)
	contents := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hello"}}},
	}
	hash := tc.hashContents(contents)
	tc.addToCache(hash, 42)

	count, err := tc.CountContents(context.Background(), contents)
	if err != nil {
		t.Fatalf("CountContents cache hit error: %v", err)
	}
	if count != 42 {
		t.Errorf("CountContents = %d, want 42 (cached)", count)
	}
}

func TestTokenCounter_hashContents_Stable(t *testing.T) {
	tc := NewTokenCounter(nil, "glm-5", nil)
	contents := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hello"}}},
	}
	h1 := tc.hashContents(contents)
	h2 := tc.hashContents(contents)
	if h1 != h2 {
		t.Errorf("hashContents not stable: %q vs %q", h1, h2)
	}
}

func TestTokenCounter_hashContents_DifferentContent(t *testing.T) {
	tc := NewTokenCounter(nil, "glm-5", nil)
	a := []*genai.Content{{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "a"}}}}
	b := []*genai.Content{{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "b"}}}}
	if tc.hashContents(a) == tc.hashContents(b) {
		t.Error("different content should produce different hashes")
	}
}

// --- detectContentType ---

func TestDetectContentType_Prose(t *testing.T) {
	if got := detectContentType("hello world this is prose text"); got != ContentTypeProse {
		t.Errorf("detectContentType(prose) = %v, want ContentTypeProse", got)
	}
}

func TestDetectContentType_Code(t *testing.T) {
	code := "func main() { x := 10; if x > 0 { fmt.Println(x) } }"
	if got := detectContentType(code); got != ContentTypeCode {
		t.Errorf("detectContentType(code) = %v, want ContentTypeCode", got)
	}
}

func TestDetectContentType_JSON(t *testing.T) {
	jsonText := `{"key": "value", "nested": {"a": 1}}`
	if got := detectContentType(jsonText); got != ContentTypeJSON {
		t.Errorf("detectContentType(json) = %v, want ContentTypeJSON", got)
	}
}

// --- containsCamelCase ---

func TestContainsCamelCase(t *testing.T) {
	if !containsCamelCase("helloWorld") {
		t.Error("containsCamelCase('helloWorld') should be true")
	}
}

func TestContainsCamelCase_NoCamel(t *testing.T) {
	if containsCamelCase("hello") {
		t.Error("containsCamelCase('hello') should be false")
	}
}

func TestContainsCamelCase_AllUpper(t *testing.T) {
	// All upper, no lower→upper transition
	if containsCamelCase("HELLO") {
		t.Error("containsCamelCase('HELLO') should be false")
	}
}
