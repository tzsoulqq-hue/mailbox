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
)

type mailboxActivities struct {
	mailboxRegisterClient pb.MailboxRegistrationServiceClient
	operations            *operationStore
}

func (a *mailboxActivities) RunMailboxRegistration(ctx context.Context, input registerMailboxWorkflowInput) (mailboxOperationResult, error) {
	operationID := strings.TrimSpace(input.OperationID)
	if err := a.markRunning(ctx, operationID, "run_registration"); err != nil {
		return mailboxOperationResult{OperationID: operationID, ErrorMessage: err.Error()}, err
	}

	resp, err := a.mailboxRegisterClient.RunMailboxRegistration(ctx, &pb.RunMailboxRegistrationRequest{
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
		message := "mailbox registration service returned empty response"
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
	if !result.Success && result.ErrorMessage == "" {
		result.ErrorMessage = "mailbox registration failed"
	}
	a.finishOperation(ctx, operationID, "run_registration", result)
	return result, nil
}

func (a *mailboxActivities) RunMailboxOAuth(ctx context.Context, input mailboxOAuthWorkflowInput) (mailboxOperationResult, error) {
	operationID := strings.TrimSpace(input.OperationID)
	if err := a.markRunning(ctx, operationID, "run_oauth"); err != nil {
		return mailboxOperationResult{OperationID: operationID, ErrorMessage: err.Error()}, err
	}

	resp, err := a.mailboxRegisterClient.RunMailboxOAuth(ctx, &pb.RunMailboxOAuthRequest{
		EmailAddress: normalizeEmail(input.EmailAddress),
		OnlyMissing:  input.OnlyMissing,
		Limit:        normalizedLimit(input.Limit),
	})
	if err != nil {
		message := fmt.Sprintf("run mailbox OAuth: %v", err)
		a.updateOperation(ctx, operationID, operationUpdate{
			Status:       operationStatusFailed,
			LastStep:     "run_oauth",
			ErrorMessage: message,
		})
		return mailboxOperationResult{OperationID: operationID, Success: false, ErrorMessage: message}, workflowruntime.NewRetryableActivityError(activityErrorMailboxOAuthUnavailable, message, err)
	}
	if resp == nil {
		message := "mailbox registration service returned empty OAuth response"
		a.updateOperation(ctx, operationID, operationUpdate{
			Status:       operationStatusFailed,
			LastStep:     "run_oauth",
			ErrorMessage: message,
		})
		return mailboxOperationResult{OperationID: operationID, Success: false, ErrorMessage: message}, workflowruntime.NewNonRetryableActivityError(activityErrorMailboxOAuthEmpty, message, nil)
	}

	result := mailboxOperationResult{
		OperationID:  operationID,
		Success:      resp.GetSuccess(),
		ErrorMessage: strings.TrimSpace(resp.GetErrorMessage()),
		MailboxCount: resp.GetProcessed(),
		FetchedCount: resp.GetSucceeded(),
		FailedCount:  resp.GetFailed(),
	}
	if !result.Success && result.ErrorMessage == "" {
		result.ErrorMessage = "mailbox OAuth failed"
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
