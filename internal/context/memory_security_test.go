package context

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestProjectMemory_ProjectOriginImportsStayInsideWorkspace(t *testing.T) {
	home := resolvedInstructionTempDir(t)
	project := resolvedInstructionTempDir(t)
	outside := resolvedInstructionTempDir(t)
	t.Setenv("HOME", home)

	const absoluteSecret = "ABSOLUTE_SECRET_MUST_NOT_REACH_MODEL"
	const homeSecret = "HOME_SECRET_MUST_NOT_REACH_MODEL"
	const symlinkSecret = "SYMLINK_INCLUDE_SECRET_MUST_NOT_REACH_MODEL"
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte(absoluteSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "symlink-secret.md"), []byte(symlinkSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "symlink-secret.md"), filepath.Join(project, "linked-secret.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "secret.md"), []byte(homeSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "relative.md"), []byte("RELATIVE_OK"), 0o600); err != nil {
		t.Fatal(err)
	}
	insideAbsolute := filepath.Join(project, "absolute-inside.md")
	if err := os.WriteFile(insideAbsolute, []byte("ABSOLUTE_INSIDE_OK"), 0o600); err != nil {
		t.Fatal(err)
	}

	projectInstructions := strings.Join([]string{
		"PROJECT_RULES",
		"@./relative.md",
		"@" + insideAbsolute,
		"@" + filepath.Join(outside, "secret.md"),
		"@~/secret.md",
		"@./linked-secret.md",
	}, "\n")
	if err := os.WriteFile(filepath.Join(project, "GOKIN.md"), []byte(projectInstructions), 0o600); err != nil {
		t.Fatal(err)
	}

	pm := NewProjectMemory(project)
	if err := pm.Load(); err != nil {
		t.Fatal(err)
	}
	got := pm.GetInstructions()
	if !strings.Contains(got, "RELATIVE_OK") || !strings.Contains(got, "ABSOLUTE_INSIDE_OK") {
		t.Fatalf("workspace-contained imports were not expanded: %q", got)
	}
	if strings.Contains(got, absoluteSecret) || strings.Contains(got, homeSecret) || strings.Contains(got, symlinkSecret) {
		t.Fatalf("repository instructions exfiltrated an external file: %q", got)
	}
	if !strings.Contains(got, "@"+filepath.Join(outside, "secret.md")) ||
		!strings.Contains(got, "@~/secret.md") ||
		!strings.Contains(got, "@./linked-secret.md") {
		t.Fatalf("blocked imports should remain visible for diagnosis: %q", got)
	}
}

func TestProjectMemory_RejectsProjectRulesDirectorySymlinkEscape(t *testing.T) {
	home := resolvedInstructionTempDir(t)
	project := resolvedInstructionTempDir(t)
	outsideRules := resolvedInstructionTempDir(t)
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(project, "GOKIN.md"), []byte("SAFE_PROJECT"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideRules, "malicious.md"), []byte("RULES_DIRECTORY_SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, ".gokin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideRules, filepath.Join(project, ".gokin", "rules")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	pm := NewProjectMemory(project)
	if err := pm.Load(); err != nil {
		t.Fatal(err)
	}
	got := pm.GetInstructions()
	if strings.Contains(got, "RULES_DIRECTORY_SECRET") || !strings.Contains(got, "SAFE_PROJECT") {
		t.Fatalf("symlinked project rules directory escaped containment: %q", got)
	}
	if strings.Contains(pm.GetSourcePath(), "malicious.md") {
		t.Fatalf("external rules file appeared in sources: %q", pm.GetSourcePath())
	}
}

func TestProjectMemory_UserLayersKeepExplicitExternalImportsAndPriority(t *testing.T) {
	home := resolvedInstructionTempDir(t)
	project := resolvedInstructionTempDir(t)
	outside := resolvedInstructionTempDir(t)
	t.Setenv("HOME", home)

	if err := os.MkdirAll(filepath.Join(home, ".config", "gokin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".gokin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "nested.md"), []byte("GLOBAL_NESTED_RELATIVE_IMPORT"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "global-import.md"), []byte("GLOBAL_ABSOLUTE_IMPORT\n@./nested.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "home-import.md"), []byte("GLOBAL_HOME_IMPORT"), 0o600); err != nil {
		t.Fatal(err)
	}
	global := "GLOBAL_LAYER\n@" + filepath.Join(outside, "global-import.md") + "\n@~/home-import.md"
	if err := os.WriteFile(filepath.Join(home, ".config", "gokin", "GOKIN.md"), []byte(global), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gokin", "GOKIN.md"), []byte("USER_LAYER"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "GOKIN.md"), []byte("PROJECT_LAYER"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "GOKIN.local.md"), []byte("LOCAL_LAYER"), 0o600); err != nil {
		t.Fatal(err)
	}

	pm := NewProjectMemory(project)
	if err := pm.Load(); err != nil {
		t.Fatal(err)
	}
	got := pm.GetInstructions()
	for _, want := range []string{"GLOBAL_ABSOLUTE_IMPORT", "GLOBAL_NESTED_RELATIVE_IMPORT", "GLOBAL_HOME_IMPORT", "USER_LAYER", "PROJECT_LAYER", "LOCAL_LAYER"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q from merged instructions: %q", want, got)
		}
	}
	globalPos := strings.Index(got, "GLOBAL_LAYER")
	userPos := strings.Index(got, "USER_LAYER")
	projectPos := strings.Index(got, "PROJECT_LAYER")
	localPos := strings.Index(got, "LOCAL_LAYER")
	if !(globalPos < userPos && userPos < projectPos && projectPos < localPos) {
		t.Fatalf("layer priority order changed: global=%d user=%d project=%d local=%d\n%s", globalPos, userPos, projectPos, localPos, got)
	}
}

func TestMergeInstructionDocuments_SizeLimitPreservesHighestPriority(t *testing.T) {
	documents := []instructionDocument{
		{label: "global", source: "global", text: strings.Repeat("g", maxMergedInstructionBytes)},
		{label: "local", source: "local", text: "LOCAL_MUST_WIN"},
	}
	got, sources := mergeInstructionDocuments(documents)
	if got != "LOCAL_MUST_WIN" {
		t.Fatalf("lower-priority content crowded out local instructions: len=%d prefix=%q", len(got), firstInstructionBytes(got, 32))
	}
	if len(sources) != 1 || sources[0] != "local" {
		t.Fatalf("source diagnostics do not match selected priority layer: %v", sources)
	}
}

func TestProjectMemory_RejectsProjectInstructionSymlinkEscape(t *testing.T) {
	home := resolvedInstructionTempDir(t)
	project := resolvedInstructionTempDir(t)
	outside := resolvedInstructionTempDir(t)
	t.Setenv("HOME", home)

	secretPath := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(secretPath, []byte("SYMLINK_SOURCE_SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretPath, filepath.Join(project, "GOKIN.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(project, "CLAUDE.md"), []byte("SAFE_FALLBACK"), 0o600); err != nil {
		t.Fatal(err)
	}

	pm := NewProjectMemory(project)
	if err := pm.Load(); err != nil {
		t.Fatal(err)
	}
	got := pm.GetInstructions()
	if strings.Contains(got, "SYMLINK_SOURCE_SECRET") || !strings.Contains(got, "SAFE_FALLBACK") {
		t.Fatalf("unsafe source symlink was followed or fallback was lost: %q", got)
	}
	if strings.Contains(pm.GetSourcePath(), filepath.Join(project, "GOKIN.md")) {
		t.Fatalf("rejected source appeared in source diagnostics: %q", pm.GetSourcePath())
	}
}

func TestProjectMemory_RecursiveIncludesAreBoundedAndUTF8Safe(t *testing.T) {
	t.Run("cycle", func(t *testing.T) {
		home := resolvedInstructionTempDir(t)
		project := resolvedInstructionTempDir(t)
		t.Setenv("HOME", home)
		if err := os.WriteFile(filepath.Join(project, "GOKIN.md"), []byte("@./a.md"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(project, "a.md"), []byte("A_ONCE\n@./b.md"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(project, "b.md"), []byte("B_ONCE\n@./a.md"), 0o600); err != nil {
			t.Fatal(err)
		}

		pm := NewProjectMemory(project)
		if err := pm.Load(); err != nil {
			t.Fatal(err)
		}
		got := pm.GetInstructions()
		if strings.Count(got, "A_ONCE") != 1 || strings.Count(got, "B_ONCE") != 1 || !strings.Contains(got, "@./a.md") {
			t.Fatalf("include cycle was not terminated deterministically: %q", got)
		}
	})

	t.Run("depth", func(t *testing.T) {
		home := resolvedInstructionTempDir(t)
		project := resolvedInstructionTempDir(t)
		t.Setenv("HOME", home)
		if err := os.WriteFile(filepath.Join(project, "GOKIN.md"), []byte("ROOT\n@./level-0.md"), 0o600); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < maxInstructionIncludeDepth+3; i++ {
			next := fmt.Sprintf("@./level-%d.md", i+1)
			if i == maxInstructionIncludeDepth+2 {
				next = "@./level-0.md"
			}
			content := fmt.Sprintf("LEVEL_%d\n%s", i, next)
			if err := os.WriteFile(filepath.Join(project, fmt.Sprintf("level-%d.md", i)), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		pm := NewProjectMemory(project)
		if err := pm.Load(); err != nil {
			t.Fatal(err)
		}
		got := pm.GetInstructions()
		if !strings.Contains(got, "LEVEL_0") || !strings.Contains(got, "LEVEL_7") {
			t.Fatalf("recursive includes stopped too early: %q", got)
		}
		if strings.Contains(got, "LEVEL_8") {
			t.Fatalf("include depth limit was bypassed: %q", got)
		}
		if !strings.Contains(got, "@./level-8.md") {
			t.Fatalf("depth-blocked directive should remain visible: %q", got)
		}
	})

	t.Run("invalid UTF-8 and oversized include", func(t *testing.T) {
		home := resolvedInstructionTempDir(t)
		project := resolvedInstructionTempDir(t)
		t.Setenv("HOME", home)
		if err := os.WriteFile(filepath.Join(project, "invalid.md"), []byte{0xff, 0xfe}, 0o600); err != nil {
			t.Fatal(err)
		}
		oversized := strings.Repeat("x", maxInstructionFileBytes+1)
		if err := os.WriteFile(filepath.Join(project, "oversized.md"), []byte(oversized), 0o600); err != nil {
			t.Fatal(err)
		}
		root := "SAFE\n@./invalid.md\n@./oversized.md"
		if err := os.WriteFile(filepath.Join(project, "GOKIN.md"), []byte(root), 0o600); err != nil {
			t.Fatal(err)
		}

		pm := NewProjectMemory(project)
		if err := pm.Load(); err != nil {
			t.Fatal(err)
		}
		got := pm.GetInstructions()
		if !utf8.ValidString(got) || !strings.Contains(got, "@./invalid.md") || !strings.Contains(got, "@./oversized.md") {
			t.Fatalf("invalid/oversized includes were not rejected safely: %q", got)
		}
		if len(got) > maxExpandedInstructionBytes {
			t.Fatalf("expanded instructions exceeded budget: %d", len(got))
		}
	})

	t.Run("invalid root falls through to safe alias", func(t *testing.T) {
		home := resolvedInstructionTempDir(t)
		project := resolvedInstructionTempDir(t)
		t.Setenv("HOME", home)
		if err := os.WriteFile(filepath.Join(project, "GOKIN.md"), []byte{0xff, 0xfe}, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(project, "CLAUDE.md"), []byte("UTF8_FALLBACK"), 0o600); err != nil {
			t.Fatal(err)
		}
		pm := NewProjectMemory(project)
		if err := pm.Load(); err != nil {
			t.Fatal(err)
		}
		if got := pm.GetInstructions(); got != "UTF8_FALLBACK" || !utf8.ValidString(got) {
			t.Fatalf("invalid primary source did not fall through safely: %q", got)
		}
	})
}

func TestProjectMemory_WatchesIncludeAndHigherPriorityCreation(t *testing.T) {
	home := resolvedInstructionTempDir(t)
	project := resolvedInstructionTempDir(t)
	t.Setenv("HOME", home)
	claudePath := filepath.Join(project, "CLAUDE.md")
	includePath := filepath.Join(project, "included.md")
	if err := os.WriteFile(includePath, []byte("INCLUDE_V1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudePath, []byte("CLAUDE\n@./included.md"), 0o600); err != nil {
		t.Fatal(err)
	}

	pm := NewProjectMemory(project)
	if err := pm.Load(); err != nil {
		t.Fatal(err)
	}
	reloaded := make(chan struct{}, 8)
	pm.OnReload(func() {
		select {
		case reloaded <- struct{}{}:
		default:
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pm.StartWatching(ctx, 10); err != nil {
		t.Fatal(err)
	}
	defer pm.StopWatching()

	pm.mu.RLock()
	includeWatcher := pm.extraWatchers[includePath]
	higherPriorityWatcher := pm.extraWatchers[filepath.Join(project, "GOKIN.md")]
	primary := pm.watcher
	pm.mu.RUnlock()
	if includeWatcher == nil || higherPriorityWatcher == nil || primary == nil {
		t.Fatalf("missing dependency watchers: include=%v higher=%v primary=%v", includeWatcher, higherPriorityWatcher, primary)
	}

	if err := os.WriteFile(includePath, []byte("INCLUDE_V2"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(includePath, future, future); err != nil {
		t.Fatal(err)
	}
	includeWatcher.checkChanges()
	waitForInstructionReload(t, reloaded, "included file edit")
	if got := pm.GetInstructions(); !strings.Contains(got, "INCLUDE_V2") {
		t.Fatalf("included file edit remained stale: %q", got)
	}

	if err := os.Remove(includePath); err != nil {
		t.Fatal(err)
	}
	includeWatcher.checkChanges()
	waitForInstructionReload(t, reloaded, "included file deletion")
	if got := pm.GetInstructions(); strings.Contains(got, "INCLUDE_V2") || !strings.Contains(got, "@./included.md") {
		t.Fatalf("deleted include remained active or lost its diagnostic directive: %q", got)
	}
	pm.mu.RLock()
	recreateWatcher := pm.extraWatchers[includePath]
	pm.mu.RUnlock()
	if recreateWatcher == nil {
		t.Fatal("deleted include dependency was no longer watched for recreation")
	}
	if err := os.WriteFile(includePath, []byte("INCLUDE_V3"), 0o600); err != nil {
		t.Fatal(err)
	}
	recreateWatcher.checkChanges()
	waitForInstructionReload(t, reloaded, "included file recreation")
	if got := pm.GetInstructions(); !strings.Contains(got, "INCLUDE_V3") {
		t.Fatalf("recreated include was not restored: %q", got)
	}

	gokinPath := filepath.Join(project, "GOKIN.md")
	if err := os.WriteFile(gokinPath, []byte("GOKIN_HIGHER_PRIORITY"), 0o600); err != nil {
		t.Fatal(err)
	}
	higherPriorityWatcher.checkChanges()
	waitForInstructionReload(t, reloaded, "higher-priority file creation")
	if got := pm.GetInstructions(); !strings.Contains(got, "GOKIN_HIGHER_PRIORITY") || strings.Contains(got, "CLAUDE") {
		t.Fatalf("higher-priority project alias did not take over: %q", got)
	}
	if got := primary.Path(); got != gokinPath {
		t.Fatalf("stable primary watcher did not rebind: got %q want %q", got, gokinPath)
	}
}

func TestProjectMemory_StaleWatcherGenerationCannotReloadOrNotify(t *testing.T) {
	home := resolvedInstructionTempDir(t)
	project := resolvedInstructionTempDir(t)
	t.Setenv("HOME", home)
	path := filepath.Join(project, "GOKIN.md")
	if err := os.WriteFile(path, []byte("GENERATION_V1"), 0o600); err != nil {
		t.Fatal(err)
	}
	pm := NewProjectMemory(project)
	if err := pm.Load(); err != nil {
		t.Fatal(err)
	}
	var callbackMu sync.Mutex
	callbacks := 0
	pm.OnReload(func() {
		callbackMu.Lock()
		callbacks++
		callbackMu.Unlock()
	})

	ctx1, cancel1 := context.WithCancel(context.Background())
	if err := pm.StartWatching(ctx1, 10); err != nil {
		t.Fatal(err)
	}
	pm.mu.RLock()
	oldCallback := pm.watcher.callback
	pm.mu.RUnlock()
	pm.StopWatching()
	cancel1()

	if err := os.WriteFile(path, []byte("GENERATION_V2"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldCallback(path)
	if got := pm.GetInstructions(); !strings.Contains(got, "GENERATION_V1") || strings.Contains(got, "GENERATION_V2") {
		t.Fatalf("stopped watcher committed a stale reload: %q", got)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	if err := pm.StartWatching(ctx2, 10); err != nil {
		t.Fatal(err)
	}
	defer pm.StopWatching()
	pm.mu.RLock()
	newCallback := pm.watcher.callback
	pm.mu.RUnlock()

	// Restarting creates a new generation; an already-obtained callback from
	// the previous generation must remain inert.
	oldCallback(path)
	if got := pm.GetInstructions(); strings.Contains(got, "GENERATION_V2") {
		t.Fatalf("old watcher generation reloaded after restart: %q", got)
	}
	callbackMu.Lock()
	gotCallbacks := callbacks
	callbackMu.Unlock()
	if gotCallbacks != 0 {
		t.Fatalf("stale watcher invoked OnReload %d times", gotCallbacks)
	}

	newCallback(path)
	if got := pm.GetInstructions(); !strings.Contains(got, "GENERATION_V2") {
		t.Fatalf("current watcher generation failed to reload: %q", got)
	}
	callbackMu.Lock()
	gotCallbacks = callbacks
	callbackMu.Unlock()
	if gotCallbacks != 1 {
		t.Fatalf("current watcher OnReload calls=%d, want 1", gotCallbacks)
	}
}

func TestProjectMemory_ConcurrentLoadPublishesWholeSnapshots(t *testing.T) {
	home := resolvedInstructionTempDir(t)
	project := resolvedInstructionTempDir(t)
	t.Setenv("HOME", home)
	path := filepath.Join(project, "GOKIN.md")
	valueA := "A:" + strings.Repeat("a", 32<<10)
	valueB := "B:" + strings.Repeat("b", 32<<10)
	if err := os.WriteFile(path, []byte(valueA), 0o600); err != nil {
		t.Fatal(err)
	}
	pm := NewProjectMemory(project)
	if err := pm.Load(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	failures := make(chan string, 1)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 500; j++ {
				got := pm.GetInstructions()
				if got != valueA && got != valueB {
					select {
					case failures <- got:
					default:
					}
					return
				}
			}
		}()
	}
	close(start)
	for i := 0; i < 40; i++ {
		value := valueA
		if i%2 == 0 {
			value = valueB
		}
		tmp := filepath.Join(project, fmt.Sprintf("instructions-%d.tmp", i))
		if err := os.WriteFile(tmp, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
		if err := pm.Load(); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	select {
	case got := <-failures:
		t.Fatalf("reader observed a partial/mixed instruction snapshot (len=%d, prefix=%q)", len(got), firstInstructionBytes(got, 16))
	default:
	}
}

func waitForInstructionReload(t *testing.T, reloaded <-chan struct{}, reason string) {
	t.Helper()
	select {
	case <-reloaded:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for instruction reload after %s", reason)
	}
}

func resolvedInstructionTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return dir
	}
	return resolved
}

func firstInstructionBytes(value string, count int) string {
	if len(value) <= count {
		return value
	}
	return value[:count]
}
