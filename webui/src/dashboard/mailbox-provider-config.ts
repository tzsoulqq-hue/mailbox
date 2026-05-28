import type { Mailbox } from './types';

export type MailboxProviderTab = 'outlook' | 'cloudflare';
export type MailboxCredentialField = 'password' | 'refresh_token' | 'access_token';

export type MailboxProviderUIConfig = {
  value: MailboxProviderTab;
  label: string;
  enumNumber: number;
  showStatus: boolean;
  tokenText?: string | ((mailbox: Mailbox) => string);
  import?: {
    description: string;
    batchPlaceholder: string;
    allowPlainEmailBatch: boolean;
    credentialFields: MailboxCredentialField[];
  };
};

export const mailboxProviderConfigs = [{
  value: 'outlook',
  label: 'Outlook',
  enumNumber: 1,
  showStatus: true,
  import: {
    description: 'Outlook 可附带密码或 OAuth token。',
    batchPlaceholder: 'account@example.com----password\naccount@example.com---password---refresh_token---access_token',
    allowPlainEmailBatch: false,
    credentialFields: ['password', 'refresh_token', 'access_token'],
  },
}, {
  value: 'cloudflare',
  label: 'Cloudflare',
  enumNumber: 2,
  showStatus: false,
  tokenText: 'Webhook',
}] satisfies MailboxProviderUIConfig[];

export function mailboxProviderConfig(provider: string | number): MailboxProviderUIConfig {
  const value = mailboxProviderValue(provider);
  return mailboxProviderConfigs.find((item) => item.value === value) || mailboxProviderConfigs[0];
}

export function mailboxProviderValue(provider: string | number): MailboxProviderTab {
  const normalized = String(provider || '').trim().toLowerCase();
  const matched = mailboxProviderConfigs.find((item) => (
    normalized === item.value ||
    normalized.includes(item.value) ||
    String(item.label).toLowerCase() === normalized ||
    Number(provider) === item.enumNumber
  ));
  if (matched) return matched.value;
  if (normalized === 'cf') return 'cloudflare';
  if (normalized === 'microsoft' || normalized === 'graph') return 'outlook';
  return 'outlook';
}
