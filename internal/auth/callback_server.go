package auth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"html"
	"net"
	"net/http"
	"time"
)

// CallbackServer handles the OAuth redirect callback
type CallbackServer struct {
	port          int
	expectedState string
	callbackPath  string
	codeChan      chan string
	errChan       chan error
	server        *http.Server
}

// NewCallbackServer creates a new callback server with the default path /oauth2callback.
func NewCallbackServer(port int, expectedState string) *CallbackServer {
	return NewCallbackServerWithPath(port, expectedState, "/oauth2callback")
}

// NewCallbackServerWithPath creates a new callback server with a custom callback path.
func NewCallbackServerWithPath(port int, expectedState, path string) *CallbackServer {
	return &CallbackServer{
		port:          port,
		expectedState: expectedState,
		callbackPath:  path,
		codeChan:      make(chan string, 1),
		errChan:       make(chan error, 1),
	}
}

// Start starts the callback server in the background
func (s *CallbackServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc(s.callbackPath, s.handleCallback)

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", s.port, err)
	}

	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		if err := s.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.trySendError(fmt.Errorf("callback server error: %w", err))
		}
	}()

	return nil
}

// Stop stops the callback server
func (s *CallbackServer) Stop() {
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.server.Shutdown(ctx)
	}
}

// WaitForCode waits for the authorization code from the callback
func (s *CallbackServer) WaitForCode(timeout time.Duration) (string, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case code := <-s.codeChan:
		return code, nil
	case err := <-s.errChan:
		return "", err
	case <-timer.C:
		return "", fmt.Errorf("timeout waiting for OAuth callback (did you complete the login in browser?)")
	}
}

// handleCallback handles the OAuth callback request
func (s *CallbackServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	// Only accept GET requests (OAuth callbacks are always GET)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check for error
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		errDesc := r.URL.Query().Get("error_description")
		s.trySendError(fmt.Errorf("OAuth error: %s - %s", errMsg, errDesc))
		s.renderResponse(w, false, "Authentication failed: "+errMsg)
		return
	}

	// Validate state (constant-time comparison to prevent timing attacks)
	state := r.URL.Query().Get("state")
	if subtle.ConstantTimeCompare([]byte(state), []byte(s.expectedState)) != 1 {
		s.trySendError(fmt.Errorf("state mismatch (possible CSRF attack)"))
		s.renderResponse(w, false, "Invalid state parameter (possible CSRF attack)")
		return
	}

	// Get authorization code
	code := r.URL.Query().Get("code")
	if code == "" {
		s.trySendError(fmt.Errorf("no authorization code received"))
		s.renderResponse(w, false, "No authorization code received")
		return
	}

	// Send code to channel (non-blocking to handle duplicate callbacks)
	select {
	case s.codeChan <- code:
	default:
	}
	s.renderResponse(w, true, "Authentication successful! You can close this window.")
}

// trySendError sends an error on the error channel without blocking (handles duplicate callbacks).
func (s *CallbackServer) trySendError(err error) {
	select {
	case s.errChan <- err:
	default:
	}
}

// renderResponse renders an HTML response page
func (s *CallbackServer) renderResponse(w http.ResponseWriter, success bool, message string) {
	message = html.EscapeString(message)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	icon := "&#10060;" // Red X
	color := "#d32f2f"
	title := "Authentication Failed"
	if success {
		icon = "&#10004;" // Green checkmark
		color = "#388e3c"
		title = "Authentication Successful"
	}

	page := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
    <title>Gokin - %s</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
            display: flex;
            justify-content: center;
            align-items: center;
            height: 100vh;
            margin: 0;
            background-color: #1a1a1a;
            color: #ffffff;
        }
        .container {
            text-align: center;
            padding: 40px;
            background-color: #2d2d2d;
            border-radius: 12px;
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.3);
        }
        .icon {
            font-size: 48px;
            margin-bottom: 20px;
            color: %s;
        }
        h1 {
            margin: 0 0 10px 0;
            font-size: 24px;
            font-weight: 600;
        }
        p {
            margin: 0;
            color: #b0b0b0;
            font-size: 14px;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="icon">%s</div>
        <h1>%s</h1>
        <p>%s</p>
    </div>
</body>
</html>`, title, color, icon, title, message)

	w.Write([]byte(page))
}
