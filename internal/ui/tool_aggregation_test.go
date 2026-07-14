package ui

import (
	"strings"
	"testing"
	"time"
)

// The Claude-Code aggregate shape: a run of title-only read/bash completions
// renders as ONE line ("Read 2 files, ran 3 shell commands") instead of a
// stack of near-identical rows. These pin the buffer/flush semantics.

func longLines(n int) string { return strings.Repeat("x\n", n) }

func TestToolAggregation_ConsecutiveReadsAndBashAggregate(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.handleToolResultWithInfo(longLines(152), "read", "internal/agent/checker.go", time.Now())
	m.handleToolResultWithInfo(longLines(198), "read", "internal/app/app.go", time.Now())
	m.handleToolResultWithInfo(longLines(15), "bash", "go test ./internal/ui", time.Now())

	// Nothing lands until a flush trigger…
	if got := stripAnsi(m.output.state.content.String()); strings.Contains(got, "Read(") {
		t.Fatalf("buffered lines must not render before a flush:\n%s", got)
	}

	// …the end of the turn flushes ONE aggregate line.
	m.handleMessageTypes(ResponseDoneMsg{})
	got := stripAnsi(m.output.state.content.String())
	if !strings.Contains(got, "Read 2 files, ran 1 shell command") {
		t.Fatalf("aggregate header missing:\n%s", got)
	}
	if strings.Contains(got, "Read(checker.go)") || strings.Contains(got, "Bash(") {
		t.Fatalf("individual rows must not render alongside the aggregate:\n%s", got)
	}
	// Compact target detail under the ⎿ corner.
	if !strings.Contains(got, "checker.go") || !strings.Contains(got, "go test ./internal/ui") {
		t.Fatalf("target detail missing:\n%s", got)
	}
}

func TestToolAggregation_SingleEntryFlushesAsLegacyLine(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.handleToolResultWithInfo(longLines(152), "read", "internal/agent/checker.go", time.Now())
	m.flushPendingToolLines()

	got := stripAnsi(m.output.state.content.String())
	if !strings.Contains(got, "Read(checker.go) · 152 lines") {
		t.Fatalf("single buffered entry must render exactly the legacy line:\n%s", got)
	}
}

func TestToolAggregation_SameFileRepeatsCollapse(t *testing.T) {
	m := NewModel()
	m.width = 100
	for range 3 {
		m.handleToolResultWithInfo(longLines(150), "read", "internal/agent/checker.go", time.Now())
	}
	m.handleMessageTypes(ResponseDoneMsg{})
	got := stripAnsi(m.output.state.content.String())
	if !strings.Contains(got, "Read checker.go ×3") {
		t.Fatalf("same-file repeats should collapse to ×N:\n%s", got)
	}
}

// A tool with a VISIBLE body (write, short-output tools) flushes the buffer
// first — the aggregate lands BEFORE it, preserving chronological order.
func TestToolAggregation_NonAggregableFlushesFirstInOrder(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.handleToolResultWithInfo(longLines(152), "read", "a.go", time.Now())
	m.handleToolResultWithInfo(longLines(120), "read", "b.go", time.Now())
	m.handleToolResultWithStatus("Created new file: /w/x.go (100 bytes)", "write", "/w/x.go", time.Now(), false, "")

	got := stripAnsi(m.output.state.content.String())
	aggIdx := strings.Index(got, "Read 2 files")
	writeIdx := strings.Index(got, "Write(x.go)")
	if aggIdx < 0 || writeIdx < 0 {
		t.Fatalf("both the aggregate and the write line must render:\n%s", got)
	}
	if aggIdx > writeIdx {
		t.Fatalf("aggregate must land BEFORE the write that triggered the flush:\n%s", got)
	}
}

func TestToolAggregation_FailureFlushesFirst(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.handleToolResultWithInfo(longLines(152), "read", "a.go", time.Now())
	m.handleToolResultWithStatus("", "bash", "go build ./...", time.Now(), true, "exit status 1")

	got := stripAnsi(m.output.state.content.String())
	readIdx := strings.Index(got, "Read(a.go)")
	failIdx := strings.Index(got, "exit status 1")
	if readIdx < 0 || failIdx < 0 || readIdx > failIdx {
		t.Fatalf("buffered read must flush before the failure line:\n%s", got)
	}
}

func TestToolAggregation_StreamTextFlushes(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.state = StateProcessing
	m.handleToolResultWithInfo(longLines(152), "read", "a.go", time.Now())
	m.handleMessageTypes(StreamTextMsg("Here is the answer\n"))

	got := stripAnsi(m.output.state.content.String())
	if !strings.Contains(got, "Read(a.go)") {
		t.Fatalf("streamed text must flush the buffer first:\n%s", got)
	}
	if strings.Index(got, "Read(a.go)") > strings.Index(got, "Here is the answer") {
		t.Fatalf("buffered line must precede the streamed text:\n%s", got)
	}
}

// A SHORT read renders its body inline (not collapsed) — it must NOT join the
// aggregate; nothing of its content may be deferred or hidden.
func TestToolAggregation_ShortOutputStaysIndividual(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.handleToolResultWithInfo("only line\n", "read", "tiny.go", time.Now())

	got := stripAnsi(m.output.state.content.String())
	if !strings.Contains(got, "Read(tiny.go)") || !strings.Contains(got, "only line") {
		t.Fatalf("short-output read must render immediately with its body:\n%s", got)
	}
	if len(m.pendingToolLines) != 0 {
		t.Fatal("short-output read must not be buffered")
	}
}

func TestAggregateTargetList_CapsAndCounts(t *testing.T) {
	entries := []aggToolEntry{
		{name: "read", target: "a.go"}, {name: "read", target: "a.go"},
		{name: "read", target: "b.go"}, {name: "read", target: "c.go"},
		{name: "read", target: "d.go"}, {name: "read", target: "e.go"},
	}
	got := stripAnsi(aggregateTargetList(entries))
	if !strings.Contains(got, "a.go ×2") {
		t.Fatalf("repeat counts missing: %q", got)
	}
	if !strings.Contains(got, "+1 more") {
		t.Fatalf("over-cap targets must be disclosed: %q", got)
	}
	if strings.Contains(got, "e.go") {
		t.Fatalf("beyond-cap target should be summarized, not listed: %q", got)
	}
}
