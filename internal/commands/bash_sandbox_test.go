package commands

import (
	"strings"
	"testing"

	"gokin/internal/config"
	"gokin/internal/repl"
	"gokin/internal/security"
)

func TestDoctorDistinguishesBashAndREPLSandbox(t *testing.T) {
	status := security.SandboxStatus{Status: "unavailable", Backend: "bubblewrap", Network: "host", Reason: "user namespaces denied"}
	replStatus := repl.Availability{Available: true, Backend: repl.BackendBubblewrap}
	output := RenderDoctor(DoctorOptions{Config: config.DefaultConfig(), WorkDir: t.TempDir(), BashSandbox: &status, HybridAvailability: &replStatus})
	for _, want := range []string{"Bash sandbox: unavailable (bubblewrap)", "fs: unrestricted", "network: host", "user namespaces denied", "Stateful hybrid available", "Hooks are privileged", "SSH intentionally"} {
		if !strings.Contains(output, want) {
			t.Fatalf("doctor omitted %q:\n%s", want, output)
		}
	}
}

func TestDoctorReportsDarwinHostLocalNetworkBoundary(t *testing.T) {
	status := security.SandboxStatus{
		Status: "enforced", Backend: "sandbox-exec", Network: "loopback-host",
		Reason: "sandbox-exec allows connections to all addresses of this host (loopback and local interfaces); other hosts are blocked",
	}
	cfg := config.DefaultConfig()
	cfg.Engine.Mode = "tools"
	output := RenderDoctor(DoctorOptions{Config: cfg, WorkDir: t.TempDir(), BashSandbox: &status})
	if !strings.Contains(output, status.Reason) {
		t.Fatalf("doctor omitted the effective host-local boundary:\n%s", output)
	}
}

func TestDoctorDoesNotAdvertiseFallbackForUnsafeScope(t *testing.T) {
	status := security.SandboxStatus{Status: "unavailable", Backend: "bubblewrap", Network: "blocked", Reason: "unsafe sandbox scope"}
	cfg := config.DefaultConfig()
	cfg.Engine.Mode = "tools"
	output := RenderDoctor(DoctorOptions{Config: cfg, WorkDir: t.TempDir(), BashSandbox: &status})
	if !strings.Contains(output, "command execution is refused") || strings.Contains(output, "commands run UNSANDBOXED") {
		t.Fatalf("unsafe scope advertised fallback: %s", output)
	}
}
