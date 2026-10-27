// Package workspace observes bounded, on-disk workspace changes. It does not
// capture file contents for undo or infer writes from shell command text.
package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"gokin/internal/gitexec"
)

const maxFileBytes = 8 << 20

type Limits struct {
	MaxFiles int
	MaxBytes int64
}

// DefaultLimits can be lowered for large workspaces without changing persisted
// config. Invalid/nonpositive overrides retain the bounded defaults.
func DefaultLimits() Limits {
	l := Limits{MaxFiles: 10000, MaxBytes: 64 << 20}
	if n, err := strconv.Atoi(os.Getenv("GOKIN_WORKSPACE_SNAPSHOT_MAX_FILES")); err == nil && n > 0 {
		l.MaxFiles = n
	}
	if n, err := strconv.ParseInt(os.Getenv("GOKIN_WORKSPACE_SNAPSHOT_MAX_BYTES"), 10, 64); err == nil && n > 0 {
		l.MaxBytes = n
	}
	return l
}

type fingerprint struct {
	value string
	info  fs.FileInfo
}

type Snapshot struct {
	Root         string
	Repo         bool
	Head         string
	Partial      bool
	Warning      string
	files        map[string]fingerprint
	repoDir      string
	gitMarker    string
	gitInfo      fs.FileInfo
	indexPath    string
	indexInfo    fs.FileInfo
	completeWalk bool
	dirty        map[string]bool
	introduced   map[string]bool
	limits       Limits
}

type Delta struct {
	Paths   []string
	Partial bool
	Warning string
}

func (s *Snapshot) incomplete(reason string) {
	s.Partial, s.Warning = true, reason
}

// Capture keeps a bounded worktree baseline, including existing ignored files:
// changing ignore rules must not invent creations or deletions. Warm Git captures
// run one status probe and refresh only paths dirty in either capture. HEAD or
// index changes refresh the whole baseline, including clean checkout conversions.
func Capture(parent context.Context, root string, limits Limits, previous *Snapshot) *Snapshot {
	return capture(parent, root, limits, previous, false)
}

// CaptureFresh refreshes the entire worktree baseline after a turn boundary or
// another writer invalidates it, while retaining repository discovery and hashes.
func CaptureFresh(parent context.Context, root string, limits Limits, previous *Snapshot) *Snapshot {
	return capture(parent, root, limits, previous, true)
}

func capture(parent context.Context, root string, limits Limits, previous *Snapshot, fresh bool) *Snapshot {
	if root != "" {
		if absolute, err := filepath.Abs(root); err == nil {
			root = absolute
		}
	}
	s := &Snapshot{Root: root, files: make(map[string]fingerprint), dirty: make(map[string]bool), introduced: make(map[string]bool), limits: limits}
	if root == "" || limits.MaxFiles <= 0 || limits.MaxBytes <= 0 {
		s.incomplete("workspace snapshot limits or root unavailable")
		return s
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	if previous != nil && (previous.Root != root || previous.Partial || previous.limits != limits) {
		previous = nil
	}
	// Repository discovery is filesystem-only after the first capture. Worktree
	// .git files are markers too; removing/replacing one invalidates the cache.
	if previous != nil && previous.Repo {
		info, err := os.Lstat(previous.gitMarker)
		if err == nil && os.SameFile(info, previous.gitInfo) && (info.IsDir() || info.ModTime() == previous.gitInfo.ModTime() && info.Size() == previous.gitInfo.Size()) {
			s.Repo, s.repoDir, s.gitMarker, s.gitInfo = true, previous.repoDir, previous.gitMarker, info
			s.indexPath = previous.indexPath
		} else {
			previous = nil
		}
	}
	if !s.Repo {
		for dir := root; ; dir = filepath.Dir(dir) {
			marker := filepath.Join(dir, ".git")
			if info, err := os.Lstat(marker); err == nil {
				out, err := gitOutput(ctx, root, 64<<10, "rev-parse", "--show-toplevel")
				if err != nil {
					s.incomplete("workspace Git root unavailable")
					return s
				}
				s.Repo, s.repoDir, s.gitMarker, s.gitInfo = true, strings.TrimSuffix(string(out), "\n"), marker, info
				break
			} else if !os.IsNotExist(err) {
				s.incomplete("workspace Git discovery failed")
				return s
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	if previous != nil && previous.Repo != s.Repo {
		previous = nil
	}
	if s.Repo {
		if s.indexPath == "" {
			out, err := gitOutput(ctx, root, 64<<10, "rev-parse", "--git-path", "index")
			if err != nil {
				s.incomplete("workspace Git index path unavailable")
				return s
			}
			s.indexPath = strings.TrimSuffix(string(out), "\n")
			if !filepath.IsAbs(s.indexPath) {
				s.indexPath = filepath.Join(root, s.indexPath)
			}
		}
		out, err := gitOutput(ctx, root, 4<<20, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all", "--", ".")
		if err != nil {
			s.incomplete("workspace Git status failed or exceeded output/time limit")
			return s
		}
		if err := s.parseStatus(out); err != nil {
			s.incomplete(err.Error())
			return s
		}
		if s.Head == "" {
			s.incomplete("workspace Git status omitted HEAD")
			return s
		}
		// Index updates may rewrite clean worktree representations (CRLF) or
		// make them dirty later without changing their bytes. Keep their baseline
		// current; metadata alone avoids extra Git launches on warm captures.
		info, err := os.Stat(s.indexPath)
		if err != nil && !os.IsNotExist(err) {
			s.incomplete("workspace Git index unreadable")
			return s
		}
		s.indexInfo = info
		if len(s.dirty) > limits.MaxFiles {
			s.incomplete("workspace snapshot file cap exceeded")
			return s
		}
	}
	var paths []string
	var err error
	if !fresh && s.Repo && previous != nil && previous.Head == s.Head && sameFileStamp(previous.indexInfo, s.indexInfo) {
		for path, f := range previous.files {
			s.files[path] = f
		}
		refresh := make(map[string]bool)
		for path := range previous.dirty {
			refresh[path] = true
		}
		for path := range s.dirty {
			refresh[path] = true
		}
		for path := range refresh {
			paths = append(paths, path)
		}
	} else {
		paths, err = walkFiles(ctx, root, limits.MaxFiles)
		if err != nil {
			s.incomplete("workspace walk failed or exceeded file/time cap")
			return s
		}
		s.completeWalk = true
		// Deletions are lstat-verified, never inferred from Git membership.
		seen := make(map[string]bool, len(paths))
		for _, path := range paths {
			seen[path] = true
		}
		if previous != nil {
			for path := range previous.files {
				if !seen[path] {
					paths = append(paths, path)
				}
			}
		}
		for path := range s.dirty {
			if !seen[path] && (previous == nil || previous.files[path].value == "") {
				paths = append(paths, path)
			}
		}
	}
	reader, err := os.OpenRoot(root)
	if err != nil {
		s.incomplete("workspace root unreadable")
		return s
	}
	defer reader.Close()
	var total int64
	for _, f := range s.files {
		if f.info != nil {
			total += f.info.Size()
		}
	}
	for _, path := range paths {
		if ctx.Err() != nil {
			s.incomplete("workspace snapshot time cap exceeded")
			break
		}
		var cached fingerprint
		if previous != nil {
			cached = previous.files[path]
		}
		f, _, err := readFingerprint(reader, path, cached)
		if err != nil {
			s.incomplete("workspace fingerprint failed or exceeded size cap")
			break
		}
		if old := s.files[path]; old.info != nil {
			total -= old.info.Size()
		}
		if f.info != nil {
			total += f.info.Size()
		}
		s.files[path] = f
		if total > limits.MaxBytes {
			s.incomplete("workspace snapshot size cap exceeded")
			break
		}
	}
	if len(s.files) > limits.MaxFiles || total > limits.MaxBytes {
		s.incomplete("workspace snapshot file or size cap exceeded")
	}
	return s
}

func sameFileStamp(a, b fs.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime() == b.ModTime()
}

func (s *Snapshot) parseStatus(out []byte) error {
	entries := bytes.Split(out, []byte{0})
	for i := 0; i < len(entries); i++ {
		entry := string(entries[i])
		if entry == "" {
			continue
		}
		if strings.HasPrefix(entry, "# ") {
			if strings.HasPrefix(entry, "# branch.oid ") {
				s.Head = strings.TrimPrefix(entry, "# branch.oid ")
			}
			continue
		}
		fields := 0
		switch entry[0] {
		case '1':
			fields = 8
		case '2':
			fields = 9
		case 'u':
			fields = 10
		case '?', '!':
			fields = 1
		default:
			return errors.New("workspace Git status was malformed")
		}
		parts := strings.SplitN(entry, " ", fields+1)
		if len(parts) != fields+1 {
			return errors.New("workspace Git status was malformed")
		}
		path := s.addRepoPath(parts[fields])
		if path != "" && fields > 1 && strings.ContainsAny(parts[1], "ARC") {
			s.introduced[path] = true
		}
		if entry[0] == '2' {
			i++
			if i >= len(entries) || len(entries[i]) == 0 {
				return errors.New("workspace Git rename status was malformed")
			}
			if strings.Contains(parts[1], "R") {
				s.addRepoPath(string(entries[i]))
			}
		}
	}
	return nil
}

// Exclude only Gokin-owned runtime IO, in both Git and filesystem enumerations.
// Sandbox toolchain caches are provisioned outside the workspace by security.
func excludedPath(path string) bool {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	for i, part := range parts {
		if part == ".git" || part == ".gokin-tmp" {
			return true
		}
		if part == ".gokin" && i+1 < len(parts) {
			switch parts[i+1] {
			case "task-output", "agent-output", "cache", "execution_journal.jsonl":
				return true
			}
		}
	}
	return false
}

func walkFiles(ctx context.Context, root string, maxEntries int) ([]string, error) {
	reader, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	var paths []string
	entries := 0
	var walk func(string) error
	walk = func(dir string) error {
		file, err := reader.Open(dir)
		if err != nil {
			return err
		}
		defer file.Close()
		for {
			batch, readErr := file.ReadDir(128)
			for _, d := range batch {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				path := filepath.Join(dir, d.Name())
				// Task transcripts and managed bash scratch are Gokin's own IO.
				if excludedPath(path) {
					continue
				}
				entries++
				if entries > maxEntries {
					return errors.New("file cap")
				}
				if d.IsDir() {
					if err := walk(path); err != nil {
						return err
					}
				} else {
					paths = append(paths, path)
				}
			}
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	err = walk(".")
	sort.Strings(paths)
	return paths, err
}

func (s *Snapshot) addRepoPath(path string) string {
	rel, err := filepath.Rel(s.Root, filepath.Join(s.repoDir, filepath.FromSlash(path)))
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "." && !excludedPath(rel) {
		s.dirty[rel] = true
		return rel
	}
	return ""
}

func readFingerprint(root *os.Root, path string, cached fingerprint) (fingerprint, int64, error) {
	info, err := root.Lstat(path)
	if os.IsNotExist(err) {
		return fingerprint{value: "deleted"}, 0, nil
	}
	if err != nil {
		return fingerprint{}, 0, err
	}
	if cached.info != nil && os.SameFile(info, cached.info) && reflect.DeepEqual(info, cached.info) {
		return cached, info.Size(), nil
	}
	mode := "100644"
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := root.Readlink(path)
		return fingerprint{value: fmt.Sprintf("120000:%x", sha256.Sum256([]byte(target))), info: info}, int64(len(target)), err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return fingerprint{}, 0, errors.New("unsupported or oversized file")
	}
	if info.Mode()&0111 != 0 {
		mode = "100755"
	}
	file, err := root.Open(path)
	if err != nil {
		return fingerprint{}, 0, err
	}
	hash := sha256.New()
	n, readErr := io.Copy(hash, io.LimitReader(file, maxFileBytes+1))
	after, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || statErr != nil || n > maxFileBytes || !os.SameFile(info, after) || info.Size() != after.Size() || info.ModTime() != after.ModTime() {
		return fingerprint{}, n, errors.New("file changed or unreadable during snapshot")
	}
	return fingerprint{value: fmt.Sprintf("%s:%x", mode, hash.Sum(nil)), info: after}, n, nil
}

// Diff compares stored worktree bytes, never repository blobs. Clean paths
// reuse the worktree baseline, so staging, commits and ignore rules alone do not
// manufacture writes (including under CRLF conversion).
func Diff(_ context.Context, before, after *Snapshot) Delta {
	if before == nil || after == nil || before.Partial || after.Partial || before.Root != after.Root || before.Repo != after.Repo {
		reason := "workspace baseline unavailable"
		if before != nil && before.Partial {
			reason = before.Warning
		} else if after != nil && after.Partial {
			reason = after.Warning
		}
		return Delta{Partial: true, Warning: reason}
	}
	paths := make(map[string]bool)
	if before.Repo && before.Head == after.Head {
		// A clean-to-clean normalization is not a project change. Capture still
		// refreshes its worktree bytes for a later dirty/index-only transition.
		for path := range before.dirty {
			paths[path] = true
		}
		for path := range after.dirty {
			paths[path] = true
		}
	} else {
		for path := range before.files {
			paths[path] = true
		}
		for path := range after.files {
			paths[path] = true
		}
	}
	if len(paths) > before.limits.MaxFiles {
		return Delta{Partial: true, Warning: "workspace delta file cap exceeded"}
	}
	d := Delta{}
	ignoreChanged := false
	for path := range paths {
		if filepath.Base(path) == ".gitignore" && before.files[path].value != after.files[path].value {
			ignoreChanged = true
		}
	}
	for path := range paths {
		b, a := before.files[path].value, after.files[path].value
		unknown := b == "" || b == "deleted" || ignoreChanged && filepath.Base(path) != ".gitignore" && b != a
		if before.Repo && (ignoreChanged || before.Head != after.Head || after.introduced[path]) && !before.completeWalk && !before.dirty[path] && unknown && a != "" && a != "deleted" {
			// Cached ignored bytes or absence may predate an earlier write,
			// exposed now by ignore rules, staging, or a commit.
			d.Partial, d.Warning = true, "workspace baseline unavailable for newly visible paths"
			continue
		}
		if b == "" {
			b = "deleted"
		}
		if a == "" {
			a = "deleted"
		}
		if b != a {
			d.Paths = append(d.Paths, filepath.Join(before.Root, path))
		}
	}
	sort.Strings(d.Paths)
	return d
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("Git output cap exceeded")
	}
	return b.Buffer.Write(p)
}

func gitOutput(ctx context.Context, dir string, limit int, args ...string) ([]byte, error) {
	cmd := gitexec.CommandContext(ctx, args...)
	cmd.Dir = dir
	gitexec.KillProcessGroupOnCancel(cmd)
	cmd.WaitDelay = 100 * time.Millisecond
	out := &boundedBuffer{limit: limit}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	err := cmd.Run()
	return out.Bytes(), err
}
