package security

import (
	"fmt"
	"os"
	"path/filepath"
)

// Keep repository state writable, but bind execution-bearing metadata last.
// A self-bind also anchors .git against renames on Linux.
func sandboxGitMounts(root string) (data, protected []sandboxMount, resultErr error) {
	root, err := canonicalSandboxPath(root)
	if err != nil {
		return nil, nil, err
	}
	entry := filepath.Join(root, ".git")
	info, err := os.Lstat(entry)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("%w: Git metadata symlink %s cannot become a sandbox grant", errUnsafeSandboxScope, entry)
	}
	var dirs []string
	if info.IsDir() {
		dir, err := canonicalSandboxPath(entry)
		if err != nil {
			return nil, nil, err
		}
		data = append(data, sandboxMount{dir, true})
		dirs = append(dirs, dir)
	} else {
		paths := worktreeMetadata(root)
		if len(paths) == 0 {
			return nil, []sandboxMount{{entry, false}}, nil
		}
		for i, path := range paths {
			name := filepath.Base(path)
			data = append(data, sandboxMount{path, i == 0 || name == "objects" || name == "refs" || name == "logs"})
		}
		dirs = append(dirs, paths[0], filepath.Dir(filepath.Dir(paths[0])))
		protected = append(protected, sandboxMount{entry, false})
	}
	for _, dir := range dirs {
		for _, name := range []string{"config", "config.worktree", "hooks", "info", "gitdir", "commondir"} {
			path := filepath.Join(dir, name)
			if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
				return nil, nil, fmt.Errorf("%w: execution-bearing Git metadata must not be a symlink: %s", errUnsafeSandboxScope, path)
			}
			if resolved, err := canonicalSandboxPath(path); err == nil {
				path = resolved
			}
			protected = append(protected, sandboxMount{path, false})
		}
	}
	return data, protected, nil
}
