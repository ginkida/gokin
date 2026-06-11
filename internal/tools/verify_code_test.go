package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGoMod(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/vc\n\ngo 1.25\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyCodeSuccess(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	res, err := NewVerifyCodeTool(dir).Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("expected a clean build to succeed, got: %s", res.Error)
	}
}

// TestVerifyCodeFailureCapsOutput pins the regression fix: a failing build with
// voluminous output must NOT dump unbounded text into the Error field (which
// escapes the downstream content caps). Many broken packages produce >12000
// chars of `go build` errors; the result must be bounded and marked truncated.
func TestVerifyCodeFailureCapsOutput(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir)
	// 40 packages × 8 undefined refs each (under go's per-package "too many
	// errors" cap of 10) => ~320 long error lines, comfortably over the cap.
	for p := range 40 {
		pkgDir := filepath.Join(dir, fmt.Sprintf("pkg%02d", p))
		if err := os.MkdirAll(pkgDir, 0755); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "package pkg%02d\n\nvar (\n", p)
		for i := range 8 {
			fmt.Fprintf(&b, "\t_ = ThisIsADeliberatelyLongUndefinedSymbolNameForTheCapTest_%02d_%02d\n", p, i)
		}
		b.WriteString(")\n")
		if err := os.WriteFile(filepath.Join(pkgDir, "a.go"), []byte(b.String()), 0644); err != nil {
			t.Fatal(err)
		}
	}

	res, err := NewVerifyCodeTool(dir).Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Success {
		t.Fatalf("expected the broken build to fail, got success: %s", res.Content)
	}
	// The Error field is the one that escapes ToMap/ResponseCompressor caps —
	// it must be bounded at the source.
	const cap = 12000
	if n := len([]rune(res.Error)); n > cap+300 {
		t.Fatalf("Error field not capped: %d runes (cap %d + prefix/margin)", n, cap)
	}
	if !strings.Contains(res.Error, "elided") {
		t.Fatalf("expected an elision marker on voluminous failure output, got: %s", res.Error)
	}
	// Head+tail trim: real error lines must survive AFTER the elision marker
	// (node/python print the fatal error last; a head-only trim cut the tail).
	_, tail, _ := strings.Cut(res.Error, "elided")
	if !strings.Contains(tail, "ThisIsADeliberatelyLongUndefinedSymbolNameForTheCapTest") {
		t.Fatalf("tail of failure output dropped by the trim, post-marker tail:\n%s", tail)
	}
}
