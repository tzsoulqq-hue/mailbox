import { normalizeUiEmail } from './email-utils';
import type { EmailSignal, InboxMessage, InboxResponse, LatestOtp } from './types';

export function latestOtpForEmail(response: InboxResponse | null, _mailboxes: unknown[], email: string): LatestOtp | null {
  const target = normalizeUiEmail(email);
  if (!target) return null;
  const candidates: LatestOtp[] = [];
  const result = inboxResultForMailbox(response, email);
  for (const message of result?.messages || []) {
    const matchesTarget = normalizeUiEmail(message.mailbox_email) === target ||
      (message.recipients || []).some((recipient) => normalizeUiEmail(recipient) === target);
    const code = verificationCodeForMessage(message);
    if (matchesTarget && code) candidates.push({ otp: code, subject: message.subject, received_at_unix: message.received_at_unix });
  }
  candidates.sort((a, b) => b.received_at_unix - a.received_at_unix);
  return candidates[0] || null;
}

export function inboxResultForMailbox(response: InboxResponse | null, email: string) {
  const target = normalizeUiEmail(email);
  if (!response || !target) return undefined;
  return (response.results || []).find((result) => {
    if (normalizeUiEmail(result.mailbox?.email_address || '') === target) return true;
    return (result.messages || []).some((message) => (
      normalizeUiEmail(message.mailbox_email) === target ||
      (message.recipients || []).some((recipient) => normalizeUiEmail(recipient) === target)
    ));
  });
}

export function verificationCodeForMessage(message: InboxMessage): string {
  const primary = signalCode(message.primary_signal, 'otp');
  if (primary) return primary;
  for (const signal of message.signals || []) {
    const code = signalCode(signal, 'otp');
    if (code) return code;
  }
  return message.otp || '';
}

export function messageSignals(message: InboxMessage): EmailSignal[] {
  const signals = [...(message.signals || [])];
  if (message.primary_signal && !signals.some((signal) => signal === message.primary_signal)) signals.unshift(message.primary_signal);
  const seen = new Set<string>();
  return signals.filter((signal) => {
    const key = `${signalKindName(signal.kind)}:${signal.code || ''}:${signal.label || ''}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return signalKindName(signal.kind) !== 'unknown';
  });
}

export function signalKindName(kind: EmailSignal['kind']): 'otp' | 'unknown' {
  if (typeof kind === 'number') {
    if (kind === 1) return 'otp';
    return 'unknown';
  }
  const value = String(kind || '').toLowerCase();
  if (value.includes('otp')) return 'otp';
  return 'unknown';
}

export function signalLabel(signal: EmailSignal): string {
  const label = String(signal.label || '').trim();
  if (label) return label;
  if (signalKindName(signal.kind) === 'otp') return '验证码';
  return '-';
}

function signalCode(signal: EmailSignal | undefined, expectedKind: 'otp') {
  if (!signal || signalKindName(signal.kind) !== expectedKind) return '';
  return String(signal.code || '').trim();
}
