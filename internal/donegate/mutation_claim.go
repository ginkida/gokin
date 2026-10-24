package donegate

import (
	"encoding/json"
	"regexp"
	"strings"
)

var mutationClaimClause = regexp.MustCompile(`[.!?]\s+|[;\n]`)
var completedMutationClaim = regexp.MustCompile(`(?i)\b(added|updated|changed|edited|modified|created|removed|deleted|inserted|implemented|wrote|fixed|replaced|renamed)\b|\bnow\s+(has|contains|includes|reads)\b|\bis\s+(?:in\s+place|now)\b|(?:^|[^а-яё])(?:добавил[аи]?|добавлен[аоы]?|изменил[аи]?|измен[её]н[аоы]?|обновил[аи]?|обновл[её]н[аоы]?|исправил[аи]?|исправлен[аоы]?|создал[аи]?|создан[аоы]?|удалил[аи]?|удал[её]н[аоы]?)(?:$|[^а-яё])|теперь\s+(есть|содержит)|(?:^|[^а-яё])на\s+месте(?:$|[^а-яё])`)
var nonMutationClaim = regexp.MustCompile(`(?i)\b(will|would|could|should|might|plan|planned|proposed|expected|whether|previous|previously|earlier|already|todo|checklist|explanation|analysis)\b|\bneed to\b|без изменений|изменений не|планир|план[:\s]|нужно|следует|можно|уже|ранее|будет|буду|когда|если`)
var nonMutationSuffix = regexp.MustCompile(`(?i)\b(previous|previously|earlier|todo|checklist|explanation|analysis)\b|ранее`)
var conditionalMutationPrefix = regexp.MustCompile(`(?i)\b(if|when|once)\b`)
var negatedMutationPrefix = regexp.MustCompile(`(?i)\b(not|no|never|without|didn't|haven't|hasn't)\b|(?:^|\s)не(?:\s|$)`)
var activeMutationPrefix = regexp.MustCompile(`(?i)^\s*(?:[-*]\s*)?(?:(?:done|completed)[\s—:,-]+)?(?:(?:i|we)(?:\s+have|'ve)?\s+)?$|\b(?:was|were|has been|have been|is now)\s*$`)

func completedEditClause(clause string) bool {
	if strings.Contains(clause, "?") {
		return false
	}
	loc := completedMutationClaim.FindStringIndex(clause)
	if loc == nil {
		return false
	}
	prefix := clause[:loc[0]]
	verb := strings.ToLower(clause[loc[0]:loc[1]])
	if nonMutationClaim.MatchString(clause[:loc[1]]) || nonMutationSuffix.MatchString(clause) || conditionalMutationPrefix.MatchString(prefix) || negatedMutationPrefix.MatchString(prefix) {
		return false
	}
	if loc[1] < len(clause) && clause[loc[1]] == '-' {
		return false
	}
	if strings.HasPrefix(verb, "now ") || strings.HasPrefix(verb, "is ") || strings.ContainsAny(verb, "абвгдежзийклмнопрстуфхцчшщыэюяё") {
		return true
	}
	return activeMutationPrefix.MatchString(prefix)
}

var fileMutationObject = regexp.MustCompile(`(?i)\b(file|files|code|comment|comments|implementation|function|functions|source|patch)\b|файл|комментар|реализац|функци|исходник|код`)
var mutationCitationSuffix = regexp.MustCompile(`(?i)\b(?:after\s+(?:reading|checking|inspecting)|verified\s+against|leaving|so\s+(?:the\s+(?:compile-time\s+)?assertion|any\s+future)|without\s+(?:editing|changing|modifying))\b`)

// ClaimedMutationPaths recognizes explicit completed-change claims, rather
// than every file reference. Plans, negations and fenced examples are excluded.
// This deliberately favors false negatives over accusing analysis prose.
func ClaimedMutationPaths(response string) []string {
	paths, _ := completedFileMutation(response)
	return paths
}

// ClaimsFileMutation also recognizes explicit claims such as "Added the
// requested comment" without requiring the answer to repeat a filename.
func ClaimsFileMutation(response string) bool {
	_, claimed := completedFileMutation(response)
	return claimed
}

func completedFileMutation(response string) ([]string, bool) {
	// Structured output is judged by its string values, not JSON key syntax.
	var value any
	if json.Unmarshal([]byte(response), &value) == nil {
		var texts []string
		var collect func(any)
		collect = func(v any) {
			switch x := v.(type) {
			case string:
				texts = append(texts, x)
			case []any:
				for _, child := range x {
					collect(child)
				}
			case map[string]any:
				for _, child := range x {
					collect(child)
				}
			}
		}
		collect(value)
		var paths []string
		claimed := false
		seen := map[string]bool{}
		for _, text := range texts {
			textPaths, textClaimed := completedTextFileMutation(text)
			claimed = claimed || textClaimed
			for _, path := range textPaths {
				if !seen[path] {
					seen[path] = true
					paths = append(paths, path)
				}
			}
		}
		return paths, claimed
	}
	return completedTextFileMutation(response)
}

func completedTextFileMutation(response string) ([]string, bool) {
	var paths []string
	claimed := false
	seen := map[string]bool{}
	inFence, changedList, proposal := false, false, false
	problemDescription := false
	for _, line := range strings.Split(response, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || strings.HasPrefix(trimmed, ">") {
			continue
		}
		if trimmed == "" {
			changedList = false
			proposal = false
			continue
		}
		lower := strings.ToLower(strings.Trim(trimmed, "#*: "))
		if lower == "what was wrong" || lower == "root cause" {
			problemDescription = true
			continue
		}
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "**") {
			problemDescription = false
		}
		if problemDescription {
			continue
		}
		if lower == "plan" || lower == "proposal" || lower == "план" {
			proposal = true
			continue
		}
		if proposal {
			continue
		}
		if lower == "files changed" || lower == "changed files" || lower == "изменённые файлы" || lower == "измененные файлы" {
			changedList = true
			continue
		}
		listItem := strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ")
		if !listItem {
			changedList = false
		}
		for _, clause := range mutationClaimClause.Split(line, -1) {
			if strings.Contains(clause, "?") || !changedList && !completedEditClause(clause) {
				continue
			}
			// An evidence citation after an edit claim does not claim that the
			// cited file was edited too ("Updated x.go after reading y.go").
			if loc := mutationCitationSuffix.FindStringIndex(clause); loc != nil {
				clause = clause[:loc[0]]
			}
			candidates := tokenizeResponseForPaths(clause)
			if len(candidates) > 0 || fileMutationObject.MatchString(clause) {
				claimed = true
			}
			for _, path := range candidates {
				if !seen[path] {
					seen[path] = true
					paths = append(paths, path)
				}
			}
		}
	}
	return paths, claimed
}
