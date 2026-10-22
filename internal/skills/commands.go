package skills

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// commandFiles scans metadata only, under the same os.Root containment and
// entry budget as skill discovery. Never follow a symlink or recurse without
// a bound, including directories containing no usable command documents.
func commandFiles(root *os.Root) ([]string, error) {
	var files []string
	remaining := MaxSkillScanEntries
	var scan func(string, int) error
	scan = func(path string, depth int) error {
		if depth > 16 {
			return fmt.Errorf("command directory nesting exceeds 16")
		}
		dir, err := root.Open(path)
		if err != nil {
			return err
		}
		entries, readErr := dir.ReadDir(remaining + 1)
		dir.Close()
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			remaining--
			if remaining < 0 {
				return fmt.Errorf("command scan exceeds %d entries", MaxSkillScanEntries)
			}
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			path := filepath.Join(path, entry.Name())
			if entry.IsDir() {
				if !validSkillName.MatchString(entry.Name()) {
					continue
				}
				if err := scan(path, depth+1); err != nil {
					return err
				}
			} else if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".md") && validSkillName.MatchString(strings.TrimSuffix(entry.Name(), ".md")) {
				files = append(files, path)
			}
		}
		return nil
	}
	err := scan(".", 0)
	return files, err
}
