package evals

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRun_DryRunCopiesFixtureMetadata(t *testing.T) {
	root := t.TempDir()
	manifestPath, fixturesRoot := writeEvalTestManifest(t, root)

	results, err := Run(context.Background(), RunOptions{
		ManifestPath: manifestPath,
		FixturesRoot: fixturesRoot,
		WorkRoot:     filepath.Join(root, "work"),
		DryRun:       true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].Status != "dry_run" {
		t.Fatalf("status = %q, want dry_run", results[0].Status)
	}
	if results[0].Workspace == "" {
		t.Fatal("workspace should be populated")
	}
}

func TestRun_CommandAndVerificationWritesJSONL(t *testing.T) {
	root := t.TempDir()
	manifestPath, fixturesRoot := writeEvalTestManifest(t, root)
	outputPath := filepath.Join(root, "results.jsonl")

	results, err := Run(context.Background(), RunOptions{
		ManifestPath: manifestPath,
		FixturesRoot: fixturesRoot,
		WorkRoot:     filepath.Join(root, "work"),
		OutputPath:   outputPath,
		AgentCommand: strings.Join([]string{
			"mkdir -p .gokin",
			"printf '%s\\n%s\\n%s\\n' '{\"event\":\"tool_start\",\"details\":{\"tool\":\"read\",\"args\":{\"file_path\":\"fixture.go\"}}}' '{\"event\":\"tool_start\",\"details\":{\"tool\":\"edit\",\"args\":{\"file_path\":\"fixture.go\"}}}' '{\"event\":\"tool_start\",\"details\":{\"tool\":\"bash\",\"args\":{\"command\":\"go test ./...\"}}}' > .gokin/execution_journal.jsonl",
			"printf 'updated fixture.go and verified with go test ./...\\n' > agent.out",
			"printf 'package fixture\\nconst Fixed = true\\n' > fixture.go",
			"printf 'Updated fixture.go and verified with go test ./...\\n'",
		}, " && "),
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	result := results[0]
	if result.Status != "passed" {
		t.Fatalf("status = %q, error = %q, verification = %+v", result.Status, result.Error, result.Verification)
	}
	if !result.Metrics["verification_passed"] {
		t.Fatalf("verification metric = false: %+v", result.Metrics)
	}
	if result.Score.Total == 0 || result.Score.Passed != result.Score.Total {
		t.Fatalf("score = %+v, want all metrics passing", result.Score)
	}
	if !containsString(result.ChangedFiles, "fixture.go") {
		t.Fatalf("changed files = %v, want fixture.go", result.ChangedFiles)
	}
	if result.Journal == nil {
		t.Fatal("journal summary should be populated")
	}
	if result.Journal.ToolCalls != 3 {
		t.Fatalf("tool calls = %d, want 3", result.Journal.ToolCalls)
	}
	if !containsString(result.Journal.FilesRead, "fixture.go") {
		t.Fatalf("journal files read = %v, want fixture.go", result.Journal.FilesRead)
	}
	if !containsString(result.Journal.FilesEdited, "fixture.go") {
		t.Fatalf("journal files edited = %v, want fixture.go", result.Journal.FilesEdited)
	}
	if !containsString(result.Journal.VerificationCommands, "go test ./...") {
		t.Fatalf("journal verification = %v, want go test ./...", result.Journal.VerificationCommands)
	}
	if len(result.Journal.FalseFileClaims) != 0 {
		t.Fatalf("false claims = %v, want none", result.Journal.FalseFileClaims)
	}

	decoded, err := ReadResults(outputPath)
	if err != nil {
		t.Fatalf("ReadResults() error = %v", err)
	}
	if len(decoded) != 1 || decoded[0].ScenarioID != "sample" {
		t.Fatalf("decoded results = %+v", decoded)
	}
}

func TestRun_ProviderMatrixExpandsScenarios(t *testing.T) {
	root := t.TempDir()
	manifestPath, fixturesRoot := writeEvalTestManifest(t, root)

	results, err := Run(context.Background(), RunOptions{
		ManifestPath: manifestPath,
		FixturesRoot: fixturesRoot,
		WorkRoot:     filepath.Join(root, "work"),
		Providers:    []string{"kimi", "glm"},
		DryRun:       true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if results[0].Provider != "kimi" || results[1].Provider != "glm" {
		t.Fatalf("providers = %q, %q; want kimi, glm", results[0].Provider, results[1].Provider)
	}
	if results[0].Metadata["provider"] != "kimi" || results[1].Metadata["provider"] != "glm" {
		t.Fatalf("metadata providers = %+v, %+v", results[0].Metadata, results[1].Metadata)
	}
	if !strings.HasSuffix(filepath.ToSlash(results[0].Workspace), "/sample/kimi") {
		t.Fatalf("workspace = %q, want provider-specific suffix", results[0].Workspace)
	}
}

func writeEvalTestManifest(t *testing.T, root string) (string, string) {
	t.Helper()
	fixturesRoot := filepath.Join(root, "fixtures")
	fixtureDir := filepath.Join(fixturesRoot, "sample-fixture")
	if err := os.MkdirAll(fixtureDir, 0755); err != nil {
		t.Fatalf("MkdirAll fixture error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixtureDir, "fixture.go"), []byte("package fixture\nconst Fixed = false\n"), 0644); err != nil {
		t.Fatalf("WriteFile fixture error = %v", err)
	}

	manifest := Manifest{
		Version:     1,
		Name:        "test",
		Description: "test manifest",
		Metrics:     []string{"task_completed", "verification_passed"},
		Scenarios: []Scenario{{
			ID:                   "sample",
			Category:             "bugfix",
			Difficulty:           "small",
			Prompt:               "fix it",
			Fixture:              "sample-fixture",
			ExpectedBehaviors:    []string{"edit fixture"},
			VerificationCommands: []string{"test -f agent.out && grep -q 'Fixed = true' fixture.go"},
			SuccessCriteria:      []string{"verification passes"},
			FailureSignals:       []string{"no edit"},
			MaxToolCalls:         5,
		}},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("Marshal manifest error = %v", err)
	}
	manifestPath := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		t.Fatalf("WriteFile manifest error = %v", err)
	}
	return manifestPath, fixturesRoot
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
