//go:build darwin

package security

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func bashSandboxProfile(mounts []sandboxMount, secrets []string, allowNetwork bool) string {
	var profile strings.Builder
	profile.WriteString("(version 1)\n(deny default)\n(allow process*)\n(allow signal (target same-sandbox))\n(allow sysctl-read)\n(allow mach-lookup)\n(allow file-read-metadata)\n(allow file-read* (literal \"/\"))\n")
	for _, path := range sandboxSystemPaths() {
		fmt.Fprintf(&profile, "(allow file-read* (subpath %s))\n", strconv.Quote(path))
	}
	for _, mount := range mounts {
		fmt.Fprintf(&profile, "(allow file-read* (subpath %s))\n", strconv.Quote(mount.path))
		if mount.writable {
			fmt.Fprintf(&profile, "(allow file-write* (subpath %s))\n", strconv.Quote(mount.path))
			fmt.Fprintf(&profile, "(deny file-write-unlink (literal %s))\n", strconv.Quote(mount.path))
		} else {
			fmt.Fprintf(&profile, "(deny file-write* (subpath %s))\n", strconv.Quote(mount.path))
		}
	}
	profile.WriteString("(allow file-read* (literal \"/dev/null\") (literal \"/dev/random\") (literal \"/dev/urandom\"))\n(allow file-write* (literal \"/dev/null\"))\n")
	for _, path := range secrets {
		fmt.Fprintf(&profile, "(deny file-read* file-write* (subpath %s))\n", strconv.Quote(path))
	}
	if allowNetwork {
		profile.WriteString("(allow network*)\n")
	} else {
		profile.WriteString("(deny network*)\n(deny network-outbound (remote unix-socket))\n(allow network-outbound (remote ip \"localhost:*\"))\n(allow network-inbound (local ip \"localhost:*\"))\n(allow network-bind (local ip \"localhost:*\"))\n")
	}
	return profile.String()
}

func wrapBashSandbox(cmd *exec.Cmd, launcher string, mounts []sandboxMount, secrets []string, runtimeDir string, allowNetwork bool) error {
	profilePath := filepath.Join(runtimeDir, "profile.sb")
	if err := os.WriteFile(profilePath, []byte(bashSandboxProfile(mounts, secrets, allowNetwork)), 0600); err != nil {
		return err
	}
	args := append([]string{launcher, "-f", profilePath, cmd.Path}, cmd.Args[1:]...)
	cmd.Path = launcher
	cmd.Args = args
	return nil
}
