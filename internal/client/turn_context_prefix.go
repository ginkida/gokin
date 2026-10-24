package client

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// ResetConversationCache drops private wire snapshots at an explicit ownership
// boundary. Current instructions/context remain supplied by the owning loop.
// The optional interface avoids expanding Client for stateless implementations.
func ResetConversationCache(c Client) {
	if isNilClient(c) {
		return
	}
	if resetter, ok := c.(interface{ ResetConversationCache() }); ok {
		resetter.ResetConversationCache()
	}
}

func (c *AnthropicClient) ResetConversationCache() {
	c.mu.Lock()
	c.turnContextPlacements = nil
	c.mu.Unlock()
}

type turnContextPlacement struct {
	Index  int
	Prefix [sha256.Size]byte
	Text   string
}

// appendStableTurnContext keeps implicit-cache message prefixes append-only.
// Moving a snapshot to the newest tool result removes it from its former
// message and invalidates everything following that position. Retain it there
// instead, appending a new snapshot only for new state or a new user message.
// Placements belong to this client session, never genai history or disk. Prefix
// hashes discard them when history is cleared, compacted, edited or replaced.
// Token estimation uses the same projection without recording placements.
func (c *AnthropicClient) appendStableTurnContext(messages []map[string]any, snapshot string, remember bool) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.isProvider("deepseek") && !c.isProvider("glm") {
		return appendTurnContextBlock(messages, snapshot)
	}
	if len(messages) == 0 {
		return messages
	}
	hash := sha256.New()
	prefixes := make([][sha256.Size]byte, len(messages))
	for i, message := range messages {
		raw, err := json.Marshal(message)
		if err != nil {
			return appendTurnContextBlock(messages, snapshot)
		}
		_, _ = hash.Write(raw)
		_, _ = hash.Write([]byte{0})
		copy(prefixes[i][:], hash.Sum(nil))
	}
	placements := make([]turnContextPlacement, 0, len(c.turnContextPlacements)+1)
	for _, placement := range c.turnContextPlacements {
		if placement.Index >= len(prefixes) || placement.Prefix != prefixes[placement.Index] {
			break
		}
		placements = append(placements, placement)
	}
	last := len(messages) - 1
	if snapshot != "" && messages[last]["role"] == "user" {
		fresh := len(placements) == 0
		if !fresh {
			prior := placements[len(placements)-1]
			fresh = prior.Text != snapshot || last > prior.Index && hasUserText(messages[last])
			if fresh && prior.Index == last {
				placements = placements[:len(placements)-1]
			}
		}
		if fresh {
			placements = append(placements, turnContextPlacement{Index: last, Prefix: prefixes[last], Text: snapshot})
		}
	}
	if remember {
		c.turnContextPlacements = placements
	}
	for i, placement := range placements {
		// Label snapshots from their first send: older bytes stay immutable,
		// while their sequence and position identify the newest state.
		text := fmt.Sprintf("Snapshot %d (as of message %d; historical point-in-time context). The last snapshot is newest; newer snapshots supersede older ones. Later messages and tool results take precedence.\n\n%s", i+1, placement.Index+1, placement.Text)
		appendTurnContextBlock(messages[:placement.Index+1], text)
	}
	return messages
}

func hasUserText(message map[string]any) bool {
	if text, ok := message["content"].(string); ok {
		return text != ""
	}
	for _, block := range extractContentArray(message["content"]) {
		if block["type"] == "text" {
			return true
		}
	}
	return false
}
