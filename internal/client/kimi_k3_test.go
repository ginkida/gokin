package client

import "testing"

// Kimi K3 registered to GLM-flagship depth (v0.100.95 "kimi support at GLM
// level"). This pins the client-package registration points; the token
// limits/pricing (internal/context) and capability tier (internal/router)
// are pinned in their own packages to avoid an import cycle.
func TestKimiK3ClientRegistration(t *testing.T) {
	info, ok := GetModelInfo("k3")
	if !ok {
		t.Fatal("k3 missing from client.AvailableModels")
	}
	if info.Provider != "kimi" {
		t.Fatalf("k3 provider = %q, want kimi", info.Provider)
	}
	if info.BaseURL != DefaultKimiBaseURL {
		t.Fatalf("k3 base URL = %q, want the Kimi coding endpoint", info.BaseURL)
	}

	// The K2.7 coding models stay available on every subscription tier.
	for _, id := range []string{"kimi-for-coding", "kimi-for-coding-highspeed"} {
		if _, ok := GetModelInfo(id); !ok {
			t.Fatalf("%s missing from AvailableModels", id)
		}
	}

	profile := GetModelProfile("k3")
	if profile.Family != "kimi" {
		t.Fatalf("k3 family = %q, want kimi", profile.Family)
	}
	if profile.ContextWindow != 1048576 {
		t.Fatalf("k3 context window = %d, want 1048576", profile.ContextWindow)
	}
	if !profile.SupportsTools || !profile.IsCoding {
		t.Fatalf("k3 profile = %+v, want tools + coding", profile)
	}

	// K3 always reasons and emits signed thinking — the factory must
	// auto-enable Extended Thinking for it.
	if !supportsKimiThinking("k3") {
		t.Fatal("supportsKimiThinking(k3) = false, want true")
	}
	// A K3-family variant should also match; an unrelated model must not.
	if !supportsKimiThinking("k3-1m") {
		t.Fatal("supportsKimiThinking(k3-1m) = false, want true")
	}
	if supportsKimiThinking("glm-5.2") {
		t.Fatal("supportsKimiThinking(glm-5.2) = true, want false")
	}
}

// kimiAlwaysOnReasoningModel guards the K3 family from the
// {"type":"disabled"} thinking marker (v0.100.101 field report: the marker
// put K3 into a degraded no-thinking mode that re-emitted its narration
// verbatim before every tool call). Pin both directions — a silent false on
// a K3 spelling re-introduces that loop, a silent true on a non-K3 model
// would leave its thinking un-disableable.
func TestKimiAlwaysOnReasoningModel(t *testing.T) {
	alwaysOn := []string{"k3", "K3", "k3-1m", "kimi-k3", "kimi-k3-0905", " kimi-k3 "}
	for _, m := range alwaysOn {
		if !kimiAlwaysOnReasoningModel(m) {
			t.Errorf("kimiAlwaysOnReasoningModel(%q) = false, want true", m)
		}
	}
	notAlwaysOn := []string{"", "kimi-for-coding", "kimi-for-coding-highspeed", "k2", "k2.7", "k30", "glm-5.2"}
	for _, m := range notAlwaysOn {
		if kimiAlwaysOnReasoningModel(m) {
			t.Errorf("kimiAlwaysOnReasoningModel(%q) = true, want false", m)
		}
	}
}
