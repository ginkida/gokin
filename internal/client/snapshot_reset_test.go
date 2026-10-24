package client

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestFallbackActivationCannotReplayPrivateSnapshots(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			requests := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				requests <- string(body)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			t.Cleanup(server.Close)
			alternate, err := NewAnthropicClient(AnthropicConfig{APIKey: "test", BaseURL: server.URL, Provider: "glm", Model: "glm-test"})
			if err != nil {
				t.Fatal(err)
			}
			alternate.appendStableTurnContext(alternate.convertHistoryToMessages(nil, "shared opening"), "PRIVATE-OLD-SNAPSHOT", true)
			primary := &fakeFallbackClientStub{id: "snapshot-primary", model: "mock-model"}
			if streaming {
				primary.sendResp = fallbackTestStream(ResponseChunk{Error: errors.New("provider unavailable")})
			} else {
				primary.sendErr = errors.New("provider unavailable")
			}
			fallback, err := NewFallbackClient([]Client{primary, alternate}, []string{"test-snapshot-primary", "test-snapshot-alternate"})
			if err != nil {
				t.Fatal(err)
			}
			fallback.SetSystemInstruction("stable")
			fallback.SetTurnContext("CURRENT-SNAPSHOT")
			history := []*genai.Content{genai.NewContentFromText("shared opening", genai.RoleUser), genai.NewContentFromText("reply", genai.RoleModel)}
			stream, err := fallback.SendMessageWithHistory(t.Context(), history, "continue")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stream.CollectContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			body := <-requests
			if strings.Contains(body, "PRIVATE-OLD-SNAPSHOT") || !strings.Contains(body, "CURRENT-SNAPSHOT") {
				t.Fatal("activated fallback replayed private snapshots")
			}
		})
	}
}

func TestSnapshotResetTraversesFallbackAndModelOverrides(t *testing.T) {
	base, err := NewAnthropicClient(AnthropicConfig{APIKey: "test", BaseURL: DefaultGLMBaseURL, Provider: "glm", Model: "glm-test"})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := WithRequestSettings(base, func() RequestSettings { return RequestSettings{Model: "glm-other"} }).(*requestSettingsClient)
	override := wrapped.selected(wrapped.settings()).(*AnthropicClient)
	other := base.WithModel("glm-alternate")
	fallback, err := NewFallbackClient([]Client{wrapped, other}, []string{"test-snapshot-wrapper", "test-snapshot-other"})
	if err != nil {
		t.Fatal(err)
	}
	for _, resetPosition := range []bool{false, true} {
		for _, leaf := range []*AnthropicClient{base, override} {
			leaf.appendStableTurnContext(leaf.convertHistoryToMessages(nil, "shared opening"), "PRIVATE", true)
		}
		if resetPosition {
			fallback.setCurrent(1)
			fallback.ResetFallbackPosition()
		} else {
			ResetConversationCache(fallback)
		}
		for _, leaf := range []*AnthropicClient{base, override} {
			if len(leaf.turnContextPlacements) != 0 {
				t.Fatal("decorator retained a private snapshot cache")
			}
		}
	}
}

func TestModelAndSessionClonesDoNotInheritSnapshotPlacements(t *testing.T) {
	c, err := NewAnthropicClient(AnthropicConfig{APIKey: "test", BaseURL: DefaultGLMBaseURL, Provider: "glm", Model: "glm-test"})
	if err != nil {
		t.Fatal(err)
	}
	c.appendStableTurnContext(c.convertHistoryToMessages(nil, "shared opening"), "PRIVATE", true)
	for _, clone := range []Client{c.WithModel("glm-other"), c.WithModel(""), c.cloneForSession()} {
		if clone == c || len(clone.(*AnthropicClient).turnContextPlacements) != 0 {
			t.Fatal("clone inherited parent snapshot placements")
		}
	}
	if len(c.turnContextPlacements) != 1 {
		t.Fatal("cloning reset the parent's cache")
	}
}
