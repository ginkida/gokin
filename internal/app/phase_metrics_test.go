package app

import (
	"testing"
	"time"
)

func TestPhaseMetrics_RecordAndSnapshot(t *testing.T) {
	m := NewPhaseMetrics()

	// Record a mix of fast and slow tool calls.
	for _, d := range []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		50 * time.Millisecond,
		100 * time.Millisecond,
		200 * time.Millisecond,
	} {
		m.Record(PhaseTool, d)
	}

	snaps := m.Snapshot()
	if len(snaps) != 1 {
		t.Fatalf("expected 1 phase snapshot, got %d", len(snaps))
	}
	s := snaps[0]
	if s.Phase != PhaseTool {
		t.Errorf("phase = %v, want %v", s.Phase, PhaseTool)
	}
	if s.Count != 5 {
		t.Errorf("count = %d, want 5", s.Count)
	}
	// P50 of 5 samples = index 2 = 50ms.
	if s.P50 != 50*time.Millisecond {
		t.Errorf("p50 = %v, want 50ms", s.P50)
	}
	// Max = largest.
	if s.Max != 200*time.Millisecond {
		t.Errorf("max = %v, want 200ms", s.Max)
	}
}

func TestPhaseMetrics_ZeroDurationIgnored(t *testing.T) {
	m := NewPhaseMetrics()
	m.Record(PhaseTool, 0)
	m.Record(PhaseTool, -1*time.Millisecond)
	if s := m.Snapshot(); len(s) != 0 {
		t.Errorf("zero/negative durations should be ignored, got %v", s)
	}
}

func TestPhaseMetrics_RingBufferBounds(t *testing.T) {
	m := NewPhaseMetrics()
	for i := 0; i < MaxSamples+50; i++ {
		m.Record(PhaseLLM, time.Duration(i)*time.Millisecond)
	}
	snaps := m.Snapshot()
	if snaps[0].Count != MaxSamples {
		t.Errorf("count = %d, want %d (ring buffer should drop oldest)", snaps[0].Count, MaxSamples)
	}
}

func TestPhaseMetrics_Reset(t *testing.T) {
	m := NewPhaseMetrics()
	m.Record(PhaseTool, 10*time.Millisecond)
	m.Reset()
	if s := m.Snapshot(); len(s) != 0 {
		t.Errorf("expected empty after reset, got %d phases", len(s))
	}
}

func TestPhaseMetrics_NilSafe(t *testing.T) {
	var m *PhaseMetrics
	m.Record(PhaseTool, 10*time.Millisecond) // must not panic
	if m.Snapshot() != nil {
		t.Error("nil Snapshot should return nil slice")
	}
	m.Reset() // must not panic
}

func TestToolMetrics_SuccessRate(t *testing.T) {
	m := NewToolMetrics()
	m.Record("read", 10*time.Millisecond, true, "")
	m.Record("read", 20*time.Millisecond, true, "")
	m.Record("read", 30*time.Millisecond, false, "permission")
	m.Record("write", 50*time.Millisecond, true, "")

	snaps := m.Snapshot()
	if len(snaps) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(snaps))
	}
	// Sorted by call count desc — read has 3 calls.
	if snaps[0].Name != "read" {
		t.Errorf("first tool = %q, want read", snaps[0].Name)
	}
	if snaps[0].Calls != 3 {
		t.Errorf("read calls = %d, want 3", snaps[0].Calls)
	}
	want := 2.0 / 3.0
	if snaps[0].SuccessRate < want-0.001 || snaps[0].SuccessRate > want+0.001 {
		t.Errorf("read success rate = %v, want ~%v", snaps[0].SuccessRate, want)
	}
	if snaps[0].TopErrorKind != "permission" {
		t.Errorf("top error = %q, want permission", snaps[0].TopErrorKind)
	}
}

func TestToolMetrics_EmptyToolIgnored(t *testing.T) {
	m := NewToolMetrics()
	m.Record("", 10*time.Millisecond, true, "")
	if s := m.Snapshot(); len(s) != 0 {
		t.Errorf("empty tool name should be ignored, got %d", len(s))
	}
}

func TestToolMetrics_NilSafe(t *testing.T) {
	var m *ToolMetrics
	m.Record("read", 10*time.Millisecond, true, "") // must not panic
	if m.Snapshot() != nil {
		t.Error("nil Snapshot should return nil")
	}
	m.Reset() // must not panic
}

func TestP95Index(t *testing.T) {
	cases := []struct {
		n    int
		want int
	}{
		{1, 0},
		{10, 9},
		{20, 19},
		{100, 95},
		{1000, 950},
	}
	for _, c := range cases {
		got := p95Index(c.n)
		if got != c.want {
			t.Errorf("p95Index(%d) = %d, want %d", c.n, got, c.want)
		}
	}
}
