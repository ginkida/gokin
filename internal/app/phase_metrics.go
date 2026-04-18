package app

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// PhaseMetrics records how long each request phase took across all model
// turns in the current session. Captures latency with enough granularity for
// /stats to show p50/p95 per phase, and keeps the buffer bounded so a long
// session doesn't accumulate unbounded memory.
//
// A "request" here is one user submission → final response. A single request
// normally contains many phases (router call, LLM API call, N tool calls).
// Per-phase samples are collected in a ring buffer of the last MaxSamples
// observations per phase; older samples are discarded.
//
// Not goroutine-safe by itself — all mutation goes through the mutex.
type PhaseMetrics struct {
	mu      sync.RWMutex
	samples map[Phase][]time.Duration
}

// Phase enumerates the measurable pieces of a single message processing run.
// The executor, router and LLM client each call Record for their slice.
type Phase string

const (
	PhaseRouter     Phase = "router"
	PhaseLLM        Phase = "llm"         // full streamed API call: TTFB → final chunk
	PhaseLLMTTFB    Phase = "llm_ttfb"    // time to first streamed byte
	PhaseTool       Phase = "tool"        // single tool invocation
	PhaseToolsTotal Phase = "tools_total" // all tool calls within one turn
	PhaseCompaction Phase = "compaction"
	PhaseEndToEnd   Phase = "end_to_end" // user submit → response done
)

// MaxSamples bounds each phase's ring buffer. 500 observations at a few
// hundred bytes each ≈ 100KB per phase, safe for long sessions.
const MaxSamples = 500

// NewPhaseMetrics returns an initialised empty collector.
func NewPhaseMetrics() *PhaseMetrics {
	return &PhaseMetrics{samples: make(map[Phase][]time.Duration)}
}

// Record appends a duration sample for the given phase. A zero or negative
// duration is silently ignored — callers frequently pass `time.Since(start)`
// where `start` may be the zero time after an early-return path.
func (m *PhaseMetrics) Record(phase Phase, d time.Duration) {
	if m == nil || d <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	buf := m.samples[phase]
	if len(buf) >= MaxSamples {
		// Shift — drop oldest. Small slice, copy is cheap.
		buf = append(buf[:0], buf[1:]...)
	}
	m.samples[phase] = append(buf, d)
}

// PhaseSnapshot is the aggregated view of one phase's samples.
type PhaseSnapshot struct {
	Phase  Phase
	Count  int
	P50    time.Duration
	P95    time.Duration
	Max    time.Duration
	Total  time.Duration
}

// Snapshot returns a deterministic list of phase snapshots sorted by phase
// name. Safe to call while other goroutines continue recording.
func (m *PhaseMetrics) Snapshot() []PhaseSnapshot {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]PhaseSnapshot, 0, len(m.samples))
	for phase, raw := range m.samples {
		if len(raw) == 0 {
			continue
		}
		buf := make([]time.Duration, len(raw))
		copy(buf, raw)
		sort.Slice(buf, func(i, j int) bool { return buf[i] < buf[j] })

		var total time.Duration
		for _, d := range buf {
			total += d
		}
		out = append(out, PhaseSnapshot{
			Phase: phase,
			Count: len(buf),
			P50:   buf[len(buf)/2],
			P95:   buf[p95Index(len(buf))],
			Max:   buf[len(buf)-1],
			Total: total,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Phase < out[j].Phase })
	return out
}

// Reset discards all samples — called on /clear so stats reflect the current
// conversation instead of accumulating across unrelated tasks.
func (m *PhaseMetrics) Reset() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.samples = make(map[Phase][]time.Duration)
}

// p95Index returns the zero-based index of the 95th percentile in a sorted
// buffer of length n. n ≥ 1 enforced by the caller (len(buf) == 0 returns
// early in Snapshot).
func p95Index(n int) int {
	idx := (n * 95) / 100
	if idx >= n {
		idx = n - 1
	}
	return idx
}

// GetPerformanceStats returns a pre-formatted multi-line string with phase
// latency percentiles and tool call statistics — ready to append to /stats
// output. Returns empty string if no samples collected yet.
func (a *App) GetPerformanceStats() string {
	var sb strings.Builder

	if a.phaseMetrics != nil {
		phases := a.phaseMetrics.Snapshot()
		if len(phases) > 0 {
			sb.WriteString("⏱️  Phase Latency (p50 / p95 / max)\n")
			for _, p := range phases {
				fmt.Fprintf(&sb, "  %-14s %3d × %7s / %7s / %7s\n",
					p.Phase, p.Count,
					p.P50.Round(time.Millisecond),
					p.P95.Round(time.Millisecond),
					p.Max.Round(time.Millisecond))
			}
			sb.WriteString("\n")
		}
	}

	if a.toolMetrics != nil {
		tools := a.toolMetrics.Snapshot()
		if len(tools) > 0 {
			sb.WriteString("🔧 Top Tools\n")
			max := 10
			if len(tools) < max {
				max = len(tools)
			}
			for _, t := range tools[:max] {
				fmt.Fprintf(&sb, "  %-22s %3d × %5.1f%% success   p95 %7s",
					t.Name, t.Calls, t.SuccessRate*100,
					t.P95.Round(time.Millisecond))
				if t.TopErrorKind != "" {
					fmt.Fprintf(&sb, "   fails: %s (%d)", t.TopErrorKind, t.TopErrorHits)
				}
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}
	}

	return sb.String()
}
