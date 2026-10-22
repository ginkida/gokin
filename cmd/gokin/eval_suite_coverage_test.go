package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvalCompleteSuiteRejectsPassingSubsetAndExposesCoverage(t *testing.T) {
	manifest, subset, _, missing := writeStaleBaselineFixture(t)
	cmd := newEvalCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"report", "--manifest", manifest, "--input", subset, "--require-complete-suite", "--json"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("a passing subset satisfied the complete suite gate")
	}
	var payload struct {
		SuiteCoverage struct {
			Complete bool `json:"complete"`
			Variants []struct {
				Missing []string `json:"missing"`
			} `json:"variants"`
		} `json:"suite_coverage"`
		Gate struct {
			Passed   bool     `json:"passed"`
			Failures []string `json:"failures"`
		} `json:"gate"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err, out.String())
	}
	if payload.Gate.Passed || payload.SuiteCoverage.Complete || len(payload.SuiteCoverage.Variants) != 1 || len(payload.SuiteCoverage.Variants[0].Missing) != 1 || payload.SuiteCoverage.Variants[0].Missing[0] != missing {
		t.Fatalf("incomplete suite evidence was lost: %s", out.String())
	}
	if !strings.Contains(strings.Join(payload.Gate.Failures, ";"), "suite coverage") {
		t.Fatalf("coverage failure was not actionable: %s", out.String())
	}
}

func TestEvalCompleteSuiteRequiresActualPassingUniqueRowsInEveryCohort(t *testing.T) {
	manifest, _, complete, _ := writeStaleBaselineFixture(t)
	data, err := os.ReadFile(complete)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		row["status"] = "passed"
		rows = append(rows, row)
	}
	for _, tc := range []struct {
		name, status string
		extra        string
		wantPass     bool
	}{
		{name: "complete", wantPass: true},
		{name: "failed", status: "failed"},
		{name: "dry-run", status: "dry_run"},
		{name: "setup-failed", status: "setup_failed"},
		{name: "duplicate", extra: "duplicate"},
		{name: "unknown", extra: "unknown"},
		{name: "incomplete-other-provider", extra: "provider"},
		{name: "incomplete-other-engine", extra: "engine"},
		{name: "incomplete-other-trial", extra: "trial"},
		{name: "empty", extra: "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "results.jsonl")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			encoder := json.NewEncoder(file)
			encode := func(row map[string]any) {
				t.Helper()
				if err := encoder.Encode(row); err != nil {
					t.Fatal(err)
				}
			}
			if tc.extra != "empty" {
				for i, row := range rows {
					copy := make(map[string]any, len(row))
					for k, v := range row {
						copy[k] = v
					}
					if i == 0 && tc.status != "" {
						copy["status"] = tc.status
					}
					encode(copy)
				}
				if tc.extra != "" {
					copy := make(map[string]any, len(rows[0]))
					for k, v := range rows[0] {
						copy[k] = v
					}
					switch tc.extra {
					case "unknown":
						copy["scenario_id"] = "unknown-scenario"
					case "provider":
						copy["provider"] = "other"
					case "engine":
						copy["engine_mode"] = "hybrid"
					case "trial":
						copy["trial"], copy["trial_count"] = 1, 2
					}
					encode(copy)
				}
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			cmd := newEvalCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"report", "--manifest", manifest, "--input", path, "--require-complete-suite"})
			if err := cmd.Execute(); (err == nil) != tc.wantPass {
				t.Fatalf("complete suite gate returned %v; output=%s", err, out.String())
			}
			if !strings.Contains(out.String(), "Current suite coverage") {
				t.Fatalf("coverage summary missing: %s", out.String())
			}
		})
	}
}

func TestEvalCompleteSuiteFailsClosedWhenManifestIsUnavailable(t *testing.T) {
	_, _, complete, _ := writeStaleBaselineFixture(t)
	cmd := newEvalCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"report", "--manifest", filepath.Join(t.TempDir(), "missing.json"), "--input", complete, "--require-complete-suite"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "audit current suite coverage") {
		t.Fatalf("missing manifest did not fail closed: %v", err)
	}
}
