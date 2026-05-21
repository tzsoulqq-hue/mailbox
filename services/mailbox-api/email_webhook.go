package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"mailboxapi/pb"
)

type graphWebhookHandler struct {
	store    *MailboxStore
	watcher  *MailWatcher
	scanGate chan struct{}
}

type graphNotificationEnvelope struct {
	Value []graphNotification `json:"value"`
}

type graphNotification struct {
	SubscriptionID string `json:"subscriptionId"`
	ClientState    string `json:"clientState"`
	Resource       string `json:"resource"`
	ChangeType     string `json:"changeType"`
}

func startWebhookServer(ctx context.Context, addr string, store *MailboxStore, watcher *MailWatcher, errCh chan<- error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return
	}

	handler := &graphWebhookHandler{
		store:    store,
		watcher:  watcher,
		scanGate: make(chan struct{}, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/webhooks/email/cloudflare", handler.handleCloudflareEmail)
	mux.HandleFunc("/webhooks/email/microsoft-graph", handler.handleGraphNotification)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logWarning("shutdown webhook server: %v", err)
		}
	}()
	go func() {
		logInfo("Starting mailbox webhook server on %s", addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("serve webhook: %w", err)
		}
	}()
}

func (h *graphWebhookHandler) handleCloudflareEmail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !validWebhookToken(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	var event pb.InboundEmailWebhook
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, &event); err != nil {
		http.Error(w, "invalid email event", http.StatusBadRequest)
		return
	}
	event.Provider = emailProviderCloudflare
	messages, err := h.store.RecordInboundEmail(r.Context(), &event)
	if err != nil {
		logWarning("record Cloudflare email webhook: %v", err)
		http.Error(w, "record email event failed", http.StatusInternalServerError)
		return
	}
	h.watcher.DispatchMailboxEvents(messages)
	logInfo("recorded Cloudflare email event recipients=%d message_id=%s", len(event.GetRecipients()), event.GetMessageId())
	w.WriteHeader(http.StatusAccepted)
}

func (h *graphWebhookHandler) handleGraphNotification(w http.ResponseWriter, r *http.Request) {
	if token := r.URL.Query().Get("validationToken"); token != "" {
		if !validGraphWebhookRequestToken(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(token))
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		logWarning("read graph webhook body: %v", err)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var envelope graphNotificationEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		logWarning("decode graph webhook body: %v", err)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if !validGraphWebhookClientState(envelope) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	logInfo("received Outlook Graph webhook notifications=%d", len(envelope.Value))
	h.triggerRefresh()
	w.WriteHeader(http.StatusAccepted)
}

func validWebhookToken(r *http.Request) bool {
	expected := strings.TrimSpace(os.Getenv("MAILBOX_WEBHOOK_TOKEN"))
	if expected == "" {
		logWarning("MAILBOX_WEBHOOK_TOKEN is required for email webhook ingestion")
		return false
	}
	token := strings.TrimSpace(r.Header.Get(defaultWebhookTokenHeader))
	return token == expected
}

func validGraphWebhookClientState(envelope graphNotificationEnvelope) bool {
	expected := webhookSecret()
	if expected == "" {
		logWarning("MAILBOX_WEBHOOK_TOKEN is required for Graph webhook ingestion")
		return false
	}
	if len(envelope.Value) == 0 {
		return false
	}
	for _, notification := range envelope.Value {
		if strings.TrimSpace(notification.ClientState) != expected {
			return false
		}
	}
	return true
}

func validGraphWebhookRequestToken(r *http.Request) bool {
	expected := webhookSecret()
	if expected == "" {
		logWarning("MAILBOX_WEBHOOK_TOKEN is required for Graph webhook validation")
		return false
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		token = strings.TrimSpace(r.URL.Query().Get("webhook_token"))
	}
	if token == "" {
		token = strings.TrimSpace(r.Header.Get(defaultWebhookTokenHeader))
	}
	if token == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		token = strings.TrimPrefix(auth, "Bearer ")
	}
	return token == expected
}

func webhookSecret() string {
	return strings.TrimSpace(os.Getenv("MAILBOX_WEBHOOK_TOKEN"))
}

func (h *graphWebhookHandler) triggerRefresh() {
	select {
	case h.scanGate <- struct{}{}:
		go h.refreshMailboxes()
	default:
		logInfo("Outlook webhook refresh already running")
	}
}

func (h *graphWebhookHandler) refreshMailboxes() {
	defer func() { <-h.scanGate }()

	timeout := envInt("OUTLOOK_WEBHOOK_FETCH_TIMEOUT_SECONDS", defaultWebhookTimeout)
	if timeout <= 0 {
		timeout = defaultWebhookTimeout
	}
	limit := envInt("OUTLOOK_WEBHOOK_MAX_MAILBOXES", defaultWebhookMaxMailboxes)
	if limit <= 0 {
		limit = defaultWebhookMaxMailboxes
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	mailboxes, err := h.watcher.store.ListOAuthMailboxes(ctx, int32(limit))
	if err != nil {
		logWarning("list OAuth mailboxes for webhook refresh: %v", err)
		return
	}
	fetched := 0
	failed := 0
	for _, mailbox := range mailboxes {
		if _, err := h.watcher.FetchMailboxInbox(ctx, mailbox, int32(h.watcher.messageLimit), 0); err != nil {
			failed++
			logWarning("webhook mailbox refresh failed for %s: %v", redactEmail(mailbox.GetEmailAddress()), err)
			continue
		}
		fetched++
	}
	logInfo("completed Outlook webhook refresh mailboxes=%d fetched=%d failed=%d", len(mailboxes), fetched, failed)
}
