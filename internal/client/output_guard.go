package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"gokin/internal/logging"
	"google.golang.org/genai"
)

// DefaultMaxToolArgumentBytes allows JSON-escaped large source-file writes.
// The per-call limit is independent of the provider's token/context limits.
const DefaultMaxToolArgumentBytes = 1 << 20

// DegenerateOutputError rejects an incomplete model round before tools execute.
// The owning loop may retry once with new guidance; Exhausted forbids outer
// retry/failover layers from restarting that budget.
type DegenerateOutputError struct {
	Model, Tool   string
	Bytes, Period int
	SizeLimit     bool
	Exhausted     bool
}

func (e *DegenerateOutputError) Error() string {
	reason := "repeated output"
	if e.SizeLimit {
		reason = "tool argument size limit"
	}
	return fmt.Sprintf("degenerate_output: %s from %s (tool=%q, bytes=%d, period=%d); partial tool calls were discarded. Ask for a smaller output or split the file/code into smaller calls", reason, e.Model, e.Tool, e.Bytes, e.Period)
}

const DegenerateOutputRetryNote = "[Output recovery] Your previous output was discarded because tool arguments or text degenerated into repetition or exceeded the argument size limit. No tools from that response were executed. Write concisely, avoid repetition, and split large code/file contents into smaller calls."

// AppendOutputRecoveryNote preserves the serialized prefix too: the local
// assistant marker separates the prior user/tool-result message from the new
// user guidance, so role coalescing cannot rewrite the prior message's bytes.
func AppendOutputRecoveryNote(history []*genai.Content) []*genai.Content {
	next := make([]*genai.Content, 0, len(history)+2)
	next = append(next, history...)
	return append(next,
		genai.NewContentFromText("[The output guard discarded the preceding incomplete response before executing any tools from it.]", genai.RoleModel),
		genai.NewContentFromText(DegenerateOutputRetryNote, genai.RoleUser))
}

type outputRecoveryKey struct{}

// ContextWithOutputRecoveryBudget shares a single retry across all model
// rounds, outer retries and continuations of one owning turn.
func ContextWithOutputRecoveryBudget(ctx context.Context) context.Context {
	if _, ok := ctx.Value(outputRecoveryKey{}).(*atomic.Bool); ok {
		return ctx
	}
	return NewOutputRecoveryBudget(ctx)
}

// NewOutputRecoveryBudget gives a child run its own budget, excluding the
// parent's retry state even when delegation inherits its context.
func NewOutputRecoveryBudget(ctx context.Context) context.Context {
	return context.WithValue(ctx, outputRecoveryKey{}, &atomic.Bool{})
}

func outputRecoveryActive(ctx context.Context) bool {
	state, _ := ctx.Value(outputRecoveryKey{}).(*atomic.Bool)
	return state != nil && state.Load()
}

// ClaimOutputRecovery must be called by an owning loop, never a generic retry
// layer. A second trip is non-retryable, including by provider failover.
func ClaimOutputRecovery(ctx context.Context, err error) (bool, error) {
	failure, ok := OutputRecoveryError(err)
	if !ok {
		return false, err
	}
	if ctx.Err() != nil {
		return false, ContextErr(ctx)
	}
	state, _ := ctx.Value(outputRecoveryKey{}).(*atomic.Bool)
	if !failure.Exhausted && state != nil && state.CompareAndSwap(false, true) {
		return true, err
	}
	exhausted := *failure
	exhausted.Exhausted = true
	return false, &exhausted
}

func recoveryTemperature(ctx context.Context, temperature float32) float32 {
	if outputRecoveryActive(ctx) {
		if temperature <= 0 {
			return 0.2
		} // An omitted temperature uses the provider default.
		return min(temperature, 0.2)
	}
	return temperature
}

// OutputRecoveryError distinguishes this class even through caller wrappers.
func OutputRecoveryError(err error) (*DegenerateOutputError, bool) {
	var failure *DegenerateOutputError
	ok := errors.As(err, &failure)
	return failure, ok && failure != nil
}

// outputGuard retains a bounded suffix, checking at most once per KiB. A KMP
// prefix table finds exact periods in linear time, independent of SSE chunking.
// A period <=256 requires >=128 repetitions, or >=512 for file-content tools.
// Opaque/base64 data and structured file rows are excluded; the argument cap
// still bounds these legitimate fixtures.
type outputGuard struct {
	tail           []byte
	prefix         []int
	bytes, checked int
}

func (g *outputGuard) check(model, tool, delta string, limit int) error {
	g.bytes += len(delta)
	failure := &DegenerateOutputError{Model: model, Tool: tool, Bytes: g.bytes}
	if limit > 0 && g.bytes > limit {
		failure.SizeLimit = true
	} else {
		window := repetitionWindow(tool)
		if len(delta) >= window {
			g.tail = append(g.tail[:0], delta[len(delta)-window:]...)
		} else {
			if excess := len(g.tail) + len(delta) - window; excess > 0 {
				g.tail = g.tail[excess:]
			}
			g.tail = append(g.tail, delta...)
		}
		if len(g.tail) < window || g.bytes-g.checked < 1024 {
			return nil
		}
		g.checked = g.bytes
		if len(g.prefix) != len(g.tail) {
			g.prefix = make([]int, len(g.tail))
		}
		prefix := g.prefix
		for i, j := 1, 0; i < len(g.tail); i++ {
			for j > 0 && g.tail[i] != g.tail[j] {
				j = prefix[j-1]
			}
			if g.tail[i] == g.tail[j] {
				j++
			}
			prefix[i] = j
		}
		period := len(g.tail) - prefix[len(g.tail)-1]
		if period > 256 || !repetitionLooksLikeLanguage(string(g.tail[:period])) ||
			(window > 32<<10 && repetitionIsStructuredRow(string(g.tail[:period]))) {
			return nil
		}
		failure.Period = period
	}
	logging.Warn("degenerate model output stopped", "model", model, "tool", tool, "bytes", failure.Bytes, "period", failure.Period, "size_limit", failure.SizeLimit)
	return failure
}

func repetitionWindow(tool string) int {
	switch tool {
	case "write", "edit", "batch", "diff", "harness", "use_tool":
		// These tools accept file contents (including nested replacements and
		// staged harness source). Larger fixtures need more evidence of a loop.
		return 128 << 10
	default:
		return 32 << 10
	}
}

// Complete payloads include closing JSON delimiters, which can conceal an
// otherwise periodic suffix. Inspect string values too, before publishing a
// tool call. This also covers clients whose SDK delivers parsed arguments.
func checkToolArgumentRepetition(model, tool string, value any) error {
	if tool == "use_tool" {
		if envelope, ok := value.(map[string]any); ok {
			if name, ok := envelope["name"].(string); ok && name != "use_tool" && name != "tool_search" {
				if arguments, ok := envelope["arguments"].(map[string]any); ok {
					return checkToolArgumentRepetition(model, name, arguments)
				}
			}
		}
	}
	switch value := value.(type) {
	case string:
		if len(value) >= repetitionWindow(tool) {
			var guard outputGuard
			return guard.check(model, tool, value, 0)
		}
	case map[string]any:
		for _, field := range value {
			if err := checkToolArgumentRepetition(model, tool, field); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range value {
			if err := checkToolArgumentRepetition(model, tool, item); err != nil {
				return err
			}
		}
	}
	return nil
}

func repetitionLooksLikeLanguage(period string) bool {
	// Only the base64/hex alphabet and line wrapping qualify as opaque data.
	// Treat the period circularly: a JSON-escaped newline can straddle its end.
	for i := range len(period) {
		b := period[i]
		if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || strings.ContainsRune("+/=_-\r\n", rune(b)) {
			continue
		}
		if b == '\\' && (period[(i+1)%len(period)] == 'n' || period[(i+1)%len(period)] == 'r') {
			continue
		}
		return true
	}
	return false
}

func repetitionIsStructuredRow(period string) bool {
	// Rotate a multiline period to a record boundary before classifying it.
	circular := period + period
	line := strings.Index(circular, "\\n")
	width := 2
	if line < 0 {
		line, width = strings.IndexByte(circular, '\n'), 1
	}
	if line >= 0 && line < len(period) {
		boundary := (line + width) % len(period)
		period = period[boundary:] + period[:boundary]
	}
	period = strings.TrimSpace(period)
	return period != "" && strings.ContainsRune("[{\"|", rune(period[0]))
}
