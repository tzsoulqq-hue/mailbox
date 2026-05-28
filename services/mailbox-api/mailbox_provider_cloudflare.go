package main

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"mailboxapi/pb"
)

func cloudflareMailboxProvider() *mailboxProviderDefinition {
	return &mailboxProviderDefinition{
		key:             emailProviderCloudflare,
		aliases:         []string{"cf", "cloudflare-email-relay"},
		provider:        pb.MailboxProvider_MAILBOX_PROVIDER_CLOUDFLARE,
		displayName:     "Cloudflare",
		storedInboxOnly: true,
		capabilities: func() *pb.MailboxProviderCapabilities {
			return &pb.MailboxProviderCapabilities{
				Provider:    pb.MailboxProvider_MAILBOX_PROVIDER_CLOUDFLARE,
				Key:         emailProviderCloudflare,
				DisplayName: "Cloudflare",
				Actions: []*pb.MailboxProviderActionCapability{
					{Action: pb.MailboxProviderAction_MAILBOX_PROVIDER_ACTION_RECEIVE_WEBHOOK},
					{Action: pb.MailboxProviderAction_MAILBOX_PROVIDER_ACTION_AUTO_CREATE_MAILBOX},
					{Action: pb.MailboxProviderAction_MAILBOX_PROVIDER_ACTION_SYNC_DOMAINS},
				},
				RetentionPolicy: &pb.MailboxMessageRetentionPolicy{
					Scope:       pb.MailboxMessageRetentionScope_MAILBOX_MESSAGE_RETENTION_SCOPE_DOMAIN,
					MaxMessages: int32(envInt("MAILBOX_CLOUDFLARE_MAX_MESSAGES_PER_DOMAIN", defaultCloudflareMaxDomain)),
				},
			}
		},
		loadDomains: loadCloudflareEmailDomains,
		domains: func(configured []string) []*pb.MailboxDomain {
			domains := make([]*pb.MailboxDomain, 0, len(configured))
			for _, domain := range configured {
				domains = append(domains, &pb.MailboxDomain{
					Provider: pb.MailboxProvider_MAILBOX_PROVIDER_CLOUDFLARE,
					Domain:   domain,
					Enabled:  true,
				})
			}
			return domains
		},
		matchesAddress: func(email string, cfg mailboxProviderRuntimeConfig) bool {
			domain := domainForEmail(email)
			if domain == "" {
				return false
			}
			for _, candidate := range cfg.domainsForProvider(emailProviderCloudflare) {
				if domain == strings.Trim(strings.ToLower(strings.TrimSpace(candidate)), ".") {
					return true
				}
			}
			return false
		},
		pruneInbound: func(ctx context.Context, tx pgx.Tx, retention mailboxInboxRetention) error {
			for domain := range retention.touchedDomains {
				if err := pruneDomainMessages(ctx, tx, emailProviderCloudflare, domain, envInt("MAILBOX_CLOUDFLARE_MAX_MESSAGES_PER_DOMAIN", defaultCloudflareMaxDomain)); err != nil {
					return err
				}
			}
			return nil
		},
		includeVirtual: func(authStatus string) bool {
			return authStatus == ""
		},
		virtualMailboxes: listCloudflareVirtualMailboxes,
		prepareProjection: func(mailbox *pb.EmailMailbox) {
			mailbox.AuthStatus = ""
			mailbox.Password = ""
			mailbox.RefreshToken = ""
			mailbox.AccessToken = ""
			mailbox.LastError = ""
		},
	}
}

func listCloudflareVirtualMailboxes(ctx context.Context, pool *pgxpool.Pool, limit int) ([]*pb.EmailMailbox, error) {
	rows, err := pool.Query(ctx, `
		SELECT 'cloudflare:' || msg.mailbox_email, msg.mailbox_email,
			$1, '', '', '', '', '', '', '', '', '', '', '', FALSE,
			MIN(msg.created_at), MAX(msg.updated_at)
		FROM mailbox_inbox_messages msg
		WHERE msg.provider = $1
		  AND NOT EXISTS (SELECT 1 FROM mailboxes m WHERE m.email = msg.mailbox_email)
		GROUP BY msg.mailbox_email
		ORDER BY MAX(msg.updated_at) DESC
		LIMIT $2
	`, emailProviderCloudflare, limit)
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
