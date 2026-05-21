export { MailboxInboxSection } from './mailbox-inbox';
export { MailboxOtpPanel } from './otp-panel';
export { canonicalUiEmail, formatEmailList, maskEmail, normalizeUiEmail } from './email-utils';
export { mergeInboxMessage, useMailboxEmailEventCache } from './mailbox-events';
export {
  inboxResultForMailbox,
  latestOtpForEmail,
  messageSignals,
  signalKindName,
  signalLabel,
  verificationCodeForMessage
} from './mailbox-signal-utils';
export type { MailboxEmailEvent, MailboxEmailEventCacheOptions } from './mailbox-events';
export type {
  EmailSignal,
  InboxMessage,
  InboxResponse,
  InboxResult,
  LatestOtp,
  Mailbox,
  MailboxDomain,
  MailboxProviderActionCapability,
  MailboxProviderCapability
} from './types';
