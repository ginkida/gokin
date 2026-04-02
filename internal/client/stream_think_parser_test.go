package client

import "testing"

func TestThinkTagParser_NoTags(t *testing.T) {
	p := &ThinkTagParser{}
	thinking, regular := p.Process("Hello world")
	if thinking != "" {
		t.Errorf("thinking = %q, want empty", thinking)
	}
	if regular != "Hello world" {
		t.Errorf("regular = %q, want %q", regular, "Hello world")
	}
}

func TestThinkTagParser_FullBlock(t *testing.T) {
	p := &ThinkTagParser{}
	thinking, regular := p.Process("<think>reasoning here</think>answer")
	if thinking != "reasoning here" {
		t.Errorf("thinking = %q, want %q", thinking, "reasoning here")
	}
	if regular != "answer" {
		t.Errorf("regular = %q, want %q", regular, "answer")
	}
}

func TestThinkTagParser_TextBeforeThink(t *testing.T) {
	p := &ThinkTagParser{}
	thinking, regular := p.Process("prefix<think>thought</think>suffix")
	if thinking != "thought" {
		t.Errorf("thinking = %q, want %q", thinking, "thought")
	}
	if regular != "prefixsuffix" {
		t.Errorf("regular = %q, want %q", regular, "prefixsuffix")
	}
}

func TestThinkTagParser_AcrossChunks(t *testing.T) {
	p := &ThinkTagParser{}

	// Chunk 1: start of think tag
	thinking1, regular1 := p.Process("hello<think>start of ")
	if regular1 != "hello" {
		t.Errorf("chunk1 regular = %q, want %q", regular1, "hello")
	}
	if thinking1 != "start of " {
		t.Errorf("chunk1 thinking = %q, want %q", thinking1, "start of ")
	}

	// Chunk 2: middle of thinking
	thinking2, regular2 := p.Process("thought continues ")
	if thinking2 != "thought continues " {
		t.Errorf("chunk2 thinking = %q, want %q", thinking2, "thought continues ")
	}
	if regular2 != "" {
		t.Errorf("chunk2 regular = %q, want empty", regular2)
	}

	// Chunk 3: end of think block + answer
	thinking3, regular3 := p.Process("end</think>the answer")
	if thinking3 != "end" {
		t.Errorf("chunk3 thinking = %q, want %q", thinking3, "end")
	}
	if regular3 != "the answer" {
		t.Errorf("chunk3 regular = %q, want %q", regular3, "the answer")
	}
}

func TestThinkTagParser_PartialOpenTag(t *testing.T) {
	p := &ThinkTagParser{}

	// Chunk ends with partial "<thi"
	thinking1, regular1 := p.Process("hello<thi")
	if regular1 != "hello" {
		t.Errorf("chunk1 regular = %q, want %q", regular1, "hello")
	}
	if thinking1 != "" {
		t.Errorf("chunk1 thinking = %q, want empty", thinking1)
	}

	// Next chunk completes the tag
	thinking2, regular2 := p.Process("nk>reasoning</think>done")
	if thinking2 != "reasoning" {
		t.Errorf("chunk2 thinking = %q, want %q", thinking2, "reasoning")
	}
	if regular2 != "done" {
		t.Errorf("chunk2 regular = %q, want %q", regular2, "done")
	}
}

func TestThinkTagParser_PartialCloseTag(t *testing.T) {
	p := &ThinkTagParser{}

	// Start think block
	p.Process("<think>reasoning")

	// Chunk ends with partial "</thi"
	thinking1, _ := p.Process(" more</thi")
	if thinking1 != " more" {
		t.Errorf("chunk thinking = %q, want %q", thinking1, " more")
	}

	// Complete the close tag
	thinking2, regular2 := p.Process("nk>answer here")
	if thinking2 != "" {
		t.Errorf("final thinking = %q, want empty", thinking2)
	}
	if regular2 != "answer here" {
		t.Errorf("final regular = %q, want %q", regular2, "answer here")
	}
}

func TestThinkTagParser_OnlyThinking(t *testing.T) {
	p := &ThinkTagParser{}
	thinking, regular := p.Process("<think>all thinking no answer</think>")
	if thinking != "all thinking no answer" {
		t.Errorf("thinking = %q, want %q", thinking, "all thinking no answer")
	}
	if regular != "" {
		t.Errorf("regular = %q, want empty", regular)
	}
}

func TestThinkTagParser_UnterminatedThink(t *testing.T) {
	p := &ThinkTagParser{}

	thinking1, regular1 := p.Process("<think>started but never closed")
	if thinking1 != "started but never closed" {
		t.Errorf("thinking = %q, want %q", thinking1, "started but never closed")
	}
	if regular1 != "" {
		t.Errorf("regular = %q, want empty", regular1)
	}

	if !p.InThinkBlock() {
		t.Error("expected to be in think block")
	}

	// Flush at end of stream
	thinking2, regular2 := p.Flush()
	if thinking2 != "" && regular2 != "" {
		t.Errorf("flush: thinking=%q, regular=%q, expected both empty (no buffer)", thinking2, regular2)
	}
}

func TestThinkTagParser_MultipleBlocks(t *testing.T) {
	p := &ThinkTagParser{}

	thinking, regular := p.Process("q1<think>thought1</think>a1<think>thought2</think>a2")
	if thinking != "thought1thought2" {
		t.Errorf("thinking = %q, want %q", thinking, "thought1thought2")
	}
	if regular != "q1a1a2" {
		t.Errorf("regular = %q, want %q", regular, "q1a1a2")
	}
}

// TestProcessStream_WithThinkTags simulates the real flow:
// chunks with <think> tags → ProcessStream → OnThinking callback fires
func TestProcessStream_WithThinkTags(t *testing.T) {
	chunks := make(chan ResponseChunk, 10)

	// Simulate what the Anthropic client sends after ThinkTagParser processes text
	parser := &ThinkTagParser{}

	// Chunk 1: opening think tag
	thinking1, text1 := parser.Process("<think>I need to check the file")
	chunks <- ResponseChunk{Thinking: thinking1, Text: text1}

	// Chunk 2: more thinking
	thinking2, text2 := parser.Process(" structure first.</think>")
	chunks <- ResponseChunk{Thinking: thinking2, Text: text2}

	// Chunk 3: answer text
	thinking3, text3 := parser.Process("Here is the answer.")
	chunks <- ResponseChunk{Thinking: thinking3, Text: text3, Done: true}

	close(chunks)

	sr := &StreamingResponse{Chunks: chunks}

	var gotThinking, gotText string
	resp, err := ProcessStream(t.Context(), sr, &StreamHandler{
		OnThinking: func(text string) {
			gotThinking += text
		},
		OnText: func(text string) {
			gotText += text
		},
	})

	if err != nil {
		t.Fatalf("ProcessStream error: %v", err)
	}

	if gotThinking != "I need to check the file structure first." {
		t.Errorf("OnThinking got %q, want %q", gotThinking, "I need to check the file structure first.")
	}
	if gotText != "Here is the answer." {
		t.Errorf("OnText got %q, want %q", gotText, "Here is the answer.")
	}
	if resp.Thinking != gotThinking {
		t.Errorf("resp.Thinking = %q, want %q", resp.Thinking, gotThinking)
	}
	if resp.Text != gotText {
		t.Errorf("resp.Text = %q, want %q", resp.Text, gotText)
	}
}

func TestPartialSuffix(t *testing.T) {
	tests := []struct {
		text string
		tag  string
		want int
	}{
		{"abc<thi", "<think>", 4},
		{"abc<", "<think>", 1},
		{"abc<think", "<think>", 6},
		{"abc", "<think>", 0},
		{"abc</thi", "</think>", 5},
		{"abc</think", "</think>", 7},
		{"abc</", "</think>", 2},
	}
	for _, tt := range tests {
		got := partialSuffix(tt.text, tt.tag)
		if got != tt.want {
			t.Errorf("partialSuffix(%q, %q) = %d, want %d", tt.text, tt.tag, got, tt.want)
		}
	}
}
