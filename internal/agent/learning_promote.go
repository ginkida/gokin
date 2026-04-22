package agent

import (
	"strings"
	"time"

	"gokin/internal/tools"

	"google.golang.org/genai"
)

// recordToolForLearning feeds a completed tool call into ProjectLearning so
// durable patterns (reliable commands, frequently-used tools) accumulate
// automatically — no explicit /memorize call required.
//
// Hook point: called from executeToolWithReflection right after executeTool
// returns, so both success/failure outcomes inform the exponential-moving-
// average success rate that ProjectLearning tracks.
//
// Scope: currently only bash commands, because those have the cleanest
// promotion signal (success/failure is unambiguous, command text is the
// natural identity, duration is meaningful). File/grep/glob calls are
// harder to summarise into a durable memory item — deliberately left for
// a follow-up pass.
//
// Noise control: ProjectLearning.FormatForPrompt already gates the Reliable
// Commands section by `SuccessRate >= 0.5 && UsageCount >= 2`, so we can
// record freely — the display layer filters. We do pre-filter obviously
// junk input (empty, nav-only, huge heredocs) to keep learning.yaml small.
func (a *Agent) recordToolForLearning(call *genai.FunctionCall, result tools.ToolResult, elapsed time.Duration) {
	if a == nil || a.learning == nil || call == nil {
		return
	}
	switch call.Name {
	case "bash":
		cmd, _ := call.Args["command"].(string)
		cmd = normalizeCommandForLearning(cmd)
		if cmd == "" {
			return
		}
		// LearnCommand treats durationMs <= 0 as "don't touch AvgDuration",
		// so passing an unset elapsed still behaves correctly.
		a.learning.LearnCommand(cmd, "", result.Success, float64(elapsed.Milliseconds()))
	}
}

// normalizeCommandForLearning prepares a bash command string for storage in
// ProjectLearning. It trims whitespace, strips a leading "cd <path> && "
// prefix (users think about `go test ./...` as the command, not
// `cd /Users/me/project && go test ./...`), and rejects input that isn't
// worth learning.
//
// Returns "" for inputs we deliberately don't record.
func normalizeCommandForLearning(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}

	// Strip leading `cd <path> && `. Mirrors stagnation_fingerprint's
	// treatment so both systems see the same canonical command text.
	// A pure `cd <path>` with no `&&` chain isn't a command worth learning
	// — it's just navigation, and the next thing the model does will be
	// recorded separately.
	if strings.HasPrefix(cmd, "cd ") {
		idx := strings.Index(cmd, " && ")
		if idx < 0 {
			return ""
		}
		cmd = strings.TrimSpace(cmd[idx+4:])
		if cmd == "" {
			return ""
		}
	}

	// Short one-liners (`ls`, `pwd`, `w`, `df`) have no value in project
	// memory — every project uses them. Threshold is intentionally loose;
	// `go vet` at 6 chars squeaks through.
	if len(cmd) < 5 {
		return ""
	}

	// Very long commands are usually inline scripts or heredocs — storing
	// them bloats learning.yaml without producing a reusable signal. A
	// user who wants to capture a script should run `/memorize` explicitly.
	if len(cmd) > 500 {
		return ""
	}

	return cmd
}
