package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

const maxFaultProxyRequestBytes = 64 << 20 // 64 MiB; comfortably covers 1M-token JSON requests.

type faultTrigger uint8

const (
	faultTriggerAnyMessage faultTrigger = iota
	faultTriggerAfterToolResult
)

type faultAction uint8

const (
	faultActionHTTP408 faultAction = iota
	faultActionHTTP429
	faultActionHTTP500
	faultActionConnectionDrop
	faultActionTruncatedStream
	faultActionEmptyStream
)

type faultProfileSpec struct {
	Name    string
	Trigger faultTrigger
	Action  faultAction
}

var supportedFaultProfiles = map[string]faultProfileSpec{
	"http-408-once":                   {Name: "http-408-once", Trigger: faultTriggerAnyMessage, Action: faultActionHTTP408},
	"http-429-once":                   {Name: "http-429-once", Trigger: faultTriggerAnyMessage, Action: faultActionHTTP429},
	"http-500-once":                   {Name: "http-500-once", Trigger: faultTriggerAnyMessage, Action: faultActionHTTP500},
	"connection-drop-once":            {Name: "connection-drop-once", Trigger: faultTriggerAnyMessage, Action: faultActionConnectionDrop},
	"truncated-stream-once":           {Name: "truncated-stream-once", Trigger: faultTriggerAnyMessage, Action: faultActionTruncatedStream},
	"empty-stream-once":               {Name: "empty-stream-once", Trigger: faultTriggerAnyMessage, Action: faultActionEmptyStream},
	"after-tool-408-once":             {Name: "after-tool-408-once", Trigger: faultTriggerAfterToolResult, Action: faultActionHTTP408},
	"after-tool-429-once":             {Name: "after-tool-429-once", Trigger: faultTriggerAfterToolResult, Action: faultActionHTTP429},
	"after-tool-500-once":             {Name: "after-tool-500-once", Trigger: faultTriggerAfterToolResult, Action: faultActionHTTP500},
	"after-tool-connection-drop-once": {Name: "after-tool-connection-drop-once", Trigger: faultTriggerAfterToolResult, Action: faultActionConnectionDrop},
	"after-tool-truncated-once":       {Name: "after-tool-truncated-once", Trigger: faultTriggerAfterToolResult, Action: faultActionTruncatedStream},
	"after-tool-empty-once":           {Name: "after-tool-empty-once", Trigger: faultTriggerAfterToolResult, Action: faultActionEmptyStream},
}

// FaultProfiles returns every supported deterministic profile in stable order.
func FaultProfiles() []string {
	return []string{
		"http-408-once", "http-429-once", "http-500-once",
		"connection-drop-once", "truncated-stream-once", "empty-stream-once",
		"after-tool-408-once", "after-tool-429-once", "after-tool-500-once",
		"after-tool-connection-drop-once", "after-tool-truncated-once", "after-tool-empty-once",
	}
}

func parseFaultProfile(raw string) (faultProfileSpec, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	spec, ok := supportedFaultProfiles[name]
	if !ok {
		return faultProfileSpec{}, fmt.Errorf("unknown fault profile %q (supported: %s)", raw, strings.Join(FaultProfiles(), ", "))
	}
	return spec, nil
}

// FaultInjectionSummary is attached to each faulted eval result. It proves the
// requested fault actually fired; a benchmark must never score an accidentally
// bypassed proxy as a successful recovery.
type FaultInjectionSummary struct {
	Profile                       string `json:"profile"`
	Requests                      int    `json:"requests"`
	MessageRequests               int    `json:"message_requests"`
	EligibleRequests              int    `json:"eligible_requests"`
	Injected                      int    `json:"injected"`
	RequestsAfterInjection        int    `json:"requests_after_injection"`
	MessageRequestsAfterInjection int    `json:"message_requests_after_injection"`
}

// FaultProxy is a loopback-only reverse proxy which injects one deterministic
// Anthropic-compatible failure, then transparently forwards every later call.
// API keys remain in request headers and are never recorded in its summary.
type FaultProxy struct {
	server    *http.Server
	listener  net.Listener
	reverse   *httputil.ReverseProxy
	transport *http.Transport
	spec      faultProfileSpec

	mu      sync.Mutex
	summary FaultInjectionSummary
}

// StartFaultProxy starts a loopback proxy for one eval scenario.
func StartFaultProxy(upstreamRaw, profileRaw string) (*FaultProxy, error) {
	spec, err := parseFaultProfile(profileRaw)
	if err != nil {
		return nil, err
	}
	upstream, err := validateFaultUpstream(upstreamRaw)
	if err != nil {
		return nil, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	reverse := httputil.NewSingleHostReverseProxy(upstream)
	reverse.Transport = transport
	originalDirector := reverse.Director
	reverse.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = upstream.Host
	}
	reverse.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, proxyErr error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":  "error",
			"error": map[string]string{"type": "proxy_error", "message": proxyErr.Error()},
		})
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("start fault proxy listener: %w", err)
	}
	p := &FaultProxy{
		listener:  listener,
		reverse:   reverse,
		transport: transport,
		spec:      spec,
		summary:   FaultInjectionSummary{Profile: spec.Name},
	}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		_ = p.server.Serve(listener)
	}()
	return p, nil
}

func validateFaultUpstream(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("fault upstream URL is required when a fault profile is enabled")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("parse fault upstream URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("fault upstream URL must be absolute http/https URL, got %q", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("fault upstream URL must not contain credentials, query, or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

// URL returns the custom model base URL to pass to the evaluated gokin process.
func (p *FaultProxy) URL() string {
	if p == nil || p.listener == nil {
		return ""
	}
	return "http://" + p.listener.Addr().String()
}

// Snapshot returns a race-safe copy of the non-sensitive proxy counters.
func (p *FaultProxy) Snapshot() FaultInjectionSummary {
	if p == nil {
		return FaultInjectionSummary{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.summary
}

// Close stops the loopback listener and releases upstream idle connections.
func (p *FaultProxy) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	err := p.server.Shutdown(ctx)
	p.transport.CloseIdleConnections()
	return err
}

func (p *FaultProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := readFaultRequestBody(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))

	isMessage := strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/messages")
	eligible := isMessage && (p.spec.Trigger != faultTriggerAfterToolResult || requestContainsToolResult(body))
	if p.observeAndShouldInject(isMessage, eligible) {
		writeInjectedFault(w, r, p.spec)
		return
	}
	p.reverse.ServeHTTP(w, r)
}

func readFaultRequestBody(body io.ReadCloser) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, maxFaultProxyRequestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read proxied request: %w", err)
	}
	if len(data) > maxFaultProxyRequestBytes {
		return nil, fmt.Errorf("proxied request exceeds %d bytes", maxFaultProxyRequestBytes)
	}
	return data, nil
}

func requestContainsToolResult(body []byte) bool {
	compact := bytes.ToLower(body)
	return bytes.Contains(compact, []byte(`"type":"tool_result"`)) ||
		bytes.Contains(compact, []byte(`"role":"tool"`))
}

func (p *FaultProxy) observeAndShouldInject(message, eligible bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.summary.Requests++
	if message {
		p.summary.MessageRequests++
	}
	if p.summary.Injected > 0 {
		p.summary.RequestsAfterInjection++
		if message {
			p.summary.MessageRequestsAfterInjection++
		}
	}
	if eligible {
		p.summary.EligibleRequests++
	}
	if !eligible || p.summary.Injected > 0 {
		return false
	}
	p.summary.Injected = 1
	return true
}

func writeInjectedFault(w http.ResponseWriter, r *http.Request, spec faultProfileSpec) {
	w.Header().Set("X-Gokin-Fault-Profile", spec.Name)
	switch spec.Action {
	case faultActionHTTP408:
		writeFaultHTTPError(w, http.StatusRequestTimeout, "timeout_error", "fault injection: HTTP 408 request timeout")
	case faultActionHTTP429:
		w.Header().Set("Retry-After", "0")
		writeFaultHTTPError(w, http.StatusTooManyRequests, "rate_limit_error", "fault injection: HTTP 429 rate limit")
	case faultActionHTTP500:
		writeFaultHTTPError(w, http.StatusInternalServerError, "api_error", "fault injection: HTTP 500 provider failure")
	case faultActionConnectionDrop:
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			writeFaultHTTPError(w, http.StatusServiceUnavailable, "connection_error", "fault injection: connection drop unavailable")
			return
		}
		conn, rw, err := hijacker.Hijack()
		if err != nil {
			writeFaultHTTPError(w, http.StatusServiceUnavailable, "connection_error", "fault injection: connection drop failed")
			return
		}
		_ = rw.Flush()
		_ = conn.Close()
	case faultActionTruncatedStream:
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"fault-injected partial response\"}}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// Deliberately omit message_stop/[DONE]; returning closes the stream.
	case faultActionEmptyStream:
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func writeFaultHTTPError(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": kind, "message": message},
	})
}
