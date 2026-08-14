// Package hybrid contains request-level policy for the optional computation
// plane. It deliberately has no runtime dependencies so the same decision is
// used by interactive routing and direct/headless execution.
package hybrid

import (
	"bytes"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Decision explains whether the REPL should be exposed for a request. It is an
// eligibility decision only: the model may still choose a simpler structured
// tool when that is cheaper.
type Decision struct {
	Enabled  bool
	Reason   string
	Strategy Strategy
}

// Strategy identifies the lowest-noise guidance for an eligible request. It is
// advisory only; capability exposure and safety remain controlled by Enabled
// and the final request schema.
type Strategy string

const (
	StrategyExplicit    Strategy = "explicit"
	StrategyAggregation Strategy = "aggregation"
	StrategyCrossFile   Strategy = "cross_file"

	aggregationAnalysisHint = "Collection analysis: use one repl_exec cell; return conclusions plus compact evidence. Prefer count_code_many for text patterns, file_stats for grouped inventory, or list_files for path-level stats; sample_limit adds evidence. Verify with structured tools."
	crossFileAnalysisHint   = "Cross-file analysis: use one repl_exec cell. Acquire paths once with list_files, read needed content with read_slice, compute the join/set relation in Python, and return conclusions plus compact evidence. Verify with structured tools."
	explicitAnalysisHint    = "Use repl_exec as requested; keep the work in one complete cell when practical, return compact evidence, and use structured tools for targeted reads or verification."
)

// Auto policy is a lightweight routing hint, not a parser for arbitrary model
// context. Bound its view so a multi-megabyte pasted log cannot multiply prompt
// length by every term dictionary. The edges preserve the conventional
// instruction-before/after-data layouts while the omitted middle cannot make
// routing cost scale with pasted evidence.
const maxAutoPolicyPromptBytes = 64 << 10

// AnalysisHint aligns prompt guidance with the final model-visible schema.
// It intentionally re-runs auto classification even in explicit hybrid mode:
// always exposing a tool is a user setting, not a reason to push a read-only
// computation plane into ordinary edits and targeted questions.
func AnalysisHint(prompt string, replExposed bool) string {
	return AnalysisHintForDecision(Decide("auto", prompt), replExposed)
}

// AnalysisHintForDecision reuses an already-computed auto eligibility result.
// Request preparation calls Decide once, then derives schema, journal evidence,
// and this transient provider hint from the same immutable decision.
func AnalysisHintForDecision(decision Decision, replExposed bool) string {
	if !replExposed || !decision.Enabled {
		return ""
	}
	switch decision.Strategy {
	case StrategyExplicit:
		return explicitAnalysisHint
	case StrategyCrossFile:
		return crossFileAnalysisHint
	default:
		return aggregationAnalysisHint
	}
}

// Decide resolves the configured engine mode for a single user prompt.
//
//   - tools: never expose the REPL
//   - hybrid: always expose the REPL (and, separately, the direct harness)
//   - auto: expose the REPL only for explicit requests or repository/data
//     questions that require aggregation, comparison, or cross-file joins
func Decide(mode, prompt string) Decision {
	switch normalizeMode(mode) {
	case "tools":
		return Decision{Reason: "engine.mode=tools"}
	case "hybrid":
		return Decision{Enabled: true, Reason: "engine.mode=hybrid"}
	}

	text, oversized := autoPolicyText(prompt)
	if text == "" {
		return Decision{Reason: "auto mode requires a request"}
	}
	if containsAny(text, explicitREPLTerms) {
		return Decision{
			Enabled: true, Reason: "explicit computation-plane request", Strategy: StrategyExplicit,
		}
	}
	hasAggregation := containsAny(text, aggregationTerms)
	hasRelationship := containsAny(text, relationshipTerms)
	if !hasAggregation && !hasRelationship {
		return Decision{Reason: "no aggregation or cross-item analysis intent"}
	}
	if hasMutationIntent(text) {
		return Decision{Reason: "cross-file mutation is cheaper with structured tools"}
	}
	hasCollectionScope := containsAny(text, collectionTerms) || containsASCIIWord(text, "repo")
	if !hasCollectionScope {
		return Decision{Reason: "no repository or dataset scope"}
	}
	if containsAny(text, singleTargetTerms) {
		return Decision{Reason: "single-target analysis is cheaper with structured tools"}
	}
	pathTargetCount := 0
	if mayContainExplicitPathTarget(text) {
		pathTargetCount = explicitPathTargetCount(text)
	}
	if pathTargetCount == 1 && !containsAny(text, exhaustiveScopeTerms) {
		return Decision{Reason: "explicit single-file analysis is cheaper with structured tools"}
	}
	if hasRelationship && pathTargetCount == 2 {
		return Decision{Reason: "explicit pairwise comparison is cheaper with structured tools"}
	}
	hasBroadScope := containsAny(text, broadScopeTerms) || containsASCIIWord(text, "repo")
	if !hasBroadScope {
		return Decision{Reason: "analysis lacks collection-scale scope"}
	}
	if hasAggregation && containsAny(text, boundedCollectionTerms) && !containsAny(text, exhaustiveScopeTerms) {
		return Decision{Reason: "bounded aggregation is cheaper with structured tools"}
	}
	// Pairwise comparison is cheaper with targeted reads. The general broad-
	// scope gate above has already rejected collection-free comparisons; this
	// final phrase gate catches natural pair wording without weakening strong
	// count/rank/group signals such as a repository-wide "top two" result.
	if !hasAggregation && hasRelationship {
		if containsAny(text, smallScopeTerms) && !containsAny(text, exhaustiveScopeTerms) {
			return Decision{Reason: "pairwise comparison is cheaper with structured tools"}
		}
	}
	strategy := StrategyAggregation
	// Count/rank/group requests often also say "compare"; their cheapest path
	// remains a batched aggregation. True set-difference/intersection signals
	// and relationship-only requests need paths/content for a Python join.
	if containsAny(text, crossFileSetTerms) || hasRelationship && !hasAggregation {
		strategy = StrategyCrossFile
	}
	reason := "collection-scale aggregation request"
	if strategy == StrategyCrossFile {
		reason = "collection-scale cross-file analysis request"
	}
	if oversized {
		reason += " (bounded prompt view)"
	}
	return Decision{Enabled: true, Reason: reason, Strategy: strategy}
}

func autoPolicyText(prompt string) (string, bool) {
	if len(prompt) <= maxAutoPolicyPromptBytes {
		return strings.ToLower(strings.TrimSpace(prompt)), false
	}

	// User instructions conventionally live at the start or end of pasted
	// material. Retain both without splitting a UTF-8 encoding. The omitted
	// middle is why ordinary inferred eligibility fails closed above.
	edgeBytes := maxAutoPolicyPromptBytes / 2
	headEnd := edgeBytes
	for headEnd > 0 && !utf8.RuneStart(prompt[headEnd]) {
		headEnd--
	}
	tailStart := len(prompt) - edgeBytes
	for tailStart < len(prompt) && !utf8.RuneStart(prompt[tailStart]) {
		tailStart++
	}
	var view strings.Builder
	view.Grow(headEnd + 1 + len(prompt) - tailStart)
	writeLowerPolicyText(&view, prompt[:headEnd])
	view.WriteByte('\n')
	writeLowerPolicyText(&view, prompt[tailStart:])
	return strings.TrimSpace(view.String()), true
}

func writeLowerPolicyText(builder *strings.Builder, value string) {
	for offset := 0; offset < len(value); {
		current := value[offset]
		if current < utf8.RuneSelf {
			if current >= 'A' && current <= 'Z' {
				current += 'a' - 'A'
			}
			builder.WriteByte(current)
			offset++
			continue
		}
		decoded, size := utf8.DecodeRuneInString(value[offset:])
		builder.WriteRune(unicode.ToLower(decoded))
		offset += size
	}
}

func normalizeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "tools", "hybrid":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return "auto"
	}
}

func containsAny(text string, terms []string) bool {
	for _, term := range terms {
		if containsTerm(text, term) {
			return true
		}
	}
	return false
}

// containsTerm prevents ASCII signals from firing inside unrelated words
// (count in account, top in desktop, most in almost). Cyrillic entries are
// intentionally stems and retain substring matching so inflected forms work.
func containsTerm(text, term string) bool {
	return containsTermWhere(text, term, func(_, _ int) bool { return true })
}

// containsTermWhere visits boundary-valid occurrences without allocating a
// token slice. The predicate can reject a lexical match based on its local
// grammatical context (used by mutation-intent detection below).
func containsTermWhere(text, term string, accept func(start, end int) bool) bool {
	if term == "" || len(term) > len(text) {
		return false
	}
	for offset := 0; offset <= len(text)-len(term); {
		index := strings.Index(text[offset:], term)
		if index < 0 {
			return false
		}
		index += offset
		first, _ := utf8.DecodeRuneInString(term)
		beforeOK := true
		if isWordRune(first) && index > 0 {
			previous, _ := utf8.DecodeLastRuneInString(text[:index])
			beforeOK = !isWordRune(previous)
		}
		after := index + len(term)
		last, _ := utf8.DecodeLastRuneInString(term)
		afterOK := true
		// ASCII policy entries are complete words/phrases and require a right
		// boundary. Cyrillic entries are intentionally inflection stems.
		if last < utf8.RuneSelf && isWordRune(last) && after < len(text) {
			next, _ := utf8.DecodeRuneInString(text[after:])
			afterOK = !isWordRune(next)
		}
		if beforeOK && afterOK && accept(index, after) {
			return true
		}
		offset = index + len(term)
	}
	return false
}

func isWordRune(value rune) bool {
	return unicode.IsLetter(value) || unicode.IsDigit(value) || value == '_'
}

func containsASCIIWord(text, word string) bool {
	for offset := 0; offset <= len(text)-len(word); {
		index := strings.Index(text[offset:], word)
		if index < 0 {
			return false
		}
		index += offset
		beforeOK := index == 0 || !isASCIIWordByte(text[index-1])
		after := index + len(word)
		afterOK := after == len(text) || !isASCIIWordByte(text[after])
		if beforeOK && afterOK {
			return true
		}
		offset = index + len(word)
	}
	return false
}

func isASCIIWordByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '_'
}

// hasExplicitPairwiseTargets catches the common natural form "compare a.go
// with b.go" even when the user never says "two". Repository scope alone must
// not turn two targeted reads into a collection-scale computation request.
// Only path-like tokens count: quoted concepts such as `nil` and `error` may
// still name dimensions of a genuinely repository-wide comparison.
func hasExplicitPairwiseTargets(text string) bool {
	return explicitPathTargetCount(text) == 2
}

// mayContainExplicitPathTarget avoids the comparatively expensive clause and
// token scan for the common case with no file-shaped token. A dot followed by
// an alphanumeric byte covers ordinary and hidden filenames; backticks may
// contain either a path or a concept and therefore require exact inspection.
func mayContainExplicitPathTarget(text string) bool {
	if strings.IndexByte(text, '`') >= 0 {
		return true
	}
	for index := 0; index+1 < len(text); index++ {
		if text[index] == '.' && isASCIIAlphaNumeric(text[index+1]) {
			return true
		}
	}
	return containsExtensionlessPathTarget(text)
}

func isASCIIAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func containsExtensionlessPathTarget(text string) bool {
	start := -1
	for index, value := range text {
		if isWordRune(value) {
			if start < 0 {
				start = index
			}
			continue
		}
		if start >= 0 && isExtensionlessPathTarget(text[start:index]) {
			return true
		}
		start = -1
	}
	return start >= 0 && isExtensionlessPathTarget(text[start:])
}

func isExtensionlessPathTarget(value string) bool {
	switch value {
	case "brewfile", "containerfile", "dockerfile", "gemfile", "jenkinsfile", "makefile", "procfile",
		"rakefile", "vagrantfile":
		return true
	default:
		return false
	}
}

func explicitPathTargetCount(text string) int {
	text = trimPolicyVerificationSuffix(firstAnalysisPolicyClause(text))
	// Three distinct values are enough to prove the request is not pairwise;
	// keep memory constant even when an untrusted prompt lists thousands of
	// file-looking tokens.
	targets := make(map[string]struct{}, 3)
	add := func(raw string) {
		if len(targets) >= 3 {
			return
		}
		target := strings.Trim(raw, " \t\r\n`'\"()[]{}<>,:;")
		target = strings.TrimRight(target, ".!?")
		if looksLikePathTarget(target) {
			targets[target] = struct{}{}
		}
	}

	for rest := text; ; {
		start := strings.IndexByte(rest, '`')
		if start < 0 {
			break
		}
		rest = rest[start+1:]
		end := strings.IndexByte(rest, '`')
		if end < 0 {
			break
		}
		if candidate := rest[:end]; !looksLikeVerificationCommand(candidate) {
			add(candidate)
		}
		rest = rest[end+1:]
	}
	// Backtick contents were handled as one target above. Mask them before the
	// ordinary field scan so command arguments such as `go test ./...` cannot
	// become a third apparent path.
	for _, field := range strings.Fields(maskDelimitedPolicyText(text, '`')) {
		add(field)
	}
	return len(targets)
}

func firstAnalysisPolicyClause(text string) string {
	fallback := ""
	for rest := text; rest != ""; {
		end, next := policyClauseBoundary(rest)
		clause := strings.TrimSpace(rest[:end])
		if fallback == "" && clause != "" {
			fallback = clause
		}
		if containsAny(clause, aggregationTerms) || containsAny(clause, relationshipTerms) {
			return clause
		}
		if next >= len(rest) {
			break
		}
		rest = rest[next:]
	}
	return fallback
}

func policyClauseBoundary(text string) (end, next int) {
	for index, value := range text {
		if value == '\n' || value == ';' {
			return index, index + utf8.RuneLen(value)
		}
		if value != '.' && value != '!' && value != '?' {
			continue
		}
		after := index + utf8.RuneLen(value)
		if after >= len(text) {
			return index, len(text)
		}
		nextRune, size := utf8.DecodeRuneInString(text[after:])
		if unicode.IsSpace(nextRune) {
			return index, after + size
		}
	}
	return len(text), len(text)
}

func trimPolicyVerificationSuffix(text string) string {
	lower := strings.ToLower(text)
	cut := len(text)
	for _, marker := range []string{
		", then run ", " and then run ", " then run ", ", finally run ",
		", then verify ", " and then verify ", " then verify ",
		", затем запусти", ", потом запусти", ", затем проверь", ", потом проверь",
	} {
		if index := strings.Index(lower, marker); index >= 0 && index < cut {
			cut = index
		}
	}
	return strings.TrimSpace(text[:cut])
}

func looksLikeVerificationCommand(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, prefix := range []string{
		"go test", "go vet", "cargo test", "npm test", "npm run test", "pnpm test",
		"yarn test", "pytest", "python -m pytest", "python3 -m pytest", "make test",
	} {
		if value == prefix || strings.HasPrefix(value, prefix+" ") {
			return true
		}
	}
	return false
}

func looksLikePathTarget(value string) bool {
	if value == "" || len(value) > 512 || strings.Contains(value, "://") ||
		strings.Contains(value, "...") || strings.ContainsAny(value, "*?[]{}") {
		return false
	}
	normalized := strings.ReplaceAll(value, "\\", "/")
	if normalized == "/" || strings.HasPrefix(normalized, "//") || strings.HasSuffix(normalized, "/") {
		return false
	}
	parts := strings.Split(normalized, "/")
	base := strings.TrimSpace(parts[len(parts)-1])
	if isExtensionlessPathTarget(strings.ToLower(base)) {
		return true
	}
	if strings.HasPrefix(base, ".") && len(base) > 1 && !strings.Contains(base[1:], ".") {
		return true
	}
	if strings.HasPrefix(base, "_") {
		return false
	}
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 || dot == len(base)-1 {
		return false
	}
	extension := base[dot+1:]
	if len(extension) > 12 {
		return false
	}
	switch strings.ToLower(extension) {
	case "bash", "c", "cc", "cfg", "conf", "cpp", "cs", "css", "csv", "cxx", "env", "fish", "go",
		"graphql", "h", "hcl", "hpp", "hs", "htm", "html", "ini", "java", "js", "json", "jsonl",
		"jsx", "kt", "kts", "lock", "log", "lua", "md", "mod", "php", "pl", "proto", "ps1", "py",
		"rb", "rs", "rst", "scala", "scss", "sh", "sql", "sum", "swift", "toml", "ts", "tsx", "txt",
		"vue", "xml", "yaml", "yml", "zsh":
		return true
	default:
		return false
	}
}

func hasMutationIntent(text string) bool {
	// Every mutation signal is one lexical word: complete ASCII words and
	// Cyrillic inflection stems anchored at a word boundary. Reject the common
	// no-mutation path in one scan before repeatedly applying the contextual
	// term matcher, quote masking, and negative-phrase normalization.
	if !mayContainMutationTerm(text) {
		return false
	}
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

func mayContainMutationTerm(text string) bool {
	start := -1
	for index, value := range text {
		if isWordRune(value) {
			if start < 0 {
				start = index
			}
			continue
		}
		if start >= 0 && isMutationPolicyWord(text[start:index]) {
			return true
		}
		start = -1
	}
	return start >= 0 && isMutationPolicyWord(text[start:])
}

func isMutationPolicyWord(word string) bool {
	switch word {
	case "fix", "implement", "add", "remove", "delete", "rename", "replace", "refactor", "update",
		"modify", "edit", "write", "create", "change", "move", "migrate", "rewrite", "convert", "generate":
		return true
	}
	for _, prefix := range cyrillicMutationTerms {
		if strings.HasPrefix(word, prefix) {
			return true
		}
	}
	return false
}

// maskQuotedPolicyText removes code/identifier mentions such as `remove` from
// mutation classification while preserving byte offsets for contextual scans.
// Backticks and double quotes are intentionally supported; apostrophes are left
// alone so contractions such as "don't" retain their meaning.
func maskQuotedPolicyText(text string) string {
	return maskDelimitedPolicyText(text, '`', '"')
}

func maskDelimitedPolicyText(text string, delimiters ...byte) string {
	hasPair := false
	for _, delimiter := range delimiters {
		start := strings.IndexByte(text, delimiter)
		if start >= 0 && strings.IndexByte(text[start+1:], delimiter) >= 0 {
			hasPair = true
			break
		}
	}
	if !hasPair {
		return text
	}
	masked := []byte(text)
	for _, delimiter := range delimiters {
		for offset := 0; offset < len(masked); {
			startRelative := bytes.IndexByte(masked[offset:], delimiter)
			if startRelative < 0 {
				break
			}
			start := offset + startRelative
			endRelative := bytes.IndexByte(masked[start+1:], delimiter)
			if endRelative < 0 {
				break
			}
			end := start + 1 + endRelative
			for index := start; index <= end; index++ {
				masked[index] = ' '
			}
			offset = end + 1
		}
	}
	return string(masked)
}

func mutationOccurrenceIsAnalytic(text string, start, end int) bool {
	if mutationTermIsNegated(text[:start]) {
		return true
	}
	if mutationTermIsCommand(text[:start]) {
		return false
	}
	previous := previousPolicyWord(text[:start])
	if previous == "that" || previous == "which" || previous == "who" || previous == "they" ||
		previous == "что" || strings.HasPrefix(previous, "котор") {
		return true
	}
	if analyticMutationFollower(nextPolicyWord(text[end:])) {
		return true
	}
	word := policyWordAround(text, start, end)
	for _, prefix := range []string{
		"добавлени", "изменени", "исправлени", "обновлени", "перемещени",
		"реализац", "рефакторинг", "создани", "удалени",
	} {
		if strings.HasPrefix(word, prefix) {
			return true
		}
	}
	return false
}

func mutationTermIsNegated(prefix string) bool {
	prefix = strings.TrimSpace(prefix)
	for _, phrase := range []string{
		"do not", "don't", "never", "not to", "without", "не", "не надо", "не нужно", "никогда не", "без",
	} {
		if prefix == phrase || strings.HasSuffix(prefix, " "+phrase) {
			return true
		}
	}
	return false
}

func mutationTermIsCommand(prefix string) bool {
	clauseStart := strings.LastIndexAny(prefix, ".!?;\n") + 1
	clause := strings.TrimSpace(prefix[clauseStart:])
	if clause == "" {
		return true
	}
	for _, phrase := range []string{
		"and", "and then", "then", "also", "please", "please also", "to", "must", "should",
		"need to", "needs to", "have to", "can you", "could you", "would you", "want you to", "let's",
		"и", "а затем", "затем", "потом", "также", "пожалуйста", "нужно", "надо", "следует",
		"можешь", "можно", "давай",
	} {
		if clause == phrase || strings.HasSuffix(clause, " "+phrase) {
			return true
		}
	}
	return false
}

func previousPolicyWord(prefix string) string {
	fields := strings.Fields(prefix)
	if len(fields) == 0 {
		return ""
	}
	return trimPolicyWord(fields[len(fields)-1])
}

func nextPolicyWord(suffix string) string {
	fields := strings.Fields(suffix)
	if len(fields) == 0 {
		return ""
	}
	return trimPolicyWord(fields[0])
}

func trimPolicyWord(value string) string {
	return strings.Trim(value, " \t\r\n`'\"()[]{}<>,:;.!?")
}

func policyWordAround(text string, start, end int) string {
	for start > 0 {
		value, size := utf8.DecodeLastRuneInString(text[:start])
		if !isWordRune(value) {
			break
		}
		start -= size
	}
	for end < len(text) {
		value, size := utf8.DecodeRuneInString(text[end:])
		if !isWordRune(value) {
			break
		}
		end += size
	}
	return text[start:end]
}

func analyticMutationFollower(word string) bool {
	switch word {
	case "call", "calls", "commit", "commits", "count", "counts", "distribution", "distributions",
		"event", "events", "frequencies", "frequency", "histories", "history", "log", "logs",
		"pattern", "patterns", "rate", "rates", "record", "records", "reference", "references",
		"statistics", "stats", "usage", "usages":
		return true
	default:
		return false
	}
}

var explicitREPLTerms = []string{
	"repl_exec", "use repl", "python repl", "python session", "hybrid engine",
	"используй repl", "через repl", "python-сесси", "гибридн",
}

// Stems intentionally favor precision over recall. A false negative leaves all
// ordinary tools available; a false positive taxes every request with another
// declaration and invites an unnecessary model/tool round trip.
var aggregationTerms = []string{
	"how many", "number of", "count", "counts", "rank", "ranking", "top ", "bottom ",
	"largest", "smallest", "biggest", "longest", "shortest",
	"most common", "most frequent", "least common", "least frequent", "fewest",
	"distribution", "percentage", "percent", "fraction", "group by", "grouped by", "per package",
	"per directory", "per folder", "per file", "average", "mean", "median", "histogram",
	"frequency", "frequencies", "percentile", "quantile", "p50", "p90", "p95", "p99",
	"breakdown", "summary by",
	"dedup", "duplicate", "unique",
	"never mentioned", "not mentioned", "never referenced", "not referenced", "unreferenced", "orphaned",
	"lack test", "lack tests", "lacks test", "lacks tests", "lacking test", "lacking tests",
	"absent from test", "absent from tests", "missing test coverage", "missing coverage", "uncovered",
	"not covered by test", "not covered by tests", "without test coverage",
	"unused exported", "unused public", "zero references",
	"common to every", "common to all", "shared by every", "shared by all",
	"present in every", "present in all", "occurs in every", "occurs in all", "intersection",
	"no tests", "all matches", "statistics", "aggregate",
	"сколько", "количеств", "доля", "процент", "ранж", "рейтинг", "топ ",
	"больше всего", "меньше всего", "наибольш", "наименьш", "число", "посчитай", "подсчитай",
	"сосчитай", "статист", "распредел", "сгрупп", "группиров", "по пакетам", "по директориям",
	"по папкам", "по файлам", "средн", "медиан", "гистограмм", "частот", "перцентил", "квантил",
	"сводк", "дубликат", "уникальн", "не упомина", "не использу", "без тест", "ни разу",
	"самые большие", "самые маленькие", "не покрыт", "общие для всех", "пересечени",
}

var relationshipTerms = []string{
	"compare", "comparison", "cross-file", "between", "correlation", "co-occurrence",
	"dependency graph", "call graph", "join", "сравн", "между", "корреляц", "зависимост",
	"граф вызов", "связи",
}

var crossFileSetTerms = []string{
	"never mentioned", "not mentioned", "never referenced", "not referenced", "unreferenced", "orphaned",
	"lack test", "lack tests", "lacks test", "lacks tests", "lacking test", "lacking tests",
	"absent from test", "absent from tests", "missing test coverage", "missing coverage", "uncovered",
	"not covered by test", "not covered by tests", "without test coverage",
	"unused exported", "unused public", "zero references",
	"common to every", "common to all", "shared by every", "shared by all",
	"present in every", "present in all", "occurs in every", "occurs in all", "intersection",
	"не упомина", "не использу", "без тест", "ни разу", "не покрыт", "общие для всех", "пересечени",
}

var collectionTerms = []string{
	"repository", "codebase", "workspace", "files", "file", "directories",
	"directory", "folders", "packages", "modules", "functions", "methods", "symbols", "tests", "commits",
	"logs", "rows", "records", "jsonl", "csv", "dataset",
	"репозитор", "кодовой баз", "воркспейс", "файл", "директор", "папк", "пакет",
	"функц", "символ", "тест", "коммит", "логи", "логов", "строк", "запис", "датасет",
}

var broadScopeTerms = []string{
	"repository", "codebase", "workspace", "dataset", "across", "cross-file",
	"all", "every", "whole", "many", "directories", "folders", "packages", "modules", "commits",
	"logs", "rows", "records", "jsonl", "csv",
	"per file", "per directory", "per folder", "per package", "per module",
	"репозитор", "кодовой баз", "воркспейс", "датасет", "по всем", "во всех", "все ",
	"кажд", "много", "директор", "папк", "пакет", "коммит", "логи", "логов", "запис",
	"по файлам", "по директориям", "по папкам", "по пакетам", "по модулям",
}

var singleTargetTerms = []string{
	"this file", "one file", "single file", "this function", "one function", "single function",
	"this method", "one method", "single method", "this symbol", "one symbol", "single symbol",
	"this class", "one class", "single class", "this type", "one type", "single type",
	"this package", "one package", "single package", "this module", "one module", "single module",
	"этом файл", "одном файл", "один файл", "этой функц", "одной функц", "одну функц",
	"этом метод", "одном метод", "этом символ", "одном символ", "этом класс", "одном класс",
	"этом тип", "одном тип", "этом пакет", "одном пакет", "этом модул", "одном модул",
}

var smallScopeTerms = []string{
	"two", "2", "both", "these two", "between two",
	"два ", "две ", "оба ", "обе ", "эти два", "эти две", "между двумя",
}

var boundedCollectionTerms = []string{
	"these two", "both", "between two", "эти два", "эти две", "оба", "обе", "между двумя",
}

var exhaustiveScopeTerms = []string{
	"across all", "all usages", "all references", "every file", "every commit", "whole repository",
	"по всем", "во всех", "каждом файл", "всему репозитор", "всех коммит",
}

var mutationTerms = []string{
	"fix", "implement", "add", "remove", "delete", "rename", "replace", "refactor", "update",
	"modify", "edit", "write", "create", "change", "move", "migrate", "rewrite", "convert", "generate",
	"исправ", "реализ", "добав", "удал", "переимен", "замен", "рефактор", "обнов", "измен",
	"отредакт", "созд", "перемест", "мигрир", "перепиш", "конвертир", "сгенерир", "почин",
}

var cyrillicMutationTerms = []string{
	"исправ", "реализ", "добав", "удал", "переимен", "замен", "рефактор", "обнов", "измен",
	"отредакт", "созд", "перемест", "мигрир", "перепиш", "конвертир", "сгенерир", "почин",
}

var nonMutationPhrases = []string{
	"do not modify", "don't modify", "do not edit", "don't edit", "without modifying", "without editing",
	"do not change", "don't change", "without changing", "do not move", "don't move",
	"leave files unchanged", "must remain unchanged", "не изменяй", "не изменять", "не меняй", "не менять",
	"не редактируй", "не редактировать", "ничего не изменяй", "оставь файлы без изменений", "без изменений",
}
