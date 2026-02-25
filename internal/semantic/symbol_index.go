package semantic

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

type fileSymbolIndex struct {
	Definitions map[string]int
	Usages      map[string]int
	Callers     map[string]int
}

func (f fileSymbolIndex) clone() fileSymbolIndex {
	out := fileSymbolIndex{
		Definitions: make(map[string]int, len(f.Definitions)),
		Usages:      make(map[string]int, len(f.Usages)),
		Callers:     make(map[string]int, len(f.Callers)),
	}
	for k, v := range f.Definitions {
		out.Definitions[k] = v
	}
	for k, v := range f.Usages {
		out.Usages[k] = v
	}
	for k, v := range f.Callers {
		out.Callers[k] = v
	}
	return out
}

var (
	goFuncDefRe     = regexp.MustCompile(`(?m)^\s*func\s+(?:\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	goTypeDefRe     = regexp.MustCompile(`(?m)^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\s+`)
	goVarDefRe      = regexp.MustCompile(`(?m)^\s*(?:var|const)\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	pyDefRe         = regexp.MustCompile(`(?m)^\s*(?:def|class)\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	jsFuncDefRe     = regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	jsClassDefRe    = regexp.MustCompile(`(?m)^\s*(?:export\s+)?class\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	jsVarDefRe      = regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	javaTypeDefRe   = regexp.MustCompile(`(?m)^\s*(?:public|private|protected)?\s*(?:abstract|final)?\s*(?:class|interface|enum)\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	javaMethodDefRe = regexp.MustCompile(`(?m)^\s*(?:public|private|protected)?\s*(?:static\s+)?(?:[\w<>\[\],]+\s+)+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	callerRe        = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	identifierRe    = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\b`)
)

func extractFileSymbols(filePath, content string) fileSymbolIndex {
	index := fileSymbolIndex{
		Definitions: make(map[string]int),
		Usages:      make(map[string]int),
		Callers:     make(map[string]int),
	}
	if strings.TrimSpace(content) == "" {
		return index
	}

	lang := DetectLanguage(filePath)
	definitionMatchers := definitionMatchersForLanguage(lang)

	for _, re := range definitionMatchers {
		for _, m := range re.FindAllStringSubmatch(content, -1) {
			if len(m) < 2 {
				continue
			}
			name := normalizeSymbolName(m[1])
			if name == "" || isKeyword(name, lang) {
				continue
			}
			index.Definitions[name]++
		}
	}

	for _, m := range callerRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 2 {
			continue
		}
		name := normalizeSymbolName(m[1])
		if name == "" || isKeyword(name, lang) {
			continue
		}
		index.Callers[name]++
	}

	for _, m := range identifierRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 2 {
			continue
		}
		name := normalizeSymbolName(m[1])
		if name == "" || isKeyword(name, lang) {
			continue
		}
		index.Usages[name]++
	}

	// Thin out low-signal usage noise.
	for symbol, count := range index.Usages {
		if count <= 1 && index.Definitions[symbol] == 0 && index.Callers[symbol] == 0 {
			delete(index.Usages, symbol)
		}
	}

	return index
}

func definitionMatchersForLanguage(lang string) []*regexp.Regexp {
	switch lang {
	case "go":
		return []*regexp.Regexp{goFuncDefRe, goTypeDefRe, goVarDefRe}
	case "python":
		return []*regexp.Regexp{pyDefRe}
	case "javascript", "typescript":
		return []*regexp.Regexp{jsFuncDefRe, jsClassDefRe, jsVarDefRe}
	case "java":
		return []*regexp.Regexp{javaTypeDefRe, javaMethodDefRe}
	default:
		ext := strings.ToLower(filepath.Ext(lang))
		if ext == ".go" {
			return []*regexp.Regexp{goFuncDefRe, goTypeDefRe, goVarDefRe}
		}
		return nil
	}
}

func normalizeSymbolName(symbol string) string {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range symbol {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return strings.TrimSpace(b.String())
}

func isKeyword(symbol, lang string) bool {
	if symbol == "" {
		return true
	}
	keywords := map[string]map[string]struct{}{
		"go": {
			"if": {}, "for": {}, "switch": {}, "case": {}, "default": {}, "func": {}, "return": {},
			"type": {}, "var": {}, "const": {}, "package": {}, "import": {}, "range": {}, "defer": {},
		},
		"python": {
			"if": {}, "for": {}, "while": {}, "def": {}, "class": {}, "return": {}, "import": {},
			"from": {}, "with": {}, "as": {}, "try": {}, "except": {}, "lambda": {},
		},
		"javascript": {
			"if": {}, "for": {}, "while": {}, "function": {}, "class": {}, "return": {}, "import": {},
			"from": {}, "const": {}, "let": {}, "var": {}, "new": {}, "switch": {},
		},
		"typescript": {
			"if": {}, "for": {}, "while": {}, "function": {}, "class": {}, "return": {}, "import": {},
			"from": {}, "const": {}, "let": {}, "var": {}, "new": {}, "switch": {}, "type": {}, "interface": {},
		},
		"java": {
			"if": {}, "for": {}, "while": {}, "switch": {}, "class": {}, "interface": {}, "enum": {},
			"return": {}, "import": {}, "package": {}, "new": {}, "public": {}, "private": {}, "protected": {},
		},
	}
	if byLang, ok := keywords[lang]; ok {
		_, exists := byLang[symbol]
		return exists
	}
	return false
}
