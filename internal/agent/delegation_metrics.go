package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gokin/internal/logging"
)

// DelegationMetrics tracks success/failure rates for delegation decisions.
type DelegationMetrics struct {
	// Metrics by delegation path: "from_agent:to_agent:context_type" -> stats
	PathMetrics map[string]*PathStats `json:"path_metrics"`

	// Rule weights that are adjusted based on historical performance
	RuleWeights map[string]float64 `json:"rule_weights"`

	// Last update timestamp
	UpdatedAt time.Time `json:"updated_at"`

	configDir string
	mu        sync.RWMutex
}

// PathStats tracks statistics for a specific delegation path.
type PathStats struct {
	FromAgent   string `json:"from_agent"`
	ToAgent     string `json:"to_agent"`
	ContextType string `json:"context_type"`

	SuccessCount int           `json:"success_count"`
	FailureCount int           `json:"failure_count"`
	TotalTime    time.Duration `json:"total_time"`

	// Recent executions for trend analysis
	RecentResults []DelegationResult `json:"recent_results"`

	LastUsed time.Time `json:"last_used"`
}

// DelegationResult represents a single delegation execution result.
type DelegationResult struct {
	Success   bool          `json:"success"`
	Duration  time.Duration `json:"duration"`
	Timestamp time.Time     `json:"timestamp"`
	ErrorType string        `json:"error_type,omitempty"`
}

const (
	// MaxRecentResults limits the number of recent results to track
	MaxRecentResults = 20

	// MinSamplesForConfidence is the minimum number of samples needed for confident decisions
	MinSamplesForConfidence = 5

	// MaxDelegationPaths is the maximum number of delegation paths to track.
	MaxDelegationPaths = 200
)

// NewDelegationMetrics creates a new delegation metrics tracker.
func NewDelegationMetrics(configDir string) *DelegationMetrics {
	dm := &DelegationMetrics{
		PathMetrics: make(map[string]*PathStats),
		RuleWeights: make(map[string]float64),
		configDir:   configDir,
	}

	// Load existing metrics
	if err := dm.load(); err != nil {
		logging.Debug("failed to load delegation metrics", "error", err)
	}

	return dm
}

// storagePath returns the path to the metrics file.
func (dm *DelegationMetrics) storagePath() string {
	return filepath.Join(dm.configDir, "memory", "delegation_metrics.json")
}

// load loads metrics from disk.
func (dm *DelegationMetrics) load() error {
	data, err := os.ReadFile(dm.storagePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var loaded DelegationMetrics
	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}

	dm.PathMetrics = loaded.PathMetrics
	dm.RuleWeights = loaded.RuleWeights
	dm.UpdatedAt = loaded.UpdatedAt

	return nil
}

// save persists metrics to disk.
// Caller must hold dm.mu write lock (writes UpdatedAt).
func (dm *DelegationMetrics) save() ([]byte, error) {
	dm.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(dm, "", "  ")
	if err != nil {
		return nil, err
	}

	return data, nil
}

// writeSnapshot writes pre-serialized data to disk without holding any locks.
func (dm *DelegationMetrics) writeSnapshot(data []byte) error {
	dir := filepath.Dir(dm.storagePath())
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(dm.storagePath(), data, 0644)
}

// RecordExecution records the outcome of a delegation.
func (dm *DelegationMetrics) RecordExecution(fromAgent, toAgent, contextType string, success bool, duration time.Duration, errorType string) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	key := buildPathKey(fromAgent, toAgent, contextType)

	stats, ok := dm.PathMetrics[key]
	if !ok {
		stats = &PathStats{
			FromAgent:     fromAgent,
			ToAgent:       toAgent,
			ContextType:   contextType,
			LastUsed:      time.Now(), // Initialize before eviction to avoid evicting new entry
			RecentResults: make([]DelegationResult, 0, MaxRecentResults),
		}
		dm.PathMetrics[key] = stats

		// Evict oldest paths if over limit
		if len(dm.PathMetrics) > MaxDelegationPaths {
			dm.evictOldest(MaxDelegationPaths)
		}
	}

	// Update counts
	if success {
		stats.SuccessCount++
	} else {
		stats.FailureCount++
	}
	stats.TotalTime += duration
	stats.LastUsed = time.Now()

	// Add to recent results
	result := DelegationResult{
		Success:   success,
		Duration:  duration,
		Timestamp: time.Now(),
		ErrorType: errorType,
	}
	stats.RecentResults = append(stats.RecentResults, result)

	// Trim to max recent results
	if len(stats.RecentResults) > MaxRecentResults {
		stats.RecentResults = stats.RecentResults[len(stats.RecentResults)-MaxRecentResults:]
	}

	// Update rule weights based on new data
	dm.updateRuleWeight(key, success)

	// Snapshot data under lock, write to disk asynchronously
	snapshot, err := dm.save()
	if err != nil {
		logging.Debug("failed to serialize delegation metrics", "error", err)
		return
	}
	go func() {
		if err := dm.writeSnapshot(snapshot); err != nil {
			logging.Debug("failed to save delegation metrics", "error", err)
		}
	}()
}

// evictOldest removes the oldest paths by LastUsed until map is at maxSize.
// Caller must hold dm.mu write lock.
func (dm *DelegationMetrics) evictOldest(maxSize int) {
	if len(dm.PathMetrics) <= maxSize {
		return
	}

	type entry struct {
		key      string
		lastUsed time.Time
	}
	entries := make([]entry, 0, len(dm.PathMetrics))
	for k, v := range dm.PathMetrics {
		entries = append(entries, entry{k, v.LastUsed})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].lastUsed.Before(entries[j].lastUsed)
	})

	toRemove := len(dm.PathMetrics) - maxSize
	for i := 0; i < toRemove; i++ {
		key := entries[i].key
		delete(dm.PathMetrics, key)
		delete(dm.RuleWeights, key)
	}
}

// updateRuleWeight adjusts the weight of a delegation rule based on outcomes.
func (dm *DelegationMetrics) updateRuleWeight(key string, success bool) {
	// Initialize weight if not present
	if _, ok := dm.RuleWeights[key]; !ok {
		dm.RuleWeights[key] = 1.0 // Default neutral weight
	}

	// Adjust weight using exponential moving average
	// Success increases weight, failure decreases it
	alpha := 0.1 // Learning rate
	if success {
		dm.RuleWeights[key] = dm.RuleWeights[key]*(1-alpha) + 1.2*alpha
	} else {
		dm.RuleWeights[key] = dm.RuleWeights[key]*(1-alpha) + 0.8*alpha
	}

	// Clamp to reasonable range
	if dm.RuleWeights[key] < 0.5 {
		dm.RuleWeights[key] = 0.5
	}
	if dm.RuleWeights[key] > 2.0 {
		dm.RuleWeights[key] = 2.0
	}
}

// GetSuccessRate returns the success rate for a delegation path.
func (dm *DelegationMetrics) GetSuccessRate(fromAgent, toAgent, contextType string) float64 {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	key := buildPathKey(fromAgent, toAgent, contextType)
	stats, ok := dm.PathMetrics[key]
	if !ok {
		return 0.5 // Default neutral
	}

	total := stats.SuccessCount + stats.FailureCount
	if total == 0 {
		return 0.5
	}

	return float64(stats.SuccessCount) / float64(total)
}

// GetRuleWeight returns the weight for a delegation rule.
func (dm *DelegationMetrics) GetRuleWeight(fromAgent, toAgent, contextType string) float64 {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	key := buildPathKey(fromAgent, toAgent, contextType)
	weight, ok := dm.RuleWeights[key]
	if !ok {
		return 1.0 // Default neutral
	}

	return weight
}

// GetRecentTrend analyzes recent executions to determine if performance is improving or declining.
// Returns a value from -1.0 (declining) to 1.0 (improving).
func (dm *DelegationMetrics) GetRecentTrend(fromAgent, toAgent, contextType string) float64 {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	return dm.getRecentTrendLocked(fromAgent, toAgent, contextType)
}

// getRecentTrendLocked is the lock-free internal version of GetRecentTrend.
// Caller must hold dm.mu (read or write).
func (dm *DelegationMetrics) getRecentTrendLocked(fromAgent, toAgent, contextType string) float64 {
	key := buildPathKey(fromAgent, toAgent, contextType)
	stats, ok := dm.PathMetrics[key]
	if !ok || len(stats.RecentResults) < MinSamplesForConfidence {
		return 0 // Not enough data
	}

	// Compare first half to second half of recent results
	mid := len(stats.RecentResults) / 2
	firstHalf := stats.RecentResults[:mid]
	secondHalf := stats.RecentResults[mid:]

	firstSuccessRate := calculateSuccessRate(firstHalf)
	secondSuccessRate := calculateSuccessRate(secondHalf)

	return secondSuccessRate - firstSuccessRate
}

// GetBestTarget returns the best delegation target for a context type based on historical data.
func (dm *DelegationMetrics) GetBestTarget(fromAgent, contextType string, candidates []string) string {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	if len(candidates) == 0 {
		return ""
	}

	bestCandidate := candidates[0]
	bestScore := 0.0

	for _, candidate := range candidates {
		key := buildPathKey(fromAgent, candidate, contextType)

		// Calculate weighted score
		stats, ok := dm.PathMetrics[key]
		if !ok {
			continue
		}

		total := stats.SuccessCount + stats.FailureCount
		if total < MinSamplesForConfidence {
			continue // Not enough data
		}

		successRate := float64(stats.SuccessCount) / float64(total)
		weight := dm.RuleWeights[key]
		trend := dm.getRecentTrendLocked(fromAgent, candidate, contextType)

		// Combined score: weighted success rate + trend bonus
		score := successRate*weight + trend*0.1

		if score > bestScore {
			bestScore = score
			bestCandidate = candidate
		}
	}

	return bestCandidate
}

// ShouldUseDelegation returns whether delegation should be used based on historical performance.
func (dm *DelegationMetrics) ShouldUseDelegation(fromAgent, toAgent, contextType string) bool {
	dm.mu.RLock()
	key := buildPathKey(fromAgent, toAgent, contextType)

	// Read all values atomically under a single lock
	stats, ok := dm.PathMetrics[key]
	var successRate float64
	if !ok {
		successRate = 0.5
	} else {
		total := stats.SuccessCount + stats.FailureCount
		if total == 0 {
			successRate = 0.5
		} else {
			successRate = float64(stats.SuccessCount) / float64(total)
		}
	}

	weight, wOk := dm.RuleWeights[key]
	if !wOk {
		weight = 1.0
	}

	trend := dm.getRecentTrendLocked(fromAgent, toAgent, contextType)
	dm.mu.RUnlock()

	// Calculate decision threshold
	threshold := 0.3 // Minimum success rate to continue using delegation

	// Adjust threshold based on trend
	if trend < -0.2 {
		threshold = 0.4 // Higher threshold if declining
	} else if trend > 0.2 {
		threshold = 0.2 // Lower threshold if improving
	}

	return successRate*weight >= threshold
}

// GetStats returns overall statistics.
func (dm *DelegationMetrics) GetStats() map[string]any {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	totalPaths := len(dm.PathMetrics)
	totalExecutions := 0
	totalSuccesses := 0

	for _, stats := range dm.PathMetrics {
		totalExecutions += stats.SuccessCount + stats.FailureCount
		totalSuccesses += stats.SuccessCount
	}

	overallSuccessRate := 0.0
	if totalExecutions > 0 {
		overallSuccessRate = float64(totalSuccesses) / float64(totalExecutions)
	}

	return map[string]any{
		"total_paths":          totalPaths,
		"total_executions":     totalExecutions,
		"overall_success_rate": overallSuccessRate,
		"last_updated":         dm.UpdatedAt,
	}
}

// Clear removes all metrics.
func (dm *DelegationMetrics) Clear() error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	dm.PathMetrics = make(map[string]*PathStats)
	dm.RuleWeights = make(map[string]float64)

	snapshot, err := dm.save()
	if err != nil {
		return err
	}
	return dm.writeSnapshot(snapshot)
}

// Helper functions

func buildPathKey(fromAgent, toAgent, contextType string) string {
	return fromAgent + ":" + toAgent + ":" + contextType
}

func calculateSuccessRate(results []DelegationResult) float64 {
	if len(results) == 0 {
		return 0.5
	}

	successes := 0
	for _, r := range results {
		if r.Success {
			successes++
		}
	}

	return float64(successes) / float64(len(results))
}
