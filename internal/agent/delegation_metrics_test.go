package agent

import (
	"os"
	"testing"
	"time"
)

// testMetricsDir creates a temp dir with delayed cleanup
// to avoid race with async writeSnapshot goroutines.
func testMetricsDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "metrics-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		time.Sleep(50 * time.Millisecond)
		os.RemoveAll(dir)
	})
	return dir
}

func TestBuildPathKey(t *testing.T) {
	key := buildPathKey("explore", "bash", "coding")
	if key != "explore:bash:coding" {
		t.Errorf("key = %q", key)
	}
}

func TestCalculateSuccessRate(t *testing.T) {
	// Empty
	if calculateSuccessRate(nil) != 0.5 {
		t.Error("empty should return 0.5")
	}

	results := []DelegationResult{
		{Success: true},
		{Success: true},
		{Success: false},
	}
	rate := calculateSuccessRate(results)
	expected := 2.0 / 3.0
	if rate < expected-0.01 || rate > expected+0.01 {
		t.Errorf("rate = %f, want %f", rate, expected)
	}
}

func TestDelegationMetricsRecordAndGetRate(t *testing.T) {
	dm := &DelegationMetrics{
		PathMetrics: make(map[string]*PathStats),
		RuleWeights: make(map[string]float64),
		configDir:   testMetricsDir(t),
	}

	dm.RecordExecution("explore", "bash", "coding", true, time.Second, "")
	dm.RecordExecution("explore", "bash", "coding", true, time.Second, "")
	dm.RecordExecution("explore", "bash", "coding", false, time.Second, "timeout")

	rate := dm.GetSuccessRate("explore", "bash", "coding")
	expected := 2.0 / 3.0
	if rate < expected-0.01 || rate > expected+0.01 {
		t.Errorf("success rate = %f, want %f", rate, expected)
	}
}

func TestDelegationMetricsDefaultRate(t *testing.T) {
	dm := &DelegationMetrics{
		PathMetrics: make(map[string]*PathStats),
		RuleWeights: make(map[string]float64),
	}

	rate := dm.GetSuccessRate("x", "y", "z")
	if rate != 0.5 {
		t.Errorf("unknown path rate = %f, want 0.5", rate)
	}
}

func TestDelegationMetricsRuleWeight(t *testing.T) {
	dm := &DelegationMetrics{
		PathMetrics: make(map[string]*PathStats),
		RuleWeights: make(map[string]float64),
		configDir:   testMetricsDir(t),
	}

	// Default weight
	w := dm.GetRuleWeight("a", "b", "c")
	if w != 1.0 {
		t.Errorf("default weight = %f", w)
	}

	// Record success → weight increases
	dm.RecordExecution("a", "b", "c", true, time.Second, "")
	w = dm.GetRuleWeight("a", "b", "c")
	if w <= 1.0 {
		t.Errorf("weight after success = %f, should be > 1.0", w)
	}

	// Record failure → weight decreases
	dm2 := &DelegationMetrics{
		PathMetrics: make(map[string]*PathStats),
		RuleWeights: make(map[string]float64),
		configDir:   testMetricsDir(t),
	}
	dm2.RecordExecution("a", "b", "c", false, time.Second, "")
	w2 := dm2.GetRuleWeight("a", "b", "c")
	if w2 >= 1.0 {
		t.Errorf("weight after failure = %f, should be < 1.0", w2)
	}
}

func TestDelegationMetricsRuleWeightClamp(t *testing.T) {
	dm := &DelegationMetrics{
		PathMetrics: make(map[string]*PathStats),
		RuleWeights: make(map[string]float64),
		configDir:   testMetricsDir(t),
	}

	// Many successes — weight should not exceed 2.0
	for i := 0; i < 100; i++ {
		dm.RecordExecution("a", "b", "c", true, time.Millisecond, "")
	}
	w := dm.GetRuleWeight("a", "b", "c")
	if w > 2.0 {
		t.Errorf("weight = %f, should be clamped to 2.0", w)
	}

	// Many failures — weight should not go below 0.5
	dm2 := &DelegationMetrics{
		PathMetrics: make(map[string]*PathStats),
		RuleWeights: make(map[string]float64),
		configDir:   testMetricsDir(t),
	}
	for i := 0; i < 100; i++ {
		dm2.RecordExecution("a", "b", "c", false, time.Millisecond, "")
	}
	w = dm2.GetRuleWeight("a", "b", "c")
	if w < 0.5 {
		t.Errorf("weight = %f, should be clamped to 0.5", w)
	}
}

func TestDelegationMetricsRecentTrend(t *testing.T) {
	dm := &DelegationMetrics{
		PathMetrics: make(map[string]*PathStats),
		RuleWeights: make(map[string]float64),
		configDir:   testMetricsDir(t),
	}

	// Not enough data
	trend := dm.GetRecentTrend("a", "b", "c")
	if trend != 0 {
		t.Errorf("insufficient data trend = %f, want 0", trend)
	}

	// Record enough data: first half failures, second half successes (improving)
	for i := 0; i < 5; i++ {
		dm.RecordExecution("a", "b", "c", false, time.Second, "")
	}
	for i := 0; i < 5; i++ {
		dm.RecordExecution("a", "b", "c", true, time.Second, "")
	}

	trend = dm.GetRecentTrend("a", "b", "c")
	if trend <= 0 {
		t.Errorf("improving trend = %f, should be positive", trend)
	}
}

func TestDelegationMetricsGetBestTarget(t *testing.T) {
	dm := &DelegationMetrics{
		PathMetrics: make(map[string]*PathStats),
		RuleWeights: make(map[string]float64),
		configDir:   testMetricsDir(t),
	}

	// No data — returns first candidate
	best := dm.GetBestTarget("explore", "coding", []string{"bash", "general"})
	if best != "bash" {
		t.Errorf("no data best = %q, want first candidate", best)
	}

	// Empty candidates
	if dm.GetBestTarget("x", "y", nil) != "" {
		t.Error("empty candidates should return empty")
	}

	// Record data for "general" being better
	for i := 0; i < 10; i++ {
		dm.RecordExecution("explore", "general", "coding", true, time.Second, "")
	}
	for i := 0; i < 10; i++ {
		dm.RecordExecution("explore", "bash", "coding", false, time.Second, "")
	}

	best = dm.GetBestTarget("explore", "coding", []string{"bash", "general"})
	if best != "general" {
		t.Errorf("best = %q, want general (higher success rate)", best)
	}
}

func TestDelegationMetricsShouldUseDelegation(t *testing.T) {
	dm := &DelegationMetrics{
		PathMetrics: make(map[string]*PathStats),
		RuleWeights: make(map[string]float64),
		configDir:   testMetricsDir(t),
	}

	// No data — default rate 0.5, weight 1.0, threshold 0.3 → should use
	if !dm.ShouldUseDelegation("a", "b", "c") {
		t.Error("should use delegation with neutral default")
	}

	// Record many failures → low success rate → should not use
	for i := 0; i < 20; i++ {
		dm.RecordExecution("x", "y", "z", false, time.Second, "")
	}
	if dm.ShouldUseDelegation("x", "y", "z") {
		t.Error("should not use delegation with very low success rate")
	}
}

func TestDelegationMetricsGetStats(t *testing.T) {
	dm := &DelegationMetrics{
		PathMetrics: make(map[string]*PathStats),
		RuleWeights: make(map[string]float64),
		configDir:   testMetricsDir(t),
	}

	dm.RecordExecution("a", "b", "c", true, time.Second, "")
	dm.RecordExecution("a", "b", "c", false, time.Second, "")

	stats := dm.GetStats()
	if stats["total_paths"].(int) != 1 {
		t.Errorf("total_paths = %v", stats["total_paths"])
	}
	if stats["total_executions"].(int) != 2 {
		t.Errorf("total_executions = %v", stats["total_executions"])
	}
	rate := stats["overall_success_rate"].(float64)
	if rate != 0.5 {
		t.Errorf("rate = %f", rate)
	}
}

func TestDelegationMetricsRecentResultsTrimming(t *testing.T) {
	dm := &DelegationMetrics{
		PathMetrics: make(map[string]*PathStats),
		RuleWeights: make(map[string]float64),
		configDir:   testMetricsDir(t),
	}

	// Record more than MaxRecentResults
	for i := 0; i < MaxRecentResults+10; i++ {
		dm.RecordExecution("a", "b", "c", true, time.Millisecond, "")
	}

	key := buildPathKey("a", "b", "c")
	dm.mu.RLock()
	recentLen := len(dm.PathMetrics[key].RecentResults)
	dm.mu.RUnlock()

	if recentLen > MaxRecentResults {
		t.Errorf("recent results = %d, should be trimmed to %d", recentLen, MaxRecentResults)
	}
}

func TestDelegationMetricsEviction(t *testing.T) {
	dm := &DelegationMetrics{
		PathMetrics: make(map[string]*PathStats),
		RuleWeights: make(map[string]float64),
		configDir:   testMetricsDir(t),
	}

	// Fill beyond MaxDelegationPaths
	for i := 0; i < MaxDelegationPaths+10; i++ {
		from := "agent"
		to := "target"
		ctx := string(rune('a' + (i % 26)))
		ctx += string(rune('0' + (i / 26)))
		dm.RecordExecution(from, to, ctx, true, time.Millisecond, "")
	}

	dm.mu.RLock()
	count := len(dm.PathMetrics)
	dm.mu.RUnlock()

	if count > MaxDelegationPaths {
		t.Errorf("paths = %d, should be evicted to %d", count, MaxDelegationPaths)
	}
}

func TestDelegationMetricsClear(t *testing.T) {
	dm := &DelegationMetrics{
		PathMetrics: make(map[string]*PathStats),
		RuleWeights: make(map[string]float64),
		configDir:   testMetricsDir(t),
	}

	dm.RecordExecution("a", "b", "c", true, time.Second, "")
	if err := dm.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	dm.mu.RLock()
	count := len(dm.PathMetrics)
	dm.mu.RUnlock()

	if count != 0 {
		t.Errorf("after clear, paths = %d", count)
	}
}
