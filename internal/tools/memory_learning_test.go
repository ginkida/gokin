package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"gokin/internal/memory"
)

// --- writtenPathsFromResult: the generic, defensive extractor -------------

func TestWrittenPathsFromResult(t *testing.T) {
	cases := []struct {
		name string
		res  ToolResult
		want []string
	}{
		{"nil data", NewSuccessResult("x"), nil},
		{"string slice", NewSuccessResultWithData("x", map[string]any{"written_paths": []string{"/a", "/b"}}), []string{"/a", "/b"}},
		{"any slice", NewSuccessResultWithData("x", map[string]any{"written_paths": []any{"/a", "/b"}}), []string{"/a", "/b"}},
		{"empties filtered", NewSuccessResultWithData("x", map[string]any{"written_paths": []string{"", "/b", ""}}), []string{"/b"}},
		{"missing key", NewSuccessResultWithData("x", map[string]any{"other": 1}), nil},
		{"wrong value shape", NewSuccessResultWithData("x", map[string]any{"written_paths": 42}), nil},
		{"data not a map", ToolResult{Success: true, Data: []string{"/a"}}, nil},
		{"any slice with non-strings", NewSuccessResultWithData("x", map[string]any{"written_paths": []any{"/a", 7, "/b"}}), []string{"/a", "/b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := writtenPathsFromResult(tc.res)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d (%v), want %d (%v)", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// --- memorize: honest message + declared written_paths --------------------

func TestMemorize_HonestMessageAndWrittenPaths(t *testing.T) {
	dir := t.TempDir()
	pl, err := memory.NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	mt := NewMemorizeTool(pl)

	res, err := mt.Execute(context.Background(), map[string]any{
		"type":    "fact",
		"key":     "test_command",
		"content": "go test -race ./...",
	})
	if err != nil || !res.Success {
		t.Fatalf("Execute failed: err=%v success=%v content=%q", err, res.Success, res.Content)
	}
	// A real write happened -> the message reports it (not the old unconditional
	// "(updated <path>)" that lied on a no-op).
	if !strings.Contains(res.Content, "updated") {
		t.Errorf("expected an honest 'updated' message after a real write, got: %q", res.Content)
	}
	if strings.Contains(res.Content, "already current") {
		t.Errorf("a real write must not say 'already current': %q", res.Content)
	}

	// written_paths must list BOTH on-disk files, and they must exist + contain
	// the memorized content so the executor can invalidate the read-dedup.
	paths := writtenPathsFromResult(res)
	if len(paths) != 2 {
		t.Fatalf("expected 2 written_paths (learning.yaml + project-memory.md), got %d: %v", len(paths), paths)
	}
	foundMarkdownWithContent := false
	for _, p := range paths {
		if p == "" {
			t.Errorf("written path must be non-empty")
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("declared written path does not exist on disk: %s (%v)", p, err)
			continue
		}
		if strings.HasSuffix(p, ".md") && strings.Contains(string(b), "go test -race ./...") {
			foundMarkdownWithContent = true
		}
	}
	if !foundMarkdownWithContent {
		t.Errorf("project-memory.md should contain the memorized fact; paths=%v", paths)
	}

	// The declared markdown path must match the store's own accessor.
	if md := pl.MarkdownPath(); md == "" || !sliceHasString(paths, md) {
		t.Errorf("MarkdownPath()=%q not present in written_paths %v", md, paths)
	}
}

// --- memory list surfaces project-learning saved via memorize -------------

func TestMemoryList_SurfacesLearningWhenStoreEmpty(t *testing.T) {
	dir := t.TempDir()
	pl, err := memory.NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	// Save knowledge via memorize.
	mzt := NewMemorizeTool(pl)
	if _, err := mzt.Execute(context.Background(), map[string]any{
		"type": "convention", "key": "logging", "content": "use structured slog",
	}); err != nil {
		t.Fatalf("memorize: %v", err)
	}

	// `memory list` with NO keyed store wired must still surface the learning,
	// not report "No memories stored" (the field report's symptom).
	mt := NewMemoryTool()
	mt.SetLearning(pl)
	res, err := mt.Execute(context.Background(), map[string]any{"action": "list"})
	if err != nil || !res.Success {
		t.Fatalf("memory list failed: err=%v success=%v", err, res.Success)
	}
	if strings.Contains(res.Content, "No memories stored") {
		t.Errorf("list must not say 'No memories stored' when learning has content:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "use structured slog") {
		t.Errorf("list must surface the memorized convention:\n%s", res.Content)
	}
	if has, _ := res.Data.(map[string]any)["has_learning"].(bool); !has {
		t.Errorf("result Data should flag has_learning=true; got %#v", res.Data)
	}
}

// --- executor seam: memorize's write invalidates the read-dedup -----------

// fakeWriterTool declares written_paths in its result without touching the
// files, so the test isolates the executor's invalidation from filesystem
// mtime self-healing.
type fakeWriterTool struct{ written []string }

func (f *fakeWriterTool) Name() string { return "fakewriter" }
func (f *fakeWriterTool) Description() string {
	return "test tool that declares written_paths"
}
func (f *fakeWriterTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "fakewriter", Description: "test"}
}
func (f *fakeWriterTool) Validate(args map[string]any) error { return nil }
func (f *fakeWriterTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	return NewSuccessResultWithData("ok", map[string]any{"written_paths": f.written}), nil
}

func TestExecutorInvalidatesReadDedupOnWrittenPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project-memory.md")
	if err := os.WriteFile(path, []byte("# memory\nold\n"), 0644); err != nil {
		t.Fatal(err)
	}

	registry := NewRegistry()
	if err := registry.Register(&fakeWriterTool{written: []string{path}}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	exec := NewExecutor(registry, nil, time.Second)
	rt := NewFileReadTracker()
	wt := NewFileWriteTracker()
	exec.SetReadTracker(rt)
	exec.SetWriteTracker(wt)

	// Read once (records), read again (now a duplicate — dedup is active).
	if dup, _, _, _ := rt.CheckAndRecord(path, 1, 2000, 12); dup {
		t.Fatalf("first read must not be a duplicate")
	}
	if dup, _, _, _ := rt.CheckAndRecord(path, 1, 2000, 12); !dup {
		t.Fatalf("second identical read must be a duplicate (dedup active) before invalidation")
	}

	// The fake tool declares it wrote `path` but does NOT change the file, so
	// mtime/size are identical — only the executor's written_paths invalidation
	// can make the next read fresh again.
	res := exec.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "w1", Name: "fakewriter", Args: map[string]any{},
	})
	if !res.Success {
		t.Fatalf("fakewriter should succeed, got %q", res.Content)
	}

	if dup, _, _, _ := rt.CheckAndRecord(path, 1, 2000, 12); dup {
		t.Errorf("after memorize-style write, a re-read must NOT be a duplicate (the 'cannot re-read to verify' bug)")
	}
	// And the write was recorded for post-compaction hints.
	if got := wt.RecentlyModifiedFiles(10); !sliceHasString(got, path) {
		t.Errorf("write tracker should record the declared path %q, got %v", path, got)
	}
}

func sliceHasString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
