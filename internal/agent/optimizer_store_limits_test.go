package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOptimizerStoresRejectOversizedAndNullDurableState(t *testing.T) {
	optimizer := NewStrategyOptimizer(t.TempDir())
	file, err := os.Create(optimizer.storagePath())
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxOptimizerStoreFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_ = file.Close()
	if err := optimizer.load(); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized optimizer load error = %v", err)
	}
	if err := os.WriteFile(optimizer.storagePath(), []byte(`{"nil":null,"ok":{"strategy_name":"ok"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := optimizer.load(); err != nil || len(optimizer.metrics) != 1 || optimizer.metrics["ok"].TaskTypes == nil {
		t.Fatalf("null-safe strategy load = %#v, %v", optimizer.metrics, err)
	}

	prompts := NewPromptOptimizer(t.TempDir())
	if err := os.WriteFile(prompts.storagePath(), []byte(`{"nil":null,"ok":{"id":"ok","base_prompt":"base"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prompts.load(); err != nil || len(prompts.variants) != 1 {
		t.Fatalf("null-safe prompt load = %#v, %v", prompts.variants, err)
	}

	delegation := NewDelegationMetrics(t.TempDir())
	if err := os.WriteFile(delegation.storagePath(), []byte(`{"path_metrics":{"nil":null,"ok":{"recent_results":[]}},"rule_weights":{"ok":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := delegation.load(); err != nil || len(delegation.PathMetrics) != 1 {
		t.Fatalf("null-safe delegation load = %#v, %v", delegation.PathMetrics, err)
	}
}

func TestStrategyOptimizerLoadKeepsMostRecentMetricsWithinLimit(t *testing.T) {
	optimizer := NewStrategyOptimizer(t.TempDir())
	metrics := make(map[string]*StrategyMetrics, MaxStrategyMetrics+1)
	for i := 0; i <= MaxStrategyMetrics; i++ {
		name := fmt.Sprintf("strategy-%d", i)
		metrics[name] = &StrategyMetrics{
			StrategyName: name,
			LastUsed:     time.Unix(int64(i), 0),
		}
	}
	data, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(optimizer.storagePath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := optimizer.load(); err != nil {
		t.Fatal(err)
	}
	if got := len(optimizer.metrics); got != MaxStrategyMetrics {
		t.Fatalf("metric count = %d, want %d", got, MaxStrategyMetrics)
	}
	if _, found := optimizer.metrics["strategy-0"]; found {
		t.Fatal("oldest metric was not evicted")
	}
	if _, found := optimizer.metrics[fmt.Sprintf("strategy-%d", MaxStrategyMetrics)]; !found {
		t.Fatal("newest metric was evicted")
	}
}
