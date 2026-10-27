package gitexec

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type hookFile struct {
	path string
	data []byte
	mode fs.FileMode
}

// Hooks contains only files present when the owning tool was constructed.
// Later repository writes cannot introduce host-executed hooks.
type Hooks struct {
	files []hookFile
	err   error
}

func CaptureHooks(root string) *Hooks {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var dir string
	for _, scope := range []string{"--local", "--global"} {
		cmd := CommandContext(ctx, "-C", root, "config", scope, "--path", "--get", "core.hooksPath")
		if output, err := cmd.Output(); err == nil {
			dir = strings.TrimSpace(string(output))
			break
		}
	}
	if dir == "" {
		output, err := CommandContext(ctx, "-C", root, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
		if err != nil {
			return nil
		}
		dir = filepath.Join(strings.TrimSpace(string(output)), "hooks")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	if dir == os.DevNull {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return &Hooks{err: fmt.Errorf("resolve existing Git hooks: %w", err)}
	}
	dir = resolved
	var files []hookFile
	active := false
	budget := 4 << 20
	count := 0
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		count++
		if count > 1000 {
			return fs.ErrInvalid
		}
		if entry.IsDir() {
			return nil
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 256<<10 || info.Size() > int64(budget) {
			return fs.ErrInvalid
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		budget -= len(data)
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, hookFile{rel, data, info.Mode().Perm()})
		if filepath.Dir(rel) == "." && !strings.HasSuffix(rel, ".sample") && info.Mode()&0111 != 0 {
			active = true
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return &Hooks{err: fmt.Errorf("capture existing Git hooks: %w", err)}
	}
	if !active {
		return nil
	}
	return &Hooks{files: files}
}

// Activate materializes the immutable snapshot under gokin's credential
// directory, which all bash policies mask, including broad HOME grants.
func (h *Hooks) Activate(ctx context.Context) (context.Context, func(), error) {
	if h == nil {
		return ctx, func() {}, nil
	}
	if h.err != nil {
		return nil, nil, h.err
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, nil, err
	}
	base = filepath.Join(base, "gokin", "trusted-hooks")
	if err := os.MkdirAll(base, 0700); err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp(base, "commit-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	for _, file := range h.files {
		path := filepath.Join(dir, file.path)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			cleanup()
			return nil, nil, err
		}
		if err := os.WriteFile(path, file.data, file.mode); err != nil {
			cleanup()
			return nil, nil, err
		}
	}
	return context.WithValue(ctx, hooksKey{}, dir), cleanup, nil
}
