package repl

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type hybridOneScanProof struct {
	id         string
	name       string
	fixture    string
	code       string
	wantValue  string
	operations map[string]int
}

func TestHybridEvalFixturesUseProductionIgnoreAwareIndex(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "evals", "hybrid", "fixtures"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("mixed source distribution", func(t *testing.T) {
		paths := hybridFixtureIndexPaths(t, filepath.Join(root, "mixed_inventory"))
		if paths["generated/ignored.ts"] {
			t.Fatal("ignored generated TypeScript file leaked into production index")
		}
		counts := map[string]int{}
		for path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			switch extension := filepath.Ext(path); extension {
			case ".go", ".py", ".ts":
				counts[extension]++
			}
		}
		for extension, want := range map[string]int{".go": 1, ".py": 2, ".ts": 3} {
			if counts[extension] != want {
				t.Fatalf("indexed source counts = %v", counts)
			}
		}
	})

	t.Run("JSON catalog", func(t *testing.T) {
		paths := hybridFixtureIndexPaths(t, filepath.Join(root, "config_catalog"))
		if paths["scratch/broken.json"] {
			t.Fatal("ignored malformed JSON leaked into production index")
		}
		jsonFiles := 0
		for path := range paths {
			if filepath.Ext(path) == ".json" {
				jsonFiles++
			}
		}
		if jsonFiles != 4 {
			t.Fatalf("indexed JSON files = %d, want 4: %v", jsonFiles, paths)
		}
	})
}

func TestHybridPositiveFixturesCompleteInOneCellAndOneScan(t *testing.T) {
	tests := []hybridOneScanProof{
		{
			id:      "repo_comment_count_rank",
			name:    "rank combined comment markers by top directory",
			fixture: "repository_analytics",
			code: `r = context.count_code(r"TODO|FIXME", regex=True, group_by="top_dir")
sorted(r["groups"].items(), key=lambda item: (-item[1], item[0]))`,
			wantValue:  "[('alpha', 4), ('beta', 2), ('.', 1)]",
			operations: map[string]int{"count_code": 1, "file_inventory": 1},
		},
		{
			id:      "repo_marker_counts_batched",
			name:    "count several markers in one pass",
			fixture: "repository_analytics",
			code: `r = context.count_code_many(["TODO", "FIXME"])
[item["matching_lines"] for item in r["counts"]]`,
			wantValue:  "[4, 3]",
			operations: map[string]int{"count_code_many": 1, "file_inventory": 1},
		},
		{
			id:      "repo_fixme_count_with_sample",
			name:    "count with bounded representative evidence",
			fixture: "repository_analytics",
			code: `r = context.count_code("FIXME", sample_limit=1)
[r["matching_lines"], len(r["samples"]), r["samples"][0]["text"].startswith("// FIXME:")]`,
			wantValue: "[3, 1, True]",
			operations: map[string]int{
				"count_code":         1,
				"count_code_sampled": 1,
				"file_inventory":     1,
			},
		},
		{
			id:      "repo_exported_functions_without_tests",
			name:    "join exported declarations with test references",
			fixture: "repository_analytics",
			code: `listing = context.list_files(pattern="*.go")
paths = [item["path"] for item in listing["files"]]
def source(path):
    return "\n".join(row["text"] for row in context.read_slice(path, 1, 2000)["lines"])
production = source("analytics.go")
tests = "\n".join(source(path) for path in paths if path.endswith("_test.go"))
uncovered = []
for line in production.splitlines():
    if line.startswith("func ") and line[5:6].isupper():
        name = line[5:].split("(", 1)[0]
        if name + "(" not in tests:
            uncovered.append(name)
sorted(uncovered)`,
			wantValue: "['Archive', 'Purge']",
			operations: map[string]int{
				"file_inventory": 1,
				"list_files":     1,
				"read_slice":     2,
			},
		},
		{
			id:      "repo_source_extension_distribution",
			name:    "aggregate production source distribution",
			fixture: "mixed_inventory",
			code: `r = context.file_stats(exclude_pattern="*_test.go", group_by="extension")
[r["groups"][ext]["files"] for ext in [".go", ".py", ".ts"]]`,
			wantValue:  "[1, 2, 3]",
			operations: map[string]int{"file_inventory": 1, "file_stats": 1},
		},
		{
			id:      "repo_json_config_join",
			name:    "join ignored-aware JSON catalog",
			fixture: "config_catalog",
			code: `import json
listing = context.list_files(pattern="*.json")
documents = []
for item in listing["files"]:
    rows = context.read_slice(item["path"], 1, 2000)["lines"]
    documents.append(json.loads("\n".join(row["text"] for row in rows)))
common = set(documents[0])
for document in documents[1:]:
    common.intersection_update(document)
timeouts = [document["timeout"] for document in documents]
[sorted(common), min(timeouts), max(timeouts), len(documents)]`,
			wantValue: "[['timeout'], 5, 30, 4]",
			operations: map[string]int{
				"file_inventory": 1,
				"list_files":     1,
				"read_slice":     4,
			},
		},
	}
	assertHybridManifestCandidatesHaveOneScanProof(t, tests)

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workDir, err := filepath.Abs(filepath.Join("..", "..", "evals", "hybrid", "fixtures", test.fixture))
			if err != nil {
				t.Fatal(err)
			}
			manager := testManager(t, workDir, nil)
			result, err := manager.Execute(t.Context(), test.code)
			if err != nil || result.Error != nil || result.Value != test.wantValue {
				t.Fatalf("one-cell result=%+v err=%v, want value %s", result, err, test.wantValue)
			}
			if result.Truncated {
				t.Fatalf("one-cell result was truncated: %+v", result)
			}
			if !reflect.DeepEqual(result.Operations, test.operations) {
				t.Fatalf("runtime operations=%v, want %v", result.Operations, test.operations)
			}
			if scans := hybridScanOperationCount(result.Operations); scans != 1 {
				t.Fatalf("directory-scale scan operations=%d, want 1: %v", scans, result.Operations)
			}
			if result.FileIndexRefreshes != 1 {
				t.Fatalf("parent-observed file index refreshes=%d, want 1", result.FileIndexRefreshes)
			}
		})
	}
}

func assertHybridManifestCandidatesHaveOneScanProof(t *testing.T, proofs []hybridOneScanProof) {
	t.Helper()
	manifestPath := filepath.Join("..", "..", "evals", "hybrid", "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Scenarios []struct {
			ID                          string   `json:"id"`
			HybridCandidate             bool     `json:"hybrid_candidate"`
			HybridRequiredOperations    []string `json:"hybrid_required_operations"`
			HybridRequiredAnyOperations []string `json:"hybrid_required_any_operations"`
			HybridMaxScanOperations     int      `json:"hybrid_max_scan_operations"`
			HybridMinFileIndexRefreshes int      `json:"hybrid_min_file_index_refreshes"`
			HybridMaxREPLCalls          int      `json:"hybrid_max_repl_calls"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	candidates := make(map[string]struct {
		required    []string
		requiredAny []string
	})
	for _, scenario := range manifest.Scenarios {
		if scenario.HybridCandidate {
			if scenario.HybridMaxScanOperations != 1 || scenario.HybridMinFileIndexRefreshes != 1 || scenario.HybridMaxREPLCalls != 1 {
				t.Fatalf("hybrid scenario %q does not require one scan, one index refresh, and one REPL call", scenario.ID)
			}
			candidates[scenario.ID] = struct {
				required    []string
				requiredAny []string
			}{scenario.HybridRequiredOperations, scenario.HybridRequiredAnyOperations}
		}
	}
	tested := make(map[string]struct {
		required    []string
		requiredAny []string
	}, len(proofs))
	for _, proof := range proofs {
		candidate, ok := candidates[proof.id]
		if !ok {
			t.Fatalf("one-scan proof %q has no hybrid manifest candidate", proof.id)
		}
		if _, duplicate := tested[proof.id]; duplicate {
			t.Fatalf("duplicate one-scan proof for %q", proof.id)
		}
		for _, operation := range candidate.required {
			if proof.operations[operation] < 1 {
				t.Fatalf("one-scan proof %q does not execute required operation %q: %v", proof.id, operation, proof.operations)
			}
		}
		if len(candidate.requiredAny) > 0 {
			matched := false
			for _, operation := range candidate.requiredAny {
				matched = matched || proof.operations[operation] > 0
			}
			if !matched {
				t.Fatalf("one-scan proof %q executes none of required alternatives %v: %v", proof.id, candidate.requiredAny, proof.operations)
			}
		}
		tested[proof.id] = candidate
	}
	if !reflect.DeepEqual(tested, candidates) {
		t.Fatalf("one-scan proofs=%v, hybrid manifest candidates=%v", tested, candidates)
	}
}

func hybridScanOperationCount(operations map[string]int) int {
	if count := operations["file_inventory"]; count > 0 {
		return count
	}
	count := 0
	for _, operation := range []string{"count_code", "count_code_many", "search_code", "list_files", "file_stats"} {
		count += operations[operation]
	}
	return count
}

func hybridFixtureIndexPaths(t *testing.T, workDir string) map[string]bool {
	t.Helper()
	manager := &Manager{opts: Options{WorkDir: workDir}, runtimeDir: t.TempDir()}
	result, err := manager.writeMatcherFileIndex(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "matcher" || result.Truncated {
		t.Fatalf("fixture index result = %+v", result)
	}
	raw, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	paths := make(map[string]bool)
	for _, path := range bytes.Split(raw, []byte{0}) {
		if len(path) > 0 {
			paths[string(path)] = true
		}
	}
	return paths
}
