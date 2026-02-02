package context

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gokin/internal/chat"
	"gokin/internal/logging"
)

// ContextAgent monitors the ContextManager and automates context health.
type ContextAgent struct {
	manager *ContextManager
	session *chat.Session

	compactionThreshold float64 // e.g., 0.8 (80%)
	checkpointInterval  time.Duration
	storageDir          string

	mu       sync.Mutex
	stopChan chan struct{}
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

	ticker := time.NewTicker(a.checkpointInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopChan:
			return
		case <-ticker.C:
			a.Checkpoint(ctx)
		case <-time.After(1 * time.Minute):
			// Every minute check if we need compaction
			a.CheckAndCompact(ctx)
		}
	}
}

// Stop stops the agent.
func (a *ContextAgent) Stop() {
	close(a.stopChan)
}

// CheckAndCompact checks token usage and triggers compaction if threshold exceeded.
func (a *ContextAgent) CheckAndCompact(ctx context.Context) {
	usage := a.manager.GetTokenUsage()
	if usage == nil || usage.MaxTokens == 0 {
		return
	}

	ratio := float64(usage.InputTokens) / float64(usage.MaxTokens)
	if ratio >= a.compactionThreshold {
		logging.Info("ContextAgent: threshold reached, starting auto-compaction",
			"ratio", fmt.Sprintf("%.2f", ratio))

		if err := a.manager.OptimizeContext(ctx); err != nil {
			logging.Error("auto-compaction failed", "error", err)
		} else {
			logging.Info("auto-compaction successful")
		}
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

	if err := os.WriteFile(filename, data, 0644); err != nil {
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
			info, _ := f.Info()
			checkpoints = append(checkpoints, info)
		}
	}

	sort.Slice(checkpoints, func(i, j int) bool {
		return checkpoints[i].ModTime().Before(checkpoints[j].ModTime())
	})

	toDelete := len(checkpoints) - max
	for i := 0; i < toDelete; i++ {
		os.Remove(filepath.Join(dir, checkpoints[i].Name()))
	}
}
