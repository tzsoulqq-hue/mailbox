package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"mailboxapi/pb"
)

const outboundMailboxEmailEventType = "mailbox.email.received"

type mailboxEventDispatcher struct {
	hooks  []*pb.OutboundEmailWebhook
	client *http.Client
}

func newMailboxEventDispatcherFromEnv() *mailboxEventDispatcher {
	path := strings.TrimSpace(os.Getenv("MAILBOX_OUTBOUND_WEBHOOKS_FILE"))
	if path == "" {
		return &mailboxEventDispatcher{}
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		logWarning("read mailbox outbound webhooks file: %v", err)
		return &mailboxEventDispatcher{}
	}
	var config pb.OutboundEmailWebhookList
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, &config); err != nil {
		logWarning("decode mailbox outbound webhooks file: %v", err)
		return &mailboxEventDispatcher{}
	}
	hooks := normalizeOutboundWebhooks(config.GetWebhooks())
	return &mailboxEventDispatcher{hooks: hooks, client: &http.Client{Timeout: 5 * time.Second}}
}

func normalizeOutboundWebhooks(items []*pb.OutboundEmailWebhook) []*pb.OutboundEmailWebhook {
	hooks := make([]*pb.OutboundEmailWebhook, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		hook := *item
		hook.Url = strings.TrimSpace(item.GetUrl())
		if hook.GetUrl() == "" {
			continue
		}
		if strings.TrimSpace(hook.GetTokenEnv()) != "" && outboundWebhookToken(&hook) == "" {
			logWarning("skip outbound mailbox webhook %s: token env is empty", hook.GetName())
			continue
		}
		if hook.GetPreviewLimit() <= 0 {
			hook.PreviewLimit = 500
		}
		hooks = append(hooks, &hook)
	}
	return hooks
}

func (d *mailboxEventDispatcher) Dispatch(messages []*pb.EmailInboxMessage) {
	if d == nil || len(d.hooks) == 0 || len(messages) == 0 {
		return
	}
	for _, message := range messages {
		if message == nil {
			continue
		}
		for _, hook := range d.hooks {
			if !webhookFilterMatches(hook.GetFilter(), message) {
				continue
			}
			go d.post(hook, outboundWebhookEvent(hook, message))
		}
	}
}

func webhookFilterMatches(filter *pb.MailboxEmailEventFilter, message *pb.EmailInboxMessage) bool {
	if filter == nil {
		return true
	}
	recipients := append([]string{message.GetMailboxEmail()}, message.GetRecipients()...)
	if !matchesProviderFilter(filter.GetProviders(), message.GetProvider()) {
		return false
	}
	if !matchesEmail(filter.GetRecipientEmails(), recipients) {
		return false
	}
	if !matchesDomain(filter.GetRecipientDomains(), recipients) {
		return false
	}
	if !matchesEmail(filter.GetSenderEmails(), []string{message.GetFromAddress()}) {
		return false
	}
	if !matchesDomain(filter.GetSenderDomains(), []string{message.GetFromAddress()}) {
		return false
	}
	if !containsAnyKeyword(message.GetSubject(), filter.GetSubjectKeywords()) {
		return false
	}
	if !matchesSignalKind(filter.GetSignalKinds(), message.GetSignals()) {
		return false
	}
	return true
}

func outboundWebhookEvent(hook *pb.OutboundEmailWebhook, message *pb.EmailInboxMessage) *pb.OutboundEmailWebhookEvent {
	payloadMessage := cloneEmailInboxMessage(message)
	payloadMessage.BodyPreview = compactMessageText(payloadMessage.GetBodyPreview(), int(hook.GetPreviewLimit()))
	if !hook.GetIncludeBody() {
		payloadMessage.BodyText = ""
		payloadMessage.HtmlBody = ""
	} else {
		payloadMessage.BodyText = compactMessageText(payloadMessage.GetBodyText(), 2000)
		payloadMessage.HtmlBody = ""
	}
	return &pb.OutboundEmailWebhookEvent{
		EventType: outboundMailboxEmailEventType,
		Message:   payloadMessage,
	}
}

func (d *mailboxEventDispatcher) post(hook *pb.OutboundEmailWebhook, event *pb.OutboundEmailWebhookEvent) {
	raw, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(event)
	if err != nil {
		logWarning("encode outbound mailbox webhook %s: %v", hook.GetName(), err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.GetUrl(), bytes.NewReader(raw))
	if err != nil {
		logWarning("create outbound mailbox webhook %s: %v", hook.GetName(), err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if token := outboundWebhookToken(hook); token != "" {
		req.Header.Set(defaultWebhookTokenHeader, token)
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		logWarning("post outbound mailbox webhook %s: %v", hook.GetName(), err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logWarning("outbound mailbox webhook %s failed status=%d", hook.GetName(), resp.StatusCode)
	}
}

func outboundWebhookToken(hook *pb.OutboundEmailWebhook) string {
	if hook == nil {
		return ""
	}
	tokenEnv := strings.TrimSpace(hook.GetTokenEnv())
	if tokenEnv == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(tokenEnv))
}

func cloneEmailInboxMessage(message *pb.EmailInboxMessage) *pb.EmailInboxMessage {
	if message == nil {
		return nil
	}
	clone := *message
	clone.Recipients = append([]string{}, message.GetRecipients()...)
	clone.Signals = append([]*pb.EmailSignal{}, message.GetSignals()...)
	return &clone
}

func matchesProviderFilter(filters []pb.MailboxProvider, provider string) bool {
	if len(filters) == 0 {
		return true
	}
	provider = normalizeEmailProvider(provider)
	for _, filter := range filters {
		if provider == mailboxProviderKey(filter) {
			return true
		}
	}
	return false
}

func mailboxProviderKey(provider pb.MailboxProvider) string {
	if definition := providerByEnum(provider); definition != nil {
		return definition.key
	}
	return ""
}

func matchesEmail(filters []string, emails []string) bool {
	if len(filters) == 0 {
		return true
	}
	allowed := map[string]struct{}{}
	for _, filter := range filters {
		if email := normalizeEmail(filter); email != "" {
			allowed[email] = struct{}{}
		}
	}
	for _, email := range emails {
		if _, ok := allowed[normalizeEmail(email)]; ok {
			return true
		}
	}
	return false
}

func matchesDomain(filters []string, emails []string) bool {
	if len(filters) == 0 {
		return true
	}
	allowed := map[string]struct{}{}
	for _, filter := range filters {
		if domain := strings.Trim(strings.ToLower(strings.TrimSpace(filter)), "."); domain != "" {
			allowed[domain] = struct{}{}
		}
	}
	for _, email := range emails {
		if domainMatches(allowed, domainForEmail(email)) {
			return true
		}
	}
	return false
}

func domainMatches(filters map[string]struct{}, domain string) bool {
	domain = strings.Trim(strings.ToLower(strings.TrimSpace(domain)), ".")
	if domain == "" {
		return false
	}
	if _, ok := filters[domain]; ok {
		return true
	}
	for filter := range filters {
		if strings.HasSuffix(domain, "."+filter) {
			return true
		}
	}
	return false
}

func containsAnyKeyword(text string, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	text = strings.ToLower(text)
	for _, keyword := range keywords {
		if trimmed := strings.ToLower(strings.TrimSpace(keyword)); trimmed != "" && strings.Contains(text, trimmed) {
			return true
		}
	}
	return false
}

func matchesSignalKind(filters []pb.EmailSignalKind, signals []*pb.EmailSignal) bool {
	if len(filters) == 0 {
		return true
	}
	wanted := map[pb.EmailSignalKind]struct{}{}
	for _, filter := range filters {
		if filter != pb.EmailSignalKind_EMAIL_SIGNAL_KIND_UNSPECIFIED {
			wanted[filter] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return true
	}
	for _, signal := range signals {
		if _, ok := wanted[signal.GetKind()]; ok {
			return true
		}
	}
	return false
}
