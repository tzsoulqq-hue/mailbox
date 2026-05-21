package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"mailboxapi/pb"
)

func outlookMailboxProvider() *mailboxProviderDefinition {
	return &mailboxProviderDefinition{
		key:         emailProviderOutlook,
		aliases:     []string{"microsoft", "graph"},
		provider:    pb.MailboxProvider_MAILBOX_PROVIDER_OUTLOOK,
		displayName: "Outlook",
		schemaStatements: func() []string {
			return []string{
				`CREATE TABLE IF NOT EXISTS mailbox_outlook_accounts (
					mailbox_email TEXT PRIMARY KEY REFERENCES mailboxes(email) ON DELETE CASCADE,
					password TEXT NOT NULL DEFAULT '',
					refresh_token TEXT NOT NULL DEFAULT '',
					access_token TEXT NOT NULL DEFAULT '',
					auth_status TEXT NOT NULL DEFAULT 'OAUTH_PENDING',
					last_error TEXT NOT NULL DEFAULT '',
					created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
					updated_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT
				)`,
				`CREATE INDEX IF NOT EXISTS idx_mailbox_outlook_accounts_auth_status ON mailbox_outlook_accounts(auth_status)`,
				`CREATE INDEX IF NOT EXISTS idx_mailbox_outlook_accounts_refresh_token ON mailbox_outlook_accounts(refresh_token)`,
			}
		},
		selectJoin: "LEFT JOIN mailbox_outlook_accounts outlook ON outlook.mailbox_email = m.email",
		selectFields: mailboxProviderSelectFields{
			password:     "CASE WHEN m.provider = 'outlook' THEN outlook.password ELSE '' END",
			refreshToken: "CASE WHEN m.provider = 'outlook' THEN outlook.refresh_token ELSE '' END",
			accessToken:  "CASE WHEN m.provider = 'outlook' THEN outlook.access_token ELSE '' END",
			authStatus:   "CASE WHEN m.provider = 'outlook' THEN outlook.auth_status ELSE '' END",
			lastError:    "CASE WHEN m.provider = 'outlook' THEN outlook.last_error ELSE '' END",
		},
		capabilities: outlookProviderCapabilities,
		upsert:       upsertOutlookMailboxData,
		authFilter: func(authStatus string, args *[]any) string {
			*args = append(*args, strings.TrimSpace(authStatus))
			return fmt.Sprintf("outlook.auth_status = $%d", len(*args))
		},
		validatePoll: validateOutlookPollableMailbox,
		updateAuth:   updateOutlookAuthStatus,
		updateTokens: updateOutlookTokens,
		prepareLegacyData: func() []string {
			return []string{
				`DO $$
				BEGIN
					IF EXISTS (
						SELECT 1 FROM information_schema.columns
						WHERE table_name = 'mailboxes' AND column_name = 'password'
					) THEN
						EXECUTE $sql$
							INSERT INTO mailbox_outlook_accounts (
								mailbox_email, password, refresh_token, access_token,
								auth_status, last_error, created_at, updated_at
							)
							SELECT email, password, refresh_token, access_token,
								CASE
									WHEN auth_status <> '' THEN auth_status
									WHEN refresh_token <> '' THEN 'AUTHORIZED'
									ELSE 'OAUTH_PENDING'
								END,
								last_error, created_at, updated_at
							FROM mailboxes
							WHERE LOWER(COALESCE(provider, 'outlook')) IN ('', 'outlook', 'microsoft', 'graph')
							ON CONFLICT (mailbox_email) DO UPDATE SET
								password = CASE WHEN EXCLUDED.password <> '' THEN EXCLUDED.password ELSE mailbox_outlook_accounts.password END,
								refresh_token = CASE WHEN EXCLUDED.refresh_token <> '' THEN EXCLUDED.refresh_token ELSE mailbox_outlook_accounts.refresh_token END,
								access_token = CASE WHEN EXCLUDED.access_token <> '' THEN EXCLUDED.access_token ELSE mailbox_outlook_accounts.access_token END,
								auth_status = CASE WHEN EXCLUDED.auth_status <> '' THEN EXCLUDED.auth_status ELSE mailbox_outlook_accounts.auth_status END,
								last_error = CASE WHEN EXCLUDED.last_error <> '' THEN EXCLUDED.last_error ELSE mailbox_outlook_accounts.last_error END,
								updated_at = GREATEST(mailbox_outlook_accounts.updated_at, EXCLUDED.updated_at)
						$sql$;
					END IF;
				END $$`,
				`UPDATE mailbox_outlook_accounts SET auth_status = 'OAUTH_PENDING', last_error = ''
					WHERE auth_status = 'AUTH_FAILED'
					AND last_error = 'registered mailbox has no OAuth refresh token'`,
				`DELETE FROM mailboxes alias
					USING mailboxes base
					WHERE alias.provider = 'outlook'
					  AND base.provider = alias.provider
					  AND split_part(alias.email, '@', 1) LIKE '%+%'
					  AND base.email = regexp_replace(alias.email, '^([^+@]+)\+[^@]*@(.+)$', '\1@\2')
					  AND base.email <> alias.email`,
			}
		},
	}
}

func outlookProviderCapabilities() *pb.MailboxProviderCapabilities {
	return &pb.MailboxProviderCapabilities{
		Provider:    pb.MailboxProvider_MAILBOX_PROVIDER_OUTLOOK,
		Key:         emailProviderOutlook,
		DisplayName: "Outlook",
		Actions: []*pb.MailboxProviderActionCapability{
			{
				Action:        pb.MailboxProviderAction_MAILBOX_PROVIDER_ACTION_IMPORT_MAILBOX,
				BulkSupported: true,
			},
			{
				Action:                pb.MailboxProviderAction_MAILBOX_PROVIDER_ACTION_RUN_OAUTH,
				RequiredMailboxFields: []string{"password"},
				RequiredAuthStatuses:  []string{authStatusOAuthPending, authStatusAuthFailed, authStatusNeedsManualVerify},
				BulkSupported:         true,
			},
			{
				Action:                pb.MailboxProviderAction_MAILBOX_PROVIDER_ACTION_FETCH_INBOX,
				RequiredAuthStatuses:  []string{authStatusAuthorized},
				RequiredMailboxFields: []string{"refresh_token"},
				BulkSupported:         true,
			},
		},
		RetentionPolicy: &pb.MailboxMessageRetentionPolicy{
			Scope:       pb.MailboxMessageRetentionScope_MAILBOX_MESSAGE_RETENTION_SCOPE_MAILBOX,
			MaxMessages: int32(envInt("MAILBOX_OUTLOOK_MAX_MESSAGES_PER_MAILBOX", defaultOutlookMaxMessages)),
		},
	}
}

func upsertOutlookMailboxData(ctx context.Context, tx pgx.Tx, mailbox *pb.EmailMailbox, now int64) error {
	authStatus := strings.TrimSpace(mailbox.GetAuthStatus())
	if authStatus == "" {
		authStatus = authStatusOAuthPending
		if strings.TrimSpace(mailbox.GetRefreshToken()) != "" {
			authStatus = authStatusAuthorized
		}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO mailbox_outlook_accounts (
			mailbox_email, password, refresh_token, access_token,
			auth_status, last_error, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$7)
		ON CONFLICT (mailbox_email) DO UPDATE SET
			password = CASE WHEN EXCLUDED.password <> '' THEN EXCLUDED.password ELSE mailbox_outlook_accounts.password END,
			refresh_token = CASE WHEN EXCLUDED.refresh_token <> '' THEN EXCLUDED.refresh_token ELSE mailbox_outlook_accounts.refresh_token END,
			access_token = CASE WHEN EXCLUDED.access_token <> '' THEN EXCLUDED.access_token ELSE mailbox_outlook_accounts.access_token END,
			auth_status = CASE
				WHEN $8 <> '' THEN EXCLUDED.auth_status
				WHEN EXCLUDED.refresh_token <> '' THEN 'AUTHORIZED'
				ELSE mailbox_outlook_accounts.auth_status
			END,
			last_error = CASE WHEN $8 <> '' OR EXCLUDED.last_error <> '' THEN EXCLUDED.last_error ELSE mailbox_outlook_accounts.last_error END,
			updated_at = EXCLUDED.updated_at
	`, normalizeEmail(mailbox.GetEmailAddress()), strings.TrimSpace(mailbox.GetPassword()),
		strings.TrimSpace(mailbox.GetRefreshToken()), strings.TrimSpace(mailbox.GetAccessToken()),
		authStatus, strings.TrimSpace(mailbox.GetLastError()), now, strings.TrimSpace(mailbox.GetAuthStatus()))
	return err
}

func validateOutlookPollableMailbox(row *mailboxRow) error {
	if strings.TrimSpace(row.RefreshToken) == "" {
		return fmt.Errorf("mailbox has no refresh token: %s", redactEmail(row.Email))
	}
	if row.AuthStatus != authStatusAuthorized {
		return fmt.Errorf("mailbox is not authorized: %s auth_status=%s", redactEmail(row.Email), row.AuthStatus)
	}
	return nil
}

func updateOutlookAuthStatus(ctx context.Context, tx pgx.Tx, email string, authStatus string, lastError string, now int64) error {
	_, err := tx.Exec(ctx, `
		UPDATE mailbox_outlook_accounts
		SET auth_status = $1, last_error = $2, updated_at = $3
		WHERE mailbox_email = $4
	`, strings.TrimSpace(authStatus), strings.TrimSpace(lastError), now, normalizeEmail(email))
	return err
}

func updateOutlookTokens(ctx context.Context, pool *pgxpool.Pool, email string, refreshToken string, accessToken string) error {
	_, err := pool.Exec(ctx, `
		UPDATE mailbox_outlook_accounts
		SET refresh_token = $1, access_token = $2, auth_status = $3, last_error = '', updated_at = $4
		WHERE mailbox_email = $5
	`, strings.TrimSpace(refreshToken), strings.TrimSpace(accessToken), authStatusAuthorized, time.Now().Unix(), normalizeEmail(email))
	return err
}
