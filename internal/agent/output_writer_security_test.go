package agent

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestAgentOutputWriterCapsPersistentTranscript(t *testing.T) {
	writer := NewAgentOutputWriter(t.TempDir(), "disk-cap")
	if writer.FilePath() == "" {
		t.Fatal("writer did not create a backing file")
	}
	payload := bytes.Repeat([]byte("x"), int(maxAgentDiskOutput)+1024)
	n, err := writer.Write(payload)
	if err != nil || n != len(payload) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	writer.Close()

	info, err := os.Stat(writer.FilePath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxAgentDiskOutput {
		t.Fatalf("transcript size = %d, limit %d", info.Size(), maxAgentDiskOutput)
	}
	data, err := os.ReadFile(writer.FilePath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, agentOutputDiskTruncationMarker) {
		t.Fatal("capped transcript lacks an on-disk truncation marker")
	}
	if !writer.DiskTruncated() || writer.TotalBytes() != int64(len(payload)) {
		t.Fatalf("diskCut/total = %v/%d", writer.DiskTruncated(), writer.TotalBytes())
	}
	if got := writer.String(); !strings.Contains(got, "Transcript prefix") || !utf8.ValidString(got) {
		t.Fatalf("bounded memory output has invalid marker or UTF-8")
	}
}

func TestAgentOutputWriterMemoryBoundaryRemainsValidUTF8(t *testing.T) {
	writer := NewAgentOutputWriter(t.TempDir(), "utf8-boundary")
	writer.WriteString(strings.Repeat("a", int(maxAgentMemoryOutput)-1))
	writer.WriteString("€tail")
	got := writer.String()
	if !utf8.ValidString(got) {
		t.Fatal("String returned malformed UTF-8")
	}
	if !strings.Contains(got, "Full transcript") {
		t.Fatalf("String lacks full-transcript marker: tail=%q", tailAgentOutput(got, 160))
	}
	writer.Close()
}

func TestAgentOutputWriterReadFromValidatesOffsetAndReadsToSnapshot(t *testing.T) {
	writer := NewAgentOutputWriter(t.TempDir(), "incremental")
	writer.WriteString("abcdef")
	if _, _, err := writer.ReadFrom(-1); err == nil {
		t.Fatal("ReadFrom accepted a negative offset")
	}
	got, next, err := writer.ReadFrom(2)
	if err != nil {
		t.Fatal(err)
	}
	if got != "cdef" || next != 6 {
		t.Fatalf("ReadFrom = %q, %d", got, next)
	}
	writer.Close()
}

func TestAgentOutputWriterConcurrentWritesRemainComplete(t *testing.T) {
	writer := NewAgentOutputWriter(t.TempDir(), "concurrent")
	const writers = 32
	const writesPerWorker = 100
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < writesPerWorker; j++ {
				writer.WriteString("x")
			}
		}()
	}
	wg.Wait()
	writer.Close()
	want := int64(writers * writesPerWorker)
	if writer.TotalBytes() != want {
		t.Fatalf("TotalBytes = %d, want %d", writer.TotalBytes(), want)
	}
	data, err := os.ReadFile(writer.FilePath())
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != want {
		t.Fatalf("persistent bytes = %d, want %d", len(data), want)
	}
}

func TestBoundedAgentResultDescribesCappedTranscriptHonestly(t *testing.T) {
	output := strings.Repeat("x", int(maxAgentMemoryOutput)+1)
	got := boundedAgentResultOutput(output, "/tmp/agent.log", true)
	if !strings.Contains(got, "Transcript prefix") || strings.Contains(got, "Full transcript") {
		t.Fatalf("capped transcript marker = %q", tailAgentOutput(got, 180))
	}
}

func tailAgentOutput(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}
