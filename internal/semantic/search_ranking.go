package semantic

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	maxDepsPerImport      = 20
	gitProbeTimeout       = 1500 * time.Millisecond
	dependencyDirectHit   = 0.75
	directoryProximity    = 0.45
	sameBasenameProx      = 0.30
	pathHintExactBoost    = 1.00
	pathHintContainsBoost = 0.75
	pathHintBaseBoost     = 0.55
)

var (
	jsImportRe     = regexp.MustCompile(`(?m)^\s*import(?:.+?\s+from\s+)?['"]([^'"]+)['"]`)
	jsRequireRe    = regexp.MustCompile(`require\(\s*['"]([^'"]+)['"]\s*\)`)
	pythonImportRe = regexp.MustCompile(`(?m)^\s*(?:from\s+([.\w]+)\s+import|import\s+([.\w]+))`)
	javaImportRe   = regexp.MustCompile(`(?m)^\s*import\s+([\w.]+);`)
	goModuleRe     = regexp.MustCompile(`(?m)^\s*module\s+([^\s]+)\s*$`)
)

type searchQuerySignals struct {
	RawLower    string
	Normalized  string
	Tokens      []string
	TokenSet    map[string]struct{}
	PathHints   []string
	SymbolHints []string
}

type fileSearchSignals struct {
	PathHintScore   float64
	DependencyScore float64
	FreshnessScore  float64
	ChangeProximity float64
	SymbolIndexScore float64

	DependencyDegree int
	DependentDegree  int
	DefinitionHits   int
	CallerHits       int
	UsageHits        int

	ChangedFileDirect bool
}

type dependencyGraphSignals struct {
	Deps       map[string]map[string]struct{}
	Dependents map[string]map[string]struct{}
	MaxDegree  int
}

type symbolHitStats struct {
	DefinitionHits int
	CallerHits     int
	UsageHits      int
}

type symbolGraphScoreSignals struct {
	Direct map[string]float64
	Hop    map[string]float64
	Stats  map[string]symbolHitStats
}

type searchSignals struct {
	Query       searchQuerySignals
	FileSignals map[string]fileSearchSignals
}

func buildSearchSignals(
	ctx context.Context,
	workDir,
	query string,
	chunksByFile map[string][]ChunkInfo,
	symbolsByFile map[string]fileSymbolIndex,
) searchSignals {
	querySignals := buildSearchQuerySignals(query)

	files := make([]string, 0, len(chunksByFile))
	for filePath := range chunksByFile {
		files = append(files, filepath.Clean(filePath))
	}

	depSignals := buildLocalDependencyGraph(workDir, files)
	symbolGraphScores := computeSymbolGraphScores(querySignals, depSignals, files, symbolsByFile)
	changedRel := discoverGitChangedPaths(ctx, workDir)
	changedDirs := make(map[string]struct{}, len(changedRel))
	changedBase := make(map[string]struct{}, len(changedRel))
	for rel := range changedRel {
		dir := normalizeRelPath(filepath.Dir(rel))
		changedDirs[dir] = struct{}{}
		if base := strings.ToLower(filepath.Base(rel)); base != "" {
			changedBase[base] = struct{}{}
		}
	}

	perFile := make(map[string]fileSearchSignals, len(files))
	for _, filePath := range files {
		rel := normalizeRelPath(relativeToWorkDir(workDir, filePath))
		outDeg := len(depSignals.Deps[filePath])
		inDeg := len(depSignals.Dependents[filePath])
		degree := outDeg + inDeg

		dependencyScore := 0.0
		if depSignals.MaxDegree > 0 {
			dependencyScore = float64(degree) / float64(depSignals.MaxDegree)
		}

		changeScore, direct := computeChangeProximity(workDir, filePath, rel, depSignals, changedRel, changedDirs, changedBase)
		directSymbolScore := symbolGraphScores.Direct[filePath]
		hopScore := symbolGraphScores.Hop[filePath]
		symbolIndexScore := clamp01(0.70*directSymbolScore + 0.30*hopScore)
		stats := symbolGraphScores.Stats[filePath]

		perFile[filePath] = fileSearchSignals{
			PathHintScore:     pathHintRelevance(querySignals, rel),
			DependencyScore:   dependencyScore,
			FreshnessScore:    fileFreshnessScore(filePath),
			ChangeProximity:   changeScore,
			SymbolIndexScore:  symbolIndexScore,
			DependencyDegree:  outDeg,
			DependentDegree:   inDeg,
			DefinitionHits:    stats.DefinitionHits,
			CallerHits:        stats.CallerHits,
			UsageHits:         stats.UsageHits,
			ChangedFileDirect: direct,
		}
	}

	return searchSignals{
		Query:       querySignals,
		FileSignals: perFile,
	}
}

func buildSearchQuerySignals(query string) searchQuerySignals {
	query = strings.TrimSpace(query)
	normalized := normalizeSearchText(query)

	seen := make(map[string]bool)
	tokens := make([]string, 0)
	pathHints := make([]string, 0)
	symbolHints := make([]string, 0)

	for _, tok := range strings.Fields(normalized) {
		if len(tok) < 2 {
			continue
		}
		if !seen[tok] {
			seen[tok] = true
			tokens = append(tokens, tok)
		}
		if looksLikePathHint(tok) {
			pathHints = append(pathHints, tok)
		}
		if looksLikeSymbolHint(tok) {
			symbolHints = append(symbolHints, tok)
		}
	}

	if len(pathHints) == 0 {
		// Fallback: short explicit file markers from raw query.
		for _, raw := range strings.Fields(strings.ToLower(query)) {
			raw = normalizeRelPath(raw)
			if raw == "" || !looksLikePathHint(raw) {
				continue
			}
			pathHints = append(pathHints, raw)
		}
	}

	// Preserve case-sensitive symbol hints from raw query text (normalized text is lowercase).
	for _, raw := range splitRawQueryTokens(query) {
		if looksLikeSymbolHint(raw) {
			symbolHints = append(symbolHints, strings.ToLower(raw))
		}
	}

	tokenSet := make(map[string]struct{}, len(tokens))
	for _, tok := range tokens {
		tokenSet[tok] = struct{}{}
	}

	return searchQuerySignals{
		RawLower:    strings.ToLower(query),
		Normalized:  normalized,
		Tokens:      tokens,
		TokenSet:    tokenSet,
		PathHints:   uniqueStrings(pathHints),
		SymbolHints: uniqueStrings(symbolHints),
	}
}

func normalizeSearchText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '/' || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

func lexicalRelevance(q searchQuerySignals, content string) float64 {
	if len(q.Tokens) == 0 {
		return 0
	}
	normalized := normalizeSearchText(content)
	if normalized == "" {
		return 0
	}

	hits := 0
	lengthWeightedHits := 0
	for _, tok := range q.Tokens {
		if strings.Contains(normalized, tok) {
			hits++
			lengthWeightedHits += minInt(len(tok), 10)
		}
	}

	base := float64(hits) / float64(len(q.Tokens))
	weighted := float64(lengthWeightedHits) / float64(len(q.Tokens)*10)
	score := 0.7*base + 0.3*weighted

	if len(q.Normalized) >= 10 && strings.Contains(normalized, q.Normalized) {
		score += 0.20
	}
	if score > 1 {
		score = 1
	}
	return score
}

func symbolHintBonus(q searchQuerySignals, content string) float64 {
	if len(q.SymbolHints) == 0 {
		return 0
	}
	lower := strings.ToLower(content)
	bonus := 0.0
	for _, hint := range q.SymbolHints {
		if strings.Contains(lower, hint) {
			bonus += 0.015
		}
	}
	if bonus > 0.08 {
		bonus = 0.08
	}
	return bonus
}

func pathHintRelevance(q searchQuerySignals, relPath string) float64 {
	if len(q.PathHints) == 0 {
		return 0
	}
	rel := strings.ToLower(normalizeRelPath(relPath))
	base := strings.ToLower(filepath.Base(rel))
	score := 0.0
	for _, hint := range q.PathHints {
		h := strings.ToLower(normalizeRelPath(hint))
		if h == "" {
			continue
		}
		switch {
		case rel == h || strings.HasSuffix(rel, "/"+h):
			score += pathHintExactBoost
		case strings.Contains(rel, h):
			score += pathHintContainsBoost
		case strings.Contains(base, h):
			score += pathHintBaseBoost
		}
	}
	if score > 1 {
		score = 1
	}
	return score
}

func fileFreshnessScore(filePath string) float64 {
	info, err := os.Stat(filePath)
	if err != nil {
		return 0
	}
	ageHours := time.Since(info.ModTime()).Hours()
	if ageHours < 0 {
		ageHours = 0
	}
	// Recent files get a moderate boost; old files decay smoothly.
	return math.Exp(-ageHours / (24 * 45))
}

func buildLocalDependencyGraph(workDir string, files []string) dependencyGraphSignals {
	graph := dependencyGraphSignals{
		Deps:       make(map[string]map[string]struct{}, len(files)),
		Dependents: make(map[string]map[string]struct{}, len(files)),
	}
	if len(files) == 0 {
		return graph
	}

	relToAbs := make(map[string]string, len(files))
	dirToFiles := make(map[string][]string)
	for _, filePath := range files {
		abs := filepath.Clean(filePath)
		rel := normalizeRelPath(relativeToWorkDir(workDir, abs))
		relToAbs[rel] = abs
		dir := normalizeRelPath(filepath.Dir(rel))
		dirToFiles[dir] = append(dirToFiles[dir], abs)
		graph.Deps[abs] = make(map[string]struct{})
		graph.Dependents[abs] = make(map[string]struct{})
	}

	modulePath := readGoModulePath(workDir)

	for _, filePath := range files {
		abs := filepath.Clean(filePath)
		content, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		lang := DetectLanguage(abs)
		imports := extractImportPaths(string(content), lang)
		if len(imports) == 0 {
			continue
		}

		sourceRel := normalizeRelPath(relativeToWorkDir(workDir, abs))
		targets := resolveLocalImportTargets(sourceRel, lang, imports, relToAbs, dirToFiles, modulePath)
		for target := range targets {
			if target == abs {
				continue
			}
			graph.Deps[abs][target] = struct{}{}
			graph.Dependents[target][abs] = struct{}{}
		}
	}

	maxDegree := 0
	for _, filePath := range files {
		abs := filepath.Clean(filePath)
		degree := len(graph.Deps[abs]) + len(graph.Dependents[abs])
		if degree > maxDegree {
			maxDegree = degree
		}
	}
	graph.MaxDegree = maxDegree
	return graph
}

func extractImportPaths(content, lang string) []string {
	switch lang {
	case "go":
		return extractGoImports(content)
	case "javascript", "typescript":
		var imports []string
		for _, m := range jsImportRe.FindAllStringSubmatch(content, -1) {
			if len(m) > 1 {
				imports = append(imports, strings.TrimSpace(m[1]))
			}
		}
		for _, m := range jsRequireRe.FindAllStringSubmatch(content, -1) {
			if len(m) > 1 {
				imports = append(imports, strings.TrimSpace(m[1]))
			}
		}
		return uniqueStrings(imports)
	case "python":
		var imports []string
		for _, m := range pythonImportRe.FindAllStringSubmatch(content, -1) {
			for _, group := range m[1:] {
				group = strings.TrimSpace(group)
				if group != "" {
					imports = append(imports, group)
					break
				}
			}
		}
		return uniqueStrings(imports)
	case "java":
		var imports []string
		for _, m := range javaImportRe.FindAllStringSubmatch(content, -1) {
			if len(m) > 1 {
				imports = append(imports, strings.TrimSpace(m[1]))
			}
		}
		return uniqueStrings(imports)
	default:
		return nil
	}
}

func extractGoImports(content string) []string {
	lines := strings.Split(content, "\n")
	var imports []string
	inBlock := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "import (") {
			inBlock = true
			continue
		}
		if inBlock {
			if strings.HasPrefix(line, ")") {
				inBlock = false
				continue
			}
			if q := firstQuotedString(line); q != "" {
				imports = append(imports, q)
			}
			continue
		}
		if strings.HasPrefix(line, "import ") {
			if q := firstQuotedString(line); q != "" {
				imports = append(imports, q)
			}
		}
	}
	return uniqueStrings(imports)
}

func firstQuotedString(s string) string {
	start := strings.IndexByte(s, '"')
	if start < 0 {
		return ""
	}
	end := strings.IndexByte(s[start+1:], '"')
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(s[start+1 : start+1+end])
}

func resolveLocalImportTargets(
	sourceRel string,
	lang string,
	imports []string,
	relToAbs map[string]string,
	dirToFiles map[string][]string,
	goModulePath string,
) map[string]struct{} {
	out := make(map[string]struct{})
	sourceDir := normalizeRelPath(path.Dir(filepath.ToSlash(sourceRel)))

	addFile := func(rel string) {
		rel = normalizeRelPath(rel)
		if rel == "" {
			return
		}
		if abs, ok := relToAbs[rel]; ok {
			out[abs] = struct{}{}
		}
	}
	addDir := func(relDir string) {
		relDir = normalizeRelPath(relDir)
		if files, ok := dirToFiles[relDir]; ok {
			for i, abs := range files {
				if i >= maxDepsPerImport {
					break
				}
				out[abs] = struct{}{}
			}
		}
	}
	addJSImportTargets := func(target string) {
		target = normalizeRelPath(target)
		if target == "" {
			return
		}
		ext := strings.ToLower(filepath.Ext(target))
		if ext != "" {
			addFile(target)
		} else {
			for _, e := range []string{".ts", ".tsx", ".js", ".jsx", ".go", ".py", ".java", ".rs"} {
				addFile(target + e)
			}
			for _, e := range []string{".ts", ".tsx", ".js", ".jsx"} {
				addFile(path.Join(target, "index"+e))
			}
		}
		addDir(target)
	}

	for _, imp := range imports {
		imp = strings.TrimSpace(imp)
		if imp == "" {
			continue
		}

		switch lang {
		case "go":
			switch {
			case strings.HasPrefix(imp, "./") || strings.HasPrefix(imp, "../"):
				rel := normalizeRelPath(path.Clean(path.Join(sourceDir, imp)))
				addDir(rel)
			case goModulePath != "" && (imp == goModulePath || strings.HasPrefix(imp, goModulePath+"/")):
				rel := strings.TrimPrefix(imp, goModulePath)
				rel = strings.TrimPrefix(rel, "/")
				addDir(rel)
			}
		case "javascript", "typescript":
			if strings.HasPrefix(imp, "./") || strings.HasPrefix(imp, "../") {
				rel := normalizeRelPath(path.Clean(path.Join(sourceDir, imp)))
				addJSImportTargets(rel)
			}
		case "python":
			if strings.HasPrefix(imp, ".") {
				trimmed := strings.TrimLeft(imp, ".")
				rel := normalizeRelPath(path.Clean(path.Join(sourceDir, strings.ReplaceAll(trimmed, ".", "/"))))
				addDir(rel)
			} else {
				addDir(strings.ReplaceAll(imp, ".", "/"))
			}
		case "java":
			addDir(strings.ReplaceAll(imp, ".", "/"))
		default:
			if strings.HasPrefix(imp, "./") || strings.HasPrefix(imp, "../") {
				rel := normalizeRelPath(path.Clean(path.Join(sourceDir, imp)))
				addDir(rel)
			}
		}
	}

	return out
}

func readGoModulePath(workDir string) string {
	data, err := os.ReadFile(filepath.Join(workDir, "go.mod"))
	if err != nil {
		return ""
	}
	m := goModuleRe.FindStringSubmatch(string(data))
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func discoverGitChangedPaths(ctx context.Context, workDir string) map[string]struct{} {
	changed := make(map[string]struct{})
	if workDir == "" {
		return changed
	}
	if !isGitRepo(ctx, workDir) {
		return changed
	}

	commands := [][]string{
		{"diff", "--name-only", "--relative"},
		{"diff", "--cached", "--name-only", "--relative"},
		{"ls-files", "--others", "--exclude-standard"},
	}

	for _, args := range commands {
		lines := runGitLines(ctx, workDir, args...)
		for _, line := range lines {
			rel := normalizeRelPath(line)
			if rel != "" {
				changed[rel] = struct{}{}
			}
		}
	}
	return changed
}

func isGitRepo(ctx context.Context, workDir string) bool {
	lines := runGitLines(ctx, workDir, "rev-parse", "--is-inside-work-tree")
	return len(lines) > 0 && strings.EqualFold(strings.TrimSpace(lines[0]), "true")
}

func runGitLines(ctx context.Context, workDir string, args ...string) []string {
	if len(args) == 0 {
		return nil
	}

	cmdCtx, cancel := context.WithTimeout(ctx, gitProbeTimeout)
	defer cancel()

	cmdArgs := append([]string{"-C", workDir}, args...)
	cmd := exec.CommandContext(cmdCtx, "git", cmdArgs...)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func computeChangeProximity(
	workDir string,
	filePath string,
	relPath string,
	deps dependencyGraphSignals,
	changed map[string]struct{},
	changedDirs map[string]struct{},
	changedBase map[string]struct{},
) (float64, bool) {
	if len(changed) == 0 {
		return 0, false
	}

	rel := normalizeRelPath(relPath)
	if _, ok := changed[rel]; ok {
		return 1.0, true
	}

	if neighbors, ok := deps.Deps[filePath]; ok {
		for dep := range neighbors {
			depRel := normalizeRelPath(relativeToWorkDir(workDir, dep))
			if _, hit := changed[depRel]; hit {
				return dependencyDirectHit, false
			}
		}
	}
	if neighbors, ok := deps.Dependents[filePath]; ok {
		for dep := range neighbors {
			depRel := normalizeRelPath(relativeToWorkDir(workDir, dep))
			if _, hit := changed[depRel]; hit {
				return dependencyDirectHit, false
			}
		}
	}

	if _, ok := changedDirs[normalizeRelPath(filepath.Dir(rel))]; ok {
		return directoryProximity, false
	}
	if _, ok := changedBase[strings.ToLower(filepath.Base(rel))]; ok {
		return sameBasenameProx, false
	}
	return 0, false
}

func deduplicateSearchResults(results SearchResults, topK int) []SearchResult {
	if len(results) == 0 {
		return nil
	}
	if topK <= 0 {
		topK = len(results)
	}

	deduped := make([]SearchResult, 0, minInt(topK, len(results)))
	for _, candidate := range results {
		duplicate := false
		for _, existing := range deduped {
			if isNearDuplicateResult(candidate, existing) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		deduped = append(deduped, candidate)
		if len(deduped) >= topK {
			break
		}
	}
	return deduped
}

func isNearDuplicateResult(a, b SearchResult) bool {
	if filepath.Clean(a.FilePath) != filepath.Clean(b.FilePath) {
		return false
	}
	if lineOverlapRatio(a.LineStart, a.LineEnd, b.LineStart, b.LineEnd) >= 0.55 {
		return true
	}
	if absInt(a.LineStart-b.LineStart) <= 8 && absInt(a.LineEnd-b.LineEnd) <= 8 {
		return true
	}
	aNorm := normalizeSearchText(a.Content)
	bNorm := normalizeSearchText(b.Content)
	if aNorm == bNorm {
		return true
	}
	return tokenSetJaccard(aNorm, bNorm) >= 0.92
}

func lineOverlapRatio(aStart, aEnd, bStart, bEnd int) float64 {
	start := maxInt(aStart, bStart)
	end := minInt(aEnd, bEnd)
	if end < start {
		return 0
	}
	overlap := end - start + 1
	lenA := maxInt(1, aEnd-aStart+1)
	lenB := maxInt(1, bEnd-bStart+1)
	den := minInt(lenA, lenB)
	return float64(overlap) / float64(den)
}

func tokenSetJaccard(a, b string) float64 {
	aSet := make(map[string]struct{})
	for _, tok := range strings.Fields(a) {
		if len(tok) >= 3 {
			aSet[tok] = struct{}{}
		}
	}
	bSet := make(map[string]struct{})
	for _, tok := range strings.Fields(b) {
		if len(tok) >= 3 {
			bSet[tok] = struct{}{}
		}
	}
	if len(aSet) == 0 && len(bSet) == 0 {
		return 1
	}
	inter := 0
	union := len(aSet)
	for tok := range bSet {
		if _, ok := aSet[tok]; ok {
			inter++
		} else {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func computeSymbolGraphScores(
	query searchQuerySignals,
	deps dependencyGraphSignals,
	files []string,
	symbolsByFile map[string]fileSymbolIndex,
) symbolGraphScoreSignals {
	signals := symbolGraphScoreSignals{
		Direct: make(map[string]float64, len(files)),
		Hop:    make(map[string]float64, len(files)),
		Stats:  make(map[string]symbolHitStats, len(files)),
	}
	if len(files) == 0 || len(query.SymbolHints) == 0 {
		return signals
	}

	for _, filePath := range files {
		idx, ok := symbolsByFile[filePath]
		if !ok {
			continue
		}
		score := 0.0
		stats := symbolHitStats{}
		for _, rawHint := range query.SymbolHints {
			hint := normalizeSymbolName(rawHint)
			if hint == "" {
				continue
			}
			if n := idx.Definitions[hint]; n > 0 {
				score += 0.70 + 0.03*float64(minInt(n, 5)-1)
				stats.DefinitionHits += n
			}
			if n := idx.Callers[hint]; n > 0 {
				score += 0.55 + 0.02*float64(minInt(n, 6)-1)
				stats.CallerHits += n
			}
			if n := idx.Usages[hint]; n > 0 {
				score += 0.28 + 0.01*float64(minInt(n, 8)-1)
				stats.UsageHits += n
			}
		}
		if score > 0 {
			signals.Direct[filePath] = clamp01(score)
			signals.Stats[filePath] = stats
		}
	}

	// Spread relevance over local dependency neighborhood (1-2 hops).
	for filePath, baseScore := range signals.Direct {
		if baseScore <= 0 {
			continue
		}

		seen := make(map[string]struct{})
		frontier := []string{filePath}
		seen[filePath] = struct{}{}

		for hop := 1; hop <= 2; hop++ {
			next := make([]string, 0)
			weight := 0.22
			if hop == 2 {
				weight = 0.12
			}
			for _, node := range frontier {
				neighbors := mergeNeighborKeys(deps.Deps[node], deps.Dependents[node])
				for _, nb := range neighbors {
					if _, ok := seen[nb]; ok {
						continue
					}
					seen[nb] = struct{}{}
					next = append(next, nb)
					signals.Hop[nb] = clamp01(signals.Hop[nb] + weight*baseScore)
				}
			}
			if len(next) == 0 {
				break
			}
			frontier = next
		}
	}

	return signals
}

func mergeNeighborKeys(a, b map[string]struct{}) []string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make([]string, 0, len(a)+len(b))
	seen := make(map[string]struct{}, len(a)+len(b))
	for key := range a {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	for key := range b {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func looksLikePathHint(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	if strings.Contains(token, "/") {
		return true
	}
	ext := strings.ToLower(filepath.Ext(token))
	return ext != "" && len(ext) <= 6
}

func looksLikeSymbolHint(token string) bool {
	if len(token) < 3 {
		return false
	}
	// Symbols often have mixed-case/underscores unlike plain sentence tokens.
	hasUnderscore := strings.Contains(token, "_")
	hasUpper := false
	for _, r := range token {
		if unicode.IsUpper(r) {
			hasUpper = true
			break
		}
	}
	return hasUnderscore || hasUpper
}

func splitRawQueryTokens(query string) []string {
	parts := strings.FieldsFunc(query, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '/')
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func normalizeRelPath(rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return ""
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	rel = strings.TrimPrefix(rel, "./")
	rel = strings.TrimPrefix(rel, "/")
	if rel == "." {
		return ""
	}
	return rel
}

func relativeToWorkDir(workDir, target string) string {
	if workDir == "" {
		return normalizeRelPath(target)
	}
	rel, err := filepath.Rel(workDir, target)
	if err != nil {
		return normalizeRelPath(target)
	}
	return normalizeRelPath(rel)
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		key := strings.ToLower(strings.TrimSpace(v))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
