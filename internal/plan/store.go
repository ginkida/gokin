package plan

import (
	"encoding/json"
	"errors"
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
	_, err := inspectStoredPlanFiles(dir, true)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare plans directory: %w", err)
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

	// Snapshot plan data under plan's lock to prevent data races
	// with concurrent CompleteStep/FailStep/StartStep calls.
	plan.mu.RLock()
	if err := validatePlanID(plan.ID); err != nil {
		plan.mu.RUnlock()
		return err
	}
	if err := validatePlanStructure(plan); err != nil {
		plan.mu.RUnlock()
		return err
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	planID := plan.ID
	plan.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("failed to marshal plan: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	filePath, err := planPath(s.dir, planID)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(filePath); os.IsNotExist(err) {
		files, inspectErr := inspectStoredPlanFiles(s.dir, false)
		if inspectErr != nil {
			return fmt.Errorf("failed to inspect plan store: %w", inspectErr)
		}
		if len(files) >= maxStoredPlans {
			return fmt.Errorf("plan store reached %d-file limit", maxStoredPlans)
		}
	} else if err != nil {
		return fmt.Errorf("failed to inspect plan path: %w", err)
	}
	if err := writePrivatePlanFile(filePath, data); err != nil {
		return fmt.Errorf("failed to write plan: %w", err)
	}

	return nil
}

// Load loads a plan from disk by ID.
func (s *PlanStore) Load(planID string) (*Plan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	filePath, err := planPath(s.dir, planID)
	if err != nil {
		return nil, err
	}
	data, err := readPrivatePlanFile(filePath)
	if err != nil {
		// errors.Is, not os.IsNotExist: readPrivatePlanFile wraps its errors, so
		// this branch was dead and a deleted plan surfaced an absolute filesystem
		// path instead of the intended "plan not found".
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("plan not found: %s", planID)
		}
		return nil, fmt.Errorf("failed to read plan: %w", err)
	}

	var plan Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("failed to unmarshal plan: %w", err)
	}
	if err := sanitizeLoadedPlan(&plan, planID); err != nil {
		return nil, err
	}

	return &plan, nil
}

// LoadLast loads the most recently updated plan that is resumable.
// If workDir is non-empty, only plans matching that directory (or plans without a WorkDir) are considered.
func (s *PlanStore) LoadLast(workDir string) (*Plan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	files, err := inspectStoredPlanFiles(s.dir, true)
	if err != nil {
		return nil, fmt.Errorf("failed to read plans directory: %w", err)
	}

	var latestPlan *Plan
	var latestTime time.Time

	for _, file := range files {
		data, err := readPrivatePlanFile(file.path)
		if err != nil {
			continue
		}

		plan := new(Plan)
		if err := json.Unmarshal(data, plan); err != nil {
			continue
		}
		if err := sanitizeLoadedPlan(plan, file.id); err != nil {
			continue
		}

		// Only consider resumable plans (paused or in_progress with pending steps)
		if !s.isResumable(plan) {
			continue
		}

		// Filter by working directory: skip plans from other directories
		if workDir != "" && plan.WorkDir != "" &&
			filepath.Clean(plan.WorkDir) != filepath.Clean(workDir) {
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

	files, err := inspectStoredPlanFiles(s.dir, true)
	if err != nil {
		return nil, fmt.Errorf("failed to read plans directory: %w", err)
	}

	var plans []PlanInfo
	for _, file := range files {
		data, err := readPrivatePlanFile(file.path)
		if err != nil {
			continue
		}

		var plan Plan
		if err := json.Unmarshal(data, &plan); err != nil {
			continue
		}
		if err := sanitizeLoadedPlan(&plan, file.id); err != nil {
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
			WorkDir:     plan.WorkDir,
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
// If workDir is non-empty, only plans matching that directory (or plans without a WorkDir) are returned.
func (s *PlanStore) ListResumable(workDir string) ([]PlanInfo, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}

	var resumable []PlanInfo
	for _, p := range all {
		if !p.IsResumable {
			continue
		}
		// Filter by working directory — strict match when specified
		if workDir != "" && p.WorkDir != "" &&
			filepath.Clean(p.WorkDir) != filepath.Clean(workDir) {
			continue
		}
		if workDir != "" && p.WorkDir == "" {
			continue
		}
		resumable = append(resumable, p)
	}

	return resumable, nil
}

// Delete removes a plan from disk.
func (s *PlanStore) Delete(planID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	filePath, err := planPath(s.dir, planID)
	if err != nil {
		return err
	}
	if err := removePrivatePlanFile(filePath); err != nil {
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
	if maxAge < 0 {
		return 0, fmt.Errorf("cleanup max age must be non-negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	files, err := inspectStoredPlanFiles(s.dir, true)
	if err != nil {
		return 0, fmt.Errorf("failed to read plans directory: %w", err)
	}

	cutoff := time.Now().Add(-maxAge)
	pausedCutoff := time.Now().Add(-maxAge * 3) // Keep paused plans 3x longer
	cleaned := 0

	for _, file := range files {
		data, err := readPrivatePlanFile(file.path)
		if err != nil {
			continue
		}

		var plan Plan
		if err := json.Unmarshal(data, &plan); err != nil {
			continue
		}
		if err := sanitizeLoadedPlan(&plan, file.id); err != nil {
			continue
		}

		// Use different cutoff for paused vs completed plans
		effectiveCutoff := cutoff
		if plan.Status == StatusPaused {
			effectiveCutoff = pausedCutoff
		}

		if plan.UpdatedAt.Before(effectiveCutoff) {
			if err := removePrivatePlanFile(file.path); err == nil {
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

	filePath, err := planPath(s.dir, planID)
	if err != nil {
		return false
	}
	return privatePlanFileExists(filePath)
}

// truncateString truncates a string to maxLen runes with ellipsis.
func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}
