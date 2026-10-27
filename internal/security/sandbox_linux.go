//go:build linux

package security

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

func wrapBashSandbox(cmd *exec.Cmd, launcher string, mounts []sandboxMount, secrets []string, runtimeDir string, allowNetwork bool) error {
	args := []string{"--die-with-parent", "--unshare-all", "--cap-drop", "ALL", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp"}
	if allowNetwork {
		args = append(args, "--share-net")
	} else {
		filter, err := openBashSocketFilter(runtimeDir)
		if err != nil {
			return err
		}
		fd := 3 + len(cmd.ExtraFiles)
		cmd.ExtraFiles = append(cmd.ExtraFiles, filter)
		args = append(args, "--seccomp", strconv.Itoa(fd))
	}
	for _, path := range sandboxSystemPaths() {
		if _, err := os.Stat(path); err == nil {
			args = append(args, "--ro-bind", path, path)
		}
	}
	for _, mount := range mounts {
		if !mount.writable {
			if _, err := os.Lstat(mount.path); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return err
			}
		}
		flag := "--ro-bind"
		if mount.writable {
			flag = "--bind"
		}
		args = append(args, flag, mount.path, mount.path)
	}
	for _, path := range secrets {
		if !sandboxPathMounted(path, mounts) {
			continue
		}
		// A bind over a symlink's target does not protect the symlink itself.
		// Reject writable ancestors that could rebind a secret to a new path.
		for _, mount := range mounts {
			if !mount.writable || !pathWithin(path, mount.path) {
				continue
			}
			for cursor := path; cursor != mount.path; cursor = filepath.Dir(cursor) {
				if info, err := os.Lstat(cursor); err == nil && info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("%w: secret path %s has a rebindable symlink %s; grant a narrower directory", errUnsafeSandboxScope, path, cursor)
				}
			}
		}
		info, err := os.Stat(path)
		if err != nil {
			for _, mount := range mounts {
				if mount.writable && pathWithin(path, mount.path) {
					return fmt.Errorf("%w: cannot safely mask missing secret %s inside writable grant %s; grant a narrower directory", errUnsafeSandboxScope, path, mount.path)
				}
			}
			if !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if info.IsDir() {
			args = append(args, "--tmpfs", path, "--remount-ro", path)
		} else {
			args = append(args, "--ro-bind", "/dev/null", path)
		}
	}
	args = append(args, "--remount-ro", "/", "--remount-ro", "/proc", "--remount-ro", "/dev", "--chdir", cmd.Dir, "--")
	args = append(args, cmd.Path)
	args = append(args, cmd.Args[1:]...)
	cmd.Path = launcher
	cmd.Args = append([]string{launcher}, args...)
	return nil
}
