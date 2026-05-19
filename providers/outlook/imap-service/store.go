package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"outlookimapservice/pb"
)

const selectMailbox = `
	SELECT id, email, provider, password, refresh_token, access_token, auth_status,
		last_error, is_primary, primary_email, created_at, updated_at
	FROM mailboxes
`

type mailboxRow struct {
	ID           string
	Email        string
	Provider     string
	Password     string
	RefreshToken string
	AccessToken  string
	AuthStatus   string
	LastError    string
	IsPrimary    bool
	PrimaryEmail string
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

func (row inboxMessageRow) toProto() (*pb.EmailInboxMessage, error) {
	recipients := []string{}
	if strings.TrimSpace(row.RecipientsJSON) != "" {
		if err := json.Unmarshal([]byte(row.RecipientsJSON), &recipients); err != nil {
			return nil, err
		}
	}
	return &pb.EmailInboxMessage{
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
	}, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

type MailboxStore struct {
	pool             *pgxpool.Pool
	aliasTokenLength int
}

func NewMailboxStore(ctx context.Context, dsn string, aliasTokenLength int) (*MailboxStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("PG_DSN is required")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	store := &MailboxStore{
		pool:             pool,
		aliasTokenLength: aliasTokenLength,
	}
	if store.aliasTokenLength <= 0 {
		store.aliasTokenLength = defaultAliasTokenLength
	}
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
			provider TEXT NOT NULL DEFAULT 'outlook',
			password TEXT NOT NULL DEFAULT '',
			refresh_token TEXT NOT NULL DEFAULT '',
			access_token TEXT NOT NULL DEFAULT '',
			auth_status TEXT NOT NULL DEFAULT 'OAUTH_PENDING',
			last_error TEXT NOT NULL DEFAULT '',
			is_primary BOOLEAN NOT NULL DEFAULT false,
			primary_email TEXT NOT NULL DEFAULT '',
			created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
			updated_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
		)`,
		`ALTER TABLE mailboxes ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT 'outlook'`,
		`ALTER TABLE mailboxes ADD COLUMN IF NOT EXISTS password TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mailboxes ADD COLUMN IF NOT EXISTS refresh_token TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mailboxes ADD COLUMN IF NOT EXISTS access_token TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mailboxes ADD COLUMN IF NOT EXISTS auth_status TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mailboxes ALTER COLUMN auth_status SET DEFAULT 'OAUTH_PENDING'`,
		`ALTER TABLE mailboxes ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mailboxes ADD COLUMN IF NOT EXISTS is_primary BOOLEAN NOT NULL DEFAULT false`,
		`ALTER TABLE mailboxes ADD COLUMN IF NOT EXISTS primary_email TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mailboxes ADD COLUMN IF NOT EXISTS created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT`,
		`ALTER TABLE mailboxes ADD COLUMN IF NOT EXISTS updated_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT`,
		`ALTER TABLE mailboxes ADD COLUMN IF NOT EXISTS last_inbox_received_at_ns BIGINT NOT NULL DEFAULT 0`,
		`CREATE TABLE IF NOT EXISTS mailbox_inbox_seen (
			provider TEXT NOT NULL DEFAULT 'outlook',
			mailbox_email TEXT NOT NULL,
			message_key TEXT NOT NULL,
			seen_at BIGINT NOT NULL,
			PRIMARY KEY (provider, mailbox_email, message_key)
		)`,
		`CREATE TABLE IF NOT EXISTS mailbox_inbox_messages (
			provider TEXT NOT NULL DEFAULT 'outlook',
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
		`ALTER TABLE mailbox_inbox_seen ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT 'outlook'`,
		`ALTER TABLE mailbox_inbox_messages ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT 'outlook'`,
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
		`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1
				FROM information_schema.columns
				WHERE table_name = 'mailboxes'
				  AND column_name = 'status'
			) THEN
				UPDATE mailboxes
				SET auth_status = CASE
					WHEN status IN ('OAUTH_PENDING', 'AUTH_FAILED', 'NEEDS_MANUAL_VERIFICATION') THEN status
					WHEN refresh_token <> '' THEN 'AUTHORIZED'
					ELSE 'OAUTH_PENDING'
				END
				WHERE auth_status = '';
			END IF;
		END $$`,
		`UPDATE mailboxes
				SET auth_status = CASE
					WHEN refresh_token <> '' THEN 'AUTHORIZED'
					ELSE 'OAUTH_PENDING'
				END
				WHERE auth_status = ''`,
		`UPDATE mailboxes SET auth_status = 'OAUTH_PENDING', last_error = ''
					WHERE auth_status = 'AUTH_FAILED'
					AND last_error = 'registered mailbox has no OAuth refresh token'`,
		`DROP INDEX IF EXISTS idx_mailboxes_status`,
		`ALTER TABLE mailboxes DROP COLUMN IF EXISTS status`,
		`CREATE INDEX IF NOT EXISTS idx_mailboxes_auth_status ON mailboxes(auth_status)`,
		`CREATE INDEX IF NOT EXISTS idx_mailboxes_provider ON mailboxes(provider)`,
		`CREATE INDEX IF NOT EXISTS idx_mailboxes_primary ON mailboxes(primary_email)`,
		`CREATE INDEX IF NOT EXISTS idx_mailbox_inbox_seen_at ON mailbox_inbox_seen(seen_at)`,
		`CREATE INDEX IF NOT EXISTS idx_mailbox_inbox_messages_received_at ON mailbox_inbox_messages(mailbox_email, received_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_mailbox_inbox_messages_provider_received_at ON mailbox_inbox_messages(provider, mailbox_email, received_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := s.pool.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
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
		insertProvider = emailProviderOutlook
	}
	isPrimary := mailbox.GetIsPrimary()
	primaryEmail := normalizeEmail(mailbox.GetPrimaryEmail())
	if primaryEmail == "" {
		if isPrimary {
			primaryEmail = email
		} else {
			primaryEmail = canonicalEmail(email)
		}
	}
	if primaryEmail == email {
		isPrimary = true
	}
	requestedAuthStatus := strings.TrimSpace(mailbox.GetAuthStatus())
	insertAuthStatus := requestedAuthStatus
	if insertAuthStatus == "" {
		insertAuthStatus = authStatusOAuthPending
		if strings.TrimSpace(mailbox.GetRefreshToken()) != "" {
			insertAuthStatus = authStatusAuthorized
		}
	}
	refreshToken := strings.TrimSpace(mailbox.GetRefreshToken())
	accessToken := strings.TrimSpace(mailbox.GetAccessToken())
	lastError := strings.TrimSpace(mailbox.GetLastError())
	rowID, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()

	_, err = s.pool.Exec(ctx, `
		INSERT INTO mailboxes (
			id, email, provider, password, refresh_token, access_token, auth_status,
			last_error, is_primary, primary_email, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (email) DO UPDATE SET
			provider = CASE WHEN $13 <> '' THEN EXCLUDED.provider ELSE mailboxes.provider END,
			password = CASE WHEN EXCLUDED.password <> '' THEN EXCLUDED.password ELSE mailboxes.password END,
			refresh_token = CASE WHEN EXCLUDED.refresh_token <> '' THEN EXCLUDED.refresh_token ELSE mailboxes.refresh_token END,
			access_token = CASE WHEN EXCLUDED.access_token <> '' THEN EXCLUDED.access_token ELSE mailboxes.access_token END,
			auth_status = CASE WHEN $14 <> '' THEN EXCLUDED.auth_status WHEN EXCLUDED.refresh_token <> '' THEN 'AUTHORIZED' ELSE mailboxes.auth_status END,
			last_error = CASE WHEN $14 <> '' OR EXCLUDED.last_error <> '' THEN EXCLUDED.last_error ELSE mailboxes.last_error END,
			is_primary = EXCLUDED.is_primary,
			primary_email = EXCLUDED.primary_email,
			updated_at = EXCLUDED.updated_at
	`, rowID, email, insertProvider, mailbox.GetPassword(), refreshToken, accessToken, insertAuthStatus, lastError, isPrimary, primaryEmail, now, now, requestedProvider, requestedAuthStatus)
	if err != nil {
		return nil, err
	}
	if isPrimary && (refreshToken != "" || accessToken != "" || requestedAuthStatus != "") {
		if _, err := s.pool.Exec(ctx, `
			UPDATE mailboxes
			SET refresh_token = CASE WHEN $1 <> '' THEN $1 ELSE refresh_token END,
				access_token = CASE WHEN $2 <> '' THEN $2 ELSE access_token END,
				auth_status = CASE WHEN $3 <> '' THEN $3 ELSE auth_status END,
				last_error = CASE WHEN $3 <> '' OR $4 <> '' THEN $4 ELSE last_error END,
				updated_at = $5
			WHERE primary_email = $6 AND is_primary = false
		`, refreshToken, accessToken, requestedAuthStatus, lastError, now, email); err != nil {
			return nil, err
		}
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
	args := []any{}
	query := selectMailbox + " WHERE 1=1"
	if trimmed := strings.TrimSpace(authStatus); trimmed != "" {
		args = append(args, trimmed)
		query += fmt.Sprintf(" AND auth_status = $%d", len(args))
	}
	if trimmed := normalizeEmailProvider(provider); trimmed != "" {
		args = append(args, trimmed)
		query += fmt.Sprintf(" AND provider = $%d", len(args))
	}
	args = append(args, n)
	query += fmt.Sprintf(" ORDER BY is_primary DESC, updated_at DESC LIMIT $%d", len(args))

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

func (s *MailboxStore) ListOAuthPrimaryMailboxes(ctx context.Context, limit int32) ([]*pb.EmailMailbox, error) {
	n := int(limit)
	if n <= 0 {
		n = 100
	}
	if n > 500 {
		n = 500
	}
	rows, err := s.pool.Query(ctx, selectMailbox+`
			WHERE is_primary = true
			AND provider = $1
			AND refresh_token <> ''
			AND auth_status = $2
			ORDER BY updated_at DESC
			LIMIT $3
		`, emailProviderOutlook, authStatusAuthorized, n)
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

func (s *MailboxStore) RecordInboxMessages(ctx context.Context, email string, messages []graphMessage) ([]graphMessage, error) {
	email = normalizeEmail(email)
	if email == "" {
		return nil, errors.New("email_address is required")
	}
	if len(messages) == 0 {
		return []graphMessage{}, nil
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := time.Now().Unix()
	unseen := make([]graphMessage, 0, len(messages))
	var maxReceivedAtNs int64
	for _, msg := range messages {
		receivedAtNs := parseGraphTimeUnixNano(msg.ReceivedDateTime)
		if receivedAtNs > maxReceivedAtNs {
			maxReceivedAtNs = receivedAtNs
		}
		inboxMsg := inboxMessage(email, msg)
		key := stableMessageKey(emailProviderOutlook, email, messageKey(msg))
		if err := insertInboxMessage(ctx, tx, inboxPersistMessage{
			key:            key,
			id:             inboxMsg.GetId(),
			mailboxEmail:   email,
			subject:        inboxMsg.GetSubject(),
			fromAddress:    inboxMsg.GetFromAddress(),
			bodyPreview:    inboxMsg.GetBodyPreview(),
			receivedAtUnix: inboxMsg.GetReceivedAtUnix(),
			recipients:     inboxMsg.GetRecipients(),
			provider:       emailProviderOutlook,
			sourceEmail:    email,
			bodyText:       inboxMsg.GetBodyText(),
			htmlBody:       inboxMsg.GetHtmlBody(),
			rawSize:        inboxMsg.GetRawSize(),
		}, now); err != nil {
			return nil, err
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO mailbox_inbox_seen (provider, mailbox_email, message_key, seen_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (provider, mailbox_email, message_key) DO NOTHING
		`, emailProviderOutlook, email, key, now)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() > 0 {
			unseen = append(unseen, msg)
		}
	}
	if maxReceivedAtNs > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE mailboxes
			SET last_inbox_received_at_ns = GREATEST(last_inbox_received_at_ns, $1),
				updated_at = $2
			WHERE email = $3
		`, maxReceivedAtNs, now, email); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return unseen, nil
}

func (s *MailboxStore) RecordInboundEmail(ctx context.Context, event *pb.InboundEmailWebhook) error {
	if event == nil {
		return errors.New("email event is required")
	}
	provider := normalizeEmailProvider(event.GetProvider())
	if provider == "" {
		return errors.New("email event provider is required")
	}
	recipients := uniqueStrings(event.GetRecipients())
	if len(recipients) == 0 {
		return errors.New("email event recipients are required")
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
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := time.Now().Unix()
	for _, recipient := range recipients {
		key := stableMessageKey(provider, recipient, firstNonEmpty(event.GetEventId(), event.GetMessageId(), event.GetSubject()))
		messageID := firstNonEmpty(event.GetMessageId(), event.GetEventId(), key)
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
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO mailbox_inbox_seen (provider, mailbox_email, message_key, seen_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (provider, mailbox_email, message_key) DO NOTHING
		`, provider, recipient, key, now); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
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

func (s *MailboxStore) ListInboxMessages(ctx context.Context, email string, limit int32) ([]*pb.EmailInboxMessage, error) {
	email = normalizeEmail(email)
	if email == "" {
		return nil, errors.New("email_address is required")
	}
	n := messageLimitValue(limit, defaultMessageLimit)
	rows, err := s.pool.Query(ctx, `
		SELECT message_id, mailbox_email, subject, from_address, body_preview,
			received_at, recipients_json, provider, source_mailbox_email, body_text,
			html_body, raw_size
		FROM mailbox_inbox_messages
		WHERE mailbox_email = $1
		ORDER BY received_at DESC, updated_at DESC, message_key DESC
		LIMIT $2
	`, email, n)
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
		out = append(out, &pb.EmailInboxMessage{
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
		})
	}
	return out, rows.Err()
}

func (s *MailboxStore) LatestMessage(ctx context.Context, email string, subjectKeyword string, issuedAfterUnix int64) (*pb.EmailInboxMessage, bool, error) {
	for _, candidate := range uniqueStrings([]string{email, canonicalEmail(email)}) {
		msg, ok, err := s.latestMessageForMailbox(ctx, candidate, subjectKeyword, issuedAfterUnix)
		if err != nil || ok {
			return msg, ok, err
		}
	}
	return nil, false, nil
}

func (s *MailboxStore) latestMessageForMailbox(ctx context.Context, email string, subjectKeyword string, issuedAfterUnix int64) (*pb.EmailInboxMessage, bool, error) {
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
	query += " ORDER BY received_at DESC, updated_at DESC, message_key DESC LIMIT 1"

	var row inboxMessageRow
	err := s.pool.QueryRow(ctx, query, args...).Scan(
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
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	msg, err := row.toProto()
	if err != nil {
		return nil, false, err
	}
	return msg, true, nil
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

	row, err := scanMailbox(tx.QueryRow(ctx, selectMailbox+" WHERE email = $1 FOR UPDATE", email))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("mailbox not found: %s", redactEmail(email))
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(ctx, "UPDATE mailboxes SET auth_status = $1, last_error = $2, updated_at = $3 WHERE email = $4", authStatus, strings.TrimSpace(lastError), now, email); err != nil {
		return nil, err
	}
	if row.IsPrimary {
		if _, err := tx.Exec(ctx, "UPDATE mailboxes SET auth_status = $1, last_error = $2, updated_at = $3 WHERE primary_email = $4 AND is_primary = false", authStatus, strings.TrimSpace(lastError), now, row.Email); err != nil {
			return nil, err
		}
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

	row, err := scanMailbox(tx.QueryRow(ctx, selectMailbox+" WHERE email = $1 FOR UPDATE", email))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	deleteEmails := []string{row.Email}
	if row.IsPrimary {
		rows, err := tx.Query(ctx, "SELECT email FROM mailboxes WHERE primary_email = $1 AND email <> $1 FOR UPDATE", row.Email)
		if err != nil {
			return false, err
		}
		defer rows.Close()
		for rows.Next() {
			var alias string
			if err := rows.Scan(&alias); err != nil {
				return false, err
			}
			if normalized := normalizeEmail(alias); normalized != "" {
				deleteEmails = append(deleteEmails, normalized)
			}
		}
		if err := rows.Err(); err != nil {
			return false, err
		}
	}

	args := make([]any, 0, len(deleteEmails))
	placeholders := make([]string, 0, len(deleteEmails))
	for _, item := range deleteEmails {
		args = append(args, item)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	inClause := strings.Join(placeholders, ",")
	if _, err := tx.Exec(ctx, "DELETE FROM mailbox_inbox_messages WHERE mailbox_email IN ("+inClause+")", args...); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM mailbox_inbox_seen WHERE mailbox_email IN ("+inClause+")", args...); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, "DELETE FROM mailboxes WHERE email IN ("+inClause+")", args...)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *MailboxStore) FindMailbox(ctx context.Context, email string) (*pb.EmailMailbox, error) {
	row, err := scanMailbox(s.pool.QueryRow(ctx, selectMailbox+" WHERE email = $1", normalizeEmail(email)))
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
	row, err := scanMailbox(s.pool.QueryRow(ctx, selectMailbox+" WHERE email = $1", email))
	if errors.Is(err, pgx.ErrNoRows) {
		canonical := canonicalEmail(email)
		if canonical != "" && canonical != email {
			row, err = scanMailbox(s.pool.QueryRow(ctx, selectMailbox+" WHERE email = $1 AND is_primary = true", canonical))
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("mailbox not found: %s", redactEmail(email))
	}
	if err != nil {
		return nil, err
	}

	primaryEmail := row.Email
	if !row.IsPrimary {
		primaryEmail = row.PrimaryEmail
		if primaryEmail == "" {
			primaryEmail = canonicalEmail(row.Email)
		}
	}
	primary, err := scanMailbox(s.pool.QueryRow(ctx, selectMailbox+" WHERE email = $1 AND is_primary = true", primaryEmail))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("primary mailbox not found for %s", redactEmail(email))
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(primary.RefreshToken) == "" {
		return nil, fmt.Errorf("primary mailbox has no refresh token: %s", redactEmail(primary.Email))
	}
	if primary.AuthStatus != authStatusAuthorized {
		return nil, fmt.Errorf("primary mailbox is not authorized: %s auth_status=%s", redactEmail(primary.Email), primary.AuthStatus)
	}
	return primary.toProto(), nil
}

func (s *MailboxStore) UpdateMailboxTokens(ctx context.Context, email string, refreshToken string, accessToken string) error {
	email = normalizeEmail(email)
	_, err := s.pool.Exec(ctx, "UPDATE mailboxes SET refresh_token = $1, access_token = $2, auth_status = $3, last_error = '', updated_at = $4 WHERE email = $5 OR primary_email = $5", strings.TrimSpace(refreshToken), strings.TrimSpace(accessToken), authStatusAuthorized, time.Now().Unix(), email)
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
		&row.IsPrimary,
		&row.PrimaryEmail,
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
	return &pb.EmailMailbox{
		EmailAddress: m.Email,
		Provider:     normalizeEmailProvider(m.Provider),
		Password:     m.Password,
		RefreshToken: m.RefreshToken,
		AccessToken:  m.AccessToken,
		AuthStatus:   m.AuthStatus,
		LastError:    m.LastError,
		IsPrimary:    m.IsPrimary,
		PrimaryEmail: m.PrimaryEmail,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
	}
}

func normalizeEmailProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "cf", "cloudflare", "cloudflare-email-relay":
		return emailProviderCloudflare
	case "outlook", "microsoft", "graph":
		return emailProviderOutlook
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
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
