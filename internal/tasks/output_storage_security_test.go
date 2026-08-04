package tasks

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSafeBufferCapsPersistentTranscript(t *testing.T) {
	var buffer safeBuffer
	path := filepath.Join(t.TempDir(), "task.log")
	if err := buffer.SetOutputFile(path); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("x"), maxTaskDiskOutput+1024)
	n, err := buffer.Write(payload)
	if err != nil || n != len(payload) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	buffer.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxTaskDiskOutput {
		t.Fatalf("task transcript size = %d, limit %d", info.Size(), maxTaskDiskOutput)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, taskOutputDiskTruncationMarker) {
		t.Fatal("capped task transcript lacks on-disk marker")
	}
	if got := buffer.String(); !strings.Contains(got, "Transcript prefix") {
		t.Fatalf("memory marker does not disclose disk cap: %q", taskOutputTail(got, 180))
	}
	if full := buffer.FullString(); !strings.HasSuffix(full, string(taskOutputDiskTruncationMarker)) {
		t.Fatal("FullString did not return the bounded transcript marker")
	}
}

func TestSafeBufferConcurrentWritesRemainComplete(t *testing.T) {
	var buffer safeBuffer
	path := filepath.Join(t.TempDir(), "task.log")
	if err := buffer.SetOutputFile(path); err != nil {
		t.Fatal(err)
	}
	const writers = 32
	const writesPerWorker = 100
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < writesPerWorker; j++ {
				_, _ = buffer.Write([]byte("x"))
			}
		}()
	}
	wg.Wait()
	buffer.Close()
	want := int64(writers * writesPerWorker)
	if buffer.TotalBytes() != want {
		t.Fatalf("TotalBytes = %d, want %d", buffer.TotalBytes(), want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != want {
		t.Fatalf("persistent bytes = %d, want %d", len(data), want)
	}
}

func TestTaskOutputFilePathContainsInvalidIDs(t *testing.T) {
	workDir := t.TempDir()
	path := taskOutputFilePath(workDir, "../../escape")
	wantDir := filepath.Join(workDir, ".gokin", "task-output")
	if filepath.Dir(path) != wantDir {
		t.Fatalf("unsafe output path %q escaped %q", path, wantDir)
	}
	if strings.Contains(filepath.Base(path), "..") {
		t.Fatalf("unsafe ID remained in output filename: %q", path)
	}
}

func taskOutputTail(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}
