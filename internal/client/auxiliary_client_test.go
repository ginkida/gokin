package client

import (
	"testing"

	"google.golang.org/genai"
)

type selfReturningAuxiliaryClient struct{ *fakeClient }

func (c *selfReturningAuxiliaryClient) CloneForAuxiliaryClient() (Client, bool) {
	return c, true
}

func TestCloneForAuxiliaryStripsForegroundRequestState(t *testing.T) {
	base, err := NewAnthropicClient(AnthropicConfig{
		APIKey:         "test-key",
		BaseURL:        "https://example.test/anthropic",
		Provider:       "kimi",
		Model:          "k3",
		ThinkingBudget: 8192,
	})
	if err != nil {
		t.Fatal(err)
	}
	base.SetTools([]*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "read"}}}})
	base.SetSystemInstruction("foreground system prompt")
	base.SetTurnContext("foreground turn context")
	base.SetStatusCallback(&DefaultStatusCallback{})
	base.setDirectHealthTracking(true)

	got, isolated := CloneForAuxiliary(base)
	if !isolated {
		t.Fatal("production Anthropic client was not isolated")
	}
	aux, ok := got.(*AnthropicClient)
	if !ok || aux == base {
		t.Fatalf("auxiliary client = %T %p, want a distinct AnthropicClient", got, got)
	}

	aux.mu.RLock()
	tools := aux.tools
	system := aux.systemInstruction
	turn := aux.turnContext
	thinking := aux.config.ThinkingBudget
	status := aux.statusCallback
	trackHealth := aux.directHealthTracking
	aux.mu.RUnlock()
	if len(tools) != 0 || system != "" || turn != "" || thinking != 0 || status != nil || trackHealth {
		t.Fatalf("auxiliary request state leaked: tools=%d system=%q turn=%q thinking=%d status=%T health=%v",
			len(tools), system, turn, thinking, status, trackHealth)
	}

	base.mu.RLock()
	baseTools := len(base.tools)
	baseThinking := base.config.ThinkingBudget
	baseSystem := base.systemInstruction
	base.mu.RUnlock()
	if baseTools != 1 || baseThinking != 8192 || baseSystem == "" {
		t.Fatalf("base client was mutated: tools=%d thinking=%d system=%q", baseTools, baseThinking, baseSystem)
	}
}

func TestCloneForAuxiliaryDoesNotMutateUnsupportedClient(t *testing.T) {
	base := &fakeClient{}
	got, isolated := CloneForAuxiliary(base)
	if isolated || got != base {
		t.Fatalf("unsupported client clone = (%T, %v), want original/false", got, isolated)
	}
}

func TestCloneForAuxiliaryRejectsSharedClone(t *testing.T) {
	base := &selfReturningAuxiliaryClient{fakeClient: &fakeClient{id: "shared"}}
	got, isolated := CloneForAuxiliary(base)
	if isolated || got != base {
		t.Fatalf("shared clone = (%T, %v), want original/false", got, isolated)
	}
}

func TestCloneForAuxiliaryFailsClosedForUncloneableFallbackChild(t *testing.T) {
	child := &fakeClient{id: "shared-child"} // WithModel deliberately returns itself.
	fallback, err := NewFallbackClient([]Client{child}, []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	got, isolated := CloneForAuxiliary(fallback)
	if isolated || got != fallback {
		t.Fatalf("uncloneable fallback = (%T, %v), want original/false", got, isolated)
	}
}

func TestCloneForAuxiliaryIsolatesFallbackChainAndClearsCallbacks(t *testing.T) {
	first := &fakeFallbackClientStub{id: "first", model: "glm-5.2"}
	second := &fakeFallbackClientStub{id: "second", model: "k3"}
	fallback, err := NewFallbackClient(
		[]Client{first, second}, []string{"glm", "kimi"})
	if err != nil {
		t.Fatal(err)
	}
	tools := []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "write"}}}}
	callback := &DefaultStatusCallback{}
	fallback.SetTools(tools)
	fallback.SetSystemInstruction("foreground system")
	fallback.SetTurnContext("foreground turn")
	fallback.SetThinkingBudget(8192)
	fallback.SetStatusCallback(callback)

	got, isolated := CloneForAuxiliary(fallback)
	if !isolated {
		t.Fatal("fallback client was not isolated")
	}
	aux, ok := got.(*FallbackClient)
	if !ok || aux == fallback {
		t.Fatalf("auxiliary fallback = %T %p, want distinct *FallbackClient", got, got)
	}
	for i, child := range aux.clients {
		stub, ok := child.(*fakeFallbackClientStub)
		if !ok {
			t.Fatalf("auxiliary child %d = %T", i, child)
		}
		if len(stub.tools) != 0 || stub.sysInstr != "" || stub.turnCtx != "" ||
			stub.thinkBudget != 0 || stub.statusCB != nil {
			t.Fatalf("auxiliary child %d leaked state: tools=%d system=%q turn=%q thinking=%d callback=%T",
				i, len(stub.tools), stub.sysInstr, stub.turnCtx, stub.thinkBudget, stub.statusCB)
		}
	}
	for i, child := range []*fakeFallbackClientStub{first, second} {
		if len(child.tools) != 1 || child.sysInstr == "" || child.turnCtx == "" ||
			child.thinkBudget != 8192 || child.statusCB != callback {
			t.Fatalf("foreground child %d was mutated: %+v", i, child)
		}
	}
}

func TestCloneForAuxiliaryFallbackSharesBuiltInProviderTransports(t *testing.T) {
	first, err := NewAnthropicClient(AnthropicConfig{
		APIKey: "first", BaseURL: "https://first.example.test", Provider: "glm", Model: "glm-5.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewAnthropicClient(AnthropicConfig{
		APIKey: "second", BaseURL: "https://second.example.test", Provider: "kimi", Model: "k3",
	})
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := NewFallbackClient(
		[]Client{first, second}, []string{"glm", "kimi"})
	if err != nil {
		t.Fatal(err)
	}
	got, isolated := CloneForAuxiliary(fallback)
	if !isolated {
		t.Fatal("built-in fallback was not isolated")
	}
	aux := got.(*FallbackClient)
	for i, original := range []*AnthropicClient{first, second} {
		cloned, ok := aux.clients[i].(*AnthropicClient)
		if !ok || cloned == original {
			t.Fatalf("child %d clone = %T %p, original = %p", i, aux.clients[i], aux.clients[i], original)
		}
		if cloned.httpClient != original.httpClient {
			t.Fatalf("child %d opened a fresh HTTP transport", i)
		}
	}
}
