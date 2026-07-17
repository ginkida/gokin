package client

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/genai"
)

// The context-bar accuracy fix, part 3 (v0.100.92 field report «≈56.5K все
// еще неточно»): the local token estimate must count the ACTUAL serialized
// request. GLM preserved-thinking now serializes unsigned reasoning_content,
// so its estimator must count it; tolerant non-GLM providers still drop
// unsigned thoughts and must not count phantom tokens.
func TestEstimateTokens_MatchesProviderSpecificUnsignedThinkingReplay(t *testing.T) {
	c := &AnthropicClient{config: AnthropicConfig{Model: "glm-5.2", BaseURL: "https://api.z.ai/api/anthropic"}}

	bigThinking := strings.Repeat("рассуждение о башне токенов ", 400) // ~11K runes
	base := []*genai.Content{
		genai.NewContentFromText("check the repo state", genai.RoleUser),
		{Role: genai.RoleModel, Parts: []*genai.Part{
			{Text: "Here is the answer."},
		}},
	}
	withUnsignedThinking := []*genai.Content{
		base[0],
		{Role: genai.RoleModel, Parts: []*genai.Part{
			{Text: bigThinking, Thought: true}, // unsigned → dropped from the wire
			{Text: "Here is the answer."},
		}},
	}

	plain, err := c.estimateTokens(base, "glm-5.2", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	inflated, err := c.estimateTokens(withUnsignedThinking, "glm-5.2", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if inflated.TotalTokens < plain.TotalTokens+1000 {
		t.Fatalf("GLM preserved reasoning must count toward estimate: plain=%d replayed=%d",
			plain.TotalTokens, inflated.TotalTokens)
	}

	// Kimi accepts omission of unsigned thoughts and still uses the legacy drop
	// behavior, so the same history must not inflate its serialized estimate.
	kimi := &AnthropicClient{config: AnthropicConfig{Model: "kimi-for-coding", BaseURL: DefaultKimiBaseURL}}
	kimiPlain, err := kimi.estimateTokens(base, "kimi-for-coding", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	kimiDropped, err := kimi.estimateTokens(withUnsignedThinking, "kimi-for-coding", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if diff := kimiDropped.TotalTokens - kimiPlain.TotalTokens; diff > 50 {
		t.Fatalf("dropped Kimi thinking inflated estimate by %d tokens", diff)
	}

	// SIGNED thinking IS serialized (replayable) and must still be counted.
	withSignedThinking := []*genai.Content{
		base[0],
		{Role: genai.RoleModel, Parts: []*genai.Part{
			{Text: bigThinking, Thought: true, ThoughtSignature: []byte("sig")},
			{Text: "Here is the answer."},
		}},
	}
	signed, err := c.estimateTokens(withSignedThinking, "glm-5.2", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if signed.TotalTokens < plain.TotalTokens+1000 {
		t.Fatalf("signed thinking must count toward the estimate: plain=%d signed=%d",
			plain.TotalTokens, signed.TotalTokens)
	}
}

// The estimator and the native count_tokens path count the SAME effective
// payload: the synthetic empty/placeholder trailing message is stripped by
// the shared helper.
func TestEstimateTokens_CountsToolTraffic(t *testing.T) {
	c := &AnthropicClient{config: AnthropicConfig{Model: "glm-5.2", BaseURL: "https://api.z.ai/api/anthropic"}}
	history := []*genai.Content{
		genai.NewContentFromText("run the tests", genai.RoleUser),
		{Role: genai.RoleModel, Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "c1", Name: "bash",
				Args: map[string]any{"command": strings.Repeat("go test ./... ", 50)}}},
		}},
		{Role: genai.RoleUser, Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{ID: "c1", Name: "bash",
				Response: map[string]any{"content": strings.Repeat("ok ", 500)}}},
		}},
	}
	resp, err := c.estimateTokens(history, "glm-5.2", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// ~700 chars command + ~1500 chars output ⇒ well above a token-free floor.
	if resp.TotalTokens < 400 {
		t.Fatalf("tool traffic under-counted: %d tokens", resp.TotalTokens)
	}

	// Sanity: the CountTokens entry point still returns the estimate flag on
	// a non-native endpoint.
	if _, isEstimate, err := c.CountTokensWithAccuracy(context.Background(), history); err != nil || !isEstimate {
		t.Fatalf("CountTokensWithAccuracy = estimate:%v err:%v, want estimate:true", isEstimate, err)
	}
}
