package ui

import (
	"strings"
	"testing"
)

// The live "what is the agent doing" line prefers STABLE, SEMANTIC signals
// over an echo of whatever text is streaming: declared activity (in-progress
// todo) > reasoning phase > current section heading > raw snippet. These pin
// the v0.100.81 hierarchy that answers «понятно, что агент делает сейчас».

func TestLiveActivityCurrentLine_ActivityBeatsStreamEcho(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.state = StateStreaming
	m.currentActivity = "Implementing backup/restore"
	m.currentResponseBuf.WriteString("- some raw markdown the model is emitting right now")

	line := m.liveActivityCurrentLine(ActivityFeedSnapshot{})
	if !strings.Contains(line, "Implementing backup/restore") {
		t.Fatalf("declared activity must lead the line: %q", line)
	}
	if strings.Contains(line, "Writing:") {
		t.Fatalf("raw stream echo must not override the declared activity: %q", line)
	}
}

func TestLiveActivityCurrentLine_ThinkingPhaseIsHonest(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.state = StateStreaming
	// Mid-turn shape: some text already streamed, then the model went back
	// into a reasoning phase (thinking chunks never enter the response buf).
	m.currentResponseBuf.WriteString("Ранее написанный текст ответа")
	m.output.AppendThinkingStream("silent reasoning tokens")

	line := m.liveActivityCurrentLine(ActivityFeedSnapshot{})
	if !strings.Contains(line, "Thinking") {
		t.Fatalf("a reasoning phase must be labeled Thinking: %q", line)
	}
	if strings.Contains(line, "Ранее написанный") {
		t.Fatalf("stale text must not render as current activity while thinking: %q", line)
	}

	// With a declared activity, the phase rides along as a suffix.
	m.currentActivity = "Analyzing the race"
	line = m.liveActivityCurrentLine(ActivityFeedSnapshot{})
	if !strings.Contains(line, "Analyzing the race") || !strings.Contains(line, "thinking") {
		t.Fatalf("activity + thinking suffix expected: %q", line)
	}

	// Once the thinking block closes, the snippet path resumes.
	m.currentActivity = ""
	m.output.EndThinking()
	line = m.liveActivityCurrentLine(ActivityFeedSnapshot{})
	if !strings.Contains(line, "Writing:") {
		t.Fatalf("after thinking ends the writing line should resume: %q", line)
	}
}

func TestLiveActivityCurrentLine_SectionHeadingBeatsSnippet(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.state = StateStreaming
	m.currentResponseBuf.WriteString("## Root cause analysis\nThe bug lives in the retry loop where")

	line := m.liveActivityCurrentLine(ActivityFeedSnapshot{})
	if !strings.Contains(line, "Writing: Root cause analysis") {
		t.Fatalf("the current section heading should label the line: %q", line)
	}
}

func TestLiveActivityCurrentLine_FencedHashIsNotAHeading(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.state = StateStreaming
	m.currentResponseBuf.WriteString("```bash\n# just a shell comment\n```\nплюс обычная строка прозы")

	line := m.liveActivityCurrentLine(ActivityFeedSnapshot{})
	if strings.Contains(line, "just a shell comment") {
		t.Fatalf("a fenced shell comment must not masquerade as a heading: %q", line)
	}
	if !strings.Contains(line, "Writing: плюс обычная строка прозы") {
		t.Fatalf("expected the snippet fallback: %q", line)
	}
}

func TestLastStreamHeading_TracksNewestSection(t *testing.T) {
	buf := "## First section\nprose\n\n## Second section\nmore prose"
	if got := lastStreamHeading(buf); got != "Second section" {
		t.Fatalf("lastStreamHeading = %q, want newest section", got)
	}
	if got := lastStreamHeading("no headings at all"); got != "" {
		t.Fatalf("expected empty for heading-less buffer, got %q", got)
	}
}

func TestMarkdownHeadingText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"# Title", "Title"},
		{"### Deep title", "Deep title"},
		{"## Closed heading ##", "Closed heading"},
		{"####### too deep", ""},     // 7 hashes is not a heading
		{"#nospace", ""},             // needs a space after hashes
		{"plain line", ""},           //
		{"# ", ""},                   // no text
		{"# issue #42", "issue #42"}, // trailing #'s only stripped as a closing run
	}
	for _, c := range cases {
		if got := markdownHeadingText(c.in); got != c.want {
			t.Errorf("markdownHeadingText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
