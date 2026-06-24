package state

import "fmt"

// Parse maps a status name to a Status value.
func Parse(name string) (Status, error) {
	switch name {
	case "draft":
		return StatusDraft, nil
	case "active":
		return StatusActive, nil
	case "closed":
		return StatusClosed, nil
	default:
		return 0, fmt.Errorf("unknown status %q", name)
	}
}
