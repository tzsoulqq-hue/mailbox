package main

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultListenAddr          = ":50051"
	defaultWebhookTokenHeader  = "X-Webhook-Token"
	defaultOAuthClientID       = "9e5f94bc-e8a4-4e73-b8be-63364c29d753"
	defaultOAuthScope          = "https://graph.microsoft.com/Mail.Read"
	defaultTokenURL            = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	defaultGraphMessagesURL    = "https://graph.microsoft.com/v1.0/me/messages"
	defaultPollIntervalSeconds = 5
	defaultMessageLimit        = 25
	defaultHTTPTimeoutSeconds  = 20
	defaultInboxOverlapSeconds = 120
	defaultWebhookMaxMailboxes = 100
	defaultWebhookTimeout      = 60
	defaultOutlookMaxMessages  = 100
	defaultCloudflareMaxDomain = 500
)

const (
	emailProviderOutlook    = "outlook"
	emailProviderCloudflare = "cloudflare"
)

const (
	authStatusAuthorized        = "AUTHORIZED"
	authStatusOAuthPending      = "OAUTH_PENDING"
	authStatusAuthFailed        = "AUTH_FAILED"
	authStatusNeedsManualVerify = "NEEDS_MANUAL_VERIFICATION"
)

var (
	emailPattern   = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)
	htmlTagPattern = regexp.MustCompile(`<[^>]+>`)
)

func envStr(name string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

func logInfo(format string, args ...any) {
	log.Printf("[MAIL] "+format, args...)
}

func logWarning(format string, args ...any) {
	log.Printf("[MAIL] WARNING "+format, args...)
}

func normalizeScope(value string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(value, ",", " ")), " ")
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func canonicalEmail(email string) string {
	normalized := normalizeEmail(email)
	local, domain, ok := strings.Cut(normalized, "@")
	if !ok || local == "" || domain == "" {
		return normalized
	}
	local, _, _ = strings.Cut(local, "+")
	return local + "@" + domain
}

func redactEmail(email string) string {
	local, domain, ok := strings.Cut(strings.TrimSpace(email), "@")
	if !ok {
		return "***"
	}
	if len(local) > 2 {
		return local[:2] + "***@" + domain
	}
	return "***@" + domain
}

func containsFold(value string, keyword string) bool {
	if keyword == "" {
		return true
	}
	return strings.Contains(strings.ToLower(value), strings.ToLower(keyword))
}

func parseGraphTime(value string) float64 {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0
	}
	return float64(parsed.UnixNano()) / float64(time.Second)
}

func parseGraphTimeUnixNano(value string) int64 {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0
	}
	return parsed.UnixNano()
}

func randomHex(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
