package commands

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	doctorSourceVersionPattern = regexp.MustCompile(`(?m)^\s*version\s*=\s*"([^"]+)"`)
	doctorVersionCorePattern   = regexp.MustCompile(`(?i)v?(\d+)\.(\d+)\.(\d+)`)
)

// checkoutSourceVersion recognizes a Gokin source checkout without invoking
// git (doctor must still work when git is missing) and reads the dev/release
// fallback used by a plain `go build ./cmd/gokin`. Unrelated repositories are
// ignored, so normal users do not see developer-only installation warnings.
type doctorCheckoutInfo struct {
	Root    string
	Version string
}

func findDoctorCheckout(workDir string) (doctorCheckoutInfo, bool) {
	dir, err := filepath.Abs(workDir)
	if err != nil {
		dir = filepath.Clean(workDir)
	}
	for {
		moduleData, readErr := os.ReadFile(filepath.Join(dir, "go.mod"))
		if readErr == nil && hasGokinModuleDirective(string(moduleData)) {
			mainData, mainErr := os.ReadFile(filepath.Join(dir, "cmd", "gokin", "main.go"))
			if mainErr != nil {
				return doctorCheckoutInfo{}, false
			}
			match := doctorSourceVersionPattern.FindSubmatch(mainData)
			if len(match) != 2 || strings.TrimSpace(string(match[1])) == "" {
				return doctorCheckoutInfo{}, false
			}
			return doctorCheckoutInfo{Root: dir, Version: strings.TrimSpace(string(match[1]))}, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return doctorCheckoutInfo{}, false
		}
		dir = parent
	}
}

func checkoutSourceVersion(workDir string) (string, bool) {
	info, ok := findDoctorCheckout(workDir)
	return info.Version, ok
}

// checkoutBuildInputNewerThan reports the newest production build input that
// post-dates the active executable. Semver alone cannot distinguish two dirty
// builds with the same fallback version. Go sources (excluding tests), module
// manifests, and the embedded REPL Python worker are production build inputs.
// A one-second tolerance avoids false positives on coarse timestamp filesystems.
func checkoutBuildInputNewerThan(root, executablePath string) (string, bool) {
	executable, err := os.Stat(executablePath)
	if err != nil {
		return "", false
	}
	cutoff := executable.ModTime().Add(time.Second)
	var newestPath string
	var newestTime time.Time
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".gokin", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		buildInput := name == "go.mod" || name == "go.sum" ||
			(strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")) ||
			(strings.HasSuffix(name, ".py") && strings.Contains(filepath.ToSlash(path), "/internal/repl/"))
		if !buildInput {
			return nil
		}
		fileInfo, infoErr := entry.Info()
		if infoErr != nil || !fileInfo.Mode().IsRegular() {
			return nil
		}
		if fileInfo.ModTime().After(cutoff) && fileInfo.ModTime().After(newestTime) {
			newestTime = fileInfo.ModTime()
			newestPath = path
		}
		return nil
	})
	if newestPath == "" {
		return "", false
	}
	relative, err := filepath.Rel(root, newestPath)
	if err != nil {
		relative = newestPath
	}
	return filepath.ToSlash(relative), true
}

func hasGokinModuleDirective(goMod string) bool {
	for line := range strings.SplitSeq(goMod, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		return line == "module gokin"
	}
	return false
}

// compareDoctorVersionCore compares only major/minor/patch. Release suffixes
// and `git describe` metadata do not make an otherwise-current local build
// look stale; an unparseable development label is reported but not warned.
func compareDoctorVersionCore(runtimeVersion, sourceVersion string) (int, bool) {
	left, ok := doctorVersionCore(runtimeVersion)
	if !ok {
		return 0, false
	}
	right, ok := doctorVersionCore(sourceVersion)
	if !ok {
		return 0, false
	}
	for i := range left {
		if left[i] < right[i] {
			return -1, true
		}
		if left[i] > right[i] {
			return 1, true
		}
	}
	return 0, true
}

func doctorVersionCore(value string) ([3]int, bool) {
	match := doctorVersionCorePattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 4 {
		return [3]int{}, false
	}
	var result [3]int
	for i := range result {
		n, err := strconv.Atoi(match[i+1])
		if err != nil {
			return [3]int{}, false
		}
		result[i] = n
	}
	return result, true
}
