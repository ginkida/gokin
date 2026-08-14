//go:build linux

package repl

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBubblewrapAppliesWorkerSeccompBeforePython(t *testing.T) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bubblewrap unavailable")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	workDir := t.TempDir()
	runtimeDir := t.TempDir()
	for label, target := range map[string]*string{
		"workdir": &workDir, "runtime": &runtimeDir, "python": &python,
	} {
		resolved, resolveErr := filepath.EvalSymlinks(*target)
		if resolveErr != nil {
			t.Fatalf("resolve %s: %v", label, resolveErr)
		}
		*target = resolved
	}
	filter, err := openWorkerSeccompFilter(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := json.Marshal([]string{
		"/gokin-must-not-write", "/tmp/gokin-must-not-write",
		"/dev/gokin-must-not-write",
		filepath.Join(workDir, "gokin-must-not-write"),
		filepath.Join(runtimeDir, "gokin-must-not-write"),
	})
	if err != nil {
		t.Fatal(err)
	}
	code := `import errno, os
with open("/dev/null", "wb") as sink:
    sink.write(b"device-io-still-works")
for target in ` + string(targets) + `:
    try:
        fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except OSError as exc:
        if exc.errno not in (errno.EACCES, errno.EPERM, errno.EROFS):
            raise
    else:
        os.close(fd)
        raise RuntimeError("write unexpectedly succeeded: " + target)
try:
    pid = os.fork()
except OSError as exc:
    if exc.errno != errno.EPERM:
        raise
else:
    if pid == 0:
        os._exit(0)
    os.waitpid(pid, 0)
    raise RuntimeError("fork unexpectedly succeeded")
print("bubblewrap-seccomp-ok")`
	cmd := buildBubblewrapCommand(
		t.Context(), bwrap, workDir, runtimeDir,
		python, []string{"-I", "-c", code}, filter,
	)
	cmd.Dir = workDir
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + runtimeDir,
		"TMPDIR=/tmp", "PYTHONDONTWRITEBYTECODE=1",
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		closeCommandExtraFiles(cmd)
		t.Fatalf("start bubblewrap seccomp probe: %v", err)
	}
	closeCommandExtraFiles(cmd)
	err = cmd.Wait()
	if err != nil || strings.TrimSpace(output.String()) != "bubblewrap-seccomp-ok" {
		t.Fatalf("bubblewrap seccomp probe: output=%q err=%v", output.String(), err)
	}
}
