package client

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestTurnContextPlacementsFollowHistoryAndStaySessionLocal(t *testing.T) {
	for _, provider := range []string{"deepseek", "glm"} {
		t.Run(provider, func(t *testing.T) {
			c := &AnthropicClient{config: AnthropicConfig{Provider: provider}}
			history := []*genai.Content{genai.NewContentFromText("first task", genai.RoleUser), genai.NewContentFromText("first answer", genai.RoleModel)}
			initial := c.appendStableTurnContext(c.convertHistoryToMessages(nil, "first task"), "OLD SNAPSHOT", true)
			if len(c.turnContextPlacements) != 1 || !strings.Contains(marshalString(t, initial), "OLD SNAPSHOT") {
				t.Fatal("initial snapshot not recorded")
			}
			counted := c.projectHistoryForCount(history, "OLD SNAPSHOT")
			if strings.Count(marshalString(t, counted), "OLD SNAPSHOT") != 1 {
				t.Fatal("count projection duplicated the existing snapshot on synthetic Continue")
			}
			projected := c.appendStableTurnContext(c.convertHistoryToMessages(history, "next task"), "NEW SNAPSHOT", false)
			if len(c.turnContextPlacements) != 1 {
				t.Fatal("token-count projection mutated placement state")
			}
			sent := c.appendStableTurnContext(c.convertHistoryToMessages(history, "next task"), "NEW SNAPSHOT", true)
			if marshalString(t, sent) != marshalString(t, projected) || len(c.turnContextPlacements) != 2 {
				t.Fatal("token projection disagrees with request serialization")
			}
			if !strings.Contains(marshalString(t, sent[0]), "OLD SNAPSHOT") || !strings.Contains(marshalString(t, sent[2]), "NEW SNAPSHOT") {
				t.Fatal("new user turn rewrote an earlier snapshot")
			}
			clone := c.cloneForSession().(*AnthropicClient)
			if len(clone.turnContextPlacements) != 0 {
				t.Fatal("new session inherited private wire snapshots")
			}
			// A cleared/replaced/compacted history no longer matches the old
			// prefix. Neither stale snapshots nor their indices may survive.
			replaced := c.appendStableTurnContext(c.convertHistoryToMessages(nil, "replacement task"), "CURRENT SNAPSHOT", true)
			raw := marshalString(t, replaced)
			if len(c.turnContextPlacements) != 1 || strings.Contains(raw, "OLD SNAPSHOT") || strings.Contains(raw, "NEW SNAPSHOT") || !strings.Contains(raw, "CURRENT SNAPSHOT") {
				t.Fatalf("stale context survived replacement: %s", raw)
			}
		})
	}
}
