package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"mailboxapi/pb"
)

type mailboxRow struct {
	ID           string
	Email        string
	Provider     string
	Password     string
	RefreshToken string
	AccessToken  string
	AuthStatus   string
	LastError    string
	CreatedAt    int64
	UpdatedAt    int64
}

type inboxMessageRow struct {
	ID             string
	MailboxEmail   string
	Subject        string
	FromAddress    string
	BodyPreview    string
	ReceivedAtUnix int64
	RecipientsJSON string
	Provider       string
	SourceEmail    string
	BodyText       string
	HTMLBody       string
	RawSize        int64
}

type inboxPersistMessage struct {
	key            string
	id             string
	mailboxEmail   string
	subject        string
	fromAddress    string
	bodyPreview    string
	receivedAtUnix int64
	recipients     []string
	provider       string
	sourceEmail    string
	bodyText       string
	htmlBody       string
	rawSize        int64
}

type inboxMessageKey struct {
	provider     string
	mailboxEmail string
	messageKey   string
}

func (row inboxMessageRow) toProto() (*pb.EmailInboxMessage, error) {
	return row.toProtoForProfile("")
}

func (row inboxMessageRow) toProtoForProfile(profile string) (*pb.EmailInboxMessage, error) {
	recipients := []string{}
	if strings.TrimSpace(row.RecipientsJSON) != "" {
		if err := json.Unmarshal([]byte(row.RecipientsJSON), &recipients); err != nil {
			return nil, err
		}
	}
	return emailMessageWithSignals(&pb.EmailInboxMessage{
		Id:                 row.ID,
		MailboxEmail:       normalizeEmail(row.MailboxEmail),
		Subject:            row.Subject,
		FromAddress:        row.FromAddress,
		BodyPreview:        row.BodyPreview,
		ReceivedAtUnix:     row.ReceivedAtUnix,
		Recipients:         uniqueStrings(recipients),
		Provider:           normalizeEmailProvider(row.Provider),
		SourceMailboxEmail: normalizeEmail(row.SourceEmail),
		BodyText:           row.BodyText,
		HtmlBody:           row.HTMLBody,
		RawSize:            row.RawSize,
	}, profile), nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

type MailboxStore struct {
	pool *pgxpool.Pool
}

func NewMailboxStore(ctx context.Context, dsn string) (*MailboxStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("PG_DSN is required")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	store := &MailboxStore{pool: pool}
	if err := store.ensureSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

func (s *MailboxStore) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

func (s *MailboxStore) ensureSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS mailboxes (
			id TEXT PRIMARY KEY,
			email TEXT NOT NULL UNIQUE,
			provider TEXT NOT NULL DEFAULT '` + defaultMailboxProvider() + `',
			created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
			updated_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
			last_inbox_received_at_ns BIGINT NOT NULL DEFAULT 0
		)`,
		`ALTER TABLE mailboxes ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT '` + defaultMailboxProvider() + `'`,
		`ALTER TABLE mailboxes ADD COLUMN IF NOT EXISTS created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT`,
		`ALTER TABLE mailboxes ADD COLUMN IF NOT EXISTS updated_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT`,
		`ALTER TABLE mailboxes ADD COLUMN IF NOT EXISTS last_inbox_received_at_ns BIGINT NOT NULL DEFAULT 0`,
		`CREATE TABLE IF NOT EXISTS mailbox_inbox_seen (
			provider TEXT NOT NULL DEFAULT '` + defaultMailboxProvider() + `',
			mailbox_email TEXT NOT NULL,
			message_key TEXT NOT NULL,
			seen_at BIGINT NOT NULL,
			PRIMARY KEY (provider, mailbox_email, message_key)
		)`,
		`CREATE TABLE IF NOT EXISTS mailbox_inbox_messages (
			provider TEXT NOT NULL DEFAULT '` + defaultMailboxProvider() + `',
			mailbox_email TEXT NOT NULL,
			message_key TEXT NOT NULL,
			message_id TEXT NOT NULL DEFAULT '',
			subject TEXT NOT NULL DEFAULT '',
			from_address TEXT NOT NULL DEFAULT '',
			body_preview TEXT NOT NULL DEFAULT '',
			body_text TEXT NOT NULL DEFAULT '',
			html_body TEXT NOT NULL DEFAULT '',
			raw_size BIGINT NOT NULL DEFAULT 0,
			received_at BIGINT NOT NULL DEFAULT 0,
			recipients_json TEXT NOT NULL DEFAULT '[]',
			source_mailbox_email TEXT NOT NULL DEFAULT '',
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL,
			PRIMARY KEY (provider, mailbox_email, message_key)
		)`,
		`ALTER TABLE mailbox_inbox_seen ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT '` + defaultMailboxProvider() + `'`,
		`ALTER TABLE mailbox_inbox_messages ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT '` + defaultMailboxProvider() + `'`,
		`ALTER TABLE mailbox_inbox_messages ADD COLUMN IF NOT EXISTS body_text TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mailbox_inbox_messages ADD COLUMN IF NOT EXISTS html_body TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mailbox_inbox_messages ADD COLUMN IF NOT EXISTS raw_size BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE mailbox_inbox_messages ADD COLUMN IF NOT EXISTS source_mailbox_email TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mailbox_inbox_messages DROP COLUMN IF EXISTS otp`,
		`ALTER TABLE mailbox_inbox_messages DROP COLUMN IF EXISTS event_type`,
		`DROP TABLE IF EXISTS mailbox_latest_otps`,
		`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1
				FROM pg_constraint
				WHERE conname = 'mailbox_inbox_seen_pkey'
				  AND conrelid = 'mailbox_inbox_seen'::regclass
			) THEN
				ALTER TABLE mailbox_inbox_seen DROP CONSTRAINT mailbox_inbox_seen_pkey;
			END IF;
		END $$`,
		`ALTER TABLE mailbox_inbox_seen ADD PRIMARY KEY (provider, mailbox_email, message_key)`,
		`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1
				FROM pg_constraint
				WHERE conname = 'mailbox_inbox_messages_pkey'
				  AND conrelid = 'mailbox_inbox_messages'::regclass
			) THEN
				ALTER TABLE mailbox_inbox_messages DROP CONSTRAINT mailbox_inbox_messages_pkey;
			END IF;
		END $$`,
		`ALTER TABLE mailbox_inbox_messages ADD PRIMARY KEY (provider, mailbox_email, message_key)`,
		`DROP INDEX IF EXISTS idx_mailboxes_assigned_account`,
		`ALTER TABLE mailboxes DROP COLUMN IF EXISTS assigned_account_id`,
		`DROP INDEX IF EXISTS idx_mailboxes_status`,
		`DROP INDEX IF EXISTS idx_mailboxes_primary`,
		`DROP INDEX IF EXISTS idx_mailboxes_auth_status`,
		`ALTER TABLE mailboxes DROP COLUMN IF EXISTS status`,
		`ALTER TABLE mailboxes DROP COLUMN IF EXISTS is_primary`,
		`ALTER TABLE mailboxes DROP COLUMN IF EXISTS primary_email`,
		`ALTER TABLE mailboxes DROP COLUMN IF EXISTS password`,
		`ALTER TABLE mailboxes DROP COLUMN IF EXISTS refresh_token`,
		`ALTER TABLE mailboxes DROP COLUMN IF EXISTS access_token`,
		`ALTER TABLE mailboxes DROP COLUMN IF EXISTS auth_status`,
		`ALTER TABLE mailboxes DROP COLUMN IF EXISTS last_error`,
		`CREATE INDEX IF NOT EXISTS idx_mailboxes_provider ON mailboxes(provider)`,
		`CREATE INDEX IF NOT EXISTS idx_mailbox_inbox_seen_at ON mailbox_inbox_seen(seen_at)`,
		`CREATE INDEX IF NOT EXISTS idx_mailbox_inbox_messages_received_at ON mailbox_inbox_messages(mailbox_email, received_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_mailbox_inbox_messages_provider_received_at ON mailbox_inbox_messages(provider, mailbox_email, received_at DESC)`,
	}
	statements = insertSchemaStatementsAfter(statements, "ALTER TABLE mailboxes ADD COLUMN IF NOT EXISTS last_inbox_received_at_ns", mailboxProviderSchemaStatements())
	statements = insertSchemaStatementsAfter(statements, "ALTER TABLE mailboxes DROP COLUMN IF EXISTS assigned_account_id", mailboxProviderLegacyStatements())
	for _, statement := range statements {
		if _, err := s.pool.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func insertSchemaStatementsAfter(statements []string, prefix string, additions []string) []string {
	if len(additions) == 0 {
		return statements
	}
	for i, statement := range statements {
		if strings.HasPrefix(strings.TrimSpace(statement), prefix) {
			out := make([]string, 0, len(statements)+len(additions))
			out = append(out, statements[:i+1]...)
			out = append(out, additions...)
			out = append(out, statements[i+1:]...)
			return out
		}
	}
	return append(statements, additions...)
}

func (s *MailboxStore) UpsertMailbox(ctx context.Context, mailbox *pb.EmailMailbox) (*pb.EmailMailbox, error) {
	if mailbox == nil {
		return nil, errors.New("mailbox is required")
	}
	email := normalizeEmail(mailbox.GetEmailAddress())
	if email == "" {
		return nil, errors.New("email_address is required")
	}
	requestedProvider := normalizeEmailProvider(mailbox.GetProvider())
	insertProvider := requestedProvider
	if insertProvider == "" {
		insertProvider = defaultMailboxProvider()
	}
	rowID, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var persistedProvider string
	if err := tx.QueryRow(ctx, `
		INSERT INTO mailboxes (id, email, provider, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$4)
		ON CONFLICT (email) DO UPDATE SET
			provider = CASE WHEN $5 <> '' THEN EXCLUDED.provider ELSE mailboxes.provider END,
			updated_at = EXCLUDED.updated_at
		RETURNING provider
	`, rowID, email, insertProvider, now, requestedProvider).Scan(&persistedProvider); err != nil {
		return nil, err
	}
	if err := mailboxProviderUpsert(ctx, tx, persistedProvider, mailbox, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.FindMailbox(ctx, email)
}

func (s *MailboxStore) ListMailboxes(ctx context.Context, authStatus string, provider string, limit int32) ([]*pb.EmailMailbox, error) {
	n := int(limit)
	if n <= 0 {
		n = 100
	}
	if n > 500 {
		n = 500
	}
	stored, err := s.listStoredMailboxes(ctx, authStatus, provider, n)
	if err != nil {
		return nil, err
	}
	out := stored
	virtual, err := listMailboxProviderVirtualMailboxes(ctx, s.pool, strings.TrimSpace(authStatus), normalizeEmailProvider(provider), n)
	if err != nil {
		return nil, err
	}
	if len(virtual) > 0 {
		out = append(out, virtual...)
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].GetUpdatedAt() > out[j].GetUpdatedAt()
		})
		if len(out) > n {
			out = out[:n]
		}
	}
	return out, nil
}

func (s *MailboxStore) listStoredMailboxes(ctx context.Context, authStatus string, provider string, limit int) ([]*pb.EmailMailbox, error) {
	args := []any{}
	query := mailboxSelectSQL() + ` WHERE 1=1`
	if trimmed := strings.TrimSpace(authStatus); trimmed != "" {
		query += " AND " + mailboxProviderAuthFilter(normalizeEmailProvider(provider), trimmed, &args)
	}
	if trimmed := normalizeEmailProvider(provider); trimmed != "" {
		args = append(args, trimmed)
		query += fmt.Sprintf(" AND m.provider = $%d", len(args))
	}
	args = append(args, limit)
	query += fmt.Sprintf(" ORDER BY m.updated_at DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*pb.EmailMailbox{}
	for rows.Next() {
		row, err := scanMailbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row.toProto())
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *MailboxStore) ListOAuthMailboxes(ctx context.Context, limit int32) ([]*pb.EmailMailbox, error) {
	n := int(limit)
	if n <= 0 {
		n = 100
	}
	if n > 500 {
		n = 500
	}
	args := []any{}
	query := mailboxSelectSQL() + " WHERE " + mailboxProviderAuthFilter("", authStatusAuthorized, &args)
	args = append(args, n)
	query += fmt.Sprintf(" ORDER BY m.updated_at DESC LIMIT $%d", len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*pb.EmailMailbox{}
	for rows.Next() {
		row, err := scanMailbox(rows)
		if err != nil {
			return nil, err
		}
		if mailboxProviderValidatePoll(row) != nil {
			continue
		}
		out = append(out, row.toProto())
	}
	return out, rows.Err()
}

func (s *MailboxStore) InboxWatermark(ctx context.Context, email string) (int64, error) {
	email = normalizeEmail(email)
	if email == "" {
		return 0, errors.New("email_address is required")
	}
	var watermark int64
	err := s.pool.QueryRow(ctx, "SELECT last_inbox_received_at_ns FROM mailboxes WHERE email = $1", email).Scan(&watermark)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("mailbox not found: %s", redactEmail(email))
	}
	return watermark, err
}

func (s *MailboxStore) HasInboxMessages(ctx context.Context, email string) (bool, error) {
	email = normalizeEmail(email)
	if email == "" {
		return false, errors.New("email_address is required")
	}
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM mailbox_inbox_messages WHERE mailbox_email = $1
		)
	`, email).Scan(&exists)
	return exists, err
}

func (s *MailboxStore) RecordInboundEmail(ctx context.Context, event *pb.InboundEmailWebhook) ([]*pb.EmailInboxMessage, error) {
	if event == nil {
		return nil, errors.New("email event is required")
	}
	provider := normalizeEmailProvider(event.GetProvider())
	if provider == "" {
		return nil, errors.New("email event provider is required")
	}
	recipients := uniqueStrings(event.GetRecipients())
	if len(recipients) == 0 {
		return nil, errors.New("email event recipients are required")
	}
	receivedAt := event.GetReceivedAtUnix()
	if receivedAt <= 0 {
		receivedAt = time.Now().Unix()
	}
	body := strings.TrimSpace(event.GetTextBody())
	if body == "" {
		body = compactMessageText(event.GetHtmlBody(), 5000)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := time.Now().Unix()
	touchedMailboxes := map[string]struct{}{}
	touchedDomains := map[string]struct{}{}
	unseen := make([]*pb.EmailInboxMessage, 0, len(recipients))
	for _, recipient := range recipients {
		touchedMailboxes[recipient] = struct{}{}
		if domain := domainForEmail(recipient); domain != "" {
			touchedDomains[domain] = struct{}{}
		}
		key := stableMessageKey(provider, recipient, firstNonEmpty(event.GetEventId(), event.GetMessageId(), event.GetSubject()))
		messageID := firstNonEmpty(event.GetMessageId(), event.GetEventId(), key)
		persisted := &pb.EmailInboxMessage{
			Id:                 messageID,
			MailboxEmail:       recipient,
			Subject:            strings.TrimSpace(event.GetSubject()),
			FromAddress:        normalizeEmail(event.GetFromAddress()),
			BodyPreview:        compactMessageText(body, 500),
			ReceivedAtUnix:     receivedAt,
			Recipients:         recipients,
			Provider:           provider,
			SourceMailboxEmail: recipient,
			BodyText:           body,
			HtmlBody:           strings.TrimSpace(event.GetHtmlBody()),
			RawSize:            event.GetRawSize(),
		}
		if err := insertInboxMessage(ctx, tx, inboxPersistMessage{
			key:            key,
			id:             messageID,
			mailboxEmail:   recipient,
			subject:        strings.TrimSpace(event.GetSubject()),
			fromAddress:    normalizeEmail(event.GetFromAddress()),
			bodyPreview:    compactMessageText(body, 500),
			receivedAtUnix: receivedAt,
			recipients:     recipients,
			provider:       provider,
			sourceEmail:    recipient,
			bodyText:       body,
			htmlBody:       strings.TrimSpace(event.GetHtmlBody()),
			rawSize:        event.GetRawSize(),
		}, now); err != nil {
			return nil, err
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO mailbox_inbox_seen (provider, mailbox_email, message_key, seen_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (provider, mailbox_email, message_key) DO NOTHING
		`, provider, recipient, key, now)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() > 0 {
			unseen = append(unseen, emailMessageWithSignals(persisted, ""))
		}
	}
	if err := mailboxProviderPruneInbound(ctx, tx, provider, mailboxInboxRetention{
		touchedMailboxes: touchedMailboxes,
		touchedDomains:   touchedDomains,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return unseen, nil
}

func insertInboxMessage(ctx context.Context, tx pgx.Tx, msg inboxPersistMessage, now int64) error {
	recipientsJSON, err := json.Marshal(uniqueStrings(msg.recipients))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO mailbox_inbox_messages (
			provider, mailbox_email, message_key, message_id, subject, from_address,
			body_preview, body_text, html_body, raw_size, received_at, recipients_json,
			source_mailbox_email,
			created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14)
		ON CONFLICT (provider, mailbox_email, message_key) DO UPDATE SET
			message_id = EXCLUDED.message_id,
			subject = EXCLUDED.subject,
			from_address = EXCLUDED.from_address,
			body_preview = EXCLUDED.body_preview,
			body_text = EXCLUDED.body_text,
			html_body = EXCLUDED.html_body,
			raw_size = EXCLUDED.raw_size,
			received_at = EXCLUDED.received_at,
			recipients_json = EXCLUDED.recipients_json,
			source_mailbox_email = EXCLUDED.source_mailbox_email,
			updated_at = EXCLUDED.updated_at
	`, normalizeEmailProvider(msg.provider), normalizeEmail(msg.mailboxEmail), msg.key, strings.TrimSpace(msg.id),
		strings.TrimSpace(msg.subject), normalizeEmail(msg.fromAddress), strings.TrimSpace(msg.bodyPreview),
		strings.TrimSpace(msg.bodyText), strings.TrimSpace(msg.htmlBody), msg.rawSize, msg.receivedAtUnix,
		string(recipientsJSON), normalizeEmail(msg.sourceEmail), now)
	return err
}

func pruneMailboxMessages(ctx context.Context, tx pgx.Tx, provider string, mailboxEmail string, limit int) error {
	provider = normalizeEmailProvider(provider)
	mailboxEmail = normalizeEmail(mailboxEmail)
	if provider == "" || mailboxEmail == "" || limit <= 0 {
		return nil
	}
	keys, err := expiredInboxKeys(ctx, tx, `
		SELECT provider, mailbox_email, message_key
		FROM mailbox_inbox_messages
		WHERE provider = $1 AND mailbox_email = $2
		ORDER BY received_at DESC, updated_at DESC, message_key DESC
		OFFSET $3
	`, provider, mailboxEmail, limit)
	if err != nil {
		return err
	}
	return deleteInboxKeys(ctx, tx, keys)
}

func pruneDomainMessages(ctx context.Context, tx pgx.Tx, provider string, domain string, limit int) error {
	provider = normalizeEmailProvider(provider)
	domain = strings.Trim(strings.ToLower(strings.TrimSpace(domain)), ".")
	if provider == "" || domain == "" || limit <= 0 {
		return nil
	}
	keys, err := expiredInboxKeys(ctx, tx, `
		SELECT provider, mailbox_email, message_key
		FROM mailbox_inbox_messages
		WHERE provider = $1 AND split_part(mailbox_email, '@', 2) = $2
		ORDER BY received_at DESC, updated_at DESC, message_key DESC
		OFFSET $3
	`, provider, domain, limit)
	if err != nil {
		return err
	}
	return deleteInboxKeys(ctx, tx, keys)
}

func expiredInboxKeys(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]inboxMessageKey, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	keys := []inboxMessageKey{}
	for rows.Next() {
		var key inboxMessageKey
		if err := rows.Scan(&key.provider, &key.mailboxEmail, &key.messageKey); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func deleteInboxKeys(ctx context.Context, tx pgx.Tx, keys []inboxMessageKey) error {
	if len(keys) == 0 {
		return nil
	}
	providers := make([]string, 0, len(keys))
	mailboxEmails := make([]string, 0, len(keys))
	messageKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		providers = append(providers, key.provider)
		mailboxEmails = append(mailboxEmails, key.mailboxEmail)
		messageKeys = append(messageKeys, key.messageKey)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM mailbox_inbox_messages msg
		USING unnest($1::text[], $2::text[], $3::text[]) expired(provider, mailbox_email, message_key)
		WHERE msg.provider = expired.provider
		  AND msg.mailbox_email = expired.mailbox_email
		  AND msg.message_key = expired.message_key
	`, providers, mailboxEmails, messageKeys); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		DELETE FROM mailbox_inbox_seen seen
		USING unnest($1::text[], $2::text[], $3::text[]) expired(provider, mailbox_email, message_key)
		WHERE seen.provider = expired.provider
		  AND seen.mailbox_email = expired.mailbox_email
		  AND seen.message_key = expired.message_key
	`, providers, mailboxEmails, messageKeys)
	return err
}

func messageMailboxEmails(accountEmail string, recipients []string) []string {
	items := []string{normalizeEmail(accountEmail)}
	for _, recipient := range recipients {
		if email := normalizeEmail(recipient); email != "" {
			items = append(items, email)
		}
	}
	return uniqueStrings(items)
}

func (s *MailboxStore) ListInboxMessages(ctx context.Context, email string, limit int32) ([]*pb.EmailInboxMessage, error) {
	return s.ListInboxMessagesSince(ctx, email, limit, 0)
}

func (s *MailboxStore) ListInboxMessagesSince(ctx context.Context, email string, limit int32, receivedAfterUnix int64) ([]*pb.EmailInboxMessage, error) {
	email = normalizeEmail(email)
	if email == "" {
		return nil, errors.New("email_address is required")
	}
	n := messageLimitValue(limit, defaultMessageLimit)
	args := []any{email}
	query := `
		SELECT message_id, mailbox_email, subject, from_address, body_preview,
			received_at, recipients_json, provider, source_mailbox_email, body_text,
			html_body, raw_size
		FROM mailbox_inbox_messages
		WHERE mailbox_email = $1
	`
	if receivedAfterUnix > 0 {
		args = append(args, receivedAfterUnix)
		query += fmt.Sprintf(" AND received_at > $%d", len(args))
	}
	args = append(args, n)
	query += fmt.Sprintf(`
		ORDER BY received_at DESC, updated_at DESC, message_key DESC
		LIMIT $%d
	`, len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*pb.EmailInboxMessage{}
	for rows.Next() {
		var row inboxMessageRow
		if err := rows.Scan(
			&row.ID,
			&row.MailboxEmail,
			&row.Subject,
			&row.FromAddress,
			&row.BodyPreview,
			&row.ReceivedAtUnix,
			&row.RecipientsJSON,
			&row.Provider,
			&row.SourceEmail,
			&row.BodyText,
			&row.HTMLBody,
			&row.RawSize,
		); err != nil {
			return nil, err
		}
		recipients := []string{}
		if err := json.Unmarshal([]byte(row.RecipientsJSON), &recipients); err != nil {
			recipients = []string{}
		}
		out = append(out, emailMessageWithSignals(&pb.EmailInboxMessage{
			Id:                 row.ID,
			MailboxEmail:       normalizeEmail(row.MailboxEmail),
			Subject:            row.Subject,
			FromAddress:        row.FromAddress,
			BodyPreview:        row.BodyPreview,
			ReceivedAtUnix:     row.ReceivedAtUnix,
			Recipients:         uniqueStrings(recipients),
			Provider:           normalizeEmailProvider(row.Provider),
			SourceMailboxEmail: normalizeEmail(row.SourceEmail),
			BodyText:           row.BodyText,
			HtmlBody:           row.HTMLBody,
			RawSize:            row.RawSize,
		}, ""))
	}
	return out, rows.Err()
}

func (s *MailboxStore) LatestMessage(ctx context.Context, email string, subjectKeyword string, issuedAfterUnix int64) (*pb.EmailInboxMessage, bool, error) {
	return s.LatestMessageWithSignal(ctx, email, subjectKeyword, issuedAfterUnix, "", pb.EmailSignalKind_EMAIL_SIGNAL_KIND_UNSPECIFIED)
}

func (s *MailboxStore) LatestMessageWithSignal(ctx context.Context, email string, subjectKeyword string, issuedAfterUnix int64, parserProfile string, signalKind pb.EmailSignalKind) (*pb.EmailInboxMessage, bool, error) {
	for _, candidate := range uniqueStrings([]string{email, canonicalEmail(email)}) {
		msg, ok, err := s.latestMessageForMailbox(ctx, candidate, subjectKeyword, issuedAfterUnix, parserProfile, signalKind)
		if err != nil || ok {
			return msg, ok, err
		}
	}
	return nil, false, nil
}

func (s *MailboxStore) latestMessageForMailbox(ctx context.Context, email string, subjectKeyword string, issuedAfterUnix int64, parserProfile string, signalKind pb.EmailSignalKind) (*pb.EmailInboxMessage, bool, error) {
	email = normalizeEmail(email)
	if email == "" {
		return nil, false, nil
	}
	args := []any{email}
	query := `
		SELECT message_id, mailbox_email, subject, from_address, body_preview,
			received_at, recipients_json, provider, source_mailbox_email, body_text,
			html_body, raw_size
		FROM mailbox_inbox_messages
		WHERE mailbox_email = $1
	`
	if issuedAfterUnix > 0 {
		args = append(args, issuedAfterUnix)
		query += fmt.Sprintf(" AND received_at >= $%d", len(args))
	}
	if keyword := strings.TrimSpace(subjectKeyword); keyword != "" {
		args = append(args, "%"+keyword+"%")
		query += fmt.Sprintf(" AND (subject ILIKE $%d OR body_preview ILIKE $%d OR body_text ILIKE $%d)", len(args), len(args), len(args))
	}
	args = append(args, 50)
	query += fmt.Sprintf(" ORDER BY received_at DESC, updated_at DESC, message_key DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	for rows.Next() {
		var row inboxMessageRow
		if err := rows.Scan(
			&row.ID,
			&row.MailboxEmail,
			&row.Subject,
			&row.FromAddress,
			&row.BodyPreview,
			&row.ReceivedAtUnix,
			&row.RecipientsJSON,
			&row.Provider,
			&row.SourceEmail,
			&row.BodyText,
			&row.HTMLBody,
			&row.RawSize,
		); err != nil {
			return nil, false, err
		}
		msg, err := row.toProtoForProfile(parserProfile)
		if err != nil {
			return nil, false, err
		}
		if messageHasSignal(msg, signalKind) {
			return msg, true, nil
		}
	}
	return nil, false, rows.Err()
}

func (s *MailboxStore) MarkEmailAuthStatus(ctx context.Context, email string, authStatus string, lastError string) (*pb.EmailMailbox, error) {
	email = normalizeEmail(email)
	authStatus = strings.TrimSpace(authStatus)
	if email == "" {
		return nil, errors.New("email_address is required")
	}
	if authStatus == "" {
		return nil, errors.New("auth_status is required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := scanMailbox(tx.QueryRow(ctx, mailboxSelectSQL()+" WHERE m.email = $1 FOR UPDATE", email))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("mailbox not found: %s", redactEmail(email))
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if err := mailboxProviderUpdateAuth(ctx, tx, row.Provider, email, authStatus, lastError, now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "UPDATE mailboxes SET updated_at = $1 WHERE email = $2", now, email); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.FindMailbox(ctx, email)
}

func (s *MailboxStore) DeleteMailbox(ctx context.Context, email string) (bool, error) {
	email = normalizeEmail(email)
	if email == "" {
		return false, errors.New("email_address is required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := scanMailbox(tx.QueryRow(ctx, mailboxSelectSQL()+" WHERE m.email = $1 FOR UPDATE", email))
	if errors.Is(err, pgx.ErrNoRows) {
		deleted, deleteErr := deleteMailboxInbox(ctx, tx, []string{email})
		if deleteErr != nil {
			return false, deleteErr
		}
		if err := tx.Commit(ctx); err != nil {
			return false, err
		}
		return deleted, nil
	}
	if err != nil {
		return false, err
	}

	deleteEmails := []string{row.Email}
	if _, err := deleteMailboxInbox(ctx, tx, deleteEmails); err != nil {
		return false, err
	}
	args, inClause := sqlInArgs(deleteEmails)
	tag, err := tx.Exec(ctx, "DELETE FROM mailboxes WHERE email IN ("+inClause+")", args...)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func deleteMailboxInbox(ctx context.Context, tx pgx.Tx, emails []string) (bool, error) {
	args, inClause := sqlInArgs(emails)
	messageTag, err := tx.Exec(ctx, "DELETE FROM mailbox_inbox_messages WHERE mailbox_email IN ("+inClause+")", args...)
	if err != nil {
		return false, err
	}
	seenTag, err := tx.Exec(ctx, "DELETE FROM mailbox_inbox_seen WHERE mailbox_email IN ("+inClause+")", args...)
	if err != nil {
		return false, err
	}
	return messageTag.RowsAffected() > 0 || seenTag.RowsAffected() > 0, nil
}

func sqlInArgs(values []string) ([]any, string) {
	args := make([]any, 0, len(values))
	placeholders := make([]string, 0, len(values))
	for _, item := range values {
		args = append(args, item)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	return args, strings.Join(placeholders, ",")
}

func (s *MailboxStore) FindMailbox(ctx context.Context, email string) (*pb.EmailMailbox, error) {
	row, err := scanMailbox(s.pool.QueryRow(ctx, mailboxSelectSQL()+" WHERE m.email = $1", normalizeEmail(email)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("mailbox not found: %s", redactEmail(email))
	}
	if err != nil {
		return nil, err
	}
	mailbox := row.toProto()
	return mailbox, nil
}

func (s *MailboxStore) PollMailboxForEmail(ctx context.Context, email string) (*pb.EmailMailbox, error) {
	email = normalizeEmail(email)
	row, err := scanMailbox(s.pool.QueryRow(ctx, mailboxSelectSQL()+" WHERE m.email = $1", email))
	if errors.Is(err, pgx.ErrNoRows) {
		canonical := canonicalEmail(email)
		if canonical != "" && canonical != email {
			row, err = scanMailbox(s.pool.QueryRow(ctx, mailboxSelectSQL()+" WHERE m.email = $1", canonical))
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("mailbox not found: %s", redactEmail(email))
	}
	if err != nil {
		return nil, err
	}

	if err := mailboxProviderValidatePoll(row); err != nil {
		return nil, err
	}
	return row.toProto(), nil
}

func (s *MailboxStore) UpdateMailboxTokens(ctx context.Context, email string, refreshToken string, accessToken string) error {
	email = normalizeEmail(email)
	row, err := scanMailbox(s.pool.QueryRow(ctx, mailboxSelectSQL()+" WHERE m.email = $1", email))
	if err != nil {
		return err
	}
	if err := mailboxProviderUpdateTokens(ctx, s.pool, row.Provider, email, refreshToken, accessToken); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, "UPDATE mailboxes SET updated_at = $1 WHERE email = $2", time.Now().Unix(), email)
	return err
}

func (s *MailboxStore) MarkAuthFailed(ctx context.Context, email string, err error) {
	if _, updateErr := s.MarkEmailAuthStatus(ctx, email, authStatusAuthFailed, err.Error()); updateErr != nil {
		logWarning("failed to mark mailbox auth failed for %s: %v", redactEmail(email), updateErr)
	}
}

func scanMailbox(scanner rowScanner) (*mailboxRow, error) {
	var row mailboxRow
	err := scanner.Scan(
		&row.ID,
		&row.Email,
		&row.Provider,
		&row.Password,
		&row.RefreshToken,
		&row.AccessToken,
		&row.AuthStatus,
		&row.LastError,
		&row.CreatedAt,
		&row.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (m *mailboxRow) toProto() *pb.EmailMailbox {
	if m == nil {
		return nil
	}
	mailbox := &pb.EmailMailbox{
		EmailAddress: m.Email,
		Provider:     normalizeEmailProvider(m.Provider),
		Password:     m.Password,
		RefreshToken: m.RefreshToken,
		AccessToken:  m.AccessToken,
		AuthStatus:   m.AuthStatus,
		LastError:    m.LastError,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
		Domain:       domainForEmail(m.Email),
	}
	prepareMailboxProjection(mailbox)
	return mailbox
}

func normalizeEmailProvider(provider string) string {
	return normalizeMailboxProviderInput(provider)
}

func domainForEmail(email string) string {
	_, domain, ok := strings.Cut(normalizeEmail(email), "@")
	if !ok {
		return ""
	}
	return domain
}

func stableMessageKey(provider string, mailboxEmail string, value string) string {
	raw := strings.Join([]string{
		normalizeEmailProvider(provider),
		normalizeEmail(mailboxEmail),
		strings.TrimSpace(value),
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
