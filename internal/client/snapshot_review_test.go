package client

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestUnchangedSnapshotProjectionMatchesSend(t *testing.T) {
	for _, provider := range []string{"deepseek", "glm"} {
		t.Run(provider, func(t *testing.T) {
			c := &AnthropicClient{config: AnthropicConfig{Provider: provider}}
			c.appendStableTurnContext(c.convertHistoryToMessages(nil, "shared opening"), "UNCHANGED SNAPSHOT", true)
			history := []*genai.Content{genai.NewContentFromText("shared opening", genai.RoleUser), genai.NewContentFromText("reply", genai.RoleModel)}
			projected := c.appendStableTurnContext(c.convertHistoryToMessages(history, "new user message"), "UNCHANGED SNAPSHOT", false)
			if len(c.turnContextPlacements) != 1 {
				t.Fatal("projection committed state")
			}
			contents := append(history, genai.NewContentFromText("new user message", genai.RoleUser))
			counted := c.projectHistoryForCount(contents, "UNCHANGED SNAPSHOT")
			sent := c.appendStableTurnContext(c.convertHistoryToMessages(history, "new user message"), "UNCHANGED SNAPSHOT", true)
			if marshalString(t, projected) != marshalString(t, sent) {
				t.Fatal("unchanged snapshot renders differently in count and send")
			}
			if marshalString(t, counted) != marshalString(t, sent) {
				t.Fatal("real count pipeline differs from sent bytes")
			}
		})
	}
}

func TestRetainedSnapshotsHaveImmutableHistoricalLabels(t *testing.T) {
	c := &AnthropicClient{config: AnthropicConfig{Provider: "glm"}}
	first := c.appendStableTurnContext(c.convertHistoryToMessages(nil, "opening"), "OLD STATE", true)
	firstBytes := marshalString(t, first[0])
	history := []*genai.Content{genai.NewContentFromText("opening", genai.RoleUser), genai.NewContentFromText("reply", genai.RoleModel)}
	next := c.appendStableTurnContext(c.convertHistoryToMessages(history, "next turn"), "CURRENT STATE", true)
	if marshalString(t, next[0]) != firstBytes {
		t.Fatal("historical label rewrote an earlier message")
	}
	for i, label := range map[int]string{0: "Snapshot 1", 2: "Snapshot 2"} {
		text := marshalString(t, next[i])
		if !strings.Contains(text, label) || !strings.Contains(text, "historical point-in-time") || !strings.Contains(text, "newer snapshots supersede older ones") || !strings.Contains(text, "Later messages and tool results take precedence") {
			t.Fatalf("snapshot is indistinguishable from current state: %s", text)
		}
	}
}
