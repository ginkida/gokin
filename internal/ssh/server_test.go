package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// ---------------------------------------------------------------------------
// In-process SSH test server
// ---------------------------------------------------------------------------

// testSSHServer is a lightweight in-process SSH server for integration tests.
// It supports exec requests (with a customizable handler) and the SFTP
// subsystem (via sftp.InMemHandler with a shared in-memory filesystem).
type testSSHServer struct {
	listener net.Listener
	config   *ssh.ServerConfig
	sftpH    sftp.Handlers
	execFn   func(ch ssh.Channel, cmd string) int
}

type testServerOpt func(*testSSHServer)

func withExecFn(fn func(ssh.Channel, string) int) testServerOpt {
	return func(ts *testSSHServer) { ts.execFn = fn }
}

func newTestSSHServer(t *testing.T, opts ...testServerOpt) *testSSHServer {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}

	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ts := &testSSHServer{
		listener: lis,
		config:   config,
		sftpH:    sftp.InMemHandler(),
		execFn: func(ch ssh.Channel, cmd string) int {
			ch.Write([]byte("ok\n"))
			return 0
		},
	}
	for _, opt := range opts {
		opt(ts)
	}

	go ts.serve()
	t.Cleanup(func() { lis.Close() })
	return ts
}

func (ts *testSSHServer) port() int {
	return ts.listener.Addr().(*net.TCPAddr).Port
}

func (ts *testSSHServer) serve() {
	for {
		conn, err := ts.listener.Accept()
		if err != nil {
			return
		}
		go ts.handleConn(conn)
	}
}

func (ts *testSSHServer) handleConn(nConn net.Conn) {
	conn, chans, reqs, err := ssh.NewServerConn(nConn, ts.config)
	if err != nil {
		nConn.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(ssh.UnknownChannelType, "only session")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go ts.handleSession(ch, chReqs)
	}
}

func (ts *testSSHServer) handleSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "exec":
			var payload struct{ Command string }
			ssh.Unmarshal(req.Payload, &payload)
			req.Reply(true, nil)
			code := ts.execFn(ch, payload.Command)
			ch.SendRequest("exit-status", false,
				ssh.Marshal(struct{ Code uint32 }{uint32(code)}))
			return

		case "subsystem":
			var payload struct{ Name string }
			ssh.Unmarshal(req.Payload, &payload)
			if payload.Name == "sftp" {
				req.Reply(true, nil)
				sftp.NewRequestServer(ch, ts.sftpH).Serve()
				return
			}
			req.Reply(false, nil)

		default:
			req.Reply(false, nil)
		}
	}
}

func testClientConfig(ts *testSSHServer) *SSHConfig {
	return &SSHConfig{
		Host:     "127.0.0.1",
		Port:     ts.port(),
		User:     "test",
		Password: "test",
		Timeout:  5 * time.Second,
	}
}

// ---------------------------------------------------------------------------
// Connect / IsConnected / Close lifecycle
// ---------------------------------------------------------------------------

func TestSSHClient_Lifecycle(t *testing.T) {
	ts := newTestSSHServer(t)
	client := NewSSHClient(testClientConfig(ts))

	if client.IsConnected() {
		t.Error("should not be connected before Connect")
	}

	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if !client.IsConnected() {
		t.Error("should be connected after Connect")
	}

	if err := client.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	if client.IsConnected() {
		t.Error("should not be connected after Close")
	}
}

func TestConnect_AlreadyConnected(t *testing.T) {
	ts := newTestSSHServer(t)
	client := NewSSHClient(testClientConfig(ts))
	defer client.Close()

	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("first Connect: %v", err)
	}

	// Second call hits the keepalive-shortcut path (c.conn != nil, err == nil).
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("second Connect (keepalive): %v", err)
	}
}

// ---------------------------------------------------------------------------
// Execute — happy paths
// ---------------------------------------------------------------------------

func TestExecute_Success(t *testing.T) {
	ts := newTestSSHServer(t, withExecFn(func(ch ssh.Channel, cmd string) int {
		ch.Write([]byte("hello world\n"))
		return 0
	}))
	client := NewSSHClient(testClientConfig(ts))
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	output, exitCode, err := client.Execute(ctx, "echo hello")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}
	if output != "hello world\n" {
		t.Errorf("output = %q, want %q", output, "hello world\n")
	}
}

func TestExecute_WithStderr(t *testing.T) {
	ts := newTestSSHServer(t, withExecFn(func(ch ssh.Channel, cmd string) int {
		ch.Write([]byte("stdout-line\n"))
		ch.Stderr().Write([]byte("stderr-line\n"))
		return 0
	}))
	client := NewSSHClient(testClientConfig(ts))
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	output, _, err := client.Execute(ctx, "cmd")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !contains(output, "stdout-line") {
		t.Errorf("output missing stdout: %q", output)
	}
	if !contains(output, "STDERR:") {
		t.Errorf("output missing STDERR marker: %q", output)
	}
	if !contains(output, "stderr-line") {
		t.Errorf("output missing stderr content: %q", output)
	}
}

func TestExecute_NonZeroExit(t *testing.T) {
	ts := newTestSSHServer(t, withExecFn(func(ch ssh.Channel, cmd string) int {
		ch.Write([]byte("partial output\n"))
		return 42
	}))
	client := NewSSHClient(testClientConfig(ts))
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	output, exitCode, err := client.Execute(ctx, "failing-cmd")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if exitCode != 42 {
		t.Errorf("exitCode = %d, want 42", exitCode)
	}
	if output != "partial output\n" {
		t.Errorf("output = %q", output)
	}
}

func TestExecute_CancelledContext(t *testing.T) {
	ts := newTestSSHServer(t, withExecFn(func(ch ssh.Channel, cmd string) int {
		time.Sleep(30 * time.Second)
		return 0
	}))
	client := NewSSHClient(testClientConfig(ts))
	defer client.Close()

	// Pre-connect so Connect returns instantly and the cancel lands during Run.
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("pre-Connect: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, _, err := client.Execute(ctx, "long-running")
	if err == nil {
		t.Error("Execute should fail with cancelled context")
	}
}

// ---------------------------------------------------------------------------
// Upload / Download via SFTP
// ---------------------------------------------------------------------------

func TestSSHClient_UploadSuccess(t *testing.T) {
	ts := newTestSSHServer(t)
	client := NewSSHClient(testClientConfig(ts))
	defer client.Close()

	// Create a local file to upload.
	localPath := filepath.Join(t.TempDir(), "upload.txt")
	content := []byte("upload test content\n")
	if err := os.WriteFile(localPath, content, 0644); err != nil {
		t.Fatalf("write local file: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.Upload(ctx, localPath, "/remote_upload.txt"); err != nil {
		t.Fatalf("Upload: %v", err)
	}
}

func TestSSHClient_DownloadSuccess(t *testing.T) {
	ts := newTestSSHServer(t)
	client := NewSSHClient(testClientConfig(ts))
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Upload first to populate the shared in-memory SFTP filesystem.
	uploadPath := filepath.Join(t.TempDir(), "source.txt")
	content := "download me\n"
	if err := os.WriteFile(uploadPath, []byte(content), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := client.Upload(ctx, uploadPath, "/downloadable.txt"); err != nil {
		t.Fatalf("Upload (setup): %v", err)
	}

	// Now download.
	downloadPath := filepath.Join(t.TempDir(), "downloaded.txt")
	if err := client.Download(ctx, "/downloadable.txt", downloadPath); err != nil {
		t.Fatalf("Download: %v", err)
	}

	got, err := os.ReadFile(downloadPath)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(got) != content {
		t.Errorf("downloaded content = %q, want %q", string(got), content)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(s != "" && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
