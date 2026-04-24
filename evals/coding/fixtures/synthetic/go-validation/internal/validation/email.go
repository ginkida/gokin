package validation

import "strings"

func ValidEmail(value string) bool {
	return strings.Contains(value, "@")
}
