package setup

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gokin/internal/config"
)

// withStdinTerminal temporarily overrides isStdinTerminal for a test.
func withStdinTerminal(t *testing.T, isTerminal bool) {
	t.Helper()
	orig := isStdinTerminal
	isStdinTerminal = func() bool { return isTerminal }
	t.Cleanup(func() { isStdinTerminal = orig })
}

// TestReadSecretLine_NonTerminalFallback (round 7) pins the non-masked
// fallback path: when stdin isn't a real terminal (piped input, tests,
// non-interactive invocations), readSecretLine must behave exactly like the
// old plain reader.ReadString('\n') call it replaced — term.ReadPassword
// requires a real TTY and would fail otherwise.
func TestReadSecretLine_NonTerminalFallback(t *testing.T) {
	withStdinTerminal(t, false)

	r := bufio.NewReader(strings.NewReader("my-secret-key-1234\n"))
	got, err := readSecretLine(r)
	if err != nil {
		t.Fatalf("readSecretLine: %v", err)
	}
	if got != "my-secret-key-1234" {
		t.Fatalf("got %q, want %q", got, "my-secret-key-1234")
	}
}

// TestReadSecretLine_NonTerminalTrimsWhitespace confirms the fallback path
// still trims surrounding whitespace like the original ReadString+TrimSpace
// call sites did.
func TestReadSecretLine_NonTerminalTrimsWhitespace(t *testing.T) {
	withStdinTerminal(t, false)

	r := bufio.NewReader(strings.NewReader("  padded-key  \n"))
	got, err := readSecretLine(r)
	if err != nil {
		t.Fatalf("readSecretLine: %v", err)
	}
	if got != "padded-key" {
		t.Fatalf("got %q, want %q", got, "padded-key")
	}
}

// TestReadSecretLine_TerminalDrainsAlreadyBufferedLineWithoutTouchingRealFD
// (round 7) pins the masked-entry path's buffer-drain safety: when stdin IS
// a terminal, readSecretLine must NOT silently drop bytes bufio already
// buffered from the reader before switching to the raw-fd term.ReadPassword
// call. This test forces the terminal branch but supplies a reader whose
// ENTIRE line (including the trailing newline) is already buffered — the
// drain loop must find it and return without ever reaching
// term.ReadPassword (which would otherwise block/fail against the real,
// non-TTY test-process stdin).
func TestReadSecretLine_TerminalDrainsAlreadyBufferedLineWithoutTouchingRealFD(t *testing.T) {
	withStdinTerminal(t, true)

	r := bufio.NewReader(strings.NewReader("already-buffered-secret\n"))
	// Force bufio to fill its internal buffer from the underlying
	// strings.Reader (which returns everything in one Read call) BEFORE
	// readSecretLine runs, simulating a paste that arrived in cooked mode
	// ahead of the raw-mode switch.
	if _, err := r.Peek(1); err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if buffered := r.Buffered(); buffered != len("already-buffered-secret\n") {
		t.Fatalf("test setup invalid: r.Buffered() = %d, want %d (Peek should have filled the whole line)", buffered, len("already-buffered-secret\n"))
	}

	got, err := readSecretLine(r)
	if err != nil {
		t.Fatalf("readSecretLine: %v", err)
	}
	if got != "already-buffered-secret" {
		t.Fatalf("got %q, want %q", got, "already-buffered-secret")
	}
}

// TestValidateWithProviderConfig_QueryModeEscapesKey (round 7) pins the fix
// for an unescaped API key in a query-param validation URL: a key
// containing characters with special meaning in a URL query string (&, #,
// %, space) must be escaped, both so the request itself isn't mangled and
// so a connection-error message (which embeds the full request URL via
// *url.Error) doesn't leak an even-more-mangled or truncated version of the
// key to the terminal. Exercises the REAL validateWithProviderConfig code
// path against an httptest server and inspects the query string it actually
// received — not a duplicated reimplementation of the URL-building logic.
func TestValidateWithProviderConfig_QueryModeEscapesKey(t *testing.T) {
	key := "a&b=c#d%e f"

	var gotQueryValue string
	var sawKey bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		_, sawKey = values["key"]
		gotQueryValue = values.Get("key")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := &config.ProviderDef{
		Name:         "fake-query-test",
		DisplayName:  "Fake Query",
		DefaultModel: "fake-model",
		GetKey:       func(api *config.APIConfig) string { return "" },
		SetKey:       func(api *config.APIConfig, key string) {},
		KeyValidation: config.KeyValidationDef{
			URL:        srv.URL,
			AuthMode:   "query",
			QueryParam: "key",
		},
	}

	ve := validateWithProviderConfig(contextWithDeadline(t, 2*time.Second), p, key)
	if ve != nil {
		t.Fatalf("validateWithProviderConfig: %+v", ve)
	}
	if !sawKey {
		t.Fatal("server never received a 'key' query parameter")
	}
	// r.URL.Query() already decodes percent-escaping, so the DECODED value
	// received by the server must equal the ORIGINAL key — proving the
	// client escaped it correctly on the way out (an unescaped "&"/"#"
	// would have truncated or split the value at the server, producing a
	// decoded value that does NOT round-trip back to the original key).
	if gotQueryValue != key {
		t.Fatalf("server received query key %q, want %q (round-tripped through escaping)", gotQueryValue, key)
	}
}
