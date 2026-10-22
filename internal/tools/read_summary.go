package tools

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
)

// SetSummaryReader binds only this foreground ReadTool to its owning session.
// CloneToolForWorkDir deliberately creates a fresh ReadTool without this
// callback: a forked skill or background agent cannot read its parent's chat.
func (t *ReadTool) SetSummaryReader(reader func(context.Context, string) (string, error)) {
	t.summaryMu.Lock()
	defer t.summaryMu.Unlock()
	t.summaryReader = reader
}
func (t *ReadTool) readSummaryResource(ctx context.Context, uri string, args map[string]any) ToolResult {
	const prefix = "gokin://summary/"
	if !strings.HasPrefix(uri, prefix) {
		return NewErrorResult("unknown session resource")
	}
	id := strings.TrimPrefix(uri, prefix)
	if len(id) != 32 {
		return NewErrorResult("invalid summary archive ID")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return NewErrorResult("invalid summary archive ID")
	}
	t.summaryMu.RLock()
	reader := t.summaryReader
	t.summaryMu.RUnlock()
	if reader == nil {
		return NewErrorResult("summary archives are unavailable in this tool's owning session")
	}
	text, err := reader(ctx, id)
	if err != nil {
		return NewErrorResult(err.Error())
	}
	if len(text) > 64<<20 {
		return NewErrorResult("summary archive exceeds the read size limit")
	}
	offset := GetIntDefault(args, "offset", 1)
	limit := GetIntDefault(args, "limit", DefaultReadLimit)
	if offset < 1 || limit < 1 {
		return NewErrorResult("offset and limit must be positive")
	}
	limit = min(limit, MaxChunkSize)
	// A resource has display lines, not filesystem source lines. Wrap long JSON
	// string values so offset/limit can reach every byte of an old tool result.
	lines := make([]string, 0)
	for _, line := range strings.Split(text, "\n") {
		runes := []rune(line)
		for len(runes) > 1000 {
			lines = append(lines, string(runes[:1000]))
			runes = runes[1000:]
		}
		lines = append(lines, string(runes))
	}
	start := min(offset-1, len(lines))
	end := min(start+limit, len(lines))
	var out strings.Builder
	out.WriteString("Archived conversation (long lines wrapped for paging):\n")
	for i := start; i < end; i++ {
		if out.Len()+len(lines[i]) > 128<<10 {
			end = i
			break
		}
		fmt.Fprintf(&out, "%6d\t%s\n", i+1, lines[i])
	}
	if end < len(lines) {
		fmt.Fprintf(&out, "\nMore archived messages: read the same resource with offset=%d.\n", end+1)
	}
	return NewSuccessResultWithData(out.String(), map[string]any{"resource_uri": uri, "virtual_resource": true, "start_line": start + 1, "end_line": end, "total_lines": len(lines), "has_more": end < len(lines)})
}
