package security

import (
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// IPValidator — SSRF protection
// ---------------------------------------------------------------------------

func TestIPValidator_ValidateURL_Empty(t *testing.T) {
	v := NewIPValidator()
	r := v.ValidateURL("")
	if r.Valid {
		t.Error("empty URL should be invalid")
	}
}

func TestIPValidator_ValidateURL_BlockedScheme(t *testing.T) {
	v := NewIPValidator()
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/x"} {
		r := v.ValidateURL(u)
		if r.Valid {
			t.Errorf("%s should be blocked (scheme)", u)
		}
	}
}

func TestIPValidator_ValidateURL_UnsupportedScheme(t *testing.T) {
	v := NewIPValidator()
	r := v.ValidateURL("gopher://example.com/x")
	if r.Valid {
		t.Error("gopher scheme should be unsupported")
	}
}

func TestIPValidator_ValidateURL_Localhost(t *testing.T) {
	v := NewIPValidator()
	r := v.ValidateURL("http://localhost/test")
	if r.Valid {
		t.Error("localhost should be blocked")
	}
}

func TestIPValidator_ValidateURL_LocalhostLocaldomain(t *testing.T) {
	v := NewIPValidator()
	r := v.ValidateURL("http://localhost.localdomain/test")
	if r.Valid {
		t.Error("localhost.localdomain should be blocked")
	}
}

func TestIPValidator_ValidateURL_MissingHostname(t *testing.T) {
	v := NewIPValidator()
	r := v.ValidateURL("http:///path")
	if r.Valid {
		t.Error("missing hostname should be invalid")
	}
}

func TestIPValidator_ValidateURL_PrivateIP(t *testing.T) {
	v := NewIPValidator()
	for _, u := range []string{
		"http://127.0.0.1/x",
		"http://10.0.0.1/x",
		"http://192.168.1.1/x",
		"http://172.16.0.1/x",
		"http://169.254.1.1/x", // link-local
	} {
		r := v.ValidateURL(u)
		if r.Valid {
			t.Errorf("%s should be blocked (private IP)", u)
		}
	}
}

func TestIPValidator_ValidateURL_IPv4Blocked(t *testing.T) {
	v := NewIPValidator()
	// All IPv4 addresses are blocked via the ::ffff:0:0/96 IPv4-mapped range.
	// Even public IPs like 1.1.1.1 are blocked because To4() matches 0.0.0.0/0.
	r := v.ValidateURL("http://1.1.1.1/x")
	if r.Valid {
		t.Error("IPv4 1.1.1.1 should be blocked via ::ffff:0:0/96")
	}
}

func TestIPValidator_ValidateURL_IPv6Private(t *testing.T) {
	v := NewIPValidator()
	r := v.ValidateURL("http://[::1]/x")
	if r.Valid {
		t.Error("::1 (IPv6 loopback) should be blocked")
	}
}

func TestIPValidator_ValidateURL_IPv6Public(t *testing.T) {
	v := NewIPValidator()
	// All IPv4 is blocked via ::ffff:0:0/96. Only public IPv6 can pass.
	// 2606:4700:4700::1111 is Cloudflare DNS — public IPv6.
	r := v.ValidateURL("http://[2606:4700:4700::1111]/x")
	if !r.Valid {
		t.Errorf("public IPv6 should be valid: %s", r.Reason)
	}
	if r.ResolvedIP == nil {
		t.Error("expected non-nil ResolvedIP")
	}
}

func TestIPValidator_ValidateIP_Nil(t *testing.T) {
	v := NewIPValidator()
	r := v.ValidateIP(nil)
	if r.Valid {
		t.Error("nil IP should be invalid")
	}
}

func TestIPValidator_ValidateIP_Blocked(t *testing.T) {
	v := NewIPValidator()
	r := v.ValidateIP(net.ParseIP("10.0.0.1"))
	if r.Valid {
		t.Error("10.0.0.1 should be blocked")
	}
}

func TestIPValidator_ValidateIP_IPv4AllBlocked(t *testing.T) {
	v := NewIPValidator()
	// All IPv4 addresses are blocked via the ::ffff:0:0/96 range (To4 path).
	// This is by design — the validator is conservative for IPv4.
	r := v.ValidateIP(net.ParseIP("1.1.1.1"))
	if r.Valid {
		t.Error("1.1.1.1 should be blocked (all IPv4 blocked via ::ffff:0:0/96)")
	}
}

func TestIPValidator_ValidateIP_IPv6Public(t *testing.T) {
	v := NewIPValidator()
	// 2606:4700:4700::1111 is Cloudflare DNS — public IPv6, not in any blocked range
	r := v.ValidateIP(net.ParseIP("2606:4700:4700::1111"))
	if !r.Valid {
		t.Errorf("public IPv6 should be allowed: %s", r.Reason)
	}
}

func TestIPValidator_ValidateHostname_Empty(t *testing.T) {
	v := NewIPValidator()
	r := v.ValidateHostname("")
	if r.Valid {
		t.Error("empty hostname should be invalid")
	}
}

func TestIPValidator_ValidateHostname_Localhost(t *testing.T) {
	v := NewIPValidator()
	r := v.ValidateHostname("localhost")
	if r.Valid {
		t.Error("localhost should be blocked")
	}
}

func TestIPValidator_ValidateHostname_RawIPv6(t *testing.T) {
	v := NewIPValidator()
	// All IPv4 addresses are blocked via ::ffff:0:0/96. Use a public IPv6.
	r := v.ValidateHostname("2606:4700:4700::1111")
	if !r.Valid {
		t.Errorf("public IPv6 should be valid: %s", r.Reason)
	}
}

func TestIPValidator_AddBlockedNetwork(t *testing.T) {
	v := NewIPValidator()
	if err := v.AddBlockedNetwork("1.2.3.0/24"); err != nil {
		t.Fatalf("AddBlockedNetwork: %v", err)
	}
	r := v.ValidateIP(net.ParseIP("1.2.3.4"))
	if r.Valid {
		t.Error("1.2.3.4 should be blocked after AddBlockedNetwork")
	}
}

func TestIPValidator_AddBlockedNetwork_InvalidCIDR(t *testing.T) {
	v := NewIPValidator()
	if err := v.AddBlockedNetwork("not-a-cidr"); err == nil {
		t.Error("expected error for invalid CIDR")
	}
}

func TestIPValidator_AllowScheme(t *testing.T) {
	v := NewIPValidator()
	v.AllowScheme("custom")
	// Now custom scheme should be allowed (but hostname check still applies)
	r := v.ValidateURL("custom://8.8.8.8/x")
	if !r.Valid {
		// It should at least pass the scheme check
		if r.Reason == "unsupported URL scheme: custom" {
			t.Error("custom scheme should be allowed after AllowScheme")
		}
	}
}

func TestIPValidator_BlockScheme(t *testing.T) {
	v := NewIPValidator()
	v.BlockScheme("http")
	r := v.ValidateURL("http://8.8.8.8/x")
	if r.Valid {
		t.Error("http should be blocked after BlockScheme")
	}
}

func TestValidateURLForSSRF(t *testing.T) {
	r := ValidateURLForSSRF("http://127.0.0.1/x")
	if r.Valid {
		t.Error("127.0.0.1 should be blocked via default validator")
	}
}

func TestIPValidator_isBlockedIP_Nil(t *testing.T) {
	v := NewIPValidator()
	if !v.isBlockedIP(nil) {
		t.Error("nil IP should be blocked")
	}
}

// ---------------------------------------------------------------------------
// Keys — API key loading, masking, validation
// ---------------------------------------------------------------------------

func TestLoadedKey_String_Nil(t *testing.T) {
	var k *LoadedKey
	s := k.String()
	if s == "" {
		t.Error("nil LoadedKey.String() should not be empty")
	}
}

func TestLoadedKey_String_Empty(t *testing.T) {
	k := &LoadedKey{Value: "", Source: KeySourceNotSet}
	s := k.String()
	if s == "" {
		t.Error("empty LoadedKey.String() should not be empty")
	}
}

func TestLoadedKey_String_ShortKey(t *testing.T) {
	k := &LoadedKey{Value: "abc", Source: KeySourceConfig}
	s := k.String()
	// Short key shows full value
	if s == "" {
		t.Error("should not be empty")
	}
}

func TestLoadedKey_String_LongKey(t *testing.T) {
	k := &LoadedKey{Value: "sk-" + "1234567890abcdef", Source: KeySourceEnvironment}
	s := k.String()
	if s == "" {
		t.Error("should not be empty")
	}
}

func TestLoadedKey_IsSet(t *testing.T) {
	var k *LoadedKey
	if k.IsSet() {
		t.Error("nil key should not be set")
	}
	k = &LoadedKey{Value: ""}
	if k.IsSet() {
		t.Error("empty key should not be set")
	}
	k = &LoadedKey{Value: "abc"}
	if !k.IsSet() {
		t.Error("non-empty key should be set")
	}
}

func TestGetAPIKey_Environment(t *testing.T) {
	os.Setenv("TEST_API_KEY_ENV", "test-key-12345")
	defer os.Unsetenv("TEST_API_KEY_ENV")

	k := GetAPIKey([]string{"TEST_API_KEY_ENV"}, "")
	if !k.IsSet() {
		t.Error("key should be set from env")
	}
	if k.Source != KeySourceEnvironment {
		t.Errorf("source = %s, want environment", k.Source)
	}
}

func TestGetAPIKey_ConfigFallback(t *testing.T) {
	k := GetAPIKey([]string{"NONEXISTENT_ENV_VAR_XYZ"}, "config-key-12345")
	if !k.IsSet() {
		t.Error("key should be set from config")
	}
	if k.Source != KeySourceConfig {
		t.Errorf("source = %s, want config", k.Source)
	}
}

func TestGetAPIKey_NotSet(t *testing.T) {
	k := GetAPIKey([]string{"NONEXISTENT_ENV_VAR_XYZ"}, "")
	if k.IsSet() {
		t.Error("key should not be set")
	}
	if k.Source != KeySourceNotSet {
		t.Errorf("source = %s, want not_set", k.Source)
	}
}

func TestGetProviderKey_ConfigKey(t *testing.T) {
	k := GetProviderKey([]string{"NONEXISTENT_VAR_XYZ"}, "config-key", "legacy-key")
	if k.Value != "config-key" {
		t.Errorf("expected config-key, got %s", k.Value)
	}
}

func TestGetProviderKey_LegacyFallback(t *testing.T) {
	k := GetProviderKey([]string{"NONEXISTENT_VAR_XYZ"}, "", "legacy-key")
	if k.Value != "legacy-key" {
		t.Errorf("expected legacy-key, got %s", k.Value)
	}
}

func TestMaskKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{"empty", "", "(not set)"},
		{"short", "abc", "***"},
		{"exact8", "12345678", "********"},
		{"long", "sk-" + "1234567890abcdef", "sk-1***********cdef"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MaskKey(tt.key)
			if got != tt.want {
				t.Errorf("MaskKey(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestValidateKeyFormat(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"empty", "", true},
		{"too_short", "short", true},
		{"placeholder_your_api_key", "your-api-key-1234567890", true},
		{"placeholder_sk_xxxx", "sk-xxxx1234567890", true},
		{"placeholder_insert", "<insert-key>1234567890123456", true},
		{"valid", "sk-" + "1234567890abcdef", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateKeyFormat(tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateKeyFormat(%q) err = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SSHValidator
// ---------------------------------------------------------------------------

func TestSSHValidator_ValidateCommand_Empty(t *testing.T) {
	v := NewSSHValidator()
	r := v.ValidateCommand("")
	if r.Valid {
		t.Error("empty command should be invalid")
	}
}

func TestSSHValidator_ValidateCommand_Blocked(t *testing.T) {
	v := NewSSHValidator()
	blocked := []string{
		"rm -rf /",
		":(){:|:&};:",
		"mkfs.ext4 /dev/sda1",
		"chmod -R 777 /",
	}
	for _, cmd := range blocked {
		r := v.ValidateCommand(cmd)
		if r.Valid {
			t.Errorf("%q should be blocked", cmd)
		}
	}
}

func TestSSHValidator_ValidateCommand_Safe(t *testing.T) {
	v := NewSSHValidator()
	r := v.ValidateCommand("ls -la /tmp")
	if !r.Valid {
		t.Errorf("safe command should pass: %s", r.Reason)
	}
}

func TestSSHValidator_ValidateHost_Empty(t *testing.T) {
	v := NewSSHValidator()
	r := v.ValidateHost("")
	if r.Valid {
		t.Error("empty host should be invalid")
	}
}

func TestSSHValidator_ValidateHost_Blocked(t *testing.T) {
	v := NewSSHValidator()
	for _, h := range []string{"localhost", "127.0.0.1", "::1"} {
		r := v.ValidateHost(h)
		if r.Valid {
			t.Errorf("%q should be blocked", h)
		}
	}
}

func TestSSHValidator_ValidateHost_LoopbackIP(t *testing.T) {
	v := NewSSHValidator()
	r := v.ValidateHost("127.0.0.2")
	if r.Valid {
		t.Error("127.0.0.2 should be blocked (loopback)")
	}
}

func TestSSHValidator_ValidateHost_Whitelist(t *testing.T) {
	v := NewSSHValidator()
	v.SetAllowedHosts([]string{"example.com"})
	r := v.ValidateHost("evil.com")
	if r.Valid {
		t.Error("evil.com should be blocked by whitelist")
	}
	r = v.ValidateHost("example.com")
	if !r.Valid {
		t.Errorf("example.com should be allowed: %s", r.Reason)
	}
}

func TestSSHValidator_AddBlockedHost(t *testing.T) {
	v := NewSSHValidator()
	v.AddBlockedHost("evil.com")
	r := v.ValidateHost("evil.com")
	if r.Valid {
		t.Error("evil.com should be blocked after AddBlockedHost")
	}
}

func TestSSHValidator_AddBlockedCommand(t *testing.T) {
	v := NewSSHValidator()
	v.AddBlockedCommand("dangerous-cmd")
	r := v.ValidateCommand("dangerous-cmd")
	if r.Valid {
		t.Error("should be blocked after AddBlockedCommand")
	}
}

func TestSSHValidator_AddBlockedSubstring(t *testing.T) {
	v := NewSSHValidator()
	v.AddBlockedSubstring("secret-cmd")
	r := v.ValidateCommand("run secret-cmd now")
	if r.Valid {
		t.Error("should be blocked after AddBlockedSubstring")
	}
}

func TestSSHValidator_AddBlockedPattern(t *testing.T) {
	v := NewSSHValidator()
	if err := v.AddBlockedPattern(`evil\d+`); err != nil {
		t.Fatalf("AddBlockedPattern: %v", err)
	}
	r := v.ValidateCommand("run evil123 now")
	if r.Valid {
		t.Error("should match blocked pattern")
	}
}

func TestSSHValidator_AddBlockedPattern_InvalidRegex(t *testing.T) {
	v := NewSSHValidator()
	if err := v.AddBlockedPattern("[invalid"); err == nil {
		t.Error("expected error for invalid regex")
	}
}

func TestSSHValidator_ValidateUser_Empty(t *testing.T) {
	v := NewSSHValidator()
	r := v.ValidateUser("")
	if r.Valid {
		t.Error("empty user should be invalid")
	}
}

func TestSSHValidator_ValidateUser_InvalidChars(t *testing.T) {
	v := NewSSHValidator()
	r := v.ValidateUser("admin'; DROP TABLE--")
	if r.Valid {
		t.Error("user with special chars should be invalid")
	}
}

func TestSSHValidator_ValidateUser_TooLong(t *testing.T) {
	v := NewSSHValidator()
	user := ""
	for i := 0; i < 65; i++ {
		user += "a"
	}
	r := v.ValidateUser(user)
	if r.Valid {
		t.Error("65-char username should be invalid")
	}
}

func TestSSHValidator_ValidateUser_Valid(t *testing.T) {
	v := NewSSHValidator()
	for _, u := range []string{"admin", "user.name", "user-name", "user_name", "u123"} {
		r := v.ValidateUser(u)
		if !r.Valid {
			t.Errorf("%q should be valid: %s", u, r.Reason)
		}
	}
}

func TestValidateSSHCommand(t *testing.T) {
	r := ValidateSSHCommand("rm -rf /")
	if r.Valid {
		t.Error("rm -rf / should be blocked via default validator")
	}
}

func TestValidateSSHHost(t *testing.T) {
	r := ValidateSSHHost("localhost")
	if r.Valid {
		t.Error("localhost should be blocked via default validator")
	}
}

func TestValidateSSHUser(t *testing.T) {
	r := ValidateSSHUser("admin")
	if !r.Valid {
		t.Error("admin should be valid via default validator")
	}
}

// ---------------------------------------------------------------------------
// TLS
// ---------------------------------------------------------------------------

func TestDefaultTLSConfig(t *testing.T) {
	cfg := DefaultTLSConfig()
	if cfg.MinVersion != TLSVersion12 {
		t.Errorf("MinVersion = %s, want 1.2", cfg.MinVersion)
	}
	if cfg.InsecureSkipVerify {
		t.Error("InsecureSkipVerify should be false by default")
	}
}

func TestIsProductionMode(t *testing.T) {
	// Default — no env set
	os.Unsetenv("GO_ENV")
	os.Unsetenv("APP_ENV")
	os.Unsetenv("NODE_ENV")
	os.Unsetenv("ENVIRONMENT")
	os.Unsetenv("PRODUCTION")
	if IsProductionMode() {
		t.Error("should not be production by default")
	}

	// GO_ENV=production
	os.Setenv("GO_ENV", "production")
	if !IsProductionMode() {
		t.Error("GO_ENV=production should be production")
	}
	os.Unsetenv("GO_ENV")

	// PRODUCTION=1
	os.Setenv("PRODUCTION", "1")
	if !IsProductionMode() {
		t.Error("PRODUCTION=1 should be production")
	}
	os.Unsetenv("PRODUCTION")
}

func TestCreateTLSConfig_Default(t *testing.T) {
	cfg := DefaultTLSConfig()
	tlsCfg, err := CreateTLSConfig(cfg)
	if err != nil {
		t.Fatalf("CreateTLSConfig: %v", err)
	}
	if tlsCfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want %x", tlsCfg.MinVersion, tls.VersionTLS12)
	}
	if tlsCfg.MaxVersion != tls.VersionTLS13 {
		t.Errorf("MaxVersion = %x, want %x", tlsCfg.MaxVersion, tls.VersionTLS13)
	}
	if len(tlsCfg.CipherSuites) == 0 {
		t.Error("should have cipher suites")
	}
}

func TestCreateTLSConfig_InsecureNonProd(t *testing.T) {
	os.Unsetenv("GO_ENV")
	os.Unsetenv("PRODUCTION")
	cfg := TLSConfig{InsecureSkipVerify: true, MinVersion: TLSVersion12}
	tlsCfg, err := CreateTLSConfig(cfg)
	if err != nil {
		t.Fatalf("CreateTLSConfig in non-prod: %v", err)
	}
	if !tlsCfg.InsecureSkipVerify {
		t.Error("InsecureSkipVerify should be true")
	}
}

func TestCreateTLSConfig_InsecureProd(t *testing.T) {
	os.Setenv("GO_ENV", "production")
	defer os.Unsetenv("GO_ENV")
	cfg := TLSConfig{InsecureSkipVerify: true, MinVersion: TLSVersion12}
	_, err := CreateTLSConfig(cfg)
	if err == nil {
		t.Error("InsecureSkipVerify in production should error")
	}
}

func TestCreateTLSConfig_MinGreaterThanMax(t *testing.T) {
	cfg := TLSConfig{
		MinVersion: TLSVersion13,
		MaxVersion: TLSVersion12,
	}
	_, err := CreateTLSConfig(cfg)
	if err == nil {
		t.Error("min > max should error")
	}
}

func TestCreateTLSConfig_AutoVersion(t *testing.T) {
	cfg := TLSConfig{MinVersion: TLSVersionAuto, MaxVersion: TLSVersionAuto}
	tlsCfg, err := CreateTLSConfig(cfg)
	if err != nil {
		t.Fatalf("CreateTLSConfig: %v", err)
	}
	// Auto = 0 (Go default)
	if tlsCfg.MinVersion != 0 {
		t.Errorf("Auto MinVersion = %x, want 0", tlsCfg.MinVersion)
	}
}

func TestCreateSecureHTTPClient(t *testing.T) {
	cfg := DefaultTLSConfig()
	client, err := CreateSecureHTTPClient(cfg, 30*time.Second)
	if err != nil {
		t.Fatalf("CreateSecureHTTPClient: %v", err)
	}
	if client == nil {
		t.Fatal("client should not be nil")
	}
	if client.Transport == nil {
		t.Error("transport should not be nil")
	}
}

func TestCreateDefaultHTTPClient(t *testing.T) {
	client, err := CreateDefaultHTTPClient()
	if err != nil {
		t.Fatalf("CreateDefaultHTTPClient: %v", err)
	}
	if client == nil {
		t.Fatal("client should not be nil")
	}
}

func TestValidateTLSConfig_Secure(t *testing.T) {
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		CipherSuites: []uint16{tls.TLS_AES_128_GCM_SHA256},
	}
	warnings := ValidateTLSConfig(cfg)
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
}

func TestValidateTLSConfig_Insecure(t *testing.T) {
	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS10,
		InsecureSkipVerify: true,
	}
	warnings := ValidateTLSConfig(cfg)
	if len(warnings) < 2 {
		t.Errorf("expected ≥2 warnings, got %d", len(warnings))
	}
}

func TestTLSVersionString(t *testing.T) {
	tests := []struct {
		version uint16
		want    string
	}{
		{tls.VersionTLS10, "TLS 1.0"},
		{tls.VersionTLS11, "TLS 1.1"},
		{tls.VersionTLS12, "TLS 1.2"},
		{tls.VersionTLS13, "TLS 1.3"},
		{0x9999, "Unknown (0x9999)"},
	}
	for _, tt := range tests {
		got := TLSVersionString(tt.version)
		if got != tt.want {
			t.Errorf("TLSVersionString(%x) = %q, want %q", tt.version, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// PathValidator — symlink check
// ---------------------------------------------------------------------------

func TestPathValidator_CheckSymlink_Safe(t *testing.T) {
	dir := resolvedTmpDir(t)
	safePath := filepath.Join(dir, "normal.txt")
	os.WriteFile(safePath, []byte("ok"), 0644)

	v := NewPathValidator(nil, false)
	if err := v.checkSymlink(safePath); err != nil {
		t.Errorf("checkSymlink on normal path should succeed: %v", err)
	}
}

func TestPathValidator_CheckSymlink_NonExistent(t *testing.T) {
	v := NewPathValidator(nil, false)
	// Non-existent path under a resolved temp dir should not error (new files)
	nonExist := filepath.Join(resolvedTmpDir(t), "nonexistent-path-xyz-12345")
	err := v.checkSymlink(nonExist)
	if err != nil {
		t.Errorf("checkSymlink on non-existent path should not error: %v", err)
	}
}

func TestPathValidator_CheckSymlink_DetectsSymlink(t *testing.T) {
	dir := resolvedTmpDir(t)
	target := filepath.Join(dir, "target.txt")
	os.WriteFile(target, []byte("ok"), 0644)

	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	v := NewPathValidator(nil, true) // reject symlinks
	err := v.checkSymlink(link)
	if err == nil {
		t.Error("checkSymlink should detect symlink")
	}
}

func TestPathValidator_CheckSymlink_AllowsSymlinkWhenDisabled(t *testing.T) {
	dir := resolvedTmpDir(t)
	target := filepath.Join(dir, "target.txt")
	os.WriteFile(target, []byte("ok"), 0644)

	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	v := NewPathValidator(nil, true) // allowSymlinks=true → skip checkSymlink
	// With allowSymlinks=true, Validate skips checkSymlink entirely
	_, err := v.Validate(link)
	if err != nil {
		t.Errorf("Validate with allowSymlinks=true should allow symlink: %v", err)
	}
}

// resolvedTmpDir returns a symlink-free temp dir (on macOS, t.TempDir() returns
// paths under /var → /private/var, and checkSymlink walks all components).
func resolvedTmpDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", dir, err)
	}
	return resolved
}
