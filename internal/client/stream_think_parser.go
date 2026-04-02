package client

import "strings"

// ThinkTagParser detects <think>...</think> tags in streaming text
// and separates thinking content from regular text.
// Many models (MiniMax, DeepSeek R1, QwQ, Qwen) embed chain-of-thought
// reasoning inside these tags within the normal text stream.
type ThinkTagParser struct {
	inThinkBlock bool
	tagBuffer    string // buffers partial "<thi" or "</thi" across chunks
}

// Process takes a text chunk and returns (thinking, regular) portions.
// Handles partial tags across chunk boundaries.
func (p *ThinkTagParser) Process(text string) (thinking, regular string) {
	// Prepend any buffered partial tag from previous chunk
	if p.tagBuffer != "" {
		text = p.tagBuffer + text
		p.tagBuffer = ""
	}

	var thinkBuf, textBuf strings.Builder

	i := 0
	for i < len(text) {
		if p.inThinkBlock {
			// Look for </think>
			closeIdx := strings.Index(text[i:], "</think>")
			if closeIdx >= 0 {
				// Everything before </think> is thinking
				thinkBuf.WriteString(text[i : i+closeIdx])
				i += closeIdx + len("</think>")
				p.inThinkBlock = false
			} else {
				// Check for partial </think> at the end
				remaining := text[i:]
				if partial := partialSuffix(remaining, "</think>"); partial > 0 {
					// Buffer the partial tag for next chunk
					thinkBuf.WriteString(remaining[:len(remaining)-partial])
					p.tagBuffer = remaining[len(remaining)-partial:]
					i = len(text)
				} else {
					thinkBuf.WriteString(remaining)
					i = len(text)
				}
			}
		} else {
			// Look for <think>
			openIdx := strings.Index(text[i:], "<think>")
			if openIdx >= 0 {
				// Everything before <think> is regular text
				textBuf.WriteString(text[i : i+openIdx])
				i += openIdx + len("<think>")
				p.inThinkBlock = true
			} else {
				// Check for partial <think> at the end
				remaining := text[i:]
				if partial := partialSuffix(remaining, "<think>"); partial > 0 {
					textBuf.WriteString(remaining[:len(remaining)-partial])
					p.tagBuffer = remaining[len(remaining)-partial:]
					i = len(text)
				} else {
					textBuf.WriteString(remaining)
					i = len(text)
				}
			}
		}
	}

	return thinkBuf.String(), textBuf.String()
}

// Flush returns any remaining buffered content.
// Call at end of stream.
func (p *ThinkTagParser) Flush() (thinking, regular string) {
	if p.tagBuffer == "" {
		return "", ""
	}
	buf := p.tagBuffer
	p.tagBuffer = ""

	// If we're in a think block, treat remaining as thinking
	if p.inThinkBlock {
		return buf, ""
	}
	return "", buf
}

// InThinkBlock returns whether the parser is currently inside a <think> block.
func (p *ThinkTagParser) InThinkBlock() bool {
	return p.inThinkBlock
}

// partialSuffix checks if the end of text is a prefix of tag.
// Returns the length of the partial match, or 0 if none.
// Example: text="abc<thi", tag="<think>" → returns 4 ("<thi")
func partialSuffix(text, tag string) int {
	maxCheck := len(tag) - 1
	if maxCheck > len(text) {
		maxCheck = len(text)
	}
	for n := maxCheck; n > 0; n-- {
		if strings.HasSuffix(text, tag[:n]) {
			return n
		}
	}
	return 0
}
