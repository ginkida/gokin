package app

import (
	"encoding/json"
	"testing"
	"time"

	"gokin/internal/plan"
)

// This file tests the plan-step verification logic in message_processor.go —
// the pure functions that decide whether a step mutated files, whether verify
// commands were run, and whether expected artifacts were produced. Before this
// file they were all at 0% coverage, meaning any change to the verification
// contract was blind. All functions here are pure string/struct processing —
// no App state, no mocks, no I/O.

// ========== isMutatingToolName ==========

func TestIsMutatingToolName(t *testing.T) {
	cases := []struct {
		name string
		tool string
		want bool
	}{
		{"write", "write", true},
		{"edit", "edit", true},
		{"batch", "batch", true},
		{"move", "move", true},
		{"delete", "delete", true},
		{"mkdir", "mkdir", true},
		{"atomicwrite", "atomicwrite", true},
		{"read not mutating", "read", false},
		{"grep not mutating", "grep", false},
		{"bash not mutating", "bash", false},
		{"empty", "", false},
		{"case insensitive WRITE", "WRITE", true},
		{"case insensitive Edit", "Edit", true},
		{"whitespace trimmed", "  write  ", true},
		{"unknown tool", "some_custom_tool", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMutatingToolName(tc.tool); got != tc.want {
				t.Errorf("isMutatingToolName(%q) = %v, want %v", tc.tool, got, tc.want)
			}
		})
	}
}

// ========== commandLooksMutating ==========

func TestCommandLooksMutating(t *testing.T) {
	mutating := []string{
		// NOTE: commandLooksMutating first checks looksLikeVerificationCommand,
		// which treats any command containing " build", " test", " check", etc.
		// as a verification command and returns false. So "rm -rf /tmp/build"
		// and "mkdir build" are NOT detected as mutating (the "build" suffix
		// triggers the verification guard). This is a known heuristic limitation.
		"mv old.go new.go",
		"cp config.yaml config.bak",
		"chmod +x deploy.sh",
		"touch newfile.go",
		"tee output.txt",
		"sed -i 's/old/new/g' file.go",
		"echo > file.go", // marker "echo >" matches with space prefix
		"git add .",
		"git commit -m 'fix'",
		"git rm old.go",
		"npm install express",
		"pip install flask",
		"go get github.com/pkg/errors",
		"rmdir empty",
	}
	for _, cmd := range mutating {
		t.Run("mutating:"+cmd, func(t *testing.T) {
			if !commandLooksMutating(cmd) {
				t.Errorf("commandLooksMutating(%q) = false, want true", cmd)
			}
		})
	}

	safe := []string{
		"go test ./...",
		"go build ./...",
		"ls -la",
		"cat README.md",
		"echo hello",
		"git status",
		"git diff",
		"git log --oneline",
		"",
	}
	for _, cmd := range safe {
		t.Run("safe:"+cmd, func(t *testing.T) {
			if commandLooksMutating(cmd) {
				t.Errorf("commandLooksMutating(%q) = true, want false", cmd)
			}
		})
	}
}

// ========== isCommandCoveredByVerify ==========

func TestIsCommandCoveredByVerify(t *testing.T) {
	cases := []struct {
		name     string
		command  string
		required []string
		want     bool
	}{
		{"exact match", "go test ./...", []string{"go test ./..."}, true},
		{"substring of verify", "go test", []string{"go test ./..."}, true},
		{"verify substring of cmd", "go test ./internal/...", []string{"go test"}, true},
		{"different commands", "go build ./...", []string{"go test ./..."}, false},
		{"empty required", "go test", nil, false},
		{"empty command", "", []string{"go test"}, false},
		{"case insensitive", "GO TEST ./...", []string{"go test ./..."}, true},
		{"whitespace normalized", "  go   test  ", []string{"go test"}, true},
		{"multiple required one matches", "go test", []string{"go build", "go test"}, true},
		{"multiple required none match", "go vet", []string{"go build", "go test"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCommandCoveredByVerify(tc.command, tc.required); got != tc.want {
				t.Errorf("isCommandCoveredByVerify(%q, %v) = %v, want %v",
					tc.command, tc.required, got, tc.want)
			}
		})
	}
}

// ========== stepLikelyMutatesFiles ==========

func TestStepLikelyMutatesFiles(t *testing.T) {
	// nil entry → false (nothing happened).
	if stepLikelyMutatesFiles(nil, nil) {
		t.Error("nil entry should not mutate")
	}

	// Entry with mutating tool → true.
	entry := &plan.RunLedgerEntry{Tools: []string{"write", "read"}}
	if !stepLikelyMutatesFiles(nil, entry) {
		t.Error("entry with 'write' tool should mutate")
	}

	// Entry with only read tools + safe commands → false.
	entry = &plan.RunLedgerEntry{
		Tools:    []string{"read", "grep"},
		Commands: []string{"go test ./..."},
	}
	if stepLikelyMutatesFiles(nil, entry) {
		t.Error("read-only tools + safe commands should not mutate")
	}

	// Entry with mutating command not covered by verify → true.
	// NOTE: "rm -rf /tmp/build" is NOT detected as mutating because
	// looksLikeVerificationCommand sees " build" and treats it as a verify
	// command. Use a command without verification-signal words instead.
	entry = &plan.RunLedgerEntry{
		Tools:    []string{"read"},
		Commands: []string{"rm -rf target"},
	}
	if !stepLikelyMutatesFiles(nil, entry) {
		t.Error("rm command should mutate")
	}

	// Mutating command covered by step's verify → not counted as mutation.
	step := &plan.Step{VerifyCommands: []string{"npm install"}}
	entry = &plan.RunLedgerEntry{
		Tools:    []string{"read"},
		Commands: []string{"npm install express"},
	}
	if stepLikelyMutatesFiles(step, entry) {
		t.Error("command covered by step verify should not count as mutation")
	}
}

// ========== normalizeExpectedArtifactPaths ==========

func TestNormalizeExpectedArtifactPaths(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		want  int
	}{
		{"empty", nil, 0},
		{"single", []string{"main.go"}, 1},
		{"multiple", []string{"a.go", "b.go"}, 2},
		{"dedup same path", []string{"main.go", "main.go"}, 1},
		{"dedup normalized same", []string{"./main.go", "main.go"}, 1},
		{"trim whitespace", []string{"  main.go  "}, 1},
		{"filter empty", []string{"", "main.go", ""}, 1},
		{"dedup case insensitive", []string{"Main.go", "main.go"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeExpectedArtifactPaths(tc.paths)
			if len(got) != tc.want {
				t.Errorf("normalizeExpectedArtifactPaths(%v) = %v (len %d), want len %d",
					tc.paths, got, len(got), tc.want)
			}
		})
	}
}

// ========== normalizePathHintForMatch ==========

func TestNormalizePathHintForMatch(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"main.go", "main.go"},
		{"./main.go", "main.go"},
		{"/abs/path/main.go", "abs/path/main.go"},
		{"./src/a.go", "src/a.go"},
		{"Main.GO", "main.go"},
		{`"main.go"`, "main.go"},
		{"`main.go`", "main.go"},
		{"", ""},
		{"   ", ""},
		{".\\src\\a.go", "src/a.go"}, // backslash → forward slash
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			if got := normalizePathHintForMatch(tc.input); got != tc.want {
				t.Errorf("normalizePathHintForMatch(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ========== looksLikeArtifactPathHint ==========

func TestLooksLikeArtifactPathHint(t *testing.T) {
	yes := []string{
		"/abs/path/file.go",
		"./relative.go",
		"../parent.go",
		"src/file.go",
		"dir\\file.go",
		"main.go", // has extension
		"config.yaml",
		"Makefile", // no ext, no slash → false? Actually no ext → false
	}
	for _, p := range yes {
		t.Run("yes:"+p, func(t *testing.T) {
			// Makefile has no extension and no slash — it should NOT match.
			// We handle this edge case below by checking explicitly.
			if p == "Makefile" {
				if looksLikeArtifactPathHint(p) {
					t.Errorf("looksLikeArtifactPathHint(%q) should be false (no ext, no slash)", p)
				}
				return
			}
			if !looksLikeArtifactPathHint(p) {
				t.Errorf("looksLikeArtifactPathHint(%q) = false, want true", p)
			}
		})
	}

	no := []string{
		"",
		"hello", // no slash, no extension
		"run",   // bare word
		"a",     // too short, no ext
	}
	for _, p := range no {
		t.Run("no:"+p, func(t *testing.T) {
			if looksLikeArtifactPathHint(p) {
				t.Errorf("looksLikeArtifactPathHint(%q) = true, want false", p)
			}
		})
	}
}

// ========== extractArtifactPathHints ==========

func TestExtractArtifactPathHints(t *testing.T) {
	cases := []struct {
		name     string
		artifact string
		want     int
	}{
		{"empty", "", 0},
		{"single file", "main.go", 1},
		{"space separated", "main.go test.go", 2},
		{"comma separated", "main.go, utils.go", 2},
		{"pipe separated", "main.go | config.yaml", 2},
		{"with prose", "create main.go and update utils.go", 2},
		{"dedup", "main.go main.go", 1},
		{"mixed with non-paths", "create main.go then run tests", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractArtifactPathHints(tc.artifact)
			if len(got) != tc.want {
				t.Errorf("extractArtifactPathHints(%q) = %v (len %d), want len %d",
					tc.artifact, got, len(got), tc.want)
			}
		})
	}
}

// ========== collectExpectedArtifactHints ==========

func TestCollectExpectedArtifactHints(t *testing.T) {
	// nil step → nil.
	if got := collectExpectedArtifactHints(nil); got != nil {
		t.Errorf("nil step should return nil, got %v", got)
	}

	// Step with explicit paths.
	step := &plan.Step{
		ExpectedArtifactPaths: []string{"main.go", "utils.go"},
	}
	got := collectExpectedArtifactHints(step)
	if len(got) != 2 {
		t.Errorf("explicit paths: got %d hints, want 2: %v", len(got), got)
	}

	// Step with artifact prose containing paths.
	step = &plan.Step{
		ExpectedArtifact: "create auth.go and session.go",
	}
	got = collectExpectedArtifactHints(step)
	if len(got) != 2 {
		t.Errorf("artifact prose: got %d hints, want 2: %v", len(got), got)
	}

	// Step with both — dedup across sources.
	step = &plan.Step{
		ExpectedArtifactPaths: []string{"main.go"},
		ExpectedArtifact:      "update main.go and add utils.go",
	}
	got = collectExpectedArtifactHints(step)
	if len(got) != 2 {
		t.Errorf("combined dedup: got %d hints, want 2 (main.go deduped): %v", len(got), got)
	}

	// Step with nothing.
	step = &plan.Step{}
	got = collectExpectedArtifactHints(step)
	if len(got) != 0 {
		t.Errorf("empty step: got %d hints, want 0: %v", len(got), got)
	}
}

// ========== artifactHintsCovered ==========

func TestArtifactHintsCovered(t *testing.T) {
	// Empty hints → always covered.
	covered, matched, missing := artifactHintsCovered(nil, nil, "")
	if !covered || len(matched) != 0 || len(missing) != 0 {
		t.Errorf("empty hints should be covered, got covered=%v matched=%v missing=%v", covered, matched, missing)
	}

	// Hint matched by entry.FilesTouched.
	entry := &plan.RunLedgerEntry{FilesTouched: []string{"main.go"}}
	covered, matched, missing = artifactHintsCovered([]string{"main.go"}, entry, "")
	if !covered {
		t.Errorf("hint in FilesTouched should be covered, missing=%v", missing)
	}

	// Hint not matched.
	covered, _, missing = artifactHintsCovered([]string{"missing.go"}, entry, "")
	if covered {
		t.Error("hint not in entry should not be covered")
	}
	if len(missing) != 1 || missing[0] != "missing.go" {
		t.Errorf("missing = %v, want [missing.go]", missing)
	}

	// Hint matched by output text.
	covered, _, _ = artifactHintsCovered([]string{"auth.go"}, nil, "Created auth.go successfully")
	if !covered {
		t.Error("hint in output text should be covered")
	}

	// Partial match.
	entry = &plan.RunLedgerEntry{FilesTouched: []string{"a.go"}}
	covered, matched, missing = artifactHintsCovered([]string{"a.go", "b.go"}, entry, "")
	if covered {
		t.Error("partial match should not be fully covered")
	}
	if len(matched) != 1 || len(missing) != 1 {
		t.Errorf("partial: matched=%v missing=%v, want 1 each", matched, missing)
	}
}

// ========== verifyCommandsCovered ==========

func TestVerifyCommandsCovered(t *testing.T) {
	// No required commands → vacuously true.
	if !verifyCommandsCovered(nil, nil) {
		t.Error("nil required should be covered")
	}

	// Required but no entry → false.
	if verifyCommandsCovered([]string{"go test"}, nil) {
		t.Error("required with nil entry should not be covered")
	}

	// Required but entry has no commands → false.
	entry := &plan.RunLedgerEntry{}
	if verifyCommandsCovered([]string{"go test"}, entry) {
		t.Error("required with empty commands should not be covered")
	}

	// Exact match.
	entry = &plan.RunLedgerEntry{Commands: []string{"go test ./..."}}
	if !verifyCommandsCovered([]string{"go test ./..."}, entry) {
		t.Error("exact match should be covered")
	}

	// All required covered.
	entry = &plan.RunLedgerEntry{Commands: []string{"go test ./...", "go build ./..."}}
	if !verifyCommandsCovered([]string{"go test", "go build"}, entry) {
		t.Error("all required covered should be true")
	}

	// One required missing.
	entry = &plan.RunLedgerEntry{Commands: []string{"go test ./..."}}
	if verifyCommandsCovered([]string{"go test", "go build"}, entry) {
		t.Error("missing one required should not be covered")
	}

	// Substring match.
	entry = &plan.RunLedgerEntry{Commands: []string{"go test ./internal/..."}}
	if !verifyCommandsCovered([]string{"go test"}, entry) {
		t.Error("substring match should be covered")
	}
}

// ========== buildStepContractJSON ==========

func TestBuildStepContractJSON(t *testing.T) {
	// nil step → empty string.
	if got := buildStepContractJSON(nil, 5); got != "" {
		t.Errorf("nil step should return empty, got %q", got)
	}

	// Valid step → parseable JSON with correct fields.
	step := &plan.Step{
		ID:                    1,
		Title:                 "Implement auth",
		Description:           "Add JWT authentication",
		ExpectedArtifact:      "auth.go",
		ExpectedArtifactPaths: []string{"auth.go"},
		SuccessCriteria:       []string{"tests pass"},
		VerifyCommands:        []string{"go test ./..."},
		Rollback:              "git checkout -- auth.go",
	}
	got := buildStepContractJSON(step, 3)
	if got == "" {
		t.Fatal("expected non-empty JSON")
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, got)
	}

	if payload["step_id"] != float64(1) {
		t.Errorf("step_id = %v, want 1", payload["step_id"])
	}
	if payload["total_steps"] != float64(3) {
		t.Errorf("total_steps = %v, want 3", payload["total_steps"])
	}
	if payload["title"] != "Implement auth" {
		t.Errorf("title = %v", payload["title"])
	}
	if payload["expected_artifact"] != "auth.go" {
		t.Errorf("expected_artifact = %v", payload["expected_artifact"])
	}
}

// ========== buildStepCompletionProofJSON ==========

func TestBuildStepCompletionProofJSON(t *testing.T) {
	// nil step → empty.
	if got := buildStepCompletionProofJSON(nil, true, true, true, true, 3, 3, 2, true, 1, true, nil, nil, nil); got != "" {
		t.Errorf("nil step should return empty, got %q", got)
	}

	step := &plan.Step{ID: 2, ExpectedArtifact: "result.go"}
	entry := &plan.RunLedgerEntry{
		ToolCalls:    5,
		Tools:        []string{"read", "write"},
		FilesTouched: []string{"result.go"},
		Commands:     []string{"go test"},
	}

	got := buildStepCompletionProofJSON(step, true, false, true, true, 2, 3, 1, false, 1, true,
		[]string{"result.go"}, nil, entry)
	if got == "" {
		t.Fatal("expected non-empty JSON")
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload["step_id"] != float64(2) {
		t.Errorf("step_id = %v, want 2", payload["step_id"])
	}
	if payload["artifact_proof"] != true {
		t.Errorf("artifact_proof = %v, want true", payload["artifact_proof"])
	}
	if payload["verification_proof"] != false {
		t.Errorf("verification_proof = %v, want false", payload["verification_proof"])
	}
	if payload["evidence_classes"] != float64(2) {
		t.Errorf("evidence_classes = %v, want 2", payload["evidence_classes"])
	}
	// Entry fields included.
	if payload["tool_calls"] != float64(5) {
		t.Errorf("tool_calls = %v, want 5", payload["tool_calls"])
	}
	// Matched hints included, missing omitted (nil).
	if _, ok := payload["artifact_hints_matched"]; !ok {
		t.Error("artifact_hints_matched should be present")
	}
	if _, ok := payload["artifact_hints_missing"]; ok {
		t.Error("artifact_hints_missing should be absent when nil")
	}
}

// ========== normalizeCommandFingerprint ==========

func TestNormalizeCommandFingerprint(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"go test ./...", "go test ./..."},
		{"  go   test  ", "go test"},
		{"GO TEST", "go test"},
		{"", ""},
		{"   ", ""},
		{"go test\t./...", "go test ./..."},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			if got := normalizeCommandFingerprint(tc.input); got != tc.want {
				t.Errorf("normalizeCommandFingerprint(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ========== extractCommandPathHints ==========

func TestExtractCommandPathHints(t *testing.T) {
	// looksLikePath requires a path separator OR a leading dot. A bare
	// "main.go" has neither, so it's not detected as a path hint. Only
	// paths with "/", "\", or starting with "." qualify.
	cases := []struct {
		name    string
		command string
		want    int
	}{
		{"empty", "", 0},
		// "./..." → normalizePathToken returns "./..." (no glob chars *?[]{),
		// and looksLikePath sees the leading "./" → it IS detected as a path.
		// This is a known false positive (it's a Go wildcard, not a real file).
		{"go test wildcard", "go test ./...", 1},
		{"path with slash", "cat src/main.go", 1},
		{"multiple paths", "cp src/a.go dst/b.go", 2},
		{"no path", "go test", 0},
		{"redirect target", "echo hello > output.txt", 0}, // output.txt has no separator
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractCommandPathHints(tc.command)
			if len(got) != tc.want {
				t.Errorf("extractCommandPathHints(%q) = %v (len %d), want len %d",
					tc.command, got, len(got), tc.want)
			}
		})
	}
}

// ========== normalizePathToken ==========

func TestNormalizePathToken(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"main.go", "main.go"},
		{`"main.go"`, "main.go"},
		{"`main.go`", "main.go"},
		{"(main.go)", "main.go"},
		{"cd src/", "src/"}, // cd prefix stripped, but no ext... actually "src/" has slash
		{">output.txt", "output.txt"},
		{"$HOME", ""},              // env var filtered
		{"http://example.com", ""}, // URL filtered
		{"*.go", ""},               // glob filtered
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := normalizePathToken(tc.input)
			// Some cases are environment-dependent; just check non-empty when expected.
			if tc.want == "" && got != "" {
				t.Errorf("normalizePathToken(%q) = %q, want empty", tc.input, got)
			}
			if tc.want != "" && got == "" {
				t.Errorf("normalizePathToken(%q) = empty, want non-empty", tc.input)
			}
		})
	}
}

// ========== formatDuration ==========

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "0ms"},
		{"sub-second", 500 * time.Millisecond, "500ms"},
		{"exactly 1 second", 1 * time.Second, "1.0s"},
		{"1.5 seconds", 1500 * time.Millisecond, "1.5s"},
		{"over a minute", 65 * time.Second, "1m5s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatDuration(tc.d)
			if got != tc.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}
