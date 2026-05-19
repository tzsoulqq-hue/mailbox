package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	workflowruntime "github.com/byte-v-forge/workflow-runtime"
	"go.temporal.io/sdk/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"mailboxapi/pb"
)

type config struct {
	listenAddr          string
	pgDSN               string
	emailAddr           string
	mailboxRegisterAddr string
	temporal            workflowruntime.Config
}

type server struct {
	pb.UnimplementedMailboxServiceServer

	emailClient       pb.EmailServiceClient
	operations        *operationStore
	workflowClient    client.Client
	workflowTaskQueue string
}

func main() {
	cfg := loadConfig()
	emailConn, err := newGRPCClient("email service", cfg.emailAddr)
	if err != nil {
		log.Fatalf("failed to connect email service: %v", err)
	}
	defer emailConn.Close()

	registerConn, err := newGRPCClient("mailbox registration service", cfg.mailboxRegisterAddr)
	if err != nil {
		log.Fatalf("failed to connect mailbox registration service: %v", err)
	}
	defer registerConn.Close()

	operations, err := newOperationStore(cfg.pgDSN)
	if err != nil {
		log.Fatalf("failed to initialize mailbox operation store: %v", err)
	}

	temporalClient, err := workflowruntime.Dial(cfg.temporal)
	if err != nil {
		log.Fatalf("failed to connect Temporal: %v", err)
	}
	defer temporalClient.Close()

	activities := &mailboxActivities{
		mailboxRegisterClient: pb.NewMailboxRegistrationServiceClient(registerConn),
		operations:            operations,
	}
	worker, err := workflowruntime.NewWorker(temporalClient, mailboxWorkerSpec(cfg.temporal.TaskQueue, activities))
	if err != nil {
		log.Fatalf("failed to create mailbox Temporal worker: %v", err)
	}
	if err := worker.Start(); err != nil {
		log.Fatalf("failed to start mailbox Temporal worker: %v", err)
	}
	defer worker.Stop()

	listener, err := net.Listen("tcp", cfg.listenAddr)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", cfg.listenAddr, err)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterMailboxServiceServer(grpcServer, &server{
		emailClient:       pb.NewEmailServiceClient(emailConn),
		operations:        operations,
		workflowClient:    temporalClient,
		workflowTaskQueue: cfg.temporal.TaskQueue,
	})

	log.Printf("mailbox API listening on %s", cfg.listenAddr)
	if err := grpcServer.Serve(listener); err != nil {
		log.Fatalf("mailbox API failed: %v", err)
	}
}

func loadConfig() config {
	temporal, err := workflowruntime.LoadConfigFromEnv(os.Getenv)
	if err != nil {
		log.Fatalf("load Temporal config: %v", err)
	}
	return config{
		listenAddr:          envDefault("LISTEN_ADDR", ":50051"),
		pgDSN:               requiredEnvAny("MAILBOX_API_PG_DSN", "PG_DSN"),
		emailAddr:           envDefault("EMAIL_ADDR", "outlook-imap-service:50051"),
		mailboxRegisterAddr: envDefault("MAILBOX_REGISTER_ADDR", "outlook-register-service:50051"),
		temporal:            temporal,
	}
}

func envDefault(name string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func requiredEnvAny(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	log.Fatalf("%s is required", strings.Join(names, " or "))
	return ""
}

func newGRPCClient(name string, addr string) (*grpc.ClientConn, error) {
	if strings.TrimSpace(addr) == "" {
		return nil, fmt.Errorf("%s address is required", name)
	}
	return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

func (s *server) ListMailboxes(ctx context.Context, req *pb.ListEmailMailboxesRequest) (*pb.ListEmailMailboxesResponse, error) {
	resp, err := s.emailClient.ListMailboxes(ctx, req)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "list mailboxes: %v", err)
	}
	if resp == nil {
		return nil, status.Error(codes.Internal, "email service returned empty mailbox list")
	}
	return resp, nil
}

func (s *server) UpsertMailbox(ctx context.Context, req *pb.UpsertEmailMailboxRequest) (*pb.UpsertEmailMailboxResponse, error) {
	mailbox := req.GetMailbox()
	if mailbox == nil || normalizeEmail(mailbox.GetEmailAddress()) == "" {
		return nil, status.Error(codes.InvalidArgument, "mailbox email_address is required")
	}
	mailbox.EmailAddress = normalizeEmail(mailbox.GetEmailAddress())
	if mailbox.GetPrimaryEmail() == "" {
		mailbox.PrimaryEmail = mailbox.GetEmailAddress()
	}
	resp, err := s.emailClient.UpsertMailbox(ctx, req)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "upsert mailbox: %v", err)
	}
	if resp == nil || resp.GetMailbox() == nil {
		return nil, status.Error(codes.Internal, "email service returned empty mailbox")
	}
	return resp, nil
}

func (s *server) DeleteMailbox(ctx context.Context, req *pb.DeleteMailboxRequest) (*pb.DeleteMailboxResponse, error) {
	email := normalizeEmail(req.GetEmailAddress())
	if email == "" {
		return nil, status.Error(codes.InvalidArgument, "email_address is required")
	}
	resp, err := s.emailClient.DeleteMailbox(ctx, &pb.DeleteMailboxRequest{EmailAddress: email})
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "delete mailbox: %v", err)
	}
	if resp == nil {
		return nil, status.Error(codes.Internal, "email service returned empty delete response")
	}
	return resp, nil
}

func (s *server) RegisterMailbox(ctx context.Context, req *pb.RegisterMailboxRequest) (*pb.RegisterMailboxResponse, error) {
	operationID := operationID("mailbox-register")
	if _, err := s.operations.create(ctx, operationID, operationActionRegisterMailbox, ""); err != nil {
		return nil, status.Errorf(codes.Internal, "create mailbox operation: %v", err)
	}

	if err := s.startMailboxWorkflow(ctx, operationID, registerMailboxWorkflowName, registerMailboxWorkflowInput{
		OperationID: operationID,
		ImportOnly:  req.GetImportOnly(),
	}); err != nil {
		s.updateOperation(ctx, operationID, operationUpdate{
			Status:       operationStatusFailed,
			LastStep:     "start_workflow",
			ErrorMessage: err.Error(),
		})
		return &pb.RegisterMailboxResponse{
			OperationId:  operationID,
			Started:      false,
			ErrorMessage: err.Error(),
		}, nil
	}

	return &pb.RegisterMailboxResponse{
		OperationId: operationID,
		Started:     true,
	}, nil
}

func (s *server) RunMailboxOAuth(ctx context.Context, req *pb.StartMailboxOAuthRequest) (*pb.StartMailboxOAuthResponse, error) {
	operationID := operationID("mailbox-oauth")
	email := normalizeEmail(req.GetEmailAddress())
	if _, err := s.operations.create(ctx, operationID, operationActionMailboxOAuth, email); err != nil {
		return nil, status.Errorf(codes.Internal, "create mailbox operation: %v", err)
	}

	if err := s.startMailboxWorkflow(ctx, operationID, mailboxOAuthWorkflowName, mailboxOAuthWorkflowInput{
		OperationID:  operationID,
		EmailAddress: email,
		OnlyMissing:  req.GetOnlyMissing(),
		Limit:        normalizedLimit(req.GetLimit()),
	}); err != nil {
		s.updateOperation(ctx, operationID, operationUpdate{
			Status:       operationStatusFailed,
			LastStep:     "start_workflow",
			ErrorMessage: err.Error(),
		})
		return &pb.StartMailboxOAuthResponse{OperationId: operationID, ErrorMessage: err.Error()}, nil
	}
	return &pb.StartMailboxOAuthResponse{
		OperationId: operationID,
		Started:     true,
	}, nil
}

func (s *server) startMailboxWorkflow(ctx context.Context, operationID string, workflowName string, input any) error {
	if s.workflowClient == nil {
		return fmt.Errorf("Temporal client is not initialized")
	}
	_, err := s.workflowClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        operationID,
		TaskQueue: s.workflowTaskQueue,
	}, workflowName, input)
	return err
}

func (s *server) FetchMailboxInboxes(ctx context.Context, req *pb.FetchMailboxInboxesRequest) (*pb.FetchMailboxInboxesResponse, error) {
	operationID := operationID("mailbox-inbox")
	email := normalizeEmail(req.GetEmailAddress())
	if _, err := s.operations.create(ctx, operationID, operationActionFetchInboxes, email); err != nil {
		return nil, status.Errorf(codes.Internal, "create mailbox operation: %v", err)
	}
	if _, err := s.operations.update(ctx, operationID, operationUpdate{
		Status:   operationStatusRunning,
		LastStep: "fetch_inboxes",
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "mark mailbox operation running: %v", err)
	}

	resp, err := s.emailClient.FetchInboxes(ctx, &pb.FetchInboxesRequest{
		LimitPerMailbox: req.GetLimitPerMailbox(),
		MaxMailboxes:    req.GetMaxMailboxes(),
		EmailAddress:    email,
	})
	if err != nil {
		s.updateOperation(ctx, operationID, operationUpdate{
			Status:       operationStatusFailed,
			LastStep:     "fetch_inboxes",
			ErrorMessage: err.Error(),
		})
		return nil, status.Errorf(codes.Unavailable, "fetch mailbox inboxes: %v", err)
	}
	if resp == nil {
		s.updateOperation(ctx, operationID, operationUpdate{
			Status:       operationStatusFailed,
			LastStep:     "fetch_inboxes",
			ErrorMessage: "email service returned empty inbox response",
		})
		return nil, status.Error(codes.Internal, "email service returned empty inbox response")
	}
	statusValue := operationStatusSucceeded
	if resp.GetFailedCount() > 0 {
		statusValue = operationStatusFailed
	}
	s.updateOperation(ctx, operationID, operationUpdate{
		Status:       statusValue,
		LastStep:     "fetch_inboxes",
		MailboxCount: resp.GetMailboxCount(),
		FetchedCount: resp.GetFetchedCount(),
		FailedCount:  resp.GetFailedCount(),
		MessageCount: resp.GetMessageCount(),
	})
	return &pb.FetchMailboxInboxesResponse{
		Results:      resp.GetResults(),
		MailboxCount: resp.GetMailboxCount(),
		FetchedCount: resp.GetFetchedCount(),
		FailedCount:  resp.GetFailedCount(),
		MessageCount: resp.GetMessageCount(),
		OperationId:  operationID,
	}, nil
}

func (s *server) GetMailboxOperation(ctx context.Context, req *pb.GetMailboxOperationRequest) (*pb.GetMailboxOperationResponse, error) {
	operationID := strings.TrimSpace(req.GetOperationId())
	if operationID == "" {
		return &pb.GetMailboxOperationResponse{ErrorMessage: "operation_id is required"}, nil
	}
	operation, err := s.operations.get(ctx, operationID)
	if err != nil {
		return &pb.GetMailboxOperationResponse{ErrorMessage: err.Error()}, nil
	}
	return &pb.GetMailboxOperationResponse{Operation: operation}, nil
}

func (s *server) ListMailboxOperations(ctx context.Context, req *pb.ListMailboxOperationsRequest) (*pb.ListMailboxOperationsResponse, error) {
	operations, err := s.operations.list(ctx, operationListFilter{
		Limit:        int(req.GetLimit()),
		Status:       req.GetStatus(),
		Action:       req.GetAction(),
		EmailAddress: req.GetEmailAddress(),
	})
	if err != nil {
		return &pb.ListMailboxOperationsResponse{ErrorMessage: err.Error()}, nil
	}
	return &pb.ListMailboxOperationsResponse{Operations: operations}, nil
}

func (s *server) updateOperation(ctx context.Context, operationID string, update operationUpdate) {
	if _, err := s.operations.update(ctx, operationID, update); err != nil {
		log.Printf("update mailbox operation failed operation=%s: %v", operationID, err)
	}
}

func normalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizedLimit(limit int32) int32 {
	if limit <= 0 {
		return 100
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func operationID(prefix string) string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return prefix + "-" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}
