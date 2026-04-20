package tools

import (
	"context"
	"strings"

	"gokin/internal/logging"
)

// maybePromptDiff wraps DiffHandler.PromptDiff with an auto-accept path for
// whitespace-only changes. Returns (approved, err) with the same semantics
// as PromptDiff itself. When the change is trivial, no UI prompt is shown
// and an info-level log entry records the skip.
//
// Kept here (next to IsTrivialDiff) so every call site in tools/ — edit,
// write, refactor, batch — gets the same behaviour without each having to
// replicate the trivial check.
func maybePromptDiff(ctx context.Context, handler DiffHandler, filePath, oldContent, newContent, toolName string, showFirst bool) (bool, error) {
	if IsTrivialDiff(oldContent, newContent) {
		logging.Debug("auto-accepted trivial diff",
			"tool", toolName, "file", filePath,
			"reason", "whitespace-only change")
		return true, nil
	}
	return handler.PromptDiff(ctx, filePath, oldContent, newContent, toolName, showFirst)
}

// IsTrivialDiff reports whether the change from old to new touches only
// whitespace or line endings — no character of actual content has shifted.
// Used to auto-accept formatter/whitespace-only edits so the user isn't
// bombarded with trivial approval prompts.
//
// Heuristic:
//   - equal inputs → trivial (not really a diff, but safe to skip prompting)
//   - after collapsing all whitespace runs to a single space and trimming,
//     strings are equal → trivial
//
// Deliberately conservative: a diff that drops/adds a non-whitespace
// character anywhere returns false, even if 99% of the change is
// reformatting. The user still approves anything semantically new.
func IsTrivialDiff(oldContent, newContent string) bool {
	if oldContent == newContent {
		return true
	}
	return normalizeWhitespace(oldContent) == normalizeWhitespace(newContent)
}

// normalizeWhitespace collapses runs of whitespace to a single space and
// trims leading/trailing whitespace. Preserves other characters verbatim.
// Operates in a single pass; no allocation beyond the output builder.
func normalizeWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inWS := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			inWS = true
			continue
		}
		if inWS && b.Len() > 0 {
			b.WriteByte(' ')
		}
		inWS = false
		b.WriteByte(c)
	}
	return b.String()
}
