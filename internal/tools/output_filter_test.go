package tools

import (
	"strings"
	"testing"
)

func TestFilterBashOutput_Dedup(t *testing.T) {
	// Must exceed 100 chars minimum for filter to engage
	lines := []string{"header line", "some context here"}
	for i := 0; i < 10; i++ {
		lines = append(lines, "the same repeated output line that keeps appearing")
	}
	lines = append(lines, "final line")
	input := strings.Join(lines, "\n")

	result := FilterBashOutput(input)
	if !strings.Contains(result, "repeated") {
		t.Errorf("expected dedup message, got:\n%s", result)
	}
	// Should appear only once (first occurrence) + repeat count
	if strings.Count(result, "the same repeated output line") != 1 {
		t.Errorf("expected repeated line to appear once, got:\n%s", result)
	}
}

func TestFilterBashOutput_NpmNoise(t *testing.T) {
	input := strings.Repeat("x", 50) + "\n" + // pad to exceed minimum
		"added 150 packages in 5s\n" +
		"npm WARN deprecated some-pkg@1.0.0: use other-pkg\n" +
		"5 packages are looking for funding\n" +
		"  run npm fund for details\n" +
		"actual output here that should be preserved\n" +
		"more real output that matters\n"
	result := FilterBashOutput(input)
	if strings.Contains(result, "npm WARN") {
		t.Errorf("expected npm WARN filtered, got:\n%s", result)
	}
	if strings.Contains(result, "looking for funding") {
		t.Errorf("expected funding noise filtered, got:\n%s", result)
	}
	if !strings.Contains(result, "actual output here") {
		t.Errorf("expected real output preserved, got:\n%s", result)
	}
}

func TestFilterBashOutput_CargoNoise(t *testing.T) {
	input := "   Compiling serde v1.0.200\n" +
		"   Compiling tokio v1.38.0\n" +
		"   Downloading regex v1.10.0\n" +
		"   Compiling myapp v0.1.0\n" +
		"error[E0308]: mismatched types\n" +
		"  --> src/main.rs:42:5\n"
	result := FilterBashOutput(input)
	if strings.Contains(result, "Compiling serde") {
		t.Errorf("expected cargo download noise filtered, got:\n%s", result)
	}
	if !strings.Contains(result, "error[E0308]") {
		t.Errorf("expected error preserved, got:\n%s", result)
	}
}

func TestFilterBashOutput_GoDownload(t *testing.T) {
	input := "go: downloading golang.org/x/text v0.14.0\n" +
		"go: downloading golang.org/x/sync v0.7.0\n" +
		"go: downloading golang.org/x/net v0.25.0\n" +
		"ok  \tmyapp/pkg\t0.5s\n"
	result := FilterBashOutput(input)
	if strings.Contains(result, "go: downloading") {
		t.Errorf("expected go download noise filtered, got:\n%s", result)
	}
	if !strings.Contains(result, "ok") {
		t.Errorf("expected test result preserved, got:\n%s", result)
	}
}

func TestFilterBashOutput_BlankLines(t *testing.T) {
	input := "first line of real output with enough content to exceed minimum\n\n\n\n\nsecond line after blank run\n\n\n\nthird line here with more padding\nfourth for padding\n"
	result := FilterBashOutput(input)
	if strings.Contains(result, "\n\n\n") {
		t.Errorf("expected blank lines collapsed, got:\n%q", result)
	}
	if !strings.Contains(result, "first line") || !strings.Contains(result, "third line") {
		t.Errorf("expected content preserved, got:\n%s", result)
	}
}

func TestFilterBashOutput_DockerPull(t *testing.T) {
	input := "a1b2c3d4e5f6: Pulling fs layer\n" +
		"a1b2c3d4e5f6: Downloading\n" +
		"a1b2c3d4e5f6: Pull complete\n" +
		"e5f6a7b8c9d0: Pulling fs layer\n" +
		"e5f6a7b8c9d0: Pull complete\n" +
		"Digest: sha256:abc123def456789\n" +
		"Status: Downloaded newer image\n" +
		"real output here should be preserved\n"
	result := FilterBashOutput(input)
	if strings.Contains(result, "Pulling") {
		t.Errorf("expected docker pull noise filtered, got:\n%s", result)
	}
	if !strings.Contains(result, "real output here") {
		t.Errorf("expected real output preserved, got:\n%s", result)
	}
}

func TestFilterBashOutput_ShortOutput(t *testing.T) {
	input := "ok"
	result := FilterBashOutput(input)
	if result != input {
		t.Errorf("expected short output unchanged, got:\n%s", result)
	}
}

func TestFilterBashOutput_CleanOutput(t *testing.T) {
	input := "line one\nline two\nline three\nline four\nline five\n"
	result := FilterBashOutput(input)
	if strings.Contains(result, "noise lines filtered") {
		t.Errorf("expected no filtering message for clean output, got:\n%s", result)
	}
}

func TestFilterBashOutput_PipNoise(t *testing.T) {
	input := "Collecting requests==2.31.0\n" +
		"  Downloading requests-2.31.0.tar.gz\n" +
		"Collecting urllib3<2\n" +
		"  Using cached urllib3-1.26.18.whl\n" +
		"Successfully installed requests-2.31.0 urllib3-1.26.18\n" +
		"real output: tests passed\n"
	result := FilterBashOutput(input)
	if strings.Contains(result, "Collecting") {
		t.Errorf("expected pip noise filtered, got:\n%s", result)
	}
	if !strings.Contains(result, "real output: tests passed") {
		t.Errorf("expected real output preserved, got:\n%s", result)
	}
}

func TestFilterBashOutput_ProgressBar(t *testing.T) {
	input := "Starting the build process for the application\n" +
		"[==========          ] 50%\n" +
		"[====================] 100%\n" +
		"Build complete: 42 files compiled successfully\n"
	result := FilterBashOutput(input)
	if strings.Contains(result, "50%") {
		t.Errorf("expected progress bar filtered, got:\n%s", result)
	}
	if !strings.Contains(result, "Build complete") {
		t.Errorf("expected result preserved, got:\n%s", result)
	}
}
