package context

import (
	"gokin/internal/logging"

	"google.golang.org/genai"
)

// AdjustBoundaryForToolPairs shifts a split boundary so that FunctionCall/FunctionResponse
// pairs are not separated. It scans messages near the boundary and moves it to avoid
// splitting any tool call/response pair across the boundary.
//
// The boundary represents the index where history is split: history[:boundary] is removed
// (or summarized) and history[boundary:] is kept. The function adjusts the boundary so
// that no FunctionCall in the removed portion has its FunctionResponse in the kept portion,
// and no FunctionResponse in the kept portion lacks its FunctionCall.
func AdjustBoundaryForToolPairs(history []*genai.Content, boundary int) int {
	if boundary <= 0 || boundary >= len(history) {
		return boundary
	}

	adjusted := boundary

	// Iteratively adjust until stable. Each iteration recollects IDs for the
	// current split point so that Case 2 works with accurate data after Case 1
	// may have shifted the boundary. Max 3 iterations prevents infinite loops.
	for iter := 0; iter < 3; iter++ {
		prev := adjusted

		// Collect IDs for the current split point
		rightCallIDs, rightResponseIDs := collectRightIDs(history, adjusted)

		// Case 1: Scan backwards — if a FunctionCall left of the boundary has
		// its FunctionResponse on the right, move boundary left to include it.
		for i := adjusted - 1; i >= 0 && i >= adjusted-10; i-- {
			if history[i] == nil {
				continue
			}
			for _, part := range history[i].Parts {
				if part != nil && part.FunctionCall != nil && part.FunctionCall.ID != "" {
					if rightResponseIDs[part.FunctionCall.ID] {
						if i < adjusted {
							adjusted = i
						}
					}
				}
			}
		}

		// Case 2: Scan forward — if a FunctionResponse at/after the boundary
		// has no matching FunctionCall on the right side, it's orphaned.
		// Move boundary forward past it so both call and response are removed.
		//
		// Recollect right-side call IDs if Case 1 moved the boundary, because
		// messages that moved from left to right may contain new calls.
		if adjusted != prev {
			rightCallIDs, _ = collectRightIDs(history, adjusted)
		}

		scanStart := adjusted
		for i := scanStart; i < len(history) && i < scanStart+10; i++ {
			if history[i] == nil {
				continue
			}
			hasOrphan := false
			for _, part := range history[i].Parts {
				if part != nil && part.FunctionResponse != nil && part.FunctionResponse.ID != "" {
					if !rightCallIDs[part.FunctionResponse.ID] {
						hasOrphan = true
					}
				}
			}
			if hasOrphan {
				adjusted = i + 1
			} else {
				break
			}
		}

		// Converged — no further changes
		if adjusted == prev {
			break
		}
	}

	// Clamp to valid range
	if adjusted < 0 {
		adjusted = 0
	}
	if adjusted > len(history) {
		adjusted = len(history)
	}

	if adjusted != boundary {
		logging.Debug("AdjustBoundaryForToolPairs shifted boundary",
			"original", boundary,
			"adjusted", adjusted,
			"history_len", len(history))
	}

	return adjusted
}

// collectRightIDs collects FunctionCall IDs and FunctionResponse IDs from history[boundary:].
func collectRightIDs(history []*genai.Content, boundary int) (callIDs, responseIDs map[string]bool) {
	callIDs = make(map[string]bool)
	responseIDs = make(map[string]bool)
	for i := boundary; i < len(history); i++ {
		if history[i] == nil {
			continue
		}
		for _, part := range history[i].Parts {
			if part == nil {
				continue
			}
			if part.FunctionCall != nil && part.FunctionCall.ID != "" {
				callIDs[part.FunctionCall.ID] = true
			}
			if part.FunctionResponse != nil && part.FunctionResponse.ID != "" {
				responseIDs[part.FunctionResponse.ID] = true
			}
		}
	}
	return
}
