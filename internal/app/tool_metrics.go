package app

import (
	"sort"
	"sync"
	"time"
)

// ToolMetrics tracks per-tool invocation counts, success rate, and duration
// samples. Data lives in-memory for the session — for cross-session stats the
// ExampleStore already persists tool sequences.
//
// Goals:
//   - /stats can show "grep called 47 times, 96% success, p95 120ms"
//   - Detect tools that systematically fail without failing loudly
//   - Provide aggregate for future autoscale decisions (batch vs serial)
//
// Granularity: one ToolStats per tool name. Error messages are bucketed by
// short classifier ("permission", "timeout", "not found", "other") rather
// than stored verbatim — full error text would bloat memory fast.
type ToolMetrics struct {
	mu    sync.RWMutex
	stats map[string]*toolStats
}

type toolStats struct {
	successCount int
	failureCount int
	totalTime    time.Duration
	durations    []time.Duration // ring buffer for p50/p95
	errorBuckets map[string]int
	lastUsed     time.Time
}

// MaxToolDurationSamples bounds per-tool ring buffer size. 200 × ~8 bytes
// ≈ 1.6 KB per tool; at 50 tools that's under 100 KB.
const MaxToolDurationSamples = 200

// NewToolMetrics returns an empty collector.
func NewToolMetrics() *ToolMetrics {
	return &ToolMetrics{stats: make(map[string]*toolStats)}
}

// Record updates counters for a single tool invocation. Called from the
// executor via phase observer. `errorKind` must be "" on success.
func (m *ToolMetrics) Record(tool string, d time.Duration, success bool, errorKind string) {
	if m == nil || tool == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	s := m.stats[tool]
	if s == nil {
		s = &toolStats{errorBuckets: map[string]int{}}
		m.stats[tool] = s
	}
	s.lastUsed = time.Now()
	s.totalTime += d

	if len(s.durations) >= MaxToolDurationSamples {
		s.durations = append(s.durations[:0], s.durations[1:]...)
	}
	s.durations = append(s.durations, d)

	if success {
		s.successCount++
		return
	}
	s.failureCount++
	if errorKind == "" {
		errorKind = "other"
	}
	s.errorBuckets[errorKind]++
}

// ToolSnapshot is the aggregated stat for one tool.
type ToolSnapshot struct {
	Name         string
	Calls        int
	SuccessRate  float64
	TotalTime    time.Duration
	AvgTime      time.Duration
	P50          time.Duration
	P95          time.Duration
	TopErrorKind string
	TopErrorHits int
	LastUsed     time.Time
}

// Snapshot returns all known tools, sorted by call count descending.
func (m *ToolMetrics) Snapshot() []ToolSnapshot {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]ToolSnapshot, 0, len(m.stats))
	for name, s := range m.stats {
		calls := s.successCount + s.failureCount
		if calls == 0 {
			continue
		}
		buf := make([]time.Duration, len(s.durations))
		copy(buf, s.durations)
		sort.Slice(buf, func(i, j int) bool { return buf[i] < buf[j] })

		snap := ToolSnapshot{
			Name:        name,
			Calls:       calls,
			SuccessRate: float64(s.successCount) / float64(calls),
			TotalTime:   s.totalTime,
			AvgTime:     s.totalTime / time.Duration(calls),
			LastUsed:    s.lastUsed,
		}
		if len(buf) > 0 {
			snap.P50 = buf[len(buf)/2]
			snap.P95 = buf[p95Index(len(buf))]
		}
		for kind, hits := range s.errorBuckets {
			if hits > snap.TopErrorHits {
				snap.TopErrorHits = hits
				snap.TopErrorKind = kind
			}
		}
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Reset clears all metrics — used on /clear.
func (m *ToolMetrics) Reset() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stats = make(map[string]*toolStats)
}

// MinSamplesForStats is the minimum number of observations required before
// Lookup reports a stable p95 / success rate. Below this the caller is told
// ok=false and should fall back to static defaults — otherwise a single
// unlucky failure would flag an otherwise-reliable tool.
const MinSamplesForStats = 5

// Lookup returns observed p95 latency and success rate for a tool. Reports
// ok=false if the tool has fewer than MinSamplesForStats recorded calls so
// adaptive policies don't react to noise from single-digit sample sizes.
func (m *ToolMetrics) Lookup(tool string) (p95 time.Duration, successRate float64, ok bool) {
	if m == nil || tool == "" {
		return 0, 0, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	s := m.stats[tool]
	if s == nil {
		return 0, 0, false
	}
	calls := s.successCount + s.failureCount
	if calls < MinSamplesForStats {
		return 0, 0, false
	}
	// Compute p95 on a local copy to avoid holding the lock through sorting
	// a long buffer — durations slice could be up to MaxToolDurationSamples.
	buf := make([]time.Duration, len(s.durations))
	copy(buf, s.durations)
	sort.Slice(buf, func(i, j int) bool { return buf[i] < buf[j] })
	if len(buf) == 0 {
		return 0, float64(s.successCount) / float64(calls), true
	}
	return buf[p95Index(len(buf))], float64(s.successCount) / float64(calls), true
}
