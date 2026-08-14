package evals

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResultCheckpointPreservesPublishedOutputUntilComplete(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "results.jsonl")
	if err := os.WriteFile(output, []byte("previous report\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	checkpoint, resumed, err := openResultCheckpoint(output, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 0 {
		t.Fatalf("resumed rows = %d, want 0", len(resumed))
	}
	result := Result{ScenarioID: "a", Status: "passed", EngineMode: "auto", Metrics: map[string]bool{}}
	if err := checkpoint.append(result); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(output); err != nil || string(got) != "previous report\n" {
		t.Fatalf("published output changed during run: %q, %v", got, err)
	}
	partial, err := ReadResults(output + resultCheckpointSuffix)
	if err != nil || len(partial) != 1 || partial[0].ScenarioID != "a" {
		t.Fatalf("checkpoint rows = %+v, err = %v", partial, err)
	}

	if err := checkpoint.publish(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(output + resultCheckpointSuffix); !os.IsNotExist(err) {
		t.Fatalf("completed checkpoint still exists: %v", err)
	}
	published, err := ReadResults(output)
	if err != nil || len(published) != 1 || published[0].ScenarioID != "a" {
		t.Fatalf("published rows = %+v, err = %v", published, err)
	}
}

func TestResultCheckpointRequiresExplicitResume(t *testing.T) {
	output := filepath.Join(t.TempDir(), "results.jsonl")
	checkpoint, _, err := openResultCheckpoint(output, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.append(Result{ScenarioID: "a", Status: "passed", Metrics: map[string]bool{}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openResultCheckpoint(output, false); err == nil || !strings.Contains(err.Error(), "--resume") {
		t.Fatalf("second writer error = %v, want explicit resume guidance", err)
	}
	resumedCheckpoint, results, err := openResultCheckpoint(output, true)
	if err != nil {
		t.Fatal(err)
	}
	if resumedCheckpoint.path != checkpoint.path || len(results) != 1 {
		t.Fatalf("resume = %q, %+v", resumedCheckpoint.path, results)
	}
}

func TestResultCheckpointNeverPublishesUnreadableOversizedRow(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "results.jsonl")
	if err := os.WriteFile(output, []byte("previous report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkpoint, _, err := openResultCheckpoint(output, false)
	if err != nil {
		t.Fatal(err)
	}
	result := Result{
		ScenarioID: "oversized",
		Status:     "failed",
		Metrics:    map[string]bool{},
		Error:      strings.Repeat("x", maxResultLineBytes),
	}
	if err := checkpoint.append(result); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized append error = %v", err)
	}
	partial, err := ReadResults(checkpoint.path)
	if err != nil || len(partial) != 0 {
		t.Fatalf("checkpoint after rejected row = %+v, %v", partial, err)
	}
	if got, err := os.ReadFile(output); err != nil || string(got) != "previous report\n" {
		t.Fatalf("published output after rejected row = %q, %v", got, err)
	}
}

func TestRunResumesExactCheckpointPrefix(t *testing.T) {
	root := t.TempDir()
	manifestPath, fixturesRoot := writeEvalTestManifest(t, root)
	seedOutput := filepath.Join(root, "seed.jsonl")
	base := RunOptions{
		ManifestPath: manifestPath,
		FixturesRoot: fixturesRoot,
		WorkRoot:     filepath.Join(root, "seed-work"),
		OutputPath:   seedOutput,
		Providers:    []string{"one", "two"},
		DryRun:       true,
	}
	seed, err := Run(context.Background(), base)
	if err != nil || len(seed) != 2 {
		t.Fatalf("seed Run() = %+v, %v", seed, err)
	}

	output := filepath.Join(root, "resumed.jsonl")
	checkpoint, _, err := openResultCheckpoint(output, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.append(seed[0]); err != nil {
		t.Fatal(err)
	}
	base.OutputPath = output
	base.WorkRoot = filepath.Join(root, "resume-work")
	base.Resume = true
	resumed, err := Run(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 2 || !resumed[0].StartedAt.Equal(seed[0].StartedAt) {
		t.Fatalf("resumed prefix was rerun or lost: seed=%+v resumed=%+v", seed, resumed)
	}
	if _, err := os.Stat(output + resultCheckpointSuffix); !os.IsNotExist(err) {
		t.Fatalf("checkpoint remains after successful resume: %v", err)
	}
}

func TestRunCancellationRetainsResumableCheckpointAndOldOutput(t *testing.T) {
	root := t.TempDir()
	manifestPath, fixturesRoot := writeEvalTestManifest(t, root)
	output := filepath.Join(root, "results.jsonl")
	if err := os.WriteFile(output, []byte("old report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := RunOptions{
		ManifestPath: manifestPath,
		FixturesRoot: fixturesRoot,
		WorkRoot:     filepath.Join(root, "work"),
		OutputPath:   output,
		DryRun:       true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results, err := Run(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "--resume") || len(results) != 0 {
		t.Fatalf("cancelled Run() = %+v, %v", results, err)
	}
	if got, readErr := os.ReadFile(output); readErr != nil || string(got) != "old report\n" {
		t.Fatalf("old output after cancellation = %q, %v", got, readErr)
	}
	if _, statErr := os.Stat(output + resultCheckpointSuffix); statErr != nil {
		t.Fatalf("checkpoint missing after cancellation: %v", statErr)
	}

	opts.Resume = true
	resumed, err := Run(context.Background(), opts)
	if err != nil || len(resumed) != 1 || resumed[0].Status != "dry_run" {
		t.Fatalf("resumed Run() = %+v, %v", resumed, err)
	}
}

func TestRunResumeRejectsChangedRunSpecification(t *testing.T) {
	root := t.TempDir()
	manifestPath, fixturesRoot := writeEvalTestManifest(t, root)
	output := filepath.Join(root, "results.jsonl")
	opts := RunOptions{
		ManifestPath: manifestPath,
		FixturesRoot: fixturesRoot,
		WorkRoot:     filepath.Join(root, "work"),
		OutputPath:   output,
		AgentCommand: "first command",
		DryRun:       true,
	}
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := snapshotFiles(filepath.Join(fixturesRoot, manifest.Scenarios[0].Fixture))
	if err != nil {
		t.Fatal(err)
	}
	matrix, err := buildRunMatrix(opts.Providers, opts.Models, opts.EngineModes, opts.FaultProfiles)
	if err != nil {
		t.Fatal(err)
	}
	matrix, err = expandRunTrials(matrix, opts.Repeat)
	if err != nil {
		t.Fatal(err)
	}
	result := Result{
		ScenarioID:       manifest.Scenarios[0].ID,
		ScenarioSpecHash: scenarioSpecHash(manifest.Scenarios[0], fixture),
		RunSpecHash:      evalRunSpecHash(manifest, manifest.Scenarios, matrix, opts),
		Category:         manifest.Scenarios[0].Category,
		Difficulty:       manifest.Scenarios[0].Difficulty,
		EngineMode:       "auto",
		Status:           "dry_run",
		Metrics:          map[string]bool{},
	}
	checkpoint, _, err := openResultCheckpoint(output, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.append(result); err != nil {
		t.Fatal(err)
	}
	opts.Resume = true
	opts.AgentCommand = "changed command"
	if _, err := Run(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "run specification changed") {
		t.Fatalf("changed run specification error = %v", err)
	}
}

func TestRunResumeRejectsChangedFixture(t *testing.T) {
	root := t.TempDir()
	manifestPath, fixturesRoot := writeEvalTestManifest(t, root)
	seedOpts := RunOptions{
		ManifestPath: manifestPath,
		FixturesRoot: fixturesRoot,
		WorkRoot:     filepath.Join(root, "seed-work"),
		OutputPath:   filepath.Join(root, "seed.jsonl"),
		DryRun:       true,
	}
	seed, err := Run(context.Background(), seedOpts)
	if err != nil || len(seed) != 1 {
		t.Fatalf("seed Run() = %+v, %v", seed, err)
	}
	output := filepath.Join(root, "resumed.jsonl")
	checkpoint, _, err := openResultCheckpoint(output, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.append(seed[0]); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile := filepath.Join(fixturesRoot, manifest.Scenarios[0].Fixture, "fixture.go")
	if err := os.WriteFile(fixtureFile, []byte("package fixture\nconst Changed = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedOpts.OutputPath = output
	seedOpts.Resume = true
	if _, err := Run(context.Background(), seedOpts); err == nil || !strings.Contains(err.Error(), "scenario contract or fixture changed") {
		t.Fatalf("changed fixture resume error = %v", err)
	}
}

func TestRunResumeRejectsExpandedMatrix(t *testing.T) {
	root := t.TempDir()
	manifestPath, fixturesRoot := writeEvalTestManifest(t, root)
	opts := RunOptions{
		ManifestPath: manifestPath,
		FixturesRoot: fixturesRoot,
		WorkRoot:     filepath.Join(root, "seed-work"),
		OutputPath:   filepath.Join(root, "seed.jsonl"),
		Providers:    []string{"one"},
		DryRun:       true,
	}
	seed, err := Run(context.Background(), opts)
	if err != nil || len(seed) != 1 {
		t.Fatalf("seed Run() = %+v, %v", seed, err)
	}
	output := filepath.Join(root, "resumed.jsonl")
	checkpoint, _, err := openResultCheckpoint(output, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.append(seed[0]); err != nil {
		t.Fatal(err)
	}
	opts.OutputPath = output
	opts.Resume = true
	opts.Providers = []string{"one", "two"}
	if _, err := Run(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "run specification changed") {
		t.Fatalf("expanded matrix resume error = %v", err)
	}
}
