package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// PlanStore provides persistent storage for plan states.
type PlanStore struct {
	dir string
	mu  sync.RWMutex
}

// PlanInfo contains metadata about a stored plan.
type PlanInfo struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Status      Status    `json:"status"`
	StepCount   int       `json:"step_count"`
	Completed   int       `json:"completed"`
	Progress    float64   `json:"progress"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	WorkDir     string    `json:"work_dir"`
	Request     string    `json:"request"`
	IsResumable bool      `json:"is_resumable"`
}

// NewPlanStore creates a new plan store.
// configDir should be the base config directory (e.g., ~/.config/gokin).
func NewPlanStore(configDir string) (*PlanStore, error) {
	dir := filepath.Join(configDir, "plans")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create plans directory: %w", err)
	}

	return &PlanStore{
		dir: dir,
	}, nil
}

// Save saves a plan to disk.
func (s *PlanStore) Save(plan *Plan) error {
	if plan == nil {
		return fmt.Errorf("cannot save nil plan")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal plan: %w", err)
	}

	filePath := filepath.Join(s.dir, plan.ID+".json")
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write plan: %w", err)
	}

	return nil
}

// Load loads a plan from disk by ID.
func (s *PlanStore) Load(planID string) (*Plan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	filePath := filepath.Join(s.dir, planID+".json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("plan not found: %s", planID)
		}
		return nil, fmt.Errorf("failed to read plan: %w", err)
	}

	var plan Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("failed to unmarshal plan: %w", err)
	}

	return &plan, nil
}

// LoadLast loads the most recently updated plan that is resumable.
func (s *PlanStore) LoadLast() (*Plan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read plans directory: %w", err)
	}

	var latestPlan *Plan
	var latestTime time.Time

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		filePath := filepath.Join(s.dir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		plan := new(Plan)
		if err := json.Unmarshal(data, plan); err != nil {
			continue
		}

		// Only consider resumable plans (paused or in_progress with pending steps)
		if !s.isResumable(plan) {
			continue
		}

		if plan.UpdatedAt.After(latestTime) {
			latestTime = plan.UpdatedAt
			latestPlan = plan
		}
	}

	if latestPlan == nil {
		return nil, fmt.Errorf("no resumable plan found")
	}

	return latestPlan, nil
}

// isResumable checks if a plan can be resumed.
func (s *PlanStore) isResumable(plan *Plan) bool {
	if plan == nil {
		return false
	}

	// Paused plans are resumable
	if plan.Status == StatusPaused {
		return true
	}

	// In-progress plans with pending steps are resumable
	if plan.Status == StatusInProgress {
		for _, step := range plan.Steps {
			if step.Status == StatusPending || step.Status == StatusPaused {
				return true
			}
		}
	}

	// Failed plans with some steps incomplete are resumable
	if plan.Status == StatusFailed {
		for _, step := range plan.Steps {
			if step.Status == StatusPending || step.Status == StatusPaused || step.Status == StatusFailed {
				return true
			}
		}
	}

	return false
}

// List returns info about all stored plans.
func (s *PlanStore) List() ([]PlanInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read plans directory: %w", err)
	}

	var plans []PlanInfo
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		filePath := filepath.Join(s.dir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		var plan Plan
		if err := json.Unmarshal(data, &plan); err != nil {
			continue
		}

		plans = append(plans, PlanInfo{
			ID:          plan.ID,
			Title:       plan.Title,
			Status:      plan.Status,
			StepCount:   len(plan.Steps),
			Completed:   plan.CompletedCount(),
			Progress:    plan.Progress(),
			CreatedAt:   plan.CreatedAt,
			UpdatedAt:   plan.UpdatedAt,
			Request:     truncateString(plan.Request, 100),
			IsResumable: s.isResumable(&plan),
		})
	}

	// Sort by UpdatedAt descending (most recent first)
	sort.Slice(plans, func(i, j int) bool {
		return plans[i].UpdatedAt.After(plans[j].UpdatedAt)
	})

	return plans, nil
}

// ListResumable returns only resumable plans.
func (s *PlanStore) ListResumable() ([]PlanInfo, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}

	var resumable []PlanInfo
	for _, p := range all {
		if p.IsResumable {
			resumable = append(resumable, p)
		}
	}

	return resumable, nil
}

// Delete removes a plan from disk.
func (s *PlanStore) Delete(planID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	filePath := filepath.Join(s.dir, planID+".json")
	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to delete plan: %w", err)
	}

	return nil
}

// Cleanup removes plans older than the specified duration.
// Completed plans are removed after maxAge, paused plans are kept longer.
func (s *PlanStore) Cleanup(maxAge time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, fmt.Errorf("failed to read plans directory: %w", err)
	}

	cutoff := time.Now().Add(-maxAge)
	pausedCutoff := time.Now().Add(-maxAge * 3) // Keep paused plans 3x longer
	cleaned := 0

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		filePath := filepath.Join(s.dir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		var plan Plan
		if err := json.Unmarshal(data, &plan); err != nil {
			continue
		}

		// Use different cutoff for paused vs completed plans
		effectiveCutoff := cutoff
		if plan.Status == StatusPaused {
			effectiveCutoff = pausedCutoff
		}

		if plan.UpdatedAt.Before(effectiveCutoff) {
			if err := os.Remove(filePath); err == nil {
				cleaned++
			}
		}
	}

	return cleaned, nil
}

// Exists checks if a plan exists.
func (s *PlanStore) Exists(planID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	filePath := filepath.Join(s.dir, planID+".json")
	_, err := os.Stat(filePath)
	return err == nil
}

// truncateString truncates a string to maxLen with ellipsis.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
