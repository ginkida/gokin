package rewind

import (
	"fmt"

	"gokin/internal/chat"
)

func (s *Store) ImportHistoryWindows(windows []chat.HistoryWindowArchive) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.importHistoryWindows(windows)
}

// Caller holds s.mu. Frames arrive in publication order. Each durable archive
// flattens just the removed prefix, so thousands of window slides do not form
// an expansion chain and unaffected recent messages keep their representation.
func (s *Store) importHistoryWindows(windows []chat.HistoryWindowArchive) error {
	if err := chat.ValidateHistoryWindows(windows); err != nil {
		return err
	}
	for _, frame := range windows {
		before, err := rawContents(frame.Before)
		if err != nil {
			return err
		}
		after, err := rawContents(frame.After)
		if err != nil {
			return err
		}
		blocks, err := s.transcriptBlocks(before)
		if err != nil {
			return err
		}
		suffix := 0
		for suffix < len(after)-1 && contentHash(before[len(before)-suffix-1]) == contentHash(after[len(after)-suffix-1]) {
			suffix++
		}
		end := len(before) - suffix
		offset := 0
		for _, block := range blocks {
			next := offset + len(block.current)
			if offset < end && end < next {
				end = next
			}
			offset = next
		}
		suffix = len(before) - end
		var original []chat.SerializedContent
		offset = 0
		for _, block := range blocks {
			offset += len(block.current)
			if offset <= end {
				original = append(original, block.original...)
			}
		}
		archive := SummaryArchive{ID: frame.ID, PointID: frame.ID, Mode: "compact", Marker: after[0], Original: original, Replacement: after[:len(after)-suffix]}
		exists := false
		for _, ref := range s.doc.Summaries {
			if ref.ID == frame.ID {
				exists = true
				var saved SummaryArchive
				if err := s.readBlob(ref.Blob, &saved); err != nil {
					return err
				}
				if !validSummaryArchive(saved, ref) || contentHash(saved) != contentHash(archive) {
					return fmt.Errorf("pending history archive differs from its durable copy")
				}
				break
			}
		}
		if !exists {
			if err := s.saveSummaryLocked(archive); err != nil {
				return err
			}
		}
	}
	return nil
}
