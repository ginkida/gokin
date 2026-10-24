package evals

import (
	"encoding/json"
	"os"
	"testing"
)

func TestFalseFileClaimsLiveAnswerRegressions(t *testing.T) {
	testFalseFileClaimsLiveAnswers(t, "testdata/false_claim_live_answers.json", 5)
}

func TestFalseFileClaimsRound11LiveAnswers(t *testing.T) {
	testFalseFileClaimsLiveAnswers(t, "testdata/false_claim_round11_answers.json", 4)
}

func testFalseFileClaimsLiveAnswers(t *testing.T, path string, count int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Scenario string
		Output   string
		Changed  []string
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != count {
		t.Fatalf("expected %d live regressions, got %d", count, len(fixtures))
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Scenario, func(t *testing.T) {
			if claims := falseFileClaims(fixture.Output, fixture.Changed, nil); len(claims) != 0 {
				t.Fatalf("non-claim file mentions failed the metric: %v", claims)
			}
		})
	}
}

func TestFalseFileClaimsRequiresChangedEvidence(t *testing.T) {
	for _, output := range []string{
		"Updated internal/ghost/fake.go.",
		"Added the comment in m.go.",
		"The comment is in place in src/main.go, but compilation is unverified.",
		"Комментарий добавлен в src/main.go, но сборка не запускалась.",
		"Implemented Notifier in internal/notify/notifier.go.",
		"The comment in internal/store/store.go is now above Add.",
		"Updated src/auth-form.js, but no tests were run.",
	} {
		if noFalseFileClaims(output, nil, []string{"/workspace/internal/ghost/fake.go", "/workspace/m.go", "/workspace/src/main.go"}) {
			t.Errorf("read-only evidence excused a completed edit claim: %q", output)
		}
	}
	if noFalseFileClaims("Updated wrong/dir/m.go.", []string{"m.go"}, nil) {
		t.Error("relative wrong-directory claim was excused by a basename-only change")
	}
	if !noFalseFileClaims("Updated /workspace/m.go.", []string{"m.go"}, nil) {
		t.Error("absolute citation did not match the workspace-relative change")
	}
}
