//go:build (!darwin && !linux) || (darwin && !cgo)

package repl

func residentMemorySupported() bool { return false }

func residentMemoryBytes(int) (uint64, error) { return 0, nil }
