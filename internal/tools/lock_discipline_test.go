package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Three defects in one sweep had the same shape: a field with a mutex, every
// access taking it, and one accessor that did not. SetAllowedDirs swapped a
// validator seventeen tools shared; SetDiffEnabled wrote a flag the permission
// path pushes into running tools; SetSelfReviewThreshold rebuilt a
// mutex-guarded map holding nothing, which is a concurrent map write and kills
// the process rather than being reported.
//
// A setter is the access most likely to skip the lock — it reads as
// configuration rather than as state, and the discipline visible everywhere
// else makes the one gap look deliberate. This is that rule as a check.
//
// Scope, stated rather than implied: it flags an accessor that assigns a field
// which the SAME FILE guards under a lock elsewhere. It cannot see
// cross-file guarding, callees of *Locked helpers, or per-instance structs
// that merely share a field name — which is why it is limited to the accessor
// family, where all three real findings lived, instead of every assignment.
func TestAccessorsDoNotSkipTheirFieldsLock(t *testing.T) {
	root := filepath.Join("..", "..")
	packages := []string{
		"internal/tools", "internal/agent", "internal/app", "internal/client",
		"internal/chat", "internal/plan", "internal/loops", "internal/mcp",
	}

	funcRe := regexp.MustCompile(`(?ms)^func (?:\([^)]*\) )?(\w+)\([^\n]*\{\n(.*?)\n\}`)
	lockRe := regexp.MustCompile(`\.(Lock|RLock)\(\)`)
	assignRe := regexp.MustCompile(`\b[a-z]\w*\.(\w+)\s*=[^=]`)
	accessorRe := regexp.MustCompile(`^(Set|Reset|Clear|Update|Enable|Disable)`)

	var findings []string
	scanned := 0
	for _, pkg := range packages {
		entries, err := os.ReadDir(filepath.Join(root, pkg))
		if err != nil {
			t.Fatalf("read %s: %v", pkg, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(root, pkg, name))
			if err != nil {
				t.Fatal(err)
			}
			scanned++

			guarded := map[string]string{}
			unguarded := map[string]string{}
			for _, fn := range funcRe.FindAllStringSubmatch(string(data), -1) {
				fname, body := fn[1], fn[2]
				holdsLock := lockRe.MatchString(body)
				for _, as := range assignRe.FindAllStringSubmatch(body, -1) {
					field := as[1]
					switch {
					case holdsLock:
						guarded[field] = fname
					// A *Locked helper documents that its caller holds the lock.
					case strings.HasSuffix(fname, "Locked") || strings.HasPrefix(fname, "New"):
					case accessorRe.MatchString(fname):
						unguarded[field] = fname
					}
				}
			}
			for field, accessor := range unguarded {
				if holder, ok := guarded[field]; ok {
					findings = append(findings, pkg+"/"+name+": "+accessor+
						" assigns "+field+" without a lock, while "+holder+" guards it")
				}
			}
		}
	}

	if scanned < 100 {
		t.Fatalf("scanned only %d files; the guard has stopped looking at the packages it guards", scanned)
	}
	sort.Strings(findings)
	if len(findings) > 0 {
		t.Fatalf("accessors skipping their field's lock:\n  %s", strings.Join(findings, "\n  "))
	}
}
