package evals

import "testing"

// TestShouldSkipCopyDir_HomeCacheDirs pins the fix for build-cache pollution:
// when an agent runs `go test`/`go build` with HOME resolving to the eval
// workspace, Go writes its build cache + telemetry under the workspace. The
// snapshot must skip those HOME-junk dirs so changed_files stays real source
// edits (they otherwise add ~1000 entries per scenario).
func TestShouldSkipCopyDir_HomeCacheDirs(t *testing.T) {
	skip := []string{
		"Library", "library", ".cache", ".config", // Go HOME caches (macOS + Linux)
		".npm", "node-compile-cache", // Node caches
		".git", "node_modules", "vendor", "__pycache__", ".gokin", // existing
	}
	for _, d := range skip {
		if !shouldSkipCopyDir(d) {
			t.Errorf("shouldSkipCopyDir(%q) = false, want true", d)
		}
	}

	// Real source dirs (and near-misses without the leading dot) must NOT be
	// skipped — only the exact junk names.
	keep := []string{"internal", "src", "config", "cache", "lib", "pkg", "cmd"}
	for _, d := range keep {
		if shouldSkipCopyDir(d) {
			t.Errorf("shouldSkipCopyDir(%q) = true, want false (real source dir)", d)
		}
	}
}
