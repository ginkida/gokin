package repl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A previous scan resolved/stat'ed every candidate in the walker and then
// stat'ed it again in count/search/list. On repositories with many small files,
// metadata syscalls dominated the actual substring match. Pin one Path.stat per
// file plus constant root overhead; the binary peek and text read do no stats.
func TestCountCodeDoesNotRestatEveryFile(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	initFileIndexGitRepo(t, workDir)
	const fileCount = 64
	for index := 0; index < fileCount; index++ {
		path := filepath.Join(workDir, "src", "file-"+strconv.Itoa(index)+".txt")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("needle\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager := testManager(t, workDir, nil)
	res, err := manager.Execute(t.Context(), `result = context.count_code("needle", path="src")
[result["matching_files"], result["searched_files"]]`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != nil {
		t.Fatalf("count scan failed: %s: %s", res.Error.Type, res.Error.Message)
	}
	if want := fmt.Sprintf("[%d, %d]", fileCount, fileCount); res.Value != want {
		t.Fatalf("scan result = %s, want %s", res.Value, want)
	}
	if res.Operations["count_code"] != 1 || res.Operations["file_inventory"] != 1 || res.FileIndexRefreshes != 1 {
		t.Fatalf("single aggregate evidence=%v refreshes=%d", res.Operations, res.FileIndexRefreshes)
	}
}

// Comparative analytics should share both inventory and file I/O. Calling
// count_code once per pattern doubles Git callbacks and disk reads; the batch
// API pins the cost to one bounded snapshot regardless of query count.
func TestCountCodeManyScansEveryFileOnce(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	initFileIndexGitRepo(t, workDir)
	const fileCount = 64
	for index := 0; index < fileCount; index++ {
		path := filepath.Join(workDir, "src", "file-"+strconv.Itoa(index)+".txt")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("needle and other\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager := testManager(t, workDir, func(opts *Options) { opts.MaxCallbacks = 1 })
	res, err := manager.Execute(t.Context(), `result = context.count_code_many(["needle", "other"], path="src", sample_limit=2)
[[[c["matching_lines"], c["matching_files"], len(c["samples"]), c["samples_truncated"]] for c in result["counts"]], result["searched_files"]]`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != nil {
		t.Fatalf("batched count failed: %s: %s", res.Error.Type, res.Error.Message)
	}
	want := fmt.Sprintf("[[[%d, %d, 2, True], [%d, %d, 2, True]], %d]",
		fileCount, fileCount, fileCount, fileCount, fileCount)
	if res.Value != want {
		t.Fatalf("batched scan result = %s, want %s", res.Value, want)
	}
	if res.Operations["count_code_many"] != 1 || res.Operations["file_inventory"] != 1 || res.FileIndexRefreshes != 1 {
		t.Fatalf("batched aggregate evidence=%v refreshes=%d", res.Operations, res.FileIndexRefreshes)
	}
}

// A same-scope compound cell should reuse the Python-side entry metadata, not
// merely avoid a second Git process in the Go parent. The first operation is
// still streaming/lazy; entries it actually visits become replayable by later
// operations without another resolve/stat pass.
func TestCompoundCellDoesNotRestatSameScope(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	initFileIndexGitRepo(t, workDir)
	const fileCount = 64
	for index := 0; index < fileCount; index++ {
		path := filepath.Join(workDir, "src", "file-"+strconv.Itoa(index)+".txt")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("needle\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager := testManager(t, workDir, nil)
	res, err := manager.Execute(t.Context(), `listed = context.list_files(path="src")
counted = context.count_code("needle", path="src")
[len(listed["files"]), counted["matching_files"]]`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != nil {
		t.Fatalf("compound scan failed: %s: %s", res.Error.Type, res.Error.Message)
	}
	if want := fmt.Sprintf("[%d, %d]", fileCount, fileCount); res.Value != want {
		t.Fatalf("compound scan result = %s, want %s", res.Value, want)
	}
	if res.FileIndexRefreshes != 1 {
		t.Fatalf("compound same-scope scan refreshed parent index %d times, want one", res.FileIndexRefreshes)
	}
	if res.Operations["file_inventory"] != 2 {
		t.Fatalf("compound logical inventory scans=%v, want two despite one physical refresh", res.Operations)
	}
}

// search_code deliberately stops when its evidence limit is reached. Reusing
// that partial snapshot must resume the original inventory for a later exact
// aggregation rather than treating the visited prefix as the whole scope.
func TestPartialSearchSnapshotResumesForExactCount(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	initFileIndexGitRepo(t, workDir)
	const fileCount = 32
	for index := 0; index < fileCount; index++ {
		path := filepath.Join(workDir, "src", fmt.Sprintf("file-%02d.txt", index))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("needle\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager := testManager(t, workDir, nil)
	res, err := manager.Execute(t.Context(), `
sample = context.search_code("needle", path="src", limit=1)
exact = context.count_code("needle", path="src")
[len(sample["matches"]), sample["truncated"], exact["matching_files"], exact["truncated"]]`)
	if err != nil || res.Error != nil {
		t.Fatalf("partial then exact scan=%+v err=%v", res, err)
	}
	want := fmt.Sprintf("[1, True, %d, False]", fileCount)
	if res.Value != want {
		t.Fatalf("partial then exact value=%s, want %s", res.Value, want)
	}
	if res.FileIndexRefreshes != 1 {
		t.Fatalf("partial then exact parent index refreshes=%d, want one", res.FileIndexRefreshes)
	}
	if res.Operations["file_inventory"] != 2 {
		t.Fatalf("partial then exact logical inventory scans=%v, want two", res.Operations)
	}
}

func TestCompoundEntrySnapshotInvalidatesBeforeCallback(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	initFileIndexGitRepo(t, workDir)
	if err := os.WriteFile(filepath.Join(workDir, "before.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t, workDir, func(opts *Options) { opts.MaxCallbacks = 4 })
	manager.SetCallHandler(func(_ context.Context, call Call) (any, error) {
		if call.Method != "rlm.call" {
			t.Fatalf("unexpected callback: %s", call.Method)
		}
		if err := os.WriteFile(filepath.Join(workDir, "after.txt"), []byte("needle\n"), 0o600); err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "content": "mutated"}, nil
	})

	res, err := manager.Execute(t.Context(), `
listed = context.list_files()
rlm("create another file")
counted = context.count_code("needle")
[len(listed["files"]), counted["matching_files"]]`)
	if err != nil || res.Error != nil || res.Value != "[1, 2]" {
		t.Fatalf("entry snapshot callback invalidation=%+v err=%v", res, err)
	}
	if res.FileIndexRefreshes != 2 {
		t.Fatalf("callback-invalidated entry snapshot refreshes=%d, want two", res.FileIndexRefreshes)
	}
}

// The worker keeps the raw bounded index alive while entries stream into a
// same-cell snapshot. Splitting the whole buffer would additionally allocate a
// list and one bytes object per file, causing a needless peak on large repos.
func TestFileIndexParsingDoesNotSplitWholeBuffer(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	initFileIndexGitRepo(t, workDir)
	for index := 0; index < 32; index++ {
		path := filepath.Join(workDir, "src", fmt.Sprintf("file-%02d.txt", index))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("needle\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager := testManager(t, workDir, nil)
	res, err := manager.Execute(t.Context(), `result = context.list_files(path="src")
[len(result["files"]), result["truncated"]]`)
	if err != nil || res.Error != nil || res.Value != "[32, False]" {
		t.Fatalf("streaming file-index parse=%+v err=%v", res, err)
	}
	if res.FileIndexRefreshes != 1 {
		t.Fatalf("streaming file-index refreshes=%d, want one", res.FileIndexRefreshes)
	}
}

func TestFileStatsDoesNotMaterializeFileList(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	initFileIndexGitRepo(t, workDir)
	const fileCount = 128
	for index := 0; index < fileCount; index++ {
		extension := ".go"
		if index%2 == 0 {
			extension = ".py"
		}
		path := filepath.Join(workDir, "src", fmt.Sprintf("file-%03d%s", index, extension))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager := testManager(t, workDir, nil)
	res, err := manager.Execute(t.Context(), `
r = context.file_stats(path="src", group_by="extension")
[r["matching_files"], r["total_bytes"], r["groups"][".go"], r["groups"][".py"], r["scanned_files"], r["truncated"]]`)
	if err != nil || res.Error != nil {
		t.Fatalf("file_stats=%+v err=%v", res, err)
	}
	want := fmt.Sprintf("[%d, %d, {'files': 64, 'bytes': 128}, {'files': 64, 'bytes': 128}, %d, False]",
		fileCount, fileCount*2, fileCount)
	if res.Value != want {
		t.Fatalf("file_stats=%s, want %s", res.Value, want)
	}
	if res.Operations["file_stats"] != 1 || res.Operations["list_files"] != 0 || res.FileIndexRefreshes != 1 {
		t.Fatalf("file_stats operational evidence=%v refreshes=%d", res.Operations, res.FileIndexRefreshes)
	}
}

func TestFileStatsKeepsLargeInventoryResultInline(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	const fileCount = 2_000
	for index := 0; index < fileCount; index++ {
		path := filepath.Join(workDir, "src", fmt.Sprintf("file-%04d.go", index))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager := testManager(t, workDir, func(opts *Options) { opts.CellTimeout = 10 * time.Second })
	stats, err := manager.Execute(t.Context(), `context.file_stats(group_by="extension")`)
	if err != nil || stats.Error != nil || stats.Artifact != nil || stats.Truncated || len(stats.Value) > 512 ||
		!strings.Contains(stats.Value, "'matching_files': 2000") ||
		!strings.Contains(stats.Value, "'.go': {'files': 2000, 'bytes': 4000}") {
		t.Fatalf("large inventory file_stats=%+v value_bytes=%d err=%v", stats, len(stats.Value), err)
	}
	listed, err := manager.Execute(t.Context(), `context.list_files()`)
	if err != nil || listed.Error != nil || listed.Artifact == nil || !listed.Truncated {
		t.Fatalf("large inventory list_files should overflow to artifact: %+v err=%v", listed, err)
	}
}

func TestCountSamplesRollbackPartialUnreadableFile(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	if err := os.WriteFile(filepath.Join(workDir, "broken.txt"), []byte("placeholder\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t, workDir, nil)
	res, err := manager.Execute(t.Context(), `context._open_searchable_text = lambda candidate: None`)
	if err != nil || res.Error == nil || res.Error.Type != "PermissionError" || len(res.Operations) != 0 {
		t.Fatalf("private search reader remained mutable: result=%+v err=%v", res, err)
	}
}

func TestCountSamplesFitInlineResultBudget(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	line := "needle " + strings.Repeat("x", 400) + "\n"
	if err := os.WriteFile(filepath.Join(workDir, "samples.txt"), []byte(strings.Repeat(line, 25)), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t, workDir, nil)
	res, err := manager.Execute(t.Context(), `context.count_code("needle", path="samples.txt", sample_limit=20)`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != nil || res.Artifact != nil || res.Truncated || len(res.Value) > 8*1024 ||
		!strings.Contains(res.Value, `'samples_truncated': True`) {
		t.Fatalf("compact samples: value_bytes=%d artifact=%+v truncated=%t error=%+v",
			len(res.Value), res.Artifact, res.Truncated, res.Error)
	}
}
