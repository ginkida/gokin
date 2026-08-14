package hybrid

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDecideModes(t *testing.T) {
	prompt := "Rank repository files by TODO count"
	if got := Decide("tools", prompt); got.Enabled {
		t.Fatalf("tools mode enabled REPL: %+v", got)
	}
	if got := Decide("hybrid", "fix the auth bug"); !got.Enabled {
		t.Fatalf("hybrid mode disabled REPL: %+v", got)
	}
	if got := Decide("auto", "please use repl_exec for this"); !got.Enabled {
		t.Fatalf("explicit request did not enable REPL: %+v", got)
	}
}

func TestDecideAutoCollectionAnalysis(t *testing.T) {
	tests := []string{
		"Which top-level directories in the repository contain the most TODO/FIXME comments? Rank the counts.",
		"Which exported functions across all files are never mentioned in any _test.go file?",
		"Сколько TODO в файлах репозитория? Сгруппируй по директориям и сравни.",
		"Compare error handling across all files in the repository",
	}
	for _, prompt := range tests {
		if got := Decide("auto", prompt); !got.Enabled {
			t.Errorf("Decide(auto, %q) disabled REPL: %s", prompt, got.Reason)
		}
	}
}

func TestDecideAutoSimpleRequests(t *testing.T) {
	tests := []string{
		"fix the auth bug",
		"show me README.md",
		"what is 2+2?",
		"compare these two values",
		"compare these two repository files",
		"сравни эти две функции репозитория",
		"compare report files",
		"list files in this directory",
	}
	for _, prompt := range tests {
		if got := Decide("auto", prompt); got.Enabled {
			t.Errorf("Decide(auto, %q) enabled REPL: %s", prompt, got.Reason)
		}
	}
}

func TestAnalysisHintRequiresBothCandidateIntentAndExposure(t *testing.T) {
	candidate := "Count TODOs per directory across the repository"
	if hint := AnalysisHint(candidate, true); hint == "" || !strings.Contains(hint, "repl_exec") ||
		!strings.Contains(hint, "count_code_many") || !strings.Contains(hint, "sample_limit") ||
		!strings.Contains(hint, "file_stats") || !strings.Contains(hint, "list_files") ||
		strings.Contains(hint, "read_slice") {
		t.Fatalf("candidate hint = %q", hint)
	}
	join := "Which exported functions across all files are never mentioned in any _test.go file?"
	if hint := AnalysisHint(join, true); !strings.Contains(hint, "list_files") ||
		!strings.Contains(hint, "read_slice") || strings.Contains(hint, "count_code_many") {
		t.Fatalf("cross-file hint = %q", hint)
	}
	explicit := Decide("auto", "Use repl_exec to inspect this file")
	if hint := AnalysisHintForDecision(explicit, true); !strings.Contains(hint, "as requested") ||
		strings.Contains(hint, "count_code_many") || strings.Contains(hint, "list_files") {
		t.Fatalf("explicit hint = %q", hint)
	}
	if hint := AnalysisHint(candidate, false); hint != "" {
		t.Fatalf("unexposed REPL produced hint %q", hint)
	}
	if hint := AnalysisHint("fix the auth bug", true); hint != "" {
		t.Fatalf("ordinary edit produced hybrid hint %q", hint)
	}
	for _, targeted := range []string{
		"Count TODO lines in this file only",
		"Compare `pair/left.json` with `pair/right.json` in this repository",
	} {
		if hint := AnalysisHint(targeted, true); hint != "" {
			t.Fatalf("targeted request %q produced hybrid hint %q", targeted, hint)
		}
	}
	for strategy, hint := range map[Strategy]string{
		StrategyExplicit: explicitAnalysisHint, StrategyAggregation: aggregationAnalysisHint,
		StrategyCrossFile: crossFileAnalysisHint,
	} {
		if len(hint) > 260 {
			t.Fatalf("request-scoped %s hint exceeds 260-byte budget: %d", strategy, len(hint))
		}
	}
}

func TestDecideAutoSelectsWorkloadStrategy(t *testing.T) {
	for _, test := range []struct {
		prompt string
		want   Strategy
	}{
		{"Count TODOs per directory across the repository", StrategyAggregation},
		{"Which files are largest across the whole workspace?", StrategyAggregation},
		{"Compare error handling across all files in the repository", StrategyCrossFile},
		{"Count and compare error frequencies across all repository files", StrategyAggregation},
		{"Сколько TODO и FIXME во всех файлах репозитория? Сравни итоги.", StrategyAggregation},
		{"Which exported APIs lack tests across the whole codebase?", StrategyCrossFile},
		{"Use repl_exec for this one file", StrategyExplicit},
	} {
		decision := Decide("auto", test.prompt)
		if !decision.Enabled || decision.Strategy != test.want {
			t.Errorf("Decide(auto, %q) = %+v, want strategy %q", test.prompt, decision, test.want)
		}
	}
}

func BenchmarkDecideCollectionPrompt(b *testing.B) {
	prompt := strings.Repeat("Review the repository data carefully. ", 120) +
		"Rank TODO and FIXME counts across all repository files by top-level directory."
	b.ReportAllocs()
	for b.Loop() {
		decision := Decide("auto", prompt)
		if !decision.Enabled {
			b.Fatal(decision.Reason)
		}
	}
}

func TestDecideOversizedPromptIsBoundedAndConservative(t *testing.T) {
	filler := strings.Repeat("x", maxAutoPolicyPromptBytes+1)
	if got := Decide("auto", "Count TODOs across every repository file. "+filler); !got.Enabled ||
		!strings.Contains(got.Reason, "bounded prompt view") {
		t.Fatalf("oversized analytics request = %+v, want bounded REPL eligibility", got)
	}
	if got := Decide("auto", filler+"\nPlease use repl_exec for this analysis."); !got.Enabled {
		t.Fatalf("oversized explicit request = %+v, want REPL exposure", got)
	}
	if got := Decide("auto", "Fix the auth bug. "+filler); got.Enabled {
		t.Fatalf("oversized ordinary request = %+v, want structured-tool path", got)
	}
	if got := Decide("tools", strings.Repeat("X", maxAutoPolicyPromptBytes*4)); got.Enabled {
		t.Fatalf("tools mode inspected/enabled an oversized request: %+v", got)
	}
}

func TestAutoPolicyTextOversizedLoweringMatchesReference(t *testing.T) {
	head := "  COUNT Δ ИСПРАВЛЕНИЙ " + strings.Repeat("A", maxAutoPolicyPromptBytes/2)
	middle := strings.Repeat("omitted", maxAutoPolicyPromptBytes)
	tail := strings.Repeat("Б", maxAutoPolicyPromptBytes/2) + " REPOSITORY  "
	prompt := head + middle + tail
	got, oversized := autoPolicyText(prompt)
	if !oversized {
		t.Fatal("oversized prompt was not bounded")
	}
	edgeBytes := maxAutoPolicyPromptBytes / 2
	headEnd := edgeBytes
	for headEnd > 0 && !utf8.RuneStart(prompt[headEnd]) {
		headEnd--
	}
	tailStart := len(prompt) - edgeBytes
	for tailStart < len(prompt) && !utf8.RuneStart(prompt[tailStart]) {
		tailStart++
	}
	want := strings.ToLower(strings.TrimSpace(
		prompt[:headEnd] + "\n" + prompt[tailStart:],
	))
	if got != want {
		t.Fatalf("direct oversized lowercase drifted: got_bytes=%d want_bytes=%d", len(got), len(want))
	}
}

func BenchmarkDecideOversizedPrompt(b *testing.B) {
	prompt := "Count TODOs across every repository file.\n" +
		strings.Repeat("ordinary pasted log material\n", 1<<15)
	b.SetBytes(int64(len(prompt)))
	b.ReportAllocs()
	for b.Loop() {
		decision := Decide("auto", prompt)
		if !decision.Enabled {
			b.Fatal(decision.Reason)
		}
	}
}

func TestMutationPrefilterPreservesContextualClassification(t *testing.T) {
	legacy := func(text string) bool {
		for _, phrase := range nonMutationPhrases {
			text = strings.ReplaceAll(text, phrase, " ")
		}
		text = maskQuotedPolicyText(text)
		for _, term := range mutationTerms {
			if containsTermWhere(text, term, func(start, end int) bool {
				return !mutationOccurrenceIsAnalytic(text, start, end)
			}) {
				return true
			}
		}
		return false
	}

	contexts := []string{
		"%s",
		"please %s every package",
		"do not %s files",
		"count %s calls per repository package",
		"rank files that %s handler across the repository",
		"`%s`",
		`"%s"`,
		"prefix%s suffix",
		"before. then %s the result",
		"посчитай %s по всем файлам репозитория",
		"не %s файлы",
	}
	words := append([]string(nil), mutationTerms...)
	words = append(words,
		"account", "countdown", "implementations", "fixed", "writer", "created_at",
		"исправлений", "реализация", "добавленный", "неисправность", "переименование",
		"ordinary", "repository", "statistics", "frequency",
	)
	for _, word := range words {
		for _, context := range contexts {
			text := strings.ToLower(fmt.Sprintf(context, word))
			if got, want := hasMutationIntent(text), legacy(text); got != want {
				t.Fatalf("mutation prefilter drift for %q: got=%t want=%t", text, got, want)
			}
		}
	}
}

func BenchmarkHasMutationIntentNoSignal(b *testing.B) {
	text := strings.Repeat("ordinary repository analytics evidence without commands ", 1200)
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	for b.Loop() {
		if hasMutationIntent(text) {
			b.Fatal("ordinary analytics text classified as mutation")
		}
	}
}
