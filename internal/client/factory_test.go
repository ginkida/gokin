package client

import (
	"context"
	"reflect"
	"testing"

	"gokin/internal/config"

	"google.golang.org/genai"
)

func TestFallbackModelForProvider_UsesProviderNativeDefaults(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{provider: "glm", want: "glm-5.2"},

		{provider: "deepseek", want: "deepseek-v4-pro"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			if got := fallbackModelForProvider("glm", tt.provider, "glm-5.2"); got != tt.want {
				t.Fatalf("model = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFallbackConfigForProvider_CustomEndpointIsPrimaryOnly(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model.CustomBaseURL = "https://glm-gateway.example/v1"

	if got := fallbackConfigForProvider(cfg, "glm", "glm"); got != cfg {
		t.Fatal("primary provider should keep original config")
	}
	fallback := fallbackConfigForProvider(cfg, "glm", "deepseek")
	if fallback == cfg || fallback.Model.CustomBaseURL != "" {
		t.Fatalf("fallback config = %#v, want independent config with default endpoint", fallback)
	}
	if cfg.Model.CustomBaseURL != "https://glm-gateway.example/v1" {
		t.Fatalf("original config was mutated: %q", cfg.Model.CustomBaseURL)
	}
}

func TestFallbackProviderCandidates_NormalizesAndDeduplicates(t *testing.T) {
	got := fallbackProviderCandidates(" GLM ", []string{"DEEPSEEK", " deepseek ", "GLM", ""})
	want := []string{"glm", "deepseek"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("providers = %v, want %v", got, want)
	}
}

func TestGetOrCreateClient_DistinguishesEndpointConfiguration(t *testing.T) {
	ClosePool()
	defer ClosePool()
	for _, env := range config.GetProvider("deepseek").EnvVars {
		t.Setenv(env, "")
	}

	custom := config.DefaultConfig()
	custom.API.DeepSeekKey = "sk-deepseek-pool-fingerprint-test"
	custom.Model.CustomBaseURL = "https://proxy.example/anthropic"
	first, err := getOrCreateClient(context.Background(), custom, "deepseek", "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("custom client: %v", err)
	}

	native := custom.Clone()
	native.Model.CustomBaseURL = ""
	second, err := getOrCreateClient(context.Background(), native, "deepseek", "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("native client: %v", err)
	}
	if first == second {
		t.Fatal("pool reused a client built for a different endpoint")
	}
	if got := first.(*AnthropicClient).config.BaseURL; got != custom.Model.CustomBaseURL {
		t.Fatalf("custom BaseURL = %q", got)
	}
	if got := second.(*AnthropicClient).config.BaseURL; got != DefaultDeepSeekBaseURL {
		t.Fatalf("native BaseURL = %q, want %q", got, DefaultDeepSeekBaseURL)
	}

	again, err := getOrCreateClient(context.Background(), native, "deepseek", "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("identical config clone: %v", err)
	}
	if again == second {
		t.Fatal("identical config reused the same mutable client wrapper")
	}
	secondAnthropic := second.(*AnthropicClient)
	againAnthropic := again.(*AnthropicClient)
	if againAnthropic.httpClient != secondAnthropic.httpClient {
		t.Fatal("session clones did not reuse the pooled HTTP transport")
	}
}

func TestGetOrCreateClient_IsolatesMutableSessionState(t *testing.T) {
	ClosePool()
	defer ClosePool()
	for _, env := range config.GetProvider("deepseek").EnvVars {
		t.Setenv(env, "")
	}
	cfg := config.DefaultConfig()
	cfg.API.DeepSeekKey = "sk-deepseek-session-isolation-test"

	firstRaw, err := getOrCreateClient(context.Background(), cfg, "deepseek", "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("first client: %v", err)
	}
	first := firstRaw.(*AnthropicClient)
	first.SetSystemInstruction("session-one-only")
	first.SetTurnContext("private-turn-context")
	first.SetThinkingBudget(0)
	first.SetTools([]*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "private_tool"}}}})

	secondRaw, err := getOrCreateClient(context.Background(), cfg, "deepseek", "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("second client: %v", err)
	}
	second := secondRaw.(*AnthropicClient)
	second.mu.RLock()
	defer second.mu.RUnlock()
	if second.systemInstruction != "" || second.turnContext != "" || len(second.tools) != 0 {
		t.Fatalf("second session inherited mutable state: system=%q turn=%q tools=%d",
			second.systemInstruction, second.turnContext, len(second.tools))
	}
	if !second.config.EnableThinking || second.config.ThinkingBudget == 0 {
		t.Fatalf("second session inherited disabled thinking: %+v", second.config)
	}
	if first.httpClient != second.httpClient {
		t.Fatal("isolated session wrappers should still share the pooled transport")
	}
}

func TestFallbackFactory_UsesFreshUnpooledOwnedChildren(t *testing.T) {
	ClosePool()
	defer ClosePool()

	cfg := config.DefaultConfig()
	cfg.Model.MaxPoolSize = 1
	cfg.Model.FallbackProviders = []string{"deepseek"}

	// Keep the one available pool slot occupied. Building or closing fallback
	// chains must neither evict nor close this independently owned client.
	pool := GetPool(cfg)
	pooled := &fakeClient{id: "pooled-single-client"}
	pool.Put("sentinel", "sentinel-model", pooled)

	var generation []*fakeClient
	createFresh := func(_ context.Context, _ *config.Config, provider, model string) (Client, error) {
		client := &fakeClient{id: provider + ":" + model}
		generation = append(generation, client)
		return client, nil
	}

	firstRaw, err := newFallbackClientFromConfigWithFactory(
		context.Background(), cfg, "glm", "glm-5.2", createFresh,
	)
	if err != nil {
		t.Fatalf("first fallback: %v", err)
	}
	first := firstRaw.(*FallbackClient)
	firstChildren := append([]*fakeClient(nil), generation...)
	generation = nil

	secondRaw, err := newFallbackClientFromConfigWithFactory(
		context.Background(), cfg, "glm", "glm-5.2", createFresh,
	)
	if err != nil {
		t.Fatalf("second fallback: %v", err)
	}
	second := secondRaw.(*FallbackClient)
	secondChildren := append([]*fakeClient(nil), generation...)

	if len(firstChildren) != 2 || len(secondChildren) != 2 {
		t.Fatalf("child counts = %d and %d, want 2 and 2", len(firstChildren), len(secondChildren))
	}
	for i := range firstChildren {
		if firstChildren[i] == secondChildren[i] {
			t.Fatalf("fallback child %d was shared between wrappers", i)
		}
	}
	if got := pool.Size(); got != 1 {
		t.Fatalf("fallback construction changed max-size pool: size=%d, want 1", got)
	}
	if got, ok := pool.Get("sentinel", "sentinel-model"); !ok || got != pooled {
		t.Fatal("fallback construction evicted the pooled single client")
	}

	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	for i, child := range firstChildren {
		if !child.closed {
			t.Errorf("first child %d was not closed", i)
		}
	}
	for i, child := range secondChildren {
		if child.closed {
			t.Errorf("closing first wrapper closed second child %d", i)
		}
	}
	if pooled.closed {
		t.Error("closing fallback wrapper closed independently pooled client")
	}

	pool.Close()
	if !pooled.closed {
		t.Error("pool Close did not close its own client")
	}
	for i, child := range secondChildren {
		if child.closed {
			t.Errorf("pool Close closed unpooled fallback child %d", i)
		}
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestNormalizeThinkingBudget nails down the helper's contract separately so
// future provider factories have a single invariant to follow.
func TestNormalizeThinkingBudget(t *testing.T) {
	const def = defaultDeepSeekThinkingBudget
	cases := []struct {
		name   string
		budget int32
		want   int32
	}{
		{"zero_uses_default", 0, def},
		{"below_min_uses_default", 500, def},
		{"negative_uses_default", -1, def},
		{"at_min_preserved", 1024, 1024},
		{"in_range_preserved", 4096, 4096},
		{"at_max_preserved", 65536, 65536},
		{"above_max_uses_default", 100000, def},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeThinkingBudget(tc.budget, def); got != tc.want {
				t.Errorf("normalizeThinkingBudget(%d) = %d, want %d", tc.budget, got, tc.want)
			}
		})
	}
}
