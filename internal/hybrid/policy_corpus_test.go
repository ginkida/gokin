package hybrid

import (
	"strings"
	"testing"
)

// This corpus protects the precision-first contract of auto mode. The negative
// cases are intentionally close to policy keywords: exposing one extra schema
// to every ordinary request is the expensive failure mode this router avoids.
func TestDecideAutoAdversarialCorpus(t *testing.T) {
	tests := []struct {
		name    string
		prompt  string
		enabled bool
	}{
		// Genuine collection-scale computation.
		{name: "english count", prompt: "Count TODOs per directory across the repository", enabled: true},
		{name: "implicit grouped collection", prompt: "Count TODOs per directory", enabled: true},
		{name: "punctuated count", prompt: "Count: how many errors occur in all workspace logs?", enabled: true},
		{name: "percentile", prompt: "What is the p95 latency across all records in this dataset?", enabled: true},
		{name: "histogram", prompt: "Build a histogram of file extensions in the workspace", enabled: true},
		{name: "explicit directory collection", prompt: "Count TODOs in all files under `src/` in the repository", enabled: true},
		{name: "extensionless directory collection", prompt: "Count TODOs in all files under `internal/app` in the repository", enabled: true},
		{name: "pair of directory collections", prompt: "Compare error frequencies in all files under `src/` and `tests/`", enabled: true},
		{name: "set difference", prompt: "Find orphaned exported functions in the whole repository", enabled: true},
		{name: "relationship graph", prompt: "Show the dependency graph between all packages in the codebase", enabled: true},
		{name: "broad comparison", prompt: "Compare error handling across all files in the repository", enabled: true},
		{name: "broad quoted concepts", prompt: "Compare `nil` and `error` handling across all repository files", enabled: true},
		{name: "dotted symbols are concepts", prompt: "Compare `http.Client` and `net.Conn` usage across all repository files", enabled: true},
		{name: "single filename as exhaustive query", prompt: "Count all references to `app.go` across the repository", enabled: true},
		{name: "russian count", prompt: "Посчитай TODO по пакетам во всём репозитории", enabled: true},
		{name: "russian average", prompt: "Покажи среднее время по всем записям логов", enabled: true},
		{name: "russian set difference", prompt: "Какие функции не используются в тестах во всех файлах репозитория?", enabled: true},
		{name: "quoted mutation-like symbol", prompt: "Count calls to `remove` across every file in the repository", enabled: true},
		{name: "unquoted mutation-like symbol", prompt: "Count remove calls per package across the whole repository", enabled: true},
		{name: "relative implementation clause", prompt: "Rank files that implement Handler across the whole repository", enabled: true},
		{name: "mutation noun used as dimension", prompt: "Show change frequency per package across the whole repository", enabled: true},
		{name: "russian relative implementation clause", prompt: "Посчитай функции, которые реализуют Handler, по всем файлам репозитория", enabled: true},
		{name: "russian mutation noun", prompt: "Посчитай количество исправлений по всем коммитам репозитория", enabled: true},
		{name: "negative fix instruction", prompt: "Count TODOs across every repository file but do not fix them", enabled: true},
		{name: "explicit override", prompt: "Use repl_exec to inspect this one file", enabled: true},
		// Natural collection requests should not require benchmark-specific
		// count/rank wording to reach the computation plane.
		{name: "largest source files", prompt: "Which source files are largest across the whole workspace?", enabled: true},
		{name: "apis lacking tests", prompt: "Which exported APIs lack test coverage across the whole codebase?", enabled: true},
		{name: "production symbols absent from tests", prompt: "List production functions absent from tests across the whole repository", enabled: true},
		{name: "common configuration keys", prompt: "Find configuration keys common to every JSON file in the workspace", enabled: true},
		{name: "cross reference coverage", prompt: "Cross-reference routes across all modules with integration tests and report missing coverage", enabled: true},
		{name: "russian largest files", prompt: "Какие исходные файлы самые большие во всём репозитории?", enabled: true},
		{name: "russian missing coverage", prompt: "Какие экспортируемые функции не покрыты тестами во всём репозитории?", enabled: true},
		{name: "not covered by tests", prompt: "Which exported functions are not covered by tests anywhere in the repository?", enabled: true},
		{name: "unused exports", prompt: "Find unused exported functions across the whole codebase", enabled: true},
		{name: "zero references", prompt: "Which public methods have zero references across all workspace packages?", enabled: true},
		{name: "present in every config", prompt: "Which keys are present in every JSON configuration file in the workspace?", enabled: true},

		// Embedded words must not masquerade as count/top/most/logs signals.
		{name: "account is not count", prompt: "Fix account file validation", enabled: false},
		{name: "desktop is not top", prompt: "Update desktop files", enabled: false},
		{name: "almost is not most", prompt: "The repository migration is almost complete", enabled: false},
		{name: "catalogs are not logs", prompt: "Compare catalogs and dialogs", enabled: false},
		{name: "countdown is not count", prompt: "Explain the countdown timer in this file", enabled: false},

		// Ordinary targeted work stays on structured tools.
		{name: "at least", prompt: "Read at least one file before answering", enabled: false},
		{name: "top level", prompt: "Show the top-level directory structure", enabled: false},
		{name: "likely cause", prompt: "What is the most likely cause in this repository?", enabled: false},
		{name: "targeted summary", prompt: "Summarize this single file", enabled: false},
		{name: "single file count", prompt: "Count TODO and FIXME lines in this file only", enabled: false},
		{name: "named file count", prompt: "Count TODO lines in `internal/app/app.go` only", enabled: false},
		{name: "named file noun count", prompt: "Count TODO lines in the app.go file", enabled: false},
		{name: "named file repository count", prompt: "Count TODO lines in `app.go` across this repository", enabled: false},
		{name: "extensionless named file", prompt: "Count TODO lines in `Dockerfile` across this repository", enabled: false},
		{name: "target in second sentence", prompt: "This task is in the repository. Count TODO lines in `internal/app/app.go` only.", enabled: false},
		{name: "targeted method count", prompt: "Count methods on this type", enabled: false},
		{name: "search scope", prompt: "Find Foo across the repository", enabled: false},
		{name: "pairwise files", prompt: "Compare these two repository files", enabled: false},
		{name: "pairwise named paths", prompt: "Compare `pair/left.json` with `pair/right.json` in this repository", enabled: false},
		{name: "pairwise unquoted paths", prompt: "Compare config/left.json with config/right.json across the repository", enabled: false},
		{name: "pairwise with inline verification", prompt: "Compare `a.go` and `b.go` across the repository, then run `go test ./...`", enabled: false},
		{name: "bounded average", prompt: "Average these two values from the file", enabled: false},
		{name: "russian pairwise", prompt: "Сравни эти две функции репозитория", enabled: false},
		{name: "largest item in named file", prompt: "What is the largest array in `internal/app/app.go`?", enabled: false},
		{name: "targeted semantic references", prompt: "Find all references to Handler in this repository", enabled: false},
		{name: "targeted semantic cross reference", prompt: "Cross-reference Handler across all modules", enabled: false},
		{name: "pairwise cross reference", prompt: "Cross-reference `a.go` and `b.go` across the repository", enabled: false},
		{name: "local missing docs", prompt: "Which API lacks documentation in this package?", enabled: false},
		{name: "local common style", prompt: "Explain the common style used in this file", enabled: false},
		{name: "targeted uncovered function", prompt: "Why is Handler not covered by tests in this package?", enabled: false},
		{name: "targeted unused variable", prompt: "Explain the unused variable warning in this package", enabled: false},
		{name: "targeted zero references", prompt: "Why does Handler have zero references in this package?", enabled: false},

		// Cross-file mutations do not benefit from a read-only computation plane.
		{name: "unique field mutation", prompt: "Add a unique ID to all records", enabled: false},
		{name: "comparison bug mutation", prompt: "Fix comparison logic across the repository", enabled: false},
		{name: "change after comparison", prompt: "Compare configs across all repository packages and change inconsistent files", enabled: false},
		{name: "migration after ranking", prompt: "Rank repository packages by legacy API usage and migrate the top package", enabled: false},
		{name: "fix after counting", prompt: "Count TODOs across the repository and fix the top package", enabled: false},
		{name: "remove after counting", prompt: "Count calls across the repository and remove obsolete ones", enabled: false},
		{name: "imperative change frequency", prompt: "Change frequency calculation across all repository packages", enabled: false},
		{name: "refactor after comparison", prompt: "Compare packages across the repository, then refactor duplicates", enabled: false},
		{name: "russian mutation", prompt: "Исправь зависимости между пакетами во всём репозитории", enabled: false},
		{name: "russian mutation after count", prompt: "Посчитай TODO по репозиторию, затем исправь найденные места", enabled: false},
		// A negative mutation instruction must not suppress a read-only analysis.
		{name: "read only qualifier", prompt: "Count TODOs in every repository file; do not modify files", enabled: true},
		{name: "change qualifier", prompt: "Count TODOs across all repository files; do not change files", enabled: true},
		{name: "russian read only qualifier", prompt: "Посчитай TODO по всем файлам репозитория, ничего не изменяй", enabled: true},
		{name: "russian unchanged qualifier", prompt: "Посчитай TODO по всем файлам репозитория, оставь всё без изменений", enabled: true},
		{name: "scope before analysis clause", prompt: "Across all repository files. Count references to `app.go` and report the total.", enabled: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := Decide("auto", test.prompt)
			if decision.Enabled != test.enabled {
				t.Fatalf("Decide(auto, %q) = enabled %t (%s), want %t",
					test.prompt, decision.Enabled, decision.Reason, test.enabled)
			}
			if decision.Reason == "" {
				t.Fatal("decision omitted reason provenance")
			}
		})
	}
}

func TestContainsTermASCIIWordBoundaries(t *testing.T) {
	for _, text := range []string{"count files", "count: files", "COUNT files"} {
		if !containsTerm(lower(text), "count") {
			t.Errorf("containsTerm(%q, count) = false", text)
		}
	}
	for _, text := range []string{"account files", "countdown files", "discount files"} {
		if containsTerm(text, "count") {
			t.Errorf("containsTerm(%q, count) = true", text)
		}
	}
	if containsTerm("непосчитай", "посчитай") {
		t.Error("Cyrillic stem matched inside an unrelated word")
	}
}

func TestHasExplicitPairwiseTargets(t *testing.T) {
	for _, prompt := range []string{
		"Compare `pair/left.json` with `pair/right.json` in this repository",
		"Compare config/left.json with config/right.json across the repository",
		"Compare README.md and LICENSE.txt",
		"Compare `pair/left.json` with `pair/right.json` in this repository. Then run `go test ./...`.",
	} {
		if !hasExplicitPairwiseTargets(strings.ToLower(prompt)) {
			t.Errorf("hasExplicitPairwiseTargets(%q) = false", prompt)
		}
	}
	for _, prompt := range []string{
		"Compare `nil` and `error` handling across all repository files",
		"Compare every JSON file across all packages",
		"Read `internal/app/app.go`",
	} {
		if hasExplicitPairwiseTargets(strings.ToLower(prompt)) {
			t.Errorf("hasExplicitPairwiseTargets(%q) = true", prompt)
		}
	}
}

func TestExplicitPathTargetCountUsesAnalysisClauseOnly(t *testing.T) {
	for _, test := range []struct {
		prompt string
		want   int
	}{
		{"This is in the repository. Count TODO in `internal/app/app.go` only.", 1},
		{"Compare `a.go` and `b.go` across the repository, then run `go test ./...`", 2},
		{"Count TODOs in all files under `src/` in the repository", 0},
		{"Count TODOs in all files under `internal/app` in the repository", 0},
		{"Compare `http.Client` and `net.Conn` usage across all repository files", 0},
		{"Count TODOs in `Dockerfile` across the repository", 1},
		{"Across all repository files. Count references to `app.go`.", 1},
	} {
		if got := explicitPathTargetCount(strings.ToLower(test.prompt)); got != test.want {
			t.Errorf("explicitPathTargetCount(%q) = %d, want %d", test.prompt, got, test.want)
		}
	}
}

func lower(value string) string {
	return strings.ToLower(value)
}
