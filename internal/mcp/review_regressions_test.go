package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/logging"
	"gokin/internal/tools"
)

func TestHTTPTransportDoesNotForwardRedirectCredentials(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var forwarded atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1); w.WriteHeader(http.StatusAccepted) }))
			defer target.Close()
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Api-Key") != "private-credential" || r.Header.Get("Mcp-Session-Id") != "private-session" {
					t.Error("initial request lost credentials")
				}
				w.Header().Set("Location", strings.Replace(target.URL, "127.0.0.1", "localhost", 1)+"/private?q=secret")
				w.WriteHeader(status)
			}))
			defer endpoint.Close()
			transport, err := NewHTTPTransport(t.Context(), endpoint.URL, map[string]string{"X-Api-Key": "private-credential"}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer transport.Close()
			transport.commitSessionID("private-session")
			err = transport.Send(&JSONRPCMessage{ID: 1, Method: MethodToolsCall, Params: map[string]any{"private": "body"}})
			if err == nil || !strings.Contains(err.Error(), "refusing to forward credentials") || strings.Contains(err.Error(), "private?q=secret") || forwarded.Load() != 0 {
				t.Fatalf("unsafe redirect response: requests=%d err=%v", forwarded.Load(), err)
			}
		})
	}
}

func TestTransportStartupLogsOmitCredentialArgumentsAndURL(t *testing.T) {
	for _, kind := range []string{"stdio", "http"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "debug.log")
			if err := logging.EnablePathLogging(path, logging.LevelDebug, ""); err != nil {
				t.Fatal(err)
			}
			defer logging.DisableLogging()
			secret := "opaque-credential-that-does-not-match-a-pattern"
			if kind == "stdio" {
				transport, err := NewStdioTransport("/bin/sh", []string{"-c", "cat >/dev/null", "--token", secret}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := transport.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				transport, err := NewHTTPTransport(context.Background(), "https://example.org/mcp?q="+secret, nil, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				transport.Close()
			}
			logging.Close()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.ToLower(string(data)), "mcp "+kind+" transport") {
				t.Fatalf("missing startup record: %s", data)
			}
			if strings.Contains(string(data), secret) {
				t.Fatalf("startup logs disclosed a credential: %s", data)
			}
		})
	}
}

func TestPluginMCPUnderscoreNamespacesDoNotCollide(t *testing.T) {
	authority, _, _, work := pluginMCPFixture(t, `{"name":"a_b","mcpServers":{"c":{"command":"unused"}}}`)
	first, err := authority.ClaudePlugins()
	if err != nil {
		t.Fatal(err)
	}
	second := t.TempDir()
	writePluginMCPFile(t, second, ".claude-plugin/plugin.json", `{"name":"a","mcpServers":{"b_c":{"command":"unused"}}}`)
	authority.ConfigureClaudePlugins([]string{first[0].Root(), second})
	configs, err := LoadClaudePluginServers(authority, work)
	if err != nil || len(configs) != 2 {
		t.Fatalf("configs=%v %v", configs, err)
	}
	registry := tools.NewRegistry()
	names := map[string]bool{}
	for _, cfg := range configs {
		info := &ToolInfo{Name: "query", InputSchema: &JSONSchema{Type: "object"}}
		tool := NewMCPTool(&Client{config: cfg}, cfg.Name, cfg.ToolPrefix, info)
		if err := registry.Register(tool); err != nil {
			t.Fatalf("plugin tool disappeared: %v", err)
		}
		if len(tool.Name()) > 64 || names[tool.Name()] || tool.Declaration().Name != tool.Name() {
			t.Fatalf("invalid or colliding name: %s", tool.Name())
		}
		names[tool.Name()] = true
		if tool.ClaudeToolName() != "mcp__plugin_a_b_c__query" {
			t.Fatalf("canonical hook name changed: %s", tool.ClaudeToolName())
		}
		again := NewMCPTool(&Client{config: cfg}, cfg.Name, cfg.ToolPrefix, info)
		if tool.Name() != again.Name() {
			t.Fatal("unstable plugin tool name")
		}
	}
}

func TestHTTPTransportErrorDoesNotExposeExpandedURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := server.URL
	server.Close()
	secret := "opaque-credential-that-does-not-match-a-pattern"
	transport, err := NewHTTPTransport(t.Context(), address+"/private-path?q="+secret, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	err = transport.Send(&JSONRPCMessage{ID: 1, Method: MethodPing})
	if err == nil {
		t.Fatal("unreachable endpoint succeeded")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "private-path") || !strings.Contains(err.Error(), address) {
		t.Fatalf("unsafe HTTP error: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = transport.SendContext(ctx, &JSONRPCMessage{ID: 2, Method: MethodPing})
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), secret) {
		t.Fatalf("cancellation was lost or URL exposed: %v", err)
	}
}

func TestHTTPTransportAllowsSameOriginRedirect(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path == "/mcp" {
					w.Header().Set("Location", "/mcp/")
					w.WriteHeader(status)
					return
				}
				if r.Method != "POST" || r.Header.Get("X-Api-Key") != "private-credential" || r.Header.Get("Mcp-Session-Id") != "private-session" {
					t.Error("same-origin redirect lost request identity")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || !strings.Contains(string(body), "private-body") {
					t.Errorf("redirect lost body: %s %v", body, err)
				}
				w.WriteHeader(http.StatusAccepted)
			}))
			defer server.Close()
			transport, err := NewHTTPTransport(t.Context(), server.URL+"/mcp", map[string]string{"X-Api-Key": "private-credential"}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer transport.Close()
			transport.commitSessionID("private-session")
			if err := transport.Send(&JSONRPCMessage{ID: 1, Method: MethodToolsCall, Params: map[string]any{"value": "private-body"}}); err != nil || calls.Load() != 2 {
				t.Fatalf("same-origin redirect failed: calls=%d err=%v", calls.Load(), err)
			}
		})
	}
}

func TestHTTPTransportRefusesHTTPSDowngrade(t *testing.T) {
	var calls atomic.Int32
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", strings.Replace(server.URL, "https://", "http://", 1)+"/private?q=opaque-secret")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	transport, err := NewHTTPTransport(t.Context(), server.URL+"/mcp", map[string]string{"X-Api-Key": "private-credential"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	transport.client.Transport = server.Client().Transport
	err = transport.Send(&JSONRPCMessage{ID: 1, Method: MethodPing})
	if err == nil || !strings.Contains(err.Error(), "refusing to forward credentials") || strings.Contains(err.Error(), "opaque-secret") || strings.Contains(err.Error(), "/private") || calls.Load() != 1 {
		t.Fatalf("unsafe downgrade handling: calls=%d err=%v", calls.Load(), err)
	}
}

func TestHTTPTransportRedirectHopLimit(t *testing.T) {
	for _, hops := range []int{5, 6} {
		t.Run(fmt.Sprint(hops), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := int(calls.Add(1))
				if n <= hops {
					w.Header().Set("Location", fmt.Sprintf("/hop/%d?q=opaque-secret", n))
					w.WriteHeader(http.StatusTemporaryRedirect)
					return
				}
				w.WriteHeader(http.StatusAccepted)
			}))
			defer server.Close()
			transport, err := NewHTTPTransport(t.Context(), server.URL, nil, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer transport.Close()
			err = transport.Send(&JSONRPCMessage{ID: 1, Method: MethodPing})
			if calls.Load() != 6 || (err == nil) != (hops == 5) {
				t.Fatalf("wrong redirect limit: hops=%d calls=%d err=%v", hops, calls.Load(), err)
			}
			if err != nil && (!strings.Contains(err.Error(), "5 redirects") || strings.Contains(err.Error(), "opaque-secret")) {
				t.Fatalf("unsafe redirect-limit error: %v", err)
			}
		})
	}
}

func TestHTTPTransportRedirectSchemeAndAuthority(t *testing.T) {
	transport, err := NewHTTPTransport(t.Context(), "http://example.test/mcp", nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	for _, tc := range []struct {
		from, to string
		allow    bool
	}{
		{"http://example.test/mcp", "http://example.test/mcp/", true},
		{"https://example.test/mcp", "https://example.test/mcp/", true},
		{"http://example.test/mcp", "https://example.test/mcp/", true},
		{"https://example.test/mcp", "http://example.test/mcp/", false},
		{"http://example.test:80/mcp", "http://example.test:81/mcp/", false},
		{"http://example.test/mcp", "https://other.test/mcp/", false},
	} {
		original, _ := http.NewRequest("POST", tc.from, nil)
		target, _ := http.NewRequest("POST", tc.to+"?q=opaque-secret", nil)
		err := transport.client.CheckRedirect(target, []*http.Request{original})
		if (err == nil) != tc.allow {
			t.Errorf("redirect %s -> %s: %v", tc.from, tc.to, err)
		}
		if err != nil && strings.Contains(err.Error(), "opaque-secret") {
			t.Error("redirect error exposed query", err)
		}
	}
	// An upgrade must not permit a later downgrade back to the original scheme.
	original, _ := http.NewRequest("POST", "http://example.test/mcp", nil)
	upgraded, _ := http.NewRequest("POST", "https://example.test/mcp", nil)
	target, _ := http.NewRequest("POST", "http://example.test/mcp/", nil)
	if err := transport.client.CheckRedirect(target, []*http.Request{original, upgraded}); err == nil {
		t.Fatal("upgrade was followed by downgrade")
	}
}
