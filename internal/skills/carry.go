package skills

import (
	"encoding/base64"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"google.golang.org/genai"
)

const (
	// SkillCarryPerInvocationTokens matches Claude Code's carry-forward budget
	// for one invoked skill. The estimate is rune-aware, content-sensitive, and
	// independent of a provider tokenizer.
	SkillCarryPerInvocationTokens = 5_000
	// SkillCarryCombinedTokens bounds the complete synthetic carry message,
	// including its envelope, block headers, truncation notices, and footers.
	SkillCarryCombinedTokens = 25_000

	skillCarryStart = "[gokin:active-skills:v1]"
	skillCarryEnd   = "[/gokin:active-skills:v1]"
	skillBlockStart = "[gokin:active-skill:v1 name="
	skillBlockEnd   = "[/gokin:active-skill:v1]"

	skillCarryTruncationNotice = "\n\n[Skill snapshot truncated: only its beginning fits the active-skill carry-forward token limit.]"
)

type carryInvocation struct {
	name       string
	rendered   string
	renderHash string
	sequence   uint64
	order      int
}

type carryMarker struct {
	name       string
	renderHash string
}

// ReattachInvocations removes synthetic active-skill carry messages generated
// by this package, then inserts one fresh user message for the latest snapshots
// that are not already present in retained raw history. Messages outside the
// strictly parsed, reserved gokin:active-skills:v1 envelope and the supplied
// invocations are never modified. insertAt is interpreted against the supplied
// history; removal of an older synthetic block before that position is accounted
// for automatically, then the result is clamped to the retained raw history.
// This keeps an "after summary" position stable across rebuilds. The envelope
// syntax is an internal control namespace and must not be used for ordinary user
// messages.
//
// A successful raw FunctionResponse from the skill tool suppresses a carry
// block when its content hashes to the current snapshot (or its structured
// changed=true metadata carries that hash). Marker-looking text copied into a
// summary is not trusted as proof that the workflow body survived.
func ReattachInvocations(history []*genai.Content, invocations []Invocation, insertAt int) []*genai.Content {
	if insertAt < 0 {
		insertAt = 0
	} else if insertAt > len(history) {
		insertAt = len(history)
	}
	rawHistory, removedBefore := removeSyntheticCarryMessages(history, insertAt)
	insertAt -= removedBefore
	current := latestCarryInvocations(invocations)
	if len(current) == 0 {
		return rawHistory
	}

	presentHashes := scanRetainedSkillSnapshots(rawHistory)
	missing := make([]carryInvocation, 0, len(current))
	for _, invocation := range current {
		if presentHashes[invocation.renderHash] {
			continue
		}
		missing = append(missing, invocation)
	}
	if len(missing) == 0 {
		return rawHistory
	}

	carryText := buildCarryText(missing)
	if carryText == "" {
		return rawHistory
	}
	carry := genai.NewContentFromText(carryText, genai.RoleUser)

	if insertAt < 0 {
		insertAt = 0
	} else if insertAt > len(rawHistory) {
		insertAt = len(rawHistory)
	}
	result := make([]*genai.Content, 0, len(rawHistory)+1)
	result = append(result, rawHistory[:insertAt]...)
	result = append(result, carry)
	result = append(result, rawHistory[insertAt:]...)

	return result
}

func latestCarryInvocations(invocations []Invocation) []carryInvocation {
	latest := make(map[string]carryInvocation, len(invocations))
	for i, invocation := range invocations {
		name := strings.ToLower(strings.TrimSpace(invocation.Name))
		if !validSkillName.MatchString(name) || strings.TrimSpace(invocation.Rendered) == "" ||
			!utf8.ValidString(invocation.Rendered) || len(invocation.Rendered) > MaxRenderedSkillBytes {
			continue
		}
		candidate := carryInvocation{
			name:       name,
			rendered:   invocation.Rendered,
			renderHash: renderHash(invocation.Rendered),
			sequence:   invocation.Sequence,
			order:      i,
		}
		existing, ok := latest[name]
		if !ok || candidate.sequence > existing.sequence ||
			(candidate.sequence == existing.sequence && candidate.order < existing.order) {
			latest[name] = candidate
		}
	}

	ordered := make([]carryInvocation, 0, len(latest))
	for _, invocation := range latest {
		ordered = append(ordered, invocation)
	}
	sortCarryInvocations(ordered)
	return ordered
}

func sortCarryInvocations(invocations []carryInvocation) {
	// A small insertion sort keeps this package's ordering dependency-free. The
	// ledger caps the list at MaxSkillCount, so O(n^2) is bounded and tiny.
	for i := 1; i < len(invocations); i++ {
		candidate := invocations[i]
		j := i - 1
		for j >= 0 && carryInvocationComesBefore(candidate, invocations[j]) {
			invocations[j+1] = invocations[j]
			j--
		}
		invocations[j+1] = candidate
	}
}

func carryInvocationComesBefore(a, b carryInvocation) bool {
	if a.sequence != b.sequence {
		return a.sequence > b.sequence
	}
	if a.order != b.order {
		return a.order < b.order
	}
	return a.name < b.name
}

func buildCarryText(invocations []carryInvocation) string {
	// The final newline between the last block and the envelope footer is part
	// of the fixed envelope. Each block additionally needs its leading newline.
	fixed := estimatedCarryTokens(skillCarryStart) + 1 + estimatedCarryTokens(skillCarryEnd)
	remaining := SkillCarryCombinedTokens - fixed
	if remaining <= 1 {
		return ""
	}

	blocks := make([]string, 0, len(invocations))
	for _, invocation := range invocations {
		if remaining <= 1 {
			break
		}
		// Count the separator immediately before the block as part of that
		// invocation's wrapper budget as well as the combined envelope.
		blockBudget := SkillCarryPerInvocationTokens - estimatedCarryTokens("\n")
		if available := remaining - 1; available < blockBudget {
			blockBudget = available
		}
		block, ok := buildCarryBlock(invocation, blockBudget)
		if !ok {
			break
		}
		blocks = append(blocks, block)
		remaining -= 1 + estimatedCarryTokens(block)
	}
	if len(blocks) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString(skillCarryStart)
	for _, block := range blocks {
		builder.WriteByte('\n')
		builder.WriteString(block)
	}
	builder.WriteByte('\n')
	builder.WriteString(skillCarryEnd)
	return builder.String()
}

func buildCarryBlock(invocation carryInvocation, budget int) (string, bool) {
	name := base64.RawURLEncoding.EncodeToString([]byte(invocation.name))
	headerPrefix := skillBlockStart + name + " hash=" + invocation.renderHash + " bytes="
	footer := "\n" + skillBlockEnd

	fullHeader := headerPrefix + strconv.Itoa(len(invocation.rendered)) + "]\n"
	full := fullHeader + invocation.rendered + footer
	if estimatedCarryTokens(full) <= budget {
		return full, true
	}

	// The decimal byte count changes the header width, so converge on a prefix
	// that fits using at most a handful of monotonic reductions.
	contentBudget := budget - estimatedCarryTokens(headerPrefix+strconv.Itoa(len(invocation.rendered)+len(skillCarryTruncationNotice))+"]\n"+skillCarryTruncationNotice+footer)
	if contentBudget < 0 {
		contentBudget = 0
	}
	prefix := carryPrefixWithinTokens(invocation.rendered, contentBudget)
	for {
		body := prefix + skillCarryTruncationNotice
		header := headerPrefix + strconv.Itoa(len(body)) + "]\n"
		block := header + body + footer
		cost := estimatedCarryTokens(block)
		if cost <= budget {
			return block, true
		}
		if prefix == "" {
			return "", false
		}
		prefix = trimLastRune(prefix)
	}
}

func carryPrefixWithinTokens(text string, budget int) string {
	if budget <= 0 || text == "" {
		return ""
	}
	usedUnits := 0
	maxUnits := budget * carryTokenUnitsPerToken
	for index, r := range text {
		cost := carryRuneTokenUnits(r)
		if usedUnits+cost > maxUnits {
			return text[:index]
		}
		usedUnits += cost
	}
	return text
}

func trimLastRune(text string) string {
	_, size := utf8.DecodeLastRuneInString(text)
	if size <= 0 {
		return ""
	}
	return text[:len(text)-size]
}

const carryTokenUnitsPerToken = 6

// estimatedCarryTokens is a deterministic provider-independent estimate. It
// targets roughly three letters/digits per token (slightly denser than the
// context package's 3.2-char code estimate), gives punctuation a higher cost,
// and treats CJK and symbols as token-dense. Counting runes avoids penalizing
// Cyrillic merely because its UTF-8 encoding uses more bytes.
func estimatedCarryTokens(text string) int {
	units := 0
	for _, r := range text {
		units += carryRuneTokenUnits(r)
	}
	if units == 0 {
		return 0
	}
	return (units + carryTokenUnitsPerToken - 1) / carryTokenUnitsPerToken
}

func carryRuneTokenUnits(r rune) int {
	switch {
	case unicode.IsSpace(r):
		return 1
	case unicode.In(r, unicode.Han, unicode.Hangul, unicode.Hiragana, unicode.Katakana):
		return carryTokenUnitsPerToken
	case unicode.IsLetter(r), unicode.IsNumber(r), unicode.IsMark(r):
		return 2 // about three runes per token
	case unicode.IsPunct(r):
		return 3 // operators and delimiters make code more token-dense
	case unicode.IsSymbol(r):
		return carryTokenUnitsPerToken
	default:
		return carryTokenUnitsPerToken
	}
}

func removeSyntheticCarryMessages(history []*genai.Content, insertAt int) ([]*genai.Content, int) {
	removed := false
	for _, content := range history {
		if isSyntheticCarryContent(content) {
			removed = true
			break
		}
	}
	if !removed {
		return history, 0
	}
	filtered := make([]*genai.Content, 0, len(history))
	removedBefore := 0
	for index, content := range history {
		if !isSyntheticCarryContent(content) {
			filtered = append(filtered, content)
		} else if index < insertAt {
			removedBefore++
		}
	}
	return filtered, removedBefore
}

func isSyntheticCarryContent(content *genai.Content) bool {
	if content == nil || content.Role != string(genai.RoleUser) || len(content.Parts) != 1 {
		return false
	}
	part := content.Parts[0]
	if part == nil || !isPlainTextPart(part) {
		return false
	}
	_, ok := parseCarryEnvelope(part.Text)
	return ok
}

func isPlainTextPart(part *genai.Part) bool {
	return part.MediaResolution == nil && part.CodeExecutionResult == nil &&
		part.ExecutableCode == nil && part.FileData == nil &&
		part.FunctionCall == nil && part.FunctionResponse == nil &&
		part.InlineData == nil && !part.Thought && len(part.ThoughtSignature) == 0 &&
		part.VideoMetadata == nil
}

func scanRetainedSkillSnapshots(history []*genai.Content) map[string]bool {
	hashes := make(map[string]bool)
	for _, content := range history {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part == nil {
				continue
			}
			// Explicit /skill invocations are submitted as an ordinary user prompt,
			// not a FunctionResponse. Exact bounded text is therefore also a full
			// delivery witness; hashing only exact parts avoids treating a summary
			// that merely mentions a marker as retained instructions.
			if part.Text != "" && len(part.Text) <= MaxRenderedSkillBytes && utf8.ValidString(part.Text) {
				hashes[renderHash(part.Text)] = true
			}
			if response := part.FunctionResponse; response != nil && response.Name == "skill" && response.Response != nil {
				success, successOK := response.Response["success"].(bool)
				rendered, contentOK := response.Response["content"].(string)
				if successOK && success {
					if contentOK && utf8.ValidString(rendered) {
						hashes[renderHash(rendered)] = true
					}
					// Executor may append a pending-notification banner to content
					// after SkillTool returns. The immutable hash remains in data.
					// changed=true proves this response delivered the full render;
					// changed=false identifies the short deduplication note instead.
					if data, ok := response.Response["data"].(map[string]any); ok {
						changed, changedOK := data["changed"].(bool)
						hash, hashOK := data["render_hash"].(string)
						if changedOK && changed && hashOK && isLowerSHA256(hash) {
							hashes[hash] = true
						}
					}
				}
			}
		}
	}
	return hashes
}

func parseCarryEnvelope(text string) ([]carryMarker, bool) {
	prefix := skillCarryStart + "\n"
	if !strings.HasPrefix(text, prefix) || !strings.HasSuffix(text, "\n"+skillCarryEnd) {
		return nil, false
	}
	position := len(prefix)
	endPosition := len(text) - len("\n"+skillCarryEnd)
	if position >= endPosition {
		return nil, false
	}

	var markers []carryMarker
	for position < endPosition {
		lineEndRelative := strings.IndexByte(text[position:endPosition], '\n')
		if lineEndRelative < 0 {
			return nil, false
		}
		lineEnd := position + lineEndRelative
		marker, contentBytes, ok := parseCarryHeader(text[position:lineEnd])
		if !ok {
			return nil, false
		}
		position = lineEnd + 1
		if contentBytes < 0 || contentBytes > endPosition-position {
			return nil, false
		}
		body := text[position : position+contentBytes]
		if !utf8.ValidString(body) {
			return nil, false
		}
		position += contentBytes
		footer := "\n" + skillBlockEnd
		if !strings.HasPrefix(text[position:endPosition], footer) {
			return nil, false
		}
		position += len(footer)
		markers = append(markers, marker)
		if position == endPosition {
			break
		}
		if text[position] != '\n' {
			return nil, false
		}
		position++
	}
	return markers, len(markers) > 0 && position == endPosition
}

func parseCarryHeader(line string) (carryMarker, int, bool) {
	if !strings.HasPrefix(line, skillBlockStart) || !strings.HasSuffix(line, "]") {
		return carryMarker{}, 0, false
	}
	remainder := strings.TrimSuffix(strings.TrimPrefix(line, skillBlockStart), "]")
	nameEnd := strings.Index(remainder, " hash=")
	if nameEnd <= 0 {
		return carryMarker{}, 0, false
	}
	encodedName := remainder[:nameEnd]
	remainder = remainder[nameEnd+len(" hash="):]
	hashEnd := strings.Index(remainder, " bytes=")
	if hashEnd <= 0 {
		return carryMarker{}, 0, false
	}
	hash := remainder[:hashEnd]
	byteText := remainder[hashEnd+len(" bytes="):]

	nameBytes, err := base64.RawURLEncoding.DecodeString(encodedName)
	if err != nil || !utf8.Valid(nameBytes) {
		return carryMarker{}, 0, false
	}
	name := string(nameBytes)
	if !validSkillName.MatchString(name) || name != strings.ToLower(strings.TrimSpace(name)) || !isLowerSHA256(hash) {
		return carryMarker{}, 0, false
	}
	contentBytes, err := strconv.Atoi(byteText)
	if err != nil || contentBytes < 0 || strconv.Itoa(contentBytes) != byteText {
		return carryMarker{}, 0, false
	}
	return carryMarker{name: name, renderHash: hash}, contentBytes, true
}

func isLowerSHA256(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, char := range hash {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
