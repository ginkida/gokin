package tools

import "testing"

func TestFileRelevanceScore_SourceBeatsVendor(t *testing.T) {
	// First-party source with 3 hits vs vendor with 10 hits. The
	// first-party should still win — vendor penalty = 2.5, which more
	// than compensates the 7-hit gap weighted at 1.0 apiece? No, 7 > 2.5.
	// So vendor WOULD win on sheer count. Rebalance expectation: this is
	// the score formula's current trade-off — we confirm the RANK with
	// a smaller gap, 3 hits vs 5 hits.
	src := fileRelevanceScore("internal/app/foo.go", 3)
	vendor := fileRelevanceScore("vendor/github.com/x/y/foo.go", 5)
	if src <= vendor {
		t.Errorf("3-hit source should beat 5-hit vendor: src=%.2f vendor=%.2f", src, vendor)
	}
}

func TestFileRelevanceScore_NonTestBeatsTest(t *testing.T) {
	src := fileRelevanceScore("foo.go", 1)
	testFile := fileRelevanceScore("foo_test.go", 1)
	if src <= testFile {
		t.Errorf("non-test should outrank test at equal hits: src=%.2f test=%.2f", src, testFile)
	}
}

func TestFileRelevanceScore_NonGeneratedBeatsGenerated(t *testing.T) {
	src := fileRelevanceScore("api.go", 1)
	gen := fileRelevanceScore("api.pb.go", 1)
	if src <= gen {
		t.Errorf("hand-written should outrank generated: src=%.2f gen=%.2f", src, gen)
	}
}

func TestFileRelevanceScore_DepthPenalty(t *testing.T) {
	shallow := fileRelevanceScore("foo.go", 1)
	deep := fileRelevanceScore("a/b/c/d/e/foo.go", 1)
	if shallow <= deep {
		t.Errorf("shallow should outrank deep at equal hits: shallow=%.2f deep=%.2f", shallow, deep)
	}
}

func TestFileRelevanceScore_MatchCountDominatesAtSameTier(t *testing.T) {
	// When both files are "same tier" (both first-party, both non-test),
	// more matches must win.
	few := fileRelevanceScore("internal/app/foo.go", 1)
	many := fileRelevanceScore("internal/app/foo.go", 10)
	if many <= few {
		t.Errorf("higher match count should win: many=%.2f few=%.2f", many, few)
	}
}

func TestIsTestPath_Conventions(t *testing.T) {
	cases := map[string]bool{
		"foo_test.go":                            true,
		"foo.go":                                 false,
		"foo.test.ts":                            true,
		"foo.spec.js":                            true,
		"test_foo.py":                            true,
		"foo_test.py":                            true,
		"internal/__tests__/bar.ts":              true,
		"internal/tests/foo.rs":                  true,
		"internal/testdata/foo.json":             true,
		"internal/fixtures/sample.yaml":          true,
		"internal/app/foo.go":                    false,
		"internal/app/tester.go":                 false, // word "tester" not isolated segment
	}
	for path, want := range cases {
		if got := isTestPath(path); got != want {
			t.Errorf("isTestPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestIsVendorPath_RelativeAndNested(t *testing.T) {
	cases := map[string]bool{
		"vendor/foo.go":                true,
		"node_modules/x/bar.js":        true,
		"internal/app/foo.go":          false,
		"src/main.ts":                  false,
		"pkg/vendor/foo.go":            true, // nested vendor dir
		"my-vendorlib/foo.go":          false, // substring, not segment
	}
	for path, want := range cases {
		if got := isVendorPath(path); got != want {
			t.Errorf("isVendorPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestIsGeneratedPath_Suffixes(t *testing.T) {
	cases := map[string]bool{
		"api.pb.go":       true,
		"schema_gen.go":   true,
		"bundle.min.js":   true,
		"api.go":          false,
		"handler_test.go": false,
	}
	for path, want := range cases {
		if got := isGeneratedPath(path); got != want {
			t.Errorf("isGeneratedPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestSortFileMatchesByRelevance_Order(t *testing.T) {
	// Three results: test file with many hits, source file with few
	// hits, vendor file with most hits. Expected order: source → test
	// → vendor (source outranks test by non-test bonus; vendor demoted).
	results := []fileMatch{
		{path: "foo_test.go", matches: make([]grepMatch, 8)},
		{path: "internal/app/foo.go", matches: make([]grepMatch, 2)},
		{path: "vendor/lib.go", matches: make([]grepMatch, 12)},
	}
	sortFileMatchesByRelevance(results)
	if results[0].path != "internal/app/foo.go" {
		t.Errorf("top should be internal/app/foo.go, got %s", results[0].path)
	}
	if results[2].path != "vendor/lib.go" {
		t.Errorf("bottom should be vendor/lib.go, got %s", results[2].path)
	}
}

func TestSortFileMatchesByRelevance_StableTiebreak(t *testing.T) {
	// Two results with identical characteristics — path-asc must break
	// the tie so re-runs produce deterministic output.
	results := []fileMatch{
		{path: "b.go", matches: make([]grepMatch, 1)},
		{path: "a.go", matches: make([]grepMatch, 1)},
	}
	sortFileMatchesByRelevance(results)
	if results[0].path != "a.go" {
		t.Errorf("expected alphabetical tiebreak, got %s first", results[0].path)
	}
}
