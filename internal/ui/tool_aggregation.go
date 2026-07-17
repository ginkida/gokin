package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Consecutive-tool aggregation (the Claude-Code "Read 2 files, ran 3 shell
// commands" shape). A run of completed read/bash tools used to render as a
// stack of near-identical one-liners — three `▪ Read(checker.go)` rows in a
// row carry one row of information. Title-only results (their bodies are
// collapsed-by-default) are now BUFFERED and flushed as one aggregate line
// the moment anything else needs the scrollback: a tool with a visible body,
// a failure, streamed text, a sub-agent line, a queued user echo, or the end
// of the turn. Ordering is preserved — the aggregate always lands before
// whatever triggered the flush.
//
// Per-tool details are not lost: buildToolResultBody already stored each
// result in the expand store before the line was buffered, and the aggregate
// carries a compact ⎿ target list.

// aggToolEntry is one buffered title-only tool completion.
type aggToolEntry struct {
	name     string // tool name ("read", "bash")
	target   string // concise target (basename / stripped command)
	outcome  string // count-only outcome ("152 lines")
	duration time.Duration
}

// aggregatableToolLine reports whether a SUCCESSFUL, body-less result of this
// tool may join the aggregation buffer. The caller's `body == ""` gate is the
// load-bearing half: grep/glob/list_dir results WITH matches render a visible
// body and therefore always emit individually (the v0.100.86 "nothing
// renderable is deferred" invariant) — only their quiet results (no matches,
// empty listings) fold into the burst line, the Claude-Code "searched for 2
// patterns" shape (v0.100.91). Tools whose body is always the signal (edit,
// write) stay out entirely.
func aggregatableToolLine(toolName string) bool {
	switch toolName {
	case "read", "bash", "grep", "glob", "list_dir":
		return true
	}
	return false
}

// bufferToolLine queues a title-only completion for aggregated emission.
func (m *Model) bufferToolLine(name, target, outcome string, duration time.Duration) {
	m.pendingToolLines = append(m.pendingToolLines, aggToolEntry{
		name: name, target: target, outcome: outcome, duration: duration,
	})
}

// flushPendingToolLines emits the buffered tool completions: a single entry
// renders EXACTLY as it would have unbuffered (byte-identical legacy line);
// several render as one aggregate header with a compact ⎿ target list.
// Idempotent and cheap when the buffer is empty — callers sprinkle it before
// every scrollback append.
func (m *Model) flushPendingToolLines() {
	entries := m.pendingToolLines
	if len(entries) == 0 {
		return
	}
	m.pendingToolLines = nil

	if len(entries) == 1 {
		e := entries[0]
		m.emitToolResultCard(m.styles.FormatToolLine(e.name, e.target, e.outcome, e.duration), "")
		return
	}

	header, total := aggregateToolHeader(entries)
	m.emitToolResultCard(m.styles.FormatToolAggregateLine(header, total), aggregateTargetList(entries))
}

// aggregateToolHeader builds the "Read 2 files, searched 2 patterns, ran 3
// shell commands" header and the summed duration.
func aggregateToolHeader(entries []aggToolEntry) (string, time.Duration) {
	var reads, searches, listings, bashes int
	readTargets := map[string]int{}
	firstReadTarget := ""
	var total time.Duration
	for _, e := range entries {
		total += e.duration
		switch e.name {
		case "read":
			reads++
			if firstReadTarget == "" {
				firstReadTarget = e.target
			}
			readTargets[e.target]++
		case "grep", "glob":
			searches++
		case "list_dir":
			listings++
		default:
			bashes++
		}
	}

	var parts []string
	if reads > 0 {
		switch {
		case len(readTargets) == 1 && reads > 1:
			parts = append(parts, fmt.Sprintf("Read %s ×%d", firstReadTarget, reads))
		case reads == 1:
			parts = append(parts, "Read "+firstReadTarget)
		default:
			parts = append(parts, fmt.Sprintf("Read %d files", len(readTargets)))
		}
	}
	appendVerb := func(lower, upper, body string) {
		if len(parts) == 0 {
			parts = append(parts, upper+" "+body)
			return
		}
		parts = append(parts, lower+" "+body)
	}
	if searches > 0 {
		appendVerb("searched", "Searched", pluralCount(searches, "pattern", "patterns"))
	}
	if listings > 0 {
		appendVerb("listed", "Listed", pluralCount(listings, "directory", "directories"))
	}
	if bashes > 0 {
		appendVerb("ran", "Ran", pluralCount(bashes, "shell command", "shell commands"))
	}
	return strings.Join(parts, ", "), total
}

// aggregateTargetList renders the compact ⎿ detail: unique targets in first-
// seen order with ×N for repeats, capped at four plus a disclosed remainder.
func aggregateTargetList(entries []aggToolEntry) string {
	counts := map[string]int{}
	var order []string
	for _, e := range entries {
		t := e.target
		if t == "" {
			t = e.name
		}
		if counts[t] == 0 {
			order = append(order, t)
		}
		counts[t]++
	}

	const show = 4
	var items []string
	for i, t := range order {
		if i >= show {
			break
		}
		label := t
		if counts[t] > 1 {
			label = fmt.Sprintf("%s ×%d", t, counts[t])
		}
		items = append(items, label)
	}
	line := strings.Join(items, ", ")
	if hidden := len(order) - show; hidden > 0 {
		line += fmt.Sprintf(" +%d more", hidden)
	}
	return lipgloss.NewStyle().Foreground(ColorDim).Render(line)
}

// FormatToolAggregateLine renders the aggregate header in the same calm
// one-line shape as FormatToolLine — dim ▪ marker, plain-text header, dim
// summed duration (shown only past the 100ms noise threshold, same rule).
func (s *Styles) FormatToolAggregateLine(header string, duration time.Duration) string {
	markerStyle := lipgloss.NewStyle().Foreground(ColorDim)
	textStyle := lipgloss.NewStyle().Foreground(ColorText)
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)

	var b strings.Builder
	b.WriteString(markerStyle.Render(toolBullet + " "))
	b.WriteString(textStyle.Render(header))
	if durStr, durColor := formatToolDurationLabel(duration); durStr != "" {
		b.WriteString(dimStyle.Render(" · "))
		b.WriteString(lipgloss.NewStyle().Foreground(durColor).Render(durStr))
	}
	return b.String()
}
