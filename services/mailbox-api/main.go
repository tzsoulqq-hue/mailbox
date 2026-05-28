package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	workflowruntime "github.com/byte-v-forge/workflow-runtime"
	"go.temporal.io/sdk/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	browserautomationv1 "github.com/byte-v-forge/browser-automation/gen/go/byte/v/forge/contracts/browserautomation/v1"

	"mailboxapi/pb"
)

type config struct {
	listenAddr            string
	pgDSN                 string
	webhookHTTPAddr       string
	browserAutomationAddr string
	providers             mailboxProviderRuntimeConfig
	workflowRuntime       workflowruntime.Config
}

type server struct {
	pb.UnimplementedMailboxServiceServer

	emailBackend      emailBackend
	operations        *operationStore
	workflowClient    client.Client
	workflowTaskQueue string
	providers         mailboxProviderRuntimeConfig
	emailEvents       *mailboxEmailEventBus
	outlookRecovery   *outlookRegistrationRunner
}

func main() {
	cfg := loadConfig()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	browserConn, err := newGRPCClient("browser automation", cfg.browserAutomationAddr)
	if err != nil {
		log.Fatalf("failed to connect browser automation: %v", err)
	}
	defer browserConn.Close()

	mailboxStore, err := NewMailboxStore(ctx, cfg.pgDSN)
	if err != nil {
		log.Fatalf("failed to initialize mailbox store: %v", err)
	}
	defer mailboxStore.Close()
	emailEvents := newMailboxEmailEventBus()
	mailWatcher := NewMailWatcher(mailboxStore, emailEvents)
	emailBackend := &EmailService{store: mailboxStore, watcher: mailWatcher, providers: cfg.providers}

	operations, err := newOperationStore(cfg.pgDSN)
	if err != nil {
		log.Fatalf("failed to initialize mailbox operation store: %v", err)
	}

	workflowClient, err := workflowruntime.Dial(cfg.workflowRuntime)
	if err != nil {
		log.Fatalf("failed to connect workflow runtime: %v", err)
	}
	defer workflowClient.Close()

	activities := newMailboxActivitiesForProviders(cfg.providers, browserautomationv1.NewBrowserAutomationServiceClient(browserConn), emailBackend, operations)
	worker, err := workflowruntime.NewWorker(workflowClient, mailboxWorkerSpec(cfg.workflowRuntime.TaskQueue, activities))
	if err != nil {
		log.Fatalf("failed to create mailbox workflow worker: %v", err)
	}
	if err := worker.Start(); err != nil {
		log.Fatalf("failed to start mailbox workflow worker: %v", err)
	}
	defer worker.Stop()

	errCh := make(chan error, 2)
	startWebhookServer(ctx, cfg.webhookHTTPAddr, mailboxStore, mailWatcher, errCh)

	listener, err := net.Listen("tcp", cfg.listenAddr)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", cfg.listenAddr, err)
	}

	grpcServer := grpc.NewServer()
	browserClient := browserautomationv1.NewBrowserAutomationServiceClient(browserConn)
	pb.RegisterMailboxServiceServer(grpcServer, &server{
		emailBackend:      emailBackend,
		operations:        operations,
		workflowClient:    workflowClient,
		workflowTaskQueue: cfg.workflowRuntime.TaskQueue,
		providers:         cfg.providers,
		emailEvents:       emailEvents,
		outlookRecovery:   newOutlookRegistrationRunner(cfg.providers.registration, browserClient, nil),
	})

	go func() {
		<-ctx.Done()
		grpcServer.GracefulStop()
	}()

	log.Printf("mailbox API listening on %s", cfg.listenAddr)
	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			errCh <- fmt.Errorf("mailbox API failed: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		stop()
		log.Fatal(err)
	}
}

func loadConfig() config {
	workflowRuntime, err := workflowruntime.LoadConfigFromEnv(os.Getenv)
	if err != nil {
		log.Fatalf("load workflow runtime config: %v", err)
	}
	return config{
		listenAddr:            envDefault("LISTEN_ADDR", ":50051"),
		pgDSN:                 requiredEnv("MAILBOX_PG_DSN"),
		webhookHTTPAddr:       envDefault("MAILBOX_WEBHOOK_HTTP_ADDR", ":8082"),
		browserAutomationAddr: envDefault("BROWSER_AUTOMATION_ADDR", "browser-automation:50051"),
		providers:             loadMailboxProviderRuntimeConfig(),
		workflowRuntime:       workflowRuntime,
	}
}

func envDefault(name string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func requiredEnv(name string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	log.Fatalf("%s is required", name)
	return ""
}

func newGRPCClient(name string, addr string) (*grpc.ClientConn, error) {
	if strings.TrimSpace(addr) == "" {
		return nil, fmt.Errorf("%s address is required", name)
	}
	return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

type emailBackend interface {
	ListMailboxes(context.Context, *pb.ListEmailMailboxesRequest) (*pb.ListEmailMailboxesResponse, error)
	UpsertMailbox(context.Context, *pb.UpsertEmailMailboxRequest) (*pb.UpsertEmailMailboxResponse, error)
	DeleteMailbox(context.Context, *pb.DeleteMailboxRequest) (*pb.DeleteMailboxResponse, error)
	WaitForEmail(context.Context, *pb.WaitForEmailRequest) (*pb.WaitForEmailResponse, error)
	ListInbox(context.Context, *pb.ListMailboxInboxRequest) (*pb.ListMailboxInboxResponse, error)
	FetchInboxes(context.Context, *pb.FetchInboxesRequest) (*pb.FetchInboxesResponse, error)
	MarkEmailAuthStatus(context.Context, *pb.MarkEmailAuthStatusRequest) (*pb.MarkEmailAuthStatusResponse, error)
}

func (s *server) ListMailboxes(ctx context.Context, req *pb.ListEmailMailboxesRequest) (*pb.ListEmailMailboxesResponse, error) {
	resp, err := s.emailBackend.ListMailboxes(ctx, req)
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
	if mailbox.GetProvider() == "" {
		mailbox.Provider = defaultMailboxProvider()
	} else {
		mailbox.Provider = normalizeMailboxProviderInput(mailbox.GetProvider())
	}
	resp, err := s.emailBackend.UpsertMailbox(ctx, req)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "upsert mailbox: %v", err)
	}
	if resp == nil || resp.GetMailbox() == nil {
		return nil, status.Error(codes.Internal, "email service returned empty mailbox")
	}
	return resp, nil
}

func (s *server) ListMailboxDomains(ctx context.Context, req *pb.ListMailboxDomainsRequest) (*pb.ListMailboxDomainsResponse, error) {
	return s.providers.ListDomains(req), nil
}

func (s *server) SyncMailboxDomains(ctx context.Context, req *pb.SyncMailboxDomainsRequest) (*pb.SyncMailboxDomainsResponse, error) {
	return s.providers.SyncDomains(req), nil
}

func (s *server) ListMailboxProviderCapabilities(ctx context.Context, req *pb.ListMailboxProviderCapabilitiesRequest) (*pb.ListMailboxProviderCapabilitiesResponse, error) {
	return s.providers.ListCapabilities(req), nil
}

func (s *server) DeleteMailbox(ctx context.Context, req *pb.DeleteMailboxRequest) (*pb.DeleteMailboxResponse, error) {
	email := normalizeEmail(req.GetEmailAddress())
	if email == "" {
		return nil, status.Error(codes.InvalidArgument, "email_address is required")
	}
	resp, err := s.emailBackend.DeleteMailbox(ctx, &pb.DeleteMailboxRequest{EmailAddress: email})
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "delete mailbox: %v", err)
	}
	if resp == nil {
		return nil, status.Error(codes.Internal, "email service returned empty delete response")
	}
	return resp, nil
}

func (s *server) WaitForMailboxEmail(ctx context.Context, req *pb.WaitForEmailRequest) (*pb.WaitForEmailResponse, error) {
	email := normalizeEmail(req.GetEmailAddress())
	if email == "" {
		return nil, status.Error(codes.InvalidArgument, "email_address is required")
	}
	resp, err := s.emailBackend.WaitForEmail(ctx, &pb.WaitForEmailRequest{
		EmailAddress:    email,
		SubjectKeyword:  strings.TrimSpace(req.GetSubjectKeyword()),
		TimeoutSeconds:  req.GetTimeoutSeconds(),
		IssuedAfterUnix: req.GetIssuedAfterUnix(),
		ParserProfile:   req.GetParserProfile(),
		SignalKind:      req.GetSignalKind(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "wait for mailbox email: %v", err)
	}
	if resp == nil {
		return nil, status.Error(codes.Internal, "email service returned empty wait response")
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
		MaxCount:    req.GetMaxCount(),
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

func (s *server) StartMailboxManualRecovery(ctx context.Context, req *pb.StartMailboxManualRecoveryRequest) (*pb.StartMailboxManualRecoveryResponse, error) {
	email := normalizeEmail(req.GetEmailAddress())
	if email == "" {
		return &pb.StartMailboxManualRecoveryResponse{Started: false, ErrorMessage: "email_address is required"}, nil
	}
	if s.outlookRecovery == nil {
		return &pb.StartMailboxManualRecoveryResponse{EmailAddress: email, Started: false, ErrorMessage: "Outlook recovery is not configured"}, nil
	}
	resp, err := s.emailBackend.ListMailboxes(ctx, &pb.ListEmailMailboxesRequest{Provider: emailProviderOutlook, Limit: 500})
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "list Outlook mailboxes: %v", err)
	}
	var mailbox *pb.EmailMailbox
	for _, candidate := range resp.GetMailboxes() {
		if normalizeEmail(candidate.GetEmailAddress()) == email {
			mailbox = candidate
			break
		}
	}
	if mailbox == nil {
		return &pb.StartMailboxManualRecoveryResponse{EmailAddress: email, Started: false, ErrorMessage: "mailbox not found"}, nil
	}
	if strings.TrimSpace(mailbox.GetHomeCountry()) == "" {
		return &pb.StartMailboxManualRecoveryResponse{EmailAddress: email, Started: false, ErrorMessage: "mailbox home_country is required for manual recovery"}, nil
	}
	session, err := s.outlookRecovery.PrepareLocalManualRecovery(ctx, mailbox)
	if err != nil {
		return &pb.StartMailboxManualRecoveryResponse{EmailAddress: email, Started: false, ErrorMessage: err.Error()}, nil
	}
	return &pb.StartMailboxManualRecoveryResponse{
		EmailAddress:  session.email,
		SessionId:     session.sessionID,
		ProxyCountry:  session.proxyCountry,
		ProxySession:  session.proxySession,
		Started:       true,
		ErrorMessage:  "",
		LocalProxyUrl: session.localProxyURL,
		RecoveryUrl:   session.recoveryURL,
		LaunchCommand: session.launchCommand,
		Instruction:   session.instruction,
	}, nil
}

func (s *server) startMailboxWorkflow(ctx context.Context, operationID string, workflowName string, input any) error {
	if s.workflowClient == nil {
		return fmt.Errorf("workflow runtime client is not initialized")
	}
	_, err := s.workflowClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        operationID,
		TaskQueue: s.workflowTaskQueue,
	}, workflowName, input)
	return err
}

func (s *server) ListMailboxInbox(ctx context.Context, req *pb.ListMailboxInboxRequest) (*pb.ListMailboxInboxResponse, error) {
	resp, err := s.emailBackend.ListInbox(ctx, req)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "list mailbox inbox: %v", err)
	}
	if resp == nil || resp.GetResult() == nil {
		return nil, status.Error(codes.Internal, "email service returned empty inbox result")
	}
	return resp, nil
}

func (s *server) StreamMailboxEmailEvents(req *pb.StreamMailboxEmailEventsRequest, stream pb.MailboxService_StreamMailboxEmailEventsServer) error {
	if s.emailEvents == nil {
		return status.Error(codes.Unavailable, "mailbox email event stream is not configured")
	}
	email := normalizeEmail(req.GetEmailAddress())
	if email == "" {
		return status.Error(codes.InvalidArgument, "email_address is required")
	}
	events := s.emailEvents.Subscribe(stream.Context(), mailboxEmailEventFilter{
		email:          email,
		subjectKeyword: req.GetSubjectKeyword(),
		parserProfile:  req.GetParserProfile(),
		signalKind:     req.GetSignalKind(),
	})
	for {
		select {
		case <-stream.Context().Done():
			return status.Error(codes.Canceled, "stream cancelled")
		case message, ok := <-events:
			if !ok {
				return nil
			}
			if err := stream.Send(&pb.StreamMailboxEmailEventsResponse{
				EmailAddress: email,
				Message:      message,
			}); err != nil {
				return err
			}
		}
	}
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

	resp, err := s.emailBackend.FetchInboxes(ctx, &pb.FetchInboxesRequest{
		LimitPerMailbox:   req.GetLimitPerMailbox(),
		MaxMailboxes:      req.GetMaxMailboxes(),
		EmailAddress:      email,
		ParserProfile:     req.GetParserProfile(),
		ReceivedAfterUnix: req.GetReceivedAfterUnix(),
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
