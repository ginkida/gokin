package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gokin/internal/fileutil"
	"gokin/internal/logging"
)

const (
	maxPlanFileBytes     int64 = 16 << 20
	maxStoredPlans             = 5000
	maxPlanIDBytes             = 128
	maxPlanSteps               = 10000
	maxPlanStepDepth           = 64
	maxPlanLedgerEntries       = 10000
)

func validatePlanID(planID string) error {
	if planID == "" || len(planID) > maxPlanIDBytes {
		return fmt.Errorf("invalid plan ID")
	}
	for i := range len(planID) {
		c := planID[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.' {
			continue
		}
		return fmt.Errorf("invalid plan ID %q", planID)
	}
	if planID == "." || planID == ".." || strings.HasPrefix(planID, ".") {
		return fmt.Errorf("invalid plan ID %q", planID)
	}
	return nil
}

func planPath(dir, planID string) (string, error) {
	if err := validatePlanID(planID); err != nil {
		return "", err
	}
	return filepath.Join(dir, planID+".json"), nil
}

func readPrivatePlanFile(path string) ([]byte, error) {
	if err := fileutil.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return fileutil.ReadPrivateFile(path, maxPlanFileBytes)
}

func writePrivatePlanFile(path string, data []byte) error {
	if int64(len(data)) > maxPlanFileBytes {
		return fmt.Errorf("plan file exceeds %d-byte limit", maxPlanFileBytes)
	}
	if err := fileutil.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	if err := fileutil.SecurePrivateFile(path); err != nil {
		return err
	}
	return fileutil.AtomicWrite(path, data, 0o600)
}

func validatePrivatePlanFile(path string) error {
	if err := fileutil.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	return fileutil.SecurePrivateFile(path)
}

func removePrivatePlanFile(path string) error {
	if err := validatePrivatePlanFile(path); err != nil {
		return err
	}
	return os.Remove(path)
}

func privatePlanFileExists(path string) bool {
	if err := fileutil.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

type storedPlanFile struct {
	id   string
	path string
}

func inspectStoredPlanFiles(dir string, repairModes bool) ([]storedPlanFile, error) {
	return inspectStoredPlanFilesWithLimit(dir, repairModes, maxStoredPlans)
}

func inspectStoredPlanFilesWithLimit(dir string, repairModes bool, limit int) ([]storedPlanFile, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("plan file limit must be positive")
	}
	if err := fileutil.EnsurePrivateDir(dir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]storedPlanFile, 0, min(len(entries), limit))
	for _, entry := range entries {
		name := entry.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if err := validatePlanID(id); err != nil {
			continue
		}
		// One anomalous entry SKIPS that entry; it does not take the store down.
		// Aborting the whole listing meant a single symlink, oversized file, or a
		// plan deleted between ReadDir and Info disabled plan persistence
		// entirely — including the Cleanup path that exists to recover from
		// exactly this. Corrupt-one, keep-the-rest is how the sibling stores
		// already behave.
		if entry.Type()&os.ModeSymlink != 0 {
			logging.Warn("skipping plan storage entry that is a symlink", "file", name)
			continue
		}
		info, err := entry.Info()
		if err != nil {
			// Most often the plan was removed between ReadDir and Info.
			logging.Debug("skipping unreadable plan storage entry", "file", name, "error", err)
			continue
		}
		if !info.Mode().IsRegular() {
			logging.Warn("skipping plan storage entry that is not a regular file", "file", name)
			continue
		}
		if info.Size() < 0 || info.Size() > maxPlanFileBytes {
			logging.Warn("skipping oversized plan file",
				"file", name, "size", info.Size(), "limit", maxPlanFileBytes)
			continue
		}
		path := filepath.Join(dir, name)
		if repairModes {
			if err := fileutil.SecurePrivateFile(path); err != nil {
				logging.Warn("skipping plan file whose permissions could not be repaired",
					"file", name, "error", err)
				continue
			}
		}
		files = append(files, storedPlanFile{id: id, path: path})
		if len(files) > limit {
			return nil, fmt.Errorf("plan store exceeds %d-file limit", limit)
		}
	}
	return files, nil
}

func validatePlanStructure(plan *Plan) error {
	if len(plan.RunLedger) > maxPlanLedgerEntries {
		return fmt.Errorf("plan run ledger exceeds %d-entry limit", maxPlanLedgerEntries)
	}
	count := 0
	var visit func([]*Step, int) error
	visit = func(steps []*Step, depth int) error {
		if depth > maxPlanStepDepth {
			return fmt.Errorf("plan step nesting exceeds depth %d", maxPlanStepDepth)
		}
		for _, step := range steps {
			if step == nil {
				return fmt.Errorf("plan contains a null step")
			}
			count++
			if count > maxPlanSteps {
				return fmt.Errorf("plan exceeds %d-step limit", maxPlanSteps)
			}
			if len(step.Children) > 0 {
				if err := visit(step.Children, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(plan.Steps, 0)
}

func sanitizeLoadedPlan(plan *Plan, expectedID string) error {
	if plan.ID != expectedID {
		return fmt.Errorf("stored plan ID %q does not match filename ID %q", plan.ID, expectedID)
	}
	if len(plan.RunLedger) > maxPlanLedgerEntries {
		return fmt.Errorf("plan run ledger exceeds %d-entry limit", maxPlanLedgerEntries)
	}
	for id, entry := range plan.RunLedger {
		if entry == nil {
			delete(plan.RunLedger, id)
		}
	}

	count := 0
	var sanitize func([]*Step, int) ([]*Step, error)
	sanitize = func(steps []*Step, depth int) ([]*Step, error) {
		if depth > maxPlanStepDepth {
			return nil, fmt.Errorf("plan step nesting exceeds depth %d", maxPlanStepDepth)
		}
		bounded := make([]*Step, 0, len(steps))
		for _, step := range steps {
			if step == nil {
				continue
			}
			count++
			if count > maxPlanSteps {
				return nil, fmt.Errorf("plan exceeds %d-step limit", maxPlanSteps)
			}
			if len(step.Children) > 0 {
				children, err := sanitize(step.Children, depth+1)
				if err != nil {
					return nil, err
				}
				step.Children = children
			}
			step.EnsureContractDefaults()
			bounded = append(bounded, step)
		}
		return bounded, nil
	}
	steps, err := sanitize(plan.Steps, 0)
	if err != nil {
		return err
	}
	plan.Steps = steps
	plan.EnsureStepContracts()
	return nil
}
