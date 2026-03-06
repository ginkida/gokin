package agent

import (
	"os"
	"testing"
	"time"
)

// testStrategyDir creates a temp dir that is not auto-cleaned by t.TempDir()
// to avoid race with async writeSnapshot goroutines.
func testStrategyDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "strategy-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		time.Sleep(50 * time.Millisecond) // Let async writes complete
		os.RemoveAll(dir)
	})
	return dir
}

func TestStrategyMetricsSuccessRate(t *testing.T) {
	m := &StrategyMetrics{}
	if m.SuccessRate() != 0.5 {
		t.Errorf("empty rate = %f, want 0.5", m.SuccessRate())
	}

	m.SuccessCount = 3
	m.FailureCount = 1
	if m.SuccessRate() != 0.75 {
		t.Errorf("3/4 rate = %f, want 0.75", m.SuccessRate())
	}
}

func TestStrategyMetricsClone(t *testing.T) {
	m := &StrategyMetrics{
		StrategyName: "explore",
		SuccessCount: 5,
		TaskTypes:    map[string]int{"coding": 3},
	}

	c := m.clone()
	if c.StrategyName != "explore" {
		t.Errorf("clone name = %q", c.StrategyName)
	}

	// Modify clone — should not affect original
	c.TaskTypes["coding"] = 99
	if m.TaskTypes["coding"] != 3 {
		t.Error("clone should be a deep copy")
	}
}

func TestStrategyOptimizerRecordAndGetRate(t *testing.T) {
	so := &StrategyOptimizer{
		metrics:   make(map[string]*StrategyMetrics),
		configDir: testStrategyDir(t),
	}

	so.RecordExecution("explore", "coding", true, time.Second)
	so.RecordExecution("explore", "coding", true, 2*time.Second)
	so.RecordExecution("explore", "coding", false, time.Second)

	rate := so.GetSuccessRate("explore")
	expected := 2.0 / 3.0
	if rate < expected-0.01 || rate > expected+0.01 {
		t.Errorf("rate = %f, want %f", rate, expected)
	}
}

func TestStrategyOptimizerDefaultRate(t *testing.T) {
	so := &StrategyOptimizer{
		metrics: make(map[string]*StrategyMetrics),
	}
	if so.GetSuccessRate("unknown") != 0.5 {
		t.Error("unknown strategy should return 0.5")
	}
}

func TestStrategyOptimizerGetMetrics(t *testing.T) {
	so := &StrategyOptimizer{
		metrics:   make(map[string]*StrategyMetrics),
		configDir: testStrategyDir(t),
	}

	so.RecordExecution("explore", "coding", true, time.Second)

	m, ok := so.GetMetrics("explore")
	if !ok {
		t.Fatal("should find metrics")
	}
	if m.SuccessCount != 1 {
		t.Errorf("SuccessCount = %d", m.SuccessCount)
	}

	// Should be a copy
	m.SuccessCount = 99
	m2, _ := so.GetMetrics("explore")
	if m2.SuccessCount == 99 {
		t.Error("GetMetrics should return deep copy")
	}

	_, ok = so.GetMetrics("nonexistent")
	if ok {
		t.Error("nonexistent should return false")
	}
}

func TestStrategyOptimizerGetAllMetrics(t *testing.T) {
	so := &StrategyOptimizer{
		metrics:   make(map[string]*StrategyMetrics),
		configDir: testStrategyDir(t),
	}

	so.RecordExecution("explore", "coding", true, time.Second)
	so.RecordExecution("bash", "testing", true, time.Second)

	all := so.GetAllMetrics()
	if len(all) != 2 {
		t.Errorf("all metrics = %d, want 2", len(all))
	}

	// Deep copy
	all["explore"].SuccessCount = 99
	m, _ := so.GetMetrics("explore")
	if m.SuccessCount == 99 {
		t.Error("GetAllMetrics should return deep copies")
	}
}

func TestStrategyOptimizerRecommendStrategy(t *testing.T) {
	so := &StrategyOptimizer{
		metrics:   make(map[string]*StrategyMetrics),
		configDir: testStrategyDir(t),
	}

	// No data → default "general"
	if so.RecommendStrategy("coding") != "general" {
		t.Errorf("no data = %q, want general", so.RecommendStrategy("coding"))
	}

	// Record data: explore is better at coding
	for i := 0; i < 10; i++ {
		so.RecordExecution("explore", "coding", true, time.Second)
	}
	for i := 0; i < 10; i++ {
		so.RecordExecution("bash", "coding", false, time.Second)
	}

	rec := so.RecommendStrategy("coding")
	if rec != "explore" {
		t.Errorf("recommended = %q, want explore (higher success rate)", rec)
	}
}

func TestStrategyOptimizerGetTopStrategies(t *testing.T) {
	so := &StrategyOptimizer{
		metrics:   make(map[string]*StrategyMetrics),
		configDir: testStrategyDir(t),
	}

	so.RecordExecution("a", "t", true, time.Second)  // 100%
	so.RecordExecution("b", "t", false, time.Second)  // 0%
	so.RecordExecution("c", "t", true, time.Second)   // 100%

	top := so.GetTopStrategies(2)
	if len(top) != 2 {
		t.Fatalf("top = %d, want 2", len(top))
	}

	// Top 2 should be the successful ones
	for _, m := range top {
		if m.SuccessRate() == 0 {
			t.Error("top strategies should not include 0% success rate")
		}
	}

	// Request more than available
	top = so.GetTopStrategies(10)
	if len(top) != 3 {
		t.Errorf("top(10) = %d, want 3 (all)", len(top))
	}
}

func TestStrategyOptimizerRecordUpdatesAvgDuration(t *testing.T) {
	so := &StrategyOptimizer{
		metrics:   make(map[string]*StrategyMetrics),
		configDir: testStrategyDir(t),
	}

	so.RecordExecution("test", "coding", true, 2*time.Second)
	so.RecordExecution("test", "coding", true, 4*time.Second)

	m, _ := so.GetMetrics("test")
	if m.AvgDuration != 3*time.Second {
		t.Errorf("AvgDuration = %v, want 3s", m.AvgDuration)
	}
	if m.TotalTime != 6*time.Second {
		t.Errorf("TotalTime = %v, want 6s", m.TotalTime)
	}
}

func TestStrategyOptimizerTaskTypes(t *testing.T) {
	so := &StrategyOptimizer{
		metrics:   make(map[string]*StrategyMetrics),
		configDir: testStrategyDir(t),
	}

	so.RecordExecution("explore", "coding", true, time.Second)
	so.RecordExecution("explore", "coding", true, time.Second)
	so.RecordExecution("explore", "testing", true, time.Second)

	m, _ := so.GetMetrics("explore")
	if m.TaskTypes["coding"] != 2 {
		t.Errorf("coding count = %d", m.TaskTypes["coding"])
	}
	if m.TaskTypes["testing"] != 1 {
		t.Errorf("testing count = %d", m.TaskTypes["testing"])
	}
}

func TestStrategyOptimizerClear(t *testing.T) {
	so := &StrategyOptimizer{
		metrics:   make(map[string]*StrategyMetrics),
		configDir: testStrategyDir(t),
	}

	so.RecordExecution("explore", "coding", true, time.Second)
	if err := so.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	all := so.GetAllMetrics()
	if len(all) != 0 {
		t.Errorf("after clear = %d", len(all))
	}
}
