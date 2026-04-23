package app

import (
	"fmt"
	"path/filepath"
	"strings"
)

// buildEvidenceFooterIfEnabled is the App-level wrapper that snapshots
// per-turn state (touched paths, commands, tools used) and hands it to
// the pure formatter. Returns "" when the completion.evidence_footer
// config knob is off, or when the formatter decides there's nothing
// worth saying.
func (a *App) buildEvidenceFooterIfEnabled(response string) string {
	if a == nil {
		return ""
	}
	if a.config != nil && !a.config.Completion.EvidenceFooter {
		return ""
	}
	touched := a.snapshotResponseTouchedPaths()
	commands := a.snapshotResponseCommands()
	tools := a.snapshotResponseToolsUsed()
	return buildResponseEvidenceFooter(response, touched, tools, commands)
}

// Evidence footer constraints. These are intentionally tight: the footer
// is meant as a final "receipt" after the model already wrote its prose
// answer, not a replacement for it.
const (
	evidenceFooterMaxFiles    = 4
	evidenceFooterMaxCommands = 2
	// A footer with 0 touched files is useless noise — it'd just say
	// "Verified: go test" with no context of what was changed.
	evidenceFooterMinFiles = 1
)

// buildResponseEvidenceFooter renders a compact, deterministic "what
// actually changed" block appended to the model's final answer. The
// intent is to make the response audit-able at a glance even if the
// model forgot to list touched paths or glossed over verification.
//
// Returns "" to skip the footer when:
//   - No touched paths (nothing to show — read-only or chat turn)
//   - The response body already recites the same files AND verification
//     signals (avoid duplicating the model's own self-summary)
//
// Keeps wrapping with `_..._` markdown italics so the footer reads as
// meta-info, not a continuation of the main answer. The leading `\n\n`
// ensures a blank line separates it from whatever the model wrote last
// even if the model didn't end with a trailing newline.
func buildResponseEvidenceFooter(response string, touchedPaths, toolsUsed, commands []string) string {
	if len(touchedPaths) < evidenceFooterMinFiles {
		return ""
	}

	files := uniquePaths(touchedPaths)
	verifyItems := pickVerificationCommands(commands, toolsUsed)

	// Cheap duplication guard: if the response already names every file
	// AND mentions verification, adding a footer just bloats it.
	if responseAlreadyDescribesEvidence(response, files, verifyItems) {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n_")
	sb.WriteString("📁 Changed: ")
	sb.WriteString(formatEvidenceFiles(files, evidenceFooterMaxFiles))
	if len(verifyItems) > 0 {
		sb.WriteString(" · ✓ Verified: ")
		sb.WriteString(formatEvidenceCommands(verifyItems, evidenceFooterMaxCommands))
	}
	sb.WriteString("_")
	return sb.String()
}

// pickVerificationCommands filters `commands` down to the ones that look
// like verification (test, lint, build, vet). Falls back to the
// verification tools in toolsUsed so a turn that only used verify_code
// (and no bash) still shows a "Verified" half.
func pickVerificationCommands(commands, toolsUsed []string) []string {
	out := make([]string, 0, 2)
	for _, cmd := range commands {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" || !commandsContainVerificationSignals([]string{cmd}) {
			continue
		}
		out = append(out, cmd)
	}
	if len(out) > 0 {
		return out
	}
	for _, tn := range toolsUsed {
		switch tn {
		case "verify_code", "run_tests":
			return []string{tn}
		}
	}
	return nil
}

// formatEvidenceFiles renders up to `limit` files joined by ", " with a
// trailing "(+N more)" when the list was truncated. Uses filepath.Base
// so the footer stays readable on narrow terminals — the full path is
// already in the tool-call history for anyone who needs it.
func formatEvidenceFiles(files []string, limit int) string {
	if len(files) == 0 {
		return ""
	}
	display := files
	more := 0
	if len(display) > limit {
		display = display[:limit]
		more = len(files) - limit
	}
	names := make([]string, 0, len(display))
	for _, f := range display {
		base := filepath.Base(strings.TrimSpace(f))
		if base == "" || base == "." {
			continue
		}
		names = append(names, base)
	}
	result := strings.Join(names, ", ")
	if more > 0 {
		result += fmt.Sprintf(" (+%d more)", more)
	}
	return result
}

// formatEvidenceCommands renders up to `limit` commands, truncating each
// to 50 chars so a `go test ./... -run TestLongName -v` doesn't run off
// the line. A trailing "..." marks truncation per command; a global
// "(+N more)" marks list truncation.
func formatEvidenceCommands(commands []string, limit int) string {
	if len(commands) == 0 {
		return ""
	}
	display := commands
	more := 0
	if len(display) > limit {
		display = display[:limit]
		more = len(commands) - limit
	}
	short := make([]string, 0, len(display))
	for _, c := range display {
		c = strings.TrimSpace(c)
		if len(c) > 50 {
			c = c[:47] + "..."
		}
		short = append(short, c)
	}
	result := strings.Join(short, ", ")
	if more > 0 {
		result += fmt.Sprintf(" (+%d more)", more)
	}
	return result
}

// responseAlreadyDescribesEvidence returns true when the response text
// already covers every touched file (by basename) AND includes at least
// one explicit verification signal. The bar is high on purpose: a
// response that names the files but forgot to mention testing still gets
// the footer appended, so the audit trail is always present.
func responseAlreadyDescribesEvidence(response string, files, verifyItems []string) bool {
	if len(files) == 0 {
		return true
	}
	lower := strings.ToLower(response)
	if strings.TrimSpace(lower) == "" {
		return false
	}

	for _, f := range files {
		base := strings.ToLower(filepath.Base(strings.TrimSpace(f)))
		if base == "" || base == "." {
			continue
		}
		if !strings.Contains(lower, base) {
			return false
		}
	}
	if len(verifyItems) == 0 {
		return true
	}
	return outputContainsVerificationSignals(response) || responseMentionsVerificationCommand(response, verifyItems)
}

// responseMentionsVerificationCommand handles cases where the response
// quotes the literal command (``go test ./internal/app``) but not the
// generic positive signal ("tests pass"). We check for the first
// verb-like token of each command — "go" alone would over-match so we
// look for "go test", "pytest", etc.
func responseMentionsVerificationCommand(response string, verifyItems []string) bool {
	lower := strings.ToLower(response)
	for _, item := range verifyItems {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		head := item
		if idx := strings.IndexAny(head, " \t"); idx > 0 && idx < len(head) {
			// Keep "go test" / "npm test" / "cargo test" as the fingerprint
			// — skipping the first space lands us at the flag/target part.
			next := head[idx+1:]
			if sp := strings.IndexAny(next, " \t"); sp > 0 {
				head = head[:idx+1+sp]
			}
		}
		if head != "" && strings.Contains(lower, head) {
			return true
		}
	}
	return false
}

// uniquePaths de-duplicates while preserving order — callers pass slices
// that may contain the same file touched by read + edit + write.
func uniquePaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
