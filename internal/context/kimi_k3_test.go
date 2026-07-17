package context

import "testing"

// Kimi K3 limits + pricing (v0.100.95 "kimi support at GLM level"). K3 has
// the 1M window and its own published rates including the cache-read discount.
func TestKimiK3LimitsAndPricing(t *testing.T) {
	limits := GetModelLimits("k3")
	if limits.MaxInputTokens != 1048576 {
		t.Fatalf("k3 max input = %d, want 1048576", limits.MaxInputTokens)
	}

	// The bare-"k3" key must not be fuzzy-matched by unrelated models: the
	// 256K K2.7 coding model keeps its own limit.
	if l := GetModelLimits("kimi-for-coding"); l.MaxInputTokens != 262144 {
		t.Fatalf("kimi-for-coding max input = %d, want 262144", l.MaxInputTokens)
	}

	p, ok := DefaultPricing["k3"]
	if !ok {
		t.Fatal("k3 has no pricing entry")
	}
	if p.InputCostPer1M != 3.00 || p.OutputCostPer1M != 15.00 {
		t.Fatalf("k3 pricing = %+v, want $3 in / $15 out", p)
	}
	if p.CachedInputCostPer1M != 0.30 {
		t.Fatalf("k3 cache-read pricing = %v, want 0.30", p.CachedInputCostPer1M)
	}
	k27 := DefaultPricing["kimi-for-coding"]
	if k27.CachedInputCostPer1M != 0.19 {
		t.Fatalf("K2.7 cache-read pricing = %v, want 0.19", k27.CachedInputCostPer1M)
	}
}
