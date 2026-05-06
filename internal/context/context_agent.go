package context

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gokin/internal/chat"
	"gokin/internal/fileutil"
	"gokin/internal/logging"
)

// ContextAgent monitors the ContextManager and automates context health.
type ContextAgent struct {
	manager *ContextManager
	session *chat.Session

	compactionThreshold float64 // e.g., 0.8 (80%)
	checkpointInterval  time.Duration
	storageDir          string

	// Predictive compaction: track token growth to compact before hitting limit
	prevTokens    int
	prevCheckTime time.Time
	growthRate    float64 // tokens per second (smoothed)

	mu       sync.Mutex
	stopChan chan struct{}
	stopOnce sync.Once
}

// NewContextAgent creates a new context health agent.
func NewContextAgent(m *ContextManager, s *chat.Session, storageDir string) *ContextAgent {
	return &ContextAgent{
		manager:             m,
		session:             s,
		compactionThreshold: 0.75, // Proactive compaction at 75%
		checkpointInterval:  10 * time.Minute,
		storageDir:          storageDir,
		stopChan:            make(chan struct{}),
	}
}

// Start begins the monitoring loop.
func (a *ContextAgent) Start(ctx context.Context) {
	logging.Info("ContextAgent started")

	checkpointTicker := time.NewTicker(a.checkpointInterval)
	defer checkpointTicker.Stop()

	compactTicker := time.NewTicker(1 * time.Minute)
	defer compactTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopChan:
			return
		case <-checkpointTicker.C:
			a.Checkpoint(ctx)
		case <-compactTicker.C:
			a.CheckAndCompact(ctx)
		}
	}
}

// Stop stops the agent. Safe to call more than once.
func (a *ContextAgent) Stop() {
	a.stopOnce.Do(func() { close(a.stopChan) })
}

// CheckAndCompact checks token usage and triggers compaction if threshold exceeded.
// Uses both threshold-based and predictive compaction strategies.
func (a *ContextAgent) CheckAndCompact(ctx context.Context) {
	usage := a.manager.GetTokenUsage()
	if usage == nil || usage.MaxTokens == 0 {
		return
	}

	currentTokens := usage.InputTokens
	ratio := float64(currentTokens) / float64(usage.MaxTokens)

	// Update growth rate estimate (exponential moving average).
	// Protected by mu — CheckAndCompact is called from the background ticker goroutine.
	a.mu.Lock()
	now := time.Now()
	if a.prevTokens > 0 && !a.prevCheckTime.IsZero() {
		elapsed := now.Sub(a.prevCheckTime).Seconds()
		if elapsed > 0 {
			instantRate := float64(currentTokens-a.prevTokens) / elapsed
			if a.growthRate == 0 {
				a.growthRate = instantRate
			} else {
				a.growthRate = a.growthRate*0.7 + instantRate*0.3 // EMA smoothing
			}
		}
	}
	a.prevTokens = currentTokens
	a.prevCheckTime = now
	growthRate := a.growthRate
	a.mu.Unlock()

	// Strategy 1: Threshold-based compaction (existing behavior)
	if ratio >= a.compactionThreshold {
		logging.Info("ContextAgent: threshold reached, starting auto-compaction",
			"ratio", fmt.Sprintf("%.2f", ratio))
		a.runCompaction(ctx)
		return
	}

	// Strategy 2: Predictive compaction — if growth rate suggests we'll hit the limit
	// within the next 2 minutes, compact preemptively.
	if growthRate > 0 && ratio > 0.5 {
		tokensRemaining := float64(usage.MaxTokens) - float64(currentTokens)
		secondsToLimit := tokensRemaining / growthRate
		if secondsToLimit < 120 { // Less than 2 minutes to limit
			logging.Info("ContextAgent: predictive compaction triggered",
				"ratio", fmt.Sprintf("%.2f", ratio),
				"growth_rate", fmt.Sprintf("%.0f tok/s", growthRate),
				"seconds_to_limit", fmt.Sprintf("%.0f", secondsToLimit))
			a.runCompaction(ctx)
			return
		}
	}
}

func (a *ContextAgent) runCompaction(ctx context.Context) {
	if err := a.manager.OptimizeContext(ctx); err != nil {
		// "Nothing to do" sentinels are not errors at this layer — auto-
		// compaction triggers on token count, and reaching a no-op state
		// (history too short / all pinned) is a normal outcome. Demote
		// to Debug so we don't pollute the error log.
		if errors.Is(err, ErrHistoryTooShort) || errors.Is(err, ErrNothingToSummarize) {
			logging.Debug("auto-compaction skipped — nothing to summarize", "reason", err)
		} else {
			logging.Error("auto-compaction failed", "error", err)
		}
	} else {
		logging.Info("auto-compaction successful")
		// Reset growth tracking after compaction (token count changed dramatically)
		a.mu.Lock()
		a.prevTokens = 0
		a.growthRate = 0
		a.mu.Unlock()
	}
}

// Checkpoint saves the current session state to disk.
func (a *ContextAgent) Checkpoint(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()

	history := a.session.GetHistory()
	if len(history) == 0 {
		return
	}

	checkpointDir := filepath.Join(a.storageDir, "checkpoints")
	if err := os.MkdirAll(checkpointDir, 0755); err != nil {
		logging.Error("failed to create checkpoint dir", "error", err)
		return
	}

	timestamp := time.Now().Format("20060102_150405")
	filename := filepath.Join(checkpointDir, fmt.Sprintf("cp_%s.json", timestamp))

	// Simplified state for checkpoint
	state := struct {
		Timestamp time.Time
		Tokens    int
		History   int // Count of messages
	}{
		Timestamp: time.Now(),
		Tokens:    a.manager.GetCurrentTokens(),
		History:   len(history),
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		logging.Error("failed to marshal checkpoint", "error", err)
		return
	}

	if err := fileutil.AtomicWrite(filename, data, 0644); err != nil {
		logging.Error("failed to write checkpoint", "error", err)
		return
	}

	// Keep only last 5 checkpoints
	a.rotateCheckpoints(checkpointDir, 5)

	logging.Debug("session checkpoint created", "file", filename)
}

func (a *ContextAgent) rotateCheckpoints(dir string, max int) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	if len(files) <= max {
		return
	}

	var checkpoints []os.FileInfo
	for _, f := range files {
		if !f.IsDir() && filepath.Ext(f.Name()) == ".json" {
			info, err := f.Info()
			if err != nil {
				continue
			}
			checkpoints = append(checkpoints, info)
		}
	}

	sort.Slice(checkpoints, func(i, j int) bool {
		return checkpoints[i].ModTime().Before(checkpoints[j].ModTime())
	})

	toDelete := len(checkpoints) - max
	for i := range toDelete {
		os.Remove(filepath.Join(dir, checkpoints[i].Name()))
	}
}
