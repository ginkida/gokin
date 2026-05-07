package app

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestSignalsShutdown_NoSilentFailures is a regression guard that pins the
// invariant established across v0.80.8 → v0.80.11: failures that happen
// during the shutdown flush path must be visible at default log level.
//
// Five separate releases this sprint caught Debug-level logs (or `_ = ...`
// discards) where the user actually wanted to know — final session save,
// active plan save, input history save, errorStore.Flush, exampleStore.Flush.
// Each one was a chunk of user data quietly disappearing on disk-full /
// permission-flip / sandboxed-write.
//
// Rather than continue whack-a-mole, this test scans signals.go (the
// canonical shutdown file) for two anti-patterns:
//
//  1. `logging.Debug("failed to ...")` — failures should be Warn/Error.
//     Debug is invisible in default config, so the failure is silent in
//     field deployments.
//  2. `_ = X.Save()` / `_ = X.Flush()` / `_ = X.Persist()` — discarding
//     a persistence error during shutdown drops user data with no signal.
//
// Allowlist: `_ = X.Close()` (close errors during shutdown are typically
// harmless), `_ = X.Stop()`, `_ = X.Cancel()`. These are control-flow
// signals, not persistence calls.
//
// If you intentionally want to suppress a logged failure here, add an
// explicit `// shutdown-lint-allow: <reason>` comment on the same line
// — the test honors that escape hatch but forces a written justification.
func TestSignalsShutdown_NoSilentFailures(t *testing.T) {
	const path = "signals.go"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	debugFailedRe := regexp.MustCompile(`logging\.Debug\(\s*"failed to `)
	silentPersistRe := regexp.MustCompile(`_ = [a-zA-Z_.]+\.(Save|Flush|Persist|Commit|Sync|Snapshot|Write)\b`)
	allowMarker := "shutdown-lint-allow:"

	var violations []string
	for i, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, allowMarker) {
			continue
		}
		if debugFailedRe.MatchString(line) {
			violations = append(violations,
				"signals.go:"+itoa(i+1)+" — `logging.Debug(\"failed to ...\")` should be Warn/Error\n  "+strings.TrimSpace(line))
		}
		if silentPersistRe.MatchString(line) {
			violations = append(violations,
				"signals.go:"+itoa(i+1)+" — silent error discard on persistence call\n  "+strings.TrimSpace(line))
		}
	}

	if len(violations) > 0 {
		t.Errorf("found %d shutdown anti-patterns — see v0.80.8/9/10/11 commits for the class. "+
			"If a suppression is intentional, add `// shutdown-lint-allow: <reason>` to the line.\n\n%s",
			len(violations), strings.Join(violations, "\n\n"))
	}
}

// itoa is a tiny helper so the test doesn't need strconv just for line numbers.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := make([]byte, 0, 8)
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
