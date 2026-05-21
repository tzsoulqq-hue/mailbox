package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"mailboxapi/pb"
)

type EmailService struct {
	pb.UnimplementedEmailServiceServer
	store     *MailboxStore
	watcher   *MailWatcher
	providers mailboxProviderRuntimeConfig
	inboxOnce sync.Once
	inboxGate chan struct{}
}

func (s *EmailService) acquireInboxLock(ctx context.Context) (func(), error) {
	s.inboxOnce.Do(func() {
		s.inboxGate = make(chan struct{}, 1)
	})
	select {
	case s.inboxGate <- struct{}{}:
		return func() { <-s.inboxGate }, nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, status.Error(codes.DeadlineExceeded, "inbox fetch wait timeout")
		}
		return nil, status.Error(codes.Canceled, "request cancelled")
	}
}

func (s *EmailService) MarkEmailAuthStatus(ctx context.Context, request *pb.MarkEmailAuthStatusRequest) (*pb.MarkEmailAuthStatusResponse, error) {
	mailbox, err := s.store.MarkEmailAuthStatus(ctx, request.GetEmailAddress(), request.GetAuthStatus(), request.GetLastError())
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &pb.MarkEmailAuthStatusResponse{Mailbox: mailbox}, nil
}

func (s *EmailService) UpsertMailbox(ctx context.Context, request *pb.UpsertEmailMailboxRequest) (*pb.UpsertEmailMailboxResponse, error) {
	mailbox, err := s.store.UpsertMailbox(ctx, request.GetMailbox())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &pb.UpsertEmailMailboxResponse{Mailbox: mailbox}, nil
}

func (s *EmailService) ListMailboxes(ctx context.Context, request *pb.ListEmailMailboxesRequest) (*pb.ListEmailMailboxesResponse, error) {
	mailboxes, err := s.store.ListMailboxes(ctx, request.GetAuthStatus(), request.GetProvider(), request.GetLimit())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.ListEmailMailboxesResponse{Mailboxes: mailboxes}, nil
}

func (s *EmailService) DeleteMailbox(ctx context.Context, request *pb.DeleteMailboxRequest) (*pb.DeleteMailboxResponse, error) {
	deleted, err := s.store.DeleteMailbox(ctx, request.GetEmailAddress())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &pb.DeleteMailboxResponse{Deleted: deleted}, nil
}

func (s *EmailService) FetchInboxes(ctx context.Context, request *pb.FetchInboxesRequest) (*pb.FetchInboxesResponse, error) {
	unlock, err := s.acquireInboxLock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()

	type inboxTarget struct {
		fetchMailbox  *pb.EmailMailbox
		resultMailbox *pb.EmailMailbox
	}
	targets := []inboxTarget{}
	requestedEmail := normalizeEmail(request.GetEmailAddress())
	if requestedEmail != "" {
		if resultMailbox, ok := s.providers.StoredInboxOnlyMailbox(requestedEmail); ok {
			if mailbox, err := s.store.FindMailbox(ctx, requestedEmail); err == nil {
				resultMailbox = mailbox
			}
			messages, err := s.store.ListInboxMessagesSince(ctx, requestedEmail, request.GetLimitPerMailbox(), request.GetReceivedAfterUnix())
			if err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
			return &pb.FetchInboxesResponse{
				MailboxCount: int32(1),
				FetchedCount: int32(1),
				MessageCount: int32(len(messages)),
				Results: []*pb.FetchMailboxInboxResult{{
					Mailbox:  resultMailbox,
					Messages: messages,
				}},
			}, nil
		}
		fetchMailbox, err := s.store.PollMailboxForEmail(ctx, requestedEmail)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		resultMailbox := fetchMailbox
		if mailbox, err := s.store.FindMailbox(ctx, requestedEmail); err == nil {
			resultMailbox = mailbox
		}
		targets = append(targets, inboxTarget{fetchMailbox: fetchMailbox, resultMailbox: resultMailbox})
	} else {
		mailboxes, err := s.store.ListOAuthMailboxes(ctx, request.GetMaxMailboxes())
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		for _, mailbox := range mailboxes {
			targets = append(targets, inboxTarget{fetchMailbox: mailbox, resultMailbox: mailbox})
		}
	}

	resp := &pb.FetchInboxesResponse{
		MailboxCount: int32(len(targets)),
		Results:      []*pb.FetchMailboxInboxResult{},
	}
	for _, target := range targets {
		select {
		case <-ctx.Done():
			return nil, status.Error(codes.Canceled, "request cancelled")
		default:
		}

		result := &pb.FetchMailboxInboxResult{Mailbox: target.resultMailbox}
		messages, err := s.watcher.FetchMailboxInbox(ctx, target.fetchMailbox, request.GetLimitPerMailbox(), request.GetReceivedAfterUnix())
		if err != nil {
			result.ErrorMessage = err.Error()
			if cached, cacheErr := s.store.ListInboxMessagesSince(ctx, target.fetchMailbox.GetEmailAddress(), request.GetLimitPerMailbox(), request.GetReceivedAfterUnix()); cacheErr == nil {
				result.Messages = cached
				resp.MessageCount += int32(len(cached))
			}
			resp.FailedCount++
		} else {
			result.Messages = messages
			resp.FetchedCount++
			resp.MessageCount += int32(len(messages))
		}
		resp.Results = append(resp.Results, result)
	}
	return resp, nil
}

func (s *EmailService) ListInbox(ctx context.Context, request *pb.ListMailboxInboxRequest) (*pb.ListMailboxInboxResponse, error) {
	email := normalizeEmail(request.GetEmailAddress())
	if email == "" {
		return nil, status.Error(codes.InvalidArgument, "email_address is required")
	}
	limit := request.GetLimit()
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	messages, err := s.store.ListInboxMessages(ctx, email, limit)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	resultMailbox := &pb.EmailMailbox{
		EmailAddress: email,
		Provider:     s.providers.ProviderForInboxAddress(email, messages),
		Domain:       domainForEmail(email),
	}
	prepareMailboxProjection(resultMailbox)
	if mailbox, err := s.store.FindMailbox(ctx, email); err == nil {
		resultMailbox = mailbox
	}
	return &pb.ListMailboxInboxResponse{Result: &pb.FetchMailboxInboxResult{
		Mailbox:  resultMailbox,
		Messages: messages,
	}}, nil
}

func (s *EmailService) WaitForEmail(ctx context.Context, request *pb.WaitForEmailRequest) (*pb.WaitForEmailResponse, error) {
	timeoutSeconds := request.GetTimeoutSeconds()
	if timeoutSeconds <= 0 {
		timeoutSeconds = 300
	}
	email := request.GetEmailAddress()
	issuedAfterUnix := request.GetIssuedAfterUnix()
	logInfo("waiting for email message email=%s timeout_seconds=%d issued_after_unix=%d", redactEmail(email), timeoutSeconds, issuedAfterUnix)
	if resp, ok, err := s.latestEmailResponse(ctx, request, issuedAfterUnix); err != nil {
		return nil, waitError(ctx, err)
	} else if ok {
		return resp, nil
	}
	if s.providers.IsStoredInboxOnlyAddress(email) {
		return s.waitForPersistedEmail(ctx, request, timeoutSeconds, issuedAfterUnix)
	}
	if err := s.watcher.PollForEmail(ctx, email); err != nil {
		if !isAuthError(err) {
			return nil, waitError(ctx, err)
		}
		logWarning("email poll auth error email=%s error=%v", redactEmail(email), err)
	} else if resp, ok, err := s.latestEmailResponse(ctx, request, issuedAfterUnix); err != nil {
		return nil, waitError(ctx, err)
	} else if ok {
		return resp, nil
	}

	deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
	for time.Now().Before(deadline) {
		sleepFor := time.Duration(s.watcher.pollInterval) * time.Second
		if remaining := time.Until(deadline); remaining < sleepFor {
			sleepFor = remaining
		}
		if sleepFor > 0 {
			timer := time.NewTimer(sleepFor)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, status.Error(codes.Canceled, "request cancelled")
			case <-timer.C:
			}
		}
		if err := s.watcher.PollForEmail(ctx, email); err != nil {
			if !isAuthError(err) {
				return nil, waitError(ctx, err)
			}
			continue
		}
		if resp, ok, err := s.latestEmailResponse(ctx, request, issuedAfterUnix); err != nil {
			return nil, waitError(ctx, err)
		} else if ok {
			return resp, nil
		}
	}
	logInfo("email message not found email=%s timeout_seconds=%d issued_after_unix=%d", redactEmail(email), timeoutSeconds, issuedAfterUnix)
	return &pb.WaitForEmailResponse{Found: false}, nil
}

func (s *EmailService) waitForPersistedEmail(ctx context.Context, request *pb.WaitForEmailRequest, timeoutSeconds int32, issuedAfterUnix int64) (*pb.WaitForEmailResponse, error) {
	deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
	for time.Now().Before(deadline) {
		sleepFor := time.Duration(s.watcher.pollInterval) * time.Second
		if remaining := time.Until(deadline); remaining < sleepFor {
			sleepFor = remaining
		}
		if sleepFor > 0 {
			timer := time.NewTimer(sleepFor)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, status.Error(codes.Canceled, "request cancelled")
			case <-timer.C:
			}
		}
		if resp, ok, err := s.latestEmailResponse(ctx, request, issuedAfterUnix); err != nil {
			return nil, waitError(ctx, err)
		} else if ok {
			return resp, nil
		}
	}
	logInfo("webhook-backed email message not found email=%s timeout_seconds=%d issued_after_unix=%d", redactEmail(request.GetEmailAddress()), timeoutSeconds, issuedAfterUnix)
	return &pb.WaitForEmailResponse{Found: false}, nil
}

func (s *EmailService) latestEmailResponse(ctx context.Context, request *pb.WaitForEmailRequest, issuedAfterUnix int64) (*pb.WaitForEmailResponse, bool, error) {
	message, ok, err := s.store.LatestMessageWithSignal(ctx, request.GetEmailAddress(), request.GetSubjectKeyword(), issuedAfterUnix, request.GetParserProfile(), request.GetSignalKind())
	if err != nil || !ok {
		return nil, false, err
	}
	logInfo("served persisted email for %s provider=%s received_at_unix=%d", redactEmail(request.GetEmailAddress()), message.GetProvider(), message.GetReceivedAtUnix())
	return &pb.WaitForEmailResponse{Found: true, Message: message}, true, nil
}

func waitError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return status.Error(codes.Canceled, "request cancelled")
	}
	return status.Error(codes.Internal, err.Error())
}

func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not authorized") ||
		strings.Contains(msg, "no refresh token") ||
		strings.Contains(msg, "AUTH_FAILED")
}
