package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gokin/internal/chat"
	"gokin/internal/fileutil"
)

const (
	maxSessionArchiveRecordBytes      int64 = 32 << 20
	maxSessionArchiveSegmentBytes     int64 = 64 << 20
	maxSessionArchiveSegments               = 1024
	maxSessionArchiveDirectoryEntries       = 10000
)

func prepareSessionArchiveDir(workDir string) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		return "", fmt.Errorf("session archive work directory is empty")
	}
	gokinDir := filepath.Join(workDir, ".gokin")
	if err := fileutil.EnsurePrivateDir(gokinDir); err != nil {
		return "", err
	}
	dir := filepath.Join(gokinDir, "session_archives")
	if err := fileutil.EnsurePrivateDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func sessionArchivePath(dir, sessionID string) (string, error) {
	if err := chat.ValidateSessionID(sessionID); err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionID+".jsonl"), nil
}

func appendSessionArchiveData(workDir, sessionID string, line []byte) error {
	if int64(len(line)) > maxSessionArchiveRecordBytes {
		return fmt.Errorf("session archive record exceeds %d-byte limit", maxSessionArchiveRecordBytes)
	}
	if err := chat.ValidateSessionID(sessionID); err != nil {
		return err
	}
	dir, err := prepareSessionArchiveDir(workDir)
	if err != nil {
		return err
	}
	path, err := sessionArchivePath(dir, sessionID)
	if err != nil {
		return err
	}
	segmentCount, err := inspectSessionArchiveSegments(dir, sessionID)
	if err != nil {
		return err
	}

	currentExists := false
	if _, err := os.Lstat(path); err == nil {
		currentExists = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if !currentExists && segmentCount >= maxSessionArchiveSegments {
		return fmt.Errorf("session archive reached %d-segment limit", maxSessionArchiveSegments)
	}
	if currentExists && segmentCount >= maxSessionArchiveSegments {
		return fmt.Errorf("session archive exceeds %d-segment limit", maxSessionArchiveSegments)
	}

	file, err := fileutil.OpenPrivateAppend(path)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	if info.Size() < 0 || info.Size() > maxSessionArchiveSegmentBytes-int64(len(line)) {
		if segmentCount >= maxSessionArchiveSegments-1 {
			_ = file.Close()
			return fmt.Errorf("session archive reached %d-segment limit", maxSessionArchiveSegments)
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := rotateSessionArchive(path, sessionID); err != nil {
			return err
		}
		file, err = fileutil.OpenPrivateAppend(path)
		if err != nil {
			return err
		}
		info, err = file.Stat()
		if err != nil {
			_ = file.Close()
			return err
		}
	}

	originalSize := info.Size()
	if err := writeSessionArchiveLine(file, line); err != nil {
		_ = file.Truncate(originalSize)
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Truncate(originalSize)
		_ = file.Sync()
		_ = file.Close()
		return err
	}
	return file.Close()
}

func writeSessionArchiveLine(file *os.File, line []byte) error {
	for len(line) > 0 {
		written, err := file.Write(line)
		if err != nil {
			return err
		}
		if written <= 0 {
			return fmt.Errorf("session archive append made no progress")
		}
		line = line[written:]
	}
	return nil
}

func rotateSessionArchive(path, sessionID string) error {
	if err := fileutil.SecurePrivateFile(path); err != nil {
		return err
	}
	for attempt := 0; attempt < 100; attempt++ {
		stamp := time.Now().UnixNano() + int64(attempt)
		rotated := filepath.Join(filepath.Dir(path), fmt.Sprintf("%s.%020d.jsonl", sessionID, stamp))
		if _, err := os.Lstat(rotated); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		return os.Rename(path, rotated)
	}
	return fmt.Errorf("could not allocate a unique session archive segment")
}

func inspectSessionArchiveSegments(dir, sessionID string) (int, error) {
	return inspectSessionArchiveSegmentsWithLimit(dir, sessionID, maxSessionArchiveSegments)
}

func inspectSessionArchiveSegmentsWithLimit(dir, sessionID string, limit int) (int, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("session archive segment limit must be positive")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	if len(entries) > maxSessionArchiveDirectoryEntries {
		return 0, fmt.Errorf("session archive directory exceeds %d-entry limit", maxSessionArchiveDirectoryEntries)
	}
	prefix := sessionID + "."
	count := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".jsonl") ||
			name == sessionID+".jsonl" {
			continue
		}
		rawTimestamp := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".jsonl")
		if _, err := strconv.ParseInt(rawTimestamp, 10, 64); err != nil {
			continue
		}
		path := filepath.Join(dir, name)
		if entry.Type()&os.ModeSymlink != 0 {
			return 0, fmt.Errorf("session archive segment %q is a symlink", path)
		}
		info, err := entry.Info()
		if err != nil {
			return 0, err
		}
		if !info.Mode().IsRegular() {
			return 0, fmt.Errorf("session archive segment %q is not a regular file", path)
		}
		if err := fileutil.SecurePrivateFile(path); err != nil {
			return 0, err
		}
		count++
		if count > limit {
			return 0, fmt.Errorf("session archive exceeds %d-segment limit", limit)
		}
	}
	return count, nil
}
