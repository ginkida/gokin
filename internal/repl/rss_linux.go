//go:build linux

package repl

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func residentMemorySupported() bool { return true }

// residentMemoryBytes sums the launcher and its descendants. bubblewrap may
// remain as a small namespace supervisor instead of execing Python in place;
// /proc/.../children keeps that implementation detail from bypassing the cap.
func residentMemoryBytes(pid int) (uint64, error) {
	queue := []int{pid}
	seen := make(map[int]bool)
	var total uint64
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current <= 0 || seen[current] {
			continue
		}
		seen[current] = true
		resident, err := linuxProcessResidentBytes(current)
		if err != nil {
			if current == pid {
				return 0, err
			}
			continue
		}
		total += resident
		childrenPath := filepath.Join("/proc", strconv.Itoa(current), "task", strconv.Itoa(current), "children")
		children, err := os.ReadFile(childrenPath)
		if err != nil {
			continue
		}
		for _, field := range strings.Fields(string(children)) {
			child, parseErr := strconv.Atoi(field)
			if parseErr == nil {
				queue = append(queue, child)
			}
		}
	}
	return total, nil
}

func linuxProcessResidentBytes(pid int) (uint64, error) {
	path := filepath.Join("/proc", strconv.Itoa(pid), "status")
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open resident memory status for pid %d: %w", pid, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "VmRSS:" {
			continue
		}
		kib, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil {
			return 0, fmt.Errorf("parse resident memory for pid %d: %w", pid, parseErr)
		}
		return kib * 1024, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read resident memory for pid %d: %w", pid, err)
	}
	return 0, fmt.Errorf("resident memory is absent for pid %d", pid)
}
