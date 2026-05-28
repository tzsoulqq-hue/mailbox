package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	browserautomationv1 "github.com/byte-v-forge/browser-automation/gen/go/byte/v/forge/contracts/browserautomation/v1"

	"mailboxapi/pb"
)

type mailboxProviderRuntimeConfig struct {
	domainStore  *mailboxProviderDomainStore
	registration outlookRegistrationConfig
}

type mailboxProviderDomainStore struct {
	mu         sync.RWMutex
	byProvider map[string][]string
}

type mailboxProviderDefinition struct {
	key               string
	aliases           []string
	provider          pb.MailboxProvider
	displayName       string
	storedInboxOnly   bool
	schemaStatements  func() []string
	selectJoin        string
	selectFields      mailboxProviderSelectFields
	capabilities      func() *pb.MailboxProviderCapabilities
	loadDomains       func() []string
	domains           func([]string) []*pb.MailboxDomain
	matchesAddress    func(string, mailboxProviderRuntimeConfig) bool
	upsert            func(context.Context, pgx.Tx, *pb.EmailMailbox, int64) error
	authFilter        func(string, *[]any) string
	validatePoll      func(*mailboxRow) error
	updateAuth        func(context.Context, pgx.Tx, string, string, string, int64) error
	updateTokens      func(context.Context, *pgxpool.Pool, string, string, string) error
	pruneInbound      func(context.Context, pgx.Tx, mailboxInboxRetention) error
	virtualMailboxes  func(context.Context, *pgxpool.Pool, int) ([]*pb.EmailMailbox, error)
	includeVirtual    func(string) bool
	prepareProjection func(*pb.EmailMailbox)
	prepareLegacyData func() []string
}

type mailboxProviderSelectFields struct {
	password               string
	refreshToken           string
	accessToken            string
	authStatus             string
	lastError              string
	homeCountry            string
	homeIP                 string
	proxyProfile           string
	lastProxyCountry       string
	lastProxySession       string
	lastProxyIP            string
	manualRecoveryRequired string
}

type mailboxInboxRetention struct {
	touchedMailboxes map[string]struct{}
	touchedDomains   map[string]struct{}
}

func loadMailboxProviderRuntimeConfig() mailboxProviderRuntimeConfig {
	cfg := mailboxProviderRuntimeConfig{
		domainStore:  &mailboxProviderDomainStore{byProvider: map[string][]string{}},
		registration: loadOutlookRegistrationConfig(),
	}
	for _, provider := range mailboxProviders() {
		if provider.loadDomains != nil {
			cfg.domainStore.set(provider.key, provider.loadDomains())
		}
	}
	return cfg
}

func (c mailboxProviderRuntimeConfig) domainsForProvider(provider string) []string {
	if c.domainStore == nil {
		return nil
	}
	return c.domainStore.get(provider)
}

func (s *mailboxProviderDomainStore) get(provider string) []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string{}, s.byProvider[normalizeMailboxProviderInput(provider)]...)
}

func (s *mailboxProviderDomainStore) set(provider string, domains []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byProvider[normalizeMailboxProviderInput(provider)] = append([]string{}, domains...)
}

func (c mailboxProviderRuntimeConfig) ListDomains(req *pb.ListMailboxDomainsRequest) *pb.ListMailboxDomainsResponse {
	provider := providerByEnum(req.GetProvider())
	if req.GetProvider() != pb.MailboxProvider_MAILBOX_PROVIDER_UNSPECIFIED {
		if provider == nil || provider.domains == nil {
			return &pb.ListMailboxDomainsResponse{Domains: []*pb.MailboxDomain{}}
		}
		return &pb.ListMailboxDomainsResponse{Domains: provider.domains(c.domainsForProvider(provider.key))}
	}
	domains := []*pb.MailboxDomain{}
	for _, provider := range mailboxProviders() {
		if provider.domains != nil {
			domains = append(domains, provider.domains(c.domainsForProvider(provider.key))...)
		}
	}
	return &pb.ListMailboxDomainsResponse{Domains: domains}
}

func (c mailboxProviderRuntimeConfig) SyncDomains(req *pb.SyncMailboxDomainsRequest) *pb.SyncMailboxDomainsResponse {
	provider := providerByEnum(req.GetProvider())
	if req.GetProvider() != pb.MailboxProvider_MAILBOX_PROVIDER_UNSPECIFIED && provider == nil {
		return &pb.SyncMailboxDomainsResponse{ErrorMessage: "provider cannot sync domains"}
	}
	providers := mailboxProviders()
	if provider != nil {
		providers = []*mailboxProviderDefinition{provider}
	}
	for _, candidate := range providers {
		if candidate.loadDomains == nil {
			continue
		}
		c.domainStore.set(candidate.key, candidate.loadDomains())
	}
	domains := c.ListDomains(&pb.ListMailboxDomainsRequest{Provider: req.GetProvider()}).GetDomains()
	return &pb.SyncMailboxDomainsResponse{Domains: domains, SyncedCount: int32(len(domains))}
}

func (c mailboxProviderRuntimeConfig) ListCapabilities(req *pb.ListMailboxProviderCapabilitiesRequest) *pb.ListMailboxProviderCapabilitiesResponse {
	providers := []*pb.MailboxProviderCapabilities{}
	for _, provider := range mailboxProviders() {
		if req.GetProvider() != pb.MailboxProvider_MAILBOX_PROVIDER_UNSPECIFIED && req.GetProvider() != provider.provider {
			continue
		}
		if provider.capabilities != nil {
			providers = append(providers, provider.capabilities())
		}
	}
	return &pb.ListMailboxProviderCapabilitiesResponse{Providers: providers}
}

func (c mailboxProviderRuntimeConfig) StoredInboxOnlyMailbox(email string) (*pb.EmailMailbox, bool) {
	email = normalizeEmail(email)
	if email == "" {
		return nil, false
	}
	for _, provider := range mailboxProviders() {
		if !provider.storedInboxOnly || provider.matchesAddress == nil || !provider.matchesAddress(email, c) {
			continue
		}
		mailbox := &pb.EmailMailbox{
			EmailAddress: email,
			Provider:     provider.key,
			Domain:       domainForEmail(email),
		}
		prepareMailboxProjection(mailbox)
		return mailbox, true
	}
	return nil, false
}

func (c mailboxProviderRuntimeConfig) ProviderForInboxAddress(email string, messages []*pb.EmailInboxMessage) string {
	for _, message := range messages {
		if provider := normalizeEmailProvider(message.GetProvider()); provider != "" {
			return provider
		}
	}
	if mailbox, ok := c.StoredInboxOnlyMailbox(email); ok {
		return mailbox.GetProvider()
	}
	return defaultMailboxProvider()
}

func (c mailboxProviderRuntimeConfig) IsStoredInboxOnlyAddress(email string) bool {
	_, ok := c.StoredInboxOnlyMailbox(email)
	return ok
}

func newMailboxActivitiesForProviders(cfg mailboxProviderRuntimeConfig, browserClient browserautomationv1.BrowserAutomationServiceClient, emailBackend emailBackend, operations *operationStore) *mailboxActivities {
	return &mailboxActivities{
		outlookRegistration: newOutlookRegistrationRunner(cfg.registration, browserClient, nil),
		emailBackend:        emailBackend,
		operations:          operations,
	}
}

func mailboxProviders() []*mailboxProviderDefinition {
	return []*mailboxProviderDefinition{
		outlookMailboxProvider(),
		cloudflareMailboxProvider(),
	}
}

func defaultMailboxProvider() string {
	providers := mailboxProviders()
	if len(providers) == 0 {
		return ""
	}
	return providers[0].key
}

func normalizeMailboxProviderInput(provider string) string {
	value := strings.ToLower(strings.TrimSpace(provider))
	if value == "" {
		return ""
	}
	if definition := providerByKey(value); definition != nil {
		return definition.key
	}
	return value
}

func providerByEnum(provider pb.MailboxProvider) *mailboxProviderDefinition {
	for _, definition := range mailboxProviders() {
		if definition.provider == provider {
			return definition
		}
	}
	return nil
}

func providerByKey(provider string) *mailboxProviderDefinition {
	value := strings.ToLower(strings.TrimSpace(provider))
	for _, definition := range mailboxProviders() {
		if definition.key == value {
			return definition
		}
		for _, alias := range definition.aliases {
			if alias == value {
				return definition
			}
		}
	}
	return nil
}

func mailboxProviderSchemaStatements() []string {
	statements := []string{}
	for _, provider := range mailboxProviders() {
		if provider.schemaStatements != nil {
			statements = append(statements, provider.schemaStatements()...)
		}
	}
	return statements
}

func mailboxProviderLegacyStatements() []string {
	statements := []string{}
	for _, provider := range mailboxProviders() {
		if provider.prepareLegacyData != nil {
			statements = append(statements, provider.prepareLegacyData()...)
		}
	}
	return statements
}

func mailboxSelectSQL() string {
	fields := mailboxProviderFieldExpressions()
	joins := ""
	for _, provider := range mailboxProviders() {
		if strings.TrimSpace(provider.selectJoin) != "" {
			joins += "\n" + provider.selectJoin
		}
	}
	return fmt.Sprintf(`
	SELECT m.id, m.email, m.provider,
		%s AS password,
		%s AS refresh_token,
		%s AS access_token,
		%s AS auth_status,
		%s AS last_error,
		%s AS home_country,
		%s AS home_ip,
		%s AS proxy_profile,
		%s AS last_proxy_country,
		%s AS last_proxy_session,
		%s AS last_proxy_ip,
		%s AS manual_recovery_required,
		m.created_at, m.updated_at
	FROM mailboxes m%s
`, fields.password, fields.refreshToken, fields.accessToken, fields.authStatus, fields.lastError,
		fields.homeCountry, fields.homeIP, fields.proxyProfile, fields.lastProxyCountry,
		fields.lastProxySession, fields.lastProxyIP, fields.manualRecoveryRequired, joins)
}

func mailboxProviderFieldExpressions() mailboxProviderSelectFields {
	expressions := mailboxProviderSelectFields{
		password:               "''",
		refreshToken:           "''",
		accessToken:            "''",
		authStatus:             "''",
		lastError:              "''",
		homeCountry:            "''",
		homeIP:                 "''",
		proxyProfile:           "''",
		lastProxyCountry:       "''",
		lastProxySession:       "''",
		lastProxyIP:            "''",
		manualRecoveryRequired: "FALSE",
	}
	for _, provider := range mailboxProviders() {
		fields := provider.selectFields
		expressions.password = coalesceProviderField(expressions.password, fields.password)
		expressions.refreshToken = coalesceProviderField(expressions.refreshToken, fields.refreshToken)
		expressions.accessToken = coalesceProviderField(expressions.accessToken, fields.accessToken)
		expressions.authStatus = coalesceProviderField(expressions.authStatus, fields.authStatus)
		expressions.lastError = coalesceProviderField(expressions.lastError, fields.lastError)
		expressions.homeCountry = coalesceProviderField(expressions.homeCountry, fields.homeCountry)
		expressions.homeIP = coalesceProviderField(expressions.homeIP, fields.homeIP)
		expressions.proxyProfile = coalesceProviderField(expressions.proxyProfile, fields.proxyProfile)
		expressions.lastProxyCountry = coalesceProviderField(expressions.lastProxyCountry, fields.lastProxyCountry)
		expressions.lastProxySession = coalesceProviderField(expressions.lastProxySession, fields.lastProxySession)
		expressions.lastProxyIP = coalesceProviderField(expressions.lastProxyIP, fields.lastProxyIP)
		expressions.manualRecoveryRequired = coalesceBoolProviderField(expressions.manualRecoveryRequired, fields.manualRecoveryRequired)
	}
	return expressions
}

func coalesceProviderField(current string, next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return current
	}
	if current == "''" {
		return fmt.Sprintf("COALESCE(%s, '')", next)
	}
	return fmt.Sprintf("COALESCE(NULLIF(%s, ''), %s)", next, current)
}

func coalesceBoolProviderField(current string, next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return current
	}
	if current == "FALSE" {
		return fmt.Sprintf("COALESCE(%s, FALSE)", next)
	}
	return fmt.Sprintf("(%s OR COALESCE(%s, FALSE))", current, next)
}

func mailboxProviderUpsert(ctx context.Context, tx pgx.Tx, provider string, mailbox *pb.EmailMailbox, now int64) error {
	if definition := providerByKey(provider); definition != nil && definition.upsert != nil {
		return definition.upsert(ctx, tx, mailbox, now)
	}
	return nil
}

func mailboxProviderAuthFilter(provider string, authStatus string, args *[]any) string {
	definition := providerByKey(provider)
	if definition != nil {
		if definition.authFilter == nil {
			return "FALSE"
		}
		return definition.authFilter(authStatus, args)
	}
	parts := []string{}
	for _, definition := range mailboxProviders() {
		if definition.authFilter != nil {
			parts = append(parts, definition.authFilter(authStatus, args))
		}
	}
	if len(parts) == 0 {
		return "FALSE"
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

func mailboxProviderValidatePoll(row *mailboxRow) error {
	if row == nil {
		return fmt.Errorf("mailbox is required")
	}
	definition := providerByKey(row.Provider)
	if definition == nil || definition.validatePoll == nil {
		return fmt.Errorf("mailbox provider cannot poll inbox: %s", row.Provider)
	}
	return definition.validatePoll(row)
}

func mailboxProviderUpdateAuth(ctx context.Context, tx pgx.Tx, provider string, email string, authStatus string, lastError string, now int64) error {
	if definition := providerByKey(provider); definition != nil && definition.updateAuth != nil {
		return definition.updateAuth(ctx, tx, email, authStatus, lastError, now)
	}
	return fmt.Errorf("mailbox provider has no auth state: %s", provider)
}

func mailboxProviderUpdateTokens(ctx context.Context, pool *pgxpool.Pool, provider string, email string, refreshToken string, accessToken string) error {
	if definition := providerByKey(provider); definition != nil && definition.updateTokens != nil {
		return definition.updateTokens(ctx, pool, email, refreshToken, accessToken)
	}
	return fmt.Errorf("mailbox provider has no token storage: %s", provider)
}

func mailboxProviderPruneInbound(ctx context.Context, tx pgx.Tx, provider string, retention mailboxInboxRetention) error {
	if definition := providerByKey(provider); definition != nil && definition.pruneInbound != nil {
		return definition.pruneInbound(ctx, tx, retention)
	}
	return nil
}

func listMailboxProviderVirtualMailboxes(ctx context.Context, pool *pgxpool.Pool, authStatus string, provider string, limit int) ([]*pb.EmailMailbox, error) {
	out := []*pb.EmailMailbox{}
	for _, definition := range mailboxProviders() {
		if provider != "" && provider != definition.key {
			continue
		}
		if definition.virtualMailboxes == nil {
			continue
		}
		if definition.includeVirtual != nil && !definition.includeVirtual(authStatus) {
			continue
		}
		items, err := definition.virtualMailboxes(ctx, pool, limit)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

func prepareMailboxProjection(mailbox *pb.EmailMailbox) {
	if mailbox == nil {
		return
	}
	if definition := providerByKey(mailbox.GetProvider()); definition != nil && definition.prepareProjection != nil {
		definition.prepareProjection(mailbox)
	}
}
