package classify

import "strings"

// serverErrorCodes are the HTTP status codes treated as server-side errors.
var serverErrorCodes = []string{"500", "502", "503", "504"}

// IsServerError reports whether msg mentions an HTTP 5xx server error.
// Timing strings like "took 500ms" must NOT count as a server error.
func IsServerError(msg string) bool {
	for _, code := range serverErrorCodes {
		if strings.Contains(msg, code) {
			return true
		}
	}
	return false
}
