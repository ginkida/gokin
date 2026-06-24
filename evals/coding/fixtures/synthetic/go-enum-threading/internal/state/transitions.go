package state

// allowed maps a Status to the set of statuses it may transition to.
//
// Contract: Active and Closed may move to Archived; Archived is TERMINAL
// (it has no outgoing transitions).
var allowed = map[Status][]Status{
	StatusDraft:  {StatusActive},
	StatusActive: {StatusClosed},
	StatusClosed: {},
}

// CanTransition reports whether moving from->to is allowed.
func CanTransition(from, to Status) bool {
	for _, t := range allowed[from] {
		if t == to {
			return true
		}
	}
	return false
}
