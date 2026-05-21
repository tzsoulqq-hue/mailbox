package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	workflowruntime "github.com/byte-v-forge/workflow-runtime"

	"mailboxapi/pb"
)

const (
	activityErrorMailboxRegistrationUnavailable = "MAILBOX_REGISTRATION_UNAVAILABLE"
	activityErrorMailboxRegistrationEmpty       = "MAILBOX_REGISTRATION_EMPTY"
	activityErrorMailboxOAuthUnavailable        = "MAILBOX_OAUTH_UNAVAILABLE"
	activityErrorMailboxOAuthEmpty              = "MAILBOX_OAUTH_EMPTY"
	emailAuthAuthorized                         = "AUTHORIZED"
	emailAuthOAuthPending                       = "OAUTH_PENDING"
	emailAuthFailed                             = "AUTH_FAILED"
	emailAuthNeedsManualVerification            = "NEEDS_MANUAL_VERIFICATION"
)

type mailboxActivities struct {
	outlookRegistration *outlookRegistrationRunner
	emailBackend        emailBackend
	operations          *operationStore
}

func (a *mailboxActivities) RunMailboxRegistration(ctx context.Context, input registerMailboxWorkflowInput) (mailboxOperationResult, error) {
	operationID := strings.TrimSpace(input.OperationID)
	if err := a.markRunning(ctx, operationID, "run_registration"); err != nil {
		return mailboxOperationResult{OperationID: operationID, ErrorMessage: err.Error()}, err
	}

	resp, err := a.outlookRegistration.RunMailboxRegistration(ctx, &pb.RunMailboxRegistrationRequest{
		Enabled:    !input.ImportOnly,
		ImportOnly: input.ImportOnly,
	})
	if err != nil {
		message := fmt.Sprintf("run mailbox registration: %v", err)
		a.updateOperation(ctx, operationID, operationUpdate{
			Status:       operationStatusFailed,
			LastStep:     "run_registration",
			ErrorMessage: message,
		})
		return mailboxOperationResult{OperationID: operationID, Success: false, ErrorMessage: message}, workflowruntime.NewRetryableActivityError(activityErrorMailboxRegistrationUnavailable, message, err)
	}
	if resp == nil {
		message := "mailbox registration runner returned empty response"
		a.updateOperation(ctx, operationID, operationUpdate{
			Status:       operationStatusFailed,
			LastStep:     "run_registration",
			ErrorMessage: message,
		})
		return mailboxOperationResult{OperationID: operationID, Success: false, ErrorMessage: message}, workflowruntime.NewNonRetryableActivityError(activityErrorMailboxRegistrationEmpty, message, nil)
	}

	result := mailboxOperationResult{
		OperationID:  operationID,
		Success:      resp.GetSuccess(),
		ExitCode:     resp.GetExitCode(),
		ErrorMessage: strings.TrimSpace(resp.GetErrorMessage()),
		MailboxCount: registeredMailboxCount(resp.GetAccounts()),
	}
	if result.Success {
		if err := a.persistRegisteredAccounts(ctx, resp.GetAccounts()); err != nil {
			result.Success = false
			result.ErrorMessage = fmt.Sprintf("persist registered mailboxes: %v", err)
		}
	}
	if !result.Success && result.ErrorMessage == "" {
		result.ErrorMessage = "mailbox registration failed"
	}
	a.finishOperation(ctx, operationID, "run_registration", result)
	return result, nil
}

func (a *mailboxActivities) SelectMailboxOAuthAccounts(ctx context.Context, req *pb.SelectMailboxOAuthAccountsRequest) (*pb.SelectMailboxOAuthAccountsResponse, error) {
	operationID := strings.TrimSpace(req.GetOperationId())
	if err := a.markRunning(ctx, operationID, "run_oauth"); err != nil {
		return nil, err
	}

	accounts, err := a.oauthAccounts(ctx, req.GetEmailAddress(), req.GetOnlyMissing(), normalizedLimit(req.GetLimit()))
	if err != nil {
		message := fmt.Sprintf("select mailbox OAuth accounts: %v", err)
		a.updateOperation(ctx, operationID, operationUpdate{
			Status:       operationStatusFailed,
			LastStep:     "run_oauth",
			ErrorMessage: message,
		})
		return &pb.SelectMailboxOAuthAccountsResponse{}, nil
	}
	return &pb.SelectMailboxOAuthAccountsResponse{Accounts: accounts}, nil
}

func (a *mailboxActivities) RunMailboxOAuthAccount(ctx context.Context, req *pb.RunMailboxOAuthAccountRequest) (*pb.RunMailboxOAuthAccountResponse, error) {
	account := req.GetAccount()
	if account == nil || normalizeEmail(account.GetEmailAddress()) == "" {
		return &pb.RunMailboxOAuthAccountResponse{Result: &pb.MailboxOAuthResult{
			Success:      false,
			ErrorMessage: "mailbox OAuth account is required",
		}}, nil
	}
	resp, err := a.outlookRegistration.RunMailboxOAuth(ctx, &pb.RunMailboxOAuthRequest{
		EmailAddress: normalizeEmail(account.GetEmailAddress()),
		OnlyMissing:  false,
		Limit:        1,
		Accounts:     []*pb.MailboxRegistrationAccount{account},
	})
	if err != nil {
		message := fmt.Sprintf("run mailbox OAuth: %v", err)
		return nil, workflowruntime.NewRetryableActivityError(activityErrorMailboxOAuthUnavailable, message, err)
	}
	if resp == nil {
		message := "mailbox registration runner returned empty OAuth response"
		return nil, workflowruntime.NewNonRetryableActivityError(activityErrorMailboxOAuthEmpty, message, nil)
	}
	if len(resp.GetResults()) == 0 {
		return &pb.RunMailboxOAuthAccountResponse{Result: &pb.MailboxOAuthResult{
			EmailAddress: normalizeEmail(account.GetEmailAddress()),
			Success:      false,
			ErrorMessage: "mailbox OAuth returned no result",
		}}, nil
	}
	return &pb.RunMailboxOAuthAccountResponse{Result: resp.GetResults()[0]}, nil
}

func (a *mailboxActivities) CompleteMailboxOAuth(ctx context.Context, req *pb.CompleteMailboxOAuthRequest) (mailboxOperationResult, error) {
	operationID := strings.TrimSpace(req.GetOperationId())
	results := req.GetResults()
	response := &pb.RunMailboxOAuthResponse{Processed: int32(len(req.GetAccounts())), Results: results}
	for _, result := range results {
		if result.GetSuccess() {
			response.Succeeded++
		} else {
			response.Failed++
		}
	}
	response.Success = response.Processed > 0 && response.Failed == 0 && response.Succeeded > 0
	if !response.Success {
		response.ErrorMessage = fmt.Sprintf("mailbox OAuth failed: %d/%d", response.Failed, response.Processed)
	}

	result := mailboxOperationResult{
		OperationID:  operationID,
		Success:      response.GetSuccess(),
		ErrorMessage: strings.TrimSpace(response.GetErrorMessage()),
		MailboxCount: response.GetProcessed(),
		FetchedCount: response.GetSucceeded(),
		FailedCount:  response.GetFailed(),
	}
	if err := a.persistOAuthResults(ctx, response.GetResults(), req.GetAccounts()); err != nil {
		result.Success = false
		result.ErrorMessage = fmt.Sprintf("persist mailbox OAuth results: %v", err)
	}
	if !result.Success && result.ErrorMessage == "" {
		result.ErrorMessage = "mailbox OAuth failed"
	}
	if len(req.GetAccounts()) == 0 {
		result.ErrorMessage = "no mailbox accounts eligible for OAuth"
		a.updateOperation(ctx, operationID, operationUpdate{
			Status:       operationStatusFailed,
			LastStep:     "run_oauth",
			ErrorMessage: result.ErrorMessage,
		})
		return result, nil
	}
	a.finishOperation(ctx, operationID, "run_oauth", result)
	return result, nil
}

func (a *mailboxActivities) markRunning(ctx context.Context, operationID string, step string) error {
	_, err := a.operations.update(ctx, operationID, operationUpdate{
		Status:   operationStatusRunning,
		LastStep: step,
	})
	return err
}

func (a *mailboxActivities) finishOperation(ctx context.Context, operationID string, step string, result mailboxOperationResult) {
	statusValue := operationStatusSucceeded
	if !result.Success || strings.TrimSpace(result.ErrorMessage) != "" {
		statusValue = operationStatusFailed
	}
	a.updateOperation(ctx, operationID, operationUpdate{
		Status:       statusValue,
		LastStep:     step,
		ErrorMessage: result.ErrorMessage,
		ExitCode:     result.ExitCode,
		MailboxCount: result.MailboxCount,
		FetchedCount: result.FetchedCount,
		FailedCount:  result.FailedCount,
		MessageCount: result.MessageCount,
	})
}

func (a *mailboxActivities) updateOperation(ctx context.Context, operationID string, update operationUpdate) {
	if _, err := a.operations.update(ctx, operationID, update); err != nil {
		log.Printf("update mailbox operation failed operation=%s: %v", operationID, err)
	}
}

func registeredMailboxCount(accounts []*pb.MailboxRegistrationAccount) int32 {
	var count int32
	for _, account := range accounts {
		if normalizeEmail(account.GetEmailAddress()) != "" {
			count++
		}
	}
	return count
}

func (a *mailboxActivities) persistRegisteredAccounts(ctx context.Context, accounts []*pb.MailboxRegistrationAccount) error {
	for _, account := range accounts {
		email := normalizeEmail(account.GetEmailAddress())
		password := strings.TrimSpace(account.GetPassword())
		if email == "" {
			continue
		}
		if password == "" {
			return fmt.Errorf("mailbox account missing password: %s", email)
		}
		if err := a.upsertMailbox(ctx, &pb.EmailMailbox{
			EmailAddress: email,
			Password:     password,
			RefreshToken: strings.TrimSpace(account.GetRefreshToken()),
			AccessToken:  strings.TrimSpace(account.GetAccessToken()),
			AuthStatus:   mailboxAuthStatus(account.GetRefreshToken(), ""),
			LastError:    "",
		}); err != nil {
			return err
		}
	}
	return nil
}

func (a *mailboxActivities) oauthAccounts(ctx context.Context, emailAddress string, onlyMissing bool, limit int32) ([]*pb.MailboxRegistrationAccount, error) {
	requestedEmail := normalizeEmail(emailAddress)
	selectedLimit := limit
	if requestedEmail != "" {
		selectedLimit = 500
	}
	resp, err := a.emailBackend.ListMailboxes(ctx, &pb.ListEmailMailboxesRequest{Limit: selectedLimit})
	if err != nil {
		return nil, fmt.Errorf("list mailboxes: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("email service returned empty mailbox list")
	}
	accounts := make([]*pb.MailboxRegistrationAccount, 0, len(resp.GetMailboxes()))
	for _, mailbox := range resp.GetMailboxes() {
		email := normalizeEmail(mailbox.GetEmailAddress())
		if email == "" {
			continue
		}
		if requestedEmail != "" && email != requestedEmail {
			continue
		}
		if strings.TrimSpace(mailbox.GetPassword()) == "" {
			continue
		}
		authStatus := mailboxAuthStatus(mailbox.GetRefreshToken(), mailbox.GetAuthStatus())
		if onlyMissing && (authStatus == emailAuthAuthorized || authStatus == emailAuthNeedsManualVerification) {
			continue
		}
		accounts = append(accounts, &pb.MailboxRegistrationAccount{
			EmailAddress: email,
			Password:     strings.TrimSpace(mailbox.GetPassword()),
			RefreshToken: strings.TrimSpace(mailbox.GetRefreshToken()),
			AccessToken:  strings.TrimSpace(mailbox.GetAccessToken()),
			Source:       "mailboxes",
		})
		if requestedEmail == "" && int32(len(accounts)) >= selectedLimit {
			break
		}
	}
	if requestedEmail != "" && len(accounts) == 0 {
		return nil, fmt.Errorf("mailbox not found or not eligible for OAuth: %s", requestedEmail)
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("no mailbox accounts eligible for OAuth")
	}
	return accounts, nil
}

func (a *mailboxActivities) persistOAuthResults(ctx context.Context, results []*pb.MailboxOAuthResult, accounts []*pb.MailboxRegistrationAccount) error {
	accountByEmail := make(map[string]*pb.MailboxRegistrationAccount, len(accounts))
	for _, account := range accounts {
		if email := normalizeEmail(account.GetEmailAddress()); email != "" {
			accountByEmail[email] = account
		}
	}
	for _, result := range results {
		email := normalizeEmail(result.GetEmailAddress())
		if email == "" {
			continue
		}
		refreshToken := strings.TrimSpace(result.GetRefreshToken())
		account := accountByEmail[email]
		password := ""
		existingRefreshToken := ""
		if account != nil {
			password = strings.TrimSpace(account.GetPassword())
			existingRefreshToken = strings.TrimSpace(account.GetRefreshToken())
		}
		if result.GetSuccess() && refreshToken != "" {
			if err := a.upsertMailbox(ctx, &pb.EmailMailbox{
				EmailAddress: email,
				Password:     password,
				RefreshToken: refreshToken,
				AccessToken:  strings.TrimSpace(result.GetAccessToken()),
				AuthStatus:   emailAuthAuthorized,
				LastError:    "",
			}); err != nil {
				return err
			}
			continue
		}
		if !result.GetSuccess() {
			authStatus := mailboxOAuthFailureStatus(result.GetErrorMessage())
			if mailboxOAuthFailureIsRuntime(result.GetErrorMessage()) && existingRefreshToken != "" {
				authStatus = emailAuthAuthorized
			}
			if err := a.markEmailAuthStatus(ctx, email, authStatus, result.GetErrorMessage()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *mailboxActivities) upsertMailbox(ctx context.Context, mailbox *pb.EmailMailbox) error {
	resp, err := a.emailBackend.UpsertMailbox(ctx, &pb.UpsertEmailMailboxRequest{Mailbox: mailbox})
	if err != nil {
		return fmt.Errorf("upsert mailbox %s: %w", normalizeEmail(mailbox.GetEmailAddress()), err)
	}
	if resp == nil || resp.GetMailbox() == nil {
		return fmt.Errorf("email service returned empty mailbox for %s", normalizeEmail(mailbox.GetEmailAddress()))
	}
	return nil
}

func (a *mailboxActivities) markEmailAuthStatus(ctx context.Context, email string, authStatus string, lastError string) error {
	resp, err := a.emailBackend.MarkEmailAuthStatus(ctx, &pb.MarkEmailAuthStatusRequest{
		EmailAddress: normalizeEmail(email),
		AuthStatus:   authStatus,
		LastError:    strings.TrimSpace(lastError),
	})
	if err != nil {
		return fmt.Errorf("mark mailbox auth status %s: %w", normalizeEmail(email), err)
	}
	if resp == nil || resp.GetMailbox() == nil {
		return fmt.Errorf("email service returned empty auth status response for %s", normalizeEmail(email))
	}
	return nil
}

func mailboxAuthStatus(refreshToken string, explicitStatus string) string {
	if status := strings.TrimSpace(explicitStatus); status != "" {
		return status
	}
	if strings.TrimSpace(refreshToken) != "" {
		return emailAuthAuthorized
	}
	return "OAUTH_PENDING"
}

func mailboxOAuthFailureStatus(errorMessage string) string {
	errorText := strings.ToLower(strings.TrimSpace(errorMessage))
	if mailboxOAuthFailureIsRuntime(errorMessage) {
		return emailAuthOAuthPending
	}
	if strings.Contains(errorText, "needs_manual_verification") || strings.Contains(errorText, "account.live.com/abuse") {
		return emailAuthNeedsManualVerification
	}
	return emailAuthFailed
}

func mailboxOAuthFailureIsRuntime(errorMessage string) bool {
	errorText := strings.ToLower(strings.TrimSpace(errorMessage))
	return strings.Contains(errorText, "camoufox") ||
		strings.Contains(errorText, "browserserverimpl.js") ||
		strings.Contains(errorText, "server process terminated unexpectedly") ||
		strings.Contains(errorText, "browser automation") ||
		strings.Contains(errorText, "browser unavailable") ||
		strings.Contains(errorText, "me-token-to-replace")
}
