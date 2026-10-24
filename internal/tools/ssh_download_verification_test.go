package tools

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	gokinssh "gokin/internal/ssh"
	"gokin/internal/testkit"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"google.golang.org/genai"
)

func TestSSHDownloadInvalidatesVerificationAndDeclaresWrites(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	handlers := sftp.InMemHandler()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		netConn, err := listener.Accept()
		if err != nil {
			return
		}
		conn, channels, requests, err := ssh.NewServerConn(netConn, config)
		if err != nil {
			netConn.Close()
			return
		}
		defer conn.Close()
		go ssh.DiscardRequests(requests)
		for channel := range channels {
			ch, requests, err := channel.Accept()
			if err != nil {
				return
			}
			go func() {
				defer ch.Close()
				for request := range requests {
					if request.Type == "subsystem" {
						request.Reply(true, nil)
						server := sftp.NewRequestServer(ch, handlers)
						defer server.Close()
						server.Serve()
						return
					}
					request.Reply(false, nil)
				}
			}()
		}
	}()
	work := testkit.ResolvedTempDir(t)
	hosts := filepath.Join(work, "known_hosts")
	if err := os.WriteFile(hosts, []byte(knownhosts.Line([]string{listener.Addr().String()}, signer.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tool := NewSSHTool()
	t.Cleanup(tool.Cleanup)
	ctx, state := ContextWithHeadlessPermissions(t.Context(), false, "default")
	port := listener.Addr().(*net.TCPAddr).Port
	client, err := tool.sessionManager.GetOrCreate(ctx, &gokinssh.SSHConfig{Host: "127.0.0.1", Port: port, User: "fixture", Password: "fixture", KnownHostsPath: hosts, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	source, target := filepath.Join(work, "source.go"), filepath.Join(work, "target.go")
	if err := os.WriteFile(source, []byte("new source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("verified source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := client.Upload(ctx, source, "/remote.go"); err != nil {
		t.Fatal(err)
	}
	RecordHeadlessVerification(ctx, "earlier tests", "passed")
	args := map[string]any{"action": "download", "host": "127.0.0.1", "port": port, "user": "fixture", "remote_path": "/remote.go", "local_path": target}
	result, err := tool.downloadFile(ctx, args)
	ObserveHeadlessToolResult(ctx, &genai.FunctionCall{Name: "ssh", Args: args}, result)
	data, readErr := os.ReadFile(target)
	paths := WrittenPathsFromResult(result)
	if err != nil || !result.Success || readErr != nil || string(data) != "new source" || len(paths) != 1 || paths[0] != target || state.Verification().Status != "not_run" {
		t.Fatalf("download lost write evidence: result=%+v err=%v data=%s readErr=%v paths=%v receipt=%+v", result, err, data, readErr, paths, state.Verification())
	}
}
