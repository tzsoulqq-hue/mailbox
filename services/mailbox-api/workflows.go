package main

import (
	"time"

	workflowruntime "github.com/byte-v-forge/workflow-runtime"
	"go.temporal.io/sdk/workflow"

	"mailboxapi/pb"
)

const (
	registerMailboxWorkflowName            = "mailbox.RegisterMailbox"
	mailboxOAuthWorkflowName               = "mailbox.RunMailboxOAuth"
	runMailboxRegistrationActivityName     = "mailbox.RunMailboxRegistration"
	selectMailboxOAuthAccountsActivityName = "mailbox.SelectMailboxOAuthAccounts"
	runMailboxOAuthAccountActivityName     = "mailbox.RunMailboxOAuthAccount"
	completeMailboxOAuthActivityName       = "mailbox.CompleteMailboxOAuth"
	mailboxRegistrationActivityTimeout     = 30 * time.Minute
	mailboxOAuthSelectActivityTimeout      = 2 * time.Minute
	mailboxOAuthAccountActivityTimeout     = 10 * time.Minute
	mailboxOAuthCompleteActivityTimeout    = 2 * time.Minute
	mailboxActivityScheduleTimeoutPadding  = 5 * time.Minute
)

type registerMailboxWorkflowInput struct {
	OperationID string
	ImportOnly  bool
}

type mailboxOAuthWorkflowInput struct {
	OperationID  string
	EmailAddress string
	OnlyMissing  bool
	Limit        int32
}

type mailboxOperationResult struct {
	OperationID  string
	Success      bool
	ErrorMessage string
	ExitCode     int32
	MailboxCount int32
	FetchedCount int32
	FailedCount  int32
	MessageCount int32
}

func mailboxWorkerSpec(taskQueue string, activities *mailboxActivities) workflowruntime.WorkerSpec {
	return workflowruntime.WorkerSpec{
		TaskQueue: taskQueue,
		Workflows: []workflowruntime.WorkflowDefinition{
			{Name: registerMailboxWorkflowName, Definition: RegisterMailboxWorkflow},
			{Name: mailboxOAuthWorkflowName, Definition: RunMailboxOAuthWorkflow},
		},
		Activities: []workflowruntime.ActivityDefinition{
			{Name: runMailboxRegistrationActivityName, Definition: activities.RunMailboxRegistration},
			{Name: selectMailboxOAuthAccountsActivityName, Definition: activities.SelectMailboxOAuthAccounts},
			{Name: runMailboxOAuthAccountActivityName, Definition: activities.RunMailboxOAuthAccount},
			{Name: completeMailboxOAuthActivityName, Definition: activities.CompleteMailboxOAuth},
		},
	}
}

func RegisterMailboxWorkflow(ctx workflow.Context, input registerMailboxWorkflowInput) (mailboxOperationResult, error) {
	ctx = workflowruntime.WithDefaultActivityOptions(ctx, mailboxActivityOptions(mailboxRegistrationActivityTimeout))
	var result mailboxOperationResult
	err := workflow.ExecuteActivity(ctx, runMailboxRegistrationActivityName, input).Get(ctx, &result)
	return result, err
}

func RunMailboxOAuthWorkflow(ctx workflow.Context, input mailboxOAuthWorkflowInput) (mailboxOperationResult, error) {
	selectCtx := workflowruntime.WithDefaultActivityOptions(ctx, mailboxActivityOptions(mailboxOAuthSelectActivityTimeout))
	accountCtx := workflowruntime.WithDefaultActivityOptions(ctx, mailboxActivityOptions(mailboxOAuthAccountActivityTimeout))
	completeCtx := workflowruntime.WithDefaultActivityOptions(ctx, mailboxActivityOptions(mailboxOAuthCompleteActivityTimeout))

	var selection pb.SelectMailboxOAuthAccountsResponse
	if err := workflow.ExecuteActivity(selectCtx, selectMailboxOAuthAccountsActivityName, &pb.SelectMailboxOAuthAccountsRequest{
		OperationId:  input.OperationID,
		EmailAddress: input.EmailAddress,
		OnlyMissing:  input.OnlyMissing,
		Limit:        input.Limit,
	}).Get(ctx, &selection); err != nil {
		return mailboxOperationResult{OperationID: input.OperationID, Success: false, ErrorMessage: err.Error()}, err
	}

	results := make([]*pb.MailboxOAuthResult, 0, len(selection.GetAccounts()))
	for _, account := range selection.GetAccounts() {
		var accountResult pb.RunMailboxOAuthAccountResponse
		err := workflow.ExecuteActivity(accountCtx, runMailboxOAuthAccountActivityName, &pb.RunMailboxOAuthAccountRequest{
			OperationId: input.OperationID,
			Account:     account,
		}).Get(ctx, &accountResult)
		if err != nil {
			results = append(results, &pb.MailboxOAuthResult{
				EmailAddress: account.GetEmailAddress(),
				Success:      false,
				ErrorMessage: err.Error(),
			})
			continue
		}
		if accountResult.GetResult() != nil {
			results = append(results, accountResult.GetResult())
		}
	}

	var result mailboxOperationResult
	err := workflow.ExecuteActivity(completeCtx, completeMailboxOAuthActivityName, &pb.CompleteMailboxOAuthRequest{
		OperationId: input.OperationID,
		Accounts:    selection.GetAccounts(),
		Results:     results,
	}).Get(ctx, &result)
	return result, err
}

func mailboxActivityOptions(timeout time.Duration) workflowruntime.ActivityOptionsMutator {
	return func(options *workflow.ActivityOptions) {
		options.StartToCloseTimeout = timeout
		options.ScheduleToCloseTimeout = timeout + mailboxActivityScheduleTimeoutPadding
		options.HeartbeatTimeout = 0
	}
}
