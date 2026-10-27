//go:build !unix

package security

import "fmt"

func privateCacheDirectory(_ string, _ ...string) (string, error) {
	return "", fmt.Errorf("secure managed cache directories are unavailable on this platform")
}
