package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"os/user"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// generateTestKey writes a valid PKCS8-encoded ed25519 private key to a temp
// file and returns its path. ssh.ParsePrivateKey supports PKCS8, so this
// exercises the real key-loading path in buildSSHConfig without depending on
// ssh-keygen being installed.
func generateTestKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})
	path := filepath.Join(t.TempDir(), "test_ed25519")
	if err := os.WriteFile(path, pemBytes, 0600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path
}

// generateTestKnownHosts creates a valid known_hosts file with a single
// localhost entry and returns its path.
func generateTestKnownHosts(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("new public key: %v", err)
	}
	line := "localhost " + sshPub.Type() + " " + base64.StdEncoding.EncodeToString(sshPub.Marshal()) + "\n"
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(line), 0644); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------------
// SSHConfig / NewSSHClient
// ---------------------------------------------------------------------------

func TestDefaultSSHConfig(t *testing.T) {
	cfg := DefaultSSHConfig()
	if cfg.Port != 22 {
		t.Errorf("Port = %d, want 22", cfg.Port)
	}
	if cfg.User == "" {
		t.Error("User should not be empty")
	}
	if cfg.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", cfg.Timeout)
	}
	if cfg.KeyPath == "" {
		t.Error("KeyPath should not be empty")
	}
	if cfg.KnownHostsPath == "" {
		t.Error("KnownHostsPath should not be empty")
	}
}

func TestNewSSHClient(t *testing.T) {
	cfg := &SSHConfig{Host: "test", Port: 22, User: "user"}
	client := NewSSHClient(cfg)
	if client.config != cfg {
		t.Error("config not set")
	}
	if client.LastUse().IsZero() {
		t.Error("LastUse should not be zero after construction")
	}
}

// ---------------------------------------------------------------------------
// Accessors: SessionKey, LastUse, Close, IsConnected
// ---------------------------------------------------------------------------

func TestSSHClientSessionKey(t *testing.T) {
	client := NewSSHClient(&SSHConfig{Host: "myhost", Port: 2222, User: "myuser"})
	key := client.SessionKey()
	if key != "myuser@myhost:2222" {
		t.Errorf("SessionKey = %q, want %q", key, "myuser@myhost:2222")
	}
}

func TestSSHClientLastUse(t *testing.T) {
	// LastUse is stamped inside NewSSHClient, so `before` must be taken
	// BEFORE construction. The original ordering (before := after the
	// constructor) passed on macOS only because its coarser clock made all
	// three timestamps equal; linux CI's ns resolution exposed LastUse
	// landing ~0.3µs before `before`.
	before := time.Now()
	client := NewSSHClient(&SSHConfig{})
	lastUse := client.LastUse()
	after := time.Now()
	if lastUse.Before(before) || lastUse.After(after) {
		t.Errorf("LastUse = %v, expected between %v and %v", lastUse, before, after)
	}
}

func TestSSHClientClose_NoConnection(t *testing.T) {
	client := NewSSHClient(&SSHConfig{})
	err := client.Close()
	if err != nil {
		t.Errorf("Close on nil conn should return nil, got %v", err)
	}
}

func TestSSHClientIsConnected_NoConnection(t *testing.T) {
	client := NewSSHClient(&SSHConfig{})
	if client.IsConnected() {
		t.Error("IsConnected should be false for nil conn")
	}
}

// ---------------------------------------------------------------------------
// expandPath
// ---------------------------------------------------------------------------

func TestExpandPath_Tilde(t *testing.T) {
	result := expandPath("~/test/path")
	usr, err := user.Current()
	if err != nil {
		t.Skip("user.Current() not available")
	}
	expected := filepath.Join(usr.HomeDir, "test/path")
	if result != expected {
		t.Errorf("expandPath(~/test/path) = %q, want %q", result, expected)
	}
}

func TestExpandPath_Absolute(t *testing.T) {
	result := expandPath("/absolute/path")
	if result != "/absolute/path" {
		t.Errorf("expandPath(/absolute/path) = %q", result)
	}
}

func TestExpandPath_Relative(t *testing.T) {
	result := expandPath("relative/path")
	if result != "relative/path" {
		t.Errorf("expandPath(relative/path) = %q", result)
	}
}

func TestExpandPath_TildeWithoutSlash(t *testing.T) {
	// "~something" does not start with "~/", so it's returned as-is
	result := expandPath("~something")
	if result != "~something" {
		t.Errorf("expandPath(~something) = %q, want %q", result, "~something")
	}
}

// ---------------------------------------------------------------------------
// buildSSHConfig
// ---------------------------------------------------------------------------

func TestBuildSSHConfig_PasswordOnly(t *testing.T) {
	client := NewSSHClient(&SSHConfig{
		User:     "test",
		Password: "secret",
		Timeout:  10 * time.Second,
	})
	cfg, err := client.buildSSHConfig()
	if err != nil {
		t.Fatalf("buildSSHConfig: %v", err)
	}
	if cfg.User != "test" {
		t.Errorf("User = %q", cfg.User)
	}
	if len(cfg.Auth) < 1 {
		t.Errorf("Auth methods = %d, want >= 1", len(cfg.Auth))
	}
}

func TestBuildSSHConfig_WithKey(t *testing.T) {
	keyPath := generateTestKey(t)
	client := NewSSHClient(&SSHConfig{
		User:    "test",
		KeyPath: keyPath,
		Timeout: 10 * time.Second,
	})
	cfg, err := client.buildSSHConfig()
	if err != nil {
		t.Fatalf("buildSSHConfig: %v", err)
	}
	if len(cfg.Auth) < 1 {
		t.Error("expected at least 1 auth method from key")
	}
}

func TestBuildSSHConfig_KeyAndPassword(t *testing.T) {
	keyPath := generateTestKey(t)
	client := NewSSHClient(&SSHConfig{
		User:     "test",
		KeyPath:  keyPath,
		Password: "secret",
		Timeout:  10 * time.Second,
	})
	cfg, err := client.buildSSHConfig()
	if err != nil {
		t.Fatalf("buildSSHConfig: %v", err)
	}
	// Key from KeyPath + password = 2 auth methods
	// (fallback loop is skipped because authMethods already has 1 from KeyPath)
	if len(cfg.Auth) < 2 {
		t.Errorf("Auth methods = %d, want >= 2 (key + password)", len(cfg.Auth))
	}
}

func TestBuildSSHConfig_KeyNonExistent_WithPassword(t *testing.T) {
	client := NewSSHClient(&SSHConfig{
		User:     "test",
		KeyPath:  "/nonexistent/key",
		Password: "secret",
		Timeout:  10 * time.Second,
	})
	cfg, err := client.buildSSHConfig()
	if err != nil {
		t.Fatalf("buildSSHConfig: %v", err)
	}
	// At least password auth; default SSH keys may add one more
	if len(cfg.Auth) < 1 {
		t.Errorf("Auth methods = %d, want >= 1 (password fallback)", len(cfg.Auth))
	}
}

// ---------------------------------------------------------------------------
// buildHostKeyCallback
// ---------------------------------------------------------------------------

func TestBuildHostKeyCallback_EmptyPath(t *testing.T) {
	client := NewSSHClient(&SSHConfig{})
	cb, err := client.buildHostKeyCallback()
	if err != nil {
		t.Fatalf("buildHostKeyCallback: %v", err)
	}
	if cb == nil {
		t.Error("callback should not be nil")
	}
}

func TestBuildHostKeyCallback_NonExistentFile(t *testing.T) {
	client := NewSSHClient(&SSHConfig{
		KnownHostsPath: "/nonexistent/known_hosts",
	})
	cb, err := client.buildHostKeyCallback()
	if err != nil {
		t.Fatalf("buildHostKeyCallback: %v", err)
	}
	if cb == nil {
		t.Error("callback should not be nil (falls back to insecure)")
	}
}

func TestBuildHostKeyCallback_ValidFile(t *testing.T) {
	khPath := generateTestKnownHosts(t)
	client := NewSSHClient(&SSHConfig{
		KnownHostsPath: khPath,
	})
	cb, err := client.buildHostKeyCallback()
	if err != nil {
		t.Fatalf("buildHostKeyCallback: %v", err)
	}
	if cb == nil {
		t.Error("callback should not be nil")
	}
}

func TestBuildHostKeyCallback_InvalidFile(t *testing.T) {
	// Create a file with invalid content that knownhosts.New can't parse
	path := filepath.Join(t.TempDir(), "bad_known_hosts")
	os.WriteFile(path, []byte("this is not valid known_hosts format\n"), 0644)

	client := NewSSHClient(&SSHConfig{
		KnownHostsPath: path,
	})
	cb, err := client.buildHostKeyCallback()
	if err != nil {
		t.Fatalf("buildHostKeyCallback: %v", err)
	}
	if cb == nil {
		t.Error("callback should not be nil (falls back to insecure on parse error)")
	}
}

// ---------------------------------------------------------------------------
// Connect — error paths (no real SSH server needed)
// ---------------------------------------------------------------------------

func TestSSHClientConnect_ConnectionRefused(t *testing.T) {
	client := NewSSHClient(&SSHConfig{
		Host:     "127.0.0.1",
		Port:     1, // port 1 is rarely listening
		User:     "test",
		Password: "test",
		Timeout:  100 * time.Millisecond,
	})
	err := client.Connect(context.Background())
	if err == nil {
		t.Error("Connect should fail for non-listening port")
	}
}

func TestSSHClientConnect_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := NewSSHClient(&SSHConfig{
		Host:     "127.0.0.1",
		Port:     1,
		User:     "test",
		Password: "test",
		Timeout:  100 * time.Millisecond,
	})
	err := client.Connect(ctx)
	if err == nil {
		t.Error("Connect should fail with cancelled context")
	}
}

// ---------------------------------------------------------------------------
// Execute / Upload / Download — error paths (Connect fails)
// ---------------------------------------------------------------------------

func TestSSHClientExecute_ConnectFails(t *testing.T) {
	client := NewSSHClient(&SSHConfig{
		Host:     "127.0.0.1",
		Port:     1,
		User:     "test",
		Password: "test",
		Timeout:  100 * time.Millisecond,
	})
	output, exitCode, err := client.Execute(context.Background(), "ls")
	if err == nil {
		t.Error("Execute should fail when Connect fails")
	}
	if output != "" {
		t.Errorf("output = %q, want empty", output)
	}
	if exitCode != -1 {
		t.Errorf("exitCode = %d, want -1", exitCode)
	}
}

func TestSSHClientExecute_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := NewSSHClient(&SSHConfig{
		Host:     "127.0.0.1",
		Port:     1,
		User:     "test",
		Password: "test",
		Timeout:  100 * time.Millisecond,
	})
	_, _, err := client.Execute(ctx, "ls")
	if err == nil {
		t.Error("Execute should fail with cancelled context")
	}
}

func TestSSHClientUpload_ConnectFails(t *testing.T) {
	client := NewSSHClient(&SSHConfig{
		Host:     "127.0.0.1",
		Port:     1,
		User:     "test",
		Password: "test",
		Timeout:  100 * time.Millisecond,
	})
	err := client.Upload(context.Background(), "/local", "/remote")
	if err == nil {
		t.Error("Upload should fail when Connect fails")
	}
}

func TestSSHClientDownload_ConnectFails(t *testing.T) {
	client := NewSSHClient(&SSHConfig{
		Host:     "127.0.0.1",
		Port:     1,
		User:     "test",
		Password: "test",
		Timeout:  100 * time.Millisecond,
	})
	err := client.Download(context.Background(), "/remote", "/local")
	if err == nil {
		t.Error("Download should fail when Connect fails")
	}
}

// ---------------------------------------------------------------------------
// SessionManager — Get, Close, CloseAll, CleanupIdle, List, GetOrCreate
// with injected clients (nil conn = disconnected)
// ---------------------------------------------------------------------------

func TestSessionManagerGet_DisconnectedClient(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	// Inject a client with nil conn (disconnected)
	client := NewSSHClient(&SSHConfig{Host: "test", Port: 22, User: "user"})
	sm.sessions["user@test:22"] = client

	// Get should return false because client is not connected
	_, ok := sm.Get("user@test:22")
	if ok {
		t.Error("disconnected client should not be returned by Get")
	}
}

func TestSessionManagerClose_ExistingClient(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	client := NewSSHClient(&SSHConfig{Host: "test", Port: 22, User: "user"})
	sm.sessions["user@test:22"] = client

	err := sm.Close("user@test:22")
	if err != nil {
		t.Errorf("Close: %v", err)
	}
	if sm.Count() != 0 {
		t.Error("session should be removed after Close")
	}
}

func TestSessionManagerCloseAll_WithClients(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	sm.sessions["user1@test:22"] = NewSSHClient(&SSHConfig{Host: "test", Port: 22, User: "user1"})
	sm.sessions["user2@test:22"] = NewSSHClient(&SSHConfig{Host: "test", Port: 22, User: "user2"})

	if sm.Count() != 2 {
		t.Fatalf("Count = %d, want 2", sm.Count())
	}

	sm.CloseAll()
	if sm.Count() != 0 {
		t.Error("all sessions should be closed after CloseAll")
	}
}

func TestSessionManagerCleanupIdle_WithIdleSession(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()
	sm.SetMaxIdle(1 * time.Millisecond)

	client := NewSSHClient(&SSHConfig{Host: "test", Port: 22, User: "user"})
	// Set lastUse to the past so it's considered idle
	client.lastUseNano.Store(time.Now().Add(-1 * time.Hour).UnixNano())
	sm.sessions["user@test:22"] = client

	cleaned := sm.CleanupIdle()
	if cleaned != 1 {
		t.Errorf("CleanupIdle = %d, want 1", cleaned)
	}
	if sm.Count() != 0 {
		t.Error("idle session should be removed")
	}
}

func TestSessionManagerCleanupIdle_NotIdleSession(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()
	sm.SetMaxIdle(1 * time.Hour)

	client := NewSSHClient(&SSHConfig{Host: "test", Port: 22, User: "user"})
	// lastUse is recent (set in NewSSHClient)
	sm.sessions["user@test:22"] = client

	cleaned := sm.CleanupIdle()
	if cleaned != 0 {
		t.Errorf("CleanupIdle = %d, want 0 (session not idle)", cleaned)
	}
	if sm.Count() != 1 {
		t.Error("session should not be removed when not idle")
	}
}

func TestSessionManagerList_WithClient(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	client := NewSSHClient(&SSHConfig{Host: "testhost", Port: 2222, User: "testuser"})
	sm.sessions["testuser@testhost:2222"] = client

	infos := sm.List()
	if len(infos) != 1 {
		t.Fatalf("List() = %d items, want 1", len(infos))
	}
	info := infos[0]
	if info.Key != "testuser@testhost:2222" {
		t.Errorf("Key = %q", info.Key)
	}
	if info.Host != "testhost" {
		t.Errorf("Host = %q", info.Host)
	}
	if info.Port != 2222 {
		t.Errorf("Port = %d", info.Port)
	}
	if info.User != "testuser" {
		t.Errorf("User = %q", info.User)
	}
	if info.Connected {
		t.Error("Connected should be false for nil conn")
	}
}

func TestSessionManagerGetOrCreate_ConnectFails(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	config := &SSHConfig{
		Host:     "127.0.0.1",
		Port:     1,
		User:     "test",
		Password: "test",
		Timeout:  100 * time.Millisecond,
	}

	_, err := sm.GetOrCreate(context.Background(), config)
	if err == nil {
		t.Error("GetOrCreate should fail when connection fails")
	}
	if sm.Count() != 0 {
		t.Error("no session should be stored after failed connection")
	}
}
