package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"
)

type outlookProxyManager struct {
	proxyURL       string
	runtimeHTTPURL string
	region         string
	stickyMinutes  int
	rotateEachUse  bool
	timeout        time.Duration
	speedCheck     bool
	speedURL       string
	speedMaxTime   time.Duration
	speedAttempts  int
}

type outlookProxySessionResponse struct {
	Session struct {
		SessionID string `json:"session_id"`
	} `json:"session"`
	Pool struct {
		Endpoints []map[string]any `json:"endpoints"`
	} `json:"pool"`
}

func newOutlookProxyManagerFromEnv() *outlookProxyManager {
	proxyURL := strings.TrimSpace(envStr("OUTLOOK_PROXY_URL", ""))
	runtimeURL := strings.TrimRight(strings.TrimSpace(envStr("OUTLOOK_PROXY_RUNTIME_HTTP_ADDR", "")), "/")
	if proxyURL == "" && runtimeURL == "" {
		return nil
	}
	stickyMinutes := envInt("OUTLOOK_PROXY_STICKY_MINUTES", 10)
	if stickyMinutes <= 0 {
		stickyMinutes = 10
	}
	timeoutSeconds := envInt("OUTLOOK_PROXY_TIMEOUT_SECONDS", defaultHTTPTimeoutSeconds)
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultHTTPTimeoutSeconds
	}
	speedMaxMillis := envInt("OUTLOOK_PROXY_SPEED_MAX_MILLIS", 500)
	if speedMaxMillis <= 0 {
		speedMaxMillis = 500
	}
	speedAttempts := envInt("OUTLOOK_PROXY_SPEED_ATTEMPTS", 4)
	if speedAttempts <= 0 {
		speedAttempts = 4
	}
	return &outlookProxyManager{
		proxyURL:       proxyURL,
		runtimeHTTPURL: runtimeURL,
		region:         envStr("OUTLOOK_PROXY_REGION", "ID"),
		stickyMinutes:  stickyMinutes,
		rotateEachUse:  envBool("OUTLOOK_PROXY_ROTATE_EACH_FETCH", false),
		timeout:        time.Duration(timeoutSeconds) * time.Second,
		speedCheck:     envBool("OUTLOOK_PROXY_SPEED_CHECK_ENABLED", true),
		speedURL:       envStr("OUTLOOK_PROXY_SPEED_CHECK_URL", "https://login.live.com/favicon.ico"),
		speedMaxTime:   time.Duration(speedMaxMillis) * time.Millisecond,
		speedAttempts:  speedAttempts,
	}
}

func (m *outlookProxyManager) enabled() bool {
	return m != nil && (strings.TrimSpace(m.proxyURL) != "" || strings.TrimSpace(m.runtimeHTTPURL) != "")
}

func (m *outlookProxyManager) clientForMailbox(ctx context.Context, email string, country string, fallback *http.Client) (*http.Client, string, error) {
	if !m.enabled() {
		return fallback, "", nil
	}
	sessionHash := ""
	if m.rotateEachUse && m.runtimeHTTPURL != "" {
		hash, err := m.rotateSession(ctx, email, country)
		if err != nil {
			return nil, "", err
		}
		sessionHash = hash
	}
	client, err := httpClientForProxyURL(m.proxyURL, m.timeout)
	if err != nil {
		return nil, "", err
	}
	return client, sessionHash, nil
}

func (m *outlookProxyManager) rotateSession(ctx context.Context, email string, country string) (string, error) {
	email = normalizeEmail(email)
	region := firstNonEmpty(strings.TrimSpace(country), strings.TrimSpace(m.region))
	attempts := 1
	if m.speedCheck {
		attempts = m.speedAttempts
	}
	var lastErr error
	var lastHash string
	for attempt := 1; attempt <= attempts; attempt++ {
		sessionID, err := m.createSession(ctx, email, region)
		if err != nil {
			return "", err
		}
		if sessionID == "" {
			return "", nil
		}
		lastHash = shortValueHash(sessionID)
		if !m.speedCheck || strings.TrimSpace(m.proxyURL) == "" {
			return lastHash, nil
		}
		elapsed, err := m.probeSpeed(ctx)
		if err == nil && elapsed <= m.speedMaxTime {
			return lastHash, nil
		}
		if err != nil {
			lastErr = fmt.Errorf("outlook proxy speed probe failed attempt=%d session=%s: %w", attempt, lastHash, err)
		} else {
			lastErr = fmt.Errorf("outlook proxy speed probe too slow attempt=%d session=%s elapsed=%s threshold=%s", attempt, lastHash, elapsed.Round(time.Millisecond), m.speedMaxTime)
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return lastHash, nil
}

func (m *outlookProxyManager) createSession(ctx context.Context, email string, region string) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	body := map[string]any{
		"force_new":   true,
		"listener_id": "outlook-provider",
		"policy": map[string]any{
			"mode":               "PROXY_SESSION_MODE_STICKY",
			"region":             region,
			"sticky_ttl_minutes": m.stickyMinutes,
			"upstream_kind":      "PROXY_UPSTREAM_KIND_DYNAMIC_IP",
			"rotation_mode":      "PROXY_ROTATION_MODE_STICKY_SESSION",
			"labels":             map[string]string{"purpose": "outlook_mailbox", "email_hash": shortValueHash(email), "home_country": region, "listener_id": "outlook-provider"},
		},
	}
	rawBody, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, m.runtimeHTTPURL+"/api/proxy-runtime/session/new", bytes.NewReader(rawBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("outlook proxy session/new: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read outlook proxy session/new response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("outlook proxy session/new failed: status=%d body=%s", resp.StatusCode, snippet(string(raw), 300))
	}
	var parsed outlookProxySessionResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("parse outlook proxy session/new response: %w", err)
	}
	return parsed.Session.SessionID, nil
}

func (m *outlookProxyManager) probeSpeed(ctx context.Context) (time.Duration, error) {
	client, err := httpClientForProxyURL(m.proxyURL, m.speedMaxTime)
	if err != nil {
		return 0, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, m.speedMaxTime)
	defer cancel()
	started := time.Now()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodHead, m.speedURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "image/avif,image/webp,image/png,image/svg+xml,image/*,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return time.Since(started), err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 500 {
		return time.Since(started), fmt.Errorf("status=%d", resp.StatusCode)
	}
	return time.Since(started), nil
}

func httpClientForProxyURL(rawURL string, timeout time.Duration) (*http.Client, error) {
	if timeout <= 0 {
		timeout = time.Duration(defaultHTTPTimeoutSeconds) * time.Second
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return &http.Client{Timeout: timeout}, nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(parsed)
	case "socks5", "socks5h":
		dialer, err := xproxy.SOCKS5("tcp", parsed.Host, nil, xproxy.Direct)
		if err != nil {
			return nil, err
		}
		transport.DialContext = func(ctx context.Context, network string, address string) (net.Conn, error) {
			type contextDialer interface {
				DialContext(context.Context, string, string) (net.Conn, error)
			}
			if d, ok := dialer.(contextDialer); ok {
				return d.DialContext(ctx, network, address)
			}
			return dialer.Dial(network, address)
		}
	default:
		return nil, fmt.Errorf("unsupported OUTLOOK_PROXY_URL scheme: %s", parsed.Scheme)
	}
	return &http.Client{Timeout: timeout, Transport: transport}, nil
}

func shortValueHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

func snippet(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}
