//go:build !linux

package repl

import (
	"fmt"
	"os"
)

func openWorkerSeccompFilter(string) (*os.File, error) {
	return nil, fmt.Errorf("worker seccomp filter is Linux-only")
}
