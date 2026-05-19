package main

import (
	"time"

	workflowruntime "github.com/byte-v-forge/workflow-runtime"
	"go.temporal.io/sdk/workflow"
)

const (
	registerMailboxWorkflowName           = "mailbox.RegisterMailbox"
	mailboxOAuthWorkflowName              = "mailbox.RunMailboxOAuth"
	runMailboxRegistrationActivityName    = "mailbox.RunMailboxRegistration"
	runMailboxOAuthActivityName           = "mailbox.RunMailboxOAuth"
	mailboxRegistrationActivityTimeout    = 30 * time.Minute
	mailboxOAuthActivityTimeout           = 30 * time.Minute
	mailboxActivityScheduleTimeoutPadding = 5 * time.Minute
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
			{Name: runMailboxOAuthActivityName, Definition: activities.RunMailboxOAuth},
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
	ctx = workflowruntime.WithDefaultActivityOptions(ctx, mailboxActivityOptions(mailboxOAuthActivityTimeout))
	var result mailboxOperationResult
	err := workflow.ExecuteActivity(ctx, runMailboxOAuthActivityName, input).Get(ctx, &result)
	return result, err
}

func mailboxActivityOptions(timeout time.Duration) workflowruntime.ActivityOptionsMutator {
	return func(options *workflow.ActivityOptions) {
		options.StartToCloseTimeout = timeout
		options.ScheduleToCloseTimeout = timeout + mailboxActivityScheduleTimeoutPadding
		options.HeartbeatTimeout = 0
	}
}
