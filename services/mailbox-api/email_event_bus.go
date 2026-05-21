package main

import (
	"context"
	"strings"
	"sync"

	"mailboxapi/pb"
)

type mailboxEmailEventBus struct {
	mu          sync.RWMutex
	subscribers map[chan *pb.EmailInboxMessage]mailboxEmailEventFilter
}

type mailboxEmailEventFilter struct {
	email          string
	subjectKeyword string
	parserProfile  string
	signalKind     pb.EmailSignalKind
}

func newMailboxEmailEventBus() *mailboxEmailEventBus {
	return &mailboxEmailEventBus{subscribers: map[chan *pb.EmailInboxMessage]mailboxEmailEventFilter{}}
}

func (b *mailboxEmailEventBus) Subscribe(ctx context.Context, filter mailboxEmailEventFilter) <-chan *pb.EmailInboxMessage {
	ch := make(chan *pb.EmailInboxMessage, 16)
	b.mu.Lock()
	b.subscribers[ch] = filter.normalize()
	b.mu.Unlock()
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subscribers, ch)
		close(ch)
		b.mu.Unlock()
	}()
	return ch
}

func (b *mailboxEmailEventBus) Publish(messages []*pb.EmailInboxMessage) {
	if b == nil || len(messages) == 0 {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, message := range messages {
		if message == nil {
			continue
		}
		for ch, filter := range b.subscribers {
			event := filter.apply(message)
			if event == nil {
				continue
			}
			select {
			case ch <- event:
			default:
			}
		}
	}
}

func (f mailboxEmailEventFilter) normalize() mailboxEmailEventFilter {
	f.email = normalizeEmail(f.email)
	f.subjectKeyword = strings.ToLower(strings.TrimSpace(f.subjectKeyword))
	f.parserProfile = strings.TrimSpace(f.parserProfile)
	return f
}

func (f mailboxEmailEventFilter) apply(message *pb.EmailInboxMessage) *pb.EmailInboxMessage {
	if f.email != "" && !mailboxEventMatchesEmail(message, f.email) {
		return nil
	}
	if f.subjectKeyword != "" && !strings.Contains(strings.ToLower(message.GetSubject()), f.subjectKeyword) {
		return nil
	}
	event := emailMessageWithSignals(cloneEmailInboxMessage(message), f.parserProfile)
	if f.signalKind != pb.EmailSignalKind_EMAIL_SIGNAL_KIND_UNSPECIFIED && !messageHasSignal(event, f.signalKind) {
		return nil
	}
	return event
}

func mailboxEventMatchesEmail(message *pb.EmailInboxMessage, email string) bool {
	if normalizeEmail(message.GetMailboxEmail()) == email || normalizeEmail(message.GetSourceMailboxEmail()) == email {
		return true
	}
	for _, recipient := range message.GetRecipients() {
		if normalizeEmail(recipient) == email {
			return true
		}
	}
	return false
}
