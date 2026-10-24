package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gokin/internal/git"
	"gokin/internal/tools"
)

var changeRequest = regexp.MustCompile(`(?i)\b(add|edit|change|update|modify|create|remove|delete|insert|implement|fix|replace|rename|write)\b|добавь|добавьте|измени|измените|обнови|исправь|создай|удали|напиши|реализуй`)
var priorWorkRequest = regexp.MustCompile(`(?i)\b(summarize|summary|review|explain|describe)\b|что ты изменил|что вы изменили|расскажи|объясни|подведи итог`)

func requestedFileChange(prompt string) bool {
	return !priorWorkRequest.MatchString(prompt) && changeRequest.MatchString(prompt)
}

// Only dirty paths need content fingerprints: a path absent from the start
// snapshot was clean. Unknown/failed probes suppress this advisory heuristic.
type claimWorkspaceSnapshot struct {
	known bool
	repo  bool
	dirty map[string]string
	head  string
}

func snapshotClaimWorkspace(parent context.Context, root string) *claimWorkspaceSnapshot {
	s := &claimWorkspaceSnapshot{dirty: map[string]string{}}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	repo, err := git.CheckGitRepo(ctx, root)
	if err != nil {
		return s
	}
	s.repo = repo
	if !repo {
		s.known = true
		return s
	}
	head := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	head.Dir = root
	head.WaitDelay = 100 * time.Millisecond
	tools.KillProcessGroupOnCancel(head)
	output, err := head.Output()
	if err != nil {
		return s
	}
	s.head = string(output)
	for _, args := range [][]string{
		{"diff", "--no-ext-diff", "--name-only", "--relative", "-z", "HEAD", "--", "."},
		{"ls-files", "--others", "--exclude-standard", "-z", "--", "."},
	} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		cmd.WaitDelay = 100 * time.Millisecond
		tools.KillProcessGroupOnCancel(cmd)
		out, err := cmd.Output()
		if err != nil {
			return s
		}
		for _, path := range strings.Split(string(out), "\x00") {
			if path == "" {
				continue
			}
			full := filepath.Join(root, path)
			info, err := os.Lstat(full)
			if os.IsNotExist(err) {
				s.dirty[path] = "deleted"
				continue
			}
			if err != nil {
				return s
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(full)
				if err != nil {
					return s
				}
				s.dirty[path] = "symlink:" + target
				continue
			}
			// Submodules and huge/non-regular files cannot provide cheap,
			// complete evidence. Prefer no warning over a guessed mismatch.
			if !info.Mode().IsRegular() || info.Size() > 8<<20 {
				return s
			}
			file, err := os.Open(full)
			if err != nil {
				return s
			}
			hash := sha256.New()
			_, readErr := io.Copy(hash, io.LimitReader(file, (8<<20)+1))
			closeErr := file.Close()
			if readErr != nil || closeErr != nil {
				return s
			}
			s.dirty[path] = fmt.Sprintf("%v:%x", info.Mode(), hash.Sum(nil))
		}
	}
	s.known = true
	return s
}

func (s *claimWorkspaceSnapshot) unchanged(root string, paths []string, shellWrite bool) bool {
	if !s.known {
		return false
	}
	if !s.repo {
		return true
	}
	current := snapshotClaimWorkspace(context.Background(), root)
	if !current.known || !current.repo || current.head != s.head {
		return false
	}
	if shellWrite || len(paths) == 1 && paths[0] == "the requested files" {
		if len(s.dirty) != len(current.dirty) {
			return false
		}
		for path, before := range s.dirty {
			if current.dirty[path] != before {
				return false
			}
		}
		return true
	}
	for _, path := range paths {
		rel, err := filepath.Rel(root, filepath.Join(root, path))
		if filepath.IsAbs(path) {
			rel, err = filepath.Rel(root, path)
		}
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
		if s.dirty[rel] != current.dirty[rel] {
			return false
		}
	}
	return true
}
