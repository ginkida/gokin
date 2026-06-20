package app

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gokin/internal/logging"
)

const (
	atRefMaxPerFile   = 32768   // 32 KB per referenced file (or line slice)
	atRefMaxTotal     = 131072  // 128 KB total across all @refs in one message
	atRefMaxFiles     = 20      // hard count ceiling regardless of size
	atRefBinarySniff  = 8192    // bytes scanned for a NUL to reject binaries
	atRefMaxRangeScan = 1 << 20 // 1 MB: how far into a file a @path:start-end ref may scan
)

// atRefRangeRe matches a trailing line-range suffix "@path:START" or
// "@path:START-END" (greedy path so a Windows "C:\f:10" keeps the drive colon).
var atRefRangeRe = regexp.MustCompile(`^(.+):(\d+)(?:-(\d+))?$`)

// parseAtRef splits an @-ref body into a path and an optional 1-based line range.
// startLine == 0 means "whole file". A malformed/zero/reversed range falls back
// to whole-file (the body is treated as a plain path).
func parseAtRef(raw string) (path string, startLine, endLine int) {
	if m := atRefRangeRe.FindStringSubmatch(raw); m != nil {
		s, _ := strconv.Atoi(m[2])
		e := s
		if m[3] != "" {
			e, _ = strconv.Atoi(m[3])
		}
		if s > 0 && e >= s {
			return m[1], s, e
		}
	}
	return raw, 0, 0
}

// sliceLines returns the 1-based inclusive [start, end] line slice of data.
// ok=false when start is past EOF (an out-of-range ref is skipped silently).
func sliceLines(data []byte, start, end int) ([]byte, bool) {
	lines := strings.Split(string(data), "\n")
	if start < 1 || start > len(lines) {
		return nil, false
	}
	if end > len(lines) {
		end = len(lines)
	}
	return []byte(strings.Join(lines[start-1:end], "\n")), true
}

// expandAtReferences turns @path tokens in a user message into inline file
// content for the AGENT. It is best-effort and CALM: a token that doesn't
// resolve to a readable, in-workspace, regular text file is left EXACTLY as the
// user typed it — we never rewrite the message body, only APPEND a "Referenced
// files" section. The displayed user message stays the raw @path text (the UI
// echoes it before this runs). Content travels in the user MESSAGE (history),
// never the cached system prefix. Never returns an error.
//
// Resolution: strip the leading @, expand ~, join workDir if relative, Clean,
// gate on isDirAccessAllowed (workDir + granted dirs — same boundary as the
// agent's tools), require a regular non-binary file, dedup by absolute path, and
// bound by per-file / total / count caps.
func (a *App) expandAtReferences(message string) string {
	if a == nil || !strings.Contains(message, "@") {
		return message
	}

	type fileRef struct {
		rel       string
		content   string
		truncated int
	}
	var refs []fileRef
	seen := make(map[string]bool)
	total := 0
	limitHit := false

	for _, token := range strings.Fields(message) {
		if len(refs) >= atRefMaxFiles {
			limitHit = true
			break
		}
		// A token is an @-ref candidate only if it starts with '@', has a body,
		// and contains exactly one '@' (rejects @@x and emails like user@host —
		// their '@' is not at index 0 so the prefix check already excludes them).
		if !strings.HasPrefix(token, "@") || len(token) < 2 || strings.Count(token, "@") != 1 {
			continue
		}
		body, startLine, endLine := parseAtRef(strings.TrimPrefix(token, "@"))
		raw := expandHomePath(body)
		abs := raw
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(a.workDir, raw)
		}
		abs = filepath.Clean(abs)
		// Dedup key includes the range so @x:1-10 and @x:20-30 don't collapse.
		dedupKey := abs
		if startLine > 0 {
			dedupKey = fmt.Sprintf("%s:%d-%d", abs, startLine, endLine)
		}
		if seen[dedupKey] {
			continue
		}
		// Same access boundary as the agent's tools — out-of-workspace @tokens
		// (and @emails/@mentions that happen to look like paths) are skipped.
		if !a.isDirAccessAllowed(abs) {
			logging.Debug("@ref skipped: outside workspace/grants", "token", token)
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || !info.Mode().IsRegular() {
			continue // missing, dir, device, symlink-to-non-regular
		}

		// A ranged ref needs more of the file to reach the requested lines; bound
		// the scan so a huge file can't be read whole. Whole-file refs stay tight.
		readCap := int64(atRefMaxPerFile) + 1
		if startLine > 0 {
			readCap = atRefMaxRangeScan + 1
		}
		f, err := os.Open(abs)
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, readCap))
		f.Close()
		if err != nil {
			continue
		}
		// Binary guard — skip files with a NUL byte in the first chunk.
		sniff := data
		if len(sniff) > atRefBinarySniff {
			sniff = sniff[:atRefBinarySniff]
		}
		if bytes.IndexByte(sniff, 0) != -1 {
			continue
		}

		rel, relErr := filepath.Rel(a.workDir, abs)
		if relErr != nil {
			rel = abs // a granted dir outside the workspace
		}
		label := rel
		if startLine > 0 {
			sliced, ok := sliceLines(data, startLine, endLine)
			if !ok {
				continue // range past EOF — skip silently
			}
			data = sliced
			label = fmt.Sprintf("%s:%d-%d", rel, startLine, endLine)
		}

		seen[dedupKey] = true
		truncated := 0
		if len(data) > atRefMaxPerFile {
			if startLine > 0 {
				truncated = len(data) - atRefMaxPerFile // omitted from the slice
			} else if t := int(info.Size()) - atRefMaxPerFile; t > 0 {
				truncated = t // omitted from the whole file
			}
			data = data[:atRefMaxPerFile]
		}
		if total+len(data) > atRefMaxTotal {
			limitHit = true
			break
		}
		total += len(data)

		refs = append(refs, fileRef{rel: label, content: string(data), truncated: truncated})
	}

	if len(refs) == 0 {
		return message
	}

	var b strings.Builder
	b.WriteString(message)
	b.WriteString("\n\n--- Referenced files ---\n")
	for _, r := range refs {
		fmt.Fprintf(&b, "\n[file: %s]\n```\n%s", r.rel, r.content)
		if !strings.HasSuffix(r.content, "\n") {
			b.WriteString("\n")
		}
		if r.truncated > 0 {
			fmt.Fprintf(&b, "… [truncated, %d bytes omitted]\n", r.truncated)
		}
		b.WriteString("```\n")
	}
	if limitHit {
		b.WriteString("\n[… reference expansion limit reached; remaining @files omitted]\n")
	}
	return b.String()
}
