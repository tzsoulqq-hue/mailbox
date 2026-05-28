package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"mailboxapi/pb"
)

const (
	operationActionRegisterMailbox = "REGISTER_MAILBOX"
	operationActionMailboxOAuth    = "MAILBOX_OAUTH"
	operationActionFetchInboxes    = "FETCH_INBOXES"

	operationStatusCreated   = "CREATED"
	operationStatusRunning   = "RUNNING"
	operationStatusSucceeded = "SUCCEEDED"
	operationStatusFailed    = "FAILED"
)

type mailboxOperationRow struct {
	OperationID  string `gorm:"primaryKey;column:operation_id"`
	Action       string `gorm:"index"`
	Status       string `gorm:"index"`
	EmailAddress string `gorm:"index"`
	LastStep     string
	ErrorMessage string
	ExitCode     int32
	MailboxCount int32
	FetchedCount int32
	FailedCount  int32
	MessageCount int32
	CreatedAt    int64 `gorm:"autoCreateTime"`
	UpdatedAt    int64 `gorm:"autoUpdateTime"`
}

func (mailboxOperationRow) TableName() string {
	return "mailbox_operations"
}

type mailboxOperationStepRow struct {
	ID           uint   `gorm:"primaryKey"`
	OperationID  string `gorm:"index:idx_mailbox_operation_steps_operation_step,unique"`
	StepName     string `gorm:"index:idx_mailbox_operation_steps_operation_step,unique"`
	Status       string
	ErrorMessage string
	StartedAt    int64
	CompletedAt  int64
	CreatedAt    int64 `gorm:"autoCreateTime"`
	UpdatedAt    int64 `gorm:"autoUpdateTime"`
}

func (mailboxOperationStepRow) TableName() string {
	return "mailbox_operation_steps"
}

type operationStore struct {
	db *gorm.DB
}

type operationUpdate struct {
	Status       string
	LastStep     string
	ErrorMessage string
	ExitCode     int32
	MailboxCount int32
	FetchedCount int32
	FailedCount  int32
	MessageCount int32
}

type operationListFilter struct {
	Limit        int
	Status       string
	Action       string
	EmailAddress string
}

func newOperationStore(dsn string) (*operationStore, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&mailboxOperationRow{}, &mailboxOperationStepRow{}); err != nil {
		return nil, err
	}
	return &operationStore{db: db}, nil
}

func (s *operationStore) create(ctx context.Context, operationID, action, emailAddress string) (*pb.MailboxOperation, error) {
	row := &mailboxOperationRow{
		OperationID:  strings.TrimSpace(operationID),
		Action:       strings.ToUpper(strings.TrimSpace(action)),
		Status:       operationStatusCreated,
		EmailAddress: normalizeEmail(emailAddress),
		LastStep:     "created",
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	_ = s.upsertStep(ctx, row.OperationID, "created", operationStatusCreated, "", row.CreatedAt, 0)
	return operationRowToProto(row), nil
}

func (s *operationStore) update(ctx context.Context, operationID string, update operationUpdate) (*pb.MailboxOperation, error) {
	updates := map[string]any{}
	if value := strings.ToUpper(strings.TrimSpace(update.Status)); value != "" {
		updates["status"] = value
	}
	if value := strings.TrimSpace(update.LastStep); value != "" {
		updates["last_step"] = value
	}
	updates["error_message"] = strings.TrimSpace(update.ErrorMessage)
	updates["exit_code"] = update.ExitCode
	updates["mailbox_count"] = update.MailboxCount
	updates["fetched_count"] = update.FetchedCount
	updates["failed_count"] = update.FailedCount
	updates["message_count"] = update.MessageCount

	if err := s.db.WithContext(ctx).Model(&mailboxOperationRow{}).
		Where("operation_id = ?", strings.TrimSpace(operationID)).
		Updates(updates).Error; err != nil {
		return nil, err
	}
	if step := strings.TrimSpace(update.LastStep); step != "" {
		operation, _ := s.get(ctx, operationID)
		now := int64(0)
		if operation != nil {
			now = operation.GetUpdatedAt()
		}
		if now <= 0 {
			now = timeNowUnix()
		}
		status := strings.ToUpper(strings.TrimSpace(update.Status))
		completedAt := int64(0)
		if status != "" && status != operationStatusRunning && status != operationStatusCreated {
			completedAt = now
		}
		previousStatus := operationStatusSucceeded
		if status == operationStatusFailed {
			previousStatus = operationStatusFailed
		}
		_ = s.completePreviousRunningSteps(ctx, operationID, step, now, previousStatus, update.ErrorMessage)
		_ = s.upsertStep(ctx, operationID, step, status, update.ErrorMessage, now, completedAt)
	}
	return s.get(ctx, operationID)
}

func (s *operationStore) completePreviousRunningSteps(ctx context.Context, operationID string, currentStep string, completedAt int64, statusValue string, errorMessage string) error {
	operationID = strings.TrimSpace(operationID)
	currentStep = strings.TrimSpace(currentStep)
	if operationID == "" || currentStep == "" || completedAt <= 0 {
		return nil
	}
	statusValue = strings.ToUpper(strings.TrimSpace(statusValue))
	if statusValue == "" {
		statusValue = operationStatusSucceeded
	}
	updates := map[string]any{
		"status":       statusValue,
		"completed_at": completedAt,
	}
	if statusValue == operationStatusFailed {
		updates["error_message"] = strings.TrimSpace(errorMessage)
	}
	return s.db.WithContext(ctx).Model(&mailboxOperationStepRow{}).
		Where("operation_id = ? AND step_name <> ? AND step_name <> ? AND status = ? AND completed_at = 0", operationID, currentStep, "run_registration", operationStatusRunning).
		Updates(updates).Error
}

func (s *operationStore) upsertStep(ctx context.Context, operationID string, stepName string, statusValue string, errorMessage string, startedAt int64, completedAt int64) error {
	operationID = strings.TrimSpace(operationID)
	stepName = strings.TrimSpace(stepName)
	if operationID == "" || stepName == "" {
		return nil
	}
	statusValue = strings.ToUpper(strings.TrimSpace(statusValue))
	if statusValue == "" {
		statusValue = operationStatusRunning
	}
	if startedAt <= 0 {
		startedAt = timeNowUnix()
	}
	row := mailboxOperationStepRow{}
	err := s.db.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).First(&row, "operation_id = ? AND step_name = ?", operationID, stepName).Error
	if err == nil {
		updates := map[string]any{
			"status":        statusValue,
			"error_message": strings.TrimSpace(errorMessage),
		}
		if row.StartedAt <= 0 {
			updates["started_at"] = startedAt
		}
		if completedAt > 0 {
			updates["completed_at"] = completedAt
		}
		return s.db.WithContext(ctx).Model(&mailboxOperationStepRow{}).Where("id = ?", row.ID).Updates(updates).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	row = mailboxOperationStepRow{
		OperationID:  operationID,
		StepName:     stepName,
		Status:       statusValue,
		ErrorMessage: strings.TrimSpace(errorMessage),
		StartedAt:    startedAt,
		CompletedAt:  completedAt,
	}
	return s.db.WithContext(ctx).Create(&row).Error
}

func (s *operationStore) get(ctx context.Context, operationID string) (*pb.MailboxOperation, error) {
	var row mailboxOperationRow
	if err := s.db.WithContext(ctx).First(&row, "operation_id = ?", strings.TrimSpace(operationID)).Error; err != nil {
		return nil, err
	}
	operation := operationRowToProto(&row)
	steps, err := s.steps(ctx, operationID)
	if err == nil {
		operation.Steps = steps
	}
	return operation, nil
}

func (s *operationStore) list(ctx context.Context, filter operationListFilter) ([]*pb.MailboxOperation, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := s.db.WithContext(ctx).Model(&mailboxOperationRow{})
	if value := strings.ToUpper(strings.TrimSpace(filter.Status)); value != "" {
		query = query.Where("status = ?", value)
	}
	if value := strings.ToUpper(strings.TrimSpace(filter.Action)); value != "" {
		query = query.Where("action = ?", value)
	}
	if value := normalizeEmail(filter.EmailAddress); value != "" {
		query = query.Where("email_address = ?", value)
	}

	var rows []mailboxOperationRow
	if err := query.Order("updated_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	operations := make([]*pb.MailboxOperation, 0, len(rows))
	for i := range rows {
		operation := operationRowToProto(&rows[i])
		steps, err := s.steps(ctx, rows[i].OperationID)
		if err == nil {
			operation.Steps = steps
		}
		operations = append(operations, operation)
	}
	return operations, nil
}

func operationRowToProto(row *mailboxOperationRow) *pb.MailboxOperation {
	if row == nil {
		return nil
	}
	return &pb.MailboxOperation{
		OperationId:  row.OperationID,
		Action:       row.Action,
		Status:       row.Status,
		EmailAddress: row.EmailAddress,
		LastStep:     row.LastStep,
		ErrorMessage: row.ErrorMessage,
		ExitCode:     row.ExitCode,
		MailboxCount: row.MailboxCount,
		FetchedCount: row.FetchedCount,
		FailedCount:  row.FailedCount,
		MessageCount: row.MessageCount,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

func (s *operationStore) steps(ctx context.Context, operationID string) ([]*pb.MailboxOperationStep, error) {
	var rows []mailboxOperationStepRow
	if err := s.db.WithContext(ctx).Where("operation_id = ?", strings.TrimSpace(operationID)).Order("started_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	steps := make([]*pb.MailboxOperationStep, 0, len(rows))
	for i := range rows {
		steps = append(steps, &pb.MailboxOperationStep{
			StepName:     rows[i].StepName,
			Status:       rows[i].Status,
			ErrorMessage: rows[i].ErrorMessage,
			StartedAt:    rows[i].StartedAt,
			CompletedAt:  rows[i].CompletedAt,
		})
	}
	return steps, nil
}

func timeNowUnix() int64 {
	return time.Now().Unix()
}
