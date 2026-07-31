package tasks

import (
	"strings"
	"testing"
)

func TestVerificationSummaryAggregatesCargoWorkspaceResults(t *testing.T) {
	output := strings.Repeat("compile output\n", 400) +
		"test result: ok. 1700 passed; 0 failed; 2 ignored; 0 measured; 0 filtered out; finished in 10.00s\n" +
		strings.Repeat("middle output\n", 400) +
		"test result: ok. 10 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out; finished in 1.00s\n" +
		"Doc-tests demo\n" +
		"test result: ok. 0 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out; finished in 0.00s\n"

	got := VerificationSummary("cargo test --workspace 2>&1", output)
	for _, want := range []string{"1710 passed", "0 failed", "2 ignored", "3 test harnesses"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q: %s", want, got)
		}
	}
	if got := VerificationSummary("go test ./...", output); got != "" {
		t.Fatalf("non-cargo command received cargo summary: %q", got)
	}
}
