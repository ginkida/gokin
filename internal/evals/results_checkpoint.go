package evals

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gokin/internal/fileutil"
)

const resultCheckpointSuffix = ".partial"

// resultCheckpoint keeps the last published report untouched while a run is
// in progress. Every completed row is published atomically to a separate,
// valid JSONL checkpoint; only a complete run replaces the requested output.
type resultCheckpoint struct {
	outputPath string
	path       string
	data       bytes.Buffer
}

func openResultCheckpoint(outputPath string, resume bool) (*resultCheckpoint, []Result, error) {
	if strings.TrimSpace(outputPath) == "" {
		return nil, nil, fmt.Errorf("output path is required")
	}
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create output dir: %w", err)
	}

	checkpointPath := outputPath + resultCheckpointSuffix
	info, statErr := os.Lstat(checkpointPath)
	switch {
	case statErr == nil:
		if !info.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("result checkpoint %q is not a regular file", checkpointPath)
		}
		if !resume {
			return nil, nil, fmt.Errorf("result checkpoint %q already exists; use --resume to continue it or move/remove it after inspection", checkpointPath)
		}
		results, err := ReadResults(checkpointPath)
		if err != nil {
			return nil, nil, fmt.Errorf("read result checkpoint %q: %w", checkpointPath, err)
		}
		data, err := marshalResultsJSONL(results)
		if err != nil {
			return nil, nil, fmt.Errorf("normalize result checkpoint %q: %w", checkpointPath, err)
		}
		checkpoint := &resultCheckpoint{outputPath: outputPath, path: checkpointPath}
		_, _ = checkpoint.data.Write(data)
		return checkpoint, results, nil
	case !os.IsNotExist(statErr):
		return nil, nil, fmt.Errorf("inspect result checkpoint %q: %w", checkpointPath, statErr)
	case resume:
		return nil, nil, fmt.Errorf("result checkpoint %q does not exist", checkpointPath)
	}

	f, err := os.OpenFile(checkpointPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("create result checkpoint %q: %w", checkpointPath, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(checkpointPath)
		return nil, nil, fmt.Errorf("sync result checkpoint %q: %w", checkpointPath, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(checkpointPath)
		return nil, nil, fmt.Errorf("close result checkpoint %q: %w", checkpointPath, err)
	}
	return &resultCheckpoint{outputPath: outputPath, path: checkpointPath}, nil, nil
}

func (c *resultCheckpoint) append(result Result) error {
	line, err := marshalResultLine(result)
	if err != nil {
		return err
	}
	previousSize := c.data.Len()
	_, _ = c.data.Write(line)
	if err := fileutil.AtomicWrite(c.path, c.data.Bytes(), 0o600); err != nil {
		c.data.Truncate(previousSize)
		return err
	}
	return nil
}

func (c *resultCheckpoint) publish() error {
	if err := fileutil.AtomicWrite(c.outputPath, c.data.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("output is published but remove completed checkpoint: %w", err)
	}
	return nil
}

func marshalResultLine(result Result) ([]byte, error) {
	b, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(b)+1 > maxResultLineBytes {
		return nil, fmt.Errorf("JSONL result for scenario %q exceeds %d-byte limit", result.ScenarioID, maxResultLineBytes)
	}
	return append(b, '\n'), nil
}

func marshalResultsJSONL(results []Result) ([]byte, error) {
	var out bytes.Buffer
	for _, result := range results {
		line, err := marshalResultLine(result)
		if err != nil {
			return nil, err
		}
		_, _ = out.Write(line)
	}
	return out.Bytes(), nil
}

func evalRunSpecHash(manifest *Manifest, scenarios []Scenario, matrix []matrixEntry, opts RunOptions) string {
	type runVariant struct {
		Provider     string `json:"provider,omitempty"`
		Model        string `json:"model,omitempty"`
		EngineMode   string `json:"engine_mode"`
		Trial        int    `json:"trial,omitempty"`
		TrialCount   int    `json:"trial_count,omitempty"`
		FaultProfile string `json:"fault_profile,omitempty"`
	}
	type runSpec struct {
		Version       int          `json:"version"`
		ManifestName  string       `json:"manifest_name"`
		Scenarios     []Scenario   `json:"scenarios"`
		Matrix        []runVariant `json:"matrix"`
		AgentCommand  string       `json:"agent_command"`
		TimeoutNanos  int64        `json:"timeout_nanos"`
		DryRun        bool         `json:"dry_run"`
		FaultUpstream string       `json:"fault_upstream"`
		GokinBin      string       `json:"gokin_bin,omitempty"`
		GokinBinHash  string       `json:"gokin_bin_hash,omitempty"`
	}
	gokinBin := strings.TrimSpace(os.Getenv("GOKIN_BIN"))
	gokinBinHash := ""
	if gokinBin != "" {
		if info, err := os.Stat(gokinBin); err == nil && info.Mode().IsRegular() {
			gokinBinHash, _ = fileHash(gokinBin)
		}
	}
	variants := make([]runVariant, 0, len(matrix))
	for _, variant := range matrix {
		variants = append(variants, runVariant{
			Provider: variant.Provider, Model: variant.Model, EngineMode: variant.EngineMode,
			Trial: variant.Trial, TrialCount: variant.TrialCount, FaultProfile: variant.FaultProfile,
		})
	}
	payload := runSpec{
		Version:       manifest.Version,
		ManifestName:  manifest.Name,
		Scenarios:     scenarios,
		Matrix:        variants,
		AgentCommand:  opts.AgentCommand,
		TimeoutNanos:  opts.Timeout.Nanoseconds(),
		DryRun:        opts.DryRun,
		FaultUpstream: opts.FaultUpstream,
		GokinBin:      gokinBin,
		GokinBinHash:  gokinBinHash,
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validateResumePrefix(scenarios []Scenario, matrix []matrixEntry, opts RunOptions, wantRunSpec string, results []Result) error {
	total := len(scenarios) * len(matrix)
	if len(results) > total {
		return fmt.Errorf("contains %d rows but current run has only %d", len(results), total)
	}
	if len(results) == 0 {
		return nil
	}
	specHashes := make(map[string]string, len(scenarios))
	for index, result := range results {
		scenario := scenarios[index/len(matrix)]
		variant := matrix[index%len(matrix)]
		if err := validateResumeIdentity(result, scenario, variant); err != nil {
			return fmt.Errorf("row %d: %w", index+1, err)
		}
		if result.RunSpecHash == "" || result.RunSpecHash != wantRunSpec {
			return fmt.Errorf("row %d: run specification changed", index+1)
		}
		wantScenarioSpec, ok := specHashes[scenario.ID]
		if !ok {
			fixturePath := filepath.Join(opts.FixturesRoot, filepath.FromSlash(scenario.Fixture))
			fixture, err := snapshotFiles(fixturePath)
			if err != nil {
				return fmt.Errorf("row %d: snapshot fixture %q: %w", index+1, scenario.Fixture, err)
			}
			wantScenarioSpec = scenarioSpecHash(scenario, fixture)
			specHashes[scenario.ID] = wantScenarioSpec
		}
		if result.ScenarioSpecHash == "" || result.ScenarioSpecHash != wantScenarioSpec {
			return fmt.Errorf("row %d: scenario contract or fixture changed", index+1)
		}
	}
	return nil
}

func validateResumeIdentity(result Result, scenario Scenario, variant matrixEntry) error {
	if result.ScenarioID != scenario.ID || result.Provider != variant.Provider ||
		result.Model != variant.Model || result.EngineMode != variant.EngineMode ||
		result.Trial != variant.Trial || result.TrialCount != variant.TrialCount ||
		result.FaultProfile != variant.FaultProfile {
		return fmt.Errorf("result identity does not match the current matrix prefix")
	}
	if result.Category != scenario.Category || result.Difficulty != scenario.Difficulty ||
		!reflect.DeepEqual(result.HybridCandidate, scenario.HybridCandidate) {
		return fmt.Errorf("scenario provenance does not match the current manifest")
	}
	return nil
}
