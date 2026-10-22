package rewind

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"gokin/internal/chat"
)

type transcriptBlock struct {
	current, original []chat.SerializedContent
}

func flattenBlocks(blocks []transcriptBlock) []chat.SerializedContent {
	var result []chat.SerializedContent
	for _, block := range blocks {
		result = append(result, block.original...)
	}
	return result
}

// transcriptBlocks retains the representation of each archive span alongside
// its original messages. A compacted span must match in full: accepting only
// its first message could silently duplicate or discard retained tool results.
// Caller holds s.mu.
func (s *Store) transcriptBlocks(history []chat.SerializedContent) ([]transcriptBlock, error) {
	lookup := make(map[string]SummaryRef, len(s.doc.Summaries))
	for _, ref := range s.doc.Summaries {
		lookup[ref.Marker] = ref
	}
	bytes, messages := 0, 0
	var expand func([]chat.SerializedContent, int) ([]transcriptBlock, error)
	expand = func(items []chat.SerializedContent, depth int) ([]transcriptBlock, error) {
		if depth > 32 {
			return nil, fmt.Errorf("summary archive nesting is too deep")
		}
		var blocks []transcriptBlock
		for i := 0; i < len(items); {
			item := items[i]
			ref, exists := lookup[contentHash(item)]
			if !exists {
				data, _ := json.Marshal(item)
				bytes += len(data)
				messages++
				if bytes > maxBlobBytes || messages > 100000 {
					return nil, fmt.Errorf("expanded transcript exceeds size limit")
				}
				blocks = append(blocks, transcriptBlock{current: items[i : i+1], original: items[i : i+1]})
				i++
				continue
			}
			var archive SummaryArchive
			if err := s.readBlob(ref.Blob, &archive); err != nil {
				return nil, err
			}
			if !validSummaryArchive(archive, ref) {
				return nil, fmt.Errorf("summary archive identity mismatch")
			}
			count := 1
			if len(archive.Replacement) != 0 {
				count = len(archive.Replacement)
				if count > len(items)-i || contentHash(archive.Replacement) != contentHash(items[i:i+count]) {
					return nil, fmt.Errorf("compacted transcript span changed; restore a checkpoint before summarizing it")
				}
			}
			original, err := expand(archive.Original, depth+1)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, transcriptBlock{current: items[i : i+count], original: flattenBlocks(original)})
			i += count
		}
		return blocks, nil
	}
	return expand(history, 0)
}

// ArchiveCompaction stores the originals before publishing a destructive
// context edit. The smallest changed span is extended to cover complete older
// archives, so repeated compaction cannot leave half of an expansion mapping.
// The caller publishes the returned history only if its source version still
// matches. An unused archive after a failed publication is harmless.
func (s *Store) ArchiveCompaction(before, after []chat.SerializedContent) ([]chat.SerializedContent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	before, err = rawContents(before)
	if err != nil {
		return nil, err
	}
	after, err = rawContents(after)
	if err != nil {
		return nil, err
	}
	if contentHash(before) == contentHash(after) {
		return after, nil
	}
	if len(before) == 0 || len(after) > chat.MaxMessages {
		return nil, fmt.Errorf("invalid context compaction span")
	}
	blocks, err := s.transcriptBlocks(before)
	if err != nil {
		return nil, err
	}
	start := 0
	for start < len(before) && start < len(after) && contentHash(before[start]) == contentHash(after[start]) {
		start++
	}
	suffix := 0
	for suffix < len(before)-start && suffix < len(after)-start && contentHash(before[len(before)-suffix-1]) == contentHash(after[len(after)-suffix-1]) {
		suffix++
	}
	// A pure deletion needs an anchor. Include one adjacent retained message,
	// rather than consuming an extra slot in the session's hard message window.
	if start+suffix == len(after) && len(after) > 0 {
		if suffix > 0 {
			suffix--
		} else {
			start--
		}
	}
	end := len(before) - suffix
	offset := 0
	for _, block := range blocks {
		next := offset + len(block.current)
		if offset < start && start < next {
			start = offset
		}
		if offset < end && end < next {
			end = next
		}
		offset = next
	}
	suffix = len(before) - end
	var original []chat.SerializedContent
	offset = 0
	for _, block := range blocks {
		next := offset + len(block.current)
		if offset >= start && next <= end {
			original = append(original, block.original...)
		}
		offset = next
	}
	if len(original) == 0 {
		return nil, fmt.Errorf("context edit does not replace original messages")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(nonce[:])
	replacement := append([]chat.SerializedContent(nil), after[start:len(after)-suffix]...)
	if len(replacement) == 0 {
		replacement = []chat.SerializedContent{{Role: "user"}}
	}
	replacement[0].Parts = append(append([]chat.SerializedPart(nil), replacement[0].Parts...), chat.SerializedPart{
		Type: "text", Text: fmt.Sprintf("[Compacted conversation: %s]\nOriginal messages: use read with file_path gokin://summary/%s (offset/limit supported).", id, id),
	})
	archive := SummaryArchive{ID: id, PointID: id, Mode: "compact", Marker: replacement[0], Original: original, Replacement: replacement}
	if err := s.saveSummaryLocked(archive); err != nil {
		return nil, err
	}
	result := append([]chat.SerializedContent(nil), after[:start]...)
	result = append(result, replacement...)
	result = append(result, after[len(after)-suffix:]...)
	return result, nil
}
