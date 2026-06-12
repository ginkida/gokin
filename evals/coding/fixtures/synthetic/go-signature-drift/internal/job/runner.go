package job

import (
	"fmt"

	"example.com/go-signature-drift/internal/store"
)

// Describe renders a one-line description of the job owner.
func Describe(s *store.Store) string {
	owner, err := s.Get("owner")
	if err != nil {
		return "unowned job"
	}
	return fmt.Sprintf("job owned by %s", owner)
}
