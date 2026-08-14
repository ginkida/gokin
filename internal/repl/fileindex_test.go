package repl

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestWriteFileIndexReportsEntryLimitWithoutPublishingPartialAsExact(t *testing.T) {
	runtimeDir := t.TempDir()
	manager := &Manager{runtimeDir: runtimeDir}
	result, err := manager.writeIndex(func(emit func(string) bool) error {
		for index := 0; index <= maxFileIndexEntries; index++ {
			if !emit("file-" + strconv.Itoa(index)) {
				return errFileIndexFull
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.Entries != maxFileIndexEntries {
		t.Fatalf("bounded index result=%+v", result)
	}
	if result.Path != filepath.Join(runtimeDir, fileIndexRuntimeName) {
		t.Fatalf("index path=%q", result.Path)
	}
	raw, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if entries := bytes.Count(raw, []byte{0}); entries != maxFileIndexEntries {
		t.Fatalf("published entries=%d want=%d", entries, maxFileIndexEntries)
	}
}

func BenchmarkWriteFileIndexLargeInventory(b *testing.B) {
	runtimeDir := b.TempDir()
	manager := &Manager{runtimeDir: runtimeDir}
	paths := make([]string, maxFileIndexEntries)
	for index := range paths {
		paths[index] = "src/package/long-enough-file-name-" + strconv.Itoa(index) + ".go"
	}
	b.ReportAllocs()
	for b.Loop() {
		result, err := manager.writeIndex(func(emit func(string) bool) error {
			for _, path := range paths {
				if !emit(path) {
					return errFileIndexFull
				}
			}
			return nil
		})
		if err != nil || result.Entries != len(paths) || result.Truncated {
			b.Fatalf("writeIndex=%+v err=%v", result, err)
		}
	}
}

func TestValidatedIndexRootRejectsEscapesAndFiles(t *testing.T) {
	workDir := t.TempDir()
	manager := &Manager{opts: Options{WorkDir: workDir}}
	file := filepath.Join(workDir, "regular.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"parent":   "../outside",
		"absolute": file,
		"file":     "regular.txt",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := manager.validatedIndexRoot(map[string]any{"path": path}); err == nil {
				t.Fatalf("unsafe index path %q accepted", path)
			}
		})
	}
}

func TestGitFileIndexSkipsIgnoreProbeForNonEmptyScope(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell wrapper")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}
	workDir := t.TempDir()
	workDir, err = filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatal(err)
	}
	init := exec.Command(gitPath, "init", workDir)
	if output, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, output)
	}
	if err := os.MkdirAll(filepath.Join(workDir, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "src", "visible.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeDir := t.TempDir()
	wrapperDir := t.TempDir()
	logPath := filepath.Join(wrapperDir, "calls.log")
	wrapperPath := filepath.Join(wrapperDir, "git-wrapper")
	wrapper := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellSingleQuote(logPath) + "\nexec " + shellSingleQuote(gitPath) + " \"$@\"\n"
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{runtimeDir: runtimeDir, opts: Options{WorkDir: workDir, GitPath: wrapperPath}}
	result, err := manager.buildFileIndex(context.Background(), map[string]any{"path": "src"})
	if err != nil || result.Entries != 1 {
		t.Fatalf("non-empty native index=%+v err=%v", result, err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(logged), []byte{'\n'})
	if len(lines) != 2 || !bytes.Contains(lines[0], []byte("check-ignore")) ||
		!bytes.Contains(lines[1], []byte("ls-files")) {
		t.Fatalf("non-empty scoped index git calls=%q, want check-ignore + ls-files", logged)
	}
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func TestGitFileIndexDoesNotExecuteRepositoryConfiguredCommands(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	workDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command(gitPath, append([]string{"-C", workDir}, args...)...)
		if output, runErr := cmd.CombinedOutput(); runErr != nil {
			t.Fatalf("git %s: %v (%s)", strings.Join(args, " "), runErr, output)
		}
	}
	runGit("init", "--quiet")
	for name, content := range map[string]string{
		"sample.txt":     "baseline\n",
		".gitattributes": "*.txt filter=evil diff=evil\n",
		".gitignore":     "ignored/\n",
		"ignored/file":   "hidden\n",
	} {
		path := filepath.Join(workDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit("add", "sample.txt", ".gitattributes", ".gitignore")
	runGit("-c", "user.name=Gokin Test", "-c", "user.email=test@example.invalid",
		"commit", "--quiet", "-m", "baseline")

	markers := map[string]string{
		"fsmonitor": filepath.Join(workDir, "fsmonitor-fired"),
		"external":  filepath.Join(workDir, "external-fired"),
		"textconv":  filepath.Join(workDir, "textconv-fired"),
		"filter":    filepath.Join(workDir, "filter-fired"),
		"pager":     filepath.Join(workDir, "pager-fired"),
	}
	writeHook := func(name, marker, tail string) string {
		t.Helper()
		path := filepath.Join(workDir, name)
		content := "#!/bin/sh\n: > \"$(dirname \"$0\")/" + filepath.Base(marker) + "\"\n" + tail
		if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
		return "./" + name
	}
	fsmonitor := writeHook("fsmonitor-hook.sh", markers["fsmonitor"], "exit 1\n")
	external := writeHook("external-diff.sh", markers["external"], "exit 0\n")
	textconv := writeHook("textconv.sh", markers["textconv"], "cat \"$1\"\n")
	filter := writeHook("filter.sh", markers["filter"], "cat\n")
	pager := writeHook("pager.sh", markers["pager"], "cat\n")
	runGit("config", "core.fsmonitor", fsmonitor)
	runGit("config", "diff.external", external)
	runGit("config", "diff.evil.textconv", textconv)
	runGit("config", "filter.evil.clean", filter)
	runGit("config", "core.pager", pager)
	runGit("config", "alias.ls-files", "!touch alias-fired")
	// Prime the clean-filter association, then change the file so ordinary Git
	// would exercise every configured execution route.
	runGit("add", "--renormalize", "sample.txt")
	if err := os.WriteFile(filepath.Join(workDir, "sample.txt"), []byte("changed!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, marker := range markers {
		_ = os.Remove(marker)
	}

	manager := &Manager{
		runtimeDir: t.TempDir(),
		opts:       Options{WorkDir: workDir, GitPath: gitPath},
	}
	root, err := manager.buildFileIndex(t.Context(), map[string]any{"path": "."})
	if err != nil || root.Source != "git" || root.Entries < 3 {
		t.Fatalf("native root index=%+v err=%v", root, err)
	}
	ignored, err := manager.buildFileIndex(t.Context(), map[string]any{"path": "ignored"})
	if err != nil || ignored.Source != "git-explicit" || ignored.Entries != 1 {
		t.Fatalf("native ignored-scope index=%+v err=%v", ignored, err)
	}
	for name, marker := range markers {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("file index executed repository %s command: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(workDir, "alias-fired")); !os.IsNotExist(err) {
		t.Fatalf("file index executed repository alias: %v", err)
	}
}
