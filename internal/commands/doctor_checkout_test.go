package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/config"
)

func makeDoctorCheckout(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module gokin\n\ngo 1.25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mainDir := filepath.Join(root, "cmd", "gokin")
	if err := os.MkdirAll(mainDir, 0o700); err != nil {
		t.Fatal(err)
	}
	mainSource := "package main\n\nvar (\n\tversion = \"" + version + "\"\n)\n"
	if err := os.WriteFile(filepath.Join(mainDir, "main.go"), []byte(mainSource), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func doctorCheckoutConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.API.ActiveProvider = "ollama"
	cfg.API.Backend = "ollama"
	return cfg
}

func makeDoctorExecutable(t *testing.T, root string, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(root, "gokin-bin")
	if err := os.WriteFile(path, []byte("test executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDoctorWarnsWhenActiveBinaryIsOlderThanCheckout(t *testing.T) {
	root := makeDoctorCheckout(t, "0.100.133")
	executable := makeDoctorExecutable(t, root, time.Now().Add(time.Hour))
	out := RenderDoctor(DoctorOptions{
		Version:        "v0.100.110",
		Config:         doctorCheckoutConfig(),
		WorkDir:        root,
		ExecutablePath: executable,
		CLI:            true,
	})
	for _, want := range []string{
		"Active binary v0.100.110 is older than checkout 0.100.133",
		"Active binary v0.100.110 is older than Gokin checkout 0.100.133",
		"Rebuild this checkout and replace",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor stale-binary output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "All systems working properly") {
		t.Fatalf("stale binary incorrectly received a clean diagnosis:\n%s", out)
	}
}

func TestDoctorReportsMatchingCheckoutWithoutIssue(t *testing.T) {
	root := makeDoctorCheckout(t, "0.100.133")
	executable := makeDoctorExecutable(t, root, time.Now().Add(time.Hour))
	nested := filepath.Join(root, "internal", "app")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	out := RenderDoctor(DoctorOptions{
		Version:        "v0.100.133-dirty",
		Config:         doctorCheckoutConfig(),
		WorkDir:        nested,
		ExecutablePath: executable,
	})
	if !strings.Contains(out, "Active binary matches checkout version 0.100.133") {
		t.Fatalf("doctor omitted matching checkout diagnosis:\n%s", out)
	}
	if !strings.Contains(out, "All systems working properly") {
		t.Fatalf("matching binary created a false issue:\n%s", out)
	}
}

func TestDoctorWarnsWhenSameVersionBinaryPredatesSourceChange(t *testing.T) {
	root := makeDoctorCheckout(t, "0.100.133")
	executable := makeDoctorExecutable(t, root, time.Now().Add(-time.Hour))
	changedFile := filepath.Join(root, "internal", "app", "new_fix.go")
	if err := os.MkdirAll(filepath.Dir(changedFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(changedFile, []byte("package app\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := RenderDoctor(DoctorOptions{
		Version:        "0.100.133",
		Config:         doctorCheckoutConfig(),
		WorkDir:        root,
		ExecutablePath: executable,
	})
	for _, want := range []string{
		"Active binary 0.100.133 predates checkout change internal/app/new_fix.go",
		"Active binary 0.100.133 predates same-version checkout changes",
		"Rebuild this checkout and replace",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor same-version stale output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "All systems working properly") {
		t.Fatalf("same-version stale binary received a clean diagnosis:\n%s", out)
	}
}

func TestDoctorIgnoresNewerTestOnlyChange(t *testing.T) {
	root := makeDoctorCheckout(t, "0.100.133")
	executableTime := time.Now().Add(time.Hour)
	executable := makeDoctorExecutable(t, root, executableTime)
	testFile := filepath.Join(root, "internal", "app", "new_fix_test.go")
	if err := os.MkdirAll(filepath.Dir(testFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testFile, []byte("package app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	newerThanExecutable := executableTime.Add(time.Hour)
	if err := os.Chtimes(testFile, newerThanExecutable, newerThanExecutable); err != nil {
		t.Fatal(err)
	}

	out := RenderDoctor(DoctorOptions{
		Version:        "0.100.133",
		Config:         doctorCheckoutConfig(),
		WorkDir:        root,
		ExecutablePath: executable,
	})
	if strings.Contains(out, "predates checkout change") {
		t.Fatalf("test-only source change incorrectly marked the binary stale:\n%s", out)
	}
	if !strings.Contains(out, "Active binary matches checkout version 0.100.133") {
		t.Fatalf("doctor omitted matching checkout diagnosis:\n%s", out)
	}
}

func TestDoctorTreatsEmbeddedREPLWorkerAsBuildInput(t *testing.T) {
	root := makeDoctorCheckout(t, "0.100.133")
	executableTime := time.Now().Add(-time.Hour)
	executable := makeDoctorExecutable(t, root, executableTime)
	worker := filepath.Join(root, "internal", "repl", "worker.py")
	if err := os.MkdirAll(filepath.Dir(worker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worker, []byte("print('embedded')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	newer := executableTime.Add(2 * time.Hour)
	if err := os.Chtimes(worker, newer, newer); err != nil {
		t.Fatal(err)
	}
	out := RenderDoctor(DoctorOptions{
		Version: "0.100.133", Config: doctorCheckoutConfig(),
		WorkDir: root, ExecutablePath: executable,
	})
	if !strings.Contains(out, "predates checkout change internal/repl/worker.py") {
		t.Fatalf("embedded worker change did not mark binary stale:\n%s", out)
	}
}

func TestDoctorIgnoresUnrelatedRepository(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := RenderDoctor(DoctorOptions{
		Version: "0.1.0",
		Config:  doctorCheckoutConfig(),
		WorkDir: root,
	})
	if strings.Contains(out, "checkout version") || strings.Contains(out, "older than checkout") {
		t.Fatalf("unrelated repository received Gokin checkout diagnosis:\n%s", out)
	}
}

func TestCompareDoctorVersionCore(t *testing.T) {
	tests := []struct {
		runtime string
		source  string
		want    int
		ok      bool
	}{
		{runtime: "v0.100.110", source: "0.100.133", want: -1, ok: true},
		{runtime: "0.100.133-dirty", source: "v0.100.133", want: 0, ok: true},
		{runtime: "1.0.0", source: "0.100.999", want: 1, ok: true},
		{runtime: "development", source: "0.100.133", want: 0, ok: false},
	}
	for _, tt := range tests {
		got, ok := compareDoctorVersionCore(tt.runtime, tt.source)
		if got != tt.want || ok != tt.ok {
			t.Errorf("compareDoctorVersionCore(%q, %q) = (%d, %v), want (%d, %v)",
				tt.runtime, tt.source, got, ok, tt.want, tt.ok)
		}
	}
}
