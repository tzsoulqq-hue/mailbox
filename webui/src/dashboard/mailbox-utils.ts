import { objectValue, stringValue } from '@/dashboard/module-kit';
import { stepDetailData } from '@/dashboard/modules/workflow/sdk';
import { normalizeUiEmail } from './email-utils';
import { mailboxProviderConfig, mailboxProviderConfigs, mailboxProviderValue, type MailboxProviderTab } from './mailbox-provider-config';
import type { Job, Mailbox, MailboxProviderActionCapability, MailboxProviderCapability } from './types';

export { mailboxProviderConfig, mailboxProviderConfigs, mailboxProviderValue, type MailboxProviderTab };
export type MailboxActionKey = 'import_mailbox' | 'run_oauth' | 'fetch_inbox' | 'receive_webhook' | 'auto_create_mailbox' | 'sync_domains';

export type MailboxBatchItem = {
  email: string;
  password: string;
  refresh_token?: string;
  access_token?: string;
  home_country?: string;
  home_ip?: string;
  proxy_profile?: string;
};

export const mailboxActions = {
  importMailbox: 'import_mailbox',
  runOAuth: 'run_oauth',
  fetchInbox: 'fetch_inbox',
  receiveWebhook: 'receive_webhook',
  autoCreateMailbox: 'auto_create_mailbox',
  syncDomains: 'sync_domains'
} as const;

export function domainForEmail(email: string) {
  const [, domain = ''] = normalizeUiEmail(email).split('@');
  return domain;
}

export function uniqueStrings(values: string[]) {
  return Array.from(new Set(values.map((value) => value.trim()).filter(Boolean))).sort();
}

export function tokenText(mailbox: Mailbox) {
  const configured = mailboxProviderConfig(mailbox.provider).tokenText;
  if (typeof configured === 'function') return configured(mailbox);
  if (configured) return configured;
  if (mailbox.refresh_token && authStatus(mailbox) === 'AUTHORIZED') return 'Refresh 可用';
  if (mailbox.refresh_token) return 'Refresh 待验证';
  if (mailbox.access_token) return '仅 Access';
  return '缺 Token';
}

export function mailboxProviderText(provider: string | number) {
  return mailboxProviderConfig(provider).label;
}

export function authStatus(mailbox: Mailbox) {
  const value = String(mailbox.auth_status || '').trim();
  if (value) return value;
  if (mailbox.refresh_token) return 'AUTHORIZED';
  return 'OAUTH_PENDING';
}

export function mailboxWorkflowEmail(job: Job) {
  if (job.action !== 'MAILBOX_OAUTH') return '';
  const candidates = [objectValue(job.result)];
  for (const step of job.steps || []) {
    const detail = stepDetailData(step);
    if (detail) candidates.push(detail);
  }
  for (const data of candidates) {
    const email = normalizeUiEmail(stringValue(data.email_address));
    if (email) return email;
  }
  return '';
}

export function parseMailboxBatch(value: string, provider: string) {
  const items: MailboxBatchItem[] = [];
  const errors: string[] = [];
  const importConfig = mailboxProviderConfig(provider).import;
  if (!importConfig) {
    return { items, errors: ['当前 provider 不支持导入'] };
  }
  const allowPlainEmailBatch = importConfig.allowPlainEmailBatch;

  value.split(/\r?\n/).forEach((raw, index) => {
    const line = raw.trim();
    if (!line) return;
    const tokenParts = line.split('---');
    if (tokenParts.length >= 3) {
      const email = tokenParts[0].trim();
      if (!email) {
        errors.push(`第 ${index + 1} 行缺少账号`);
        return;
      }
      items.push({
        email,
        password: tokenParts[1].trim(),
        refresh_token: tokenParts[2].trim(),
        access_token: tokenParts[3]?.trim() || '',
        home_country: tokenParts[4]?.trim() || '',
        home_ip: tokenParts[5]?.trim() || '',
        proxy_profile: tokenParts[6]?.trim() || '',
      });
      return;
    }
    const delimiterIndex = line.indexOf('----');
    if (allowPlainEmailBatch && delimiterIndex < 0) {
      items.push({ email: line, password: '' });
      return;
    }
    if (delimiterIndex < 0) {
      errors.push(`第 ${index + 1} 行缺少 ----`);
      return;
    }
    const email = line.slice(0, delimiterIndex).trim();
    const password = line.slice(delimiterIndex + 4).trim();
    if (!email) {
      errors.push(`第 ${index + 1} 行缺少账号`);
      return;
    }
    items.push({ email, password });
  });

  return { items, errors };
}

export function capabilityForProvider(capabilities: MailboxProviderCapability[], provider: string) {
  const target = mailboxProviderValue(provider);
  return capabilities.find((capability) => (
    mailboxProviderValue(capability.key || String(capability.provider || '')) === target ||
    providerNumberKey(capability.provider) === target
  ));
}

export function providerAction(capability: MailboxProviderCapability | undefined, action: MailboxActionKey) {
  return (capability?.actions || []).find((item) => actionKey(item.action) === action);
}

export function canRunMailboxAction(mailbox: Mailbox, action: MailboxProviderActionCapability | undefined) {
  if (!action) return false;
  if (!requiredFieldsPresent(mailbox, action.required_mailbox_fields || [])) return false;
  const statuses = action.required_auth_statuses || [];
  return statuses.length === 0 || statuses.includes(authStatus(mailbox));
}

export function bulkMailboxActionCount(mailboxes: Mailbox[], action: MailboxProviderActionCapability | undefined) {
  if (!action?.bulk_supported) return 0;
  return mailboxes.filter((mailbox) => canRunMailboxAction(mailbox, action)).length;
}

export function canRunProviderMailboxAction(capabilities: MailboxProviderCapability[], mailbox: Mailbox, action: MailboxActionKey) {
  return canRunMailboxAction(mailbox, providerAction(capabilityForProvider(capabilities, mailbox.provider), action));
}

export function actionKey(action: string | number): MailboxActionKey | '' {
  if (typeof action === 'number') {
    return {
      1: mailboxActions.importMailbox,
      2: mailboxActions.runOAuth,
      3: mailboxActions.fetchInbox,
      4: mailboxActions.receiveWebhook,
      5: mailboxActions.autoCreateMailbox,
      6: mailboxActions.syncDomains
    }[action] || '';
  }
  const value = String(action || '').toLowerCase();
  if (value.includes('import_mailbox')) return mailboxActions.importMailbox;
  if (value.includes('run_oauth')) return mailboxActions.runOAuth;
  if (value.includes('fetch_inbox')) return mailboxActions.fetchInbox;
  if (value.includes('receive_webhook')) return mailboxActions.receiveWebhook;
  if (value.includes('auto_create_mailbox')) return mailboxActions.autoCreateMailbox;
  if (value.includes('sync_domains')) return mailboxActions.syncDomains;
  return '';
}

function requiredFieldsPresent(mailbox: Mailbox, fields: string[]) {
  const values: Record<string, string> = {
    password: mailbox.password,
    refresh_token: mailbox.refresh_token,
    access_token: mailbox.access_token
  };
  return fields.every((field) => !!String(values[field] || '').trim());
}

function providerNumberKey(provider: string | number) {
  const numeric = Number(provider);
  return mailboxProviderConfigs.find((item) => item.enumNumber === numeric)?.value || '';
}
