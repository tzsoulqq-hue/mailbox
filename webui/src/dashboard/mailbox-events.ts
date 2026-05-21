import { useEventQueryCache, type QueryKey } from '@/dashboard/module-kit';
import { normalizeUiEmail } from './email-utils';
import type { InboxMessage, InboxResult, Mailbox } from './types';

export type MailboxEmailEvent = {
  email_address: string;
  message?: InboxMessage;
};

export type MailboxEmailEventCacheOptions = {
  enabled?: boolean;
  email?: string;
  signalKind?: 'otp' | 'any';
  issuedAfterUnix?: number;
  inboxQueryKey?: QueryKey;
  onMessage?: (message: InboxMessage, event: MailboxEmailEvent) => void;
};

export function useMailboxEmailEventCache(options: MailboxEmailEventCacheOptions) {
  const email = normalizeUiEmail(options.email || '');
  const enabled = options.enabled !== false && !!email;
  const url = mailboxEventURL(email, options.signalKind, options.issuedAfterUnix);
  useEventQueryCache<MailboxEmailEvent>({
    enabled,
    url,
    eventName: 'email',
    targets: options.inboxQueryKey ? [{
      queryKey: options.inboxQueryKey,
      update: (prev, event) => event.message ? mergeInboxMessage(prev as InboxResult | null | undefined, event.email_address || email, event.message) : prev
    }] : [],
    onEvent: (event) => {
      if (event.message) options.onMessage?.(event.message, event);
    }
  });
}

export function mergeInboxMessage(result: InboxResult | null | undefined, email: string, message: InboxMessage): InboxResult {
  const target = normalizeUiEmail(email || message.mailbox_email);
  const current: InboxResult = result ? { ...result, messages: [...(result.messages || [])] } : {
    mailbox: { email_address: target } as Mailbox,
    messages: [],
    error_message: ''
  };
  if (!resultMatchesEmail(current, target)) current.mailbox = { email_address: target } as Mailbox;
  current.messages = mergeMessages(current.messages || [], message);
  return current;
}

function mergeMessages(messages: InboxMessage[], message: InboxMessage) {
  const key = messageKey(message);
  const next = messages.filter((item) => messageKey(item) !== key);
  next.unshift(message);
  return next.sort((a, b) => (b.received_at_unix || 0) - (a.received_at_unix || 0)).slice(0, 20);
}

function resultMatchesEmail(result: InboxResult, email: string) {
  if (normalizeUiEmail(result.mailbox?.email_address || '') === email) return true;
  return (result.messages || []).some((message) => messageMatchesEmail(message, email));
}

function messageMatchesEmail(message: InboxMessage, email: string) {
  if (normalizeUiEmail(message.mailbox_email) === email) return true;
  return (message.recipients || []).some((recipient) => normalizeUiEmail(recipient) === email);
}

function messageKey(message: InboxMessage) {
  return [message.provider || '', message.mailbox_email || '', message.id || `${message.received_at_unix}:${message.subject}`].join(':');
}

function mailboxEventURL(email: string, signalKind = 'otp', issuedAfterUnix = 0) {
  const params = new URLSearchParams({
    email_address: email,
    signal_kind: signalKind,
  });
  void issuedAfterUnix;
  return `/api/mailboxes/events?${params.toString()}`;
}
