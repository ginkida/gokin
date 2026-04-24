package client

import (
	"testing"

	"google.golang.org/genai"
)

// requiresThinkingReplay gates the thinking-replay requirement per
// provider. Before v0.71.4 this wasn't explicit and we silently stripped
// thinking blocks, causing DeepSeek to 400 with "content[].thinking
// must be passed back to the API."
func TestRequiresThinkingReplay_ProviderMatrix(t *testing.T) {
	cases := []struct {
		name           string
		baseURL        string
		enableThinking bool
		want           bool
	}{
		// DeepSeek with thinking — strict.
		{"deepseek + thinking", "https://api.deepseek.com/anthropic", true, true},
		// Anthropic native — strict.
		{"anthropic native + thinking", DefaultAnthropicBaseURL, true, true},
		{"empty url (anthropic default) + thinking", "", true, true},
		// Kimi — tolerates missing thinking blocks.
		{"kimi + thinking", "https://api.kimi.com/coding", true, false},
		// MiniMax — tolerates.
		{"minimax + thinking", "https://api.minimax.io/anthropic", true, false},
		// GLM — tolerates.
		{"glm + thinking", "https://api.z.ai/api/anthropic", true, false},
		// Thinking disabled — never need replay regardless of provider.
		{"deepseek without thinking", "https://api.deepseek.com/anthropic", false, false},
		{"anthropic without thinking", DefaultAnthropicBaseURL, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ac := &AnthropicClient{
				config: AnthropicConfig{
					BaseURL:        c.baseURL,
					EnableThinking: c.enableThinking,
				},
			}
			if got := ac.requiresThinkingReplay(); got != c.want {
				t.Errorf("requiresThinkingReplay() = %v, want %v", got, c.want)
			}
		})
	}
}

// canSerialiseAssistantForProvider: when the provider demands thinking
// replay and the assistant message has an unsigned thought, drop the
// entire message. Without this, we would silently strip the thought
// and the provider would 400 on the now-incomplete message.
func TestCanSerialiseAssistantForProvider_DropsUnsignedThinkingForStrictProvider(t *testing.T) {
	deepseek := &AnthropicClient{
		config: AnthropicConfig{
			BaseURL:        "https://api.deepseek.com/anthropic",
			EnableThinking: true,
		},
	}

	// Unsigned thought + tool_use — DeepSeek-strict would reject if
	// we stripped the thought and kept the tool_use. Must drop whole turn.
	parts := []*genai.Part{
		{Thought: true, Text: "let me think about this"}, // no ThoughtSignature
		{FunctionCall: &genai.FunctionCall{ID: "call_1", Name: "read"}},
	}
	if deepseek.canSerialiseAssistantForProvider(parts) {
		t.Error("DeepSeek-strict must refuse an unsigned-thought message (drop whole turn)")
	}

	// Signed thought + tool_use — fine to serialise.
	signedParts := []*genai.Part{
		{Thought: true, Text: "let me think", ThoughtSignature: []byte("sig-abc")},
		{FunctionCall: &genai.FunctionCall{ID: "call_2", Name: "read"}},
	}
	if !deepseek.canSerialiseAssistantForProvider(signedParts) {
		t.Error("signed-thought message must pass")
	}
}

// Same input on a tolerant provider (Kimi) must still serialise —
// Kimi accepts messages without thinking-block replay.
func TestCanSerialiseAssistantForProvider_TolerantProviderAllowsUnsignedThinking(t *testing.T) {
	kimi := &AnthropicClient{
		config: AnthropicConfig{
			BaseURL:        "https://api.kimi.com/coding",
			EnableThinking: true,
		},
	}
	parts := []*genai.Part{
		{Thought: true, Text: "let me think"}, // no signature
		{FunctionCall: &genai.FunctionCall{ID: "call_1", Name: "read"}},
	}
	if !kimi.canSerialiseAssistantForProvider(parts) {
		t.Error("Kimi should tolerate unsigned thinking (tool_use still serialisable)")
	}
}

// Text-only assistant turn with no thinking at all — always serialisable.
func TestCanSerialiseAssistantForProvider_PlainTextAlwaysOK(t *testing.T) {
	for _, base := range []string{
		DefaultAnthropicBaseURL,
		"https://api.deepseek.com/anthropic",
		"https://api.kimi.com/coding",
	} {
		ac := &AnthropicClient{config: AnthropicConfig{BaseURL: base, EnableThinking: true}}
		parts := []*genai.Part{{Text: "I'll help you with that."}}
		if !ac.canSerialiseAssistantForProvider(parts) {
			t.Errorf("plain text message should serialise for %s", base)
		}
	}
}

// Empty parts slice — fall through to hasSerializableAssistantParts which
// rightly returns false.
func TestCanSerialiseAssistantForProvider_EmptyPartsUnserialisable(t *testing.T) {
	ac := &AnthropicClient{config: AnthropicConfig{BaseURL: DefaultAnthropicBaseURL, EnableThinking: true}}
	if ac.canSerialiseAssistantForProvider(nil) {
		t.Error("nil parts should not serialise")
	}
	if ac.canSerialiseAssistantForProvider([]*genai.Part{}) {
		t.Error("empty parts should not serialise")
	}
}
