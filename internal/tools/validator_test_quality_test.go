package tools

import (
	"context"
	"testing"
)

func TestTestQualityValidator_Matches(t *testing.T) {
	v := &TestQualityValidator{}

	if !v.Matches("foo_test.go") {
		t.Error("should match _test.go files")
	}
	if v.Matches("foo.go") {
		t.Error("should not match non-test files")
	}
	if v.Matches("test.yml") {
		t.Error("should not match non-Go files")
	}
}

func TestTestQualityValidator_IgnoredError(t *testing.T) {
	v := &TestQualityValidator{}
	src := []byte(`package foo

func TestSomething(t *testing.T) {
	err := doSomething()
	_ = err
}
`)
	warnings := v.Validate(context.Background(), "foo_test.go", src, ".")
	found := false
	for _, w := range warnings {
		if w.Line == 5 && w.Severity == "warning" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected warning for '_ = err' at line 5, got %v", warnings)
	}
}

func TestTestQualityValidator_NoAssertions(t *testing.T) {
	v := &TestQualityValidator{}
	src := []byte(`package foo

func TestEmpty(t *testing.T) {
	x := 42
	_ = x
}
`)
	warnings := v.Validate(context.Background(), "foo_test.go", src, ".")
	found := false
	for _, w := range warnings {
		if w.Message != "" && contains(w.Message, "no assertions") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'no assertions' warning, got %v", warnings)
	}
}

func TestTestQualityValidator_WithAssertions(t *testing.T) {
	v := &TestQualityValidator{}
	src := []byte(`package foo

func TestGood(t *testing.T) {
	result := compute()
	if result != 42 {
		t.Errorf("got %d, want 42", result)
	}
}
`)
	warnings := v.Validate(context.Background(), "foo_test.go", src, ".")
	for _, w := range warnings {
		if contains(w.Message, "no assertions") {
			t.Errorf("should not warn about assertions for test with t.Errorf, got %v", w)
		}
	}
}

func TestTestQualityValidator_HTTPWithoutShort(t *testing.T) {
	v := &TestQualityValidator{}
	src := []byte(`package foo

func TestHTTPCall(t *testing.T) {
	resp, err := http.Get("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp
}
`)
	warnings := v.Validate(context.Background(), "foo_test.go", src, ".")
	found := false
	for _, w := range warnings {
		if contains(w.Message, "HTTP") || contains(w.Message, "network") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected HTTP warning, got %v", warnings)
	}
}

func TestTestQualityValidator_HTTPWithShort(t *testing.T) {
	v := &TestQualityValidator{}
	src := []byte(`package foo

func TestHTTPCall(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	resp, err := http.Get("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp
}
`)
	warnings := v.Validate(context.Background(), "foo_test.go", src, ".")
	for _, w := range warnings {
		if contains(w.Message, "HTTP") || contains(w.Message, "network") {
			t.Errorf("should not warn about HTTP when testing.Short() guard present, got %v", w)
		}
	}
}

func TestTestQualityValidator_BenchmarkNoAssertionOK(t *testing.T) {
	v := &TestQualityValidator{}
	src := []byte(`package foo

func BenchmarkSomething(b *testing.B) {
	for i := 0; i < b.N; i++ {
		compute()
	}
}
`)
	warnings := v.Validate(context.Background(), "foo_test.go", src, ".")
	for _, w := range warnings {
		if contains(w.Message, "no assertions") {
			t.Errorf("should not warn about assertions for benchmarks, got %v", w)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
