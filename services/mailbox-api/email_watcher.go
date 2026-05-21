package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"mailboxapi/pb"
)

type oauthEntry struct {
	refreshToken string
	manager      *OAuthManager
}

type MailWatcher struct {
	store        *MailboxStore
	graphURL     string
	messageLimit int
	pollInterval int
	inboxOverlap int
	httpClient   *http.Client
	outbound     *mailboxEventDispatcher
	events       *mailboxEmailEventBus

	mu            sync.Mutex
	oauthManagers map[string]oauthEntry
}

type GraphFetchError struct {
	StatusCode int
	Body       string
	RetryAfter time.Duration
}

func (e *GraphFetchError) Error() string {
	body := e.Body
	if len(body) > 500 {
		body = body[:500]
	}
	return fmt.Sprintf("status=%d body=%s", e.StatusCode, body)
}

func (e *GraphFetchError) IsAuth() bool {
	return e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden
}

func (e *GraphFetchError) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= http.StatusInternalServerError
}

func NewMailWatcher(store *MailboxStore, events *mailboxEmailEventBus) *MailWatcher {
	messageLimit := envInt("OUTLOOK_MESSAGE_LIMIT", defaultMessageLimit)
	if messageLimit < 1 {
		messageLimit = 1
	}
	if messageLimit > 100 {
		messageLimit = 100
	}
	pollInterval := envInt("OUTLOOK_POLL_INTERVAL_SECONDS", defaultPollIntervalSeconds)
	if pollInterval < 1 {
		pollInterval = 1
	}
	inboxOverlap := envInt("OUTLOOK_INBOX_OVERLAP_SECONDS", defaultInboxOverlapSeconds)
	if inboxOverlap < 0 {
		inboxOverlap = 0
	}
	timeout := envInt("OUTLOOK_HTTP_TIMEOUT_SECONDS", defaultHTTPTimeoutSeconds)
	if timeout <= 0 {
		timeout = defaultHTTPTimeoutSeconds
	}
	return &MailWatcher{
		store:         store,
		graphURL:      envStr("OUTLOOK_GRAPH_MESSAGES_URL", defaultGraphMessagesURL),
		messageLimit:  messageLimit,
		pollInterval:  pollInterval,
		inboxOverlap:  inboxOverlap,
		httpClient:    &http.Client{Timeout: time.Duration(timeout) * time.Second},
		outbound:      newMailboxEventDispatcherFromEnv(),
		events:        events,
		oauthManagers: map[string]oauthEntry{},
	}
}

func (w *MailWatcher) PollForEmail(ctx context.Context, email string) error {
	mailbox, err := w.store.PollMailboxForEmail(ctx, email)
	if err != nil {
		return err
	}
	messages, err := w.fetchMailboxMessages(ctx, mailbox, w.messageLimit, 0)
	if err != nil {
		return err
	}
	unseen, err := w.store.RecordInboxMessages(ctx, mailbox.GetEmailAddress(), messages)
	if err != nil {
		return err
	}
	w.DispatchMailboxEvents(unseen)
	return nil
}

func (w *MailWatcher) FetchMailboxInbox(ctx context.Context, mailbox *pb.EmailMailbox, limit int32, receivedAfterUnix int64) ([]*pb.EmailInboxMessage, error) {
	watermark, err := w.store.InboxWatermark(ctx, mailbox.GetEmailAddress())
	if err != nil {
		return nil, err
	}
	messageLimit := messageLimitValue(limit, w.messageLimit)
	receivedAfter := inboxReceivedAfter(watermark, w.inboxOverlap)
	hasPersistedMessages, err := w.store.HasInboxMessages(ctx, mailbox.GetEmailAddress())
	if err != nil {
		return nil, err
	}
	if !hasPersistedMessages {
		receivedAfter = 0
	}
	messages, err := w.fetchMailboxMessages(ctx, mailbox, messageLimit, receivedAfter)
	if err != nil {
		return nil, err
	}
	unseen, err := w.store.RecordInboxMessages(ctx, mailbox.GetEmailAddress(), messages)
	if err != nil {
		return nil, err
	}
	w.DispatchMailboxEvents(unseen)
	return w.store.ListInboxMessagesSince(ctx, mailbox.GetEmailAddress(), int32(messageLimit), receivedAfterUnix)
}

func (w *MailWatcher) DispatchMailboxEvents(messages []*pb.EmailInboxMessage) {
	if len(messages) == 0 {
		return
	}
	w.outbound.Dispatch(messages)
	if w.events != nil {
		w.events.Publish(messages)
	}
}

func (w *MailWatcher) fetchMailboxMessages(ctx context.Context, mailbox *pb.EmailMailbox, limit int, receivedAfterNs int64) ([]graphMessage, error) {
	manager := w.oauthManagerForMailbox(mailbox)
	accessToken, err := manager.GetAccessToken(ctx)
	if err != nil {
		w.store.MarkAuthFailed(ctx, mailbox.GetEmailAddress(), err)
		return nil, err
	}
	if err := w.persistTokens(ctx, mailbox, manager); err != nil {
		w.store.MarkAuthFailed(ctx, mailbox.GetEmailAddress(), err)
		return nil, err
	}
	messages, err := w.fetchRecentMessages(ctx, accessToken, limit, receivedAfterNs)
	if err != nil {
		var graphErr *GraphFetchError
		if !errors.As(err, &graphErr) {
			w.store.MarkAuthFailed(ctx, mailbox.GetEmailAddress(), err)
			return nil, err
		}
		if !graphErr.IsAuth() {
			return nil, err
		}
		logInfo("Graph auth error for %s; refreshing token and retrying", redactEmail(mailbox.GetEmailAddress()))
		accessToken, err = manager.RefreshAccessToken(ctx)
		if err == nil {
			err = w.persistTokens(ctx, mailbox, manager)
		}
		if err == nil {
			messages, err = w.fetchRecentMessages(ctx, accessToken, limit, receivedAfterNs)
		}
		if err != nil {
			w.store.MarkAuthFailed(ctx, mailbox.GetEmailAddress(), err)
			return nil, err
		}
	}
	return messages, nil
}

func (w *MailWatcher) oauthManagerForMailbox(mailbox *pb.EmailMailbox) *OAuthManager {
	key := normalizeEmail(mailbox.GetEmailAddress())
	refreshToken := strings.TrimSpace(mailbox.GetRefreshToken())
	w.mu.Lock()
	defer w.mu.Unlock()
	entry, ok := w.oauthManagers[key]
	if !ok || entry.refreshToken != refreshToken {
		entry = oauthEntry{refreshToken: refreshToken, manager: NewOAuthManager(refreshToken)}
		w.oauthManagers[key] = entry
	}
	return entry.manager
}

func (w *MailWatcher) persistTokens(ctx context.Context, mailbox *pb.EmailMailbox, manager *OAuthManager) error {
	refreshToken, accessToken := manager.CurrentTokens()
	if refreshToken != mailbox.GetRefreshToken() || accessToken != mailbox.GetAccessToken() {
		return w.store.UpdateMailboxTokens(ctx, mailbox.GetEmailAddress(), refreshToken, accessToken)
	}
	return nil
}

func (w *MailWatcher) fetchRecentMessages(ctx context.Context, accessToken string, limit int, receivedAfterNs int64) ([]graphMessage, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		messages, err := w.fetchOnce(ctx, accessToken, limit, receivedAfterNs)
		if err == nil {
			return messages, nil
		}
		lastErr = err
		var graphErr *GraphFetchError
		if !errors.As(err, &graphErr) || attempt == 2 || !graphErr.Retryable() {
			break
		}
		delay := graphErr.RetryAfter
		if delay <= 0 {
			delay = time.Duration(attempt+1) * 500 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func (w *MailWatcher) fetchOnce(ctx context.Context, accessToken string, limit int, receivedAfterNs int64) ([]graphMessage, error) {
	if strings.TrimSpace(w.graphURL) == defaultGraphMessagesURL {
		return w.fetchOnceWithGraphSDK(ctx, accessToken, limit, receivedAfterNs)
	}
	return w.fetchOnceREST(ctx, accessToken, limit, receivedAfterNs)
}

func (w *MailWatcher) fetchOnceREST(ctx context.Context, accessToken string, limit int, receivedAfterNs int64) ([]graphMessage, error) {
	u, err := url.Parse(w.graphURL)
	if err != nil {
		return nil, err
	}
	query := u.Query()
	query.Set("$top", strconv.Itoa(messageLimitValue(int32(limit), w.messageLimit)))
	query.Set("$orderby", "receivedDateTime desc")
	query.Set("$select", "id,subject,from,bodyPreview,body,toRecipients,ccRecipients,bccRecipients,internetMessageHeaders,receivedDateTime")
	if receivedAfterNs > 0 {
		query.Set("$filter", "receivedDateTime gt "+time.Unix(0, receivedAfterNs).UTC().Format(time.RFC3339Nano))
	}
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Prefer", `outlook.body-content-type="text"`)

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &GraphFetchError{
			StatusCode: resp.StatusCode,
			Body:       string(raw),
			RetryAfter: retryAfter(resp.Header.Get("Retry-After")),
		}
	}
	var decoded graphMessagesResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded.Value, nil
}

func inboxMessages(mailboxEmail string, messages []graphMessage) []*pb.EmailInboxMessage {
	out := make([]*pb.EmailInboxMessage, 0, len(messages))
	for _, msg := range messages {
		out = append(out, inboxMessage(mailboxEmail, msg))
	}
	return out
}

func inboxMessage(mailboxEmail string, msg graphMessage) *pb.EmailInboxMessage {
	bodyPreview := strings.TrimSpace(msg.BodyPreview)
	if bodyPreview == "" {
		bodyPreview = compactMessageText(msg.Body.Content, 500)
	}
	body := msg.BodyPreview + "\n" + msg.Body.Content
	return &pb.EmailInboxMessage{
		Id:                 msg.ID,
		MailboxEmail:       normalizeEmail(mailboxEmail),
		Subject:            strings.TrimSpace(msg.Subject),
		FromAddress:        strings.TrimSpace(msg.From.EmailAddress.Address),
		BodyPreview:        compactMessageText(bodyPreview, 500),
		ReceivedAtUnix:     int64(parseGraphTime(msg.ReceivedDateTime)),
		Recipients:         uniqueStrings(messageAddresses(msg)),
		Provider:           emailProviderOutlook,
		SourceMailboxEmail: normalizeEmail(mailboxEmail),
		BodyText:           compactMessageText(body, 5000),
	}
}

func compactMessageText(value string, limit int) string {
	text := htmlTagPattern.ReplaceAllString(html.UnescapeString(value), " ")
	text = strings.Join(strings.Fields(strings.ReplaceAll(text, "\u00a0", " ")), " ")
	if limit > 0 && len(text) > limit {
		runes := []rune(text)
		if len(runes) > limit {
			return string(runes[:limit])
		}
	}
	return text
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, value := range values {
		trimmed := normalizeEmail(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func messageLimitValue(limit int32, fallback int) int {
	n := int(limit)
	if n <= 0 {
		n = fallback
	}
	if n <= 0 {
		n = defaultMessageLimit
	}
	if n > 100 {
		n = 100
	}
	return n
}

func inboxReceivedAfter(watermarkNs int64, overlapSeconds int) int64 {
	if watermarkNs <= 0 {
		return 0
	}
	after := watermarkNs - int64(overlapSeconds)*int64(time.Second)
	if after < 0 {
		return 0
	}
	return after
}

func retryAfter(value string) time.Duration {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 {
		return 0
	}
	if n > 10 {
		n = 10
	}
	return time.Duration(n) * time.Second
}

func messageKey(msg graphMessage) string {
	if msg.ID != "" {
		return msg.ID
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func messageAddresses(msg graphMessage) []string {
	out := []string{}
	for _, list := range [][]graphRecipient{msg.ToRecipients, msg.CcRecipients, msg.BccRecipients} {
		for _, recipient := range list {
			if address := strings.TrimSpace(recipient.EmailAddress.Address); address != "" {
				out = append(out, address)
			}
		}
	}
	for _, header := range msg.InternetMessageHeaders {
		name := strings.ToLower(strings.TrimSpace(header.Name))
		value := header.Value
		if recipientHeaders[name] {
			out = append(out, emailPattern.FindAllString(value, -1)...)
			continue
		}
		if name == "received" {
			idx := strings.LastIndex(strings.ToLower(value), " for ")
			if idx >= 0 {
				out = append(out, emailPattern.FindAllString(value[idx+5:], -1)...)
			}
		}
	}
	return out
}

var recipientHeaders = map[string]bool{
	"to":                   true,
	"cc":                   true,
	"bcc":                  true,
	"delivered-to":         true,
	"envelope-to":          true,
	"x-envelope-to":        true,
	"x-original-to":        true,
	"x-original-recipient": true,
	"resent-to":            true,
	"apparently-to":        true,
	"x-forwarded-to":       true,
	"x-ms-exchange-organization-originalrecipient":          true,
	"x-ms-exchange-organization-originalenveloperecipients": true,
}

type graphMessagesResponse struct {
	Value []graphMessage `json:"value"`
}

type graphMessage struct {
	ID                     string           `json:"id"`
	Subject                string           `json:"subject"`
	From                   graphRecipient   `json:"from"`
	BodyPreview            string           `json:"bodyPreview"`
	Body                   graphBody        `json:"body"`
	ToRecipients           []graphRecipient `json:"toRecipients"`
	CcRecipients           []graphRecipient `json:"ccRecipients"`
	BccRecipients          []graphRecipient `json:"bccRecipients"`
	InternetMessageHeaders []graphHeader    `json:"internetMessageHeaders"`
	ReceivedDateTime       string           `json:"receivedDateTime"`
}

type graphBody struct {
	Content string `json:"content"`
}

type graphRecipient struct {
	EmailAddress graphEmailAddress `json:"emailAddress"`
}

type graphEmailAddress struct {
	Address string `json:"address"`
}

type graphHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
