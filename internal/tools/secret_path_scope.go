package tools

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"gokin/internal/security"
)

// secretPathScoped exposes the stable validator without swapping it while
// parallel tools run. Kept alongside the allowedDirsSetter propagation path.
type secretPathScoped interface {
	secretPathValidator() *security.PathValidator
}

func (t *BatchTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *CopyTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *DeleteTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *DiffTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *EditTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *FindReferencesTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *GitBlameTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *GitDiffTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *GitLogTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *GitStatusTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *GlobTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *GoSearchTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *GoToDefinitionTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *GrepTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *LSPTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *ListDirTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *MkdirTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *MoveTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *ReadTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *RefactorTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *ReviewChangesTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *RunTestsTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *TreeTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *VerifyCodeTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

func (t *WriteTool) secretPathValidator() *security.PathValidator { return t.pathValidator }

// CheckImpactTool owns the same search engine as grep, including its deny set.
func (t *CheckImpactTool) secretPathValidator() *security.PathValidator {
	return t.engine.pathValidator
}

// walkScopedFiles prunes credential subtrees before enumerating them. WalkDir
// never descends into symlinked directories; a symlinked FILE is reported when
// its target is a regular file outside every credential location (the glob
// engine this replaced returned such files too). Glob matching is relative to
// the validated root so a pattern cannot introduce an unvalidated absolute or
// parent directory.
//
// The walk starts at the pattern's static prefix ("src/**/*.go" walks src/) and
// a pattern without "**" never descends deeper than it has segments, so a
// root-level "*.go" reads one directory instead of the whole tree.
func walkScopedFiles(root, pattern string, validator *security.PathValidator, visit func(string, fs.DirEntry) error) error {
	slashPattern := filepath.ToSlash(pattern)
	if !doublestar.ValidatePattern(slashPattern) {
		return doublestar.ErrBadPattern
	}
	walkRoot := root
	if base, _ := doublestar.SplitPattern(slashPattern); base != "" && base != "." {
		walkRoot = filepath.Join(root, filepath.FromSlash(base))
		if rel, err := filepath.Rel(root, walkRoot); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
	}
	recursive := strings.Contains(slashPattern, "**")
	segments := strings.Count(strings.Trim(slashPattern, "/"), "/") + 1
	return filepath.WalkDir(walkRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if validator.IsSecretPath(path) {
				return filepath.SkipDir
			}
			if !recursive && path != walkRoot {
				if rel, relErr := filepath.Rel(root, path); relErr == nil && strings.Count(filepath.ToSlash(rel), "/")+1 >= segments {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(path)
			if err != nil || validator.IsSecretPath(target) {
				return nil
			}
			if info, err := os.Stat(target); err != nil || !info.Mode().IsRegular() {
				return nil
			}
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		matched, err := doublestar.Match(slashPattern, filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		if matched {
			return visit(path, entry)
		}
		return nil
	})
}

// Cache-only metadata never enters model arguments or tool execution. Capture
// once before dispatch and reuse it for publication, so a policy change during
// a read cannot put old authority into the new policy's cache namespace.
func secretPathCacheArgs(tool Tool, args map[string]any) map[string]any {
	scoped, ok := tool.(secretPathScoped)
	if !ok || scoped.secretPathValidator() == nil {
		return args
	}
	revision := scoped.secretPathValidator().SecretPathRevision()
	if revision == 0 {
		return args
	}
	cloned := maps.Clone(args)
	if cloned == nil {
		cloned = make(map[string]any)
	}
	cloned["_gokin_secret_path_policy_revision"] = revision
	return cloned
}
