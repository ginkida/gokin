package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gokin/internal/gitexec"
	"gokin/internal/testkit"
)

func writeFile(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotGitRuntimeExclusions(t *testing.T) {
	root := gitFixture(t)
	before := Capture(t.Context(), root, DefaultLimits(), nil)
	for _, path := range []string{".gokin/task-output/task.log", ".gokin/agent-output/agent.log", ".gokin/execution_journal.jsonl", ".gokin-tmp/scratch", ".gokin/cache/generated", "sub/.gokin/task-output/task.log"} {
		writeFile(t, root, path, "runtime output")
	}
	after := Capture(t.Context(), root, DefaultLimits(), before)
	if d := Diff(t.Context(), before, after); d.Partial || len(d.Paths) != 0 {
		t.Fatalf("Gokin runtime IO reported as user writes: %+v", d)
	}
}

func TestSnapshotRepositoryCacheInvalidatesWhenGitDisappears(t *testing.T) {
	root := gitFixture(t)
	before := Capture(t.Context(), root, DefaultLimits(), nil)
	if err := os.Rename(filepath.Join(root, ".git"), filepath.Join(t.TempDir(), "moved-git")); err != nil {
		t.Fatal(err)
	}
	after := Capture(t.Context(), root, DefaultLimits(), before)
	if before.Partial || after.Partial || !before.Repo || after.Repo {
		t.Fatalf("stale repository discovery: before=%+v after=%+v", before, after)
	}
	if delta := Diff(t.Context(), before, after); !delta.Partial {
		t.Fatalf("repository transition claimed exact attribution: %+v", delta)
	}
}

func TestSnapshotIgnoreRulesDoNotInventWrites(t *testing.T) {
	for _, removing := range []bool{false, true} {
		t.Run(map[bool]string{false: "ignore", true: "unignore"}[removing], func(t *testing.T) {
			root := gitFixture(t)
			writeFile(t, root, "draft.go", "unchanged\n")
			initial, final := "", "draft.go\n"
			if removing {
				initial, final = final, initial
			}
			writeFile(t, root, ".gitignore", initial)
			runGit(t, root, "add", ".gitignore")
			runGit(t, root, "commit", "-qm", "ignore baseline")
			before := Capture(t.Context(), root, DefaultLimits(), nil)
			writeFile(t, root, ".gitignore", final)
			after := Capture(t.Context(), root, DefaultLimits(), before)
			if d := Diff(t.Context(), before, after); d.Partial || !reflect.DeepEqual(d.Paths, []string{filepath.Join(root, ".gitignore")}) {
				t.Fatalf("enumeration membership is not a filesystem change: %+v", d)
			}
		})
	}
}

func TestSnapshotIgnoreChangeAfterIgnoredCreationDoesNotInventWrite(t *testing.T) {
	for _, change := range []string{"ignore rules", "stage", "commit", "rename"} {
		t.Run(change, func(t *testing.T) {
			t.Setenv("GIT_OPTIONAL_LOCKS", "0")
			root := gitFixture(t)
			writeFile(t, root, ".gitignore", "draft.go\n")
			runGit(t, root, "add", ".gitignore")
			runGit(t, root, "commit", "-qm", "ignore draft")
			before := Capture(t.Context(), root, DefaultLimits(), nil)
			writeFile(t, root, "draft.go", "old\n")
			before = Capture(t.Context(), root, DefaultLimits(), before)
			var want []string
			switch change {
			case "ignore rules":
				writeFile(t, root, ".gitignore", "")
				want = []string{filepath.Join(root, ".gitignore")}
			case "stage", "commit":
				runGit(t, root, "add", "-f", "draft.go")
				if change == "commit" {
					runGit(t, root, "commit", "-qm", "track existing bytes")
				}
			case "rename":
				runGit(t, root, "mv", "-f", "value.go", "draft.go")
				want = []string{filepath.Join(root, "value.go")}
			}
			after := Capture(t.Context(), root, DefaultLimits(), before)
			d := Diff(t.Context(), before, after)
			if !reflect.DeepEqual(d.Paths, want) {
				t.Fatalf("Git visibility invented an earlier creation: %+v want=%v", d, want)
			}
			if _, known := before.files["draft.go"]; !known && !d.Partial {
				t.Fatalf("missing ignored-file baseline was claimed exact: %+v", d)
			}
		})
	}
}

func TestSnapshotIgnoreChangeAfterIgnoredModificationDoesNotInventWrite(t *testing.T) {
	t.Setenv("GIT_OPTIONAL_LOCKS", "0")
	root := gitFixture(t)
	writeFile(t, root, ".gitignore", "draft.go\n")
	writeFile(t, root, "draft.go", "baseline\n")
	runGit(t, root, "add", ".gitignore")
	runGit(t, root, "commit", "-qm", "ignore draft")
	before := Capture(t.Context(), root, DefaultLimits(), nil)
	writeFile(t, root, "draft.go", "earlier write\n")
	before = Capture(t.Context(), root, DefaultLimits(), before)
	writeFile(t, root, ".gitignore", "")
	after := Capture(t.Context(), root, DefaultLimits(), before)
	d := Diff(t.Context(), before, after)
	if !d.Partial || !reflect.DeepEqual(d.Paths, []string{filepath.Join(root, ".gitignore")}) {
		t.Fatalf("unignore attributed a stale ignored-file baseline: %+v", d)
	}
}

func TestSnapshotCRLFUsesWorktreeBytes(t *testing.T) {
	for _, attributes := range []bool{false, true} {
		t.Run(map[bool]string{false: "autocrlf", true: "attribute"}[attributes], func(t *testing.T) {
			root := gitFixture(t)
			if attributes {
				writeFile(t, root, ".gitattributes", "*.go text eol=crlf\n")
				runGit(t, root, "add", ".gitattributes")
				runGit(t, root, "commit", "-qm", "attributes")
			} else {
				runGit(t, root, "config", "core.autocrlf", "true")
			}
			writeFile(t, root, "value.go", "dirty\r\n")
			before := Capture(t.Context(), root, DefaultLimits(), nil)
			runGit(t, root, "commit", "-am", "commit without changing worktree")
			after := Capture(t.Context(), root, DefaultLimits(), before)
			if d := Diff(t.Context(), before, after); d.Partial || len(d.Paths) != 0 {
				t.Fatalf("commit changed no worktree bytes: %+v", d)
			}
			before = after
			runGit(t, root, "checkout", "--", "value.go")
			after = Capture(t.Context(), root, DefaultLimits(), before)
			if d := Diff(t.Context(), before, after); d.Partial || len(d.Paths) != 0 {
				t.Fatalf("CRLF checkout changed no worktree bytes: %+v", d)
			}
		})
	}
}

func TestSnapshotCRLFNormalizationThenIndexOnlyChange(t *testing.T) {
	root := gitFixture(t)
	runGit(t, root, "config", "core.autocrlf", "true")
	writeFile(t, root, ".gitattributes", "*.go text eol=crlf\n")
	before := Capture(t.Context(), root, DefaultLimits(), nil)
	if err := os.Remove(filepath.Join(root, "value.go")); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "checkout", "--", "value.go")
	after := Capture(t.Context(), root, DefaultLimits(), before)
	if d := Diff(t.Context(), before, after); d.Partial || len(d.Paths) != 0 {
		t.Fatalf("clean normalization reported a write: %+v", d)
	}
	content, err := os.ReadFile(filepath.Join(root, "value.go"))
	if err != nil || string(content) != "old\r\n" {
		t.Fatalf("checkout did not normalize the fixture: %q %v", content, err)
	}
	before = after
	runGit(t, root, "rm", "--cached", "value.go")
	after = Capture(t.Context(), root, DefaultLimits(), before)
	if d := Diff(t.Context(), before, after); d.Partial || len(d.Paths) != 0 {
		t.Fatalf("index-only change compared against stale LF bytes: %+v", d)
	}
}

func TestSnapshotWarmCaptureUsesOneGitLaunch(t *testing.T) {
	root := gitFixture(t)
	bin := testkit.ResolvedTempDir(t)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(bin, "calls")
	wrapper := filepath.Join(bin, "git")
	script := "#!/bin/sh\nprintf 'call\\n' >> '" + counter + "'\nexec '" + git + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := gitexec.WithExecutable(context.Background(), wrapper)
	before := Capture(ctx, root, DefaultLimits(), nil)
	if err := os.WriteFile(counter, nil, 0600); err != nil {
		t.Fatal(err)
	}
	after := Capture(ctx, root, DefaultLimits(), before)
	calls, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if before.Partial || after.Partial || strings.Count(string(calls), "call\n") != 1 {
		t.Fatalf("warm capture must use one status launch, got %q (%s)", calls, after.Warning)
	}
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := gitexec.CommandContext(t.Context(), append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s %v", args, out, err)
	}
}

func gitFixture(t *testing.T) string {
	t.Helper()
	root := testkit.ResolvedTempDir(t)
	runGit(t, root, "init", "-q")
	writeFile(t, root, "value.go", "old\n")
	writeFile(t, root, "untouched.go", "same\n")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-qm", "initial")
	return root
}

func TestSnapshotGitContentDeltas(t *testing.T) {
	for _, scenario := range []string{"modify", "dirty modify", "dirty unchanged", "stage only", "commit only", "new", "delete", "rename", "checkout file", "checkout HEAD"} {
		t.Run(scenario, func(t *testing.T) {
			root := gitFixture(t)
			if scenario == "dirty modify" || scenario == "dirty unchanged" || scenario == "stage only" || scenario == "commit only" || scenario == "checkout file" {
				writeFile(t, root, "value.go", "pre\n")
			}
			if scenario == "checkout HEAD" {
				writeFile(t, root, "value.go", "new\n")
				runGit(t, root, "commit", "-am", "second")
			}
			before := Capture(t.Context(), root, DefaultLimits(), nil)
			want := []string{filepath.Join(root, "value.go")}
			switch scenario {
			case "modify", "dirty modify":
				writeFile(t, root, "value.go", "new\n")
			case "dirty unchanged":
				want = nil
			case "stage only":
				runGit(t, root, "add", "value.go")
				want = nil
			case "commit only":
				runGit(t, root, "commit", "-am", "unchanged content")
				want = nil
			case "new":
				writeFile(t, root, "new file\n.go", "new\n")
				want = []string{filepath.Join(root, "new file\n.go")}
			case "delete":
				if err := os.Remove(filepath.Join(root, "value.go")); err != nil {
					t.Fatal(err)
				}
			case "rename":
				runGit(t, root, "mv", "value.go", "renamed.go")
				want = []string{filepath.Join(root, "renamed.go"), filepath.Join(root, "value.go")}
			case "checkout file":
				runGit(t, root, "checkout", "--", "value.go")
			case "checkout HEAD":
				runGit(t, root, "checkout", "--detach", "HEAD~1")
			}
			after := Capture(t.Context(), root, DefaultLimits(), before)
			d := Diff(t.Context(), before, after)
			if before.Partial || after.Partial || d.Partial || !reflect.DeepEqual(d.Paths, want) {
				t.Fatalf("before=%+v after=%+v delta=%+v want=%q", before, after, d, want)
			}
		})
	}
}

func TestSnapshotGitSubdirectory(t *testing.T) {
	root := gitFixture(t)
	writeFile(t, root, "sub dir/value.go", "old\n")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-qm", "subdirectory")
	sub := filepath.Join(root, "sub dir")
	before := Capture(t.Context(), sub, DefaultLimits(), nil)
	writeFile(t, root, "sub dir/value.go", "new\n")
	writeFile(t, root, "untouched.go", "outside snapshot root\n")
	after := Capture(t.Context(), sub, DefaultLimits(), before)
	d := Diff(t.Context(), before, after)
	if d.Partial || !reflect.DeepEqual(d.Paths, []string{filepath.Join(sub, "value.go")}) {
		t.Fatalf("subdirectory delta=%+v", d)
	}
}

func TestSnapshotNonGitContentAndCachedMetadata(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	writeFile(t, root, "value.go", "old\n")
	writeFile(t, root, "untouched.go", "same\n")
	before := Capture(t.Context(), root, DefaultLimits(), nil)
	info, err := os.Stat(filepath.Join(root, "value.go"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "value.go", "new\n")
	// Size and mtime alone cannot validate a cached content fingerprint.
	if err := os.Chtimes(filepath.Join(root, "value.go"), info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after := Capture(t.Context(), root, DefaultLimits(), before)
	d := Diff(t.Context(), before, after)
	if d.Partial || !reflect.DeepEqual(d.Paths, []string{filepath.Join(root, "value.go")}) {
		t.Fatalf("cached delta=%+v", d)
	}
	unchanged := Capture(t.Context(), root, DefaultLimits(), after)
	if d := Diff(t.Context(), after, unchanged); d.Partial || len(d.Paths) != 0 {
		t.Fatalf("untouched files reported: %+v", d)
	}
}

func TestSnapshotCapsAndSymlinkBoundary(t *testing.T) {
	for _, scenario := range []string{"file count", "total bytes", "single file", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			root := testkit.ResolvedTempDir(t)
			limits := DefaultLimits()
			writeFile(t, root, "one", "one")
			writeFile(t, root, "two", "two")
			switch scenario {
			case "file count":
				limits.MaxFiles = 1
			case "total bytes":
				limits.MaxBytes = 5
			case "single file":
				f, err := os.Create(filepath.Join(root, "large"))
				if err != nil {
					t.Fatal(err)
				}
				err = f.Truncate(maxFileBytes + 1)
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			case "symlink":
				outside := testkit.ResolvedTempDir(t)
				writeFile(t, outside, "secret", "external content")
				if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
			}
			s := Capture(t.Context(), root, limits, nil)
			if scenario == "symlink" {
				if s.Partial || len(s.files) != 3 || s.files["link/secret"].value != "" {
					t.Fatalf("symlink followed: %+v", s)
				}
				return
			}
			if !s.Partial || s.Warning == "" {
				t.Fatalf("cap silently accepted: %+v", s)
			}
			if d := Diff(t.Context(), s, s); !d.Partial || len(d.Paths) != 0 {
				t.Fatalf("partial baseline invented changes: %+v", d)
			}
		})
	}
}

func TestSnapshotLimitsEnvironment(t *testing.T) {
	t.Setenv("GOKIN_WORKSPACE_SNAPSHOT_MAX_FILES", "3")
	t.Setenv("GOKIN_WORKSPACE_SNAPSHOT_MAX_BYTES", "4096")
	if got := DefaultLimits(); got.MaxFiles != 3 || got.MaxBytes != 4096 {
		t.Fatal(got)
	}
}

func TestSnapshotReusesUnchangedDirtyFingerprint(t *testing.T) {
	root := gitFixture(t)
	writeFile(t, root, "value.go", "dirty\n")
	before := Capture(t.Context(), root, DefaultLimits(), nil)
	after := Capture(t.Context(), root, DefaultLimits(), before)
	if before.Partial || after.Partial || before.files["value.go"].info == nil || before.files["value.go"].info != after.files["value.go"].info {
		t.Fatal("unchanged dirty fingerprint was not reused between commands")
	}
}
